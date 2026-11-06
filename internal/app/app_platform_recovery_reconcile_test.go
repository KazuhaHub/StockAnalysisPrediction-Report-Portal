package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// unknownImportFixture leaves a restore import whose first answer was lost and that the scheduler then
// gave up on: the operation is recovery_required, its effect unknown and its grant still issued.
func unknownImportFixture(t *testing.T) (*platformImportFixtureState, string) {
	t.Helper()
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	f.executor.err = errors.New("lost reply")
	lease, err := f.drive(t, commit.OperationID)
	if err == nil {
		t.Fatal("a lost reply did not surface")
	}
	f.executor.err = nil
	if err := f.s.finishPlatformRestoreImportForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "recovery_required" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "unknown" {
		t.Fatal("fixture: the import did not end recovery_required with an unknown effect")
	}
	return f, commit.OperationID
}

// While the Portal is paused, an import the scheduler gave up on is settled by reading the original
// effect back from the runner: an applied receipt consumes the grant and completes the operation, so the
// installation can be resumed.
func TestRecoveryReconcileSettlesCompletedUnknownImport(t *testing.T) {
	f, operation := unknownImportFixture(t)
	effect := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=?`, operation)
	admitted := scalar[int64](t, f.s.st, `SELECT admitted_fence FROM app_platform_operation_effects WHERE operation_id=?`, operation)
	settled, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture())
	if err != nil || settled != 1 {
		t.Fatalf("reconcile: settled=%d err=%v", settled, err)
	}
	if f.executor.reads < 1 {
		t.Fatal("the effect was not read back")
	}
	if f.executor.last.command.EffectID != effect || f.executor.last.command.InstallationFence != itoa(admitted) {
		t.Fatalf("the original effect was not read at its stored identity and fence: %+v", f.executor.last.command)
	}
	if f.executor.calls != 1 {
		// The first call was the lost Execute; reconcile must only read.
		t.Fatalf("reconcile executed the import again: calls=%d", f.executor.calls)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "applied" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "consumed" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "complete" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND error_code IS NULL AND completed_ms IS NOT NULL`, operation) != 1 {
		t.Fatal("an applied receipt did not settle the effect, the grant and the operation")
	}
	if _, err := loadPlatformResumeImport(f.s.st.poolExec(), f.installation); err != nil {
		t.Fatalf("a reconciled import is not a resume source: %v", err)
	}
	if again, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture()); err != nil || again != 0 {
		t.Fatalf("a settled import was reconciled again: %d %v", again, err)
	}
}

func TestRecoveryReconcileKeepsAnUnknownAnswer(t *testing.T) {
	f, operation := unknownImportFixture(t)
	f.executor.readErr = errors.New("endpoint unreachable")
	if settled, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture()); err != nil || settled != 0 {
		t.Fatalf("an unreachable runner settled something: %d %v", settled, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "unknown" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "issued" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "recovery_required" {
		t.Fatal("an unknown answer changed the effect, the grant or the operation")
	}
}

// A definite absence settles the import only once every command signed for the effect has expired.
func TestRecoveryReconcileRevertsADefiniteAbsenceAfterExpiry(t *testing.T) {
	f, operation := unknownImportFixture(t)
	f.executor.absent = true
	if settled, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture()); err != nil || settled != 0 {
		t.Fatalf("an absence inside the signed window was taken as final: %d %v", settled, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "unknown" {
		t.Fatal("an unexpired absence reverted the effect")
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json='{"signed_until_ms":1}' WHERE operation_id=?`, operation)
	if settled, err := f.s.reconcilePlatformRecoveryImports(context.Background(), platformRestoreEndpointFixture()); err != nil || settled != 1 {
		t.Fatalf("an expired absence did not settle: %d %v", settled, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, operation) != "reverted" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" {
		t.Fatal("a definite absence did not revert the effect and revoke the grant")
	}
	f.executor.absent = false
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("a new import review was refused after the absence settled:", err)
	}
	retry, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5611))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.drive(t, retry.OperationID); err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the retry did not complete: %v", err)
	}
}

// The scheduler settles such imports by itself while the Portal is paused and it has nothing else to run.
func TestRecoveryReconcileRunsFromTheSchedulerWhenIdle(t *testing.T) {
	f, operation := unknownImportFixture(t)
	worker, err := newPlatformWritableInstallScheduler(f.s, platformRestoreEndpointFixture(), platformFixtureID(5620), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "complete" {
		t.Fatal("an idle scheduler did not settle the unknown import")
	}
}
