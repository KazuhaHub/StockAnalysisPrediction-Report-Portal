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
		m.Presentation.Routes = []appplatform.Route{{ID: "editor", Path: "/editor", Layout: "viewport", Query: platformRouteQueryFixture()}, {ID: "home", Path: "/", Layout: "document", Query: platformRouteQueryFixture()}}
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
	testNativeWritableManagedUpdate(
		t, s, actor, endpoint, admitted.InstallationID)
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

func testNativeWritableManagedUpdate(t *testing.T, s *Server, actor platformActorSession, endpoint platformRuntimeEndpoint, installation string) {
	t.Helper()
	ctx := context.Background()
	facts, err := s.observePlatformManagedRuntime(ctx, actor, endpoint)
	if err != nil {
		t.Fatal("managed update runtime", err)
	}
	appID := scalar[string](t, s.st, `SELECT app_id
		FROM app_platform_installations WHERE id=?`, installation)
	raw, archive := platformManagedReleaseFixture(
		t, facts.observation.Profile().Platform)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = appID
	manifest.Version = "1.1.0-beta.2"
	manifest.Backend.Image = os.Getenv("PORTAL_MANAGED_IMAGE")
	manifest.Frontend.SHA256 = platformDigest(archive)
	manifest.Data.Schema = 1
	manifest.Data.ReadableSchemas = []int{1}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(
		actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal("stage managed update", err)
	}
	review, err := s.createPlatformManagedUpdateReview(
		ctx, actor, installation, release.sha256, 1, endpoint)
	if err != nil {
		t.Fatal("managed update review", err)
	}
	var projection platformUpdateProjection
	if err := json.Unmarshal([]byte(review.JSON), &projection); err != nil ||
		projection.Target.Runtime == nil ||
		projection.Target.Runtime.RuntimeID != facts.runtime.id ||
		projection.Target.ReleaseSHA256 != release.sha256 {
		t.Fatal("managed update review projection", err)
	}
	admitted, err := s.consumePlatformManagedUpdateReview(
		ctx, actor, review.ID, review.SHA256,
		platformFixtureID(979), endpoint)
	if err != nil {
		t.Fatal("managed update admission", err)
	}
	if scalar[int](t, s.st, `SELECT managed
		FROM app_platform_operations WHERE id=?`,
		admitted.OperationID) != 1 ||
		scalar[string](t, s.st, `SELECT action||':'||stage
			FROM app_platform_operations WHERE id=?`,
			admitted.OperationID) != "update:queued" {
		t.Fatal("managed update did not create a managed operation")
	}
	selected, err := s.claimNextPlatformManagedUpdate(
		ctx, platformFixtureID(981))
	if err != nil || selected.lease.id != admitted.OperationID ||
		selected.lease.snapshot.Stage != appplatform.StageQueued {
		t.Fatal("managed update queue claim", selected, err)
	}
	lease := selected.lease
	for range 4 {
		lease, err = s.advancePlatformManagedUpdateStage(ctx, lease)
		if err != nil {
			t.Fatal("managed update staging", lease.snapshot.Stage, err)
		}
	}
	if lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("managed update did not reach quiesce", lease.snapshot)
	}
	snapshot, err := s.executePlatformManagedUpdateQuiesce(
		ctx, lease, endpoint)
	if err != nil || snapshot.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("managed update source quiesce", snapshot.snapshot, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET lease_until_ms=1 WHERE id=?`, admitted.OperationID)
	worker, err := newPlatformWritableInstallScheduler(
		s, endpoint, platformFixtureID(980), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	done := make(chan error, 1)
	go func() { done <- worker.serve(workerCtx) }()
	joined := false
	defer func() {
		cancel()
		if !joined {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		stage, code := "", ""
		if err := s.st.queryRow(`SELECT stage,COALESCE(error_code,'')
			FROM app_platform_operations WHERE id=?`,
			admitted.OperationID).Scan(&stage, &code); err != nil {
			t.Fatal(err)
		}
		if stage == "complete" {
			break
		}
		if stage == "recovery_required" || stage == "failed" {
			t.Fatalf("managed update stopped at %s:%s: %+v",
				stage, code, worker.currentStatus())
		}
		select {
		case err := <-done:
			joined = true
			t.Fatalf("managed update worker stopped: %v %+v",
				err, worker.currentStatus())
		case <-workerCtx.Done():
			stage := scalar[string](t, s.st, `SELECT stage
				FROM app_platform_operations WHERE id=?`,
				admitted.OperationID)
			owner := scalar[string](t, s.st, `SELECT COALESCE(lease_owner,'')
				FROM app_platform_operations WHERE id=?`,
				admitted.OperationID)
			t.Fatalf("managed update deadline: stage=%s owner=%s %+v",
				stage, owner, worker.currentStatus())
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-done; err != nil {
		joined = true
		t.Fatal(err)
	}
	joined = true
	if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision
		FROM app_platform_installations WHERE id=?`,
		installation) != "ready:2:2" ||
		scalar[string](t, s.st, `SELECT release_sha256
			FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			installation) != release.sha256 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='retired'
			AND generation=1`, installation) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations
			WHERE id=? AND managed=1 AND stage='complete'
			AND completed_ms IS NOT NULL`, admitted.OperationID) != 1 {
		t.Fatal("managed update did not activate generation two")
	}
	if binding, err := s.inspectPlatformLaunchAccess(
		actor, installation); err != nil ||
		binding.generation != "2" ||
		binding.releaseSHA != release.sha256 {
		t.Fatal("updated managed app did not launch", binding, err)
	}
	t.Log("MANAGED_UPDATE_A_TO_B")
}
