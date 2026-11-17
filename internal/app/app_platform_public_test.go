package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// platformPublicFixtureFiles replaces the fixture archive files (browser test).
var platformPublicFixtureFiles map[string]string

type platformPublicEnv struct {
	s        *Server
	mux      *http.ServeMux
	actor    platformActorSession
	cookie   string
	id       string
	calls    atomic.Int64
	queries  []string
	mu       sync.Mutex
	response func(route appplatform.PublicRoute, query string) (driver.BackendResponse, error)
}

func testPublicBlock() *appplatform.Public {
	return &appplatform.Public{Routes: []appplatform.PublicRoute{
		{ID: "history", Path: "/v1/public/history", Query: []string{"from", "limit", "to"}, CacheSeconds: 10, ResponseBytes: 4096},
		{ID: "status", Path: "/v1/public/status", CacheSeconds: 10, ResponseBytes: 1000},
	}, Document: "public/index.html"}
}

// platformPublicFixture is a ready backend installation that declares the public projection and
// whose backend answers through a recording hook (the runner path is covered separately).
func platformPublicFixture(t *testing.T, pg bool) *platformPublicEnv {
	t.Helper()
	platformManagedFixtureOptional = []string{"public.projection"}
	platformManagedFixturePublic = testPublicBlock()
	platformFixtureArchiveFiles = map[string]string{"public/index.html": "<!doctype html><title>Public</title>", "public/app.js": "console.log(1)", "private.txt": "secret"}
	if platformPublicFixtureFiles != nil {
		platformFixtureArchiveFiles = platformPublicFixtureFiles
	}
	t.Cleanup(func() {
		platformManagedFixtureOptional, platformManagedFixturePublic, platformFixtureArchiveFiles = nil, nil, nil
	})
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	t.Cleanup(func() {
		mustExec(t, s.st, `DELETE FROM audit_log WHERE action LIKE 'app_platform.public_projection_%'`)
	})
	env := &platformPublicEnv{s: s, actor: actor, id: lease.installationID, mux: http.NewServeMux()}
	env.cookie = s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	s.wireRoutes(env.mux)
	env.response = func(route appplatform.PublicRoute, query string) (driver.BackendResponse, error) {
		return driver.BackendResponse{Status: 200, Body: []byte(`{"route":"` + route.ID + `"}`)}, nil
	}
	s.platformPublicFetchHook = func(ctx context.Context, a platformPublicAuthority, route appplatform.PublicRoute, query string) (driver.BackendResponse, error) {
		env.calls.Add(1)
		env.mu.Lock()
		env.queries = append(env.queries, route.ID+"?"+query)
		env.mu.Unlock()
		return env.response(route, query)
	}
	return env
}

func (e *platformPublicEnv) admin(t *testing.T, method, suffix, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/admin/app-platform/installations/"+e.id+"/"+suffix, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: e.cookie})
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *platformPublicEnv) preview(t *testing.T) platformPublicPreview {
	t.Helper()
	w := e.admin(t, "GET", "public-projection", "")
	var out platformPublicPreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("preview", w.Code, w.Body.String())
	}
	return out
}

func (e *platformPublicEnv) enableWith(t *testing.T, routes []string, document bool, client, backend int) *httptest.ResponseRecorder {
	t.Helper()
	p := e.preview(t)
	body, _ := json.Marshal(map[string]any{"enabled": true, "policyRevision": p.PolicyRevision, "routes": routes, "document": document, "clientPerMinute": client, "backendPerSecond": backend, "disclosureSha256": p.DisclosureSHA256})
	return e.admin(t, "POST", "public-projection", string(body))
}

func (e *platformPublicEnv) enable(t *testing.T) {
	t.Helper()
	if w := e.enableWith(t, []string{"history", "status"}, true, 600, 5); w.Code != 200 {
		t.Fatal("enable", w.Code, w.Body.String())
	}
}

func (e *platformPublicEnv) disable(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	p := e.preview(t)
	return e.admin(t, "POST", "public-projection", `{"enabled":false,"policyRevision":"`+p.PolicyRevision+`","routes":null,"document":null,"clientPerMinute":null,"backendPerSecond":null,"disclosureSha256":null}`)
}

