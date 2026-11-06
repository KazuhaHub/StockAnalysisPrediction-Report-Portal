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
}

// OfflineEnvelope contains the exact signed declaration and frontend bytes.
// It deliberately does not establish publisher trust or execution authority.
type OfflineEnvelope struct {
	Descriptor []byte
	Signature  []byte
	Frontend   []byte
	Inspection OfflineEnvelopeInspection
}

// InspectOfflineEnvelope validates a bounded development .portalapp archive.
// The current initial format contains only descriptor.json, descriptor.sig and
// blobs/<frontend-sha256>/<declared-frontend-name>. Managed image payloads are
// refused until their exact OCI layout is implemented and verified.
func InspectOfflineEnvelope(raw []byte) (OfflineEnvelope, error) {
	var zero OfflineEnvelope
	if len(raw) == 0 || len(raw) > MaxOfflineEnvelopeBytes ||
		!boundedFrontendDirectory(raw, MaxOfflineEnvelopeEntries) {
		return zero, ErrInvalidOfflineEnvelope
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) != 3 {
		return zero, ErrInvalidOfflineEnvelope
	}
	files := make(map[string]*zip.File, 3)
	seen := make(map[string]bool, 3)
	var compressed uint64
	for _, file := range reader.File {
		name := file.Name
		mode := file.Mode()
		if !safeRelativePath(name) || strings.HasSuffix(name, "/") ||
			file.Flags&^uint16(0x080e) != 0 ||
			(file.Method != zip.Store && file.Method != zip.Deflate) ||
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
	if parsed.Manifest.Backend != nil {
		return zero, ErrManagedOfflineNotReady
	}
	artifactName := path.Base(parsed.Manifest.Frontend.Artifact)
	frontendBlob := "blobs/" + parsed.Manifest.Frontend.SHA256 + "/" + artifactName
	if artifactName == "." || artifactName == "/" {
		return zero, ErrInvalidOfflineEnvelope
	}
	frontendFile, ok := files[frontendBlob]
	if !ok || len(files) != 3 {
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
	sum := sha256.Sum256(descriptor)
	return OfflineEnvelope{
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
	}, nil
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
