package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
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

// A definite absence settles the start only once every command signed for the effect has expired: the
// original commands (none later than the end of the operation) and the one signed for this read. Then
// the resume is returned to review and a new resume can be admitted.
func TestRecoveryReconcileRevertsAnAbsentStartAfterExpiryAndAllowsARetry(t *testing.T) {
	f, operation, fake := unknownResumeStartFixture(t)
	fake.absent = true
	generation := scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	if selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil || selected.lease.id != "" {
		t.Fatalf("an absence inside the signed window was taken as final: %+v %v", selected, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "unknown" {
		t.Fatal("an unexpired absence reverted the effect")
	}
	// Both windows are past: the operation ended long ago and the read's own command has expired.
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=1 WHERE id=?`, operation)
	raw := scalar[string](t, f.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation)
	reservation, _, ok := platformStartRecoveryReceipt(raw)
	if !ok {
		t.Fatalf("the signed read was not recorded on the effect: %s", raw)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='start'`, platformStartPendingReceipt(reservation, 1), operation)
	if selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil || selected.lease.id != "" {
		t.Fatalf("reconcile: %+v %v", selected, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "reverted" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "failed" ||
		scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "disabled:review_required" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, f.installation) != 0 ||
		scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation) != generation {
		t.Fatal("a definite absence did not revert the effect and return the installation to review")
	}
	if platformOperationBlocksLaunch(t, f.s, operation) {
		t.Fatal("a reverted resume still blocks launch")
	}
	// A new resume is admitted and reaches ready.
	review, err := f.s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, f.target())
	if err != nil {
		t.Fatal("a new resume review was refused after the absence settled:", err)
	}
	retry, err := f.s.consumePlatformRestoreResumeReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5641))
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
