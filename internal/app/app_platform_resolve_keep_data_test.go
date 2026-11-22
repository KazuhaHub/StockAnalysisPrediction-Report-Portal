package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformResolveKeepDataFixture is the state a failed import leaves in production: the data was imported, the start did
// not run, the operation ended recovery_required with its rollback restore prepared and never admitted by the runner,
// and the installation is disabled in recovery_required.
type platformResolveKeepDataState struct {
	s            *Server
	actor        platformActorSession
	installation string
	operation    string
	endpoint     platformRuntimeEndpoint
	runner       *platformFakeMigrationRunner
	target       *platformManagedInstallTarget
	runtime      platformRuntimeRegistration
}

func platformResolveKeepDataFixture(t *testing.T) platformResolveKeepDataState {
	t.Helper()
	s, actor, enabled, target, runtime, _ := platformImportDisabledFixture(t, false)
	installation := enabled.installationID
	endpoint := platformCheckpointEndpointFixture()
	review, err := s.createPlatformManagedImportDataReviewWithRuntimeContext(context.Background(), actor, installation, platformTestArchiveSHA, target, runtime, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(context.Background(), actor, review.ID, review.SHA256, platformFixtureID(1470), target, runtime, appplatform.ActionImportData)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedSameRelease(context.Background(), platformFixtureID(1471), appplatform.ActionImportData)
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal(err)
	}
	current := selected.lease
	if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if current, err = s.validatePlatformImportData(context.Background(), current, endpoint); err != nil {
		t.Fatal(err)
	}
	if current, err = s.advancePlatformManagedEnableStage(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	importLease, err := s.commitPlatformCheckpoint(context.Background(), current, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.preparePlatformImportData(context.Background(), importLease)
	if err != nil {
		t.Fatal(err)
	}
	startLease, err := s.commitPlatformImportData(context.Background(), importLease, intent, endpoint, platformDataImportObservation(intent, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	// The rollback is prepared, the runner refuses it before admitting it, and the operation ends recovery_required.
	if _, needed, err := s.preparePlatformImportRollback(context.Background(), startLease); err != nil || !needed {
		t.Fatal("rollback intent", needed, err)
	}
	if err := s.finishPlatformManagedSameReleaseForReview(context.Background(), startLease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "recovery_required" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "disabled:recovery_required" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='import' AND effect_number=2 AND state='prepared'`, commit.OperationID) != 1 {
		t.Fatal("the fixture is not the failed import state")
	}
	// Long enough ago that no command signed for the operation can still arrive.
	if _, err := s.st.exec(`UPDATE app_platform_operations SET completed_ms=completed_ms-3600000 WHERE id=?`, commit.OperationID); err != nil {
		t.Fatal(err)
	}
	runner := &platformFakeMigrationRunner{t: t}
	runner.readRestore = func(platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	s.platformMigration = runner
	return platformResolveKeepDataState{s: s, actor: actor, installation: installation, operation: commit.OperationID, endpoint: endpoint, runner: runner, target: target, runtime: runtime}
}

func (f platformResolveKeepDataState) review(t *testing.T) (platformRemovalReview, error) {
	t.Helper()
	return f.s.createPlatformResolveKeepDataReview(context.Background(), f.actor, f.installation, f.endpoint)
}

func (f platformResolveKeepDataState) confirm(t *testing.T, review platformRemovalReview, key string) (platformResolveKeepDataResult, error) {
	t.Helper()
	return f.s.consumePlatformResolveKeepDataReview(context.Background(), f.actor, review.ID, review.SHA256, key, f.endpoint)
}

// The exact production state resolves: the data and the pre-import checkpoint stay, the rollback is closed as abandoned
// without a runner command, the operation is read as resolved, and nothing is pending any more.
func TestPlatformResolveKeepDataResolvesTheFailedImportSQLite(t *testing.T) {
	f := platformResolveKeepDataFixture(t)
	settings, err := f.s.platformInstallationSettings(f.actor, f.installation)
	if err != nil || !settings.OperationPending || !settings.ResolveKeepDataEligible {
		t.Fatalf("the failed import is not pending and resolvable before the resolution: %v %v %v", settings.OperationPending, settings.ResolveKeepDataEligible, err)
	}
	observed := scalar[int64](t, f.s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, f.installation)
	sets := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=? AND state='verified'`, f.operation)
	review, err := f.review(t)
	if err != nil {
		t.Fatal("review", err)
	}
	var plan platformResolveKeepDataPlan
	if jsonErr := json.Unmarshal([]byte(review.JSON), &plan); jsonErr != nil || plan.OperationID != f.operation || plan.ObservedBytes != observed ||
		len(plan.AbandonedEffects) != 1 || plan.AbandonedEffects[0].Kind != "restore" || plan.CheckpointRecoverySt == "" {
		t.Fatalf("the review does not say what will happen: %+v %v", plan, jsonErr)
	}
	calls := len(f.runner.calls)
	result, err := f.confirm(t, review, platformFixtureID(1480))
	if err != nil || result.OperationID != f.operation || result.Abandoned != 1 {
		t.Fatal("confirm", result, err)
	}
	if got := scalar[string](t, f.s.st, `SELECT state||':'||receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='import' AND effect_number=2`, f.operation); got != `reverted:`+platformAbandonedReceipt {
		t.Fatalf("the rollback was not closed as abandoned: %s", got)
	}
	for _, call := range f.runner.calls[calls:] {
		if call == "execute-restore" || call == "execute-migrate" || call == "execute-discard" {
			t.Fatalf("the resolution issued a runner command: %s", call)
		}
	}
	if scalar[int64](t, f.s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, f.installation) != observed ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=? AND state='verified'`, f.operation) != sets {
		t.Fatal("the data or its pre-import checkpoint changed")
	}
	if scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "disabled:recovery_required" {
		t.Fatal("the installation did not stay where a repair is reviewed from")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=? AND actor<>'system'`, platformResolveKeepDataAudit, f.operation) != 1 {
		t.Fatal("the resolution is not audited with its actor and operation")
	}
	settings, err = f.s.platformInstallationSettings(f.actor, f.installation)
	if err != nil || settings.OperationPending || settings.ResolveKeepDataEligible {
		t.Fatalf("an operation is still pending or resolvable after the resolution: %v %v %v", settings.OperationPending, settings.ResolveKeepDataEligible, err)
	}
	// Repeating the confirmation answers what it answered and does nothing more.
	again, err := f.confirm(t, review, platformFixtureID(1480))
	if err != nil || again != result {
		t.Fatalf("an idempotent repeat answered %+v %v", again, err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformResolveKeepDataAudit) != 1 {
		t.Fatal("a repeat was audited again")
	}
	// A new review finds nothing left to resolve.
	if _, err := f.review(t); !errors.Is(err, errPlatformResolveKeepDataUnavailable) {
		t.Fatalf("a resolved installation can be reviewed again: %v", err)
	}
}

func TestPlatformResolveKeepDataRefusals(t *testing.T) {
	t.Run("the runner holds the restore", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		f.runner.readRestore = func(platformRestoreIntent) (platformSettlingRestoreObservation, error) {
			return platformFakeObservation{}, nil
		}
		if _, err := f.review(t); err == nil {
			t.Fatal("a rollback the runner holds was abandoned")
		}
	})
	t.Run("the runner cannot be asked", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		f.runner.readRestore = func(platformRestoreIntent) (platformSettlingRestoreObservation, error) {
			return nil, errors.New("runner unreachable")
		}
		if _, err := f.review(t); err == nil {
			t.Fatal("a rollback was abandoned without the runner's answer")
		}
	})
	t.Run("the rollback was observed by the runner", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_operation_effects SET observed_fence=5 WHERE operation_id=? AND stage='import' AND effect_number=2`, f.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); !errors.Is(err, errAppPlatformOperationConflict) {
			t.Fatalf("an effect the runner admitted was abandoned: %v", err)
		}
	})
	t.Run("another effect is unsettled", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_operation_effects SET state='unknown' WHERE operation_id=? AND stage='import' AND effect_number=1`, f.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); !errors.Is(err, errAppPlatformOperationConflict) {
			t.Fatalf("an unsettled import was accepted: %v", err)
		}
	})
	t.Run("the installation is enabled", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, f.installation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); !errors.Is(err, errPlatformResolveKeepDataUnavailable) {
			t.Fatalf("an enabled installation was resolved: %v", err)
		}
	})
	t.Run("the operation is not a same-release lifecycle one", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_operations SET action='update' WHERE id=?`, f.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); err == nil {
			t.Fatal("an update was resolved by keeping data")
		}
	})
	t.Run("the operation names no single release", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_review_plans SET target_release_sha256=NULL WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, f.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); err == nil {
			t.Fatal("an operation of no single release was resolved")
		}
	})
	t.Run("the operation was completed moments ago", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		if _, err := f.s.st.exec(`UPDATE app_platform_operations SET completed_ms=completed_ms+3600000 WHERE id=?`, f.operation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.review(t); !errors.Is(err, errAppPlatformOperationConflict) {
			t.Fatalf("a rollback that may still arrive was abandoned: %v", err)
		}
	})
	t.Run("a confirmation of a changed state", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		review, err := f.review(t)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.st.exec(`UPDATE app_platform_data_namespaces SET observed_bytes=observed_bytes+1 WHERE owner_installation_id=?`, f.installation); err != nil {
			t.Fatal(err)
		}
		if _, err := f.confirm(t, review, platformFixtureID(1481)); !errors.Is(err, errAppPlatformPlanChanged) {
			t.Fatalf("a review of another state was confirmed: %v", err)
		}
		if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='import' AND effect_number=2`, f.operation) != "prepared" {
			t.Fatal("a refused confirmation changed the effect")
		}
	})
	t.Run("a confirmation of another administrator", func(t *testing.T) {
		f := platformResolveKeepDataFixture(t)
		review, err := f.review(t)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.consumePlatformResolveKeepDataReview(context.Background(), platformActorSession{}, review.ID, review.SHA256, platformFixtureID(1482), f.endpoint); err == nil {
			t.Fatal("a confirmation without an administrator session was accepted")
		}
	})
}

