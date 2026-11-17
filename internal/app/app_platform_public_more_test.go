package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPublicDocumentIsSandboxedAndLimitedToPublic(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		base := "/public/apps/" + e.id
		w := e.get(base+"/", "", nil)
		if w.Code != 200 || w.Body.String() != "<!doctype html><title>Public</title>" || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatal("document", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
		}
		const csp = "sandbox allow-scripts; default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
		for k, v := range map[string]string{"Content-Security-Policy": csp, "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Cache-Control": "public, max-age=10"} {
			if w.Header().Get(k) != v {
				t.Fatal(k, w.Header().Get(k))
			}
		}
		if w.Header().Get("Cross-Origin-Resource-Policy") != "" {
			t.Fatal("a resource policy would block the sandboxed page's own scripts")
		}
		if w.Header().Get("Set-Cookie") != "" {
			t.Fatal("cookie on a public document")
		}
		if w := e.get(base+"/app.js", "", nil); w.Code != 200 || w.Body.String() != "console.log(1)" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Header().Get("Content-Security-Policy") != csp {
			t.Fatal("asset", w.Code, w.Header().Get("Content-Type"))
		}
		for _, p := range []string{"/private.txt", "/..%2Fprivate.txt", "/../private.txt", "/nope.js", "/%2e%2e/x", "/a//b", "/app.js/", "/app.js?x=1", "/ap%00p.js", "/data"} {
			w := e.get(base+p, "", nil)
			if w.Code == 200 || strings.Contains(w.Body.String(), "secret") {
				t.Fatal("reached outside public/:", p, w.Code)
			}
		}
		// The root entry file of the app is not a public file: only names under public/ exist here.
		if w := e.get(base+"/index.html", "", nil); w.Code != 200 || strings.Contains(w.Body.String(), "Fixture") {
			t.Fatal("index.html must map to public/index.html", w.Code, w.Body.String())
		}
		// The page reads its data from the same prefix, cookie-less.
		if w := e.get(base+"/data/status", "", nil); w.Code != 200 {
			t.Fatal(w.Code)
		}
	})
}

func TestPublicSourceCeilingIsPerClientAndRetryable(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		if w := e.enableWith(t, []string{"status"}, false, 3, 5); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		for i := 0; i < 3; i++ {
			if w := e.get("/public/apps/"+e.id+"/data/status", "192.0.2.1:1", nil); w.Code != 200 {
				t.Fatal(i, w.Code)
			}
		}
		w := e.get("/public/apps/"+e.id+"/data/status", "192.0.2.1:2", nil)
		if w.Code != 429 || w.Header().Get("Retry-After") != "60" || w.Body.String() != `{"code":"rate_limited"}` {
			t.Fatal("fourth request", w.Code, w.Header())
		}
		if w := e.get("/public/apps/"+e.id+"/data/status", "192.0.2.9:1", nil); w.Code != 200 {
			t.Fatal("another source was refused", w.Code)
		}
	})
}

