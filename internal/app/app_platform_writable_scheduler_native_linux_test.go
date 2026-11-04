//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The scheduler resumes a side-effect-free staging lease after the first worker
// is lost, then drives the remaining real runner work to completion.
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
	interrupted, err := s.claimNextPlatformWritableInstall(
		ctx, platformFixtureID(970))
	if err != nil || interrupted.lease.id != admitted.OperationID {
		t.Fatal("managed staging interruption claim", interrupted, err)
	}
	for _, stage := range []appplatform.Stage{
		appplatform.StageValidate,
		appplatform.StageDownload,
		appplatform.StageStage,
	} {
		interrupted.lease, err =
			s.st.advancePlatformInstallStaging(interrupted.lease)
		if err != nil || interrupted.lease.snapshot.Stage != stage {
			t.Fatal("managed staging interruption", stage, err)
		}
	}
	t.Log("MANAGED_INSTALL_STAGING_INTERRUPTED")
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET lease_until_ms=0 WHERE id=?`, admitted.OperationID)
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
	if worker.active.id != "" || worker.attempts != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, admitted.InstallationID) != "ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL AND lease_until_ms IS NULL AND error_code IS NULL AND fence=2`, admitted.OperationID) != 1 {
		t.Fatal("background completion retained authority or hid a failure")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state='applied'`, admitted.OperationID) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, admitted.OperationID) != 4 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.install_claimed' AND target_id=?`, admitted.OperationID) != 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.install_review_required' AND target_id=?`, admitted.OperationID) != 0 {
		t.Fatal("background install repeated/recovered an effect")
	}
	t.Log("MANAGED_INSTALL_STAGING_RESTART_RESUMED")
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
	updateOperation, updateRelease := testNativeWritableManagedUpdate(
		t, s, actor, endpoint, admitted.InstallationID)
	savePlatformWritableFixtureOwner(
		t, s, updateOperation, admitted.InstallationID,
		updateRelease, os.Getenv("PORTAL_MANAGED_IMAGE"))
	active, err := s.preparePlatformWritableBackendAccess(
		ctx, actor, admitted.InstallationID)
	if err != nil || active.intent.update == nil ||
		active.intent.update.start.running == nil {
		if err == nil {
			active.Close()
		}
		t.Fatal("updated writable cleanup witness", err)
	}
	updateWitness, err := json.Marshal(platformWritableUpdateStartFixtureMessage{
		Format:    "portal.fixture.writable-update-start/1-dev.1",
		Raw:       active.intent.update.raw,
		Signature: active.intent.update.signature,
		Receipt:   active.intent.update.receipt,
		Running:   *active.intent.update.start.running,
	})
	active.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(os.Getenv("TMPDIR"), "writable-update-start.json"),
		updateWitness, 0600); err != nil {
		t.Fatal(err)
	}
	oldWitness := filepath.Join(
		os.Getenv("TMPDIR"), "writable-auto-start.json")
	if err := os.Rename(
		oldWitness,
		filepath.Join(os.Getenv("TMPDIR"),
			"writable-auto-start.retired.json")); err != nil &&
		!os.IsNotExist(err) {
		t.Fatal(err)
	}
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
	activeRelease := scalar[string](t, s.st, `SELECT release_sha256
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		admitted.InstallationID)
	testNativeWritableLaunchHTTP(
		t, s, actor, admitted.InstallationID, activeRelease, endpoint)
	t.Log("PORTAL_WRITABLE_BACKGROUND_CURRENT_VIEWER")
	mustExec(t, s.st, `UPDATE users SET role=? WHERE username=?`,
		role, actor.username)
	logManagedUpdateState(t, s, updateOperation, admitted.InstallationID)
	rows, err := s.st.query(`SELECT action,stage,completed_ms
		FROM app_platform_operations WHERE installation_id=?
		ORDER BY created_ms,id`, admitted.InstallationID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var action, stage string
		var completed sql.NullInt64
		if err := rows.Scan(&action, &stage, &completed); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		t.Logf("managed update preflight operation: %s/%s completed=%v",
			action, stage, completed)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	failedOperation, _ := testNativeWritableManagedUpdateVersion(
		t, s, actor, endpoint, admitted.InstallationID,
		"1.2.0-beta.3", true)
	testNativeWritableManagedRestore(
		t, s, actor, endpoint, admitted.InstallationID,
		updateRelease, updateOperation, failedOperation)
	testNativeManagedDisableEnable(
		t, s, actor, endpoint, admitted.InstallationID)
}

func assertNativePlatformUserGrant(
	t *testing.T, s *Server, installation, username, stage string,
) {
	t.Helper()
	if count := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_access_grants WHERE installation_id=?
		AND principal_type='user' AND username=?`,
		installation, username); count != 1 {
		t.Fatalf("native managed %s lost the reader grant", stage)
	}
}

func testNativeWritableManagedUpdate(t *testing.T, s *Server, actor platformActorSession, endpoint platformRuntimeEndpoint, installation string) (string, string) {
	return testNativeWritableManagedUpdateVersion(
		t, s, actor, endpoint, installation,
		"1.1.0-beta.2", false)
}

