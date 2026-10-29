//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Fresh operation: no stage calls, fault injection, manually claimed lease or
// candidate preparation advances this installation. Only serve drives it.
func testNativeWritableBackgroundCompletion(t *testing.T, s *Server, actor platformActorSession, endpoint platformRuntimeEndpoint) {
	t.Helper()
	ctx := context.Background()
	facts, err := s.observePlatformManagedRuntime(ctx, actor, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	raw, archive := platformManagedReleaseFixture(t, facts.observation.Profile().Platform)
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.ID = "notes-background-demo"
	archive = platformSDKBrowserArchive(t, archive)
	if os.Getenv("APP_SDK_BROWSER_DIR") != "" {
		m.Presentation.Routes = []appplatform.Route{{ID: "editor", Path: "/editor", Layout: "viewport"}, {ID: "home", Path: "/", Layout: "document"}}
	}
	m.Frontend.SHA256 = platformDigest(archive)
	m.Backend.Image = os.Getenv("PORTAL_MANAGED_IMAGE")
	raw, _ = json.Marshal(m)
	release, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal("stage background package", err)
	}
	plan, err := s.createPlatformWritableInstallReview(ctx, actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}}, endpoint)
	if err != nil {
		t.Fatal("review background install", err)
	}
	admitted, err := s.consumePlatformWritableInstallReview(ctx, actor, plan.ID, plan.SHA256, platformFixtureID(975), endpoint)
	if err != nil {
		t.Fatal("admit background install", err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, admitted.OperationID) != "queued" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, admitted.OperationID) != 0 {
		t.Fatal("background fixture did not start clean")
	}
	savePlatformWritableFixtureOwner(t, s, admitted.OperationID, admitted.InstallationID, release.sha256, m.Backend.Image)
	worker, err := newPlatformWritableInstallScheduler(s, endpoint, platformFixtureID(976), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	done := make(chan error, 1)
	go func() { done <- worker.serve(workerCtx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			if err := <-done; err != nil {
				t.Error("stop background completion worker", err)
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, admitted.OperationID) != "complete" {
		select {
		case err := <-done:
			joined = true
			t.Fatal("background install stopped early", err, worker.currentStatus())
		case <-workerCtx.Done():
			t.Fatal("background install deadline", worker.currentStatus())
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-done; err != nil {
		joined = true
		t.Fatal(err)
	}
	joined = true
	if worker.active.id != "" || worker.attempts != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, admitted.InstallationID) != "ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL AND lease_until_ms IS NULL AND error_code IS NULL AND fence=1`, admitted.OperationID) != 1 {
		t.Fatal("background completion retained authority or hid a failure")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state='applied'`, admitted.OperationID) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, admitted.OperationID) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.install_claimed' AND target_id=?`, admitted.OperationID) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.install_review_required' AND target_id=?`, admitted.OperationID) != 0 {
		t.Fatal("background install repeated/recovered an effect")
	}
	access, err := s.preparePlatformWritableBackendAccess(ctx, actor, admitted.InstallationID)
	if err != nil {
		t.Fatal("completed background history", err)
	}
	witness, err := json.Marshal(platformWritableStartFixtureMessage{Raw: access.intent.start.raw, Signature: access.intent.start.signature, Receipt: access.intent.start.running, WriterReceipt: access.intent.start.writer.receipt})
	access.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("TMPDIR"), "writable-auto-start.json"), witness, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("PORTAL_WRITABLE_BACKGROUND_COMPLETE")
	// Current normal-user authorization, not possession of the installer role.
	role := scalar[string](t, s.st, `SELECT role FROM users WHERE username=?`, actor.username)
	defer mustExec(t, s.st, `UPDATE users SET role=? WHERE username=?`, role, actor.username)
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	for _, method := range []string{"POST", "GET"} {
		var body []byte
		if method == "POST" {
			body = []byte(`{"role":"admin","subject":"forged"}`)
		}
		r, err := s.requestPlatformWritableBackend(ctx, actor, admitted.InstallationID, method, "/v1/echo", body, endpoint)
		var got struct {
			Count  int64           `json:"count"`
			Viewer protocol.Viewer `json:"viewer"`
		}
		if err != nil || r.Status != 200 || json.Unmarshal(r.Body, &got) != nil || got.Count != 1 || got.Viewer.Role != "user" || len(got.Viewer.Subject) != 64 {
			t.Fatal("background app did not serve one current authorized write", method, err)
		}
	}
	testNativeWritableLaunchHTTP(t, s, actor, admitted.InstallationID, release.sha256, endpoint)
	t.Log("PORTAL_WRITABLE_BACKGROUND_CURRENT_VIEWER")
}
