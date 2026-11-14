package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

const admissionInstallation = "11111111-2222-4333-8444-555555555555"

type admissionRoute struct {
	method, path string
	json         bool // the route answers its busy refusal as JSON
	budget       func(*Server) *platformDocumentBudget
}

func admissionRoutes() []admissionRoute {
	base := "/api/app-platform/installations/" + admissionInstallation
	docs := func(s *Server) *platformDocumentBudget { return &s.platformDocuments }
	return []admissionRoute{
		{"GET", "/api/app-platform/apps", false, docs},
		{"GET", base + "/status", false, docs},
		{"GET", base + "/launch", false, docs},
		{"POST", base + "/reports", false, docs},
		{"POST", base + "/objects", false, docs},
		{"GET", "/app-documents/" + admissionInstallation + "/1/1/" + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 64) + "/index.html", false, docs},
		{"POST", base + "/backend", true, func(s *Server) *platformDocumentBudget { return &s.platformBackendRequests }},
		{"POST", base + "/network", true, func(s *Server) *platformDocumentBudget { return &s.platformNetworkRequests }},
		{"POST", base + "/network/stream", true, func(s *Server) *platformDocumentBudget { return &s.platformNetworkStreams }},
	}
}

func admissionServer(t *testing.T) (*Server, *spyDriver, *http.ServeMux) {
	t.Helper()
	st, d := newSpyTestStore(t)
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
	st.SetSetting("public_url", "https://portal.example")
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, d, mux
}

func admissionCall(mux *http.ServeMux, route admissionRoute, cookie string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func fillBudget(t *testing.T, b *platformDocumentBudget) {
	t.Helper()
	for i := 0; i < 2; i++ {
		release, ok := b.acquire()
		if !ok {
			t.Fatal("budget was not free")
		}
		t.Cleanup(release)
	}
}

func busyBytes(route admissionRoute) (string, string) {
	w := httptest.NewRecorder()
	if route.json {
		platformJSONBusy(w)
	} else {
		platformHTTPBusy(w)
	}
	return w.Body.String(), w.Header().Get("Retry-After")
}

// A full budget answers a signed cookie's request with the budget's own 429 and touches the database
// not at all: no statement, no connection.
func TestPlatformBudgetFullAnswersBeforeAnyDatabaseStatement(t *testing.T) {
	for _, route := range admissionRoutes() {
		t.Run(route.method+" "+route.path[:min(len(route.path), 48)], func(t *testing.T) {
			s, d, mux := admissionServer(t)
			cookie := s.managedCookie(*s.st.GetUser("alice"), "sid", time.Now().Add(time.Hour).Unix())
			fillBudget(t, route.budget(s))
			d.mu.Lock()
			d.tracing, d.trace = true, nil
			d.mu.Unlock()
			w := admissionCall(mux, route, cookie)
			d.mu.Lock()
			trace := append([]string(nil), d.trace...)
			d.tracing = false
			d.mu.Unlock()
			body, retry := busyBytes(route)
			if w.Code != 429 || w.Body.String() != body || w.Header().Get("Retry-After") != retry {
				t.Fatalf("got %d %q retry %q, want 429 %q retry %q", w.Code, w.Body.String(), w.Header().Get("Retry-After"), body, retry)
			}
			if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Security-Policy") != platformDocumentErrorCSP {
				t.Fatal("document response policy missing on the refusal", w.Header())
			}
			if len(trace) != 0 {
				t.Fatalf("a refused request issued %d database operations: %v", len(trace), trace)
			}
		})
	}
}

// Without a cookie this server signed, a full budget changes nothing: the request takes the same path
// and gets the same answer as with a free budget.
func TestPlatformBudgetFullDoesNotChangeUnsignedClients(t *testing.T) {
	s0, _, _ := admissionServer(t)
	good := s0.managedCookie(*s0.st.GetUser("alice"), "sid", time.Now().Add(time.Hour).Unix())
	cases := map[string]string{
		"none":      "",
		"malformed": "not-a-cookie",
		"bad mac":   strings.SplitN(good, ".", 2)[0] + ".00",
		"no mac":    strings.SplitN(good, ".", 2)[0],
		"expired":   s0.managedCookie(*s0.st.GetUser("alice"), "sid", time.Now().Add(-time.Hour).Unix()),
		"owner attribution token": func() string {
			msg := fmt.Sprintf("%s|%d|%s|%d", ownerTokenPrefix, 1, "alice", time.Now().Add(time.Hour).Unix())
			return encodeSessionMessage(msg) + "." + s0.hmac(msg)
		}(),
		"other key": func() string {
			o := &Server{cfg: &config.Config{SecretKey: "ffffffffffffffffffffffffffffffff"}}
			return o.managedCookie(User{Username: "alice"}, "sid", time.Now().Add(time.Hour).Unix())
		}(),
	}
	for _, route := range admissionRoutes() {
		for name, cookie := range cases {
			s, _, mux := admissionServer(t)
			free := admissionCall(mux, route, cookie)
			fillBudget(t, route.budget(s))
			full := admissionCall(mux, route, cookie)
			if free.Code != 401 && free.Code != 404 {
				t.Fatalf("%s %s: unsigned client reached %d", route.path, name, free.Code)
			}
			if full.Code != free.Code || full.Body.String() != free.Body.String() {
				t.Fatalf("%s %s: full budget changed the answer: %d %q vs %d %q", route.path, name, full.Code, full.Body.String(), free.Code, free.Body.String())
			}
		}
	}
}

