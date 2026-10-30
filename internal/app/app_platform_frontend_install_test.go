package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func testPlatformFrontendInstall(t *testing.T, pg bool) {
	s, actor, release := platformInstallFixture(t, pg)
	review, err := s.createPlatformFrontendInstallReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(950))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(951))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.runPlatformStaticInstall(context.Background(), lease, platformRuntimeEndpoint{})
	if err != nil || result.activation.installationID != admitted.InstallationID {
		t.Fatal("frontend-only install needed a runner", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 0 {
		t.Fatal("frontend-only install provisioned runtime/data/effect")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE data_namespace_id IS NULL AND data_format=0 AND state='active'`) != 1 {
		t.Fatal("frontend-only activation manufactured data")
	}
	binding, err := s.inspectPlatformLaunchAccess(actor, admitted.InstallationID)
	if err != nil {
		t.Fatal("frontend-only result cannot launch", err)
	}
	doc, err := s.readPlatformLaunchDocumentContext(context.Background(), actor, binding)
	if err != nil || len(doc.Content) == 0 {
		t.Fatal("frontend-only document unavailable", err)
	}
}
func TestPlatformFrontendInstallSQLite(t *testing.T)   { testPlatformFrontendInstall(t, false) }
func TestPlatformFrontendInstallPostgres(t *testing.T) { testPlatformFrontendInstall(t, true) }

func platformFrontendQueuedFixture(t *testing.T, pg bool) (*Server, platformActorSession, string) {
	t.Helper()
	s, actor, release := platformInstallFixture(t, pg)
	review, err := s.createPlatformFrontendInstallReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(952))
	if err != nil {
		t.Fatal(err)
	}
	return s, actor, admitted.OperationID
}
func testPlatformFrontendScheduler(t *testing.T, pg bool) {
	s, actor, id := platformFrontendQueuedFixture(t, pg)
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(953), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.tick(context.Background()); err != nil || w.currentStatus().code != "complete" {
		t.Fatal("no-data scheduler needed native infrastructure", err, w.currentStatus())
	}
	installation := scalar[string](t, s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, id)
	if _, err = s.inspectPlatformLaunchAccess(actor, installation); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_data_namespaces`)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects`) != 0 {
		t.Fatal("no-data worker touched native storage")
	}
}
func TestPlatformFrontendSchedulerSQLite(t *testing.T)   { testPlatformFrontendScheduler(t, false) }
func TestPlatformFrontendSchedulerPostgres(t *testing.T) { testPlatformFrontendScheduler(t, true) }
func TestPlatformFrontendServiceSQLite(t *testing.T) {
	s, actor, id := platformFrontendQueuedFixture(t, false)
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(954), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	done := make(chan error, 1)
	go func() { done <- w.serve(ctx) }()
	stopped := false
	defer func() {
		cancel()
		if !stopped {
			<-done
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, id)
		if stage == "complete" {
			break
		}
		select {
		case err := <-done:
			stopped = true
			t.Fatal("frontend service ended early", err, w.currentStatus())
		case <-ctx.Done():
			t.Fatal("frontend service did not complete", w.currentStatus())
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-done; err != nil {
		stopped = true
		t.Fatal(err)
	}
	stopped = true
	installation := scalar[string](t, s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, id)
	if _, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil {
		t.Fatal(err)
	}
}
func testPlatformFrontendProofRefusals(t *testing.T, pg bool) {
	for _, kind := range []string{"proof", "document", "epoch", "binding", "schema", "review", "effect", "namespace", "quota", "pause", "recipient"} {
		t.Run(kind, func(t *testing.T) {
			s, actor, id := platformFrontendQueuedFixture(t, pg)
			selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(955))
			if err != nil {
				t.Fatal(err)
			}
			result, err := s.runPlatformStaticInstall(context.Background(), selected.lease, platformRuntimeEndpoint{})
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "proof":
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence='{}'`)
			case "document":
				raw := scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations`)
				var proof platformFrontendReadiness
				if json.Unmarshal([]byte(raw), &proof) != nil {
					t.Fatal("invalid fixture")
				}
				proof.DocumentSHA = strings.Repeat("0", 64)
				encoded, _ := json.Marshal(proof)
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?`, string(encoded))
			case "epoch", "binding", "schema":
				raw := scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations`)
				var proof platformFrontendReadiness
				if json.Unmarshal([]byte(raw), &proof) != nil {
					t.Fatal("invalid proof fixture")
				}
				switch kind {
				case "epoch":
					proof.Epoch = platformFixtureID(969)
				case "binding":
					proof.Installation = platformFixtureID(970)
				case "schema":
					proof.DeclaredSchema++
				}
				encoded, _ := json.Marshal(proof)
				mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=?`, string(encoded))
			case "review":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=?`, strings.Repeat("0", 64))
			case "quota":
				mustExec(t, s.st, `UPDATE app_platform_activations SET data_format=1`)
			case "pause":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "recipient":
				mustExec(t, s.st, `UPDATE users SET created_at=created_at||'-changed' WHERE username=?`, actor.username)
			case "namespace":
				platformTestInsert(t, s.st.poolExec(), "data_namespaces", map[string]any{"id": platformFixtureID(956), "origin_installation_id": result.activation.installationID, "owner_installation_id": result.activation.installationID, "state": "active", "data_format": 1, "quota_bytes": 1, "observed_bytes": 0, "writer_fence": 1, "revision": 1, "retained_ms": nil, "deleted_ms": nil, "created_ms": 1, "updated_ms": 1})
			case "effect":
				platformTestInsert(t, s.st.poolExec(), "operation_effects", map[string]any{"operation_id": id, "stage": "prepare_data", "effect_number": 1, "effect_id": platformFixtureID(957), "kind": "prepare-data", "payload_sha256": strings.Repeat("a", 64), "runtime_id": nil, "controller_epoch": selected.lease.controllerEpoch, "admitted_fence": 1, "observed_fence": 1, "state": "applied", "receipt_json": "{}", "created_ms": 1, "updated_ms": 1})
			}
			if _, err := s.inspectPlatformLaunchAccess(actor, result.activation.installationID); err == nil {
				t.Fatal("invalid no-data activation launched", kind)
			}
		})
	}
}
func TestPlatformFrontendProofRefusalsSQLite(t *testing.T) {
	testPlatformFrontendProofRefusals(t, false)
}
func TestPlatformFrontendProofRefusalsPostgres(t *testing.T) {
	testPlatformFrontendProofRefusals(t, true)
}

func TestPlatformFrontendDefaultNoImplicitAdminSQLite(t *testing.T) {
	s, actor, release := platformInstallFixture(t, false)
	review, err := s.createPlatformInstallReview(actor, release.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(review.JSON), &p) != nil || p.Format != platformFrontendInstallFormat || p.DataQuotaBytes != 0 {
		t.Fatal("generic installer did not default to no data")
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(958))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(959))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.runPlatformStaticInstall(context.Background(), selected.lease, platformRuntimeEndpoint{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.inspectPlatformLaunchAccess(actor, admitted.InstallationID); err == nil {
		t.Fatal("administrator bypassed empty app audience")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("empty audience acquired implicit access")
	}
}

func TestPlatformFrontendHTTPWithoutRunnerSQLite(t *testing.T) {
	s, actor, id := platformFrontendQueuedFixture(t, false)
	selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(960))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.runPlatformStaticInstall(context.Background(), selected.lease, platformRuntimeEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	response := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+result.activation.installationID+"/launch", cookie, "")
	var payload struct {
		Source string `json:"source"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &payload) != nil || payload.Source == "" {
		t.Fatal("no-runner HTTP launch failed", response.Code, response.Body.String())
	}
	document := sessionTestRequest(mux, "GET", payload.Source, cookie, "")
	if document.Code != 200 || !strings.Contains(document.Body.String(), "Fixture") || document.Header().Get("Cache-Control") != "no-store" || !strings.Contains(document.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatal("no-runner document policy failed", document.Code)
	}
	directory, err := s.listPlatformApps(context.Background(), actor, "")
	if err != nil || len(directory.Items) != 1 || !directory.Items[0].Openable {
		t.Fatal("no-data app not visible in authorized directory", err)
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=(SELECT installation_id FROM app_platform_operations WHERE id=?)`, id)
	if got := sessionTestRequest(mux, "GET", payload.Source, cookie, ""); got.Code != 404 {
		t.Fatal("no-data document survived access revocation", got.Code)
	}
}
func TestPlatformFrontendDoesNotBecomeNativeSQLite(t *testing.T) {
	s, _, _ := platformFrontendQueuedFixture(t, false)
	native, err := s.claimNextPlatformStaticInstall(context.Background(), platformFixtureID(961))
	if err != nil || native.lease.id != "" {
		t.Fatal("native worker claimed frontend-only job", err)
	}
	selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(962))
	if err != nil {
		t.Fatal(err)
	}
	lease := selected.lease
	for range 4 {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := dumpOf(t, s.st)
	if _, err := s.preparePlatformInstallData(context.Background(), lease); err == nil {
		t.Fatal("frontend job minted native command")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("native rejection mutated frontend job")
	}
}
func TestPlatformFrontendRequiredStorageRefusedSQLite(t *testing.T) {
	s, actor, _ := platformInstallFixture(t, false)
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.2")
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Capabilities.Required = []string{"storage.files"}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.createPlatformInstallReview(actor, release.sha256, 1); err == nil {
		t.Fatal("storage requirement silently declined by no-data provider")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != 0 {
		t.Fatal("unsupported requirements created confirmable plan")
	}
}

func testPlatformFrontendRecoveryWithoutResources(t *testing.T, pg bool) {
	s, actor, _ := platformFrontendQueuedFixture(t, pg)
	selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(965))
	if err != nil {
		t.Fatal(err)
	}
	old := selected.lease
	for range 4 {
		old, err = s.st.advancePlatformInstallStaging(old)
		if err != nil {
			t.Fatal(err)
		}
	}
	old, err = s.st.advancePlatformInstallLocalStage(context.Background(), old, true)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0`)
	resumed, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(966))
	if err != nil || resumed.reviewed || resumed.lease.snapshot.Stage != appplatform.StageActivate || resumed.lease.fence != old.fence+1 {
		t.Fatal("effect-free frontend activation could not resume", err)
	}
	if _, err = s.activatePlatformStaticInstall(context.Background(), old); err == nil {
		t.Fatal("old frontend worker survived takeover")
	}
	result, err := s.runPlatformStaticInstall(context.Background(), resumed.lease, platformRuntimeEndpoint{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.inspectPlatformLaunchAccess(actor, result.activation.installationID); err != nil {
		t.Fatal(err)
	}
}
func TestPlatformFrontendRecoverySQLite(t *testing.T) {
	testPlatformFrontendRecoveryWithoutResources(t, false)
}
func TestPlatformFrontendRecoveryPostgres(t *testing.T) {
	testPlatformFrontendRecoveryWithoutResources(t, true)
}
func testPlatformFrontendActivationRollback(t *testing.T, pg bool) {
	s, actor, _ := platformFrontendQueuedFixture(t, pg)
	selected, err := s.claimNextPlatformFrontendInstall(context.Background(), platformFixtureID(967))
	if err != nil {
		t.Fatal(err)
	}
	lease := selected.lease
	for range 4 {
		lease, err = s.st.advancePlatformInstallStaging(lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	lease, err = s.st.advancePlatformInstallLocalStage(context.Background(), lease, true)
	if err != nil {
		t.Fatal(err)
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_frontend_activation_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture activation failure';END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_frontend_activation_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.install_activated') EXECUTE FUNCTION fixture_frontend_activation_failure()`)
		t.Cleanup(func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_frontend_activation_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_frontend_activation_failure()`)
		})
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_frontend_activation_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.install_activated' BEGIN SELECT RAISE(ABORT,'fixture activation failure');END`)
	}
	before := dumpOf(t, s.st)
	if _, err = s.activatePlatformStaticInstall(context.Background(), lease); err == nil {
		t.Fatal("no-data activation ignored failed audit")
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("no-data activation partly committed")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER fixture_frontend_activation_failure ON audit_log`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER fixture_frontend_activation_failure`)
	}
	result, err := s.activatePlatformStaticInstall(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.inspectPlatformLaunchAccess(actor, result.installationID); err != nil {
		t.Fatal(err)
	}
}
func TestPlatformFrontendActivationRollbackSQLite(t *testing.T) {
	testPlatformFrontendActivationRollback(t, false)
}
func TestPlatformFrontendActivationRollbackPostgres(t *testing.T) {
	testPlatformFrontendActivationRollback(t, true)
}
func TestPlatformFrontendIgnoresUnusableRuntimeSQLite(t *testing.T) {
	s, actor, _ := platformFrontendQueuedFixture(t, false)
	if _, err := s.preparePlatformRuntimeBinding(actor, platformFixtureID(400)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=1,revision=2,sealed_control_key='unusable'`)
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(968), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.tick(context.Background()); err != nil || w.currentStatus().code != "complete" {
		t.Fatal("frontend install depended on runtime key/availability", err)
	}
	if scalar[string](t, s.st, `SELECT sealed_control_key FROM app_platform_runtime_bindings`) != "unusable" {
		t.Fatal("frontend path repaired/touched runtime configuration")
	}
}
