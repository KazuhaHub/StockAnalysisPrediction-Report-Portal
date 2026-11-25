package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type platformMachineEnv struct {
	s        *Server
	mux      *http.ServeMux
	actor    platformActorSession
	cookie   string
	id       string
	calls    atomic.Int64
	mu       sync.Mutex
	seen     []string
	clients  []string
	response func(route appplatform.MachineRoute, query string, token platformMachineToken) (driver.BackendResponse, error)
}

func testMachineBlock() *appplatform.Machine {
	return &appplatform.Machine{Routes: []appplatform.MachineRoute{
		{ID: "bars", Path: "/v1/machine/bars", Query: []string{"from", "limit", "symbol", "to"}, ResponseBytes: 4096, TimeoutMS: 2000, Description: "Daily bars"},
		{ID: "extra", Path: "/v1/machine/extra", ResponseBytes: 1000, TimeoutMS: 2000},
		{ID: "margin", Path: "/v1/machine/margin/latest", Query: []string{"date"}, ResponseBytes: 1000, TimeoutMS: 2000},
	}}
}

// platformMachineFixture is a ready backend installation that declares machine.read and whose backend
// answers through a recording hook (the runner path is covered separately).
func platformMachineFixture(t *testing.T, pg bool) *platformMachineEnv {
	t.Helper()
	platformManagedFixtureOptional.set(t, []string{"machine.read"})
	platformManagedFixtureMachine.set(t, testMachineBlock())
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
	t.Cleanup(func() { mustExec(t, s.st, `DELETE FROM audit_log WHERE action LIKE 'app_platform.machine_%'`) })
	env := &platformMachineEnv{s: s, actor: actor, id: lease.installationID, mux: http.NewServeMux()}
	env.cookie = s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	s.wireRoutes(env.mux)
	env.response = func(route appplatform.MachineRoute, query string, token platformMachineToken) (driver.BackendResponse, error) {
		return driver.BackendResponse{Status: 200, Body: []byte(`{"route":"` + route.ID + `"}`)}, nil
	}
	s.platformMachineFetchHook = func(ctx context.Context, a platformMachineAuthority, token platformMachineToken, route appplatform.MachineRoute, query string) (driver.BackendResponse, error) {
		env.calls.Add(1)
		env.mu.Lock()
		env.seen = append(env.seen, route.ID+"?"+query)
		env.clients = append(env.clients, token.Handle)
		env.mu.Unlock()
		return env.response(route, query, token)
	}
	return env
}

func (e *platformMachineEnv) admin(t *testing.T, method, suffix, body string, cookie bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "/api/admin/app-platform/installations/"+e.id+"/"+suffix, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: e.cookie})
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *platformMachineEnv) preview(t *testing.T) platformMachinePreview {
	t.Helper()
	w := e.admin(t, "GET", "machine-read", "", true)
	var out platformMachinePreview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("preview", w.Code, w.Body.String())
	}
	return out
}

func (e *platformMachineEnv) enableWith(t *testing.T, routes []string, perSecond, concurrency int) *httptest.ResponseRecorder {
	t.Helper()
	p := e.preview(t)
	body, _ := json.Marshal(map[string]any{"enabled": true, "policyRevision": p.PolicyRevision, "routes": routes, "installationPerSecond": perSecond, "installationConcurrency": concurrency, "disclosureSha256": p.DisclosureSHA256})
	return e.admin(t, "POST", "machine-read", string(body), true)
}

func (e *platformMachineEnv) enable(t *testing.T) {
	t.Helper()
	if w := e.enableWith(t, []string{"bars", "extra", "margin"}, 50, 16); w.Code != 200 {
		t.Fatal("enable", w.Code, w.Body.String())
	}
}

func (e *platformMachineEnv) disable(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	p := e.preview(t)
	return e.admin(t, "POST", "machine-read", `{"enabled":false,"policyRevision":"`+p.PolicyRevision+`","routes":null,"installationPerSecond":null,"installationConcurrency":null,"disclosureSha256":null}`, true)
}

type machineMade struct {
	Token       string                   `json:"token"`
	Info        platformMachineTokenView `json:"tokenInfo"`
	OpenAPIPath string                   `json:"openapiPath"`
}

func (e *platformMachineEnv) createRaw(t *testing.T, label string, routes []string, perMinute int, cidrs []string, days int) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"label": label, "routes": routes, "perMinute": perMinute, "sourceCidrs": cidrs, "expiresInDays": days})
	return e.admin(t, "POST", "machine-tokens", string(body), true)
}

func (e *platformMachineEnv) create(t *testing.T, label string, routes []string, perMinute int, cidrs []string, days int) machineMade {
	t.Helper()
	w := e.createRaw(t, label, routes, perMinute, cidrs, days)
	var out machineMade
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !strings.HasPrefix(out.Token, "pmr_") {
		t.Fatal("create", w.Code, w.Body.String())
	}
	return out
}

func (e *platformMachineEnv) simple(t *testing.T) machineMade {
	t.Helper()
	return e.create(t, "Dify production", []string{"bars", "margin"}, 600, []string{}, 90)
}

