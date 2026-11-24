package app

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformCheckpointRefusedObservation is the runner's typed answer to a refused checkpoint: a failed status for the
// command instead of a receipt.
type platformCheckpointRefusedObservation struct {
	platformCheckpointTestObservation
	status *journal.MigrateDataStatus
}

func (o platformCheckpointRefusedObservation) DataEffectStatus() (journal.MigrateDataStatus, error) {
	if o.status == nil {
		return journal.MigrateDataStatus{}, journal.ErrFenceReconciliation
	}
	return *o.status, nil
}

// platformSnapshotStageFixture is a managed update whose source was quiesced and whose snapshot is the next effect:
// the state the production update wedged in.
func platformSnapshotStageFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, lease, quiesce, _ := platformManagedUpdateQuiesceFixture(t, pg)
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), lease, quiesce, platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil || snapshot.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("snapshot stage", snapshot.snapshot, err)
	}
	return s, snapshot
}

func platformRefusedCheckpoint(t *testing.T, intent platformCheckpointIntent, code string) platformCheckpointRefusedObservation {
	t.Helper()
	failure := &journal.DataEffectFailedError{Header: journal.CheckpointCommandHeader(intent.command), Code: code}
	status := failure.Status(time.Now().UTC().UnixMilli())
	return platformCheckpointRefusedObservation{
		platformCheckpointTestObservation: platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture()),
		status:                            &status,
	}
}

// The runner's typed refusal of a checkpoint is a definite failure of the stage: it is recognised for exactly this
// command and never committed as a receipt.
func TestPlatformCheckpointCommitRecognisesTheRunnersTypedRefusal(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	intent, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformCheckpointEndpointFixture()
	for _, code := range []string{"checkpoint_unsafe", "checkpoint_too_large", "checkpoint_unsettled"} {
		_, err = s.commitPlatformCheckpoint(context.Background(), snapshot, intent, endpoint, platformRefusedCheckpoint(t, intent, code))
		var failed *platformDataEffectFailedError
		if !errors.Is(err, errPlatformDataEffectFailed) || !errors.As(err, &failed) || failed.code != code {
			t.Fatal("a typed refusal was not a definite failure", code, err)
		}
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, snapshot.id) != "snapshot" {
		t.Fatal("a refusal advanced the stage or settled the effect as applied")
	}
	// A refusal that is not bound to this command is not recognised; it is stage evidence that does not hold.
	other := platformRefusedCheckpoint(t, intent, "checkpoint_unsafe")
	other.status.EffectID = platformFixtureID(4)
	if _, err = s.commitPlatformCheckpoint(context.Background(), snapshot, intent, endpoint, other); errors.Is(err, errPlatformDataEffectFailed) || !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatal("a refusal of another effect was accepted", err)
	}
}

// A managed update whose snapshot is refused ends for review at once, from the snapshot stage, and the installation
// keeps its source generation: the update is never retried against a refusal that answers the same every time.
func TestPlatformManagedUpdateWithRefusedSnapshotEndsFailedWithSourceKept(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	intent, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, snapshot.installationID)
	w, err := newPlatformWritableInstallScheduler(s, platformManagedRemovalFixtureEndpoint(), platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	calls := 0
	w.runOverride = func(ctx context.Context, lease platformOperationLease) (platformInstallRun, error) {
		calls++
		_, err := s.commitPlatformCheckpoint(ctx, lease, intent, platformCheckpointEndpointFixture(), platformRefusedCheckpoint(t, intent, "checkpoint_unsafe"))
		return platformInstallRun{}, err
	}
	if err := w.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || w.active.id != "" || w.currentStatus().code != "review_required" {
		t.Fatalf("calls=%d status=%+v", calls, w.currentStatus())
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != "recovery_required:checkpoint_unsafe" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, snapshot.installationID) != generation ||
		scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, snapshot.installationID) != "recovery_required" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL`, snapshot.id) != 1 {
		t.Fatal("the update did not end for review with its source generation kept")
	}
}

// An operation that was admitted and then wedged at the snapshot stage (its review plan expired, its fence moved on
// with every lease it lost) is finished, not looped on.
func TestPlatformManagedUpdateWedgedAtSnapshotIsClaimedAndFinished(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=1 WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, snapshot.id)
	var reclaimed platformOperationLease
	for i := range 3 {
		mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, snapshot.id)
		selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6130+i))
		if err != nil || selected.lease.id != snapshot.id || selected.lease.snapshot.Stage != appplatform.StageSnapshot {
			t.Fatal("an admitted operation was not reclaimed after its review plan expired", i, selected, err)
		}
		reclaimed = selected.lease
	}
	if reclaimed.fence < snapshot.fence+3 {
		t.Fatal("the fence did not move on", reclaimed.fence, snapshot.fence)
	}
	// The checkpoint intent written at the old fence is still the operation's own effect.
	if _, err := s.preparePlatformCheckpoint(context.Background(), reclaimed); err != nil {
		t.Fatal("a drifted fence refused the admitted checkpoint intent", err)
	}
	if scalar[int64](t, s.st, `SELECT admitted_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id) != reclaimed.fence {
		t.Fatal("the intent was not carried to the current fence")
	}
	if err := s.finishPlatformUpdateForReview(context.Background(), reclaimed); err != nil {
		t.Fatal("the wedged operation could not be finished", err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, snapshot.id) != "recovery_required" ||
		scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, snapshot.installationID) != "recovery_required" {
		t.Fatal("the wedged operation did not end for review")
	}
}