func testNativeWritableManagedUpdateVersion(
	t *testing.T, s *Server, actor platformActorSession,
	endpoint platformRuntimeEndpoint, installation, version string,
	failAtProbe bool,
) (string, string) {
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
	manifest.Version = version
	manifest.Backend.Image = os.Getenv("PORTAL_MANAGED_IMAGE")
	manifest.Capabilities.Required = []string{
		"background.run", "tasks.schedule",
	}
	archive = platformSDKBrowserArchive(t, archive)
	if os.Getenv("APP_SDK_BROWSER_DIR") != "" {
		manifest.Presentation.Routes = []appplatform.Route{
			{ID: "editor", Path: "/editor", Layout: "viewport",
				Query: platformRouteQueryFixture()},
			{ID: "home", Path: "/", Layout: "document",
				Query: platformRouteQueryFixture()},
		}
	}
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
	admissionKey := platformFixtureID(979)
	if failAtProbe {
		admissionKey = platformFixtureID(984)
	}
	admitted, err := s.consumePlatformManagedUpdateReview(
		ctx, actor, review.ID, review.SHA256,
		admissionKey, endpoint)
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
	migrate, err := s.dispatchPlatformCheckpoint(ctx, snapshot, endpoint)
	if err != nil || migrate.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("managed update checkpoint", migrate.snapshot, err)
	}
	verifyNativeCheckpointRecoveryAssociations(
		t, s, admitted.OperationID, installation)
	start, err := s.executePlatformManagedUpdateMigrate(
		ctx, migrate, endpoint)
	if err != nil || start.snapshot.Stage != appplatform.StageStart {
		t.Fatal("managed update migrate", start.snapshot, err)
	}
	probe, err := s.executePlatformUpdateStart(ctx, start, endpoint)
	if err != nil || probe.snapshot.Stage != appplatform.StageProbe {
		logManagedUpdateContainers(t, s, admitted.OperationID)
		t.Fatal("managed update start", probe.snapshot, err)
	}
	if failAtProbe {
		if err := s.finishPlatformManagedUpdateForReview(
			ctx, probe); err != nil {
			t.Fatal("managed update recovery", err)
		}
		t.Log("MANAGED_UPDATE_FAILED_FOR_RESTORE")
		return admitted.OperationID, release.sha256
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
			logManagedUpdateContainers(t, s, admitted.OperationID)
			logManagedUpdateState(t, s, admitted.OperationID, installation)
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
		logManagedUpdateState(t, s, admitted.OperationID, installation)
		t.Fatal("updated managed app did not launch", binding, err)
	}
	testNativeUpdatedBackendTask(t, s, installation, endpoint)
	t.Log("MANAGED_UPDATE_A_TO_B")
	return admitted.OperationID, release.sha256
}