func (e *platformMachineEnv) get(path, remote string, headers map[string]string) *httptest.ResponseRecorder {
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

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func (e *platformMachineEnv) data(token, route, query, remote string) *httptest.ResponseRecorder {
	target := "/machine/apps/" + e.id + "/data/" + route
	if query != "" {
		target += "?" + query
	}
	return e.get(target, remote, bearer(token))
}

const machineUnauthorized = `{"code":"unauthorized"}`

func TestMachineReadIsOffUntilGrantedAndTokensAreShownOnce(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		// Not the single page, and nothing to authenticate with.
		w := e.get("/machine/apps/"+e.id+"/data/bars", "", nil)
		if w.Code != 401 || w.Body.String() != machineUnauthorized || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal("unauthenticated:", w.Code, w.Body.String())
		}
		// No token can be made while the capability is off.
		if w := e.createRaw(t, "x", []string{"bars"}, 60, []string{}, 90); w.Code != 409 {
			t.Fatal("token made with the capability off", w.Code, w.Body.String())
		}
		p := e.preview(t)
		if p.Granted || !p.Ready || p.Disclosure != "Holders of a machine token for this app can read these responses without signing in, until the token expires or is revoked." || p.OpenAPIPath != "/machine/apps/"+e.id+"/openapi.json" || len(p.Declared.Routes) != 3 {
			t.Fatalf("preview: %+v", p)
		}
		// Enabling needs the digest of what was shown.
		stale, _ := json.Marshal(map[string]any{"enabled": true, "policyRevision": p.PolicyRevision, "routes": []string{"bars"}, "installationPerSecond": 5, "installationConcurrency": 4, "disclosureSha256": strings.Repeat("a", 64)})
		if w := e.admin(t, "POST", "machine-read", string(stale), true); w.Code != 409 {
			t.Fatal("enable without the digest", w.Code)
		}
		if w := e.enableWith(t, []string{"bars", "nope"}, 5, 4); w.Code != 400 {
			t.Fatal("enable with an undeclared route", w.Code)
		}
		if w := e.enableWith(t, []string{"bars"}, 51, 4); w.Code != 400 {
			t.Fatal("enable above the per second maximum", w.Code)
		}
		e.enable(t)
		made := e.simple(t)
		// The secret is shown by the create response alone.
		secret := made.Token[17:]
		if len(made.Token) != 60 || made.Info.Handle != made.Token[4:16] || made.Info.State != "active" || made.Info.Label != "Dify production" || made.OpenAPIPath != "/machine/apps/"+e.id+"/openapi.json" {
			t.Fatalf("created: %+v", made)
		}
		for name, w := range map[string]*httptest.ResponseRecorder{"list": e.admin(t, "GET", "machine-tokens", "", true), "preview": e.admin(t, "GET", "machine-read", "", true)} {
			if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "secret_sha256") || strings.Contains(w.Body.String(), made.Token) {
				t.Fatal(name, "shows the secret or its digest")
			}
		}
		// The database holds only the digest: neither the token nor its secret appears in any table or
		// audit row.
		dump := dumpOf(t, e.s.st)
		if bytes.Contains(dump, []byte(secret)) || bytes.Contains(dump, []byte(made.Token)) {
			t.Fatal("the secret is stored")
		}
		if scalar[string](t, e.s.st, `SELECT secret_sha256 FROM app_platform_machine_tokens WHERE handle=?`, made.Info.Handle) != appplatform.MachineSecretDigest(secret) {
			t.Fatal("the stored digest is not the digest of the secret part")
		}
		if got := e.data(made.Token, "bars", "", ""); got.Code != 200 || got.Body.String() != `{"route":"bars"}` {
			t.Fatal("granted route", got.Code, got.Body.String())
		}
		for _, row := range []string{platformMachineEnabledAudit, platformMachineTokenCreatedAudit} {
			if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, row) != 1 {
				t.Fatal("audit row missing:", row)
			}
		}
		if strings.Contains(scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=?`, platformMachineTokenCreatedAudit), secret) {
			t.Fatal("the audit row carries the secret")
		}
	})
}

func TestMachineTokenCreationRules(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		if w := e.enableWith(t, []string{"bars", "margin"}, 5, 4); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		cases := map[string]*httptest.ResponseRecorder{
			"a route the policy left out": e.createRaw(t, "x", []string{"extra"}, 60, []string{}, 90),
			"an undeclared route":         e.createRaw(t, "x", []string{"nope"}, 60, []string{}, 90),
			"no routes":                   e.createRaw(t, "x", []string{}, 60, []string{}, 90),
			"a repeated route":            e.createRaw(t, "x", []string{"bars", "bars"}, 60, []string{}, 90),
			"no label":                    e.createRaw(t, "", []string{"bars"}, 60, []string{}, 90),
			"a long label":                e.createRaw(t, strings.Repeat("l", 65), []string{"bars"}, 60, []string{}, 90),
			"a control in the label":      e.createRaw(t, "a\nb", []string{"bars"}, 60, []string{}, 90),
			"zero per minute":             e.createRaw(t, "x", []string{"bars"}, 0, []string{}, 90),
			"601 per minute":              e.createRaw(t, "x", []string{"bars"}, 601, []string{}, 90),
			"zero days":                   e.createRaw(t, "x", []string{"bars"}, 60, []string{}, 0),
			"366 days":                    e.createRaw(t, "x", []string{"bars"}, 60, []string{}, 366),
			"a bad cidr":                  e.createRaw(t, "x", []string{"bars"}, 60, []string{"10.0.0.0/33"}, 90),
			"nine cidrs":                  e.createRaw(t, "x", []string{"bars"}, 60, []string{"10.0.0.0/8", "10.1.0.0/16", "10.2.0.0/16", "10.3.0.0/16", "10.4.0.0/16", "10.5.0.0/16", "10.6.0.0/16", "10.7.0.0/16", "10.8.0.0/16"}, 90),
		}
		for name, w := range cases {
			if w.Code != 400 {
				t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
			}
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_machine_tokens`) != 0 {
			t.Fatal("a refused request left a token")
		}
		made := e.create(t, "Edge", []string{"margin", "bars"}, 10, []string{"10.1.2.3", "10.0.0.0/8"}, 365)
		row, err := e.s.lookupPlatformMachineToken(context.Background(), made.Info.Handle)
		if err != nil || strings.Join(row.Routes, ",") != "bars,margin" || strings.Join(row.CIDRs, ",") != "10.0.0.0/8,10.1.2.3/32" || row.PerMinute != 10 || row.CreatedBy != e.actor.username ||
			row.ExpiresMS-row.CreatedMS != 365*24*60*60*1000 || row.PolicyRevision != 1 {
			t.Fatalf("stored: %+v %v", row, err)
		}
		// At most sixteen active tokens per installation.
		for i := 1; i < appplatform.MaxMachineActiveTokens; i++ {
			e.create(t, fmt.Sprintf("t%d", i), []string{"bars"}, 60, []string{}, 30)
		}
		if w := e.createRaw(t, "one too many", []string{"bars"}, 60, []string{}, 30); w.Code != 409 {
			t.Fatal("seventeenth token", w.Code)
		}
		// A non-administrator cannot manage tokens, whatever else is presented.
		if w := e.admin(t, "GET", "machine-tokens", "", false); w.Code != 401 && w.Code != 403 {
			t.Fatal("tokens listed without a session", w.Code)
		}
	})
}

