package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
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

// One installation is imported at most once per controller epoch, whichever recovery set was used.
func TestRestoreImportRefusedAfterAnyImportInTheEpoch(t *testing.T) {
	t.Run("consumed grant on another recovery set", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		mustExec(t, f.s.st, `INSERT INTO app_platform_recovery_sets(id,installation_id,data_namespace_id,operation_id,release_sha256,data_format,checkpoint_id,manifest_sha256,bytes,state,controller_epoch,writer_fence,verified_ms,deleted_ms,created_ms,updated_ms)
			SELECT ?,installation_id,data_namespace_id,operation_id,release_sha256,data_format,?,manifest_sha256,bytes,state,controller_epoch,writer_fence,verified_ms,deleted_ms,created_ms,updated_ms FROM app_platform_recovery_sets WHERE id=?`,
			platformFixtureID(5600), platformFixtureID(5601), f.recoverySet)
		epoch := f.s.st.GetSetting("app_platform_controller_epoch", "")
		mustExec(t, f.s.st, `INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'consumed',7,1,1)`,
			platformFixtureID(5602), platformFixtureID(5600), f.replacement.id, epoch)
		if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
			t.Fatal("an installation already imported from another recovery set was admitted")
		}
	})
	t.Run("completed import without a consumed grant", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		commit := f.reviewAndConsume(t)
		if done, err := f.drive(t, commit.OperationID); err != nil || done.snapshot.Stage != appplatform.StageComplete {
			t.Fatal(err)
		}
		mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked',consumed_ms=NULL`)
		if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
			t.Fatal("an installation with a completed import was admitted again")
		}
	})
}

// The runner bounds a bulk data action at two minutes, so the attempt that drives it must outlive that
// bound, and still end before its lease does.
func TestRestoreImportAttemptOutlivesTheRunnerBound(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	worker, err := newPlatformWritableInstallScheduler(f.s, platformRestoreEndpointFixture(), platformFixtureID(5302), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8 && f.executor.calls == 0; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal(i, err)
		}
	}
	if f.executor.calls != 1 {
		t.Fatal("the import never reached the runner")
	}
	leaseBound := time.Duration(appPlatformLeaseMS) * time.Millisecond
	if f.executor.deadline < 2*time.Minute+5*time.Second || f.executor.deadline >= leaseBound {
		t.Fatalf("the import attempt had %s; it must exceed the runner's two-minute bound and fit the %s lease", f.executor.deadline, leaseBound)
	}
	_ = commit
}

// A runner that is still busy with the import has not failed the attempt.
func TestRestoreImportBusyRunnerIsNotAFailedAttempt(t *testing.T) {
	f := platformRestoreImportFixture(t, false)
	commit := f.reviewAndConsume(t)
	worker, err := newPlatformWritableInstallScheduler(f.s, platformRestoreEndpointFixture(), platformFixtureID(5303), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	f.executor.err, f.executor.readErr = errPlatformImportBusy, errPlatformImportBusy
	for i := 0; i < platformInstallAttemptLimit*3; i++ {
		_ = worker.tick(context.Background())
	}
	if worker.attempts != 0 || scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "restore_data" {
		t.Fatalf("a busy runner counted as a failed attempt: attempts=%d stage=%s", worker.attempts, scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID))
	}
	f.executor.err, f.executor.readErr = nil, nil
	for i := 0; i < 4 && scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" {
		t.Fatal("the import did not complete once the runner was free")
	}
}

// A review is refused while the installation holds any live claim on an import: an issued grant on any
// of its recovery sets, or an import effect the runner has not settled.
func TestRestoreImportRefusedWhileAnyGrantOrEffectIsLive(t *testing.T) {
	t.Run("issued grant on another recovery set", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		mustExec(t, f.s.st, `INSERT INTO app_platform_recovery_sets(id,installation_id,data_namespace_id,operation_id,release_sha256,data_format,checkpoint_id,manifest_sha256,bytes,state,controller_epoch,writer_fence,verified_ms,deleted_ms,created_ms,updated_ms)
			SELECT ?,installation_id,data_namespace_id,operation_id,release_sha256,data_format,?,manifest_sha256,bytes,state,controller_epoch,writer_fence,verified_ms,deleted_ms,created_ms,updated_ms FROM app_platform_recovery_sets WHERE id=?`,
			platformFixtureID(5610), platformFixtureID(5611), f.recoverySet)
		mustExec(t, f.s.st, `INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,'issued',NULL,1,1)`,
			platformFixtureID(5612), platformFixtureID(5610), f.replacement.id, f.s.st.GetSetting("app_platform_controller_epoch", ""))
		if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
			t.Fatal("a review was admitted beside an issued grant on another recovery set")
		}
	})
	t.Run("unsettled import effect without a grant", func(t *testing.T) {
		f := platformRestoreImportFixture(t, false)
		commit := f.reviewAndConsume(t)
		f.executor.err = errors.New("lost reply")
		lease, err := f.drive(t, commit.OperationID)
		if err == nil {
			t.Fatal("a lost reply did not surface")
		}
		if err := f.s.finishPlatformRestoreImportForReview(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked'`)
		if _, err := f.s.createPlatformRestoreImportReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
			t.Fatal("a review was admitted beside an unsettled import effect")
		}
	})
}

// The runner lets a claimed import run past the connection that carried it, up to a bound sized from
// the archive. The Portal polls by read-receipt, so its polling window must cover the longest bound.
func TestRestoreImportPollingWindowCoversTheRunnerBound(t *testing.T) {
	window := time.Duration(platformBusyWaitLimit) * platformImportAttemptTimeout
	if bound := journal.ImportCheckpointBound(512 << 20); window < bound {
		t.Fatalf("the Portal polls for %s but a claimed import may run for %s", window, bound)
	}
	if platformImportAttemptTimeout >= time.Duration(appPlatformLeaseMS)*time.Millisecond {
		t.Fatal("an import attempt must end inside the operation lease")
	}
}