func verifyNativeCheckpointRecoveryAssociations(
	t *testing.T, s *Server, operationID, installation string,
) {
	t.Helper()
	var recoveryID, namespace, checkpointID, release, manifest, epoch string
	var format, writerFence, bytes int64
	var state string
	if err := s.st.queryRow(`
		SELECT id,data_namespace_id,checkpoint_id,release_sha256,data_format,
		       writer_fence,manifest_sha256,bytes,state,controller_epoch
		FROM app_platform_recovery_sets
		WHERE operation_id=? AND installation_id=?`,
		operationID, installation).Scan(
		&recoveryID, &namespace, &checkpointID, &release, &format,
		&writerFence, &manifest, &bytes, &state, &epoch,
	); err != nil {
		t.Fatal("coordinated recovery set", err)
	}
	if !platformValidUUID(recoveryID) || !platformValidUUID(namespace) ||
		!platformValidUUID(checkpointID) || !platformValidSHA(release) ||
		!platformValidSHA(manifest) || !platformValidUUID(epoch) ||
		state != "verified" || format < 1 || writerFence < 1 || bytes <= 0 {
		t.Fatal("coordinated recovery set is incomplete")
	}
	var framework, planRaw, planSHA string
	if err := s.st.queryRow(`
		SELECT p.plan_json,p.plan_sha256,o.controller_epoch
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=?`,
		operationID, installation).Scan(
		&planRaw, &planSHA, &framework,
	); err != nil || platformDigest([]byte(planRaw)) != planSHA ||
		framework != epoch {
		t.Fatal("recovery set did not bind the reviewed operation", err)
	}
	var plan platformManagedUpdatePlan
	runtimeEpoch := ""
	if err := json.Unmarshal([]byte(planRaw), &plan); err == nil &&
		plan.TargetRuntime != nil {
		runtimeEpoch = plan.TargetRuntime.ControllerEpoch
	}
	if json.Unmarshal([]byte(planRaw), &plan) != nil ||
		plan.TargetRuntime == nil ||
		plan.InstallationID != installation ||
		plan.SourceReleaseSHA256 != release ||
		plan.TargetReleaseSHA256 == "" ||
		plan.ControllerEpoch != epoch ||
		plan.TargetRuntime.ControllerEpoch != epoch ||
		plan.DataNamespaceID != namespace ||
		plan.DataFormat != format ||
		plan.SourceWriterFence != writerFence ||
		plan.RecoverySetID != recoveryID ||
		plan.CheckpointOperationID != operationID ||
		plan.CheckpointID != checkpointID ||
		plan.CheckpointManifestSHA256 != manifest ||
		plan.CheckpointArchiveBytes != bytes {
		t.Fatalf("recovery set plan lost code/runtime identity: "+
			"installation=%s/%s source=%s/%s target-empty=%t epoch=%s/%s runtime=%v",
			plan.InstallationID, installation,
			plan.SourceReleaseSHA256, release,
			plan.TargetReleaseSHA256 == "",
			runtimeEpoch, epoch,
			plan.TargetRuntime != nil)
	}
	var runtimeID, fingerprint string
	if err := s.st.queryRow(`
		SELECT i.runtime_id,r.verification_key_fingerprint,r.controller_epoch
		FROM app_platform_installations i
		JOIN app_platform_runtime_bindings r ON r.id=i.runtime_id
		WHERE i.id=?`, installation).Scan(
		&runtimeID, &fingerprint, &framework,
	); err != nil || runtimeID != plan.TargetRuntime.RuntimeID ||
		!platformValidSHA(fingerprint) || framework != epoch {
		t.Fatal("recovery set lost runtime key association", err)
	}
	var sourceRelease, sourceState string
	var sourceGeneration, sourceFormat int64
	if err := s.st.queryRow(`
		SELECT release_sha256,generation,data_format,state
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		installation).Scan(
		&sourceRelease, &sourceGeneration, &sourceFormat, &sourceState,
	); err != nil || sourceRelease != release || sourceState != "active" ||
		sourceFormat != format || sourceGeneration != plan.SourceGeneration {
		t.Fatal("recovery set lost source code/data identity", err)
	}
	var effectID, receiptRaw string
	if err := s.st.queryRow(`
		SELECT effect_id,receipt_json
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND state='applied'`,
		operationID).Scan(&effectID, &receiptRaw); err != nil {
		t.Fatal("checkpoint receipt association", err)
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(receiptRaw))
	if err != nil || plan.CheckpointEffectID != effectID ||
		receipt.EffectID != effectID ||
		receipt.OperationID != operationID ||
		receipt.InstallationID != installation ||
		receipt.NamespaceID != namespace ||
		receipt.CheckpointID != checkpointID ||
		receipt.ReleaseSHA256 != release ||
		receipt.DataFormat != strconv.FormatInt(format, 10) ||
		receipt.WriterFence != strconv.FormatInt(writerFence, 10) ||
		receipt.ManifestSHA256 != manifest ||
		receipt.ArchiveBytes != strconv.FormatInt(bytes, 10) {
		t.Fatal("checkpoint receipt lost recovery-set association", err)
	}
	t.Log("NATIVE_COORDINATED_CHECKPOINT_ASSOCIATIONS_VERIFIED")
}

func testNativeWritableManagedRestore(
	t *testing.T, s *Server, actor platformActorSession,
	endpoint platformRuntimeEndpoint, installation, sourceRelease,
	sourceOperation, failedOperation string,
) {
	t.Helper()
	ctx := context.Background()
	recoverySetID := scalar[string](t, s.st, `SELECT id
		FROM app_platform_recovery_sets
		WHERE operation_id=? AND state='verified'`, failedOperation)
	var operationID string
	var expectedGeneration int64
	if platformLifecycleBrowserConfigured() {
		host := startPlatformLifecycleBrowserHost(t, s, actor, endpoint)
		confirmed := runPlatformLifecycleBrowser(
			t, host, installation, "restore", "confirm", "")
		operationID = confirmed.OperationID
		expectedGeneration = scalar[int64](t, s.st, `SELECT target_generation
			FROM app_platform_operations WHERE id=?`, operationID)
		if scalar[string](t, s.st, `SELECT action||':'||stage
			FROM app_platform_operations WHERE id=?`,
			operationID) != "restore:quiesce" ||
			scalar[int64](t, s.st, `SELECT COALESCE(completed_ms,0)
				FROM app_platform_operations WHERE id=?`,
				operationID) != 0 {
			t.Fatal("browser restore confirmation was not durable")
		}
		t.Log("NATIVE_RESTORE_BROWSER_CONFIRMED")
	} else {
		review, err := s.createPlatformManagedRestoreReview(
			ctx, actor, installation, recoverySetID)
		if err != nil {
			t.Fatal("managed restore review", err)
		}
		commit, err := s.consumePlatformManagedRestoreReview(
			ctx, actor, review.ID, review.SHA256,
			platformFixtureID(982))
		if err != nil || commit.Stage != appplatform.StageQuiesce ||
			commit.OperationID == "" {
			t.Fatal("managed restore admission", commit, err)
		}
		operationID = commit.OperationID
		expectedGeneration = commit.Generation
	}
	worker := platformFixtureID(983)
	selected, err := s.claimNextPlatformManagedRestore(ctx, worker)
	if err != nil || selected.lease.id != operationID {
		t.Fatal("managed restore claim", selected, err)
	}
	current := selected.lease
	witnessPath := filepath.Join(
		os.Getenv("TMPDIR"), "writable-update-start.json")
	if err := os.Remove(witnessPath); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		t.Fatal("clear managed restore witness", err)
	}
	var registrationRaw, registrationSignature []byte
	var runningReceipt journal.UpdateStartRunningReceipt
	for {
		renewed, err := s.st.updatePlatformOperationLeaseContext(
			ctx, current.id, worker, &current)
		if err != nil {
			t.Fatal("managed restore lease renewal",
				current.snapshot.Stage, err)
		}
		current = renewed
		if current.snapshot.Stage == appplatform.StageActivate {
			intent, err := s.preparePlatformRestoreStartRegistration(
				ctx, current)
			if err != nil || intent.start.running == nil {
				t.Fatal("managed restore registration witness", err)
			}
			registrationRaw = bytes.Clone(intent.raw)
			registrationSignature =
				bytes.Clone(intent.signature)
			runningReceipt = *intent.start.running
		}
		stage := current.snapshot.Stage
		next, err := s.runPlatformManagedRestore(
			ctx, current, endpoint)
		if err != nil {
			t.Fatal("managed restore execution",
				stage, err)
		}
		if stage == appplatform.StageActivate {
			var receiptRaw string
			if err := s.st.queryRow(`SELECT receipt_json
				FROM app_platform_operation_effects
				WHERE operation_id=? AND stage='activate'
				AND state='applied'`,
				operationID).Scan(&receiptRaw); err != nil {
				t.Fatal("managed restore registration receipt",
					err)
			}
			receipt, err := journal.ParseUpdateStartRegistrationReceipt(
				[]byte(receiptRaw))
			if err != nil {
				t.Fatal("managed restore registration receipt",
					err)
			}
			witness, err := json.Marshal(
				platformWritableUpdateStartFixtureMessage{
					Format:    "portal.fixture.writable-update-start/1-dev.1",
					Raw:       registrationRaw,
					Signature: registrationSignature,
					Receipt:   receipt,
					Running:   runningReceipt,
				})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				witnessPath, witness, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if next.id == "" {
			break
		}
		current = next
	}
	if scalar[string](t, s.st, `SELECT phase||':'||generation
		FROM app_platform_installations WHERE id=?`,
		installation) != "ready:3" ||
		scalar[string](t, s.st, `SELECT release_sha256
			FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			installation) != sourceRelease ||
		scalar[string](t, s.st, `SELECT stage||':'||COALESCE(error_code,'')
			FROM app_platform_operations WHERE id=?`,
			sourceOperation) != "complete:" ||
		scalar[string](t, s.st, `SELECT stage||':'||error_code
			FROM app_platform_operations WHERE id=?`,
			failedOperation) !=
			"recovery_required:update_recovery_required" ||
		scalar[string](t, s.st, `SELECT stage
			FROM app_platform_operations WHERE id=?`,
			operationID) != "complete" ||
		scalar[int64](t, s.st, `SELECT generation
			FROM app_platform_installations WHERE id=?`,
			installation) != expectedGeneration {
		logManagedUpdateState(t, s, operationID, installation)
		t.Fatal("managed restore did not activate restored source")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce'
		AND state='applied'
		AND json_extract(receipt_json,'$.state') IN ('stopped','absent')`,
		operationID) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='restore_data'
			AND kind='restore' AND state='applied'`,
			operationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage IN ('start','activate')
			AND state='applied'`,
			operationID) != 2 {
		t.Fatal("managed restore effects incomplete")
	}
	savePlatformWritableFixtureOwner(
		t, s, operationID, installation,
		sourceRelease, os.Getenv("PORTAL_MANAGED_IMAGE"))
	if platformLifecycleBrowserConfigured() {
		host := startPlatformLifecycleBrowserHost(t, s, actor, endpoint)
		runPlatformLifecycleBrowser(
			t, host, installation, "restore", "progress", operationID)
		t.Log("NATIVE_RESTORE_BROWSER_COMPLETE")
	}
	t.Log("MANAGED_RESTORE_STOP_REMOVE_TARGET")
	t.Log("MANAGED_RESTORE_DATA_VERIFIED")
	t.Log("MANAGED_RESTORE_START_ACTIVATED")
}

func testNativeManagedDisableEnable(
	t *testing.T, s *Server, actor platformActorSession,
	endpoint platformRuntimeEndpoint, installation string,
) {
	t.Helper()
	ctx := context.Background()
	readerName := "managed-enable-reader"
	if err := s.st.UpsertUser(User{
		Username: readerName, PasswordHash: "fixture", Role: "user",
		Active: true,
	}); err != nil {
		t.Fatal("native managed enable reader", err)
	}
	reader := platformReportSession(t, s, readerName)
	grantID, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{
		"id": grantID, "installation_id": installation,
		"principal_type": "user", "username": reader.username,
		"account_created":    reader.created,
		"account_generation": reader.generation,
		"group_id":           nil, "group_created": nil,
		"revision": 1, "created_ms": 1, "updated_ms": 1,
	})
	generationBefore := scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, installation)
	releaseBefore := scalar[string](t, s.st, `SELECT release_sha256
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, installation)
	grantsBefore := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_access_grants WHERE installation_id=?
		AND principal_type='user' AND username=?`, installation,
		reader.username)
	capabilitiesBefore := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_capability_grants WHERE installation_id=?`,
		installation)
	publisherID := scalar[string](t, s.st, `SELECT publisher_id
		FROM app_platform_installations WHERE id=?`, installation)
	appID := scalar[string](t, s.st, `SELECT app_id
		FROM app_platform_installations WHERE id=?`, installation)
	publisherRevision := scalar[int64](t, s.st, `SELECT revision
		FROM app_platform_publishers WHERE id=?`, publisherID)
	updateReview := createNativeManagedUpdateReview(
		t, s, actor, installation, "1.0.0-beta.90", endpoint)
	revocation, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "policy", publisherID: publisherID,
			expectedRevision: publisherRevision, enabled: false,
			namespaces: []appplatform.PublisherNamespace{{
				Kind: "exact", Value: appID,
			}},
		})
	if err != nil {
		t.Fatal("native package revocation", err)
	}
	if revocation.quarantined < 1 {
		t.Fatal("native package revocation did not quarantine any app",
			revocation)
	}
	disableOperationID := scalar[string](t, s.st,
		`SELECT id FROM app_platform_operations
		 WHERE installation_id=? AND action='disable' AND managed=1
		   AND stage='queued'`,
		installation)
	if disableOperationID == "" {
		t.Fatal("native package revocation did not queue its installation")
	}
	var disabled platformInstallRun
	settled := false
	for attempt := 0; attempt < 64; attempt++ {
		disableClaim, err := s.claimNextPlatformManagedUninstall(
			ctx, platformFixtureID(991+attempt))
		if err != nil {
			t.Fatal("native package revocation disable claim",
				disableClaim, err)
		}
		if disableClaim.lease.id == "" {
			settled = true
			break
		}
		run, err := s.runPlatformManagedUninstall(
			ctx, disableClaim.lease, endpoint)
		if err != nil || run.lease.snapshot.Stage !=
			appplatform.StageComplete {
			t.Fatal("native package revocation stop execution", run, err)
		}
		if disableClaim.lease.installationID == installation {
			if disableClaim.lease.id != disableOperationID {
				t.Fatal("native package revocation claimed the wrong operation",
					disableOperationID, disableClaim.lease.id)
			}
			disabled = run
		}
	}
	if !settled {
		t.Fatal("native package revocation left too many queued stops")
	}
	if disabled.lease.id == "" {
		t.Fatal("native package revocation did not stop its installation")
	}
	assertNativePlatformUserGrant(
		t, s, installation, reader.username, "disable")
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`,
		installation) != "disabled:disabled" ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			installation) != 0 ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`, installation) != "active" ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_access_grants WHERE installation_id=?
			AND principal_type='user' AND username=?`,
			installation, reader.username) != grantsBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_capability_grants WHERE installation_id=?`,
			installation) != capabilitiesBefore {
		t.Fatal("native package revocation lost data or access")
	}
	t.Log("PACKAGE_REVOCATION_MANAGED_STOP_VERIFIED")
	t.Log("MANAGED_DISABLE_STOP_VERIFIED")

	if _, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "policy", publisherID: publisherID,
			expectedRevision: revocation.revision, enabled: true,
			namespaces: []appplatform.PublisherNamespace{
				{Kind: "prefix", Value: "notes-"},
				{Kind: "exact", Value: "scheduled-digest"},
			},
		}); err != nil {
		t.Fatal("restore publisher trust after revocation drill", err)
	}
	facts, err := s.observePlatformManagedRuntime(ctx, actor, endpoint)
	if err != nil {
		t.Fatal("native managed enable runtime", err)
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		t.Fatal("native managed enable target", err)
	}
	enableActor := platformSecondAdminActor(
		t, s, "managed-enable-admin-two")
	enableReview, err := s.createPlatformManagedEnableReviewWithRuntimeContext(
		ctx, enableActor, installation, target, facts.runtime)
	if err != nil {
		t.Fatal("native managed enable review", err)
	}
	enableCommit := raceNativeUpdateAndEnable(
		t, s, actor, enableActor, installation, updateReview,
		enableReview, target, facts.runtime, endpoint)
	t.Log("MANAGED_UPDATE_ENABLE_OPERATOR_RACE")
	enableClaim, err := s.claimNextPlatformManagedEnable(
		ctx, platformFixtureID(993))
	if err != nil || enableClaim.lease.id != enableCommit.OperationID {
		t.Fatal("native managed enable claim", enableClaim, err)
	}
	if _, err := s.runPlatformManagedEnable(
		ctx, enableClaim.lease, endpoint); err != nil {
		t.Fatal("native managed enable execution", err)
	}
	assertNativePlatformUserGrant(
		t, s, installation, reader.username, "enable")
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`,
		installation) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation
			FROM app_platform_installations WHERE id=?`,
			installation) != generationBefore+1 ||
		scalar[string](t, s.st, `SELECT release_sha256
			FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			installation) != releaseBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_access_grants WHERE installation_id=?
			AND principal_type='user' AND username=?`,
			installation, reader.username) != grantsBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_capability_grants WHERE installation_id=?`,
			installation) != capabilitiesBefore {
		t.Fatal("native managed enable changed the retained tuple")
	}
	t.Log("MANAGED_ENABLE_START_PROBE_ACTIVATED")
	if binding, err := s.inspectPlatformLaunchAccess(
		reader, installation); err != nil {
		t.Fatalf("native managed enable launch access %+v: %v",
			binding, err)
	}
	response, err := s.requestPlatformWritableBackend(
		ctx, reader, installation, "GET", "/v1/echo", nil, endpoint)
	if err != nil || response.Status != 200 {
		t.Fatal("native managed enable backend request", response, err)
	}
	t.Log("MANAGED_ENABLE_BACKEND_REQUEST")

	repairUpdateActor := platformSecondAdminActor(
		t, s, "managed-repair-update-admin")
	repairUpdateReview := createNativeManagedUpdateReview(
		t, s, repairUpdateActor, installation,
		"1.0.0-beta.91", endpoint)
	repairDisableReview, err := s.createPlatformRemovalReview(
		actor, installation, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("native repair disable review", err)
	}
	repairDisableCommit, err := s.consumePlatformRemovalReview(
		actor, repairDisableReview.ID, repairDisableReview.SHA256,
		platformFixtureID(994))
	if err != nil {
		t.Fatal("native repair disable admission", err)
	}
	repairDisableClaim, err := s.claimNextPlatformManagedUninstall(
		ctx, platformFixtureID(995))
	if err != nil || repairDisableClaim.lease.id !=
		repairDisableCommit.OperationID {
		t.Fatal("native repair disable claim", repairDisableClaim, err)
	}
	repairDisabled, err := s.runPlatformManagedUninstall(
		ctx, repairDisableClaim.lease, endpoint)
	if err != nil || repairDisabled.lease.snapshot.Stage !=
		appplatform.StageComplete {
		t.Fatal("native repair disable execution", repairDisabled, err)
	}
	repairFacts, err := s.observePlatformManagedRuntime(
		ctx, actor, endpoint)
	if err != nil {
		t.Fatal("native repair runtime", err)
	}
	repairTarget, err := managedInstallTarget(repairFacts, endpoint)
	if err != nil {
		t.Fatal("native repair target", err)
	}
	var repairBrowserHost *platformLifecycleBrowserHost
	admitRepair := func() platformRemovalCommit {
		if platformLifecycleBrowserConfigured() {
			repairBrowserHost = startPlatformLifecycleBrowserHost(
				t, s, actor, endpoint)
			confirmed := runPlatformLifecycleBrowser(
				t, repairBrowserHost, installation, "repair", "confirm", "")
			if scalar[string](t, s.st, `SELECT action
				FROM app_platform_operations WHERE id=?`,
				confirmed.OperationID) != "repair" ||
				scalar[int64](t, s.st, `SELECT COALESCE(completed_ms,0)
					FROM app_platform_operations WHERE id=?`,
					confirmed.OperationID) != 0 {
				t.Fatal("browser repair confirmation was not durable")
			}
			t.Log("NATIVE_REPAIR_BROWSER_CONFIRMED")
			return platformRemovalCommit{
				OperationID: confirmed.OperationID,
				Generation: scalar[int64](t, s.st, `SELECT target_generation
					FROM app_platform_operations WHERE id=?`,
					confirmed.OperationID),
			}
		}
		repairReview, err :=
			s.createPlatformManagedRepairReviewWithRuntimeContext(
				ctx, actor, installation, repairTarget, repairFacts.runtime)
		if err != nil {
			t.Fatal("native repair review", err)
		}
		repairCommit, err :=
			s.consumePlatformManagedRepairReviewWithRuntimeContext(
				ctx, actor, repairReview.ID, repairReview.SHA256,
				platformFixtureID(996), repairTarget, repairFacts.runtime)
		if err != nil {
			t.Fatal("native repair admission", err)
		}
		return repairCommit
	}
	repairCommit := raceNativeUpdateAndRepair(
		t, s, repairUpdateActor, repairUpdateReview, installation,
		admitRepair, endpoint)
	repairOperationID := repairCommit.OperationID
	expectedRepairGeneration := repairCommit.Generation
	t.Log("MANAGED_UPDATE_REPAIR_OPERATOR_RACE")
	repairClaim, err := s.claimNextPlatformManagedRepair(
		ctx, platformFixtureID(997))
	if err != nil || repairClaim.lease.id != repairOperationID {
		t.Fatal("native repair claim", repairClaim, err)
	}
	if _, err := s.runPlatformManagedRepair(
		ctx, repairClaim.lease, endpoint); err != nil {
		t.Fatal("native repair execution", err)
	}
	if repairBrowserHost != nil {
		runPlatformLifecycleBrowser(
			t, repairBrowserHost, installation, "repair", "progress",
			repairOperationID)
		t.Log("NATIVE_REPAIR_BROWSER_COMPLETE")
	}
	assertNativePlatformUserGrant(
		t, s, installation, reader.username, "repair")
	actualState := scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`, installation)
	actualGeneration := scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, installation)
	actualRelease := scalar[string](t, s.st, `SELECT release_sha256
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, installation)
	actualCapabilities := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_capability_grants WHERE installation_id=?`,
		installation)
	if actualState != "enabled:ready" ||
		actualGeneration != expectedRepairGeneration ||
		actualRelease != releaseBefore ||
		actualCapabilities != capabilitiesBefore {
		t.Fatalf("native repair changed the retained tuple state=%s generation=%d/%d release=%s/%s capabilities=%d/%d",
			actualState, actualGeneration, expectedRepairGeneration,
			actualRelease, releaseBefore, actualCapabilities,
			capabilitiesBefore)
	}
	response, err = s.requestPlatformWritableBackend(
		ctx, reader, installation, "GET", "/v1/echo", nil, endpoint)
	if err != nil || response.Status != 200 {
		t.Fatal("native repair backend request", response, err)
	}
	t.Log("MANAGED_REPAIR_START_PROBE_ACTIVATED")
	t.Log("MANAGED_REPAIR_BACKEND_REQUEST")

	deleteDisableReview, err := s.createPlatformRemovalReview(
		actor, installation, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("native delete-data disable review", err)
	}
	deleteDisableCommit, err := s.consumePlatformRemovalReview(
		actor, deleteDisableReview.ID, deleteDisableReview.SHA256,
		platformFixtureID(998))
	if err != nil {
		t.Fatal("native delete-data disable admission", err)
	}
	deleteDisableClaim, err := s.claimNextPlatformManagedUninstall(
		ctx, platformFixtureID(999))
	if err != nil || deleteDisableClaim.lease.id !=
		deleteDisableCommit.OperationID {
		t.Fatal("native delete-data disable claim",
			deleteDisableClaim, err)
	}
	deleteDisabled, err := s.runPlatformManagedUninstall(
		ctx, deleteDisableClaim.lease, endpoint)
	if err != nil || deleteDisabled.lease.snapshot.Stage !=
		appplatform.StageComplete {
		t.Fatal("native delete-data disable execution",
			deleteDisabled, err)
	}
	namespaceID := scalar[string](t, s.st, `SELECT id
		FROM app_platform_data_namespaces
		WHERE origin_installation_id=?`, installation)
	deleteReview, err := s.createPlatformDeleteDataReview(
		ctx, actor, installation, namespaceID)
	if err != nil {
		t.Fatal("native standalone delete-data review", err)
	}
	deleteCommit, err := s.consumePlatformDeleteDataReview(
		actor, deleteReview.ID, deleteReview.SHA256,
		platformFixtureID(1000))
	if err != nil {
		t.Fatal("native standalone delete-data admission", err)
	}
	deleteClaim, err := s.claimNextPlatformDeleteData(
		ctx, platformFixtureID(1001))
	if err != nil || deleteClaim.lease.id != deleteCommit.OperationID {
		t.Fatal("native standalone delete-data claim", deleteClaim, err)
	}
	deleteRun, err := s.runPlatformDeleteData(
		ctx, deleteClaim.lease, endpoint)
	if err != nil || deleteRun.lease.snapshot.Stage !=
		appplatform.StageComplete {
		t.Fatal("native standalone delete-data execution", deleteRun, err)
	}
	actualNamespace := scalar[string](t, s.st, `SELECT state||':'||
		COALESCE(owner_installation_id,'')
		FROM app_platform_data_namespaces WHERE id=?`, namespaceID)
	actualDeleteStage := scalar[string](t, s.st, `SELECT stage
		FROM app_platform_operations WHERE id=?`,
		deleteCommit.OperationID)
	if actualNamespace != "deleted:" ||
		actualDeleteStage != "complete" {
		t.Fatalf("native standalone delete-data did not settle namespace=%s stage=%s",
			actualNamespace, actualDeleteStage)
	}
	t.Log("STANDALONE_DELETE_DATA_NAMESPACE_ABSENT")
}

