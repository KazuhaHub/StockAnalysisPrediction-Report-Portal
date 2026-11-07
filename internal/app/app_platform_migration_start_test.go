package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func platformReadinessFor(intent platformUpdateStartIntent) journal.UpdateStartReadiness {
	return journal.UpdateStartReadiness{
		Format:               "portal.runner.update-start-readiness/1-dev.1",
		PayloadSHA256:        intent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(mustMarshalPlatformUpdateStartReceipt(*intent.running)),
		NativeID:             intent.running.NativeID, Protocol: "portal.backend/1",
		DataFormat: intent.command.Payload.DataFormat,
		ObservedMS: intent.running.NativeObservedMS,
	}
}

// platformMigratedUpdateToStart applies the migration through the fake runner and returns the lease at the
// start stage.
func platformMigratedUpdateToStart(t *testing.T, pg bool) (*Server, platformOperationLease, platformRuntimeEndpoint, platformManagedUpdatePlan) {
	t.Helper()
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	start, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || start.snapshot.Stage != appplatform.StageStart {
		t.Fatal("migration did not reach the start", start.snapshot, err)
	}
	var plan platformManagedUpdatePlan
	raw := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, lease.id)
	if err := jsonUnmarshalPlan(raw, &plan); err != nil {
		t.Fatal(err)
	}
	return s, start, endpoint, plan
}

// platformMigratedUpdateToRegistration drives the start, probe and registration with the runner's receipts,
// leaving the update at the activation.
func platformMigratedUpdateToRegistration(t *testing.T, s *Server, start platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, platformUpdateStartRegistrationIntent, platformUpdateStartTestObservation, platformUpdateStartIntent) {
	t.Helper()
	intent, err := s.preparePlatformUpdateStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	reservationObservation := platformUpdateStartReservationFixture(intent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(reservationObservation, intent, endpoint)
	if !ok {
		t.Fatal("reservation observation refused")
	}
	admission, err := s.commitPlatformUpdateStartReservation(context.Background(), start, intent, endpoint, reservationObservation, reservation)
	if err != nil {
		t.Fatal(err)
	}
	running := platformUpdateStartRunningFixture(admission.intent, reservation, endpoint)
	probeLease, err := s.commitPlatformUpdateStartReceipt(context.Background(), start, admission.intent, endpoint, running)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := s.preparePlatformUpdateProbe(context.Background(), probeLease)
	if err != nil || probe.running == nil {
		t.Fatal("prepare probe", err)
	}
	readiness := platformUpdateStartReservationFixture(probe, endpoint)
	readiness.readiness = platformReadinessFor(probe)
	activate, err := s.commitPlatformUpdateStartReadiness(context.Background(), probeLease, probe, endpoint, readiness)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.preparePlatformUpdateStartRegistration(context.Background(), activate)
	if err != nil {
		t.Fatal(err)
	}
	return activate, registration, platformUpdateStartRegistrationObservationForIntent(t, registration, endpoint), intent
}

// A migrated update starts under the fence the migration reserved: the checkpoint's S is the checkpoint fence,
// the migration's S+1 is the source fence and the target runs at S+2, with the data format it migrated to.
func testMigratedStartUsesFencePlus2AndTargetFormat(t *testing.T, pg bool) {
	s, start, endpoint, plan := platformMigratedUpdateToStart(t, pg)
	intent, err := s.preparePlatformUpdateStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	p := intent.command.Payload
	migrateEffect := scalar[string](t, s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, start.id)
	migrateReceipt := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, start.id)
	if p.CheckpointWriterFence != itoa64(plan.SourceWriterFence) || p.SourceWriterFence != itoa64(plan.SourceWriterFence+1) ||
		p.TargetWriterFence != itoa64(plan.SourceWriterFence+2) || p.DataFormat != "2" ||
		p.MigrateEffectID != migrateEffect || p.MigrateReceiptSHA256 != platformDigest([]byte(migrateReceipt)) {
		t.Fatalf("migrated start is not bound to the migration: %+v", p)
	}
	if scalar[int64](t, s.st, `SELECT data_format FROM app_platform_activations WHERE installation_id=? AND generation=2`, start.installationID) != 2 {
		t.Fatal("the candidate activation does not carry the migrated format")
	}
	// Without the applied migration there is no start.
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='reverted' WHERE operation_id=? AND stage='migrate' AND effect_number=2`, start.id)
	if _, err := s.preparePlatformUpdateStart(context.Background(), start); err == nil {
		t.Fatal("a start was prepared without an applied migration")
	}
	_ = endpoint
}

func TestMigratedStartUsesFencePlus2AndTargetFormatSQLite(t *testing.T) {
	testMigratedStartUsesFencePlus2AndTargetFormat(t, false)
}
func TestMigratedStartUsesFencePlus2AndTargetFormatPostgres(t *testing.T) {
	testMigratedStartUsesFencePlus2AndTargetFormat(t, true)
}

// platformMigratedUpdateActivated runs the whole update to its activation.
func platformMigratedUpdateActivated(t *testing.T, pg bool) (*Server, platformOperationLease, platformManagedUpdatePlan) {
	t.Helper()
	s, start, endpoint, plan := platformMigratedUpdateToStart(t, pg)
	activate, registration, observation, _ := platformMigratedUpdateToRegistration(t, s, start, endpoint)
	registered, err := s.commitPlatformUpdateStartRegistration(context.Background(), activate, registration, endpoint, observation)
	if err != nil {
		t.Fatal("registration", err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, registered, endpoint)
	defer applied.registration.Close()
	result, err := s.commitPlatformUpdateStartActivation(context.Background(), registered, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("activation", result, err)
	}
	return s, registered, plan
}

func testMigratedActivationSetsWriterFenceAndFormat(t *testing.T, pg bool) {
	s, lease, plan := platformMigratedUpdateActivated(t, pg)
	if scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != plan.SourceWriterFence+2 ||
		scalar[int64](t, s.st, `SELECT data_format FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND generation=2 AND data_format=2`, lease.installationID) != 1 ||
		scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "ready" {
		t.Fatal("activation did not take the migration's fence and format")
	}
}

func TestMigratedActivationSetsWriterFenceAndFormatSQLite(t *testing.T) {
	testMigratedActivationSetsWriterFenceAndFormat(t, false)
}
func TestMigratedActivationSetsWriterFenceAndFormatPostgres(t *testing.T) {
	testMigratedActivationSetsWriterFenceAndFormat(t, true)
}

// Fixture B: after a successful migration and activation the application launches on the migrated data.
func testMigratedLaunchAfterActivation(t *testing.T, pg bool) {
	platformWritableUpdateFixtures(t)
	s, lease, _ := platformMigratedUpdateActivated(t, pg)
	actor := platformManagedFixtureSession(t, s)
	launch, err := s.inspectPlatformLaunchAccess(actor, lease.installationID)
	if err != nil || launch.generation != "2" {
		t.Fatal("a migrated application cannot launch", launch, err)
	}
}

func TestMigratedLaunchAfterActivationSQLite(t *testing.T)   { testMigratedLaunchAfterActivation(t, false) }
func TestMigratedLaunchAfterActivationPostgres(t *testing.T) { testMigratedLaunchAfterActivation(t, true) }
