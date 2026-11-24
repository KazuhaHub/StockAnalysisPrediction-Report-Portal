package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformExpectGateOpen expects nothing about the installation's operations to hold launch, the backend, the proxy,
// grants or ingress: no unresolved operation and no unresolved effect.
func platformExpectGateOpen(t *testing.T, s *Server, installation string) {
	t.Helper()
	operations, err := platformUnresolvedOperationCount(s.st.poolExec(), installation)
	if err != nil || operations != 0 {
		t.Fatal("an operation still gates the installation", operations, err)
	}
	effects, err := platformUnresolvedOperationEffectCount(s.st.poolExec(), installation)
	if err != nil || effects != 0 {
		t.Fatal("an effect still gates the installation", effects, err)
	}
}

// platformExpectResolvedByRepair expects the operation to carry the recovered marker and the audit of the repair that
// resolved it.
func platformExpectResolvedByRepair(t *testing.T, s *Server, operation string) {
	t.Helper()
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='recovered' AND state='applied'`, operation) != 1 {
		t.Fatal("the operation was not marked resolved", operation)
	}
	var detail struct {
		Operation string `json:"operation_id"`
		Repair    string `json:"repair_operation_id"`
	}
	raw := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.recovery_resolved_by_repair' AND target_id=?`, operation)
	if err := json.Unmarshal([]byte(raw), &detail); err != nil || detail.Operation != operation || !platformValidUUID(detail.Repair) {
		t.Fatal("the resolution is not audited", raw, err)
	}
}

// An update that ended at the snapshot stage changed no data: the snapshot comes before the migration. Once a repair
// has brought the source back, the update is resolved, so nothing it left holds launch and the rest for ever.
func TestPlatformRepairResolvesAnUpdateThatEndedAtTheSnapshot(t *testing.T) {
	for _, code := range []string{"checkpoint_unsafe", "checkpoint_too_large", "checkpoint_incomplete"} {
		t.Run(code, func(t *testing.T) {
			var s *Server
			var snapshot platformOperationLease
			var fixture platformManagedUpdateReviewFixture
			if code == "checkpoint_incomplete" {
				s, snapshot = platformSnapshotStageFixture(t, false)
				fixture = lastPlatformUpdateFixture
				if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
					t.Fatal(err)
				}
				platformExhaustedSnapshot(t, s, snapshot)
			} else {
				s, snapshot, fixture = platformRefusedSnapshotEnded(t, code)
			}
			if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != code {
				t.Fatal("the update did not end with", code)
			}
			// Until the source is back, the ended update is what the installation is waiting on.
			if operations, err := platformUnresolvedOperationCount(s.st.poolExec(), snapshot.installationID); err != nil || operations != 1 {
				t.Fatal("the ended update is not pending resolution before the repair", operations, err)
			}
			platformRepairBringsTheSourceBack(t, s, snapshot, fixture)
			platformExpectGateOpen(t, s, snapshot.installationID)
			platformExpectResolvedByRepair(t, s, snapshot.id)
		})
	}
}

// A retry that ended at the snapshot stage leaves the installation disabled for recovery; repair is the way back, and
// once it has brought the source back, the retry and the failed update it retried are both resolved.
func TestPlatformRepairResolvesARetryThatEndedAtTheSnapshotAndTheUpdateItRetried(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	fixture := lastPlatformUpdateFixture
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	parent, parentPlan := platformFixtureID(6190), platformFixtureID(6191)
	// The failed update the retry names: a clone of the operation, ended for review earlier, with its own plan.
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'update',source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,?,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, parentPlan, platformDigest([]byte(parentPlan)), snapshot.id)
	mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,?,actor_scope,?,?,'update',managed,source_generation,target_generation,'recovery_required',state_revision,row_revision,NULL,NULL,fence,controller_epoch,'update_recovery_required',created_ms-1,created_ms-2,updated_ms
		FROM app_platform_operations WHERE id=?`, parent, parentPlan, platformDigest([]byte(parent)), platformDigest([]byte(parentPlan)), snapshot.id)
	var raw string
	if err := s.st.queryRow(`SELECT plan_json FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, snapshot.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var review platformUpdateProjection
	if err := json.Unmarshal([]byte(raw), &review); err != nil {
		t.Fatal(err)
	}
	review.RecoversOperationID = parent
	changed, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, string(changed), platformDigest(changed), snapshot.id)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='recovery_required' WHERE id=?`, snapshot.installationID)
	platformExhaustedSnapshot(t, s, snapshot)
	if operations, err := platformUnresolvedOperationCount(s.st.poolExec(), snapshot.installationID); err != nil || operations != 2 {
		t.Fatal("the retry and the update it retried are not both pending before the repair", operations, err)
	}
	platformRepairBringsTheSourceBack(t, s, snapshot, fixture)
	platformExpectGateOpen(t, s, snapshot.installationID)
	platformExpectResolvedByRepair(t, s, snapshot.id)
	platformExpectResolvedByRepair(t, s, parent)
}

// A retry that ended plan_expired before it began did nothing: it is not the failed update's successor, so the update
// can still be retried. Any other retry, in any state, is.
func TestPlatformExpiredUnbegunRetryIsNotASuccessor(t *testing.T) {
	s, operation := platformManagedUpdateOperationFixture(t, false)
	parent := platformFixtureID(6195)
	review := `{"recovers_operation_id":"` + parent + `"}`
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, review, operation.id)
	count := func() int {
		t.Helper()
		n, err := platformRetrySuccessorCount(s.st.poolExec(), operation.installationID, parent)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 1 {
		t.Fatal("a queued retry is not a successor")
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='failed',error_code='plan_expired',completed_ms=1 WHERE id=?`, operation.id)
	if count() != 0 {
		t.Fatal("a retry that expired before it began still blocks the update it named")
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required',error_code='update_recovery_required' WHERE id=?`, operation.id)
	if count() != 1 {
		t.Fatal("a retry that failed after it began is not a successor")
	}
}

