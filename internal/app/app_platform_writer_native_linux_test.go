//go:build linux && (amd64 || arm64)

package app

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Runs inside the existing non-root Portal fixture against its real root managed
// endpoint. No native evidence or private signing key is injected into Portal RPC.
func testNativePortalWritableWriter(t *testing.T, s *Server, session platformActorSession, endpoint platformRuntimeEndpoint) {
	t.Helper()
	ctx := context.Background()
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if facts.observation.Profile().WritableSandboxProfile != control.WritableCandidateSandboxProfile || facts.observation.Profile().ExecutionReady {
		t.Fatal("writable adapter missing or claimed readiness")
	}
	raw, archive := platformManagedReleaseFixture(t, facts.observation.Profile().Platform)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = "notes-writer-demo"
	manifest.Backend.Image = os.Getenv("PORTAL_MANAGED_IMAGE")
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal("stage writable package", err)
	}
	plan, err := s.createPlatformWritableInstallReview(ctx, session, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: session.username}}, endpoint)
	if err != nil {
		t.Fatal("writable review", err)
	}
	var p platformInstallProjection
	if err := json.Unmarshal([]byte(plan.JSON), &p); err != nil || p.Format != platformWritableInstallFormat || p.Runtime == nil || p.Runtime.Profile.WritableSandboxProfile != control.WritableCandidateSandboxProfile {
		t.Fatal("review omitted writable contract", err)
	}
	if _, err := s.consumePlatformManagedInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(890), endpoint); err == nil {
		t.Fatal("readonly consumer adopted writable review")
	}
	admitted, err := s.consumePlatformWritableInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(890), endpoint)
	if err != nil {
		t.Fatal("writable consumption", err)
	}
	replay, err := s.consumePlatformWritableInstallReview(ctx, session, plan.ID, plan.SHA256, platformFixtureID(890), endpoint)
	if err != nil || !replay.Replayed || replay.OperationID != admitted.OperationID {
		t.Fatal("writable admission replay", err)
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(891))
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []appplatform.Stage{appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage, appplatform.StagePrepareData} {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil || lease.snapshot.Stage != stage {
			t.Fatal("writable staging", stage, err)
		}
	}
	lease, err = s.dispatchPlatformInstallData(ctx, lease, endpoint)
	if err != nil || lease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("writable prepared data", err)
	}
	if _, err := s.preparePlatformInstallCandidate(ctx, lease); err == nil {
		t.Fatal("readonly candidate adopted writable review")
	}
	intent, err := s.preparePlatformInstallWriter(ctx, lease)
	if err != nil {
		t.Fatal("Portal writer intent", err)
	}
	again, err := s.preparePlatformInstallWriter(ctx, lease)
	if err != nil || !samePlatformWriterIntent(intent, again) {
		t.Fatal("writer changed before dispatch", err)
	}
	if _, err := s.reconcilePlatformInstallWriter(ctx, lease, endpoint); err == nil {
		t.Fatal("unattempted writer recovered as physical success")
	}
	t.Log("PORTAL_WRITABLE_REVIEW_CONFIRMED")
	// Force Portal rollback after root permissions and runner receipt have settled.
	mustExec(t, s.st, `CREATE TRIGGER portal_writer_settlement_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.writer_observed' BEGIN SELECT RAISE(ABORT,'writer settlement fixture'); END`)
	if _, err := s.dispatchPlatformInstallWriter(ctx, lease, endpoint); err == nil {
		t.Fatal("Portal writer settlement trigger ignored")
	}
	if scalar[int](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != 2 || scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND effect_number=2`, lease.id) != "prepared" {
		t.Fatal("failed Portal commit partially settled writer")
	}
	observed, err := platformRunWriter(ctx, endpoint, intent, transport.WritableWriterRead)
	if err != nil {
		t.Fatal("exact native writer result after Portal rollback", err)
	}
	before, err := observed.WriterReceipt()
	if err != nil || before.Claim.ResourceID != intent.resourceID {
		t.Fatal("writer receipt lost future process identity", err)
	}
	mustExec(t, s.st, `DROP TRIGGER portal_writer_settlement_failure`)
	completed, err := s.completePlatformInstallWriter(ctx, lease, endpoint)
	if err != nil {
		t.Fatal("Portal writer reconciliation", err)
	}
	if completed.rowRevision != lease.rowRevision || completed.snapshot.Stage != appplatform.StageStart || completed.fence != lease.fence {
		t.Fatal("writer completion advanced operation/process stage")
	}
	final, err := s.preparePlatformInstallWriter(ctx, completed)
	if err != nil || !final.applied || final.receipt != before || final.resourceID != intent.resourceID || final.effectID != intent.effectID || final.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("writer reconciliation replaced source/result", err)
	}
	if _, err := s.dispatchPlatformInstallWriter(ctx, completed, endpoint); err != nil {
		t.Fatal("applied Portal writer replay", err)
	}
	if scalar[int](t, s.st, `SELECT revision FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != 3 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 2 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.writer_observed' AND target_id=?`, lease.id) != 1 {
		t.Fatal("writer effect/namespace not atomic or replay not idempotent")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state<>'candidate'`, lease.installationID) != 0 || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "pending" {
		t.Fatal("writer granted app access")
	}
	t.Log("PORTAL_WRITER_SETTLEMENT_RECONCILED")
	t.Log("PORTAL_WRITER_RETAINED_IDENTITY")
	testNativePortalWritableStart(t, s, completed, endpoint, final)
}
