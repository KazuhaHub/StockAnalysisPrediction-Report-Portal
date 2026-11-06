package app

import (
	"net/http"
	"strings"
	"testing"
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
