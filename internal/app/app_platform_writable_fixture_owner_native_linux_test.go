//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func savePlatformWritableFixtureOwner(t *testing.T, s *Server, operation, installation, release, image string) {
	t.Helper()
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	e := s.st.poolExec()
	var epoch string
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	r, seed, err := s.platformDataRuntime(e, epoch, dek)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(seed)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	now := time.Now().UnixMilli()
	p := platformWritableFixtureOwner{"portal.fixture.writable-install-owner/1-dev.1", r.deploymentID, epoch, operation, installation, release, image, r.controlRevision, now, now + 90000}
	raw, _ := json.Marshal(p)
	encoded, _ := json.Marshal(platformWritableFixtureOwnerEnvelope{raw, ed25519.Sign(key, platformWritableFixtureOwnerFrame(raw))})
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "writable-auto-owner.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

// Root uses the completed signed start whenever available. The operation-only
// fallback is cleanup for an interrupted test, not successful-install evidence.
func checkPlatformWritableAutoFixture(t *testing.T, dir string, a control.Authority, socket string, native *driver.CandidateRuntime, cleanup bool) {
	t.Helper()
	encoded, err := os.ReadFile(filepath.Join(dir, "writable-auto-owner.json"))
	if os.IsNotExist(err) && cleanup {
		return
	}
	if err != nil {
		t.Fatal("automatic install owner missing", err)
	}
	p, err := verifyPlatformWritableFixtureOwner(encoded, a, os.Getenv("APP_READY_NATIVE_IMAGE"), time.Now().UnixMilli())
	if err != nil {
		t.Fatal("automatic install owner refused", err)
	}
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", append([]string{"--host", "unix://" + socket}, args...)...).CombinedOutput()
		if err != nil || len(out) > 65536 {
			t.Fatal("automatic fixture cleanup inspection", err)
		}
		return out
	}
	inspectImage := func() string {
		t.Helper()
		id := string(bytes.TrimSpace(run("image", "inspect", p.Image, "--format", "{{.Id}}")))
		if len(id) != 71 || id[:7] != "sha256:" || !platformValidSHA(id[7:]) {
			t.Fatal("invalid automatic fixture image identity")
		}
		return id
	}
	path := filepath.Join(dir, "writable-auto-start.json")
	completed, err := os.ReadFile(path)
	if err == nil {
		var m platformWritableStartFixtureMessage
		var c control.WritableCandidateCommand
		var w control.DataWriterCommand
		var ref control.Command
		if len(completed) > 32768 || json.Unmarshal(completed, &m) != nil || json.Unmarshal(m.Raw, &c) != nil || json.Unmarshal(c.WriterRaw, &w) != nil || json.Unmarshal(w.DataRaw, &ref) != nil || ref.OperationID != p.OperationID || ref.InstallationID != p.InstallationID || ref.Payload.ReleaseSHA256 != p.ReleaseSHA {
			t.Fatal("automatic start witness owner changed")
		}
		if inspectImage() != m.WriterReceipt.Native.ImageID {
			t.Fatal("automatic completed image changed")
		}
		checkPlatformWritableStartFixture(t, path, a, socket, native, cleanup)
		return
	}
	if !cleanup || !os.IsNotExist(err) {
		t.Fatal("automatic install did not produce completed start witness", err)
	}
	// No completed source: remove at most one container of this pre-authorized
	// unique operation, after verifying all immutable ownership labels and image.
	ids := bytes.Fields(run("container", "ls", "-a", "--no-trunc", "--filter", "label=org.portal.app.operation="+p.OperationID, "--format", "{{.ID}}"))
	if len(ids) == 0 {
		return
	}
	if len(ids) != 1 || !platformValidSHA(string(ids[0])) {
		t.Fatal("ambiguous automatic fixture ownership")
	}
	id := string(ids[0])
	expectedImageID := inspectImage()
	var rows []struct {
		ID, Name, Image, Created string
		Config                   struct {
			Image  string
			Labels map[string]string
		}
	}
	if json.Unmarshal(run("container", "inspect", id), &rows) != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Config.Image != expectedImageID {
		t.Fatal("automatic fixture container changed")
	}
	l := rows[0].Config.Labels
	for name, value := range map[string]string{"format": "writable-candidate/1-dev.1", "sandbox-profile": control.WritableCandidateSandboxProfile, "deployment": p.DeploymentID, "epoch": p.Epoch, "operation": p.OperationID, "installation": p.InstallationID, "release": p.ReleaseSHA, "generation": "1", "control-fence": "1", "writer-fence": "1"} {
		if l["org.portal.app."+name] != value {
			t.Fatal("foreign automatic fixture label", name)
		}
	}
	for _, name := range []string{"namespace", "resource", "start-effect", "start-claim"} {
		if !platformValidUUID(l["org.portal.app."+name]) {
			t.Fatal("invalid automatic fixture identity", name)
		}
	}
	for _, name := range []string{"data-receipt", "writer-receipt", "start-payload", "start-ownership"} {
		if !platformValidSHA(l["org.portal.app."+name]) {
			t.Fatal("invalid automatic fixture digest", name)
		}
	}
	resource := l["org.portal.app.resource"]
	created, err := time.Parse(time.RFC3339Nano, rows[0].Created)
	if err != nil || created.UnixMilli() < p.IssuedMS || created.UnixMilli() > p.ExpiresMS || rows[0].Name != "/portal-app-"+resource || expectedImageID != rows[0].Image {
		t.Fatal("automatic fixture time/image/name changed")
	}
	run("container", "stop", "--time", "10", id)
	run("container", "rm", id)
	identity := driver.CandidateIdentity{DeploymentID: p.DeploymentID, ControllerEpoch: p.Epoch, InstallationID: p.InstallationID, OperationID: p.OperationID, ResourceID: resource, NamespaceID: l["org.portal.app.namespace"], ReleaseSHA256: p.ReleaseSHA, Generation: "1", ControlFence: "1", WriterFence: "1"}
	if err := native.ReleaseCandidateHTTPStorage(context.Background(), identity); err != nil {
		t.Error("automatic fixture IPC cleanup", err)
	}
	t.Log("PORTAL_WRITABLE_BACKGROUND_FALLBACK_CLEANUP")
}