// Every way of failing to authenticate is the same answer, and none of them reaches the backend.
func TestMachineAuthenticationFailuresAreUniform(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		good := e.simple(t)
		revoked := e.simple(t)
		expired := e.simple(t)
		if w := e.admin(t, "POST", "machine-tokens/"+revoked.Info.Handle+"/revoke", `{}`, true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		mustExec(t, e.s.st, `UPDATE app_platform_machine_tokens SET created_ms=1,expires_ms=2 WHERE handle=?`, expired.Info.Handle)
		// A token of the same installation presented for another one, and a made-up installation.
		other := "00000000-0000-4000-8000-000000000000"
		wrongSecret := good.Token[:17] + strings.Repeat("A", 43)
		apiToken := "0123456789abcdef0123456789abcdef0123456789abcdef"
		if err := e.s.st.CreateToken(apiToken, "all", "all", ""); err != nil {
			t.Fatal(err)
		}
		type try struct {
			name, path string
			headers    map[string]string
		}
		base := "/machine/apps/" + e.id + "/data/bars"
		tries := []try{
			{"no header", base, nil},
			{"basic", base, map[string]string{"Authorization": "Basic " + good.Token}},
			{"no scheme", base, map[string]string{"Authorization": good.Token}},
			{"lower case scheme", base, map[string]string{"Authorization": "bearer " + good.Token}},
			{"truncated", base, bearer(good.Token[:59])},
			{"padded", base, bearer(good.Token + "x")},
			{"wrong secret", base, bearer(wrongSecret)},
			{"unknown handle", base, bearer("pmr_aaaaaaaaaaaa_" + good.Token[17:])},
			{"revoked", base, bearer(revoked.Token)},
			{"expired", base, bearer(expired.Token)},
			{"another installation", "/machine/apps/" + other + "/data/bars", bearer(good.Token)},
			{"not a uuid", "/machine/apps/not-a-uuid/data/bars", bearer(good.Token)},
			{"an api token with every scope", base, bearer(apiToken)},
			{"a session cookie alone", base, map[string]string{"Cookie": cookieName + "=" + e.cookie}},
			{"a session cookie and a wrong token", base, map[string]string{"Cookie": cookieName + "=" + e.cookie, "Authorization": "Bearer " + wrongSecret}},
			{"the openapi route", "/machine/apps/" + e.id + "/openapi.json", bearer(revoked.Token)},
		}
		var first *httptest.ResponseRecorder
		for i, c := range tries {
			w := e.get(c.path, fmt.Sprintf("10.20.%d.1:1", i), c.headers)
			if w.Code != 401 || w.Body.String() != machineUnauthorized {
				t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
			}
			if first == nil {
				first = w
			}
			for _, h := range []string{"Content-Type", "Cache-Control", "WWW-Authenticate", "Content-Security-Policy", "X-Content-Type-Options"} {
				if w.Header().Get(h) != first.Header().Get(h) {
					t.Fatalf("%s: header %s differs", c.name, h)
				}
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("%s: CORS or cookie header", c.name)
			}
		}
		// Two Authorization headers are refused too.
		r := httptest.NewRequest("GET", base, nil)
		r.Header.Add("Authorization", "Bearer "+good.Token)
		r.Header.Add("Authorization", "Bearer "+good.Token)
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatal("two Authorization headers", w.Code)
		}
		if e.calls.Load() != 0 {
			t.Fatal("a refused request reached the backend")
		}
		// The api token stays unusable the other way round too: a machine token is no api token.
		if e.s.st.TokenValid(good.Token, "reports") {
			t.Fatal("a machine token is a valid api token")
		}
		if w := e.get(base, "10.30.0.1:1", bearer(good.Token)); w.Code != 200 {
			t.Fatal("the good token", w.Code)
		}
	})
}

func TestMachineRoutesRoutePermissionAndPolicyShrink(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.create(t, "Dify", []string{"bars", "margin"}, 600, []string{}, 90)
		if w := e.data(made.Token, "bars", "", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
		// Declared and reviewed but not on this token: 403 to its holder. Undeclared: 404.
		if w := e.data(made.Token, "extra", "", ""); w.Code != 403 || w.Body.String() != `{"code":"route_not_permitted"}` {
			t.Fatal("route not on the token", w.Code, w.Body.String())
		}
		for _, id := range []string{"nope", "Bars", "bars%2F..", "bars.json"} {
			if w := e.data(made.Token, id, "", ""); w.Code != 404 || w.Body.String() != `{"code":"not_found"}` {
				t.Fatal("undeclared route", id, w.Code, w.Body.String())
			}
		}
		// Only declared names, once each, within bounds.
		for _, q := range []string{"nope=1", "from=a&from=b", "from", "=1", "from=" + strings.Repeat("a", 257), "from=%7f", "from=%00", "from=é"} {
			if w := e.data(made.Token, "bars", q, ""); w.Code != 400 || w.Body.String() != `{"code":"bad_query"}` {
				t.Fatal("bad query", q, w.Code, w.Body.String())
			}
		}
		if e.calls.Load() != 1 {
			t.Fatal("a refused request reached the backend", e.calls.Load())
		}
		if w := e.data(made.Token, "bars", "to=b&from=a&symbol=600519", ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if e.seen[len(e.seen)-1] != "bars?from=a&symbol=600519&to=b" {
			t.Fatal("query not canonicalised:", e.seen[len(e.seen)-1])
		}
		// Policy shrink: a route dropped from the policy stops working on a token that still lists it.
		if w := e.enableWith(t, []string{"bars"}, 50, 16); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if w := e.data(made.Token, "margin", "", ""); w.Code != 403 {
			t.Fatal("a route dropped from the policy", w.Code)
		}
		if w := e.data(made.Token, "bars", "", ""); w.Code != 200 {
			t.Fatal("the route that stayed", w.Code)
		}
		if scalar[string](t, e.s.st, `SELECT routes_json FROM app_platform_machine_tokens WHERE handle=?`, made.Info.Handle) != `["bars","margin"]` {
			t.Fatal("the token was rewritten")
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachinePolicyChangedAudit) != 1 {
			t.Fatal("policy change not audited")
		}
	})
}