// get sends an anonymous request from one source address, optionally with credentials that must
// make no difference.
func (e *platformPublicEnv) get(path, remote string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if remote != "" {
		r.RemoteAddr = remote
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *platformPublicEnv) data(route, query string) *httptest.ResponseRecorder {
	target := "/public/apps/" + e.id + "/data/" + route
	if query != "" {
		target += "?" + query
	}
	return e.get(target, "", nil)
}

const publicNotFound = `{"code":"not_found"}`

func TestPublicProjectionIsOffUntilGranted(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		for _, path := range []string{"/public/apps/" + e.id + "/data/status", "/public/apps/" + e.id + "/", "/public/apps/" + e.id + "/app.js", "/public/apps/00000000-0000-4000-8000-000000000000/data/status", "/public/apps/not-a-uuid/data/status"} {
			w := e.get(path, "", nil)
			if w.Code != 404 || w.Body.String() != publicNotFound || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatal("not uniform before the grant:", path, w.Code, w.Body.String())
			}
		}
		if e.calls.Load() != 0 {
			t.Fatal("the backend was reached")
		}
		e.enable(t)
		w := e.data("status", "")
		if w.Code != 200 || w.Body.String() != `{"route":"status"}` {
			t.Fatal("granted route", w.Code, w.Body.String())
		}
		// A route the review left out, or one the descriptor never declared, is the same 404.
		if w := e.data("nope", ""); w.Code != 404 || w.Body.String() != publicNotFound {
			t.Fatal("undeclared route", w.Code)
		}
	})
}

func TestPublicDataResponseHeaders(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		w := e.data("status", "")
		h := w.Header()
		want := map[string]string{
			"Content-Type":                "application/json",
			"X-Content-Type-Options":      "nosniff",
			"Content-Security-Policy":     "default-src 'none'; frame-ancestors 'none'",
			"Access-Control-Allow-Origin": "*",
		}
		for k, v := range want {
			if h.Get(k) != v {
				t.Fatal(k, h.Get(k))
			}
		}
		if h.Get("Cache-Control") != "public, max-age=10" || h.Get("Set-Cookie") != "" {
			t.Fatal("cache policy or cookie", h.Get("Cache-Control"), h.Get("Set-Cookie"))
		}
		// A cached answer never advertises more life than it has left.
		time.Sleep(1100 * time.Millisecond)
		again := e.data("status", "")
		if again.Header().Get("Cache-Control") != "public, max-age=9" && again.Header().Get("Cache-Control") != "public, max-age=10" {
			t.Fatal("cached answer lifetime", again.Header().Get("Cache-Control"))
		}
		if e.calls.Load() != 1 {
			t.Fatal("not cached")
		}
	})
}

func TestPublicRoutesAreGetOnly(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		for _, target := range []string{"/public/apps/" + e.id + "/data/status", "/public/apps/" + e.id + "/", "/public/apps/" + e.id + "/app.js"} {
			for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
				r := httptest.NewRequest(method, target, strings.NewReader(`{}`))
				w := httptest.NewRecorder()
				e.mux.ServeHTTP(w, r)
				if w.Code != 405 && w.Code != 404 || e.calls.Load() != 0 {
					t.Fatal(method, target, w.Code)
				}
				if strings.Contains(w.Body.String(), "<html") {
					t.Fatal("the SPA answered a write", method, target)
				}
			}
		}
	})
}

func TestPublicQueryIsCheckedWithoutABackendCall(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		for _, q := range []string{"x=1", "from=a&from=b", "from=" + strings.Repeat("a", 129), "from", "limit=%00", "status=1"} {
			if w := e.data("history", q); w.Code != 400 || w.Body.String() != `{"code":"bad_query"}` {
				t.Fatal(q, w.Code, w.Body.String())
			}
		}
		if w := e.data("status", "limit=1"); w.Code != 400 {
			t.Fatal("a route with no declared names took a query", w.Code)
		}
		if e.calls.Load() != 0 {
			t.Fatal("a refused query reached the backend")
		}
		// The same query in another order is one cache entry and one backend call, in canonical order.
		if w := e.data("history", "to=2&from=1"); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := e.data("history", "from=1&to=2"); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if e.calls.Load() != 1 || e.queries[0] != "history?from=1&to=2" {
			t.Fatal("canonical query", e.calls.Load(), e.queries)
		}
	})
}

func TestPublicRequestWithASessionIsTheAnonymousRequest(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		anonymous := e.data("status", "")
		e.s.platformPublic.purge(e.id)
		w := e.get("/public/apps/"+e.id+"/data/status", "", map[string]string{"Cookie": cookieName + "=" + e.cookie, "Authorization": "Bearer x"})
		if w.Code != anonymous.Code || w.Body.String() != anonymous.Body.String() || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Cache-Control") != anonymous.Header().Get("Cache-Control") {
			t.Fatal("a credential changed the answer", w.Code, w.Body.String())
		}
		if e.calls.Load() != 2 {
			t.Fatal("expected two fetches around the purge")
		}
	})
}

