package appplatform

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestFrontendDocumentEmbedsVerifiedLocalResources(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: `<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="./style.css"></head><body><div id="app"></div><img src="icon.svg"><script src="app.js" defer></script></body></html>`}, frontendTestEntry{name: "app.js", body: "console.log('fixture')", method: zip.Deflate}, frontendTestEntry{name: "style.css", body: `body{background:url("icon.svg");font-family:Test}@font-face{font-family:Test;src:url(font.woff2)}`}, frontendTestEntry{name: "icon.svg", body: `<svg xmlns="http://www.w3.org/2000/svg"/>`}, frontendTestEntry{name: "font.woff2", body: "fixture-font"})
	before := bytes.Clone(raw)
	result, err := BuildFrontendDocument(frontendManifest(t, raw), raw)
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Content)
	if !strings.Contains(text, "data:text/javascript; charset=utf-8;base64,"+base64.StdEncoding.EncodeToString([]byte("console.log('fixture')"))) || strings.Contains(text, `src="app.js"`) || !strings.Contains(text, "data:image/svg+xml;base64,") {
		t.Fatal("document retained relative executable resources")
	}
	if result.SourceSHA256 == "" || result.DocumentSHA256 == "" || !bytes.Equal(raw, before) {
		t.Fatal("missing derived integrity")
	}
	if strings.Contains(result.CSP, "allow-same-origin") || !strings.Contains(result.CSP, "connect-src 'none'") || !strings.Contains(result.CSP, "sandbox allow-scripts") {
		t.Fatal("sandbox boundary was weakened")
	}
}
func TestFrontendDocumentRejectsUnsupportedInputs(t *testing.T) {
	for _, body := range []string{`<script src="app.js" src="missing.js"></script>`, `<script/src="app.js" src="missing.js"></script>`, `<base href="https://evil.invalid/">`, `<iframe src="/api/admin"></iframe>`, `<img src="https://evil.invalid/pixel">`, `<script type="module" src="app.js"></script>`, `<script src="/app.js"></script>`, `<script src="app.js?key=x"></script>`, `<div onclick="alert(1)"></div>`, `<meta http-equiv="refresh" content="0;url=https://evil.invalid">`, `<img src="icon.svg" srcset="https://evil.invalid/a 1x">`, `<link rel="stylesheet" href="missing.css">`, `<link rel="preload" href="app.js">`} {
		t.Run(body, func(t *testing.T) {
			raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: body}, frontendTestEntry{name: "app.js", body: "console.log(1)"}, frontendTestEntry{name: "icon.svg", body: "<svg/>"})
			result, err := BuildFrontendDocument(frontendManifest(t, raw), raw)
			if err == nil || result.Content != nil || result.CSP != "" {
				t.Fatal("unsupported document received executable output")
			}
		})
	}
	for _, css := range []string{`@import "more.css";`, `@\69mport "more.css";`, `body{background:url(https://evil.invalid/a)}`, `body{background:URL(icon.svg)}`, `body{background:u\72l(icon.svg)}`, `body{background:url("\69con.svg")}`, `body{content:"unterminated}`, `body{filter:url('#bad\"value')}`} {
		raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: `<link rel="stylesheet" href="style.css">`}, frontendTestEntry{name: "style.css", body: css}, frontendTestEntry{name: "icon.svg", body: "<svg/>"})
		if result, err := BuildFrontendDocument(frontendManifest(t, raw), raw); err == nil || result.Content != nil {
			t.Fatalf("unsupported CSS accepted: %q", css)
		}
	}
}
func TestFrontendDocumentInlineScriptsAreDataResources(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: `<style>body{color:red}</style><div id="app"></div><script>parent.postMessage('fixture','*')</script>`})
	result, err := BuildFrontendDocument(frontendManifest(t, raw), raw)
	if err != nil || strings.Contains(string(result.Content), "parent.postMessage") || !strings.Contains(string(result.Content), "data:text/javascript") {
		t.Fatal("inline script escaped transport policy")
	}
}
func TestFrontendDocumentBoundsHTMLAndExpansion(t *testing.T) {
	body := strings.Repeat("<div>", 100) + "fixture" + strings.Repeat("</div>", 100)
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: body})
	if result, err := BuildFrontendDocument(frontendManifest(t, raw), raw); err == nil || result.Content != nil {
		t.Fatal("unbounded DOM depth accepted")
	}
}

func TestFrontendDocumentBoundsRepeatedResourceExpansion(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: strings.Repeat(`<script src="app.js"></script>`, 20)}, frontendTestEntry{name: "app.js", body: strings.Repeat(" ", 1<<20), method: zip.Deflate})
	if result, err := BuildFrontendDocument(frontendManifest(t, raw), raw); err == nil || result.Content != nil {
		t.Fatal("repeated resource expansion exceeded document budget")
	}
}
func TestFrontendDocumentRejectsChangedArchive(t *testing.T) {
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<div>fixture</div>"})
	m := frontendManifest(t, raw)
	raw[len(raw)-1] ^= 1
	if result, err := BuildFrontendDocument(m, raw); err == nil || result.Content != nil {
		t.Fatal("changed archive compiled")
	}
}