func TestMachineSourceCIDRs(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		fenced := e.create(t, "Fenced", []string{"bars"}, 600, []string{"10.0.0.0/8", "2001:db8::/32"}, 90)
		open := e.create(t, "Open", []string{"bars"}, 600, []string{}, 90)
		for remote, want := range map[string]int{"10.1.2.3:5": 200, "[2001:db8::7]:5": 200, "192.0.2.1:5": 401, "[2001:db9::1]:5": 401, "11.0.0.1:5": 401} {
			if w := e.data(fenced.Token, "bars", "", remote); w.Code != want || (want == 401 && w.Body.String() != machineUnauthorized) {
				t.Fatal("fenced token from", remote, w.Code)
			}
			if w := e.data(open.Token, "bars", "", remote); w.Code != 200 {
				t.Fatal("open token from", remote, w.Code)
			}
		}
		// A forwarding header is believed only from a trusted proxy.
		if w := e.get("/machine/apps/"+e.id+"/data/bars", "192.0.2.9:5", map[string]string{"Authorization": "Bearer " + fenced.Token, "X-Forwarded-For": "10.1.2.3"}); w.Code != 401 {
			t.Fatal("an untrusted forwarding header opened the fence", w.Code)
		}
	})
}

func TestMachineLimits(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		if w := e.enableWith(t, []string{"bars", "extra", "margin"}, 4, 16); w.Code != 200 {
			t.Fatal(w.Code)
		}
		slow := e.create(t, "Slow", []string{"bars"}, 3, []string{}, 90)
		other := e.create(t, "Other", []string{"bars"}, 600, []string{}, 90)
		for i := 0; i < 3; i++ {
			if w := e.data(slow.Token, "bars", "", fmt.Sprintf("10.40.0.%d:1", i+1)); w.Code != 200 {
				t.Fatal("within the per minute limit", i, w.Code)
			}
		}
		w := e.data(slow.Token, "bars", "", "10.40.0.9:1")
		if w.Code != 429 || w.Header().Get("Retry-After") == "" || w.Body.String() != `{"code":"rate_limited"}` {
			t.Fatal("beyond the per minute limit", w.Code, w.Body.String())
		}
		calls := e.calls.Load()
		// Another token of the same installation is not affected by it, but the installation's own per
		// second limit (4) holds across tokens: three more calls fill it and the next is refused.
		if w := e.data(other.Token, "bars", "", "10.40.1.1:1"); w.Code != 200 {
			t.Fatal("another token", w.Code)
		}
		if e.calls.Load() != calls+1 {
			t.Fatal("calls")
		}
		last := 0
		for i := 0; i < 10 && last != 429; i++ {
			last = e.data(other.Token, "bars", "", fmt.Sprintf("10.40.2.%d:1", i+1)).Code
		}
		if last != 429 {
			t.Fatal("the installation limit did not hold across tokens")
		}
		// The per source ceiling applies before the token is looked at.
		for i := 0; i < platformMachineSourcePerMinute+5; i++ {
			e.get("/machine/apps/"+e.id+"/data/bars", "10.50.0.1:1", nil)
		}
		if w := e.get("/machine/apps/"+e.id+"/data/bars", "10.50.0.1:1", bearer(other.Token)); w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatal("source ceiling", w.Code)
		}
	})
}

func TestMachineInstallationConcurrencyIsBounded(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		if w := e.enableWith(t, []string{"bars", "extra", "margin"}, 50, 2); w.Code != 200 {
			t.Fatal(w.Code)
		}
		made := e.create(t, "Busy", []string{"bars"}, 600, []string{}, 90)
		release := make(chan struct{})
		started := make(chan struct{}, 4)
		e.response = func(route appplatform.MachineRoute, query string, token platformMachineToken) (driver.BackendResponse, error) {
			started <- struct{}{}
			<-release
			return driver.BackendResponse{Status: 200, Body: []byte(`{}`)}, nil
		}
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.data(made.Token, "bars", "", fmt.Sprintf("10.60.0.%d:1", i+1))
			}()
		}
		<-started
		<-started
		w := e.data(made.Token, "bars", "", "10.60.1.1:1")
		if w.Code != 503 || w.Header().Get("Retry-After") == "" {
			t.Fatal("third concurrent call", w.Code)
		}
		close(release)
		wg.Wait()
		if e.s.platformMachine.concurrent(e.id) != 0 {
			t.Fatal("a slot leaked")
		}
		e.response = func(route appplatform.MachineRoute, query string, token platformMachineToken) (driver.BackendResponse, error) {
			return driver.BackendResponse{Status: 200, Body: []byte(`{}`)}, nil
		}
		if w := e.data(made.Token, "bars", "", "10.60.2.1:1"); w.Code != 200 {
			t.Fatal("after the calls ended", w.Code)
		}
	})
}

