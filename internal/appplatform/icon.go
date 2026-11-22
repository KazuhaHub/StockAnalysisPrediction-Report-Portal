package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
)

// Custom icon limits. The icon travels inside the signed descriptor, so it is
// bounded tightly and re-encoded canonically before the host ever serves it.
const (
	MaxIconPNGBytes = 16 * 1024
	MinIconPixels   = 64
	MaxIconPixels   = 256
	// maxIconBase64 is the encoded length of MaxIconPNGBytes.
	maxIconBase64 = (MaxIconPNGBytes + 2) / 3 * 4
)

var errInvalidIcon = errors.New("invalid app icon")

// CanonicalIcon validates a descriptor's display.icon_png and returns the
// canonical re-encoded PNG with its SHA-256. The input must be strict standard
// base64 of a square PNG within the size limits; ancillary chunks and trailing
// bytes do not survive re-encoding.
func CanonicalIcon(encoded string) ([]byte, string, error) {
	if encoded == "" || len(encoded) > maxIconBase64 || len(encoded)%4 != 0 {
		return nil, "", errInvalidIcon
	}
	// The stdlib decoder silently skips CR and LF, so check the alphabet first.
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return nil, "", errInvalidIcon
		}
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) > MaxIconPNGBytes || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, "", errInvalidIcon
	}
	// Dimensions are checked before the pixel data is inflated.
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width != config.Height || config.Width < MinIconPixels || config.Width > MaxIconPixels {
		return nil, "", errInvalidIcon
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", errInvalidIcon
	}
	if b := img.Bounds(); b.Dx() != config.Width || b.Dy() != config.Height || b.Min != (image.Point{}) {
		return nil, "", errInvalidIcon
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img); err != nil {
		return nil, "", errInvalidIcon
	}
	sum := sha256.Sum256(out.Bytes())
	return out.Bytes(), hex.EncodeToString(sum[:]), nil
}

// DisplayIcon returns the canonical custom icon of a display block, if it has
// one. The display must already have passed validation.
func DisplayIcon(display *AppDisplay) (png []byte, sha256 string, ok bool) {
	if display == nil || display.IconPNG == nil {
		return nil, "", false
	}
	png, sha256, err := CanonicalIcon(*display.IconPNG)
	return png, sha256, err == nil
}