// After the resolution a repair can be reviewed, at the generation a failed import of an install-sourced installation
// leaves (the install's own, below the disable's), and refuses without it.
func TestPlatformResolveKeepDataMakesRepairEligibleSQLite(t *testing.T) {
	f := platformResolveKeepDataFixture(t)
	review, err := f.review(t)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.confirm(t, review, platformFixtureID(1490)); err != nil {
		t.Fatal(err)
	}
	// The review itself is admitted from there, and keeps the data: it is a repair of the same release.
	repair, err := f.s.createPlatformManagedRepairReviewWithRuntimeContext(context.Background(), f.actor, f.installation, f.target, f.runtime)
	if err != nil || repair.ID == "" {
		t.Fatalf("a repair cannot be reviewed after the resolution: %v", err)
	}
	for _, generation := range []int64{scalar[int64](t, f.s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation), 1} {
		if _, err := f.s.st.exec(`UPDATE app_platform_installations SET generation=? WHERE id=?`, generation, f.installation); err != nil {
			t.Fatal(err)
		}
		settings, err := f.s.platformInstallationSettings(f.actor, f.installation)
		if err != nil || !settings.RepairEligible {
			t.Fatalf("a repair is not offered after the resolution at generation %d: %v %v", generation, settings.RepairEligible, err)
		}
	}
}
