package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func failedScript(fake *platformFakeMigrationRunner, endpoint platformRuntimeEndpoint) {
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, endpoint, "failed", "exit_nonzero"), nil
	}
}

// settledFixture is a migrate-data that failed (C1) and was settled: the checkpoint was restored.
func settledFixture(t *testing.T, pg bool) *settleFixture {
	t.Helper()
	f := settleAfterEnd(t, pg, failedScript)
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	f.step(t)
	f.step(t)
	if f.settleState(t) != "settled" {
		t.Fatal("fixture did not settle")
	}
	return f
}

func recoverySetOf(t *testing.T, s *Server, operation string) string {
	t.Helper()
	return scalar[string](t, s.st, `SELECT id FROM app_platform_recovery_sets WHERE operation_id=?`, operation)
}

// Neither choice is offered before settlement completes.
func testChoicesRefusedBeforeSettlement(t *testing.T, pg bool) {
	f := settleAfterEnd(t, pg, failedScript)
	session := platformManagedFixtureSession(t, f.s)
	set := recoverySetOf(t, f.s, f.op)
	if _, err := f.s.createPlatformManagedRestoreReview(context.Background(), session, f.lease.installationID, set); !errors.Is(err, errAppPlatformMigrationUnsettled) &&
		err == nil {
		t.Fatal("a roll back was reviewed before settlement")
	} else if err != nil && !errors.Is(err, errAppPlatformMigrationUnsettled) && !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if err := requirePlatformRetrySource(f.s.st.poolExec(), f.lease.installationID, f.op); !errors.Is(err, errAppPlatformMigrationUnsettled) {
		t.Fatalf("a retry was admitted before settlement: %v", err)
	}
	if platformMigrationHasPlan(t, f.s) {
		t.Fatal("a review plan was minted")
	}
}

func platformMigrationHasPlan(t *testing.T, s *Server) bool {
	t.Helper()
	return scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE action='restore'`) != 0
}

func TestChoicesRefusedBeforeSettlementSQLite(t *testing.T) {
	testChoicesRefusedBeforeSettlement(t, false)
}
func TestChoicesRefusedBeforeSettlementPostgres(t *testing.T) {
	testChoicesRefusedBeforeSettlement(t, true)
}

// Roll back after a settled migration restores nothing and stops nothing: the settlement did both, a checkpoint
// restores once, and the stages that would repeat that advance on its evidence.
func testRollbackAfterSettledMigrationEvidenceOnlyRestore(t *testing.T, pg bool) {
	f := settledFixture(t, pg)
	session := platformManagedFixtureSession(t, f.s)
	set := recoverySetOf(t, f.s, f.op)
	review, err := f.s.createPlatformManagedRestoreReview(context.Background(), session, f.lease.installationID, set)
	if err != nil {
		t.Fatal("review after settlement:", err)
	}
	commit, err := f.s.consumePlatformManagedRestoreReview(context.Background(), session, review.ID, review.SHA256, platformFixtureID(7001))
	if err != nil {
		t.Fatal("commit after settlement:", err)
	}
	selected, err := f.s.claimNextPlatformManagedRestore(context.Background(), platformFixtureID(7002))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("restore was not claimed", selected, err)
	}
	lease := selected.lease
	restores := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, f.op)
	for lease.snapshot.Stage != appplatform.StageStart {
		next, err := f.s.runPlatformManagedRestore(context.Background(), lease, f.endpoint)
		if err != nil {
			t.Fatalf("stage %s: %v", lease.snapshot.Stage, err)
		}
		lease = next
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 0 ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, f.op) != restores ||
		len(f.fake.calls) != restores*0+f.fake.count("check-root")+f.fake.count("execute-restore") {
		t.Fatal("a roll back after settlement sent commands of its own")
	}
	intent, err := f.s.preparePlatformRestoreStart(context.Background(), lease)
	if err != nil {
		t.Fatal("restore start after settlement:", err)
	}
	if intent.command.Payload.SourceCheckpointOperationID != f.op {
		t.Fatalf("restore start names the wrong checkpoint: %+v", intent.command.Payload)
	}
}

func TestRollbackAfterSettledMigrationEvidenceOnlyRestoreSQLite(t *testing.T) {
	testRollbackAfterSettledMigrationEvidenceOnlyRestore(t, false)
}
func TestRollbackAfterSettledMigrationEvidenceOnlyRestorePostgres(t *testing.T) {
	testRollbackAfterSettledMigrationEvidenceOnlyRestore(t, true)
}

// A retry is admitted only from the installation's settled failed update.
func testRetryReviewFromSettledMigration(t *testing.T, pg bool) {
	f := settledFixture(t, pg)
	pool := f.s.st.poolExec()
	if err := requirePlatformRetrySource(pool, f.lease.installationID, f.op); err != nil {
		t.Fatal("a settled failed update refused a retry:", err)
	}
	if err := requirePlatformRetrySource(pool, f.lease.installationID, platformFixtureID(9999)); err == nil {
		t.Fatal("a retry named an operation that is not there")
	}
	if err := requirePlatformRetrySource(pool, platformFixtureID(9998), f.op); err == nil {
		t.Fatal("a retry named another installation's update")
	}
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE operation_id=? AND stage='settled'`, f.op)
	if err := requirePlatformRetrySource(pool, f.lease.installationID, f.op); err == nil {
		t.Fatal("a retry was admitted from an update that is not settled")
	}
}