// The pre-check never holds a slot, and a request that passed it can still lose the authoritative
// acquire, which answers exactly the same 429.
func TestPlatformBudgetPrecheckNeverHoldsASlotAndAcquireStaysAuthoritative(t *testing.T) {
	s, _, _ := admissionServer(t)
	sess := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	budget := &s.platformDocuments
	var admitted atomic.Int32
	racing := false
	h := s.platformBudgetPrecheck(budget, platformHTTPBusy, func(w http.ResponseWriter, r *http.Request) {
		admitted.Add(1)
		if racing { // the budget fills after the pre-check let this request through
			fillBudget(t, budget)
		}
		_, release, ok := s.platformHTTPAdmission(w, r, "alice")
		if ok {
			if len(budget.slots) != 1 {
				t.Fatal("an admitted request must hold exactly one slot", len(budget.slots))
			}
			release()
		}
	})
	do := func(cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	if w := do(sess); w.Code != 200 || admitted.Load() != 1 || len(budget.slots) != 0 {
		t.Fatal("free budget: full path unchanged", w.Code, admitted.Load(), len(budget.slots))
	}
	// Anonymous and forged clients reach the next handler, which refuses them, and hold nothing.
	for _, c := range []string{"", "junk", "junk.junk"} {
		do(c)
		if len(budget.slots) != 0 {
			t.Fatal("an unauthenticated client held a slot", c)
		}
	}
	racing = true
	w := do(sess)
	want := httptest.NewRecorder()
	platformHTTPBusy(want)
	if admitted.Load() != 5 || w.Code != 429 || w.Body.String() != want.Body.String() || w.Header().Get("Retry-After") != "1" {
		t.Fatal("a request that lost the race must get the same 429", w.Code, w.Body.String(), admitted.Load())
	}
}

// Every route leaves its budget empty after refusing an unauthenticated client.
func TestPlatformUnauthenticatedClientsNeverHoldBudgetSlots(t *testing.T) {
	s, _, mux := admissionServer(t)
	for _, route := range admissionRoutes() {
		for _, cookie := range []string{"", "junk", "junk.junk"} {
			admissionCall(mux, route, cookie)
			if n := len(route.budget(s).slots); n != 0 {
				t.Fatalf("%s: %d slot(s) held after an unauthenticated request", route.path, n)
			}
		}
	}
}

// The session reads of a relay attempt run through the statement cache: each text is prepared once
// and every later attempt reuses it, none takes the driver's direct (unprepared) path.
func TestPlatformSessionReadsUseTheStatementCache(t *testing.T) {
	s, d, _ := admissionServer(t)
	cookie := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	// The route without the pre-check, against a full budget: exactly the work every refused attempt
	// paid before the pre-check existed (the two session reads, then the 429).
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/app-platform/installations/{installation}/backend", platformDocumentHeaders(s.requireUserJSON(s.apiPlatformBackend)))
	route := admissionRoutes()[6] // the backend relay
	fillBudget(t, &s.platformBackendRequests)
	if w := admissionCall(mux, route, cookie); w.Code != 429 {
		t.Fatal("expected the budget refusal after the session reads", w.Code)
	}
	d.mu.Lock()
	d.tracing, d.trace = true, nil
	directBefore := map[string]int{}
	for q, n := range d.direct {
		directBefore[q] = n
	}
	d.mu.Unlock()
	for i := 0; i < 5; i++ {
		admissionCall(mux, route, cookie)
	}
	d.mu.Lock()
	trace := append([]string(nil), d.trace...)
	d.tracing = false
	d.mu.Unlock()
	seen := map[string]bool{}
	for _, q := range trace {
		for _, part := range []string{"user_sessions", "FROM users u WHERE u.username", "notification_source_cycles"} {
			if !strings.Contains(q, part) {
				continue
			}
			seen[part] = true
			if d.directOf(q) != directBefore[q] {
				t.Fatalf("session read bypassed the cache: %s", q)
			}
			if d.preparesOf(q) != 1 {
				t.Fatalf("session read prepared %d times: %s", d.preparesOf(q), q)
			}
		}
	}
	if len(seen) != 3 {
		t.Fatalf("expected the user, session and generation reads in the trace, saw %v of %d statements", seen, len(trace))
	}
	t.Logf("a refused attempt issued %d database operations per request before the pre-check", len(trace)/5)
}

// BenchmarkPlatformRelayFloodSmallReadP95 floods the backend relay route against a full budget on a
// file-backed SQLite store with its single connection, while a stream of small reads is timed. The
// "authentication_first" variant is the order before the pre-check; "admission_first" is the route.
func BenchmarkPlatformRelayFloodSmallReadP95(b *testing.B) {
	for _, variant := range []string{"authentication_first", "admission_first"} {
		b.Run(variant, func(b *testing.B) {
			st, err := OpenStore("sqlite", filepath.Join(b.TempDir(), "portal.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer st.Close()
			s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}}
			st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
			r0 := httptest.NewRequest("GET", "/", nil)
			r0.Header.Set("User-Agent", "Chrome/130.0")
			w0 := httptest.NewRecorder()
			if !s.setSessionCookieFor(w0, r0, *st.GetUser("alice"), time.Hour) {
				b.Fatal("session")
			}
			cookie := w0.Result().Cookies()[0].Value
			var handler http.HandlerFunc = s.requireUserJSON(s.apiPlatformBackend)
			if variant == "admission_first" {
				handler = s.platformBudgetPrecheck(&s.platformBackendRequests, platformJSONBusy, handler)
			}
			for i := 0; i < 2; i++ {
				if _, ok := s.platformBackendRequests.acquire(); !ok {
					b.Fatal("budget")
				}
			}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			var floods atomic.Int64
			for i := 0; i < 16; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						r := httptest.NewRequest("POST", "/api/app-platform/installations/"+admissionInstallation+"/backend", strings.NewReader(`{}`))
						r.Header.Set("Content-Type", "application/json")
						r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
						handler(httptest.NewRecorder(), r)
						floods.Add(1)
						time.Sleep(4 * time.Millisecond) // paced: both variants see the same offered load
					}
				}()
			}
			time.Sleep(100 * time.Millisecond)
			samples := make([]time.Duration, 0, 2000)
			for i := 0; i < 2000; i++ {
				start := time.Now()
				var n int
				_ = st.queryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
				samples = append(samples, time.Since(start))
				time.Sleep(time.Millisecond)
			}
			close(stop)
			wg.Wait()
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			b.ReportMetric(float64(samples[len(samples)*95/100].Microseconds()), "p95_us")
			b.ReportMetric(float64(samples[len(samples)/2].Microseconds()), "p50_us")
			b.ReportMetric(float64(floods.Load()), "flood_attempts")
		})
	}
}

