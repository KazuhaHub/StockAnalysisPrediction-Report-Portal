package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
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

// iconEncodes counts canonical re-encodes; tests use it to prove that parsing never encodes.
var iconEncodes atomic.Int64

// decodeIcon is the validation shared by the parser and CanonicalIcon: strict base64, size, PNG magic, a
// square 64-256 px image (checked before any pixel data is inflated) and a full decode. It never encodes.
func decodeIcon(encoded string) (image.Image, error) {
	if encoded == "" || len(encoded) > maxIconBase64 || len(encoded)%4 != 0 {
		return nil, errInvalidIcon
	}
	// The stdlib decoder silently skips CR and LF, so check the alphabet first.
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/' || c == '=') {
			return nil, errInvalidIcon
		}
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) > MaxIconPNGBytes || !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, errInvalidIcon
	}
	config, err := png.DecodeConfig(bytes.NewReader(raw))
	if err != nil || config.Width != config.Height || config.Width < MinIconPixels || config.Width > MaxIconPixels {
		return nil, errInvalidIcon
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, errInvalidIcon
	}
	if b := img.Bounds(); b.Dx() != config.Width || b.Dy() != config.Height || b.Min != (image.Point{}) {
		return nil, errInvalidIcon
	}
	return img, nil
}

// ValidateIcon checks a descriptor's display.icon_png without producing the canonical form. It is what the
// descriptor parser runs, so it stays cheap.
func ValidateIcon(encoded string) error {
	_, err := decodeIcon(encoded)
	return err
}

type canonicalIcon struct {
	png []byte
	sha string
}

const canonicalIconCacheEntries = 64

var canonicalIcons = struct {
	sync.Mutex
	byInput map[[32]byte]canonicalIcon
	order   [][32]byte
}{byInput: map[[32]byte]canonicalIcon{}}

// CanonicalIcon validates a descriptor's display.icon_png and returns the canonical re-encoded PNG with its
// SHA-256. Ancillary chunks and trailing bytes do not survive re-encoding. The result is memoised by the hash
// of the input in a small bounded cache, so repeated listings of the same descriptor cost one hash.
func CanonicalIcon(encoded string) ([]byte, string, error) {
	key := sha256.Sum256([]byte(encoded))
	canonicalIcons.Lock()
	hit, ok := canonicalIcons.byInput[key]
	canonicalIcons.Unlock()
	if ok {
		return hit.png, hit.sha, nil
	}
	img, err := decodeIcon(encoded)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	iconEncodes.Add(1)
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img); err != nil {
		return nil, "", errInvalidIcon
	}
	sum := sha256.Sum256(out.Bytes())
	entry := canonicalIcon{out.Bytes(), hex.EncodeToString(sum[:])}
	canonicalIcons.Lock()
	if _, dup := canonicalIcons.byInput[key]; !dup {
		canonicalIcons.byInput[key] = entry
		canonicalIcons.order = append(canonicalIcons.order, key)
		if len(canonicalIcons.order) > canonicalIconCacheEntries {
			delete(canonicalIcons.byInput, canonicalIcons.order[0])
			canonicalIcons.order = canonicalIcons.order[1:]
		}
	}
	canonicalIcons.Unlock()
	return entry.png, entry.sha, nil
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
