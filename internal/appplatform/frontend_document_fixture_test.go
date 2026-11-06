package appplatform

import (
	"os"
	"path/filepath"
	"testing"
)

// Explicit development fixture export only. Never used by startup or a market.
// Supply a disposable output filename that does not already exist.
func TestFrontendDocumentBrowserFixture(t *testing.T) {
	input := os.Getenv("APP_FRONTEND_BROWSER_INPUT_DIR")
	output := os.Getenv("APP_FRONTEND_BROWSER_OUTPUT")
	if input == "" || output == "" {
		t.Skip("browser fixture input/output not requested")
	}
	script, err := os.ReadFile(filepath.Join(input, "app.js"))
	if err != nil {
		t.Fatal(err)
	}
	css, err := os.ReadFile(filepath.Join(input, "style.css"))
	if err != nil {
		t.Fatal(err)
	}
	if len(script) > MaxFrontendFileBytes || len(css) > MaxFrontendFileBytes {
		t.Fatal("fixture build is oversized")
	}
	raw := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: `<!doctype html><html><head><meta charset="utf-8"><link rel="stylesheet" href="style.css"></head><body><div id="app"></div><script src="app.js"></script></body></html>`}, frontendTestEntry{name: "app.js", body: string(script)}, frontendTestEntry{name: "style.css", body: string(css)})
	result, err := BuildFrontendDocument(frontendManifest(t, raw), raw)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(result.Content); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	policy, err := os.OpenFile(output+".csp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.WriteString(result.CSP); err != nil {
		policy.Close()
		t.Fatal(err)
	}
	if err := policy.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("Document %s; source %s; response CSP: %s", result.DocumentSHA256, result.SourceSHA256, result.CSP)
}
