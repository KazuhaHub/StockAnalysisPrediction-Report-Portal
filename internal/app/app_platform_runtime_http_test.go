package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
)

const (
	platformRuntimeStatusPath  = "/api/admin/app-platform/runtime"
	platformRuntimeControlPath = "/api/admin/app-platform/control/runtime"
)

func platformRuntimeHTTPFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformRecoveryHTTP) {
	t.Helper()
	s, actor := platformActorTestServer(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, actor, platformRecoveryHTTP{t: t, mux: mux, cookie: s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}
}

func (h platformRecoveryHTTP) get(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	return sessionTestRequest(h.mux, "GET", path, h.cookie, "")
}

func platformRuntimeState(h platformRecoveryHTTP) map[string]any {
	h.t.Helper()
	return h.json(h.get(platformRuntimeStatusPath), 200)
}

// A fresh host is enrolled from the console alone: initialize, prepare (which hands out the document
// host-setup reads), confirm once the runner serves. Every step is visible on the status read.
func testPlatformRuntimeEnrollmentThroughHTTP(t *testing.T, pg bool) {
	s, actor, h := platformRuntimeHTTPFixture(t, pg)
	// The Postgres test database is shared, so audit rows are counted from where this test started.
	count := func(action string) int {
		return scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, action)
	}
	base := map[string]int{"app_platform.runtime_prepared": count("app_platform.runtime_prepared"), "app_platform.runtime_confirmed": count("app_platform.runtime_confirmed")}
	audits := func(action string) int { return count(action) - base[action] }

	// The surface keeps its siblings' guards.
	for _, method := range []string{"GET", "POST"} {
		path := platformRuntimeStatusPath
		if method == "POST" {
			path = platformRuntimeControlPath
		}
		if got := sessionTestRequest(h.mux, method, path, "", `{"action":"prepare"}`); got.Code != 401 && got.Code != 403 {
			t.Fatalf("an anonymous %s reached the runtime surface: %d", method, got.Code)
		}
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if got := h.post(platformRuntimeControlPath, `{"action":"prepare"}`); got.Code != 403 {
		t.Fatalf("a non-administrator prepared a runtime: %d", got.Code)
	}
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)

	// Before initialization nothing can be prepared, and the status says why.
	if st := platformRuntimeState(h); st["state"] != "uninitialized" || st["endpoint_configured"] != false || st["runtime"] != nil {
		t.Fatalf("fresh status: %v", st)
	}
	if got := h.post(platformRuntimeControlPath, `{"action":"prepare"}`); got.Code != 503 {
		t.Fatalf("prepare before initialization answered %d: %s", got.Code, got.Body)
	}
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	if st := platformRuntimeState(h); st["state"] != "none" {
		t.Fatalf("initialized status: %v", st)
	}

	for _, bad := range []string{`{"action":"adopt"}`, `{"action":"prepare","deployment_id":"` + platformFixtureID(1) + `"}`, `{"action":"prepare","extra":1}`,
		`{"action":"confirm"}`, `{"action":"confirm","deployment_id":"nope"}`} {
		if got := h.post(platformRuntimeControlPath, bad); got.Code != 400 {
			t.Fatalf("%s answered %d", bad, got.Code)
		}
	}

	// Prepare mints the deployment and returns the document host-setup accepts.
	prepared := h.json(h.post(platformRuntimeControlPath, `{"action":"prepare"}`), 200)
	deployment, epoch := h.str(prepared, "deployment_id"), s.st.GetSetting("app_platform_controller_epoch", "")
	if !platformValidUUID(deployment) || h.str(prepared, "controller_epoch") != epoch || prepared["enabled"] != false {
		t.Fatalf("prepared: %v", prepared)
	}
	doc, err := service.ParseEnrollment([]byte(h.str(prepared, "enrollment")))
	if err != nil {
		t.Fatalf("host-setup refuses the console's document: %v", err)
	}
	pub, _ := hex.DecodeString(h.str(prepared, "public_key"))
	key, _ := service.EnrollmentPublicKey(doc)
	if doc.Controller != (service.Controller{DeploymentID: deployment, ControllerEpoch: epoch, ControlRevision: 1, Fingerprint: h.str(prepared, "verification_key_fingerprint")}) || !bytes.Equal(key, pub) {
		t.Fatalf("document identity: %+v", doc)
	}
	if audits("app_platform.runtime_prepared") != 1 {
		t.Fatal("prepare was not audited once")
	}
	// Preparing again while pending hands out the same document and audits nothing new.
	again := h.json(h.post(platformRuntimeControlPath, `{"action":"prepare"}`), 200)
	if again["enrollment"] != prepared["enrollment"] || again["runtime_id"] != prepared["runtime_id"] || audits("app_platform.runtime_prepared") != 1 {
		t.Fatalf("prepare is not idempotent: %v", again)
	}
	st := platformRuntimeState(h)
	if runtime, _ := st["runtime"].(map[string]any); st["state"] != "pending" || runtime["deployment_id"] != deployment || runtime["runtime_id"] != prepared["runtime_id"] {
		t.Fatalf("pending status: %v", st)
	}
	if _, leaked := st["enrollment"]; leaked {
		t.Fatal("the status read carries the document")
	}

	// The replacement route stays recovery-only and cannot pick this binding up.
	if got := h.post("/api/admin/app-platform/control/replacement-runtime", `{"action":"prepare","deployment_id":"`+deployment+`"}`); got.Code != 409 {
		t.Fatalf("the recovery route prepared on a normal Portal: %d", got.Code)
	}

	// Confirm needs Portal's runner endpoint, and names its absence.
	confirm := `{"action":"confirm","deployment_id":"` + deployment + `"}`
	if got := h.json(h.post(platformRuntimeControlPath, confirm), 409); got["code"] != "app_platform_runtime_endpoint_missing" {
		t.Fatalf("confirm without endpoint: %v", got)
	}
	endpoint := platformUpdateStartEndpointFixture()
	s.platformBackendEndpoint.Store(&endpoint)
	if st := platformRuntimeState(h); st["endpoint_configured"] != true {
		t.Fatalf("endpoint not reported: %v", st)
	}

	// A runner that reports another identity is refused and changes nothing.
	registered := platformRuntimeRegistration{id: h.str(prepared, "runtime_id"), deploymentID: deployment, controllerEpoch: epoch, controlRevision: 1, revision: 1, fingerprint: h.str(prepared, "verification_key_fingerprint"), publicKey: pub}
	other := registered
	other.deploymentID = platformFixtureID(9)
	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return runtimeObservationFixture(other), nil
	}
	if got := h.json(h.post(platformRuntimeControlPath, confirm), 409); got["code"] != "app_platform_runtime_not_observed" {
		t.Fatalf("mismatched observation: %v", got)
	}
	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return platformRuntimeObservation{}, errors.New("dial: no such file")
	}
	if got := h.json(h.post(platformRuntimeControlPath, confirm), 409); got["code"] != "app_platform_runtime_not_observed" {
		t.Fatalf("unreachable runner: %v", got)
	}
	if platformRuntimeState(h)["state"] != "pending" {
		t.Fatal("a refused confirm changed the binding")
	}
	// A confirm for another deployment is refused.
	if got := h.json(h.post(platformRuntimeControlPath, `{"action":"confirm","deployment_id":"`+platformFixtureID(9)+`"}`), 409); got["code"] != "app_platform_runtime_binding_unavailable" {
		t.Fatalf("confirm of an unknown deployment: %v", got)
	}

	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return runtimeObservationFixture(registered), nil
	}
	confirmed := h.json(h.post(platformRuntimeControlPath, confirm), 200)
	observed, _ := confirmed["observed"].(map[string]any)
	if confirmed["enabled"] != true || observed["deployment_id"] != deployment || observed["controller_epoch"] != epoch || observed["verification_key_fingerprint"] != registered.fingerprint {
		t.Fatalf("confirmed: %v", confirmed)
	}
	if audits("app_platform.runtime_confirmed") != 1 || scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, registered.id) != 1 {
		t.Fatal("confirm was not recorded once")
	}
	if st := platformRuntimeState(h); st["state"] != "confirmed" {
		t.Fatalf("confirmed status: %v", st)
	}
	// Repeating confirm answers the confirmed binding without a new audit; prepare no longer applies.
	if again := h.json(h.post(platformRuntimeControlPath, confirm), 200); again["enabled"] != true || audits("app_platform.runtime_confirmed") != 1 {
		t.Fatalf("repeated confirm: %v", again)
	}
	if got := h.json(h.post(platformRuntimeControlPath, `{"action":"prepare"}`), 409); got["code"] != "app_platform_runtime_binding_unavailable" {
		t.Fatalf("prepare after confirm: %v", got)
	}
}

