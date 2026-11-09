package appplatform

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
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
	return InspectOfflineEnvelopeAt(bytes.NewReader(raw), int64(len(raw)))
}

// InspectOfflineEnvelopeAt is InspectOfflineEnvelope over a random-access
// source of a known size, so a package staged on disk is inspected without
// being held in memory. The returned envelope keeps reading from source: the
// caller must keep it open while it uses OpenBlob.
func InspectOfflineEnvelopeAt(source io.ReaderAt, size int64) (OfflineEnvelope, error) {
	var zero OfflineEnvelope
	if source == nil || size <= 0 || size > MaxManagedOfflineEnvelopeBytes ||
		!boundedZipDirectoryAt(source, size, MaxManagedOfflineEntries) {
		return zero, ErrInvalidOfflineEnvelope
	}
	reader, err := zip.NewReader(source, size)
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
			file.CompressedSize64 > uint64(size)-compressed {
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
	if backend == nil && (size > MaxOfflineEnvelopeBytes || len(files) != 3) {
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
	if blob.Kind != ImageBlobLayer {
		return nil
	}
	// The layer is also decompressed, exactly as the runner will, so Portal never stages a package whose layer
	// the runner would refuse: the tar must hash to the declared diff_id and stop at the declared size.
	again, err := file.Open()
	if err != nil {
		return ErrInvalidOfflineEnvelope
	}
	defer again.Close()
	if err := verifyLayerContent(again, blob); err != nil {
		return ErrInvalidOfflineEnvelope
	}
	return nil
}

// verifyLayerContent decompresses a layer stream (when its media type is compressed) while hashing it, and checks
// the diff_id and the exact uncompressed size. It reads at most UncompressedBytes+1 bytes of tar.
func verifyLayerContent(stream io.Reader, blob ImageBlob) error {
	compressed, ok := ImageBlobCompressed(blob.MediaType)
	if !ok || blob.UncompressedBytes < 1 {
		return ErrInvalidImage
	}
	reader := stream
	if compressed {
		zr, err := gzip.NewReader(stream)
		if err != nil {
			return ErrInvalidImage
		}
		defer zr.Close()
		reader = zr
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(reader, blob.UncompressedBytes+1))
	if err != nil || n != blob.UncompressedBytes || hex.EncodeToString(hash.Sum(nil)) != blob.DiffID {
		return ErrInvalidImage
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

// boundedZipDirectoryAt is boundedFrontendDirectory over a random-access
// source: it reads the first four bytes, the end-of-central-directory window
// and the central directory itself, never the member data.
func boundedZipDirectoryAt(source io.ReaderAt, size int64, maxEntries int) bool {
	var head [4]byte
	if size < 22 || size > MaxManagedOfflineEnvelopeBytes {
		return false
	}
	if n, err := source.ReadAt(head[:], 0); (err != nil && err != io.EOF) || n != 4 || head != [4]byte{'P', 'K', 3, 4} {
		return false
	}
	window := int64(22 + 65535)
	if window > size {
		window = size
	}
	tail := make([]byte, window)
	if n, err := source.ReadAt(tail, size-window); (err != nil && err != io.EOF) || int64(n) != window {
		return false
	}
	tailStart := size - window
	for end := len(tail) - 22; end >= 0; end-- {
		if !bytes.Equal(tail[end:end+4], []byte{'P', 'K', 5, 6}) {
			continue
		}
		h := tail[end:]
		eocd := tailStart + int64(end)
		if eocd+22+int64(binary.LittleEndian.Uint16(h[20:22])) != size {
			continue
		}
		count := int(binary.LittleEndian.Uint16(h[10:12]))
		if binary.LittleEndian.Uint16(h[4:6]) != 0 || binary.LittleEndian.Uint16(h[6:8]) != 0 ||
			int(binary.LittleEndian.Uint16(h[8:10])) != count || count == 0 || count > maxEntries {
			return false
		}
		dirSize := int64(binary.LittleEndian.Uint32(h[12:16]))
		dirOffset := int64(binary.LittleEndian.Uint32(h[16:20]))
		if dirOffset+dirSize != eocd || dirSize > 4<<20 {
			return false
		}
		directory := make([]byte, dirSize)
		if n, err := source.ReadAt(directory, dirOffset); (err != nil && err != io.EOF) || int64(n) != dirSize {
			return false
		}
		records := 0
		for cursor := 0; cursor < len(directory); {
			if len(directory)-cursor < 46 || !bytes.Equal(directory[cursor:cursor+4], []byte{'P', 'K', 1, 2}) {
				return false
			}
			record := directory[cursor:]
			if binary.LittleEndian.Uint16(record[34:36]) != 0 {
				return false
			}
			length := 46 + int(binary.LittleEndian.Uint16(record[28:30])) + int(binary.LittleEndian.Uint16(record[30:32])) + int(binary.LittleEndian.Uint16(record[32:34]))
			if length > len(directory)-cursor {
				return false
			}
			records++
			if records > maxEntries {
				return false
			}
			cursor += length
		}
		return records == count
	}
	return false
}
