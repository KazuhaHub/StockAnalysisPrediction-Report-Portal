package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformRefusedSnapshotEnded takes a managed update to the snapshot stage and ends it through the scheduler on the
// runner's typed refusal, the way production did.
func platformRefusedSnapshotEnded(t *testing.T, code string) (*Server, platformOperationLease, platformManagedUpdateReviewFixture) {
	t.Helper()
	s, snapshot := platformSnapshotStageFixture(t, false)
	fixture := lastPlatformUpdateFixture
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
	s, snapshot, fixture := platformRefusedSnapshotEnded(t, "checkpoint_unsafe")
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
