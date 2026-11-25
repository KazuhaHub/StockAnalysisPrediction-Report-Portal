package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformRefusedSnapshotEnded takes a managed update to the snapshot stage and ends it through the scheduler on the
// runner's typed refusal, the way production did.
func platformRefusedSnapshotEnded(t *testing.T, code string) (*Server, platformOperationLease, platformManagedUpdateReviewFixture) {
	t.Helper()
	s, snapshot := platformSnapshotStageFixture(t, false)
	fixture := lastPlatformUpdateFixture.get(t)
	intent, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	w, err := newPlatformWritableInstallScheduler(s, platformManagedRemovalFixtureEndpoint(), platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	w.runOverride = func(ctx context.Context, lease platformOperationLease) (platformInstallRun, error) {
		_, err := s.commitPlatformCheckpoint(ctx, lease, intent, platformCheckpointEndpointFixture(), platformRefusedCheckpoint(t, intent, code))
		return platformInstallRun{}, err
	}
	if err := w.tick(context.Background()); err != nil || w.currentStatus().code != "review_required" {
		t.Fatalf("the refused update was not ended: %v %+v", err, w.currentStatus())
	}
	return s, snapshot, fixture
}

// A snapshot the runner refused leaves nothing unresolved: the data was never touched (the snapshot comes before
// the migration), so its effect and its recovery set end, the installation stops being gated by them, and the runner's
// own failure code is what the operation reports.
func TestPlatformRefusedSnapshotLeavesNoUnresolvedEffectAndKeepsTheRunnersCode(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"checkpoint_unsafe", "checkpoint_too_large"} {
		t.Run(code, func(t *testing.T) {
			s, snapshot, _ := platformRefusedSnapshotEnded(t, code)
			if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != code ||
				scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, snapshot.id) != "recovery_required" {
				t.Fatal("the operation does not carry the runner's failure code")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id) != "reverted" ||
				scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE operation_id=?`, snapshot.id) != "deleted" {
				t.Fatal("the snapshot effect and its recovery set were not ended")
			}
			effects, err := platformUnresolvedOperationEffectCount(s.st.poolExec(), snapshot.installationID)
			if err != nil || effects != 0 {
				t.Fatal("an unresolved effect still gates the installation", effects, err)
			}
			var receipt struct {
				State string `json:"state"`
				Code  string `json:"code"`
			}
			if err := json.Unmarshal([]byte(scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id)), &receipt); err != nil ||
				receipt.State != "failed" || receipt.Code != code {
				t.Fatal("the effect does not record the refusal", receipt, err)
			}
		})
	}
}

// After such an ending the source generation can come back through the existing repair action: it is reviewed,
// admitted, checkpointed and started, and the installation is enabled and ready again.
func TestPlatformRepairBringsTheSourceBackAfterARefusedSnapshot(t *testing.T) {
	t.Parallel()
	s, snapshot, fixture := platformRefusedSnapshotEnded(t, "checkpoint_unsafe")
	platformRepairBringsTheSourceBack(t, s, snapshot, fixture)
}

// platformRepairBringsTheSourceBack reviews, admits, checkpoints and starts a repair of the installation of an update
// that ended at its snapshot, and expects it enabled and ready at the target generation.
func platformRepairBringsTheSourceBack(t *testing.T, s *Server, snapshot platformOperationLease, fixture platformManagedUpdateReviewFixture) {
	t.Helper()
	installation := snapshot.installationID
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "disabled:recovery_required" {
		t.Fatal("the installation is not offered to repair")
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), fixture.activeEpoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformManagedRepairReviewWithRuntimeContext(context.Background(), fixture.actor, installation, fixture.target, runtime)
	if err != nil {
		t.Fatal("a repair cannot be reviewed after the refused snapshot", err)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil || plan.Action != appplatform.ActionRepair || plan.SourceGeneration != generation {
		t.Fatal("repair review plan", plan)
	}
	commit, err := s.consumePlatformManagedRepairReviewWithRuntimeContext(context.Background(), fixture.actor, review.ID, review.SHA256, platformFixtureID(6160), fixture.target, runtime)
	if err != nil {
		t.Fatal("repair admission", err)
	}
	selected, err := s.claimNextPlatformManagedRepair(context.Background(), platformFixtureID(6161))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("repair claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage != appplatform.StageQuiesce {
		if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil {
			t.Fatal("repair stage", current.snapshot.Stage, err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), current)
	if err != nil {
		t.Fatal("repair checkpoint", err)
	}
	endpoint := platformCheckpointEndpointFixture()
	startLease, err := s.commitPlatformCheckpoint(context.Background(), current, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil || startLease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("repair checkpoint commit", startLease.snapshot, err)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != plan.TargetGeneration {
		t.Fatal("repair did not bring the source back")
	}
}

// The attempt that sends a checkpoint is bulk work with the runner's own bound, not the short attempt: cut at 20s it
// would abandon a walk that is still verifying, over and over.
func TestPlatformCheckpointAttemptHasTheBulkTimeout(t *testing.T) {
	t.Parallel()
	for name, lease := range map[string]platformOperationLease{
		"update snapshot": {snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionUpdate, Managed: true, Stage: appplatform.StageSnapshot}},
		"enable snapshot": {snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionEnable, Managed: true, Stage: appplatform.StageSnapshot}},
		"repair quiesce":  {snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionRepair, Managed: true, Stage: appplatform.StageQuiesce}},
	} {
		if platformAttemptTimeout(lease) != platformImportAttemptTimeout {
			t.Fatal(name, "does not get the bulk timeout")
		}
	}
	quiesce := platformOperationLease{snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionUpdate, Managed: true, Stage: appplatform.StageQuiesce}}
	if platformAttemptTimeout(quiesce) != platformInstallAttemptTimeout {
		t.Fatal("an update's quiesce is not a checkpoint")
	}
}

// platformExhaustedSnapshot ends an update at the snapshot stage the other way: every attempt fails with an error that
// settled nothing (the walk timed out), until the scheduler gives up.
func platformExhaustedSnapshot(t *testing.T, s *Server, snapshot platformOperationLease) {
	t.Helper()
	w, err := newPlatformWritableInstallScheduler(s, platformManagedRemovalFixtureEndpoint(), platformFixtureID(6120), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.active = snapshot
	w.runOverride = func(context.Context, platformOperationLease) (platformInstallRun, error) {
		return platformInstallRun{}, errors.New("checkpoint walk timed out")
	}
	for i := 0; i < 2*platformInstallAttemptLimit && w.currentStatus().code != "review_required"; i++ {
		_ = w.tick(context.Background())
	}
	if w.currentStatus().code != "review_required" {
		t.Fatalf("the scheduler never gave up: %+v", w.currentStatus())
	}
}

// An update that ends at the snapshot stage on exhausted attempts, with no verdict from the runner, ends the same way
// as a refusal: the effect reverted, the empty recovery set deleted, the installation disabled for recovery with the
// code checkpoint_incomplete, and repair as the way back.
func TestPlatformSnapshotEndedOnExhaustedAttemptsIsNotADeadEnd(t *testing.T) {
	t.Parallel()
	s, snapshot := platformSnapshotStageFixture(t, false)
	fixture := lastPlatformUpdateFixture.get(t)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	platformExhaustedSnapshot(t, s, snapshot)
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != "recovery_required:checkpoint_incomplete" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id) != "reverted" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE operation_id=?`, snapshot.id) != "deleted" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, snapshot.installationID) != "disabled:recovery_required" {
		t.Fatal("an update that ran out of attempts at the snapshot is a dead end")
	}
	if effects, err := platformUnresolvedOperationEffectCount(s.st.poolExec(), snapshot.installationID); err != nil || effects != 0 {
		t.Fatal("an unresolved effect still gates the installation", effects, err)
	}
	platformRepairBringsTheSourceBack(t, s, snapshot, fixture)
}

