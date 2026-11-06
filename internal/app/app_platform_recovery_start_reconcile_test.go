package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// platformStartReadFake stands in for the runner's restore-start read-receipt. It never executes
// anything: the reconcile path has only this one call.
type platformStartReadFake struct {
	calls   int
	last    platformUpdateStartIntent
	absent  bool
	err     error
	applied bool
}

func (f *platformStartReadFake) read(_ context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
	f.calls++
	f.last = intent
	// The runner verifies the read purpose: only the read signature is a read, and it is not an execute.
	if _, err := control.VerifyUpdateStartRead(intent.raw, intent.readSignature, intent.authority, time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	if _, err := control.VerifyUpdateStartRead(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli()); err == nil {
		return nil, errors.New("the execute signature verifies as a read")
	}
	if _, err := control.VerifyUpdateStart(intent.raw, intent.readSignature, intent.authority, time.Now().UnixMilli()); err == nil {
		return nil, errors.New("the read signature verifies as an execute")
	}
	switch {
	case f.err != nil:
		return nil, f.err
	case f.absent:
		return nil, errPlatformStartAbsent
	case f.applied:
		return platformUpdateStartRunningFixture(intent, *intent.reservation, endpoint), nil
	}
	return nil, errors.New("runner has not settled the start")
}

// unknownResumeStartFixture leaves a resume whose start was reserved and then lost: the execute reply never
// arrived, so the operation ended recovery_required with its start effect unknown.
func unknownResumeStartFixture(t *testing.T) (*platformResumeFixtureState, string, *platformStartReadFake) {
	t.Helper()
	ctx := context.Background()
	f := platformResumeFixture(t, false)
	commit := f.reviewAndConsumeResume(t)
	lease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	intent, err := f.s.preparePlatformRestoreStart(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	reservation := platformUpdateStartReservationFixture(intent, f.endpoint).reservation
	reserved, err := f.s.commitPlatformRestoreStartReservation(ctx, lease, intent, f.endpoint, reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformRestoreResumeForReview(ctx, reserved.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "recovery_required" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, commit.OperationID) != "unknown" {
		t.Fatal("fixture: the resume did not end recovery_required with an unknown start")
	}
	fake := &platformStartReadFake{}
	f.s.platformReadStartReceipt = fake.read
	return f, commit.OperationID, fake
}

// While the Portal is paused, a resume whose start is unknown is settled by reading the ORIGINAL start
// effect back at the fence it was admitted under: an applied receipt settles the effect and puts the
// operation back to the probe stage on the lease it needs, and the unchanged stage code then takes the
// installation to ready.
func TestRecoveryReconcileSettlesUnknownResumeStart(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.applied = true
	effect := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation)
	admitted := scalar[int64](t, f.s.st, `SELECT admitted_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation)
	revision := scalar[int64](t, f.s.st, `SELECT state_revision FROM app_platform_operations WHERE id=?`, operation)
	selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640))
	if err != nil || selected.lease.id != operation {
		t.Fatalf("reconcile: %+v %v", selected, err)
	}
	if fake.calls != 1 || fake.last.effectID != effect || fake.last.command.InstallationFence != itoa(admitted) || fake.last.command.Kind != "restore-start" {
		t.Fatalf("the original effect was not read at its stored identity and fence: calls=%d %+v", fake.calls, fake.last.command)
	}
	lease := selected.lease
	if lease.snapshot.Stage != appplatform.StageProbe || int64(lease.snapshot.Revision) < revision || lease.fence != admitted || lease.owner != platformFixtureID(5640) {
		t.Fatalf("the settled start did not return the operation to probe on its own fence: %+v", lease)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "applied" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND stage='probe' AND completed_ms IS NULL AND error_code IS NULL`, operation) != 1 {
		t.Fatal("an applied receipt did not settle the effect and reopen the operation")
	}
	if done := platformResumeProbeToReady(t, f.s, lease, f.endpoint); done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the reopened resume did not complete")
	}
	if scalar[string](t, f.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation) != "ready" {
		t.Fatal("the installation is not ready")
	}
	if again, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil || again.lease.id != "" || fake.calls != 1 {
		t.Fatalf("a settled resume was reconciled again: %+v %v calls=%d", again, err, fake.calls)
	}
}

func TestRecoveryReconcileKeepsAnUnknownStartAnswer(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.err = errors.New("endpoint unreachable")
	selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640))
	if err != nil || selected.lease.id != "" {
		t.Fatalf("an unreachable runner settled something: %+v %v", selected, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "unknown" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "recovery_required" {
		t.Fatal("an unknown answer changed the effect or the operation")
	}
}

// A reserved start is one the runner itself acknowledged, so an absence reply for it means the runner's
// journal lost the row. That is a journal-integrity failure: the effect stays unknown, the operation stays
// recovery_required, the installation is not returned to review and no new resume is admitted, however
// long ago every signed command expired. It is audited once and settled only by abandoning the runtime.
func TestRecoveryReconcileTreatsAnAbsentReservedStartAsALostJournal(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.absent = true
	generation := scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	// Every window is past: the operation ended long ago and the recorded read expired.
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=1 WHERE id=?`, operation)
	for i := 0; i < 2; i++ {
		raw := scalar[string](t, f.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation)
		if reservation, _, ok := platformStartRecoveryReceipt(raw); ok {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='start'`, platformStartPendingReceipt(reservation, 1), operation)
		}
		if selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil || selected.lease.id != "" {
			t.Fatalf("reconcile: %+v %v", selected, err)
		}
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "unknown" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "recovery_required" ||
		scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "enabled:recovery_required" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, f.installation) != 1 ||
		scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation) != generation {
		t.Fatal("an absent reserved start was reverted")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_start_journal_lost' AND target_id=?`, operation) != 1 {
		t.Fatal("the lost journal row was not audited exactly once")
	}
	if !platformOperationBlocksLaunch(t, f.s, operation) {
		t.Fatal("a lost start no longer blocks launch")
	}
	if _, err := f.s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, f.target()); err == nil {
		t.Fatal("a new resume was admitted over a start whose journal row is lost")
	}
}

