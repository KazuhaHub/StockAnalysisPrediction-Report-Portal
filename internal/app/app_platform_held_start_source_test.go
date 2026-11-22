package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// An import or an enable that failed with its start held leaves the installation disabled in recovery_required at the
// generation it had. The settlement of that start reads its source from there, whichever lifecycle operation the
// source is: the install itself, or an update or an earlier enable.
func platformRequireHeldStartSource(t *testing.T, s *Server, installation, epoch string, runtime platformRuntimeRegistration, action appplatform.Action) {
	t.Helper()
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	if _, err := s.st.exec(`UPDATE app_platform_installations SET phase='recovery_required' WHERE id=?`, installation); err != nil {
		t.Fatal(err)
	}
	lease := platformOperationLease{installationID: installation, controllerEpoch: epoch, snapshot: appplatform.OperationSnapshot{
		Action: action, Managed: true, Generation: uint64(generation), Revision: 1, Stage: appplatform.StageRecoveryRequired}}
	if _, err := loadPlatformCheckpointSourceMode(s.st.poolExec(), lease, runtime, now, true, 0); err != nil {
		t.Fatalf("the source of a held %s start is not readable from recovery_required: %v", action, err)
	}
	// A lease that is not at the recovery stage is not given that reading.
	lease.snapshot.Stage = appplatform.StageQueued
	if _, err := loadPlatformCheckpointSourceMode(s.st.poolExec(), lease, runtime, now, true, 0); err == nil {
		t.Fatalf("a queued %s read its source from recovery_required", action)
	}
}

func TestPlatformHeldStartSourceUpdateSourcedSQLite(t *testing.T) {
	s, actor, enabled, _, runtime := platformDataActionFixture(t, false)
	platformCompleteSyntheticDisable(t, s, actor, enabled)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, enabled.installationID)
	platformRequireHeldStartSource(t, s, enabled.installationID, epoch, runtime, appplatform.ActionImportData)
}

func TestPlatformHeldStartSourceInstallSourcedSQLite(t *testing.T) {
	s, _, lease := platformManagedRemovalStopFixtureAction(t, false, appplatform.ActionDisable)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(platformManagedRemovalObservationFixture(intent, endpoint), intent, endpoint)
	if !ok {
		t.Fatal("disable stop receipt")
	}
	if _, err := s.settlePlatformManagedRemoval(context.Background(), lease, intent, receipt); err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	platformRequireHeldStartSource(t, s, lease.installationID, epoch, runtime, appplatform.ActionImportData)
}