// The runner's checkpoint codes, and the code of an update whose plan expired, reach the management and status views as
// themselves, not as the generic failure.
func TestPlatformCheckpointCodesReachThePublicStatus(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"checkpoint_unsafe", "checkpoint_too_large", "checkpoint_incomplete", "plan_expired"} {
		if got := platformPublicOperationErrorCode(code); got != code {
			t.Fatalf("%s is shown as %q", code, got)
		}
	}
}

// The system's disable is audited against the installation, with the code and the fact that it was disabled for
// recovery.
func TestPlatformSnapshotEndingAuditsTheDisableForRecovery(t *testing.T) {
	t.Parallel()
	s, snapshot, _ := platformRefusedSnapshotEnded(t, "checkpoint_unsafe")
	var detail struct {
		Operation string `json:"operation_id"`
		Code      string `json:"code"`
		Disabled  bool   `json:"disabled_for_recovery"`
	}
	raw := scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.installation_disabled_for_recovery' AND target_id=?`, snapshot.installationID)
	if err := json.Unmarshal([]byte(raw), &detail); err != nil || detail.Operation != snapshot.id || detail.Code != "checkpoint_unsafe" || !detail.Disabled {
		t.Fatal("the disable for recovery is not audited", raw, err)
	}
	if !strings.Contains(scalar[string](t, s.st, `SELECT detail FROM audit_log WHERE action='app_platform.update_recovery_required' AND target_id=?`, snapshot.id), "checkpoint_unsafe") {
		t.Fatal("the operation's audit row does not carry the code")
	}
}

// An update that retries another failed one (the installation is enabled in recovery_required) and ends at the snapshot
// is disabled for recovery as well, never stranded enabled and in recovery with its source stopped.
func TestPlatformRetryEndedAtTheSnapshotIsDisabledForRecovery(t *testing.T) {
	t.Parallel()
	s, snapshot := platformSnapshotStageFixture(t, false)
	if _, err := s.preparePlatformCheckpoint(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.st.queryRow(`SELECT plan_json FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, snapshot.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var review platformUpdateProjection
	if err := json.Unmarshal([]byte(raw), &review); err != nil {
		t.Fatal(err)
	}
	review.RecoversOperationID = platformFixtureID(6190)
	changed, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, string(changed), platformDigest(changed), snapshot.id)
	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='recovery_required' WHERE id=?`, snapshot.installationID)
	platformExhaustedSnapshot(t, s, snapshot)
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, snapshot.installationID) != "disabled:recovery_required" ||
		scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, snapshot.id) != "checkpoint_incomplete" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, snapshot.id) != "reverted" {
		t.Fatal("a retry ended at the snapshot stranded the installation")
	}
}