func TestMachineBackendAnswersAreMappedAndNothingElsePassesThrough(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.create(t, "Maps", []string{"bars", "extra"}, 600, []string{}, 90)
		big := bytes.Repeat([]byte("a"), 2<<20)
		long := []byte(`{"m":"` + strings.Repeat("x", platformMachinePassBytes) + `"}`)
		cases := []struct {
			name   string
			route  string
			status int
			body   []byte
			err    error
			want   int
			code   string
		}{
			{"json", "bars", 200, []byte(`{"a":1}`), nil, 200, ""},
			{"html", "bars", 200, []byte(`<html>`), nil, 502, "backend_error"},
			{"empty", "bars", 200, nil, nil, 502, "backend_error"},
			{"a redirect", "bars", 302, []byte(`{}`), nil, 502, "backend_error"},
			{"created", "bars", 201, []byte(`{}`), nil, 502, "backend_error"},
			{"over the route's size", "extra", 200, []byte(`{"m":"` + strings.Repeat("x", 1000) + `"}`), nil, 502, "backend_error"},
			{"two mebibytes", "bars", 200, big, nil, 502, "backend_error"},
			{"validation message", "bars", 422, []byte(`{"error":"from must be a date"}`), nil, 422, ""},
			{"bad request", "bars", 400, []byte(`{"error":"x"}`), nil, 400, ""},
			{"not found", "bars", 404, []byte(`{"error":"no such symbol"}`), nil, 404, ""},
			{"a long validation message", "bars", 422, long, nil, 502, "backend_error"},
			{"forbidden", "bars", 403, []byte(`{"error":"x"}`), nil, 502, "backend_error"},
			{"conflict", "bars", 409, []byte(`{"error":"x"}`), nil, 502, "backend_error"},
			{"throttled by the app", "bars", 429, []byte(`{"error":"x"}`), nil, 429, "rate_limited"},
			{"runner unavailable", "bars", 0, nil, errors.New("down"), 503, "unavailable"},
		}
		for i, c := range cases {
			e.response = func(route appplatform.MachineRoute, query string, token platformMachineToken) (driver.BackendResponse, error) {
				return driver.BackendResponse{Status: c.status, Body: append([]byte(nil), c.body...)}, c.err
			}
			w := e.data(made.Token, c.route, "", fmt.Sprintf("10.70.0.%d:1", i+1))
			if w.Code != c.want {
				t.Fatalf("%s: %d %s", c.name, w.Code, w.Body.String())
			}
			if c.code != "" && w.Body.String() != `{"code":"`+c.code+`"}` {
				t.Fatalf("%s: %s", c.name, w.Body.String())
			}
			if c.want == 200 || c.want == 400 || c.want == 404 || c.want == 422 {
				if w.Body.String() != string(c.body) {
					t.Fatalf("%s: body not passed through", c.name)
				}
			}
			h := w.Header()
			if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != "default-src 'none'; frame-ancestors 'none'" || h.Get("Content-Type") != "application/json" {
				t.Fatalf("%s: headers %v", c.name, h)
			}
			if h.Get("Access-Control-Allow-Origin") != "" || h.Get("Set-Cookie") != "" || h.Get("Access-Control-Allow-Credentials") != "" {
				t.Fatalf("%s: CORS or cookie header", c.name)
			}
		}
	})
}

func TestMachineRoutesAreGetOnlyWithNoCORS(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.simple(t)
		for _, path := range []string{"/machine/apps/" + e.id + "/data/bars", "/machine/apps/" + e.id + "/openapi.json", "/machine/", "/machine/apps/" + e.id + "/other"} {
			for _, method := range []string{"OPTIONS", "POST", "PUT", "DELETE", "PATCH", "HEAD"} {
				r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
				r.Header.Set("Origin", "https://evil.example")
				r.Header.Set("Access-Control-Request-Method", "GET")
				r.Header.Set("Authorization", "Bearer "+made.Token)
				w := httptest.NewRecorder()
				e.mux.ServeHTTP(w, r)
				if w.Code != 405 || !strings.Contains(w.Header().Get("Allow"), "GET") {
					t.Fatal(method, path, w.Code, w.Header())
				}
				for k := range w.Header() {
					if strings.HasPrefix(strings.ToLower(k), "access-control-") {
						t.Fatal("CORS header on", method, path, k)
					}
				}
			}
		}
		// A GET of an unknown path under /machine/ is a JSON 404, never the single page.
		if w := e.get("/machine/elsewhere", "", nil); w.Code != 404 || w.Body.String() != `{"code":"not_found"}` {
			t.Fatal("unknown path", w.Code, w.Body.String())
		}
		if e.calls.Load() != 0 {
			t.Fatal("the backend was reached")
		}
	})
}

