package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type platformCapabilityHTTP struct {
	s            *Server
	mux          *http.ServeMux
	installation string
	cookie       string
	runner       *platformCapabilityRunner
}

func platformCapabilityHTTPEnv(t *testing.T, pg bool) *platformCapabilityHTTP {
	t.Helper()
	s, actor, installation := platformCapabilityFixture(t, pg)
	runner := platformCapabilityRunnerFor(t, s, installation, nil)
	s.platformBackendEndpoint.Store(&platformRuntimeEndpoint{path: "/run/portal-apps/runner.sock", controllerUID: 41001, controllerGID: 41001})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	return &platformCapabilityHTTP{s: s, mux: mux, installation: installation, cookie: cookie, runner: runner}
}

func (p *platformCapabilityHTTP) do(method, path, body, contentType string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	r.AddCookie(&http.Cookie{Name: cookieName, Value: p.cookie})
	w := httptest.NewRecorder()
	p.mux.ServeHTTP(w, r)
	return w
}

func (p *platformCapabilityHTTP) planBody(t *testing.T, extra string) string {
	t.Helper()
	req := platformCapabilityRequestFor(t, p.s, p.installation, nil, nil)
	return `{"capability":"network.http","policyRevision":"` + itoa64(req.PolicyRevision) + `","previousPolicySha256":"` + req.PreviousPolicySHA256 + `","add":[{"id":"extra","origin":"https://other.example.com","paths":["/v2"],"methods":["GET"],"network":"public"}],"remove":[]` + extra + `}`
}

func (p *platformCapabilityHTTP) planPath() string {
	return "/api/admin/app-platform/installations/" + p.installation + "/capability-policy/plan"
}

