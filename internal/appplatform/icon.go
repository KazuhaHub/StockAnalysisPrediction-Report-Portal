package appplatform

import (
	"bytes"
	"container/list"
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

// iconDecodes counts full validation decodes; tests use it to prove validation is memoised.
var iconDecodes atomic.Int64

// decodeIcon is the validation shared by the parser and CanonicalIcon: strict base64, size, PNG magic, a
// square 64-256 px image (checked before any pixel data is inflated) and a full decode. It never encodes.
func decodeIcon(encoded string) (image.Image, error) {
	iconDecodes.Add(1)
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

// iconMemoEntries bounds each memo; it matches the host's served-icon cache so a full listing fits.
const iconMemoEntries = 256

// iconLRU is a small mutex-guarded least-recently-used map keyed by the SHA-256 of a descriptor's icon_png.
type iconLRU[V any] struct {
	mu      sync.Mutex
	order   *list.List // front is the most recently used
	entries map[[32]byte]*list.Element
}

type iconLRUEntry[V any] struct {
	key   [32]byte
	value V
}

func newIconLRU[V any]() *iconLRU[V] {
	return &iconLRU[V]{order: list.New(), entries: map[[32]byte]*list.Element{}}
}

func (c *iconLRU[V]) get(key [32]byte) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*iconLRUEntry[V]).value, true
	}
	var zero V
	return zero, false
}

func (c *iconLRU[V]) put(key [32]byte, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		c.order.MoveToFront(el)
		return
	}
	c.entries[key] = c.order.PushFront(&iconLRUEntry[V]{key, value})
	for c.order.Len() > iconMemoEntries {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*iconLRUEntry[V]).key)
	}
}

var validIcons = newIconLRU[struct{}]()

// ValidateIcon checks a descriptor's display.icon_png without producing the canonical form. It is what the
// descriptor parser runs. A success is memoised by the hash of the input, so a hot path that parses the same
// descriptor again pays a hash and a lookup instead of a decode. Failures are never memoised.
func ValidateIcon(encoded string) error {
	key := sha256.Sum256([]byte(encoded))
	if _, ok := validIcons.get(key); ok {
		return nil
	}
	if _, err := decodeIcon(encoded); err != nil {
		return err
	}
	validIcons.put(key, struct{}{})
	return nil
}

type canonicalIcon struct {
	png []byte
	sha string
}

var canonicalIcons = newIconLRU[canonicalIcon]()

// CanonicalIcon validates a descriptor's display.icon_png and returns the canonical re-encoded PNG with its
// SHA-256. Ancillary chunks and trailing bytes do not survive re-encoding. The result is memoised by the hash
// of the input, so repeated listings of the same descriptor cost one hash.
func CanonicalIcon(encoded string) ([]byte, string, error) {
	key := sha256.Sum256([]byte(encoded))
	if hit, ok := canonicalIcons.get(key); ok {
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
	canonicalIcons.put(key, entry)
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