func TestRetryReviewFromSettledMigrationSQLite(t *testing.T) {
	testRetryReviewFromSettledMigration(t, false)
}
func TestRetryReviewFromSettledMigrationPostgres(t *testing.T) {
	testRetryReviewFromSettledMigration(t, true)
}

// There is no retry inside the failed operation: it is never claimed again, however settled it is.
func TestNoInOperationRetry(t *testing.T) {
	f := settledFixture(t, false)
	selected, err := f.s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7100))
	if err != nil || selected.lease.id != "" {
		t.Fatalf("a failed update was claimed again: %v %v", selected.lease.id, err)
	}
	lease, err := platformSettlementLease(f.s.st.poolExec(), f.op)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.runPlatformManagedUpdate(context.Background(), lease, f.endpoint); err == nil {
		t.Fatal("a recovery_required update was run")
	}
}

// retryToMigrate admits a retry of the failed update recovers and takes it, on the failed update's evidence and a
// fresh checkpoint, to the point where migrate-data is sent. It returns the lease at the migrate stage.
func retryToMigrate(t *testing.T, f *settleFixture, fx platformManagedUpdateReviewFixture, planID, operationID, recovers string) platformOperationLease {
	t.Helper()
	platformInsertManagedUpdateOperation(t, fx, planID, operationID, recovers)
	return driveRetryToMigrate(t, f, operationID)
}

