//go:build linux

package driver

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestDockerAcceptanceBackendImage(t *testing.T) {
	socket := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_SOCKET")
	if socket == "" {
		t.Skip("explicit disposable Linux Docker acceptance socket not set")
	}
	reference := os.Getenv("PORTAL_APP_RUNNER_ACCEPTANCE_IMAGE")
	if reference == "" {
		t.Fatal("explicit digest-pinned disposable image required")
	}
	raw, _, _ := managedImageFixture(t)
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	backend := manifest["backend"].(map[string]any)
	backend["image"], backend["platforms"] = reference, []string{"linux/" + runtime.GOARCH}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectBackendImage(context.Background(), socket, raw, parsed.SHA256)
	if err != nil || got.ReleaseSHA256 != parsed.SHA256 || got.Reference != reference || !nativeImageID.MatchString(got.ImageID) || got.Platform != "linux/"+runtime.GOARCH {
		t.Fatalf("native image observation: %+v %v", got, err)
	}
	// This real-daemon observation grants no installation/start authority.
	t.Logf("verified local immutable image for %s", got.Platform)
}