// A start that never recorded a reservation was never admitted by the runner, so there is nothing to
// read: ending the resume returns the installation to review at once, and a new resume can be admitted.
func TestResumeEndWithoutReservationReturnsToReviewAndAllowsARetry(t *testing.T) {
	ctx := context.Background()
	f := platformResumeFixture(t, false)
	commit := f.reviewAndConsumeResume(t)
	lease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	if _, err := f.s.preparePlatformRestoreStart(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformRestoreResumeForReview(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "disabled:review_required" {
		t.Fatal("a start without a reservation was not returned to review")
	}
	review, err := f.s.createPlatformRestoreResumeReview(ctx, f.actor, f.installation, f.target())
	if err != nil {
		t.Fatal("a new resume review was refused:", err)
	}
	retry, err := f.s.consumePlatformRestoreResumeReview(ctx, f.actor, review.ID, review.SHA256, platformFixtureID(5641))
	if err != nil {
		t.Fatal(err)
	}
	start := f.driveResume(t, retry.OperationID, appplatform.StageStart)
	if done := platformResumeStartToReady(t, f.s, start, f.endpoint); done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the retried resume did not complete")
	}
}

// The scheduler settles such a start by itself while the Portal is paused and it has nothing else to run.
func TestRecoveryReconcileResumeStartRunsFromTheSchedulerWhenIdle(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.applied = true
	worker, err := newPlatformWritableInstallScheduler(f.s, f.endpoint, platformFixtureID(5642), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = worker.tick(context.Background())
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "applied" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND stage<>'recovery_required' AND completed_ms IS NULL`, operation) != 1 {
		t.Fatal("an idle scheduler did not settle the unknown start and take the resume on")
	}
	if worker.active.id != operation {
		t.Fatalf("the scheduler does not hold the reopened resume: %+v", worker.active)
	}
}

// A resume the reconcile cannot continue is left as it is: when the installation's fence moved since the
// operation held it, the stored fence no longer matches, so even an applied answer settles nothing and the
// start stays unknown.
func TestRecoveryReconcileRefusesAnInstallationFenceThatMoved(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.applied = true
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, f.installation)
	selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640))
	if err != nil || selected.lease.id != "" {
		t.Fatalf("a moved fence was continued: %+v %v", selected, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "unknown" {
		t.Fatal("the effect was settled although the operation could not continue")
	}
}
