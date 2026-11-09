package driver

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// ImageBlobSource opens runner-private scratch blobs by digest. The driver trusts none of their bytes: it
// re-hashes every byte it writes into the load archive.
type ImageBlobSource interface {
	Open(digest string) (io.ReadCloser, error)
	// Verified reports whether the blob is held in exactly this role: the upload-time verification record equals
	// the plan's kind, media type, diff_id and uncompressed size for it. A blob verified for another role is not
	// the blob this load needs.
	Verified(expected appplatform.ImageBlob) bool
}

// Typed outcomes of a rejected load. Docker cannot undo a load, so "rejected" means the image was not accepted.
const (
	LoadOutcomeArchiveMismatch = "archive_mismatch"
	LoadOutcomeRetagDetected   = "retag_detected"
	LoadOutcomeInspectMismatch = "inspect_mismatch"
	LoadOutcomeLoadRejected    = "load_rejected"
	// LoadOutcomeBlobRefused: an upload for a blob this image needs failed verification for good, so the load can
	// never complete. It is set by the journal, with no Docker call.
	LoadOutcomeBlobRefused = "blob_refused"
)

const (
	LoadStateLoaded   = "loaded"
	LoadStatePresent  = "present"
	LoadStateRejected = "rejected"
)

var (
	ErrImageBlobUnavailable = errors.New("image blob is unavailable in scratch")
	errArchiveMismatch      = errors.New("scratch blob differs from its digest")
)

// LoadImageRequest is trusted infrastructure input from a verified, durably claimed journal attempt.
type LoadImageRequest struct {
	Descriptor    []byte
	ReleaseSHA256 string
	Platform      string
	Source        ImageBlobSource
}

// LoadImageSnapshot is the native observation of one load. It names no tag, path or native handle.
type LoadImageSnapshot struct {
	ReleaseSHA256 string
	Platform      string
	ImageID       string
	Store         ImageStore
	State         string
	Outcome       string
	Removed       bool
	ObservedMS    int64
	// DaemonDetail is the daemon's own HTTP status and error text for a rejected load, bounded. It is for logs
	// and tests only; it never reaches a receipt or the wire.
	DaemonDetail string
}

const maxDaemonDetail = 512

type listedImage struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
}

const maxImageListBytes = 8 << 20
const maxListedImages = 4096

// imageLoadTimeout bounds one load: hashing, decompression-free copy into Docker and its unpack.
const ImageLoadTimeout = 10 * time.Minute

func probeContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, probeTimeout)
}

func readImageList(ctx context.Context, client *http.Client) (map[string]string, error) {
	ctx, cancel := probeContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/json?all=1", nil)
	if err != nil {
		return nil, ErrInvalidResponse
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.Join(ErrUnavailable, ctx.Err())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxImageListBytes+1))
	if err != nil {
		return nil, errors.Join(ErrUnavailable, ctx.Err())
	}
	var listed []listedImage
	if len(raw) > maxImageListBytes || json.Unmarshal(raw, &listed) != nil || len(listed) > maxListedImages {
		return nil, ErrInvalidResponse
	}
	result := make(map[string]string, len(listed))
	for _, image := range listed {
		tags := append([]string(nil), image.RepoTags...)
		sort.Strings(tags)
		encoded, _ := json.Marshal(tags)
		if !nativeImageID.MatchString(image.ID) || len(image.RepoTags) > 1024 {
			return nil, ErrInvalidResponse
		}
		result[image.ID] = string(encoded)
	}
	return result, nil
}

// sameImagesExcept reports whether two image lists agree on every (ID, RepoTags) pair other than the expected ID.
func sameImagesExcept(before, after map[string]string, expected string) bool {
	count := func(m map[string]string) int {
		n := len(m)
		if _, ok := m[expected]; ok {
			n--
		}
		return n
	}
	if count(before) != count(after) {
		return false
	}
	for id, tags := range before {
		if id != expected && after[id] != tags {
			return false
		}
	}
	return true
}

type imageOnDaemon int

const (
	imageAbsent imageOnDaemon = iota
	imageValid
	imageInvalid
)