// What stops a token takes effect on the next request, in the same second, with no backend call.
func TestMachineRevocationExpiryDisableAndPauseAreImmediate(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		check := func(token string, want int, why string) {
			t.Helper()
			before := e.calls.Load()
			if w := e.data(token, "bars", "", "10.80.0.1:1"); w.Code != want {
				t.Fatal(why, w.Code, w.Body.String())
			}
			if want != 200 && e.calls.Load() != before {
				t.Fatal(why, "reached the backend")
			}
		}
		revoked, expiring, paused := e.simple(t), e.simple(t), e.simple(t)
		for _, m := range []machineMade{revoked, expiring, paused} {
			check(m.Token, 200, "fresh token")
		}
		if w := e.admin(t, "POST", "machine-tokens/"+revoked.Info.Handle+"/revoke", `{}`, true); w.Code != 200 {
			t.Fatal(w.Code)
		}
		check(revoked.Token, 401, "revoked")
		if w := e.admin(t, "POST", "machine-tokens/"+revoked.Info.Handle+"/revoke", `{}`, true); w.Code != 200 {
			t.Fatal("revoking twice", w.Code)
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachineTokenRevokedAudit) != 1 {
			t.Fatal("revocation audited twice")
		}
		now := time.Now().UnixMilli()
		mustExec(t, e.s.st, `UPDATE app_platform_machine_tokens SET created_ms=?,expires_ms=? WHERE handle=?`, now-100_000, now-1000, expiring.Info.Handle)
		check(expiring.Token, 401, "expired")
		e.s.sweepPlatformMachineExpiry(time.Now())
		e.s.sweepPlatformMachineExpiry(time.Now())
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachineTokenExpiredAudit) != 1 {
			t.Fatal("expiry not reported exactly once")
		}
		// A restore pause refuses with 503 and no backend call; lifting it restores service.
		mustExec(t, e.s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		check(paused.Token, 503, "restore pause")
		mustExec(t, e.s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
		check(paused.Token, 200, "pause lifted")
		// An installation that is not ready is unavailable, not unauthorised.
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
		check(paused.Token, 503, "disabled installation")
		mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='enabled' WHERE id=?`, e.id)
		// Withdrawing the capability revokes every token in the same transaction.
		if w := e.disable(t); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		check(paused.Token, 401, "capability disabled")
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_machine_tokens WHERE state='active'`) != 0 {
			t.Fatal("a token survived the withdrawal")
		}
		// Enabling again does not bring an old token back.
		e.enable(t)
		check(paused.Token, 401, "capability re-enabled")
		// A new token works, and uninstalling is the same revocation by another path.
		fresh := e.simple(t)
		check(fresh.Token, 200, "new token")
		err := e.s.withAppPlatformActorContext(context.Background(), e.actor, false, func(m migExec, actor platformActorFacts, c platformActorControl) error {
			n, err := revokePlatformMachineTokens(m, actor, c.nowMS, e.id, "uninstall")
			if err == nil && n != 1 {
				err = fmt.Errorf("revoked %d tokens", n)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		check(fresh.Token, 401, "uninstalled")
	})
}

func TestMachineRotation(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		old := e.create(t, "Dify", []string{"bars", "margin"}, 77, []string{"10.0.0.0/8"}, 365)
		rotate := func(handle string, grace, days int) *httptest.ResponseRecorder {
			return e.admin(t, "POST", "machine-tokens/"+handle+"/rotate", fmt.Sprintf(`{"graceDays":%d,"expiresInDays":%d}`, grace, days), true)
		}
		for name, w := range map[string]*httptest.ResponseRecorder{"grace 8": rotate(old.Info.Handle, 8, 90), "grace -1": rotate(old.Info.Handle, -1, 90), "366 days": rotate(old.Info.Handle, 3, 366), "no days": rotate(old.Info.Handle, 3, 0), "unknown": rotate("aaaaaaaaaaaa", 3, 90)} {
			if w.Code != 400 && w.Code != 404 {
				t.Fatal(name, w.Code, w.Body.String())
			}
		}
		w := rotate(old.Info.Handle, 3, 90)
		var next machineMade
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &next) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		if next.Info.Handle == old.Info.Handle || next.Info.Label != "Dify" || next.Info.PerMinute != 77 || strings.Join(next.Info.Routes, ",") != "bars,margin" || strings.Join(next.Info.SourceCIDRs, ",") != "10.0.0.0/8" {
			t.Fatalf("successor: %+v", next.Info)
		}
		oldRow, _ := e.s.lookupPlatformMachineToken(context.Background(), old.Info.Handle)
		now := time.Now().UnixMilli()
		if oldRow.ExpiresMS > now+3*24*60*60*1000+5000 || oldRow.ExpiresMS < now+3*24*60*60*1000-60_000 || oldRow.State != "active" {
			t.Fatalf("the predecessor's grace: %d vs now %d", oldRow.ExpiresMS, now)
		}
		// Both work during the overlap.
		for _, m := range []machineMade{old, next} {
			if w := e.data(m.Token, "bars", "", "10.1.1.1:1"); w.Code != 200 {
				t.Fatal("during the overlap", w.Code)
			}
		}
		// A grace beyond the current expiry never lengthens it, and zero ends it at once.
		short := e.create(t, "Short", []string{"bars"}, 60, []string{}, 1)
		if w := rotate(short.Info.Handle, 7, 30); w.Code != 200 {
			t.Fatal(w.Code)
		}
		row, _ := e.s.lookupPlatformMachineToken(context.Background(), short.Info.Handle)
		if row.ExpiresMS > now+24*60*60*1000+60_000 {
			t.Fatal("rotation lengthened the predecessor", row.ExpiresMS-now)
		}
		third := rotate(next.Info.Handle, 0, 30)
		if third.Code != 200 {
			t.Fatal(third.Code, third.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
		if w := e.data(next.Token, "bars", "", "10.1.1.2:1"); w.Code != 401 {
			t.Fatal("a rotation with no grace left the predecessor valid", w.Code)
		}
		if w := rotate(next.Info.Handle, 3, 30); w.Code != 404 {
			t.Fatal("rotating an expired token", w.Code)
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachineTokenRotatedAudit) != 3 {
			t.Fatal("rotations not audited")
		}
	})
}

func TestMachineAuditSourceSummaryAndLastUse(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.simple(t)
		for i := 0; i < 3; i++ {
			if w := e.data(made.Token, "bars", "from=a", "203.0.113.5:4000"); w.Code != 200 {
				t.Fatal(w.Code)
			}
		}
		e.data(made.Token, "extra", "", "203.0.113.5:4000")
		e.data("pmr_aaaaaaaaaaaa_"+made.Token[17:], "bars", "", "203.0.113.6:1")
		// The address is in audit once per token per day, with no secret, header or body.
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformMachineTokenSourceAudit) != 1 {
			t.Fatal("source audit rows")
		}
		detail := scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=?`, platformMachineTokenSourceAudit)
		if !strings.Contains(detail, `"address":"203.0.113.5"`) || !strings.Contains(detail, made.Info.Handle) || strings.Contains(detail, made.Token[17:]) {
			t.Fatal(detail)
		}
		if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE detail LIKE ? OR detail LIKE ?`, "%"+made.Token[17:]+"%", "%Bearer%") != 0 {
			t.Fatal("a secret or header reached the audit log")
		}
		// last_used is written, at most once a minute.
		first := scalar[int64](t, e.s.st, `SELECT last_used_ms FROM app_platform_machine_tokens WHERE handle=?`, made.Info.Handle)
		time.Sleep(20 * time.Millisecond)
		e.data(made.Token, "bars", "", "203.0.113.5:4000")
		if first == 0 || scalar[int64](t, e.s.st, `SELECT last_used_ms FROM app_platform_machine_tokens WHERE handle=?`, made.Info.Handle) != first {
			t.Fatal("last use written more than once a minute")
		}
		// The traffic summary counts requests and refusals by class, never an address.
		e.s.flushPlatformMachineSummary(e.id)
		var summary struct {
			Requests     int            `json:"requests"`
			BackendCalls int            `json:"backend_calls"`
			Bytes        int            `json:"bytes_out"`
			Refusals     map[string]int `json:"refusals"`
			Handle       string         `json:"handle"`
		}
		raw := scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=? AND target_id=?`, platformMachineSummaryAudit, e.id)
		if json.Unmarshal([]byte(raw), &summary) != nil || summary.Requests != 5 || summary.BackendCalls != 4 || summary.Bytes == 0 || summary.Refusals["route_not_permitted"] != 1 || summary.Handle != made.Info.Handle || strings.Contains(raw, "203.0.113") {
			t.Fatal(raw)
		}
		list := e.admin(t, "GET", "machine-tokens", "", true)
		if list.Code != 200 || !strings.Contains(list.Body.String(), platformMachineSummaryAudit) || !strings.Contains(list.Body.String(), `"address":"203.0.113.5"`) || !strings.Contains(list.Body.String(), `"lastUsedAt":"`) {
			t.Fatal(list.Code, list.Body.String())
		}
	})
}

