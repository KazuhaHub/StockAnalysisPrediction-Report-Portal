package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const (
	recoveryPlansPath      = "/api/admin/app-platform/plans"
	recoveryOperationsPath = "/api/admin/app-platform/operations"
	recoveryEndPath        = "/api/admin/app-platform/control/end-recovery"
)

func platformRecoveryActionHTTP(t *testing.T, s *Server, actor platformActorSession) platformRecoveryHTTP {
	t.Helper()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return platformRecoveryHTTP{t: t, mux: mux, cookie: s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}
}

// reviewAndCommit takes one recovery action through the plan and operation routes and returns the
// committed operation id.
func (h platformRecoveryHTTP) reviewAndCommit(action, installation, key string) map[string]any {
	h.t.Helper()
	review := h.json(h.post(recoveryPlansPath, `{"action":"`+action+`","installation_id":"`+installation+`"}`), 200)
	body := `{"plan_id":"` + h.str(review, "plan_id") + `","plan_sha256":"` + h.str(review, "plan_sha256") + `","idempotency_key":"` + key + `"}`
	committed := h.json(h.post(recoveryOperationsPath, body), 202)
	if replay := h.json(h.post(recoveryOperationsPath, body), 202); replay["replayed"] != true || replay["operation_id"] != committed["operation_id"] {
		h.t.Fatalf("%s replay: %v", action, replay)
	}
	return committed
}

func (h platformRecoveryHTTP) endRecovery(want int) {
	h.t.Helper()
	if got := h.post(recoveryEndPath, `{"action":"end_recovery"}`); got.Code != want {
		h.t.Fatalf("end-recovery answered %d, want %d: %s", got.Code, want, got.Body.String())
	}
}

// A restored static frontend app has no runner and no recovery set, so the only way back is to
// activate its restored release again on the current epoch, reviewed like every other recovery action.
func TestRecoveryResumesAStaticFrontendThroughRoutes(t *testing.T) {
	s, admin, _, installation := platformObjectsFixturePG(t, false)
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	platformSimulateRestore(t, s)
	h := platformRecoveryActionHTTP(t, s, admin)
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation)
	h.endRecovery(409)

	committed := h.reviewAndCommit("frontend_resume", installation, platformFixtureID(5900))
	if committed["stage"] != "complete" {
		t.Fatalf("frontend resume: %v", committed)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != generation+1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND generation=?`, installation, generation+1) != 1 ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, committed["operation_id"]) != "complete" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.frontend_resumed' AND target_id=?`, installation) != 1 {
		t.Fatal("the frontend was not activated again, or left no audit row")
	}
	// A settled installation is not a candidate for either recovery action.
	for _, action := range []string{"frontend_resume", "recovery_disable"} {
		if got := h.post(recoveryPlansPath, `{"action":"`+action+`","installation_id":"`+installation+`"}`); got.Code == 200 {
			t.Fatalf("%s was reviewed for a settled installation", action)
		}
	}
	h.endRecovery(200)
}

func TestRecoveryRefusesAFrontendWhosePublisherWasNotTrusted(t *testing.T) {
	s, admin, _, installation := platformObjectsFixturePG(t, false)
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
	platformSimulateRestore(t, s)
	h := platformRecoveryActionHTTP(t, s, admin)
	if got := h.post(recoveryPlansPath, `{"action":"frontend_resume","installation_id":"`+installation+`"}`); got.Code == 200 {
		t.Fatal("a frontend of a publisher disabled before the restore was reviewed for resume")
	}
	// It can still be disabled, which settles it.
	h.reviewAndCommit("recovery_disable", installation, platformFixtureID(5901))
	h.endRecovery(200)
}

// An installation with no way back (no recovery set, or one the administrator gives up on) is disabled
// through the same routes, and recovery ends only once every installation is settled that way.
func TestRecoveryDisablesRestoredInstallationsThroughRoutes(t *testing.T) {
	s, actor, updateLease, _ := platformManagedRestoreReviewFixture(t, false)
	first := updateLease.installationID
	publisher := scalar[string](t, s.st, `SELECT id FROM app_platform_publishers`)
	oldRuntime := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings`)
	platformSimulateRestore(t, s)
	second := platformFixtureID(5910)
	if err := platformSeedInstallation(t, s.st.poolExec(), second, publisher, "second-app"); err != nil {
		t.Fatal(err)
	}
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	mustExec(t, s.st, `UPDATE app_platform_installations SET runtime_id=?,desired_state='disabled',phase='review_required',generation=2,controller_epoch=? WHERE id=?`, oldRuntime, epoch, second)
	h := platformRecoveryActionHTTP(t, s, actor)
	h.endRecovery(409)
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, second)
	committed := h.reviewAndCommit("recovery_disable", second, platformFixtureID(5911))
	if committed["stage"] != "complete" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, second) != "disabled:disabled" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, second) != generation+1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.recovery_disabled' AND target_id=?`, second) != 1 {
		t.Fatalf("recovery disable: %v", committed)
	}
	h.endRecovery(409)
	h.reviewAndCommit("recovery_disable", first, platformFixtureID(5912))
	h.endRecovery(200)
	if got := h.post(recoveryPlansPath, `{"action":"recovery_disable","installation_id":"`+first+`"}`); got.Code != 409 || !strings.Contains(got.Body.String(), "app_platform_recovery_inactive") {
		t.Fatalf("recovery disable outside recovery: %d %s", got.Code, got.Body.String())
	}
}

