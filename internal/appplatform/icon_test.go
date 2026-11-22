package appplatform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"strings"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func crc32Bytes(b []byte) []byte {
	sum := crc32.ChecksumIEEE(b)
	return []byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)}
}

func TestCanonicalIconAccepts(t *testing.T) {
	raw := testPNG(t, 64, 64)
	got, sum, err := CanonicalIcon(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum) != 64 || !bytes.HasPrefix(got, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("sum=%q", sum)
	}
	again, sum2, err := CanonicalIcon(base64.StdEncoding.EncodeToString(got))
	if err != nil || sum2 != sum || !bytes.Equal(again, got) {
		t.Fatalf("canonical form is not stable: %v", err)
	}
	if _, _, err := CanonicalIcon(base64.StdEncoding.EncodeToString(testPNG(t, 256, 256))); err != nil {
		t.Fatalf("256x256: %v", err)
	}
}

func TestCanonicalIconStripsAncillaryChunks(t *testing.T) {
	raw := testPNG(t, 64, 64)
	// Insert a tEXt chunk right after IHDR (8 signature + 25 IHDR bytes).
	body := []byte("tEXtComment\x00hello")
	chunk := new(bytes.Buffer)
	chunk.Write([]byte{0, 0, 0, byte(len(body) - 4)})
	chunk.Write(body)
	chunk.Write(crc32Bytes(body))
	with := append(append(append([]byte{}, raw[:33]...), chunk.Bytes()...), raw[33:]...)
	with = append(with, []byte("trailing")...)
	got, _, err := CanonicalIcon(base64.StdEncoding.EncodeToString(with))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(got, []byte("tEXt")) || bytes.Contains(got, []byte("trailing")) {
		t.Fatal("ancillary chunk or trailing data survived")
	}
}