// What the Portal signs for a machine request, and what the runner will accept of it: the command
// binds the digest of a machine token that names the client and never the secret, the app's verifier
// accepts it for exactly this request, and no other kind of token verifies as it (or it as them).
func TestMachineRequestSignsAnAssertionTheRunnerAndAppAccept(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.simple(t)
		auth, err := e.s.readPlatformMachineAuthority(context.Background(), e.id)
		if err != nil {
			t.Fatal(err)
		}
		row, err := e.s.lookupPlatformMachineToken(context.Background(), made.Info.Handle)
		if err != nil {
			t.Fatal(err)
		}
		route, _ := auth.route("bars")
		target := appplatform.MachineRequestTarget(route.Path, "from=1")
		session := platformActorSession{anonymousMachine: row.ID}
		var raw, sig []byte
		var token string
		var command control.WritableBackendRequestCommand
		a, err := e.s.withPlatformWritableBackendAccess(context.Background(), session, e.id, func(a platformWritableBackendAccess, seed []byte) error {
			var err error
			raw, sig, token, command, err = signPlatformWritableBackendRequest(a, seed, "GET", target, nil, "fixture-boot", platformMachineAssertion(auth, row, route, "from=1"))
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		defer a.Close()
		if !appplatform.IsMachineAssertion(token) || appplatform.IsPublicAssertion(token) || command.AssertionSHA256 != platformDigest([]byte(token)) || command.Method != "GET" || command.Path != target || command.BodySHA256 != platformDigest(nil) {
			t.Fatal("command does not bind the machine token", command)
		}
		v, err := control.VerifyWritableBackendRequest(raw, sig, a.intent.start.writer.authority, time.Now().UnixMilli())
		if err != nil || control.ValidateWritableBackendRequestData(v, nil, token) != nil {
			t.Fatal("the runner refused the machine request", err)
		}
		signer, err := a.intent.registration.Signer("fixture-boot")
		if err != nil {
			t.Fatal(err)
		}
		claims, err := appplatform.VerifyMachineAssertion(signer, token, target, time.Now())
		if err != nil || claims.Route != "bars" || claims.Generation != a.intent.generation || claims.ClientID != made.Info.Handle || claims.ClientLabel != "Dify production" {
			t.Fatal(claims, err)
		}
		if strings.Contains(token, made.Token[17:]) || strings.Contains(string(raw), made.Token[17:]) {
			t.Fatal("the secret is in the assertion")
		}
		// Kinds stay apart in both directions.
		viewer, err := signer.Sign("GET", target, nil, protocol.Viewer{Subject: "s", Role: "user", OUs: []string{"o"}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if appplatform.IsMachineAssertion(viewer) {
			t.Fatal("a viewer token looks like a machine token")
		}
		if _, err := appplatform.VerifyMachineAssertion(signer, "mch."+viewer, target, time.Now()); err == nil {
			t.Fatal("a viewer token verified as machine")
		}
		if _, err := appplatform.VerifyPublicAssertion(signer, "pub."+strings.TrimPrefix(token, "mch."), target, time.Now()); err == nil {
			t.Fatal("a machine token verified as public")
		}
		if control.ValidateWritableBackendRequestData(v, []byte(`{}`), token) == nil {
			t.Fatal("a body rode a machine token")
		}
		// An access read for another token, or after this token is revoked, signs nothing.
		other := e.simple(t)
		otherRow, _ := e.s.lookupPlatformMachineToken(context.Background(), other.Info.Handle)
		_, err = e.s.withPlatformWritableBackendAccess(context.Background(), platformActorSession{anonymousMachine: otherRow.ID}, e.id, func(a platformWritableBackendAccess, seed []byte) error {
			_, _, _, _, err := signPlatformWritableBackendRequest(a, seed, "GET", target, nil, "fixture-boot", platformMachineAssertion(auth, row, route, "from=1"))
			return err
		})
		if err == nil {
			t.Fatal("an assertion was signed for a token other than the one admitted")
		}
		if w := e.admin(t, "POST", "machine-tokens/"+other.Info.Handle+"/revoke", `{}`, true); w.Code != 200 {
			t.Fatal(w.Code)
		}
		if _, err := e.s.withPlatformWritableBackendAccess(context.Background(), platformActorSession{anonymousMachine: otherRow.ID}, e.id, nil); err == nil {
			t.Fatal("access was granted to a revoked token")
		}
		// And the session-less access is not one a viewer or public identity can borrow.
		if _, err := e.s.withPlatformWritableBackendAccess(context.Background(), platformActorSession{anonymousPublic: true}, e.id, nil); err == nil {
			t.Fatal("a public identity was admitted with no public grant")
		}
	})
}

func TestMachineOpenAPIDocument(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.create(t, "Dify", []string{"bars", "margin"}, 600, []string{}, 90)
		if w := e.get("/machine/apps/"+e.id+"/openapi.json", "", nil); w.Code != 401 {
			t.Fatal("openapi without a token", w.Code)
		}
		w := e.get("/machine/apps/"+e.id+"/openapi.json", "", bearer(made.Token))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(w.Code, w.Header())
		}
		var doc struct {
			OpenAPI string `json:"openapi"`
			Info    struct{ Title, Version string }
			Servers []struct{ URL string } `json:"servers"`
			Paths   map[string]map[string]struct {
				OperationID string `json:"operationId"`
				Description string `json:"description"`
				Parameters  []struct {
					Name   string            `json:"name"`
					In     string            `json:"in"`
					Schema map[string]any    `json:"schema"`
					Req    bool              `json:"required"`
					X      map[string]string `json:"-"`
				} `json:"parameters"`
				Responses map[string]any `json:"responses"`
				Security  []map[string][]string
			} `json:"paths"`
			Components struct {
				SecuritySchemes map[string]map[string]string `json:"securitySchemes"`
			} `json:"components"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.OpenAPI != "3.0.3" || len(doc.Servers) != 1 || !strings.HasPrefix(doc.Servers[0].URL, "http") || doc.Components.SecuritySchemes["bearerAuth"]["scheme"] != "bearer" || doc.Components.SecuritySchemes["bearerAuth"]["type"] != "http" {
			t.Fatalf("document: %s", w.Body.String())
		}
		// One GET per route the token may call (not "extra", which it lacks), the declared query names
		// as optional string parameters, and nothing else.
		if len(doc.Paths) != 2 {
			t.Fatal("paths", len(doc.Paths))
		}
		bars := doc.Paths["/machine/apps/"+e.id+"/data/bars"]["get"]
		if len(doc.Paths["/machine/apps/"+e.id+"/data/bars"]) != 1 || bars.OperationID != "bars" || bars.Description != "Daily bars" || len(bars.Parameters) != 4 || bars.Responses["200"] == nil || bars.Responses["401"] == nil {
			t.Fatalf("bars: %+v", bars)
		}
		for i, name := range []string{"from", "limit", "symbol", "to"} {
			p := bars.Parameters[i]
			if p.Name != name || p.In != "query" || p.Schema["type"] != "string" || p.Req {
				t.Fatalf("parameter %d: %+v", i, p)
			}
		}
		if len(doc.Paths["/machine/apps/"+e.id+"/data/margin"]["get"].Parameters) != 1 {
			t.Fatal("margin parameters")
		}
		if strings.Contains(w.Body.String(), made.Token[17:]) || strings.Contains(w.Body.String(), "/v1/machine/") {
			t.Fatal("the document leaks the secret or the backend path")
		}
		if e.calls.Load() != 0 {
			t.Fatal("the document cost a backend call")
		}
		// A route dropped from the policy disappears from the document.
		if w := e.enableWith(t, []string{"bars"}, 50, 16); w.Code != 200 {
			t.Fatal(w.Code)
		}
		again := e.get("/machine/apps/"+e.id+"/openapi.json", "", bearer(made.Token))
		if strings.Contains(again.Body.String(), "/data/margin") || !strings.Contains(again.Body.String(), "/data/bars") {
			t.Fatal("policy shrink not reflected", again.Body.String())
		}
	})
}

// AUTH-05 stays as it was and gains refusals for the new audience (doc 183 section 9, item 3): a
// machine token is no viewer, no extension credential and no api token, and the reverse.
func TestMachineTokenIsRefusedWhereViewersAreServed(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		e := platformMachineFixture(t, pg)
		e.enable(t)
		made := e.simple(t)
		for _, path := range []string{
			"/api/app-platform/installations/" + e.id + "/status",
			"/api/app-platform/installations/" + e.id + "/launch",
			"/api/v1/extensions/some-app/principals",
			"/api/v1/reports",
			"/api/admin/app-platform/installations/" + e.id + "/machine-tokens",
		} {
			w := e.get(path, "", bearer(made.Token))
			if w.Code != 401 && w.Code != 403 {
				t.Fatal("a machine token was served at", path, w.Code)
			}
		}
		for _, path := range []string{"/api/app-platform/installations/" + e.id + "/backend", "/api/v1/extensions/some-app/request"} {
			r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+made.Token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, r)
			if w.Code != 401 && w.Code != 403 {
				t.Fatal("a machine token was served at", path, w.Code)
			}
		}
		if e.calls.Load() != 0 {
			t.Fatal("a refused request reached the backend")
		}
		// Management of tokens is the administrator's alone: not even a valid machine token manages one.
		r := httptest.NewRequest("POST", "/api/admin/app-platform/installations/"+e.id+"/machine-tokens/"+made.Info.Handle+"/revoke", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+made.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		e.mux.ServeHTTP(w, r)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("a machine token revoked itself", w.Code)
		}
		if w := e.data(made.Token, "bars", "", ""); w.Code != 200 {
			t.Fatal("the token no longer works", w.Code)
		}
		// A non-administrator session is refused by the management routes.
		mustExec(t, e.s.st, `UPDATE users SET role='user' WHERE username=?`, e.actor.username)
		if w := e.admin(t, "POST", "machine-tokens", `{"label":"x","routes":["bars"],"perMinute":60,"sourceCidrs":[],"expiresInDays":30}`, true); w.Code != 401 && w.Code != 403 {
			t.Fatal("a non-administrator made a token", w.Code)
		}
	})
}

// The machine class is its own registration: never a session route, never the cookie-admitting
// bearer class, and only these patterns.
func TestMachineRoutesAreTheirOwnAuthClass(t *testing.T) {
	t.Parallel()
	s := &Server{}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	var machine []string
	for _, row := range s.routeTable {
		if strings.Contains(row.pattern, "/machine/") {
			if row.class != authMachine {
				t.Errorf("%s is registered as class %v, want authMachine", row.pattern, row.class)
			}
			machine = append(machine, row.pattern)
		}
		if row.class == authMachine && !strings.Contains(row.pattern, " /machine/") && !strings.HasPrefix(row.pattern, "/machine/") {
			t.Errorf("%s is authMachine outside /machine/", row.pattern)
		}
	}
	if len(machine) != 3 {
		t.Fatal(machine)
	}
	for _, row := range s.sessionRoutes() {
		if row.class == authMachine {
			t.Fatal("a machine route is a session route")
		}
	}
}
