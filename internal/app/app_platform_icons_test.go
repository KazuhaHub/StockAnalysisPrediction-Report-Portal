package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// isolatePlatformIcons gives the test its own empty icon cache and restores the shared one afterwards.
func isolatePlatformIcons(t *testing.T) {
	t.Helper()
	saved := platformIcons
	platformIcons = newPlatformIconCache()
	t.Cleanup(func() { platformIcons = saved })
}

func iconTestPNG(t *testing.T, size int) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < size; i++ {
		img.Set(i, i, color.NRGBA{200, 30, 30, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestPlatformDisplayViewCarriesHashNotBytes(t *testing.T) {
	isolatePlatformIcons(t)
	encoded := iconTestPNG(t, 64)
	view := platformDisplayViewOf(&appplatform.AppDisplay{Icon: "file-text", IconPNG: &encoded, Texts: []appplatform.AppDisplayText{{Locale: "en-US", Name: "Notes", Description: "d"}}})
	if view == nil || len(view.IconSHA256) != 64 {
		t.Fatalf("view=%+v", view)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), encoded) || strings.Contains(string(body), "icon_png") || !strings.Contains(string(body), `"icon_sha256":"`+view.IconSHA256+`"`) {
		t.Fatal(string(body))
	}
	if _, ok := platformIcons.get(view.IconSHA256); !ok {
		t.Fatal("icon not cached")
	}
	if platformDisplayViewOf(nil) != nil {
		t.Fatal("nil display must stay nil")
	}
	plain := platformDisplayViewOf(&appplatform.AppDisplay{Icon: "cloud"})
	if plain.IconSHA256 != "" {
		t.Fatal("built-in icon only")
	}
}

func TestPlatformIconCacheIsBounded(t *testing.T) {
	t.Parallel()
	c := newPlatformIconCache()
	for i := 0; i < platformIconCacheEntries+10; i++ {
		c.put(fmt.Sprintf("%064x", i), []byte{byte(i)})
	}
	if c.order.Len() != platformIconCacheEntries || len(c.entries) != platformIconCacheEntries {
		t.Fatal(c.order.Len(), len(c.entries))
	}
	if _, ok := c.get(fmt.Sprintf("%064x", 0)); ok {
		t.Fatal("oldest entry should be evicted")
	}
	if _, ok := c.get(fmt.Sprintf("%064x", platformIconCacheEntries+9)); !ok {
		t.Fatal("newest entry must be kept")
	}
}

func TestPlatformIconRoute(t *testing.T) {
	isolatePlatformIcons(t)
	s, _, _, _, cookie := platformManagementFixture(t, false)
	mux := http.NewServeMux()
	s.platformIconRoute(mux)
	encoded := iconTestPNG(t, 64)
	sha := platformDisplayViewOf(&appplatform.AppDisplay{Icon: "file-text", IconPNG: &encoded}).IconSHA256

	got := sessionTestRequest(mux, "GET", "/api/app-platform/icons/"+sha+".png", cookie, "")
	if got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	want := map[string]string{
		"Content-Type":                 "image/png",
		"X-Content-Type-Options":       "nosniff",
		"Content-Security-Policy":      "default-src 'none'; sandbox",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Cache-Control":                "private, max-age=31536000, immutable",
	}
	for k, v := range want {
		if got.Header().Get(k) != v {
			t.Errorf("%s = %q", k, got.Header().Get(k))
		}
	}
	if _, err := png.Decode(bytes.NewReader(got.Body.Bytes())); err != nil {
		t.Fatal("body is not a PNG", err)
	}

	for _, path := range []string{
		"/api/app-platform/icons/" + strings.Repeat("0", 64) + ".png", // miss
		"/api/app-platform/icons/" + sha,                              // no extension
		"/api/app-platform/icons/" + strings.ToUpper(sha) + ".png",    // not canonical
		"/api/app-platform/icons/..%2f" + sha + ".png",
	} {
		if got := sessionTestRequest(mux, "GET", path, cookie, ""); got.Code != 404 {
			t.Errorf("%s: %d", path, got.Code)
		}
	}
	if got := sessionTestRequest(mux, "GET", "/api/app-platform/icons/"+sha+".png", "", ""); got.Code != 401 {
		t.Fatalf("signed-out request: %d", got.Code)
	}
}

func TestPlatformIconsSkipGzip(t *testing.T) {
	t.Parallel()
	if gzipEligible("/api/app-platform/icons/" + strings.Repeat("a", 64) + ".png") {
		t.Fatal("PNG must not be compressed again")
	}
}

func TestPlatformMarketCatalogueShowsPublisherNameAndIconHash(t *testing.T) {
	isolatePlatformIcons(t)
	h := newMarketCatalogueHarness(t)
	raw, _, _ := platformSignedReleaseFixture(t, "1.0.0")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	encoded, name := iconTestPNG(t, 64), "Kazuha Hub"
	manifest.ID, manifest.Publisher, manifest.PublisherName = "notes-icon", "fixture-publisher", &name
	manifest.Presentation.Display.IconPNG = &encoded
	signedRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	h.add("fixture-publisher", "notes-icon", "1.0.0", platformDigest(signedRaw), signedRaw, platformSignTestDescriptorAs(t, signedRaw, "fixture-publisher"))

	_, apps, body := h.fetch()
	if len(apps) != 1 || apps[0].Display == nil {
		t.Fatal(body)
	}
	if strings.Contains(body, encoded) || strings.Contains(body, "icon_png") {
		t.Fatal("JSON must not carry icon bytes")
	}
	var parsed struct {
		Apps []struct {
			PublisherName string `json:"publisher_name"`
			Display       struct {
				IconSHA256 string `json:"icon_sha256"`
			} `json:"display"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Apps[0].PublisherName != "Kazuha Hub" || len(parsed.Apps[0].Display.IconSHA256) != 64 {
		t.Fatal(body)
	}
	if _, ok := platformIcons.get(parsed.Apps[0].Display.IconSHA256); !ok {
		t.Fatal("verified catalogue entry must fill the icon cache")
	}
}

func TestPlatformUnverifiedDisplayHasNoCustomIcon(t *testing.T) {
	isolatePlatformIcons(t)
	encoded := iconTestPNG(t, 64)
	view := platformDisplayViewUnverified(&appplatform.AppDisplay{Icon: "cloud", IconPNG: &encoded})
	if view.IconSHA256 != "" || view.Icon != "cloud" || platformIcons.order.Len() != 0 {
		t.Fatalf("view=%+v cached=%d", view, platformIcons.order.Len())
	}
	if platformDisplayViewUnverified(nil) != nil {
		t.Fatal("nil display must stay nil")
	}
}