// The expiry of a review plan governs a start the claim admits. Once claimed, the update carries on: a plan that
// expires between the claim and the first quiesce effect does not end it for review with its source never stopped.
func TestPlatformClaimedUpdateCarriesOnPastAPlanThatExpiresBeforeTheQuiesceEffect(t *testing.T) {
	s, lease := platformUpdateAtQuiesceWithoutEffect(t)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=1 WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, lease.id)
	if _, err := s.preparePlatformManagedUpdateQuiesce(context.Background(), lease); err != nil {
		t.Fatal("a claimed update was refused for a plan that expired after its claim:", err)
	}
}

// When the runner settled the snapshot but no reply came back on any attempt, the scheduler reads the receipt once
// more before ending the update: a verified checkpoint is committed and the update goes on, instead of ending
// checkpoint_incomplete with the runner's archive left behind.
func TestPlatformSnapshotWithALostReplyIsReadBackBeforeTheUpdateEnds(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	intent, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	w, err := newPlatformWritableInstallScheduler(s, endpoint, platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, errors.New("checkpoint reply lost")
	}
	reads := 0
	w.checkpointReadOverride = func(ctx context.Context, lease platformOperationLease) (platformOperationLease, error) {
		reads++
		return s.commitPlatformCheckpoint(ctx, lease, intent, endpoint, platformCheckpointObservationFixture(intent, endpoint))
	}
	for i := 0; i < 2*platformInstallAttemptLimit && reads == 0; i++ {
		_ = w.tick(context.Background())
	}
	if reads != 1 {
		t.Fatal("the receipt was not read back once the attempts ran out", reads)
	}
	if stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, snapshot.id); stage != string(appplatform.StageMigrate) ||
		w.active.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("a checkpoint the runner settled was not committed", stage, w.active.snapshot.Stage)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE operation_id=?`, snapshot.id) != "verified" {
		t.Fatal("the verified recovery set was not kept")
	}
}

// When the read back finds nothing either, the update ends checkpoint_incomplete as before.
func TestPlatformSnapshotWithNoReceiptOnReadBackEndsIncomplete(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	w, err := newPlatformWritableInstallScheduler(s, platformManagedRemovalFixtureEndpoint(), platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, errors.New("checkpoint walk timed out")
	}
	reads := 0
	w.checkpointReadOverride = func(context.Context, platformOperationLease) (platformOperationLease, error) {
		reads++
		return platformOperationLease{}, errors.New("the runner holds no receipt")
	}
	for i := 0; i < 2*platformInstallAttemptLimit && w.currentStatus().code != "review_required"; i++ {
		_ = w.tick(context.Background())
	}
	if reads != 1 {
		t.Fatal("the receipt was not read back exactly once before the update ended", reads)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != "recovery_required:checkpoint_incomplete" {
		t.Fatal("an update with no receipt on read back did not end checkpoint_incomplete")
	}
}

// A checkpoint code names an ending at the snapshot stage and nothing else: the finisher refuses it at a later stage,
// so the repair that resolves such endings can rely on the code.
func TestPlatformCheckpointCodeIsRefusedAfterTheSnapshotStage(t *testing.T) {
	s, lease, _, _ := platformManagedUpdateQuiesceFixture(t, false)
	if err := s.finishPlatformManagedUpdateWith(context.Background(), lease, nil, "checkpoint_unsafe"); !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatal("a checkpoint code was accepted at the quiesce stage", err)
	}
}

// A read back that finds the checkpoint refused (the runner settled the refusal while the reply was lost) ends the
// update with the runner's code, not checkpoint_incomplete.
func TestPlatformSnapshotRefusalFoundOnReadBackKeepsTheRunnersCode(t *testing.T) {
	s, snapshot := platformSnapshotStageFixture(t, false)
	intent, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newPlatformWritableInstallScheduler(s, platformManagedRemovalFixtureEndpoint(), platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, errors.New("checkpoint reply lost")
	}
	w.checkpointReadOverride = func(ctx context.Context, lease platformOperationLease) (platformOperationLease, error) {
		return s.commitPlatformCheckpoint(ctx, lease, intent, platformCheckpointEndpointFixture(), platformRefusedCheckpoint(t, intent, "checkpoint_unsafe"))
	}
	for i := 0; i < 2*platformInstallAttemptLimit && w.currentStatus().code != "review_required"; i++ {
		_ = w.tick(context.Background())
	}
	if got := scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, snapshot.id); got != "recovery_required:checkpoint_unsafe" {
		t.Fatal("a refusal found on read back lost the runner's code:", got)
	}
}
