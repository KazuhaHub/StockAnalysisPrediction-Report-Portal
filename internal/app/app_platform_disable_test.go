package app

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformFrontendDisablePreservesData(t *testing.T, pg bool) {
	s, actor := platformRemovalFixture(t, pg, false)
	beforeData := scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces`)
	beforeAccess := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`)
	plan, err := s.createPlatformRemovalReview(actor, platformFixtureID(1), appplatform.ActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(20)); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendUninstall(context.Background(), platformFixtureID(30))
	if err != nil || selected.lease.id == "" || selected.lease.snapshot.Action != appplatform.ActionDisable {
		t.Fatal("frontend disable claim", selected, err)
	}
	if _, err := s.runPlatformFrontendDisable(context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations`) != "disabled:disabled" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations`) != "complete" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE state='active'`) != 0 ||
		scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces`) != beforeData ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != beforeAccess {
		t.Fatal("frontend disable removed retained data or authority rows")
	}
}

func testPlatformManagedDisableWaitsForStopEvidence(t *testing.T, pg bool) {
	s, _, lease := platformManagedRemovalStopFixtureAction(t, pg, appplatform.ActionDisable)
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, lease.installationID)
	pluginTable := platformPluginDataTableName(appID)
	beforeData := scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID)
	beforeAccess := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if intent.command.Stage != "stop" || intent.command.Kind != "stop" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "disabled:disabled" {
		t.Fatal("managed disable prepared the wrong intent")
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(
		platformManagedRemovalObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("managed disable stop observation refused")
	}
	completed, err := s.settlePlatformManagedRemoval(
		context.Background(), lease, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("managed disable completion", completed.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "disabled:disabled" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "complete" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='stop' AND state='applied'`, lease.id) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != beforeAccess ||
		scalar[int](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != beforeData ||
		!s.st.tableExists(pluginTable) {
		t.Fatal("managed disable deleted retained data, code or grant rows")
	}
}

func testPlatformManagedDisableOfflineIsTruthful(t *testing.T, pg bool) {
	t.Run("before dispatch", func(t *testing.T) {
		s, _, lease := platformManagedRemovalStopFixtureAction(t, pg, appplatform.ActionDisable)
		if err := s.finishPlatformManagedUninstallForReview(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "failed:managed_disable_unavailable" ||
			scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "disabled:disabled" ||
			scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id) != 0 {
			t.Fatal("offline disable claimed a process stop it never requested")
		}
	})
	t.Run("after durable intent", func(t *testing.T) {
		s, _, lease := platformManagedRemovalStopFixtureAction(t, pg, appplatform.ActionDisable)
		intent, err := s.preparePlatformManagedRemovalIntent(context.Background(), lease)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.finishPlatformManagedUninstallForReview(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required:managed_disable_stop_pending" ||
			scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "disabled:recovery_required" ||
			scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE effect_id=?`, intent.command.EffectID) != "prepared" {
			t.Fatal("unconfirmed managed disable was reported as stopped")
		}
	})
}

func TestPlatformFrontendDisableSchedulerPreservesPluginData(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"retained across disable"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(1210))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(1211), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.currentStatus().code != "complete" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "disabled:disabled" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, installation) == 0 ||
		!s.st.tableExists(pluginTable) {
		t.Fatal("disable scheduler cleared retained plugin data or authority")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err == nil {
		t.Fatal("disabled app remained launchable")
	}
}

func testPlatformManagedDisableSchedulerRequiresRecovery(t *testing.T, pg bool) {
	s, actor, installed := platformManagedAccessFixture(t, pg)
	plan, err := s.createPlatformRemovalReview(actor, installed.installationID, appplatform.ActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(actor, plan.ID, plan.SHA256, platformFixtureID(1212))
	if err != nil {
		t.Fatal(err)
	}
	scheduler := platformSchedulerFixture(t, s)
	for attempt := 0; attempt < platformInstallAttemptLimit; attempt++ {
		_ = scheduler.tick(context.Background())
	}
	if scheduler.currentStatus().code != "review_required" ||
		scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "recovery_required:managed_disable_stop_pending" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installed.installationID) != "disabled:recovery_required" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, installed.installationID) == 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces WHERE owner_installation_id=? AND state='active'`, installed.installationID) != 1 {
		t.Fatal("managed disable scheduler misreported an unavailable runner")
	}
}

func TestPlatformFrontendDisablePreservesDataSQLite(t *testing.T) {
	testPlatformFrontendDisablePreservesData(t, false)
}

func TestPlatformFrontendDisablePreservesDataPostgres(t *testing.T) {
	testPlatformFrontendDisablePreservesData(t, true)
}

func TestPlatformManagedDisableWaitsForStopEvidenceSQLite(t *testing.T) {
	testPlatformManagedDisableWaitsForStopEvidence(t, false)
}

func TestPlatformManagedDisableWaitsForStopEvidencePostgres(t *testing.T) {
	testPlatformManagedDisableWaitsForStopEvidence(t, true)
}

func TestPlatformManagedDisableOfflineIsTruthfulSQLite(t *testing.T) {
	testPlatformManagedDisableOfflineIsTruthful(t, false)
}

func TestPlatformManagedDisableOfflineIsTruthfulPostgres(t *testing.T) {
	testPlatformManagedDisableOfflineIsTruthful(t, true)
}

func TestPlatformManagedDisableSchedulerRequiresRecoverySQLite(t *testing.T) {
	testPlatformManagedDisableSchedulerRequiresRecovery(t, false)
}

func TestPlatformManagedDisableSchedulerRequiresRecoveryPostgres(t *testing.T) {
	testPlatformManagedDisableSchedulerRequiresRecovery(t, true)
}