// platformExpireReview makes the review plan of the operation expired, and its lease free to claim.
func platformExpireReview(t *testing.T, s *Server, operation string) {
	t.Helper()
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=1 WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, operation)
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=? AND lease_owner IS NOT NULL`, operation)
}

// platformExpectPlanExpired expects the operation to have ended failed with plan_expired, with nothing to settle, and
// the installation left as it was.
func platformExpectPlanExpired(t *testing.T, s *Server, operation, installation string) {
	t.Helper()
	if scalar[string](t, s.st, `SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, operation) != "failed:plan_expired" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL`, operation) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, operation) != 0 ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" {
		t.Fatal("the expired update did not end failed with plan_expired and nothing else")
	}
}

// Expiry of the review plan blocks only a new start, and a new start whose plan expired ends: it is failed with
// plan_expired, with no effects to settle, instead of being skipped for ever.
func TestPlatformManagedUpdateNotYetStartedEndsOnceItsReviewPlanExpired(t *testing.T) {
	s, operation := platformManagedUpdateOperationFixture(t, false)
	platformExpireReview(t, s, operation.id)
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6140))
	if err != nil || selected.lease.id != "" {
		t.Fatal("a new start was admitted after its review plan expired", selected, err)
	}
	platformExpectPlanExpired(t, s, operation.id, operation.installationID)
	// It stays ended: nothing claims it again.
	if selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6141)); err != nil || selected.lease.id != "" {
		t.Fatal("an ended update was claimed again", selected, err)
	}
}

// platformUpdateAtQuiesceWithoutEffect takes an update to the quiesce stage without preparing the stop: nothing
// was sent, so nothing is stopped.
func platformUpdateAtQuiesceWithoutEffect(t *testing.T) (*Server, platformOperationLease) {
	t.Helper()
	s, operation := platformManagedUpdateOperationFixture(t, false)
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6120))
	if err != nil || selected.lease.id != operation.id {
		t.Fatal("claim", selected, err)
	}
	lease := selected.lease
	for range 4 {
		if lease, err = s.advancePlatformManagedUpdateStage(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	if lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("not at quiesce", lease.snapshot)
	}
	return s, lease
}

// An update at the quiesce stage with no effect row has stopped nothing: an expired review plan ends it there.
func TestPlatformManagedUpdateAtQuiesceWithoutAnEffectEndsOnceItsReviewPlanExpired(t *testing.T) {
	s, lease := platformUpdateAtQuiesceWithoutEffect(t)
	platformExpireReview(t, s, lease.id)
	if selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6170)); err != nil || selected.lease.id != "" {
		t.Fatal("an update that stopped nothing was carried on after its review plan expired", selected, err)
	}
	platformExpectPlanExpired(t, s, lease.id, lease.installationID)
}

// Once a quiesce effect row exists, in any state, the stop may have been sent: the update is admitted whatever the
// clock says, and carried on.
func TestPlatformManagedUpdateAtQuiesceWithAnEffectIsClaimedAfterItsReviewPlanExpired(t *testing.T) {
	s, lease, _, _ := platformManagedUpdateQuiesceFixture(t, false)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce'`, lease.id) != 1 {
		t.Fatal("the fixture holds no quiesce effect")
	}
	platformExpireReview(t, s, lease.id)
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6171))
	if err != nil || selected.lease.id != lease.id || selected.lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("a quiesce whose stop may have been sent was not carried on", selected, err)
	}
	// And it carries on: its stop intent is prepared again under the expired plan.
	if _, err := s.preparePlatformManagedUpdateQuiesce(context.Background(), selected.lease); err != nil {
		t.Fatal("an admitted update at quiesce could not carry on", err)
	}
}