func testPlatformCapabilityPolicyHTTP(t *testing.T, pg bool) {
	p := platformCapabilityHTTPEnv(t, pg)
	plans := func() int {
		return scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE plan_json LIKE '%capability-policy-review%'`)
	}
	// Settings before: the v1 policy is read as one public target.
	settings := p.do("GET", "/api/admin/app-platform/installations/"+p.installation+"/settings", "", "", nil)
	if settings.Code != 200 {
		t.Fatal("settings", settings.Code, settings.Body.String())
	}
	var before struct {
		NetworkPolicy struct {
			Format  string `json:"format"`
			Targets []struct {
				ID, Origin string
			} `json:"targets"`
		} `json:"networkPolicy"`
	}
	if err := json.Unmarshal(settings.Body.Bytes(), &before); err != nil || before.NetworkPolicy.Format != "portal.http-policy/1-dev.1" || len(before.NetworkPolicy.Targets) != 1 || before.NetworkPolicy.Targets[0].ID != "default" {
		t.Fatal("settings networkPolicy before", settings.Body.String(), err)
	}

	// Refusals write no plan.
	refusals := []struct {
		name, body, contentType string
		header                  map[string]string
		want                    int
	}{
		{"cross-origin", p.planBody(t, ""), "application/json", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"wrong content type", p.planBody(t, ""), "text/plain", nil, 415},
		{"unknown key", p.planBody(t, `,"extra":1`), "application/json", nil, 400},
		{"duplicate key", strings.Replace(p.planBody(t, ""), `"remove":[]`, `"remove":[],"remove":[]`, 1), "application/json", nil, 400},
		{"trailing value", p.planBody(t, "") + `{}`, "application/json", nil, 400},
		{"other capability", strings.Replace(p.planBody(t, ""), `"network.http"`, `"network.stream"`, 1), "application/json", nil, 400},
		{"numeric revision", strings.Replace(p.planBody(t, ""), `"policyRevision":"1"`, `"policyRevision":1`, 1), "application/json", nil, 400},
		{"missing remove", strings.Replace(p.planBody(t, ""), `,"remove":[]`, ``, 1), "application/json", nil, 400},
		{"unknown target key", strings.Replace(p.planBody(t, ""), `"network":"public"`, `"network":"public","port":1`, 1), "application/json", nil, 400},
		{"null add", strings.Replace(p.planBody(t, ""), `"add":[{"id":"extra","origin":"https://other.example.com","paths":["/v2"],"methods":["GET"],"network":"public"}]`, `"add":null`, 1), "application/json", nil, 400},
		{"bad bounds key", p.planBody(t, `,"bounds":{"retries":3}`), "application/json", nil, 400},
		{"empty bounds", p.planBody(t, `,"bounds":{}`), "application/json", nil, 400},
	}
	for _, tc := range refusals {
		w := p.do("POST", p.planPath(), tc.body, tc.contentType, tc.header)
		if w.Code != tc.want {
			t.Fatal(tc.name, "status", w.Code, w.Body.String())
		}
	}
	if plans() != 0 {
		t.Fatal("a refused request wrote a plan")
	}

	// A non-administrator is refused with no plan written.
	var actorName string
	if err := p.s.st.queryRow(`SELECT username FROM user_sessions ORDER BY id LIMIT 1`).Scan(&actorName); err != nil {
		t.Fatal(err)
	}
	mustExec(t, p.s.st, `UPDATE users SET role='user' WHERE username=?`, actorName)
	if w := p.do("POST", p.planPath(), p.planBody(t, ""), "application/json", nil); w.Code != 403 {
		t.Fatal("non-admin plan", w.Code, w.Body.String())
	}
	if plans() != 0 {
		t.Fatal("a non-admin request wrote a plan")
	}
	mustExec(t, p.s.st, `UPDATE users SET role='admin' WHERE username=?`, actorName)

	// The plan, then the consume.
	w := p.do("POST", p.planPath(), p.planBody(t, ""), "application/json", nil)
	if w.Code != 200 {
		t.Fatal("plan", w.Code, w.Body.String())
	}
	var plan struct {
		ID     string `json:"plan_id"`
		SHA256 string `json:"plan_sha256"`
		Diff   struct {
			Added []struct {
				ID        string `json:"id"`
				Network   string `json:"network"`
				PlainHTTP bool   `json:"plain_http"`
			} `json:"added"`
			Removed []any    `json:"removed"`
			Paused  []string `json:"paused_bindings"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || len(plan.Diff.Added) != 1 || plan.Diff.Added[0].ID != "extra" || plan.Diff.Added[0].Network != "public" || plan.Diff.Removed == nil || plan.Diff.Paused == nil {
		t.Fatal("plan diff", w.Body.String(), err)
	}
	consumePath := "/api/admin/app-platform/capability-policy-reviews/" + plan.ID
	for _, tc := range []struct {
		name, body, contentType string
		header                  map[string]string
		want                    int
	}{
		{"cross-origin consume", `{"sha256":"` + plan.SHA256 + `"}`, "application/json", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"extra consume key", `{"sha256":"` + plan.SHA256 + `","force":true}`, "application/json", nil, 400},
		{"missing sha", `{}`, "application/json", nil, 400},
		{"short sha", `{"sha256":"abc"}`, "application/json", nil, 400},
		{"wrong sha", `{"sha256":"` + strings.Repeat("a", 64) + `"}`, "application/json", nil, 409},
	} {
		if w := p.do("POST", consumePath, tc.body, tc.contentType, tc.header); w.Code != tc.want {
			t.Fatal(tc.name, "status", w.Code, w.Body.String())
		}
	}
	if scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE id=? AND consumed_ms IS NULL`, plan.ID) != 1 {
		t.Fatal("a refused consume consumed the plan")
	}
	if w := p.do("POST", consumePath, `{"sha256":"`+plan.SHA256+`"}`, "application/json", nil); w.Code != 200 {
		t.Fatal("consume", w.Code, w.Body.String())
	}
	if w := p.do("POST", consumePath, `{"sha256":"`+plan.SHA256+`"}`, "application/json", nil); w.Code != 409 {
		t.Fatal("replay", w.Code)
	}

	// Settings after: the v2 policy with both targets.
	settings = p.do("GET", "/api/admin/app-platform/installations/"+p.installation+"/settings", "", "", nil)
	var after struct {
		NetworkPolicy struct {
			Format  string `json:"format"`
			Targets []struct {
				ID      string `json:"id"`
				Origin  string `json:"origin"`
				Network string `json:"network"`
			} `json:"targets"`
			PolicySHA256 string `json:"policy_sha256"`
		} `json:"networkPolicy"`
	}
	if err := json.Unmarshal(settings.Body.Bytes(), &after); err != nil || after.NetworkPolicy.Format != "portal.http-policy/2-dev.1" || len(after.NetworkPolicy.Targets) != 2 ||
		after.NetworkPolicy.PolicySHA256 != platformDigest([]byte(scalar[string](t, p.s.st, `SELECT policy_json FROM app_platform_capability_grants WHERE installation_id=? AND capability='network.http'`, p.installation))) {
		t.Fatal("settings networkPolicy after", settings.Body.String(), err)
	}
	if bytes.Contains(settings.Body.Bytes(), []byte(`"policy_json"`)) {
		t.Fatal("settings leaked a raw policy")
	}
}

func TestPlatformCapabilityPolicyHTTPSQLite(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityPolicyHTTP(t, false)
}
func TestPlatformCapabilityPolicyHTTPPostgres(t *testing.T) {
	t.Parallel()
	testPlatformCapabilityPolicyHTTP(t, true)
}