// driveRetryToMigrate claims the admitted retry and takes it, on the failed update's evidence and a fresh
// checkpoint, to the point where migrate-data is sent.
func driveRetryToMigrate(t *testing.T, f *settleFixture, operationID string) platformOperationLease {
	t.Helper()
	selected, err := f.s.claimNextPlatformManagedUpdate(context.Background(), platformFixtureID(7200))
	if err != nil || selected.lease.id != operationID {
		t.Fatal("the retry was not claimed", selected.lease.id, err)
	}
	lease := selected.lease
	for lease.snapshot.Stage != appplatform.StageQuiesce {
		if lease, err = f.s.advancePlatformManagedUpdateStage(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	lease, handled, err := f.s.runPlatformRetryEvidenceStage(context.Background(), lease)
	if err != nil || !handled || lease.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("the retry did not take the stop from the failed update's evidence", lease.snapshot.Stage, handled, err)
	}
	if effectState(t, f.s, operationID, "quiesce", 1) != "applied" || f.fake.count("remove-stop") != 0 {
		t.Fatal("a retry stopped the source again")
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpoint, err := f.s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil {
		t.Fatal("fresh checkpoint:", err)
	}
	lease, err = f.s.commitPlatformCheckpoint(context.Background(), lease, checkpoint, checkpointEndpoint, platformCheckpointObservationFixture(checkpoint, checkpointEndpoint))
	if err != nil || lease.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("fresh checkpoint did not commit", lease.snapshot.Stage, err)
	}
	lease, handled, err = f.s.runPlatformRetryEvidenceStage(context.Background(), lease)
	if err != nil || !handled || lease.snapshot.Stage != appplatform.StageMigrate || effectState(t, f.s, operationID, "migrate", 1) != "applied" {
		t.Fatal("the retry did not take the source removal from the failed update's evidence", lease.snapshot.Stage, handled, err)
	}
	return lease
}

// A retry that completes resolves the failed update it recovered, so the application launches again.
func testRetryCompletionResolvesFailedUpdateForLaunch(t *testing.T, pg bool) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, pg)
	fx := lastPlatformUpdateFixture
	lease := retryToMigrate(t, f, fx, platformFixtureID(7210), platformFixtureID(7211), f.op)
	session := platformManagedFixtureSession(t, f.s)
	if _, err := f.s.inspectPlatformLaunchAccess(session, f.lease.installationID); err == nil {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		if i.command.Payload.RemoveCodeEffectID == "" {
			t.Error("the retry's migration names no source removal")
		}
		return platformMigrateAnswers(i, f.endpoint, "migrated", ""), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), lease, f.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	activate, registration, observation, _ := platformMigratedUpdateToRegistration(t, f.s, start, f.endpoint)
	registered, err := f.s.commitPlatformUpdateStartRegistration(context.Background(), activate, registration, f.endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, f.s, registered, f.endpoint)
	defer applied.registration.Close()
	result, err := f.s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the retry did not activate", err)
	}
	if scalar[string](t, f.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.lease.installationID) != "ready" ||
		effectState(t, f.s, f.op, "recovered", 1) != "applied" {
		t.Fatal("the retry did not resolve the failed update")
	}
	if launch, err := f.s.inspectPlatformLaunchAccess(session, f.lease.installationID); err != nil || launch.generation != "2" {
		t.Fatal("a recovered application cannot launch", launch, err)
	}
}

func TestRetryCompletionResolvesFailedUpdateForLaunchSQLite(t *testing.T) {
	testRetryCompletionResolvesFailedUpdateForLaunch(t, false)
}
func TestRetryCompletionResolvesFailedUpdateForLaunchPostgres(t *testing.T) {
	testRetryCompletionResolvesFailedUpdateForLaunch(t, true)
}

// A retry that itself fails is settled and retried in turn; the retry that finally completes resolves the whole
// chain, down to the root.
func testRetryOfRetryResolvesRoot(t *testing.T, pg bool) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, pg)
	fx := lastPlatformUpdateFixture
	second := retryToMigrate(t, f, fx, platformFixtureID(7310), platformFixtureID(7311), f.op)
	failedScript(f.fake, f.endpoint)
	if _, err := f.s.dispatchPlatformMigrateData(context.Background(), second, f.endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	retry := &settleFixture{s: f.s, lease: f.lease, endpoint: f.endpoint, fake: f.fake, op: second.id}
	retry.step(t)
	retry.step(t)
	if retry.settleState(t) != "settled" {
		t.Fatal("the failed retry did not settle", retry.settleState(t))
	}
	if err := requirePlatformRetrySource(f.s.st.poolExec(), f.lease.installationID, f.op); err != nil {
		t.Fatal("the root failed update stopped being the root of a chain:", err)
	}
	third := retryToMigrate(t, f, fx, platformFixtureID(7320), platformFixtureID(7321), second.id)
	f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, f.endpoint, "migrated", ""), nil
	}
	start, err := f.s.dispatchPlatformMigrateData(context.Background(), third, f.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	activate, registration, observation, _ := platformMigratedUpdateToRegistration(t, f.s, start, f.endpoint)
	registered, err := f.s.commitPlatformUpdateStartRegistration(context.Background(), activate, registration, f.endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, f.s, registered, f.endpoint)
	defer applied.registration.Close()
	if _, err := f.s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot); err != nil {
		t.Fatal(err)
	}
	for _, failed := range []string{f.op, second.id} {
		if effectState(t, f.s, failed, "recovered", 1) != "applied" {
			t.Fatalf("%s was left unresolved", failed)
		}
	}
	if _, err := f.s.inspectPlatformLaunchAccess(platformManagedFixtureSession(t, f.s), f.lease.installationID); err != nil {
		t.Fatal("the application cannot launch after the chain recovered:", err)
	}
}

