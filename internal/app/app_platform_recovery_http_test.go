package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformRecoveryHTTP struct {
	t      *testing.T
	mux    *http.ServeMux
	cookie string
}

func (h platformRecoveryHTTP) post(path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	return sessionTestRequest(h.mux, "POST", path, h.cookie, body)
}

func (h platformRecoveryHTTP) json(rec *httptest.ResponseRecorder, want int) map[string]any {
	h.t.Helper()
	if rec.Code != want {
		h.t.Fatalf("answered %d, want %d: %s", rec.Code, want, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h platformRecoveryHTTP) str(m map[string]any, key string) string {
	h.t.Helper()
	v, _ := m[key].(string)
	if v == "" {
		h.t.Fatalf("response has no %q: %v", key, m)
	}
	return v
}

// A restored, paused Portal is brought back to ready and out of recovery through the HTTP surface
// alone. The runner is a fake at the two seams a real one would sit behind: the enrollment
// observation and the import action. Everything else is the Portal's own code.
func testRecoveryThroughHTTP(t *testing.T, pg bool) {
	s, actor, updateLease, recoverySetID := platformManagedRestoreReviewFixture(t, pg)
	installation := updateLease.installationID
	platformSimulateRestore(t, s)
	endpoint := platformUpdateStartEndpointFixture()
	s.platformBackendEndpoint.Store(&endpoint)
	executor := &platformImportTestExecutor{}
	s.platformImportCheckpoint = executor
	var registered platformRuntimeRegistration
	s.platformObserveRuntime = func(context.Context, platformRuntimeEndpoint) (platformRuntimeObservation, error) {
		return runtimeObservationFixture(registered), nil
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	h := platformRecoveryHTTP{t: t, mux: mux, cookie: s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}
	const (
		replacement = "/api/admin/app-platform/control/replacement-runtime"
		plans       = "/api/admin/app-platform/plans"
		operations  = "/api/admin/app-platform/operations"
	)
	epoch := s.st.GetSetting("app_platform_controller_epoch", "")
	audits := func(action string) int {
		return scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, action)
	}

	// Enrollment: the surface keeps its siblings' guards.
	deployment := platformFixtureID(5100)
	prepare := `{"action":"prepare","deployment_id":"` + deployment + `"}`
	if got := sessionTestRequest(mux, "POST", replacement, "", prepare); got.Code != 401 && got.Code != 403 {
		t.Fatalf("an anonymous request reached enrollment: %d", got.Code)
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if got := h.post(replacement, prepare); got.Code != 403 {
		t.Fatalf("a non-administrator enrolled a runtime: %d", got.Code)
	}
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	for _, bad := range []string{`{"action":"prepare"}`, `{"action":"prepare","deployment_id":"nope"}`, `{"action":"adopt","deployment_id":"` + deployment + `"}`,
		`{"action":"prepare","deployment_id":"` + deployment + `","extra":1}`} {
		if got := h.post(replacement, bad); got.Code != 400 {
			t.Fatalf("%s answered %d", bad, got.Code)
		}
	}
	prepared := h.json(h.post(replacement, prepare), 200)
	if h.str(prepared, "deployment_id") != deployment || h.str(prepared, "controller_epoch") != epoch || prepared["enabled"] != false {
		t.Fatalf("prepared: %v", prepared)
	}
	if again := h.json(h.post(replacement, prepare), 200); h.str(again, "runtime_id") != h.str(prepared, "runtime_id") || audits("app_platform.runtime_replacement_prepared") < 1 {
		t.Fatalf("enrollment is not idempotent: %v", again)
	}
	pub, err := hex.DecodeString(h.str(prepared, "public_key"))
	if err != nil || len(pub) != 32 {
		t.Fatalf("public key: %v", err)
	}
	registered = platformRuntimeRegistration{id: h.str(prepared, "runtime_id"), deploymentID: deployment, controllerEpoch: epoch, controlRevision: 1, revision: 1,
		fingerprint: h.str(prepared, "verification_key_fingerprint"), publicKey: pub}
	confirmed := h.json(h.post(replacement, `{"action":"confirm","deployment_id":"`+deployment+`"}`), 200)
	if confirmed["enabled"] != true || scalar[int](t, s.st, `SELECT enabled FROM app_platform_runtime_bindings WHERE id=?`, registered.id) != 1 {
		t.Fatalf("confirmed: %v", confirmed)
	}
	registered.revision = 2

	// restore_import: review, commit, replay.
	review := h.json(h.post(plans, `{"action":"restore_import","installation_id":"`+installation+`","recovery_set_id":"`+recoverySetID+`"}`), 200)
	planID, planSHA := h.str(review, "plan_id"), h.str(review, "plan_sha256")
	if r, _ := review["review"].(map[string]any); r["action"] != "restore_import" || r["recovery_set_id"] != recoverySetID || r["runtime_id"] != registered.id {
		t.Fatalf("import review: %v", review)
	}
	key := platformFixtureID(5800)
	commitBody := `{"plan_id":"` + planID + `","plan_sha256":"` + planSHA + `","idempotency_key":"` + key + `"}`
	imported := h.json(h.post(operations, commitBody), 202)
	importOp := h.str(imported, "operation_id")
	if replay := h.json(h.post(operations, commitBody), 202); replay["replayed"] != true || replay["operation_id"] != importOp {
		t.Fatalf("import replay: %v", replay)
	}
	other := `{"plan_id":"` + planID + `","plan_sha256":"` + planSHA + `","idempotency_key":"` + platformFixtureID(5801) + `"}`
	if got := h.post(operations, other); got.Code != 409 {
		t.Fatalf("a second key consumed the import review: %d", got.Code)
	}

	// The scheduler of a paused Portal carries the import across the fake runner.
	worker, err := newPlatformWritableInstallScheduler(s, platformRestoreEndpointFixture(), platformFixtureID(5805), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12 && scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, importOp) != "complete"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal("import tick:", err)
		}
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, importOp) != "complete" || executor.calls != 1 {
		t.Fatalf("the import did not complete (runner calls=%d)", executor.calls)
	}
	var progress map[string]any
	if rec := sessionTestRequest(mux, "GET", operations+"/"+importOp, h.cookie, ""); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &progress) != nil || progress["stage"] != "complete" {
		t.Fatalf("import progress: %d %s", rec.Code, rec.Body.String())
	}

	// Resume: review through the observed runtime, commit.
	state := &platformResumeFixtureState{platformImportFixtureState: &platformImportFixtureState{replacement: registered}, endpoint: endpoint}
	s.platformRecoveryTarget = func(context.Context, platformActorSession, platformRuntimeEndpoint) (*platformManagedInstallTarget, error) {
		return state.target(), nil
	}
	resumeReview := h.json(h.post(plans, `{"action":"resume","installation_id":"`+installation+`"}`), 200)
	if r, _ := resumeReview["review"].(map[string]any); r["action"] != "resume" || r["installation_id"] != installation || r["import_operation_id"] != importOp {
		t.Fatalf("resume review: %v", resumeReview)
	}
	resumeKey := platformFixtureID(5802)
	resumeBody := `{"plan_id":"` + h.str(resumeReview, "plan_id") + `","plan_sha256":"` + h.str(resumeReview, "plan_sha256") + `","idempotency_key":"` + resumeKey + `"}`
	resumed := h.json(h.post(operations, resumeBody), 202)
	resumeOp := h.str(resumed, "operation_id")
	if replay := h.json(h.post(operations, resumeBody), 202); replay["replayed"] != true || replay["operation_id"] != resumeOp {
		t.Fatalf("resume replay: %v", replay)
	}
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/end-recovery", h.cookie, `{"action":"end_recovery"}`); got.Code != 409 {
		t.Fatalf("recovery ended with a resume in flight: %d", got.Code)
	}
	for i := 0; i < 8 && scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, resumeOp) != "start"; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal("resume tick:", err)
		}
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, resumeOp) != "start" || worker.active.id != resumeOp {
		t.Fatal("the resume did not reach its start stage")
	}
	done := platformResumeStartToReady(t, s, worker.active, endpoint)
	if done.snapshot.Stage != appplatform.StageComplete || scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, installation) != "ready" {
		t.Fatal("the installation is not ready")
	}

	end := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/end-recovery", h.cookie, `{"action":"end_recovery"}`)
	if end.Code != 200 || scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "0" {
		t.Fatalf("recovery did not end: %d %s", end.Code, end.Body.String())
	}
	for _, action := range []string{"app_platform.runtime_replacement_prepared", "app_platform.runtime_confirmed", "app_platform.operation_admitted", "app_platform.recovery_ended"} {
		if audits(action) < 1 {
			t.Fatalf("no %s audit row", action)
		}
	}
	// Outside recovery the recovery surface has no standing.
	for _, body := range []string{prepare, `{"action":"confirm","deployment_id":"` + deployment + `"}`} {
		if got := h.post(replacement, body); got.Code != 409 || !strings.Contains(got.Body.String(), "app_platform_recovery_inactive") {
			t.Fatalf("enrollment outside recovery: %d %s", got.Code, got.Body.String())
		}
	}
	for _, body := range []string{`{"action":"restore_import","installation_id":"` + installation + `","recovery_set_id":"` + recoverySetID + `"}`, `{"action":"resume","installation_id":"` + installation + `"}`} {
		if got := h.post(plans, body); got.Code != 409 || !strings.Contains(got.Body.String(), "app_platform_recovery_inactive") {
			t.Fatalf("%s outside recovery: %d %s", body, got.Code, got.Body.String())
		}
	}
}

func TestRecoveryThroughHTTP(t *testing.T)         { testRecoveryThroughHTTP(t, false) }
func TestRecoveryThroughHTTPPostgres(t *testing.T) { testRecoveryThroughHTTP(t, true) }

// A replacement is never enrolled beside a binding that is still in force: the refusal is its own
// typed error, not a generic outage, and it changes nothing.
func TestReplacementRuntimeRouteRefusedBesideLiveBinding(t *testing.T) {
	s, session := runtimeRegistrationFixture(t, false)
	first, err := s.preparePlatformRuntimeBinding(session, platformFixtureID(400))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.confirmPlatformObservedRuntime(session, first, runtimeObservationFixture(first)); err != nil {
		t.Fatal(err)
	}
	platformSetPause(t, s.st, "1")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/replacement-runtime", cookie,
		`{"action":"prepare","deployment_id":"`+platformFixtureID(5100)+`"}`)
	if got.Code != 409 || !strings.Contains(got.Body.String(), "app_platform_replacement_unavailable") {
		t.Fatalf("replacement beside a live binding: %d %s", got.Code, got.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_runtime_bindings`) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.runtime_replacement_prepared'`) != 0 {
		t.Fatal("a refused enrollment changed state")
	}
}