func TestPublicBackendAccessCarriesNoViewerAndNeedsTheGrant(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		anon := platformActorSession{anonymousPublic: true}
		if a, err := e.s.preparePlatformWritableBackendAccess(context.Background(), anon, e.id); err == nil {
			a.Close()
			t.Fatal("an anonymous access opened without the grant")
		}
		e.enable(t)
		a, err := e.s.preparePlatformWritableBackendAccess(context.Background(), anon, e.id)
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if a.viewer.Subject != "" || a.viewer.Role != "" || len(a.viewer.OUs) != 0 || a.account != (platformActorAccount{}) || a.publicPolicy == "" {
			t.Fatalf("anonymous access carries identity: %+v", a)
		}
		auth, err := e.s.readPlatformPublicAuthority(context.Background(), e.id)
		if err != nil || a.publicPolicy != auth.policyJSON {
			t.Fatal("access and authority disagree", err)
		}
		// The assertion of a request binds the route and carries no viewer at all.
		route, _ := auth.route("history")
		signer, err := a.intent.registration.Signer("boot-1")
		if err != nil {
			t.Fatal(err)
		}
		token, err := platformPublicAssertion(auth, route, "from=1")(signer, a, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		claims, err := appplatform.VerifyPublicAssertion(signer, token, "/v1/public/history?from=1", time.Now())
		if err != nil || claims.Route != "history" || claims.Generation != a.intent.generation {
			t.Fatal(claims, err)
		}
		if strings.Contains(token, "viewer") {
			t.Fatal("viewer in a public token")
		}
		stale := auth
		stale.policyJSON = "{}"
		if _, err := platformPublicAssertion(stale, route, "from=1")(signer, a, time.Now()); err == nil {
			t.Fatal("an assertion was signed for a policy the access does not show")
		}
		// Withdrawal closes the access at once.
		if w := e.disable(t); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if b, err := e.s.preparePlatformWritableBackendAccess(context.Background(), anon, e.id); err == nil {
			b.Close()
			t.Fatal("anonymous access survived the withdrawal")
		}
	})
}

func TestPublicBackendRateAndSingleFlight(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		if w := e.enableWith(t, []string{"history", "status"}, true, 600, 5); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		// 1000 distinct allowed queries from 1000 sources: the backend sees the installation's
		// ceiling and everything else is a 429 with Retry-After.
		var ok, limited int
		started := time.Now()
		for i := 0; i < 1000; i++ {
			w := e.get(fmt.Sprintf("/public/apps/%s/data/history?from=%d", e.id, i), fmt.Sprintf("10.%d.%d.1:5000", i/250, i%250), nil)
			switch w.Code {
			case 200:
				ok++
			case 429:
				limited++
				if w.Header().Get("Retry-After") == "" {
					t.Fatal("429 without Retry-After")
				}
			default:
				t.Fatal(w.Code, w.Body.String())
			}
		}
		// A burst of 5 plus what the bucket refills while the loop runs: never more.
		allowed := 5 + int(5*time.Since(started).Seconds()) + 2
		if calls := e.calls.Load(); calls > int64(allowed) || calls < 5 || ok != int(calls) || limited != 1000-ok {
			t.Fatal("backend ceiling", e.calls.Load(), ok, limited, allowed)
		}
	})
}

func TestPublicIdenticalRequestsShareOneFetch(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		release := make(chan struct{})
		e.response = func(route appplatform.PublicRoute, query string) (driver.BackendResponse, error) {
			<-release
			return driver.BackendResponse{Status: 200, Body: []byte(`{"slow":true}`)}, nil
		}
		var wg sync.WaitGroup
		codes := make([]int, 12)
		for i := range codes {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[i] = e.get("/public/apps/"+e.id+"/data/status", fmt.Sprintf("10.9.0.%d:1", i+1), nil).Code
			}()
		}
		time.Sleep(300 * time.Millisecond)
		close(release)
		wg.Wait()
		for _, c := range codes {
			if c != 200 {
				t.Fatal(codes)
			}
		}
		if e.calls.Load() != 1 {
			t.Fatal("identical requests fetched", e.calls.Load())
		}
	})
}