// A cookie this server signed passes the pre-check even when its session is no longer live, so while
// a budget is full such a client sees the budget's 429 where a free budget would give it a 401. The
// documented visible change of the pre-check.
func TestPlatformBudgetFullRefusesDeadSessionsWith429AndFreeBudgetWith401(t *testing.T) {
	route := admissionRoutes()[6] // the backend relay
	cases := map[string]func(*Server, string){
		"revoked session": func(s *Server, _ string) { mustExec(t, s.st, `UPDATE user_sessions SET revoked=1`) },
		"disabled account": func(s *Server, _ string) {
			mustExec(t, s.st, `UPDATE users SET active=0 WHERE username='alice'`)
		},
		"stale session revision": func(s *Server, _ string) {
			mustExec(t, s.st, `UPDATE users SET session_rev=session_rev+1 WHERE username='alice'`)
		},
	}
	for name, kill := range cases {
		s, _, mux := admissionServer(t)
		cookie := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
		kill(s, cookie)
		if w := admissionCall(mux, route, cookie); w.Code != 401 {
			t.Fatalf("%s: free budget answered %d, want 401", name, w.Code)
		}
		fillBudget(t, route.budget(s))
		if w := admissionCall(mux, route, cookie); w.Code != 429 {
			t.Fatalf("%s: full budget answered %d, want 429", name, w.Code)
		}
	}
}