func createNativeManagedUpdateReview(
	t *testing.T, s *Server, actor platformActorSession,
	installation, version string, endpoint platformRuntimeEndpoint,
) platformRemovalReview {
	t.Helper()
	var descriptor, frontend []byte
	if err := s.st.queryRow(`SELECT r.descriptor,f.content
		FROM app_platform_activations a
		JOIN app_platform_releases r ON r.sha256=a.release_sha256
		JOIN app_platform_frontend_artifacts f ON f.sha256=r.frontend_sha256
		WHERE a.installation_id=? AND a.state='active'`,
		installation).Scan(&descriptor, &frontend); err != nil {
		t.Fatal("native update source release", err)
	}
	var manifest appplatform.Manifest
	if err := json.Unmarshal(descriptor, &manifest); err != nil ||
		manifest.Backend == nil {
		t.Fatal("native update descriptor", err)
	}
	manifest.Version = version
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(
		actor, raw, platformSignTestDescriptor(t, raw), frontend)
	if err != nil {
		t.Fatal("native update target release", err)
	}
	review, err := s.createPlatformManagedUpdateReview(
		context.Background(), actor, installation,
		release.sha256, release.trustRevision, endpoint)
	if err != nil {
		t.Fatal("native managed update review", err)
	}
	return review
}