func TestRetryOfRetryResolvesRootSQLite(t *testing.T)   { testRetryOfRetryResolvesRoot(t, false) }
func TestRetryOfRetryResolvesRootPostgres(t *testing.T) { testRetryOfRetryResolvesRoot(t, true) }

// The settings projection follows the settlement: settling, then settled with both choices, blocked when the
// fence has moved, and absent for an installation with nothing to recover.
func TestSettingsProjectsMigrationRecovery(t *testing.T) {
	f := settleAfterEnd(t, false, orphanedScript)
	session := platformManagedFixtureSession(t, f.s)
	read := func() *platformSettingsMigrationRecovery {
		settings, err := f.s.platformInstallationSettings(session, f.lease.installationID)
		if err != nil {
			t.Fatal(err)
		}
		return settings.MigrationRecovery
	}
	if got := read(); got == nil || got.State != "settling" || got.OperationID != f.op || len(got.Choices) != 0 {
		t.Fatalf("settling: %+v", got)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, f.lease.installationID)
	if got := read(); got == nil || got.State != "blocked" || len(got.Choices) != 0 {
		t.Fatalf("blocked: %+v", got)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence-1 WHERE id=?`, f.lease.installationID)
	g := settledFixture(t, false)
	gs := platformManagedFixtureSession(t, g.s)
	settings, err := g.s.platformInstallationSettings(gs, g.lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	got := settings.MigrationRecovery
	if got == nil || got.State != "settled" || got.OperationID != g.op || len(got.Choices) != 2 ||
		got.Choices[0].Kind != "rollback" || got.Choices[0].RecoverySetID != recoverySetOf(t, g.s, g.op) ||
		got.Choices[1].Kind != "retry" || got.Choices[1].RecoversOperationID != g.op || got.Choices[1].TrustRevision == "" || !platformValidSHA(got.Choices[1].ReleaseSHA256) {
		t.Fatalf("settled: %+v", got)
	}
	mustExec(t, g.s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, g.lease.installationID)
	if settings, err = g.s.platformInstallationSettings(gs, g.lease.installationID); err != nil || settings.MigrationRecovery != nil {
		t.Fatal("a recovered installation still offers choices", err)
	}
}

// A retry's migrate-data names the failed update it recovers and the source removal that update really made: the
// runner holds no row under the portal's own copy of that evidence, and admits only the removal of the update the
// retry recovers. A retry of a retry names the update it recovers and still carries the root's removal.
func TestRetryMigrateCommandNamesRecoveredUpdateAndRealRemoval(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := settledFixture(t, false)
	fx := lastPlatformUpdateFixture
	realRemoval := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=1 AND kind='remove-code'`, f.op)
	var sent []control.MigrateDataPayload
	answer := func(state, outcome string) {
		f.fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
			sent = append(sent, i.command.Payload)
			return platformMigrateAnswers(i, f.endpoint, state, outcome), nil
		}
	}
	second := retryToMigrate(t, f, fx, platformFixtureID(7330), platformFixtureID(7331), f.op)
	answer("failed", "exit_nonzero")
	if _, err := f.s.dispatchPlatformMigrateData(context.Background(), second, f.endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].RecoversOperationID != f.op || sent[0].RemoveCodeEffectID != realRemoval {
		t.Fatalf("the first retry's migration: %+v (the real removal is %s)", sent, realRemoval)
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	retry := &settleFixture{s: f.s, lease: f.lease, endpoint: f.endpoint, fake: f.fake, op: second.id}
	retry.step(t)
	retry.step(t)
	third := retryToMigrate(t, f, fx, platformFixtureID(7340), platformFixtureID(7341), second.id)
	sent = nil
	answer("migrated", "")
	if _, err := f.s.dispatchPlatformMigrateData(context.Background(), third, f.endpoint); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].RecoversOperationID != second.id || sent[0].RemoveCodeEffectID != realRemoval {
		t.Fatalf("the second retry's migration: %+v (the real removal is %s)", sent, realRemoval)
	}
}
