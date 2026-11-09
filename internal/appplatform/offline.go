package appplatform

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
)

const (
	MaxOfflineEnvelopeBytes = MaxDescriptorBytes +
		MaxDescriptorSignatureBytes + MaxFrontendArchiveBytes + (1 << 20)
	MaxOfflineEnvelopeEntries = 64
)

// refusedImageMemberNames are the layout names a publisher must never supply:
// the runner writes its own index and the daemon never sees a publisher layout.
var refusedImageMemberNames = map[string]bool{
	"manifest.json": true, "repositories": true, "index.json": true, "oci-layout": true,
}

var (
	ErrInvalidOfflineEnvelope = errors.New("invalid application offline envelope")
	ErrManagedOfflineNotReady = errors.New("managed offline envelope requires image payload support")
)

type OfflineEnvelopeInspection struct {
	Format           string             `json:"format"`
	AppID            string             `json:"app_id"`
	Version          string             `json:"version"`
	Publisher        string             `json:"publisher"`
	Execution        string             `json:"execution"`
	DescriptorSHA256 string             `json:"descriptor_sha256"`
	SignatureBytes   int64              `json:"signature_bytes"`
	FrontendBlob     string             `json:"frontend_blob"`
	Frontend         FrontendInspection `json:"frontend"`
	Platforms        []string           `json:"platforms,omitempty"`
	ImageBlobs       int                `json:"image_blobs,omitempty"`
}

// OfflineEnvelope contains the exact signed declaration and frontend bytes.
// It deliberately does not establish publisher trust or execution authority.
type OfflineEnvelope struct {
	Descriptor []byte
	Signature  []byte
	Frontend   []byte
	Inspection OfflineEnvelopeInspection
	// Images holds the verified plan of every declared platform. It is empty
	// for a frontend-only envelope.
	Images []ImagePlan
	blobs  map[string]*zip.File
}

// OpenBlob streams one verified image blob member. Only digests of Images are
// available.
func (e OfflineEnvelope) OpenBlob(digest string) (io.ReadCloser, error) {
	file, ok := e.blobs[digest]
	if !ok {
		return nil, ErrInvalidOfflineEnvelope
	}
	return file.Open()
}

// InspectOfflineEnvelope validates a bounded development .portalapp archive.
// Version 1 contains only descriptor.json, descriptor.sig and
// blobs/<frontend-sha256>/<declared-frontend-name>. Version 2 adds one flat,
// stored blobs/<sha256> member per image manifest, config and layer, and is
// required by a managed descriptor (doc 170 resolution 5).
func InspectOfflineEnvelope(raw []byte) (OfflineEnvelope, error) {
	var zero OfflineEnvelope
	if len(raw) == 0 || len(raw) > MaxManagedOfflineEnvelopeBytes ||
		!boundedFrontendDirectory(raw, MaxManagedOfflineEntries) {
		return zero, ErrInvalidOfflineEnvelope
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) < 3 {
		return zero, ErrInvalidOfflineEnvelope
	}
	files := make(map[string]*zip.File, len(reader.File))
	seen := make(map[string]bool, len(reader.File))
	var compressed uint64
	for _, file := range reader.File {
		name := file.Name
		mode := file.Mode()
		if !safeRelativePath(name) || strings.HasSuffix(name, "/") ||
			refusedImageMemberNames[strings.ToLower(path.Base(name))] ||
			file.Flags&^uint16(0x080e) != 0 ||
			(file.Method != zip.Store && file.Method != zip.Deflate) ||
			(isImageBlobMember(name) && file.Method != zip.Store) ||
			!mode.IsRegular() || mode.Type() != 0 || mode.Perm()&0111 != 0 ||
			mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 ||
			file.CompressedSize64 > uint64(len(raw))-compressed {
			return zero, ErrInvalidOfflineEnvelope
		}
		compressed += file.CompressedSize64
		folded := strings.ToLower(name)
		if seen[folded] {
			return zero, ErrInvalidOfflineEnvelope
		}
		seen[folded] = true
		files[name] = file
	}
	descriptorFile, descriptorOK := files["descriptor.json"]
	signatureFile, signatureOK := files["descriptor.sig"]
	if !descriptorOK || !signatureOK {
		return zero, ErrInvalidOfflineEnvelope
	}
	descriptor, err := readOfflineEntry(
		descriptorFile, MaxDescriptorBytes)
	if err != nil {
		return zero, err
	}
	signature, err := readOfflineEntry(
		signatureFile, MaxDescriptorSignatureBytes)
	if err != nil {
		return zero, err
	}
	if _, ok := parseDescriptorSignature(signature); !ok {
		return zero, ErrInvalidOfflineEnvelope
	}
	parsed, err := ParseDescriptor(descriptor)
	if err != nil {
		return zero, ErrInvalidOfflineEnvelope
	}
	backend := parsed.Manifest.Backend
	if backend != nil && len(backend.Images) == 0 {
		// A registry reference cannot travel in a package.
		return zero, ErrManagedOfflineNotReady
	}
	if backend == nil && (len(raw) > MaxOfflineEnvelopeBytes || len(files) != 3) {
		return zero, ErrInvalidOfflineEnvelope
	}
	artifactName := path.Base(parsed.Manifest.Frontend.Artifact)
	frontendBlob := "blobs/" + parsed.Manifest.Frontend.SHA256 + "/" + artifactName
	if artifactName == "." || artifactName == "/" {
		return zero, ErrInvalidOfflineEnvelope
	}
	frontendFile, ok := files[frontendBlob]
	if !ok {
		return zero, ErrInvalidOfflineEnvelope
	}
	frontend, err := readOfflineEntry(
		frontendFile, MaxFrontendArchiveBytes)
	if err != nil {
		return zero, err
	}
	inspection, err := InspectFrontend(parsed.Manifest, frontend)
	if err != nil {
		return zero, ErrInvalidOfflineEnvelope
	}
	var plans []ImagePlan
	var blobs map[string]*zip.File
	if backend != nil {
		plans, blobs, err = inspectOfflineImages(backend.Images, files, 3)
		if err != nil {
			return zero, err
		}
	}
	sum := sha256.Sum256(descriptor)
	result := OfflineEnvelope{
		Descriptor: append([]byte(nil), descriptor...),
		Signature:  append([]byte(nil), signature...),
		Frontend:   append([]byte(nil), frontend...),
		Inspection: OfflineEnvelopeInspection{
			Format:           "portal.offline-envelope/1-dev.1",
			AppID:            parsed.Manifest.ID,
			Version:          parsed.Manifest.Version,
			Publisher:        parsed.Manifest.Publisher,
			Execution:        "static",
			DescriptorSHA256: hex.EncodeToString(sum[:]),
			SignatureBytes:   int64(len(signature)),
			FrontendBlob:     frontendBlob,
			Frontend:         inspection,
		},
		Images: plans,
		blobs:  blobs,
	}
	if backend != nil {
		result.Inspection.Format = "portal.offline-envelope/2-dev.1"
		result.Inspection.Execution = "managed"
		result.Inspection.Platforms = append([]string(nil), backend.Platforms...)
		result.Inspection.ImageBlobs = len(blobs)
	}
	return result, nil
}

