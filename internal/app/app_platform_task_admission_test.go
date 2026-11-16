package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The session-free task route has its own two-slot budget. A submission that finds it full is refused
// 429 app_platform_busy before the service identity is verified, so a flooding backend cannot spend
// the database or the admission lock on refusals (capacity envelope plan, SEC-09).

func taskAdmissionRequest(installation, identity string) *http.Request {
	r := httptest.NewRequest(http.MethodPost,
		"/api/app-platform/installations/"+installation+"/tasks/background",
		strings.NewReader(`{"occurrenceId":"flood","handler":"app.backend.task","payload":{}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Portal-Service-Identity", identity)
	return r
}

func TestPlatformServiceTaskFullBudgetAnswersBusyBeforeAnyDatabaseStatement(t *testing.T) {
	s, d, mux := admissionServer(t)
	fillBudget(t, &s.platformServiceTasks)
	d.mu.Lock()
	d.tracing, d.trace = true, nil
	d.mu.Unlock()
	identity := base64.RawURLEncoding.EncodeToString([]byte("not-a-verified-identity"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, taskAdmissionRequest(admissionInstallation, identity))
	d.mu.Lock()
	trace := append([]string(nil), d.trace...)
	d.tracing = false
	d.mu.Unlock()
	want := httptest.NewRecorder()
	platformJSONBusy(want)
	if w.Code != 429 || w.Body.String() != want.Body.String() || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d %q retry %q, want 429 %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"), want.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("document response policy missing on the refusal", w.Header())
	}
	if len(trace) != 0 {
		t.Fatalf("a refused submission issued %d database operations: %v", len(trace), trace)
	}
}

// Nothing the handler would read from the store is reachable once the budget is full: a Server with
// no store at all answers the 429 instead of dereferencing it.
func TestPlatformServiceTaskFullBudgetNeverReachesIdentityVerification(t *testing.T) {
	s := &Server{}
	fillBudget(t, &s.platformServiceTasks)
	r := taskAdmissionRequest(admissionInstallation, base64.RawURLEncoding.EncodeToString([]byte("x")))
	r.SetPathValue("installation", admissionInstallation)
	w := httptest.NewRecorder()
	s.apiPlatformServiceTaskSchedule(w, r)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "app_platform_busy") {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

// A request that fails the cheap, database-free checks never takes a slot, so a malformed client
// cannot hold the budget.
func TestPlatformServiceTaskMalformedRequestsAreNotAdmittedAgainstTheBudget(t *testing.T) {
	s := &Server{}
	fillBudget(t, &s.platformServiceTasks)
	for name, mutate := range map[string]func(*http.Request){
		"content type": func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"identity":     func(r *http.Request) { r.Header.Set("X-Portal-Service-Identity", "not base64!") },
	} {
		r := taskAdmissionRequest(admissionInstallation, base64.RawURLEncoding.EncodeToString([]byte("x")))
		r.SetPathValue("installation", admissionInstallation)
		mutate(r)
		w := httptest.NewRecorder()
		s.apiPlatformServiceTaskSchedule(w, r)
		if w.Code == 429 {
			t.Fatalf("%s: a malformed request was answered by the budget: %s", name, w.Body.String())
		}
	}
}

// Below the budget nothing changes, and every exit path gives the slot back.
func TestPlatformServiceTaskUnderBudgetBehaviourIsUnchangedAndSlotsAreReleased(t *testing.T) {
	s, installation := platformServiceIdentityFixture(t)
	assertFree := func(step string) {
		t.Helper()
		var releases []func()
		for i := 0; i < 2; i++ {
			release, ok := s.platformServiceTasks.acquire()
			if !ok {
				t.Fatalf("%s: slot %d was not released", step, i)
			}
			releases = append(releases, release)
		}
		for _, release := range releases {
			release()
		}
	}
	body := `{"occurrenceId":"budget-task","handler":"app.backend.task","payload":{"value":1}}`
	if w := platformTaskHTTPFixture(t, s, installation, body); w.Code != 200 {
		t.Fatalf("admitted: %d %s", w.Code, w.Body.String())
	}
	assertFree("success")
	if w := platformTaskHTTPFixture(t, s, installation,
		`{"occurrenceId":"budget-task","handler":"app.backend.task","payload":{"value":2}}`); w.Code != 409 {
		t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
	}
	assertFree("conflict")
	if w := platformTaskHTTPFixture(t, s, installation, `{"occurrenceId":""}`); w.Code != 400 {
		t.Fatalf("invalid body: %d %s", w.Code, w.Body.String())
	}
	assertFree("invalid body")
	revoked, _, err := s.issuePlatformServiceIdentity(context.Background(), installation,
		platformFixtureID(6301), []string{"tasks.schedule"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='tasks.schedule'`, installation)
	if w := platformTaskHTTPWithIdentity(t, s, installation, body, revoked); w.Code != 404 {
		t.Fatalf("revoked: %d %s", w.Code, w.Body.String())
	}
	assertFree("identity refused")
}

// The route's budget is its own: a flood of task submissions cannot make the session-facing backend
// relay answer busy, and the reverse.
func TestPlatformServiceTaskBudgetIsDedicated(t *testing.T) {
	s := &Server{}
	if &s.platformServiceTasks == &s.platformBackendRequests || &s.platformServiceTasks == &s.platformDocuments {
		t.Fatal("the service task budget is shared")
	}
	fillBudget(t, &s.platformServiceTasks)
	if s.platformBackendRequests.full() || s.platformDocuments.full() || s.platformNetworkRequests.full() {
		t.Fatal("a full service task budget filled another budget")
	}
}