func TestPublicGrantRouteIsAReviewedAdminAction(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		p := e.preview(t)
		if p.Granted || !p.Ready || p.Disclosure != "Anyone who knows this address can read these responses without signing in." || p.URL != "/public/apps/"+e.id+"/" ||
			len(p.Declared.Routes) != 2 || p.Declared.Routes[0].Path != "/v1/public/history" || p.Declared.Routes[0].CacheSeconds != 10 || p.Declared.Document != "public/index.html" ||
			len(p.Policy.Routes) != 2 || p.Policy.ClientPerMinute != 60 || p.Policy.BackendPerSecond != 5 || len(p.DisclosureSHA256) != 64 {
			t.Fatalf("preview: %+v", p)
		}
		post := func(enabled bool, mutate func(map[string]any)) *httptest.ResponseRecorder {
			body := map[string]any{"enabled": enabled, "policyRevision": p.PolicyRevision, "routes": []string{"history"}, "document": false, "clientPerMinute": 60, "backendPerSecond": 5, "disclosureSha256": p.DisclosureSHA256}
			if mutate != nil {
				mutate(body)
			}
			raw, _ := json.Marshal(body)
			return e.admin(t, "POST", "public-projection", string(raw))
		}
		bad := map[string]func(map[string]any){
			"no digest":        func(b map[string]any) { b["disclosureSha256"] = strings.Repeat("0", 64) },
			"other digest":     func(b map[string]any) { b["disclosureSha256"] = platformDigest([]byte("x")) },
			"unknown route":    func(b map[string]any) { b["routes"] = []string{"admin"} },
			"no routes":        func(b map[string]any) { b["routes"] = []string{} },
			"unsorted":         func(b map[string]any) { b["routes"] = []string{"status", "history"} },
			"limit too high":   func(b map[string]any) { b["clientPerMinute"] = 601 },
			"backend too high": func(b map[string]any) { b["backendPerSecond"] = 21 },
			"zero":             func(b map[string]any) { b["backendPerSecond"] = 0 },
			"extra key":        func(b map[string]any) { b["policy"] = "{}" },
			"revision":         func(b map[string]any) { b["policyRevision"] = "9" },
			"string flag":      func(b map[string]any) { b["enabled"] = "true" },
			"null routes":      func(b map[string]any) { b["routes"] = nil },
			"document missing": func(b map[string]any) { delete(b, "document") },
		}
		for name, mutate := range bad {
			if w := post(true, mutate); w.Code < 400 || w.Code >= 500 {
				t.Fatal(name, w.Code, w.Body.String())
			}
		}
		if scalar[int](t, e.s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != 0 {
			t.Fatal("a refused request changed the grant")
		}
		// A withdrawal that carries a policy is refused too: it carries nothing.
		if w := post(false, nil); w.Code != 400 {
			t.Fatal("withdrawal with a policy", w.Code)
		}
		if w := post(true, nil); w.Code != 200 {
			t.Fatal("enable", w.Code, w.Body.String())
		}
		if got := scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id); got != `{"format":"portal.public-projection-policy/1-dev.1","routes":["history"],"document":false,"client_per_minute":60,"backend_per_second":5}` {
			t.Fatal("stored policy", got)
		}
		// Same policy again is a no-op, a different one is audited as a change.
		before := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.public_projection_%'`)
		if w := post(true, nil); w.Code != 200 || scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.public_projection_%'`) != before {
			t.Fatal("idempotent enable audited", w.Code)
		}
		if w := post(true, func(b map[string]any) { b["routes"] = []string{"history", "status"} }); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if w := e.disable(t); w.Code != 200 {
			t.Fatal(w.Code)
		}
		for _, action := range []string{"enabled", "policy_changed", "disabled"} {
			if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=? AND actor=?`, "app_platform.public_projection_"+action, e.id, e.actor.username) != 1 {
				t.Fatal("audit", action)
			}
		}
		// Anyone but an administrator is refused, and a signed-out caller too.
		mustExec(t, e.s.st, `UPDATE users SET role='user' WHERE username=?`, e.actor.username)
		if w := e.admin(t, "GET", "public-projection", ""); w.Code != 403 {
			t.Fatal("non-admin preview", w.Code)
		}
		if w := post(true, nil); w.Code != 403 {
			t.Fatal("non-admin enable", w.Code)
		}
		mustExec(t, e.s.st, `UPDATE users SET role='admin' WHERE username=?`, e.actor.username)
		r := httptest.NewRequest("POST", "/api/admin/app-platform/installations/"+e.id+"/public-projection", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("signed-out enable", w.Code)
		}
	})
}

func TestPublicGrantIsRefusedWhileNotReady(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		p := e.preview(t)
		body := `{"enabled":true,"policyRevision":"` + p.PolicyRevision + `","routes":["status"],"document":false,"clientPerMinute":60,"backendPerSecond":5,"disclosureSha256":"` + p.DisclosureSHA256 + `"}`
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='review_required' WHERE id=?`, e.id)
		w := e.admin(t, "POST", "public-projection", body)
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET phase='ready' WHERE id=?`, e.id)
		if w.Code != 409 || !strings.Contains(w.Body.String(), "app_platform_installation_not_ready") {
			t.Fatal("not ready", w.Code, w.Body.String())
		}
		if e.preview(t).Ready != true {
			t.Fatal("ready preview")
		}
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
		w = e.admin(t, "POST", "public-projection", body)
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, e.id)
		if w.Code != 409 {
			t.Fatal("disabled installation", w.Code)
		}
		if scalar[int](t, e.s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id) != 0 {
			t.Fatal("a refused enable changed the grant")
		}
	})
}

func TestPublicGrantWithPolicyDoesNotBlockBackendAccessAndTamperingDoes(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		a, err := e.s.preparePlatformWritableBackendAccess(context.Background(), e.actor, e.id)
		if err != nil {
			t.Fatal("a granted public policy refused the viewer access:", err)
		}
		a.Close()
		row := `app_platform_capability_grants SET %s WHERE installation_id=? AND capability='public.projection'`
		for name, set := range map[string]string{
			"garbage policy":   `policy_json='not json'`,
			"ungranted policy": `granted=0`,
			"foreign format":   `policy_json='{"format":"x"}'`,
		} {
			saved := scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id)
			mustExec(t, e.s.st, `UPDATE `+fmt.Sprintf(row, set), e.id)
			b, err := e.s.preparePlatformWritableBackendAccess(context.Background(), e.actor, e.id)
			if err == nil {
				b.Close()
				t.Fatal(name, "was accepted")
			}
			mustExec(t, e.s.st, `UPDATE app_platform_capability_grants SET granted=1,policy_json=? WHERE installation_id=? AND capability='public.projection'`, saved, e.id)
		}
		// Another capability keeps its exact reviewed policy: the relaxation is for this one only.
		mustExec(t, e.s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='notifications.publish'`, scalar[string](t, e.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='public.projection'`, e.id), e.id)
		if b, err := e.s.preparePlatformWritableBackendAccess(context.Background(), e.actor, e.id); err == nil {
			b.Close()
			t.Fatal("a public policy was accepted on another capability")
		}
	})
}

func TestPublicSummaryAuditCountsWithoutClientAddresses(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformPublicFixture(t, pg)
		e.enable(t)
		e.data("status", "")
		e.data("status", "")
		e.data("history", "x=1")
		e.data("nope", "")
		e.get("/public/apps/"+e.id+"/data/status", "203.0.113.77:9", nil)
		e.s.flushPlatformPublicSummary(e.id)
		var detail, ip string
		if err := e.s.st.queryRow(`SELECT detail,ip FROM audit_log WHERE action='app_platform.public_projection_summary' AND target_id=?`, e.id).Scan(&detail, &ip); err != nil {
			t.Fatal(err)
		}
		var d struct {
			Requests, CacheHits, BackendFetches int
			Refusals                            map[string]int
		}
		raw := map[string]any{}
		if json.Unmarshal([]byte(detail), &raw) != nil {
			t.Fatal(detail)
		}
		if raw["requests"].(float64) != 5 || raw["cache_hits"].(float64) != 2 || raw["backend_fetches"].(float64) != 1 || raw["refusals"].(map[string]any)["bad_query"].(float64) != 1 || raw["refusals"].(map[string]any)["route_refused"].(float64) != 1 {
			t.Fatal("summary", detail)
		}
		_ = d
		if ip != "" || strings.Contains(detail, "203.0.113") || strings.Contains(detail, "192.0.2") {
			t.Fatal("a client address reached the audit", ip, detail)
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.public_projection_summary'`) != 1 {
			t.Fatal("one summary per window")
		}
	})
}

