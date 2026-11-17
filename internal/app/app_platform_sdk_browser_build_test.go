package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestSDKBrowserArchiveDocument(t *testing.T) {
	dir := os.Getenv("APP_SDK_BROWSER_BUILD_DIR")
	if dir == "" {
		t.Skip("explicit SDK browser build fixture only")
	}
	archive, err := os.ReadFile(filepath.Join(dir, "frontend.zip"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := platformSignedReleaseFixture(t, "1.0.0-beta.3")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Frontend.SHA256 = platformDigest(archive)
	document, err := appplatform.BuildFrontendDocument(manifest, archive)
	if err != nil || document.SourceSHA256 != manifest.Frontend.SHA256 || len(document.Content) == 0 || strings.Contains(document.CSP, "allow-same-origin") || !strings.Contains(document.CSP, "connect-src 'none'") {
		t.Fatal("actual SDK bundle did not compile into isolated document", err)
	}
}
