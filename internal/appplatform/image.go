package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Limits of managed image content (doc 170 resolution 5).
const (
	MaxManagedOfflineEnvelopeBytes = 384 << 20
	MaxManagedOfflineEntries       = 160
	MaxImageBlobBytes              = 256 << 20
	MaxImageMetadataBytes          = 1 << 20
)

// Descriptor bounds, exported for the runner that enforces them.
const (
	MaxImageLayers                 = maxImageLayers
	MaxImageLayerUncompressedBytes = maxImageLayerUncompressedBytes
)

var ErrInvalidImage = errors.New("invalid managed image content")

// ImageBlobKind names the role a blob plays in an image. It is carried by the
// blob channel so the runner never infers a role from bytes.
type ImageBlobKind string

const (
	ImageBlobManifest ImageBlobKind = "manifest"
	ImageBlobConfig   ImageBlobKind = "config"
	ImageBlobLayer    ImageBlobKind = "layer"
)

const (
	MediaTypeOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeOCIConfig      = "application/vnd.oci.image.config.v1+json"
	MediaTypeDockerConfig   = "application/vnd.docker.container.image.v1+json"
)

// imageLayerMediaTypes maps every accepted layer media type to whether the
// blob is gzip-compressed. zstd, foreign and non-distributable layers are
// refused in this release.
var imageLayerMediaTypes = map[string]bool{
	"application/vnd.oci.image.layer.v1.tar":            false,
	"application/vnd.oci.image.layer.v1.tar+gzip":       true,
	"application/vnd.docker.image.rootfs.diff.tar":      false,
	"application/vnd.docker.image.rootfs.diff.tar.gzip": true,
}

// ImageBlobCompressed reports whether a layer media type is gzip-compressed.
func ImageBlobCompressed(mediaType string) (compressed, ok bool) {
	compressed, ok = imageLayerMediaTypes[mediaType]
	return
}

// ImageBlob is one content-addressed member of a managed image.
type ImageBlob struct {
	Digest            string        `json:"digest"`
	Kind              ImageBlobKind `json:"kind"`
	MediaType         string        `json:"media_type"`
	Size              int64         `json:"size"`
	DiffID            string        `json:"diff_id,omitempty"`
	UncompressedBytes int64         `json:"uncompressed_bytes,omitempty"`
}

// ImagePlan is the verified shape of one platform image: the manifest, the
// config and the ordered layers, all bound to the signed descriptor entry.
type ImagePlan struct {
	Platform string
	Manifest ImageBlob
	Config   ImageBlob
	Layers   []ImageBlob
}