func TestPublicSummaryWindowRollsOver(t *testing.T) {
	var p platformPublicState
	start := time.Unix(1_800_000_000, 0)
	if p.record("i", start, "request") != nil || p.record("i", start.Add(9*time.Minute), "hit") != nil {
		t.Fatal("window closed early")
	}
	done := p.record("i", start.Add(10*time.Minute), "request")
	if done == nil || done.requests != 1 || done.hits != 1 {
		t.Fatalf("%+v", done)
	}
}

func TestPublicLimiterCannotBeUsedToFlushOtherCallers(t *testing.T) {
	l := &platformPublicLimiter{capacity: 64}
	now := time.Now()
	for i := 0; i < 63; i++ {
		l.allow(fmt.Sprintf("ip:10.0.0.%d", i), 1, time.Minute, now)
	}
	for i, want := range []bool{true, true, false} {
		if l.allow("ip:victim", 2, time.Minute, now) != want {
			t.Fatal("victim allowance", i)
		}
	}
	// A flood of fresh keys fills nothing and resets nothing: the victim stays at its ceiling and
	// the keys without room share one bounded allowance.
	allowed := 0
	for i := 0; i < 5000; i++ {
		if l.allow(fmt.Sprintf("ip:198.51.%d.%d", i/250, i%250), 1, time.Minute, now) {
			allowed++
		}
	}
	if l.allow("ip:victim", 2, time.Minute, now) || l.allow("ip:10.0.0.1", 1, time.Minute, now) {
		t.Fatal("a flood reset a live caller")
	}
	if allowed > platformPublicOverflowPerWindow {
		t.Fatal("unbounded overflow", allowed)
	}
	if len(l.recs) > 64 {
		t.Fatal("table grew past its capacity", len(l.recs))
	}
	// Records that have lapsed make room again.
	later := now.Add(2 * time.Minute)
	if !l.allow("ip:fresh", 1, time.Minute, later) {
		t.Fatal("no room after the windows lapsed")
	}
	if platformPublicClientKey("2001:db8:1:2:3:4:5:6") != platformPublicClientKey("2001:db8:1:2:ffff::1") || platformPublicClientKey("2001:db8:1:2::1") == platformPublicClientKey("2001:db8:1:3::1") {
		t.Fatal("an IPv6 client owns one /64")
	}
	if platformPublicClientKey("192.0.2.1") == platformPublicClientKey("192.0.2.2") {
		t.Fatal("IPv4 clients are distinct")
	}
}

