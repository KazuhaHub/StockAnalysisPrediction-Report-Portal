package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func TestPlatformNotificationWriterFixtureStages(t *testing.T) {
	s, session := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, session)
	raw, archive := platformManagedReleaseFixture(t, "linux/amd64")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = "notes-writer-notify-demo"
	manifest.Version = "1.0.0-beta.4"
	manifest.Capabilities.Required = []string{"network.http", "notifications.publish", "reports.read", "secrets.use", "storage.files"}
	manifest.Capabilities.Optional = nil
	if dir := os.Getenv("APP_SDK_BROWSER_DIR"); dir != "" {
		if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
			t.Fatal("invalid SDK browser fixture directory")
		}
		file, err := os.Open(filepath.Join(dir, "frontend.zip"))
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		archive, err = io.ReadAll(io.LimitReader(file, int64(appplatform.MaxFrontendArchiveBytes)+1))
		if err != nil || len(archive) > appplatform.MaxFrontendArchiveBytes {
			t.Fatal("SDK browser archive unavailable or oversized", err)
		}
	}
	manifest.Frontend.SHA256 = platformDigest(archive)
	manifest.Backend = &appplatform.Backend{Kind: "oci", Image: "registry.k8s.io/pause@sha256:" + strings.Repeat("a", 64), Platforms: []string{"linux/amd64"}, Protocol: transport.ManagedBackendProtocol, Resources: appplatform.Resources{MemoryMiB: 256, CPUMillis: 500, PIDs: 64}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal("notification fixture descriptor", err)
	}
	if _, err := appplatform.InspectFrontend(parsed.Manifest, archive); err != nil {
		t.Fatal("notification fixture frontend", err)
	}
	signature := platformSignTestDescriptor(t, raw)
	release, err := s.stagePlatformRelease(session, raw, signature, archive)
	if err != nil {
		t.Fatal("notification fixture release", err)
	}
	if release.appID != manifest.ID || release.publisherID != manifest.Publisher {
		t.Fatal("notification fixture release identity changed")
	}
}
