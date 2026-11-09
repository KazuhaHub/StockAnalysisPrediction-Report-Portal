package app

import (
	"context"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformTrustQuarantineFrontend(t *testing.T, pg bool) {
	s, actor, reader, installation := platformObjectsFixturePG(t, pg)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(
		t, s, reader, installation,
		platformObjectCreateBody(binding, `{"body":"retain during quarantine"}`)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	beforeData := scalar[int64](t, s.st, `
		SELECT observed_bytes FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`, installation)
	revision, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "revoke_key", publisherID: "fixture-publisher",
			expectedRevision: 1, keyID: "first-key",
			expectedKeyRevision: 1,
		})
	if err != nil || revision.quarantined != 1 {
		t.Fatal("revocation did not quarantine the running app", revision, err)
	}
	if scalar[string](t, s.st, `
		SELECT desired_state||':'||phase FROM app_platform_installations
		WHERE id=?`, installation) != "disabled:disabled" ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_operations
			WHERE installation_id=? AND action='disable'
			  AND managed=0 AND stage='queued'`, installation) != 1 ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active'`, installation) != 0 {
		t.Fatal("quarantine did not revoke live authority and queue stop")
	}
	if !s.st.tableExists(pluginTable) ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM `+pluginTable) != 1 ||
		scalar[string](t, s.st, `
			SELECT state FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`, installation) != "active" ||
		scalar[int64](t, s.st, `
			SELECT observed_bytes FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`, installation) != beforeData {
		t.Fatal("quarantine deleted code, object or managed data")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err == nil {
		t.Fatal("quarantined app remained launchable")
	}
	if _, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "revoke_key", publisherID: "fixture-publisher",
			expectedRevision: 2, keyID: "first-key",
			expectedKeyRevision: 2,
		}); err != nil || scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_operations
			WHERE installation_id=? AND action='disable'`,
		installation) != 1 {
		t.Fatal("repeated revocation duplicated quarantine work", err)
	}
	scheduler, err := newPlatformInstallScheduler(
		s, platformRuntimeEndpoint{}, platformFixtureID(995), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `
		SELECT stage FROM app_platform_operations
		WHERE installation_id=? AND action='disable'`, installation) != "complete" ||
		!s.st.tableExists(pluginTable) ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM `+pluginTable) != 1 {
		t.Fatal("quarantine stop completed with data loss")
	}
}

func TestPlatformTrustQuarantineFrontendSQLite(t *testing.T) {
	testPlatformTrustQuarantineFrontend(t, false)
}

func TestPlatformTrustQuarantineFrontendPostgres(t *testing.T) {
	testPlatformTrustQuarantineFrontend(t, true)
}

func testPlatformTrustQuarantineManaged(t *testing.T, pg bool) {
	s, actor, lease := platformManagedAccessFixture(t, pg)
	beforeWriterFence := scalar[int64](t, s.st, `
		SELECT writer_fence FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`, lease.installationID)
	revision, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "policy", publisherID: "fixture-publisher",
			expectedRevision: 1, enabled: false,
			namespaces: []appplatform.PublisherNamespace{{
				Kind: "prefix", Value: "notes-",
			}},
		})
	if err != nil || revision.quarantined != 1 {
		t.Fatal("publisher disable did not quarantine managed app", revision, err)
	}
	if scalar[string](t, s.st, `
		SELECT desired_state||':'||phase FROM app_platform_installations
		WHERE id=?`, lease.installationID) != "disabled:disabled" ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_operations
			WHERE installation_id=? AND action='disable'
			  AND managed=1 AND stage='queued'`, lease.installationID) != 1 ||
		scalar[int](t, s.st, `
			SELECT COUNT(*) FROM app_platform_runtime_bindings
			WHERE enabled=1`) != 1 {
		t.Fatal("managed quarantine lost stop intent or runtime identity")
	}
	if scalar[string](t, s.st, `
		SELECT state FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`, lease.installationID) != "active" ||
		scalar[int64](t, s.st, `
			SELECT writer_fence FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`, lease.installationID) != beforeWriterFence+1 {
		t.Fatal("managed quarantine did not fence but retain data")
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("quarantined managed app remained launchable")
	}
	selected, err := s.claimNextPlatformManagedUninstall(
		context.Background(), platformFixtureID(996))
	if err != nil || selected.lease.id == "" ||
		selected.lease.snapshot.Action != appplatform.ActionDisable {
		t.Fatal("managed quarantine was not claimable", selected, err)
	}
	if err := s.finishPlatformManagedUninstallForReview(
		context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `
		SELECT stage||':'||error_code FROM app_platform_operations
		WHERE id=?`, selected.lease.id) != "failed:managed_disable_unavailable" ||
		scalar[string](t, s.st, `
			SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "disabled:disabled" ||
		scalar[string](t, s.st, `
			SELECT state FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`, lease.installationID) != "active" ||
		scalar[int64](t, s.st, `
			SELECT writer_fence FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`,
			lease.installationID) != beforeWriterFence+1 {
		t.Fatal("unavailable runner fabricated a stop or damaged data")
	}
}

func TestPlatformTrustQuarantineManagedSQLite(t *testing.T) {
	testPlatformTrustQuarantineManaged(t, false)
}

func TestPlatformTrustQuarantineManagedPostgres(t *testing.T) {
	testPlatformTrustQuarantineManaged(t, true)
}

// A revocation quarantines every affected installation. One whose load has no verdict cannot take a new operation
// yet; that must not abort the revocation. Fails if the revocation errors, if it quarantines the blocked
// installation anyway, or if the deferral leaves no record.
func TestTrustRevocationSkipsAnInstallationWithAnUnsettledLoadAndRecordsWhy(t *testing.T) {
	s, actor, _, installation := platformObjectsFixturePG(t, false)
	operation := scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? ORDER BY created_ms LIMIT 1`, installation)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT o.id,'stage',1,?,'load_image',?,NULL,o.controller_epoch,1,0,'unknown','{}',o.created_ms,o.updated_ms FROM app_platform_operations o WHERE o.id=?`,
		platformFixtureID(8901), platformDigest([]byte("load")), operation)
	revision, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{
		kind: "revoke_key", publisherID: "fixture-publisher", expectedRevision: 1, keyID: "first-key", expectedKeyRevision: 1})
	if err != nil || revision.quarantined != 0 {
		t.Fatalf("the revocation was aborted or quarantined the blocked installation: %+v %v", revision, err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='disable'`, installation) != 0 {
		t.Fatal("a disable was admitted for an installation with an unsettled load")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.trust_quarantine_deferred' AND target_id=?`, installation) != 1 {
		t.Fatal("the deferral was not recorded")
	}
}
