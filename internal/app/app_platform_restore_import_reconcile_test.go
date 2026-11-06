package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// dispatchLost leaves an import effect behind whose first answer was lost, then takes the operation
// over under a new lease, which raises the installation fence above the one the effect was admitted at.
func dispatchLostAndTakeOver(t *testing.T, f *platformImportFixtureState, operation string) (lease platformOperationLease, admitted int64) {
	t.Helper()
	f.executor.err = errors.New("lost reply")
	if _, err := f.drive(t, operation); err == nil {
		t.Fatal("a lost reply did not surface")
	}
	f.executor.err = nil
	admitted = scalar[int64](t, f.s.st, `SELECT admitted_fence FROM app_platform_operation_effects WHERE operation_id=?`, operation)
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, operation)
	lease, err := f.s.st.claimPlatformOperation(operation, platformFixtureID(5310))
	if err != nil {
		t.Fatal(err)
	}
	if lease.fence <= admitted {
		t.Fatalf("takeover did not raise the fence: %d <= %d", lease.fence, admitted)
	}
	return lease, admitted
}

func TestRestoreImportTakeoverSettlesViaReconcile(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	lease, admitted := dispatchLostAndTakeOver(t, f, commit.OperationID)
	effect := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID)
	done, err := f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture())
	if err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the takeover did not settle the original effect: %v", err)
	}
	if f.executor.calls != 1 || f.executor.reads != 1 {
		t.Fatalf("takeover must read the receipt, not execute again: calls=%d reads=%d", f.executor.calls, f.executor.reads)
	}
	if f.executor.last.command.EffectID != effect || f.executor.last.command.InstallationFence != itoa(admitted) {
		t.Fatalf("the effect was re-signed under a new identity or fence: %+v", f.executor.last.command)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != 1 ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "applied" ||
		scalar[int64](t, f.s.st, `SELECT admitted_fence FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != admitted ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "consumed" {
		t.Fatal("the original effect was not settled and its grant consumed")
	}
}

func TestRestoreImportDefiniteNoRowRevokesGrantAndAllowsRetry(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	lease, _ := dispatchLostAndTakeOver(t, f, commit.OperationID)
	f.executor.absent = true
	_, err := f.s.runPlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture())
	if !errors.Is(err, errPlatformImportEnded) {
		t.Fatalf("a definite no-row answer must end the operation: %v", err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "revoked" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "reverted" {
		t.Fatal("no-row did not fail the operation, revoke the grant and revert the effect")
	}
	f.executor.absent = false
	review, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet)
	if err != nil {
		t.Fatal("retry review refused:", err)
	}
	retry, err := f.s.consumePlatformRestoreImportReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5211))
	if err != nil {
		t.Fatal(err)
	}
	if done, err := f.drive(t, retry.OperationID); err != nil || done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the retry did not complete: %v", err)
	}
}

func TestRestoreImportUnknownStaysUnknownAndIsReRead(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	lease, _ := dispatchLostAndTakeOver(t, f, commit.OperationID)
	f.executor.readErr = errors.New("endpoint unreachable")
	for i := 1; i <= 2; i++ {
		_, outcome, err := f.s.reconcilePlatformRestoreImport(context.Background(), lease, platformRestoreEndpointFixture())
		if err != nil || outcome != platformImportUnknown || f.executor.reads != i {
			t.Fatalf("round %d: outcome=%q err=%v reads=%d", i, outcome, err, f.executor.reads)
		}
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != 1 ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != "prepared" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_import_grants`) != "issued" {
		t.Fatal("an unknown effect changed state, identity or grant")
	}
}
