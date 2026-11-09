package app

import (
	"context"
	"errors"
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

// A trust revocation never skips an installation, whatever its load is doing. With a load that has no verdict the
// quarantine still retires and disables the activation, bumps the fence, fences the data writer and queues the
// stop; launch is refused; the load is later settled as an outcome Portal does not know; and a start-bearing
// operation is refused until then. Fails if any part of the quarantine is deferred or lost.
func TestTrustRevocationFullyQuarantinesAnInstallationWithAnUnsettledLoad(t *testing.T) {
	s, actor, lease := platformManagedAccessFixture(t, false)
	writerBefore := scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID)
	fenceBefore := scalar[int64](t, s.st, `SELECT control_fence FROM app_platform_installations WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT o.id,'stage',1,?,'load_image',?,i.runtime_id,o.controller_epoch,1,0,'unknown','{}',o.created_ms,o.updated_ms
		FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.id=?`,
		platformFixtureID(8901), platformDigest([]byte("load")), lease.id)
	revision, err := s.changePlatformPublisherTrust(actor, platformPublisherChange{
		kind: "policy", publisherID: "fixture-publisher", expectedRevision: 1, enabled: false,
		namespaces: []appplatform.PublisherNamespace{{Kind: "prefix", Value: "notes-"}}})
	if err != nil || revision.quarantined != 1 {
		t.Fatalf("the revocation skipped or failed the installation: %+v %v", revision, err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "disabled:disabled" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, lease.installationID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='disable' AND stage='queued'`, lease.installationID) != 1 {
		t.Fatal("the quarantine did not retire and disable the activation and queue the stop")
	}
	if scalar[int64](t, s.st, `SELECT control_fence FROM app_platform_installations WHERE id=?`, lease.installationID) != fenceBefore+1 ||
		scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != writerBefore+1 {
		t.Fatal("the quarantine did not bump the fence and the data writer fence")
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("the quarantined app remained launchable")
	}
	// Service identity is refused for the quarantined installation (it needs an enabled, ready one with an active
	// activation), which is what stops its tasks and event deliveries from being issued credentials.
	if _, err := loadPlatformServiceFacts(s.st.poolExec(), lease.installationID, nil, false); !errors.Is(err, errAppPlatformServiceUnavailable) {
		t.Fatalf("service facts were available after the quarantine: %v", err)
	}
	// A start-bearing operation waits for the load; the stop did not.
	if err := tryAdmit(t, s, lease.installationID, "update"); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("a start was admitted while the load had no verdict: %v", err)
	}
	// The fence moved, so the load can never settle under its lease: the driver settles it as unknown-outcome.
	endpoint := platformRuntimeEndpoint{path: "/run/test", controllerUID: 1, controllerGID: 1}
	if _, err := s.settlePlatformLoadImages(context.Background(), endpoint); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT state||receipt_json FROM app_platform_operation_effects WHERE kind='load_image'`) != `reverted{"outcome":"abandoned_outcome_unknown"}` {
		t.Fatal("the load was not settled as abandoned_outcome_unknown")
	}
	if err := tryAdmit(t, s, lease.installationID, "update"); errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("the installation is still blocked after the load settled")
	}
}

// Defence in depth: service identity re-verifies the publisher's trust, as the launch path does, so a revoked key
// stops it before any quarantine has run. Fails if a service identity is issued for a release whose key was revoked.
func TestServiceIdentityReverifiesPublisherTrust(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	if _, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5150), []string{"reports.read"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_publisher_keys SET revoked_ms=1`)
	if _, _, err := s.issuePlatformServiceIdentity(context.Background(), installation, platformFixtureID(5151), []string{"reports.read"}, time.Minute); err == nil {
		t.Fatal("a service identity was issued for a release whose key was revoked")
	}
}