func raceNativeUpdateAndEnable(
	t *testing.T, s *Server,
	updateActor, enableActor platformActorSession,
	installation string,
	updateReview, enableReview platformRemovalReview,
	target *platformManagedInstallTarget,
	runtime platformRuntimeRegistration,
	endpoint platformRuntimeEndpoint,
) platformRemovalCommit {
	t.Helper()
	type outcome struct {
		enable bool
		err    error
		commit platformRemovalCommit
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := s.consumePlatformManagedUpdateReview(
			context.Background(), updateActor, updateReview.ID,
			updateReview.SHA256, platformFixtureID(992), endpoint)
		results <- outcome{err: err}
	})
	wg.Go(func() {
		commit, err := s.consumePlatformManagedEnableReviewWithRuntimeContext(
			context.Background(), enableActor, enableReview.ID,
			enableReview.SHA256, platformFixtureID(993),
			target, runtime)
		results <- outcome{enable: true, err: err, commit: commit}
	})
	wg.Wait()
	close(results)
	var enableCommit platformRemovalCommit
	for result := range results {
		if result.enable {
			if result.err != nil || result.commit.OperationID == "" {
				t.Fatal("native update/enable winner", result.err)
			}
			enableCommit = result.commit
		} else if !errors.Is(result.err, errAppPlatformPlanChanged) &&
			!errors.Is(result.err, errAppPlatformOperationConflict) {
			t.Fatal("native update contender", result.err)
		}
	}
	pending := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_operations WHERE installation_id=?
		AND action IN ('update','enable') AND completed_ms IS NULL`,
		installation)
	consumed := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_review_plans WHERE id IN (?,?)
		AND consumed_ms IS NOT NULL`, updateReview.ID, enableReview.ID)
	if pending != 1 || consumed != 1 {
		t.Fatal("native update/enable race", pending, consumed)
	}
	return enableCommit
}