// inspectLoadedImage reads the loaded image by its expected ID and judges it against the platform: linux, the
// right architecture and no declared volumes.
func inspectLoadedImage(ctx context.Context, client *http.Client, expected, arch string) (imageOnDaemon, error) {
	ctx, cancel := probeContext(ctx)
	defer cancel()
	var response imageResponse
	err := readJSONOrMissing(ctx, client, "/v"+APIVersion+"/images/"+expected+"/json", &response, ErrImageMissing)
	if errors.Is(err, ErrImageMissing) {
		return imageAbsent, nil
	}
	if errors.Is(err, ErrInvalidResponse) {
		return imageInvalid, nil
	}
	if err != nil {
		return imageAbsent, err
	}
	var config imageConfig
	if response.ID != expected || response.OS != "linux" || response.Architecture != arch || (response.Variant != "" && !(arch == "arm64" && response.Variant == "v8")) ||
		!validProjection(response.Config, &imageConfig{}) || json.Unmarshal(response.Config, &config) != nil || len(config.Volumes) != 0 {
		return imageInvalid, nil
	}
	return imageValid, nil
}

func removeLoadedImage(ctx context.Context, client *http.Client, id string) error {
	ctx, cancel := probeContext(ctx)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/"+id+"?force=false&noprune=false", nil)
	if err != nil || !nativeImageID.MatchString(id) {
		return ErrInvalidResponse
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.Join(ErrContainerUncertain, ctx.Err())
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotFound {
		return ErrContainerUncertain
	}
	return nil
}

// ociIndex is the only index the runner ever gives Docker. It names one manifest by digest and carries no
// annotation, name or tag, so the load cannot retag any image.
type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	Manifests     []ociIndexEntry `json:"manifests"`
}
type ociIndexEntry struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

func buildOCIIndex(plan appplatform.ImagePlan) ([]byte, error) {
	return json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociIndexEntry{{MediaType: plan.Manifest.MediaType, Digest: "sha256:" + plan.Manifest.Digest, Size: plan.Manifest.Size}}})
}

// writeLoadArchive writes the OCI layout archive from verified scratch blobs. It hashes the exact bytes it writes
// and aborts before the tar is closed on the first mismatch, so Docker never sees a complete archive containing
// bytes that were not checked as they were written.
func writeLoadArchive(w io.Writer, plan appplatform.ImagePlan, source ImageBlobSource, store ImageStore) error {
	index, err := buildOCIIndex(plan)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(w)
	put := func(name string, body []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := put("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)); err != nil {
		return err
	}
	if err := put("index.json", index); err != nil {
		return err
	}
	if store == ImageStoreClassic {
		// The classic store reads only the legacy layout. The runner writes it itself, from the verified blobs,
		// with no repository tag, so a load can never name an image. The OCI files stay for daemons that read them.
		manifest, err := buildLegacyManifest(plan)
		if err != nil {
			return err
		}
		if err := put("manifest.json", manifest); err != nil {
			return err
		}
	}
	written := map[string]bool{}
	for _, blob := range plan.Blobs() {
		if !source.Verified(blob) {
			return errors.Join(ErrImageBlobUnavailable, errors.New("blob is not verified for its role"))
		}
		if written[blob.Digest] {
			continue
		}
		written[blob.Digest] = true
		file, err := source.Open(blob.Digest)
		if err != nil {
			return errors.Join(ErrImageBlobUnavailable, err)
		}
		err = copyVerifiedBlob(tw, file, blob)
		file.Close()
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

// buildLegacyManifest is the runner-generated manifest.json of the classic store's loader: the config and the
// layers by their exact archive paths, and RepoTags null so no tag is ever created.
func buildLegacyManifest(plan appplatform.ImagePlan) ([]byte, error) {
	layers := make([]string, 0, len(plan.Layers))
	for _, layer := range plan.Layers {
		layers = append(layers, "blobs/sha256/"+layer.Digest)
	}
	return json.Marshal([]struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}{{Config: "blobs/sha256/" + plan.Config.Digest, RepoTags: nil, Layers: layers}})
}

func copyVerifiedBlob(tw *tar.Writer, file io.Reader, blob appplatform.ImageBlob) error {
	if err := tw.WriteHeader(&tar.Header{Name: "blobs/sha256/" + blob.Digest, Mode: 0o644, Size: blob.Size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}); err != nil {
		return err
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tw, hash), io.LimitReader(file, blob.Size))
	if err != nil {
		return errors.Join(ErrImageBlobUnavailable, err)
	}
	var extra [1]byte
	if more, _ := file.Read(extra[:]); n != blob.Size || more != 0 || hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		return errArchiveMismatch
	}
	return nil
}