// One operation that cannot be admitted does not stop the claim of another: it is skipped and logged.
func TestPlatformManagedUpdateClaimSkipsAFailingOperation(t *testing.T) {
	s, first := platformManagedUpdateOperationFixture(t, false)
	// An unrelated installation with its own queued update, cloned from the first and queued just after it.
	otherInstallation, otherPlan, otherOperation := platformFixtureID(6149), platformFixtureID(6150), platformFixtureID(6151)
	mustExec(t, s.st, `INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms)
		SELECT ?,'com.example.otherclaim',publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms
		FROM app_platform_installations WHERE id=?`, otherInstallation, first.installationID)
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,?,app_id,publisher_id,'update',source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, otherPlan, otherInstallation, first.id)
	mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		SELECT ?,?,?,actor_scope,?,?,'update',managed,source_generation,target_generation,stage,state_revision,row_revision,NULL,NULL,fence,controller_epoch,NULL,NULL,created_ms+1,updated_ms
		FROM app_platform_operations WHERE id=?`, otherOperation, otherInstallation, otherPlan, platformDigest([]byte(otherOperation)), platformDigest([]byte(otherPlan)), first.id)
	second := platformOperationLease{id: otherOperation}
	real := platformAdmitManagedUpdate
	resetPlatformManagedUpdateSkipLog()
	t.Cleanup(func() { platformAdmitManagedUpdate = real; resetPlatformManagedUpdateSkipLog() })
	platformAdmitManagedUpdate = func(s *Server, e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) (platformAdmittedManagedUpdate, error) {
		if lease.id == first.id {
			return platformAdmittedManagedUpdate{}, errAppPlatformStageEvidence
		}
		return platformAdmittedManagedUpdate{target: &platformManagedInstallTarget{RuntimeID: platformFixtureID(1)}}, nil
	}
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6152))
	if err != nil || selected.lease.id != second.id {
		t.Fatal("a failing operation stopped the claim of the next one", selected, err)
	}
	if !strings.Contains(logged.String(), first.id) {
		t.Fatalf("the skipped operation was not logged: %q", logged.String())
	}
	if scalar[string](t, s.st, `SELECT COALESCE(lease_owner,'') FROM app_platform_operations WHERE id=?`, first.id) != "" {
		t.Fatal("the skipped operation was leased")
	}
	// With nothing else to claim, a failing operation is skipped without an error that would starve the other claims.
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, second.id)
	platformAdmitManagedUpdate = func(*Server, migExec, platformOperationLease, appplatform.PublisherTrust, int64) (platformAdmittedManagedUpdate, error) {
		return platformAdmittedManagedUpdate{}, errAppPlatformStageEvidence
	}
	if selected, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6153)); err != nil || selected.lease.id != "" {
		t.Fatal("every operation failing admission must leave the claim idle, not failed", selected, err)
	}
	// A database or driver error is not about one operation: it is returned, never skipped (on PostgreSQL the
	// transaction is already aborted, and every later operation would fail for a reason that is not theirs).
	broken := errors.New("driver: bad connection")
	platformAdmitManagedUpdate = func(*Server, migExec, platformOperationLease, appplatform.PublisherTrust, int64) (platformAdmittedManagedUpdate, error) {
		return platformAdmittedManagedUpdate{}, broken
	}
	if _, err := s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(6154)); !errors.Is(err, broken) {
		t.Fatal("a driver error was skipped", err)
	}
}

// The log of skipped operations is bounded: it starts over past its size instead of growing with every operation.
func TestPlatformManagedUpdateSkipLogIsBounded(t *testing.T) {
	resetPlatformManagedUpdateSkipLog()
	t.Cleanup(resetPlatformManagedUpdateSkipLog)
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	for i := range 3 * platformManagedUpdateSkipLogLimit {
		logPlatformManagedUpdateSkipped(platformFixtureID(20000+i), errAppPlatformStageEvidence)
	}
	platformManagedUpdateSkipLog.Lock()
	size := len(platformManagedUpdateSkipLog.reasons)
	platformManagedUpdateSkipLog.Unlock()
	if size > platformManagedUpdateSkipLogLimit {
		t.Fatal("the skip log grew past its bound", size)
	}
	before := strings.Count(logged.String(), "\n")
	logPlatformManagedUpdateSkipped(platformFixtureID(1), errAppPlatformStageEvidence)
	logPlatformManagedUpdateSkipped(platformFixtureID(1), errAppPlatformStageEvidence)
	if strings.Count(logged.String(), "\n") != before+1 {
		t.Fatal("the same reason was logged twice for one operation")
	}
}

// An intent whose fence is ahead of the lease's was not written under any claim this lease follows: it is refused, where
// one behind it (an earlier claim of the same operation) is carried forward.
func TestPlatformCheckpointIntentWithAFenceAheadOfTheLeaseIsRefused(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET admitted_fence=admitted_fence+100 WHERE operation_id=? AND stage='snapshot'`, snapshot.id)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatal("an intent from a fence ahead of the lease's was accepted", err)
	}
}