func raceNativeUpdateAndRepair(
	t *testing.T, s *Server, updateActor platformActorSession,
	updateReview platformRemovalReview, installation string,
	admitRepair func() platformRemovalCommit,
	endpoint platformRuntimeEndpoint,
) platformRemovalCommit {
	t.Helper()
	updateResult := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := s.consumePlatformManagedUpdateReview(
			context.Background(), updateActor, updateReview.ID,
			updateReview.SHA256, platformFixtureID(996), endpoint)
		updateResult <- err
	}()
	<-started
	repairCommit := admitRepair()
	if repairCommit.OperationID == "" {
		t.Fatal("native repair contender had no operation")
	}
	updateErr := <-updateResult
	if !errors.Is(updateErr, errAppPlatformPlanChanged) &&
		!errors.Is(updateErr, errAppPlatformOperationConflict) {
		t.Fatal("native repair winner update contender", updateErr)
	}
	pending := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_operations WHERE installation_id=?
		AND action IN ('update','repair') AND completed_ms IS NULL`,
		installation)
	consumed := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL`,
		updateReview.ID)
	if pending != 1 || consumed != 0 {
		t.Fatal("native update/repair race", pending, consumed)
	}
	return repairCommit
}

func logManagedUpdateState(t *testing.T, s *Server, operationID, installationID string) {
	t.Helper()
	stage, code, revision, generation, fence := "", "", int64(0), int64(0), int64(0)
	if err := s.st.queryRow(`SELECT stage,COALESCE(error_code,''),state_revision,
		target_generation,fence
		FROM app_platform_operations WHERE id=?`,
		operationID).Scan(&stage, &code, &revision, &generation, &fence); err != nil {
		t.Logf("managed update operation state unavailable: %v", err)
		return
	}
	t.Logf("managed update operation: stage=%s code=%s revision=%d target=%d fence=%d",
		stage, code, revision, generation, fence)
	phase, installGeneration, installPolicy, installFence := "", int64(0), int64(0), int64(0)
	if err := s.st.queryRow(`SELECT phase,generation,policy_revision,control_fence
		FROM app_platform_installations WHERE id=?`,
		installationID).Scan(&phase, &installGeneration, &installPolicy, &installFence); err == nil {
		t.Logf("managed update installation: phase=%s generation=%d policy=%d fence=%d",
			phase, installGeneration, installPolicy, installFence)
	} else {
		t.Logf("managed update installation unavailable: %v", err)
	}
	rows, err := s.st.query(`SELECT stage,state,effect_id,observed_fence
		FROM app_platform_operation_effects
		WHERE operation_id=? ORDER BY created_ms,stage`,
		operationID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			effectStage, state, effectID := "", "", ""
			var observed int64
			if rows.Scan(&effectStage, &state, &effectID, &observed) == nil {
				t.Logf("managed update effect: stage=%s state=%s id=%s observed=%d",
					effectStage, state, effectID, observed)
			}
		}
	} else {
		t.Logf("managed update effects unavailable: %v", err)
	}
	rows, err = s.st.query(`SELECT generation,state,
		CASE WHEN readiness_evidence='{}' THEN 'empty' ELSE 'set' END,
		COALESCE(observed_ms,0)
		FROM app_platform_activations
		WHERE installation_id=? ORDER BY generation`,
		installationID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var generation, observed int64
			activationState, readiness := "", ""
			if rows.Scan(&generation, &activationState, &readiness, &observed) == nil {
				t.Logf("managed update activation: generation=%d state=%s readiness=%s observed=%d",
					generation, activationState, readiness, observed)
			}
		}
	} else {
		t.Logf("managed update activations unavailable: %v", err)
	}
	rows, err = s.st.query(`SELECT capability,granted,policy_revision,review_plan_id
		FROM app_platform_capability_grants WHERE installation_id=?
		ORDER BY capability`, installationID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			capability, review := "", ""
			var granted, policy int64
			if rows.Scan(&capability, &granted, &policy, &review) == nil {
				t.Logf("managed update capability: capability=%s granted=%d policy=%d review=%s",
					capability, granted, policy, review)
			}
		}
	} else {
		t.Logf("managed update capabilities unavailable: %v", err)
	}
	action, format, review := "", "", ""
	if err := s.st.queryRow(`SELECT o.action,
		COALESCE(json_extract(p.plan_json,'$.format'),''),p.id
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`, operationID).Scan(&action, &format, &review); err == nil {
		t.Logf("managed update review: action=%s format=%s id=%s",
			action, format, review)
	} else {
		t.Logf("managed update review unavailable: %v", err)
	}
}

func logManagedUpdateContainers(t *testing.T, s *Server, operationID string) {
	t.Helper()
	effectID := scalar[string](t, s.st, `SELECT effect_id
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start'`, operationID)
	if effectID == "" {
		return
	}
	label := "org.portal.app.start-effect=" + effectID
	out, err := exec.Command(
		"docker", "ps", "-a", "--filter", "label="+label,
		"--format", "{{.ID}}\t{{.Status}}").CombinedOutput()
	t.Logf("managed update containers: %s (%v)",
		strings.TrimSpace(string(out)), err)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, _, _ := strings.Cut(line, "\t")
		if id == "" {
			continue
		}
		logs, logErr := exec.Command(
			"docker", "logs", "--tail", "40", id).CombinedOutput()
		t.Logf("managed update container %s logs (%v):\n%s",
			id, logErr, strings.TrimSpace(string(logs)))
	}
}