func TestPlatformRuntimeEnrollmentThroughHTTPSQLite(t *testing.T) {
	t.Parallel()
	testPlatformRuntimeEnrollmentThroughHTTP(t, false)
}
func TestPlatformRuntimeEnrollmentThroughHTTPPostgres(t *testing.T) {
	t.Parallel()
	testPlatformRuntimeEnrollmentThroughHTTP(t, true)
}

// A restored Portal in recovery shows that state and refuses an ordinary prepare: its runtime is
// enrolled through the replacement procedure.
func TestPlatformRuntimeStatusInRecovery(t *testing.T) {
	t.Parallel()
	s, actor, h := platformRuntimeHTTPFixture(t, false)
	if err := s.initializeAppPlatformControl(actor); err != nil {
		t.Fatal(err)
	}
	if _, err := s.preparePlatformRuntimeBinding(actor, platformFixtureID(400)); err != nil {
		t.Fatal(err)
	}
	platformSimulateRestore(t, s)
	if st := platformRuntimeState(h); st["state"] != "paused" {
		t.Fatalf("recovery status: %v", st)
	}
	if got := h.post(platformRuntimeControlPath, `{"action":"prepare"}`); got.Code < 400 {
		t.Fatalf("ordinary prepare admitted in recovery: %d", got.Code)
	}
	if got := h.post(platformRuntimeControlPath, `{"action":"confirm","deployment_id":"`+platformFixtureID(400)+`"}`); got.Code < 400 {
		t.Fatalf("ordinary confirm admitted in recovery: %d", got.Code)
	}
	// Recovery ended with only retired bindings: this surface does not offer a prepare it would refuse.
	mustExec(t, s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	if st := platformRuntimeState(h); st["state"] != "conflict" || st["runtime"] != nil {
		t.Fatalf("retired-only status: %v", st)
	}
	if got := h.json(h.post(platformRuntimeControlPath, `{"action":"prepare"}`), 409); got["code"] != "app_platform_runtime_binding_unavailable" {
		t.Fatalf("prepare beside retired bindings: %v", got)
	}
}