// isImageBlobMember reports whether a member name has the flat image blob form
// blobs/<sha256>. The frontend member has a further path segment.
func isImageBlobMember(name string) bool {
	digest, ok := strings.CutPrefix(name, "blobs/")
	return ok && digestPattern.MatchString(digest)
}

// inspectOfflineImages verifies every platform image against its signed entry
// and requires the archive's image members to equal the referenced blob set:
// an extra member and a missing one are both refused. fixed is the count of
// non-image members (descriptor, signature, frontend).
func inspectOfflineImages(images []BackendImage, files map[string]*zip.File, fixed int) ([]ImagePlan, map[string]*zip.File, error) {
	plans := make([]ImagePlan, 0, len(images))
	blobs := map[string]*zip.File{}
	known := map[string]ImageBlob{}
	remember := func(blob ImageBlob) bool {
		if prior, ok := known[blob.Digest]; ok {
			return prior == blob
		}
		known[blob.Digest] = blob
		return true
	}
	for _, image := range images {
		manifestFile, configFile := files["blobs/"+image.ManifestDigest], files["blobs/"+image.ConfigDigest]
		if manifestFile == nil || configFile == nil {
			return nil, nil, ErrInvalidOfflineEnvelope
		}
		manifestRaw, err := readOfflineEntry(manifestFile, MaxImageMetadataBytes)
		if err != nil {
			return nil, nil, err
		}
		configRaw, err := readOfflineEntry(configFile, MaxImageMetadataBytes)
		if err != nil {
			return nil, nil, err
		}
		plan, err := PlanImage(image, manifestRaw, configRaw)
		if err != nil {
			return nil, nil, ErrInvalidOfflineEnvelope
		}
		for _, blob := range plan.Blobs() {
			file := files["blobs/"+blob.Digest]
			if file == nil || !remember(blob) || int64(file.UncompressedSize64) != blob.Size {
				return nil, nil, ErrInvalidOfflineEnvelope
			}
			if blob.Kind == ImageBlobLayer {
				if err := verifyOfflineBlob(file, blob); err != nil {
					return nil, nil, err
				}
			}
			blobs[blob.Digest] = file
		}
		plans = append(plans, plan)
	}
	if len(files) != fixed+len(blobs) {
		return nil, nil, ErrInvalidOfflineEnvelope
	}
	return plans, blobs, nil
}

// verifyOfflineBlob hashes a stored member against the digest its name and the
// manifest declare, without holding the blob in memory.
func verifyOfflineBlob(file *zip.File, blob ImageBlob) error {
	if blob.Size > MaxImageBlobBytes {
		return ErrInvalidOfflineEnvelope
	}
	stream, err := file.Open()
	if err != nil {
		return ErrInvalidOfflineEnvelope
	}
	defer stream.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(stream, blob.Size+1))
	if err != nil || n != blob.Size || hex.EncodeToString(hash.Sum(nil)) != blob.Digest {
		return ErrInvalidOfflineEnvelope
	}
	return nil
}

func readOfflineEntry(file *zip.File, limit int64) ([]byte, error) {
	if file == nil || file.UncompressedSize64 > uint64(limit) {
		return nil, ErrInvalidOfflineEnvelope
	}
	stream, err := file.Open()
	if err != nil {
		return nil, ErrInvalidOfflineEnvelope
	}
	raw, readErr := io.ReadAll(io.LimitReader(stream, limit+1))
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) > limit ||
		uint64(len(raw)) != file.UncompressedSize64 {
		return nil, ErrInvalidOfflineEnvelope
	}
	return raw, nil
}