type loadPost struct {
	archiveErr error
	status     int
	daemonErr  bool
	detail     string
}

// postLoadArchive streams the archive to the daemon. The returned error is uncertain: the daemon may have
// accepted any part of the stream.
func postLoadArchive(ctx context.Context, client *http.Client, plan appplatform.ImagePlan, source ImageBlobSource, store ImageStore) (loadPost, error) {
	var result loadPost
	reader, writer := io.Pipe()
	writeDone := make(chan error, 1)
	go func() {
		err := writeLoadArchive(writer, plan, source, store)
		writer.CloseWithError(err)
		writeDone <- err
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+dockerRequestHost+"/v"+APIVersion+"/images/load?quiet=1", reader)
	if err != nil {
		reader.Close()
		<-writeDone
		return result, ErrInvalidResponse
	}
	request.Header.Set("Content-Type", "application/x-tar")
	response, err := client.Do(request)
	reader.Close()
	result.archiveErr = <-writeDone
	if err != nil {
		if errors.Is(result.archiveErr, errArchiveMismatch) {
			return result, nil
		}
		if errors.Is(result.archiveErr, ErrImageBlobUnavailable) {
			return result, result.archiveErr
		}
		return result, errors.Join(ErrContainerUncertain, err, ctx.Err())
	}
	defer response.Body.Close()
	if errors.Is(result.archiveErr, ErrImageBlobUnavailable) {
		return result, result.archiveErr
	}
	result.status = response.StatusCode
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if readErr != nil {
		return result, errors.Join(ErrContainerUncertain, readErr)
	}
	if response.StatusCode != http.StatusOK {
		result.detail = boundedDetail("status " + strconv.Itoa(response.StatusCode) + ": " + string(raw))
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var message struct {
			Error       string          `json:"error"`
			ErrorDetail json.RawMessage `json:"errorDetail"`
		}
		if len(bytes.TrimSpace(line)) != 0 && json.Unmarshal(line, &message) == nil && (message.Error != "" || len(message.ErrorDetail) != 0) {
			result.daemonErr = true
			if result.detail == "" {
				result.detail = boundedDetail(string(bytes.TrimSpace(line)))
			}
		}
	}
	return result, nil
}

func boundedDetail(text string) string {
	if len(text) > maxDaemonDetail {
		text = text[:maxDaemonDetail]
	}
	return strings.ToValidUTF8(text, "?")
}