func TestCanonicalIconRejects(t *testing.T) {
	good := testPNG(t, 64, 64)
	var gifBuf bytes.Buffer
	if err := gif.Encode(&gifBuf, image.NewPaletted(image.Rect(0, 0, 64, 64), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString
	big := append(append([]byte{}, good...), bytes.Repeat([]byte{0}, MaxIconPNGBytes+1-len(good))...)
	cases := map[string]string{
		"gif":             enc(gifBuf.Bytes()),
		"svg":             enc([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64"/>`)),
		"truncated":       enc(good[:len(good)-20]),
		"truncated idat":  enc(append(append([]byte{}, good[:len(good)/2]...), good[len(good)-12:]...)),
		"too large dims":  enc(testPNG(t, 300, 300)),
		"too small dims":  enc(testPNG(t, 63, 63)),
		"not square":      enc(testPNG(t, 64, 65)),
		"16KiB+1":         enc(big),
		"newline":         enc(good)[:8] + "\n" + enc(good)[8:],
		"space":           enc(good)[:8] + " " + enc(good)[8:],
		"url alphabet":    strings.NewReplacer("+", "-", "/", "_").Replace(enc(append([]byte{0xfb, 0xff, 0xfe}, good...))),
		"missing padding": strings.TrimRight(enc(append(append([]byte{}, good...), make([]byte, (4-len(good)%3)%3)...)), "="),
		"empty":           "",
		"data uri":        "data:image/png;base64," + enc(good),
	}
	for name, in := range cases {
		if _, _, err := CanonicalIcon(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCanonicalIconRejectsOversizedBase64(t *testing.T) {
	if _, _, err := CanonicalIcon(strings.Repeat("A", 22000)); err == nil {
		t.Fatal("accepted")
	}
}

func TestDescriptorIconPNG(t *testing.T) {
	raw, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	presentation := root["presentation"].(map[string]any)
	enc := base64.StdEncoding.EncodeToString
	cases := []struct {
		name  string
		icon  string
		valid bool
	}{
		{"valid", enc(testPNG(t, 64, 64)), true},
		{"not square", enc(testPNG(t, 64, 65)), false},
		{"not base64", "%%%", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			presentation["display"] = map[string]any{"icon": "file-text", "icon_png": c.icon, "texts": []any{
				map[string]any{"locale": "en-US", "name": "Notes", "description": "Personal notes"},
				map[string]any{"locale": "zh-CN", "name": "笔记", "description": "个人笔记"},
				map[string]any{"locale": "zh-TW", "name": "筆記", "description": "個人筆記"},
			}}
			b, _ := json.Marshal(root)
			_, err := ParseDescriptor(b)
			if (err == nil) != c.valid {
				t.Fatalf("valid=%v err=%v", c.valid, err)
			}
		})
	}
}

// Parsing validates only: the expensive canonical re-encode runs when a hash is produced, once per input.
func TestParserDoesNotEncodeAndCanonicalFormIsMemoised(t *testing.T) {
	// A size no other test uses, so the memo cannot already hold it.
	encoded := base64.StdEncoding.EncodeToString(testPNG(t, 66, 66))
	before := iconEncodes.Load()
	if err := ValidateIcon(encoded); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	root["presentation"].(map[string]any)["display"] = map[string]any{"icon": "file-text", "icon_png": encoded, "texts": []any{
		map[string]any{"locale": "en-US", "name": "Notes", "description": "Personal notes"},
		map[string]any{"locale": "zh-CN", "name": "笔记", "description": "个人笔记"},
		map[string]any{"locale": "zh-TW", "name": "筆記", "description": "個人筆記"},
	}}
	b, _ := json.Marshal(root)
	if _, err := ParseDescriptor(b); err != nil {
		t.Fatal(err)
	}
	if got := iconEncodes.Load(); got != before {
		t.Fatalf("parsing encoded %d time(s)", got-before)
	}
	_, first, err := CanonicalIcon(encoded)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := CanonicalIcon(encoded)
	if err != nil || second != first {
		t.Fatal(err)
	}
	if got := iconEncodes.Load(); got != before+1 {
		t.Fatalf("expected one encode for two calls, got %d", got-before)
	}
}

// A hot path that parses the same descriptor again must not decode the image again.
func TestSecondParseOfTheSameDescriptorDoesNotDecode(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(testPNG(t, 67, 67))
	raw, err := os.ReadFile("testdata/descriptors/static.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	root["presentation"].(map[string]any)["display"] = map[string]any{"icon": "file-text", "icon_png": encoded, "texts": []any{
		map[string]any{"locale": "en-US", "name": "Notes", "description": "Personal notes"},
		map[string]any{"locale": "zh-CN", "name": "笔记", "description": "个人笔记"},
		map[string]any{"locale": "zh-TW", "name": "筆記", "description": "個人筆記"},
	}}
	b, _ := json.Marshal(root)
	before := iconDecodes.Load()
	for i := 0; i < 3; i++ {
		if _, err := ParseDescriptor(b); err != nil {
			t.Fatal(err)
		}
	}
	if got := iconDecodes.Load() - before; got != 1 {
		t.Fatalf("three parses decoded %d times", got)
	}
	// A failure is never remembered as a success.
	bad := encoded[:len(encoded)-4] + "AAAA"
	for i := 0; i < 2; i++ {
		if ValidateIcon(bad) == nil {
			t.Fatal("corrupted icon accepted")
		}
	}
}

func TestIconMemoIsLeastRecentlyUsed(t *testing.T) {
	c := newIconLRU[int]()
	key := func(i int) [32]byte { return [32]byte{byte(i), byte(i >> 8)} }
	for i := 0; i < iconMemoEntries; i++ {
		c.put(key(i), i)
	}
	c.get(key(0)) // touch the oldest
	c.put(key(iconMemoEntries), 0)
	if _, ok := c.get(key(0)); !ok {
		t.Fatal("recently used entry was evicted")
	}
	if _, ok := c.get(key(1)); ok {
		t.Fatal("least recently used entry survived")
	}
	if c.order.Len() != iconMemoEntries || len(c.entries) != iconMemoEntries {
		t.Fatal("memo is not bounded")
	}
}