func TestPublicCacheBoundsAndIdentity(t *testing.T) {
	var p platformPublicState
	now := time.Now()
	entry := func(inst, key, tuple string, size int) *platformPublicEntry {
		return &platformPublicEntry{key: key, installation: inst, tuple: tuple, body: make([]byte, size), cacheSeconds: 10, expires: now.Add(time.Hour)}
	}
	p.useTuple("a", "t1")
	for i := 0; i < 300; i++ {
		if !p.store(entry("a", fmt.Sprint("k", i), "t1", 10), now) {
			t.Fatal("store", i)
		}
	}
	if n, _ := p.cacheSize("a"); n != platformPublicEntriesPerInstallation {
		t.Fatal("entries per installation", n)
	}
	if p.lookup("k0", now) != nil || p.lookup("k299", now) == nil {
		t.Fatal("oldest entries go first")
	}
	// Bytes: nine 1 MiB entries cannot all stay in 8 MiB.
	p.purge("a")
	p.useTuple("a", "t1")
	for i := 0; i < 9; i++ {
		p.store(entry("a", fmt.Sprint("b", i), "t1", 1<<20), now)
	}
	if _, b := p.cacheSize("a"); b > platformPublicBytesPerInstallation {
		t.Fatal("bytes per installation", b)
	}
	if p.store(entry("a", "huge", "t1", 9<<20), now) {
		t.Fatal("an entry larger than the installation bound was kept")
	}
	// The global bound is taken from the installation holding the most.
	for _, id := range []string{"b", "c", "d", "e", "f"} {
		p.useTuple(id, "t")
		for i := 0; i < 8; i++ {
			p.store(entry(id, id+fmt.Sprint(i), "t", 1<<20), now)
		}
	}
	if p.total > platformPublicBytesTotal {
		t.Fatal("global bytes", p.total)
	}
	// A new tuple empties the installation; a store under the old tuple is refused.
	p.useTuple("a", "t2")
	if n, _ := p.cacheSize("a"); n != 0 {
		t.Fatal("tuple change kept entries", n)
	}
	if p.store(entry("a", "late", "t1", 10), now) {
		t.Fatal("a response of the old release was stored")
	}
	if !p.store(entry("a", "ok", "t2", 10), now) {
		t.Fatal("store under the new tuple")
	}
	// An expired entry is gone on lookup.
	stale := entry("a", "stale", "t2", 10)
	stale.expires = now.Add(-time.Second)
	p.store(stale, now.Add(-2*time.Second))
	if p.lookup("stale", now) != nil {
		t.Fatal("expired entry served")
	}
}

func TestPublicBackendFetchIsBoundedByConcurrencyAndRate(t *testing.T) {
	var p platformPublicState
	now := time.Now()
	var ends []func()
	for i := 0; i < platformPublicFetchConcurrency; i++ {
		end, ok := p.beginFetch("i", 20, now)
		if !ok {
			t.Fatal("slot", i)
		}
		ends = append(ends, end)
	}
	if _, ok := p.beginFetch("i", 20, now); ok {
		t.Fatal("more than the concurrency")
	}
	for _, end := range ends {
		end()
	}
	// Tokens: a burst of the rate, then one per 1/rate second.
	var q platformPublicState
	granted := 0
	for i := 0; i < 50; i++ {
		if end, ok := q.beginFetch("j", 5, now); ok {
			end()
			granted++
		}
	}
	if granted != 5 {
		t.Fatal("burst", granted)
	}
	if end, ok := q.beginFetch("j", 5, now.Add(1200*time.Millisecond)); !ok {
		t.Fatal("no refill")
	} else {
		end()
	}
}

func TestPublicHandlersNeverTouchCredentials(t *testing.T) {
	for _, file := range []string{"app_platform_public_http.go", "app_platform_public_authority.go", "app_platform_public_state.go"} {
		raw, err := readTestFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{".Cookie(", "Cookies()", "Authorization", "sessionID(", "platformHTTPReader", "SetCookie", "s.verify("} {
			if strings.Contains(raw, forbidden) {
				t.Fatal(file, "touches", forbidden)
			}
		}
	}
	_ = http.StatusOK
}

func readTestFile(name string) (string, error) {
	raw, err := os.ReadFile(name)
	return string(raw), err
}