// loadImageHTTP loads one verified image and observes the result. The caller holds the runner's load lock and the
// durable unknown claim. An error means the outcome is uncertain: loading is convergent, so the same claim is
// completed by running it again, never under a new identity.
func loadImageHTTP(ctx context.Context, client *http.Client, request LoadImageRequest, arch string) (LoadImageSnapshot, error) {
	var zero LoadImageSnapshot
	parsed, err := appplatform.ParseDescriptor(request.Descriptor)
	if err != nil || parsed.SHA256 != request.ReleaseSHA256 || parsed.Manifest.Backend == nil || request.Source == nil || "linux/"+arch != request.Platform {
		return zero, ErrInvalidResponse
	}
	entry, ok := backendImageEntry(parsed.Manifest.Backend, request.Platform)
	if !ok {
		return zero, ErrUnsupported
	}
	var total int64
	for _, layer := range entry.Layers {
		total += int64(layer.UncompressedBytes)
	}
	if total > appplatform.MaxBackendImageUncompressedBytes {
		return zero, ErrInvalidResponse
	}
	var info infoResponse
	infoCtx, cancel := probeContext(ctx)
	err = readJSON(infoCtx, client, "/v"+APIVersion+"/info", &info)
	cancel()
	if err != nil {
		return zero, err
	}
	store := imageStoreFromInfo(info)
	expected := ExpectedImageID(store, entry)
	snapshot := LoadImageSnapshot{ReleaseSHA256: request.ReleaseSHA256, Platform: request.Platform, ImageID: expected, Store: store}
	before, err := readImageList(ctx, client)
	if err != nil {
		return zero, err
	}
	_, wasPresent := before[expected]
	if wasPresent {
		state, err := inspectLoadedImage(ctx, client, expected, arch)
		if err != nil {
			return zero, err
		}
		snapshot.ObservedMS = time.Now().UnixMilli()
		if state == imageValid {
			snapshot.State = LoadStatePresent
		} else {
			// An image already at the expected ID that fails inspection was not introduced by this load, so it
			// is reported and never removed.
			snapshot.State, snapshot.Outcome = LoadStateRejected, LoadOutcomeInspectMismatch
		}
		return snapshot, nil
	}
	plan, err := readImagePlan(entry, request.Source)
	if errors.Is(err, appplatform.ErrInvalidImage) {
		// The scratch copy no longer matches the descriptor, so nothing was sent to the daemon.
		snapshot.State, snapshot.Outcome, snapshot.ObservedMS = LoadStateRejected, LoadOutcomeArchiveMismatch, time.Now().UnixMilli()
		return snapshot, nil
	}
	if err != nil {
		return zero, err
	}
	post, err := postLoadArchive(ctx, client, plan, request.Source, store)
	if err != nil {
		return zero, err
	}
	after, err := readImageList(ctx, client)
	if err != nil {
		return zero, err
	}
	reject := func(outcome string) (LoadImageSnapshot, error) {
		snapshot.State, snapshot.Outcome, snapshot.ObservedMS = LoadStateRejected, outcome, time.Now().UnixMilli()
		snapshot.DaemonDetail = post.detail
		// Docker cannot undo a load. The image is removed by ID only when this load introduced it.
		if _, present := after[expected]; present && !wasPresent {
			if err := removeLoadedImage(ctx, client, expected); err != nil {
				return zero, err
			}
			snapshot.Removed = true
		}
		return snapshot, nil
	}
	if !sameImagesExcept(before, after, expected) {
		return reject(LoadOutcomeRetagDetected)
	}
	if errors.Is(post.archiveErr, errArchiveMismatch) {
		return reject(LoadOutcomeArchiveMismatch)
	}
	if post.status != http.StatusOK || post.daemonErr {
		return reject(LoadOutcomeLoadRejected)
	}
	state, err := inspectLoadedImage(ctx, client, expected, arch)
	if err != nil {
		return zero, err
	}
	if state != imageValid {
		return reject(LoadOutcomeInspectMismatch)
	}
	snapshot.State, snapshot.ObservedMS = LoadStateLoaded, time.Now().UnixMilli()
	return snapshot, nil
}

// readImagePlan reads the manifest and config blobs from scratch and binds them to the descriptor entry.
func readImagePlan(entry appplatform.BackendImage, source ImageBlobSource) (appplatform.ImagePlan, error) {
	read := func(digest string) ([]byte, error) {
		file, err := source.Open(digest)
		if err != nil {
			return nil, errors.Join(ErrImageBlobUnavailable, err)
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, appplatform.MaxImageMetadataBytes+1))
		if err != nil {
			return nil, errors.Join(ErrImageBlobUnavailable, err)
		}
		return raw, nil
	}
	manifest, err := read(entry.ManifestDigest)
	if err != nil {
		return appplatform.ImagePlan{}, err
	}
	config, err := read(entry.ConfigDigest)
	if err != nil {
		return appplatform.ImagePlan{}, err
	}
	return appplatform.PlanImage(entry, manifest, config)
}

// loadedImagePresentHTTP is the read-only question a read-back asks: is the image a descriptor entry implies
// already in the daemon, and valid? It loads nothing and removes nothing.
func loadedImagePresentHTTP(ctx context.Context, client *http.Client, descriptor []byte, releaseSHA256, platform, arch string) (bool, error) {
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.SHA256 != releaseSHA256 || parsed.Manifest.Backend == nil || "linux/"+arch != platform {
		return false, ErrInvalidResponse
	}
	entry, ok := backendImageEntry(parsed.Manifest.Backend, platform)
	if !ok {
		return false, ErrUnsupported
	}
	var info infoResponse
	infoCtx, cancel := probeContext(ctx)
	err = readJSON(infoCtx, client, "/v"+APIVersion+"/info", &info)
	cancel()
	if err != nil {
		return false, err
	}
	state, err := inspectLoadedImage(ctx, client, ExpectedImageID(imageStoreFromInfo(info), entry), arch)
	return state == imageValid, err
}