func TestPublicBackendAnswersAreBounded(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		if w := e.enableWith(t, []string{"history", "status"}, true, 600, 20); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		cases := map[string]struct {
			resp driver.BackendResponse
			err  error
			want int
		}{
			"too large":    {driver.BackendResponse{Status: 200, Body: []byte(`{"x":"` + strings.Repeat("a", 1100) + `"}`)}, nil, 503},
			"server error": {driver.BackendResponse{Status: 500, Body: []byte(`{}`)}, nil, 503},
			"redirect":     {driver.BackendResponse{Status: 302, Body: []byte(`{}`)}, nil, 503},
			"created":      {driver.BackendResponse{Status: 201, Body: []byte(`{}`)}, nil, 503},
			"not json":     {driver.BackendResponse{Status: 200, Body: []byte(`<html>`)}, nil, 503},
			"empty":        {driver.BackendResponse{Status: 200}, nil, 503},
			"refused":      {driver.BackendResponse{Status: 404, Body: []byte(`{}`)}, nil, 404},
			"runner down":  {driver.BackendResponse{}, fmt.Errorf("down"), 503},
		}
		for name, c := range cases {
			e.response = func(appplatform.PublicRoute, string) (driver.BackendResponse, error) { return c.resp, c.err }
			e.s.platformPublic.purge(e.id)
			w := e.data("status", "")
			if w.Code != c.want || strings.Contains(w.Body.String(), "html") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal(name, w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
			}
			if n, _ := e.s.platformPublic.cacheSize(e.id); n != 0 {
				t.Fatal(name, "a refused answer was cached")
			}
			// A refusal frees the single flight and the concurrency slot: the next call fetches.
		}
		// The ceiling resets between cases only through time, so wait for tokens before the final check.
		time.Sleep(1100 * time.Millisecond)
		e.response = func(appplatform.PublicRoute, string) (driver.BackendResponse, error) {
			return driver.BackendResponse{Status: 200, Body: []byte(`{"ok":1}`)}, nil
		}
		e.s.platformPublic.purge(e.id)
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal("recovery", w.Code, w.Body.String())
		}
	})
}

func TestPublicWithdrawalIsImmediateAndEmptiesTheCache(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := e.get("/public/apps/"+e.id+"/app.js", "", nil); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if n, _ := e.s.platformPublic.cacheSize(e.id); n != 2 {
			t.Fatal("cache entries", n)
		}
		if w := e.disable(t); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if n, b := e.s.platformPublic.cacheSize(e.id); n != 0 || b != 0 {
			t.Fatal("cache survived the withdrawal", n, b)
		}
		before := e.calls.Load()
		for _, p := range []string{"/data/status", "/", "/app.js"} {
			if w := e.get("/public/apps/"+e.id+p, "", nil); w.Code != 404 || w.Body.String() != publicNotFound {
				t.Fatal(p, w.Code)
			}
		}
		if e.calls.Load() != before {
			t.Fatal("backend reached after withdrawal")
		}
		if scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != "{}" ||
			scalar[int](t, e.s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != 0 {
			t.Fatal("the withdrawn row did not return to {}")
		}
		// And a stale cache entry cannot outlive a change of the policy underneath it.
		e.enable(t)
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := e.enableWith(t, []string{"status"}, false, 600, 5); w.Code != 200 {
			t.Fatal("policy change", w.Code, w.Body.String())
		}
		if n, _ := e.s.platformPublic.cacheSize(e.id); n != 0 {
			t.Fatal("cache survived a policy change")
		}
		if w := e.data("history", ""); w.Code != 404 {
			t.Fatal("a route removed from the policy is still served", w.Code)
		}
		if w := e.get("/public/apps/"+e.id+"/", "", nil); w.Code != 404 {
			t.Fatal("the document stayed on after the policy turned it off", w.Code)
		}
	})
}

func TestPublicRefusesWhileUnsafeAndAnswersUniformlyWhenOff(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
		mustExec(t, e.s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		w := e.data("status", "")
		mustExec(t, e.s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
		if w.Code != 503 || w.Body.String() != `{"code":"unavailable"}` {
			t.Fatal("restore pause served from the cache", w.Code, w.Body.String())
		}
		if w := e.data("status", ""); w.Code != 200 {
			t.Fatal("did not recover", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
		if w := e.data("status", ""); w.Code != 404 || w.Body.String() != publicNotFound {
			t.Fatal("disabled installation", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, e.id)
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='disabled' WHERE id=?`, e.id)
		if w := e.data("status", ""); w.Code != 404 {
			t.Fatal("not ready", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, e.id)
		mustExec(t, e.s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='public.projection'`, e.id)
		if w := e.data("status", ""); w.Code != 404 {
			t.Fatal("grant row off", w.Code)
		}
	})
}