// Blobs lists the manifest, the config and the layers in that order.
func (p ImagePlan) Blobs() []ImageBlob {
	out := make([]ImageBlob, 0, 2+len(p.Layers))
	return append(append(out, p.Manifest, p.Config), p.Layers...)
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type ociManifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        ociDescriptor     `json:"config"`
	Layers        []ociDescriptor   `json:"layers"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type ociConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Config       struct {
		Volumes map[string]json.RawMessage `json:"Volumes"`
	} `json:"config"`
	RootFS struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

// PlanImage verifies one platform image against its signed descriptor entry.
// The caller has already checked that the manifest and config bytes hash to the
// digests the entry declares; this binds the manifest's references, the layer
// order and the platform to the entry, and refuses every form the runner will
// not load.
func PlanImage(entry BackendImage, manifestRaw, configRaw []byte) (ImagePlan, error) {
	var zero ImagePlan
	if len(manifestRaw) == 0 || len(manifestRaw) > MaxImageMetadataBytes || len(configRaw) == 0 || len(configRaw) > MaxImageMetadataBytes {
		return zero, ErrInvalidImage
	}
	if sha256Hex(manifestRaw) != entry.ManifestDigest || sha256Hex(configRaw) != entry.ConfigDigest {
		return zero, ErrInvalidImage
	}
	var manifest ociManifest
	if !decodeStrictImageJSON(manifestRaw, &manifest, true) || manifest.SchemaVersion != 2 ||
		(manifest.MediaType != MediaTypeOCIManifest && manifest.MediaType != MediaTypeDockerManifest) {
		return zero, ErrInvalidImage
	}
	if manifest.Config.MediaType != MediaTypeOCIConfig && manifest.Config.MediaType != MediaTypeDockerConfig {
		return zero, ErrInvalidImage
	}
	configDigest, ok := imageDigestHex(manifest.Config.Digest)
	if !ok || configDigest != entry.ConfigDigest || manifest.Config.Size != int64(len(configRaw)) {
		return zero, ErrInvalidImage
	}
	if len(manifest.Layers) != len(entry.Layers) || len(manifest.Layers) == 0 {
		return zero, ErrInvalidImage
	}
	var config ociConfig
	if !decodeStrictImageJSON(configRaw, &config, false) || config.OS != "linux" || config.Architecture != platformArchitecture(entry.Platform) ||
		config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(entry.Layers) || len(config.Config.Volumes) != 0 {
		return zero, ErrInvalidImage
	}
	plan := ImagePlan{
		Platform: entry.Platform,
		Manifest: ImageBlob{Digest: entry.ManifestDigest, Kind: ImageBlobManifest, MediaType: manifest.MediaType, Size: int64(len(manifestRaw))},
		Config:   ImageBlob{Digest: entry.ConfigDigest, Kind: ImageBlobConfig, MediaType: manifest.Config.MediaType, Size: int64(len(configRaw))},
	}
	for i, layer := range manifest.Layers {
		digest, ok := imageDigestHex(layer.Digest)
		diffID, diffOK := imageDigestHex(config.RootFS.DiffIDs[i])
		if _, known := imageLayerMediaTypes[layer.MediaType]; !known || !ok || !diffOK ||
			layer.Size < 1 || layer.Size > MaxImageBlobBytes || diffID != entry.Layers[i].DiffID {
			return zero, ErrInvalidImage
		}
		plan.Layers = append(plan.Layers, ImageBlob{
			Digest: digest, Kind: ImageBlobLayer, MediaType: layer.MediaType, Size: layer.Size,
			DiffID: entry.Layers[i].DiffID, UncompressedBytes: int64(entry.Layers[i].UncompressedBytes),
		})
	}
	return plan, nil
}

// ManifestConfigDescriptor reads the config descriptor (media type and size) a manifest declares, so a caller can
// tell what the config blob must look like before the config itself has arrived. The manifest bytes must already
// hash to the entry's digest.
func ManifestConfigDescriptor(entry BackendImage, manifestRaw []byte) (mediaType string, size int64, ok bool) {
	if len(manifestRaw) == 0 || len(manifestRaw) > MaxImageMetadataBytes || sha256Hex(manifestRaw) != entry.ManifestDigest {
		return "", 0, false
	}
	var manifest ociManifest
	if !decodeStrictImageJSON(manifestRaw, &manifest, true) || manifest.SchemaVersion != 2 {
		return "", 0, false
	}
	digest, valid := imageDigestHex(manifest.Config.Digest)
	return manifest.Config.MediaType, manifest.Config.Size, valid && digest == entry.ConfigDigest
}

func platformArchitecture(platform string) string { return strings.TrimPrefix(platform, "linux/") }

// imageDigestHex accepts only the "sha256:<hex>" form and returns the hex.
func imageDigestHex(value string) (string, bool) {
	hex, ok := strings.CutPrefix(value, "sha256:")
	return hex, ok && digestPattern.MatchString(hex)
}

// decodeStrictImageJSON refuses duplicate keys and trailing data, so a document
// has one interpretation. A closed document (the manifest) also refuses
// unknown fields and keys that differ only by case, which encoding/json would
// otherwise merge into one field; the config is open and keeps exact-case
// keys because label maps legitimately differ by case.
func decodeStrictImageJSON(raw []byte, out any, closed bool) bool {
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	if token, err := scan.Token(); err != nil || token != json.Delim('{') || !scanImageObject(scan, 1, closed) {
		return false
	}
	if _, err := scan.Token(); err != io.EOF {
		return false
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	if closed {
		decode.DisallowUnknownFields()
	}
	return decode.Decode(out) == nil
}

// scanImageObject mirrors scanObject without the descriptor key grammar: OCI
// keys are camelCase.
func scanImageObject(d *json.Decoder, depth int, fold bool) bool {
	if depth > maxDescriptorDepth {
		return false
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if fold {
			key = strings.ToLower(key)
		}
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		if !scanImageValue(d, depth+1, fold) {
			return false
		}
	}
	token, err := d.Token()
	return err == nil && token == json.Delim('}')
}

func scanImageValue(d *json.Decoder, depth int, fold bool) bool {
	if depth > maxDescriptorDepth {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		return scanImageObject(d, depth, fold)
	case json.Delim('['):
		for d.More() {
			if !scanImageValue(d, depth+1, fold) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim(']')
	}
	_, delimiter := token.(json.Delim)
	return !delimiter
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