// A resumed static frontend is an ordinary ready installation: it launches through the real route and
// can later take a frontend update, both of which re-verify the activation it was resumed with.
func TestRecoveryResumedFrontendLaunchesAndTakesAnUpdate(t *testing.T) {
	s, actor, installation, _ := platformUpdateFixture(t, []int{1})
	platformSimulateRestore(t, s)
	h := platformRecoveryActionHTTP(t, s, actor)
	h.reviewAndCommit("frontend_resume", installation, platformFixtureID(5920))
	h.endRecovery(200)
	// Trust in the publishers is an administrator decision taken once recovery has ended.
	mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=1,revision=revision+1`)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	launch := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+installation+"/launch", cookie, "")
	var err error
	if launch.Code != 200 {
		_, err = s.inspectPlatformLaunchAccess(actor, installation)
		t.Fatalf("a resumed frontend did not launch: %d %s (%v)", launch.Code, launch.Body.String(), err)
	}
	// The restore moved the publisher's trust revision, so a release is staged afresh for the update.
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.3")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = nil
	if raw, err = json.Marshal(manifest); err != nil {
		t.Fatal(err)
	}
	target, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, scalar[int64](t, s.st, `SELECT revision FROM app_platform_publishers`))
	if err != nil {
		t.Fatal("update review of a resumed frontend:", err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5921))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5922))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runPlatformStaticUpdate(context.Background(), lease); err != nil {
		t.Fatal("update of a resumed frontend:", err)
	}
	binding, err := s.inspectPlatformLaunchAccess(actor, installation)
	if err != nil || binding.releaseSHA != target.sha256 {
		t.Fatalf("the updated frontend does not launch: %+v %v", binding, err)
	}
}

// A managed installation given up in recovery stays recorded as managed, cannot be enabled (its runtime
// is retired and its data is on a host that is gone), and can be uninstalled by a removal that touches
// only the Portal: no runner is called and the old host's data is not on this one.
func TestRecoveryDisabledManagedInstallationIsUninstalledPortalSideOnly(t *testing.T) {
	s, actor, updateLease, _ := platformManagedRestoreReviewFixture(t, false)
	installation := updateLease.installationID
	oldRuntime := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings`)
	platformSimulateRestore(t, s)
	h := platformRecoveryActionHTTP(t, s, actor)
	committed := h.reviewAndCommit("recovery_disable", installation, platformFixtureID(5930))
	if scalar[int](t, s.st, `SELECT managed FROM app_platform_operations WHERE id=?`, committed["operation_id"]) != 1 {
		t.Fatal("a recovery disable of a managed installation was recorded as unmanaged")
	}
	h.endRecovery(200)
	if got := h.post(recoveryPlansPath, `{"action":"enable","installation_id":"`+installation+`"}`); got.Code == 200 {
		t.Fatalf("a recovery-disabled managed installation on a retired runtime was reviewed for enable: %s", got.Body.String())
	}
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_data_namespaces WHERE origin_installation_id=?`, installation)
	removed := h.reviewAndCommit("uninstall", installation, platformFixtureID(5931))
	if removed["stage"] != "complete" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "removed:removed" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, removed["operation_id"]) != "complete" ||
		scalar[int](t, s.st, `SELECT managed FROM app_platform_operations WHERE id=?`, removed["operation_id"]) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, removed["operation_id"]) != 0 {
		t.Fatalf("portal-side uninstall: %v", removed)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE id=?`, namespace) != "deleted" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE installation_id=? AND state<>'deleted'`, installation) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, installation) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.portal_side_uninstall' AND target_id=?`, installation) != 1 {
		t.Fatal("the orphaned data, recovery sets and grants were not settled, or the removal left no audit row")
	}
	if scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, oldRuntime) != 0 {
		t.Fatal("the retired runtime binding changed")
	}
}

// A portal-side uninstall retires the installation's secret bindings in the same transaction as it does
// for an ordinary removal: a binding left usable would outlive an installation whose host is gone.
func TestRecoveryPortalSideUninstallRevokesSecretBindings(t *testing.T) {
	s, actor, updateLease, _ := platformManagedRestoreReviewFixture(t, false)
	installation := updateLease.installationID
	platformSimulateRestore(t, s)
	plan := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE installation_id=? AND action='install'`, installation)
	platformTestInsert(t, s.st.poolExec(), "credentials", map[string]any{"id": platformFixtureID(2001), "label": "Fixture custody", "state": "active", "sealed_value": "fixture-envelope", "value_revision": 1, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})
	platformTestInsert(t, s.st.poolExec(), "secret_bindings", map[string]any{"id": platformFixtureID(2010), "installation_id": installation, "name": "first", "credential_id": platformFixtureID(2001), "credential_value_revision": 1, "operation": "http_bearer", "state": "active", "policy_json": "{}", "policy_revision": 1, "review_plan_id": plan, "revision": 1, "created_ms": 1, "updated_ms": 1, "revoked_ms": nil})
	h := platformRecoveryActionHTTP(t, s, actor)
	h.reviewAndCommit("recovery_disable", installation, platformFixtureID(5930))
	h.endRecovery(200)
	removed := h.reviewAndCommit("uninstall", installation, platformFixtureID(5931))
	if removed["stage"] != "complete" || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.portal_side_uninstall' AND target_id=?`, installation) != 1 {
		t.Fatalf("fixture: the removal was not portal-side: %v", removed)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_secret_bindings WHERE id=?`, platformFixtureID(2010)) != "revoked" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_secret_bindings WHERE installation_id=? AND state<>'revoked'`, installation) != 0 {
		t.Fatal("a portal-side uninstall left a secret binding usable")
	}
}
