package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformProxyHTTP struct {
	s            *Server
	mux          *http.ServeMux
	installation string
	cookie       string
}

func platformProxyHTTPEnv(t *testing.T, pg bool) *platformProxyHTTP {
	t.Helper()
	s, actor, installation := platformProxyFixture(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	return &platformProxyHTTP{s: s, mux: mux, installation: installation, cookie: cookie}
}

func (p *platformProxyHTTP) do(method, path, body, contentType string, header map[string]string) *httptest.ResponseRecorder {
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

func (p *platformProxyHTTP) root() string {
	return "/api/admin/app-platform/installations/" + p.installation + "/network-proxy"
}

type platformProxyViewBody struct {
	PolicyRevision       string   `json:"policy_revision"`
	Granted              bool     `json:"granted"`
	Ready                bool     `json:"ready"`
	DeclaredHosts        []string `json:"declared_hosts"`
	MaxDailyBytes        int64    `json:"max_daily_bytes"`
	PreviousPolicySHA256 string   `json:"previous_policy_sha256"`
	Policy               *struct {
		Hosts      []string `json:"hosts"`
		DailyBytes int64    `json:"daily_bytes"`
	} `json:"policy"`
	Today struct {
		Day      string `json:"day"`
		Bytes    int64  `json:"bytes"`
		Budget   int64  `json:"budget"`
		Reported bool   `json:"reported"`
	} `json:"today"`
	Rollups []struct {
		Hour     string           `json:"hour"`
		Host     string           `json:"host"`
		Tunnels  int64            `json:"tunnels"`
		BytesIn  int64            `json:"bytes_in"`
		BytesOut int64            `json:"bytes_out"`
		Closes   map[string]int64 `json:"closes"`
	} `json:"rollups"`
}

func (p *platformProxyHTTP) view(t *testing.T) platformProxyViewBody {
	t.Helper()
	w := p.do("GET", p.root(), "", "", nil)
	if w.Code != 200 {
		t.Fatal("view", w.Code, w.Body.String())
	}
	var v platformProxyViewBody
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func testPlatformProxyHTTP(t *testing.T, pg bool) {
	p := platformProxyHTTPEnv(t, pg)
	plans := func() int {
		return scalar[int](t, p.s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE plan_json LIKE '%proxy-policy-review%'`)
	}
	v := p.view(t)
	if v.Granted || !v.Ready || v.Policy != nil || strings.Join(v.DeclaredHosts, ",") != "*.eastmoney.com,qt.gtimg.cn" || v.MaxDailyBytes != appplatform.ProxyDailyBytesMax ||
		v.Today.Reported || v.Today.Budget != appplatform.ProxyDailyBytesMax || len(v.Rollups) != 0 || len(v.PreviousPolicySHA256) != 64 {
		t.Fatalf("view before: %+v", v)
	}
	planPath := p.root() + "/plan"
	body := func(extra string) string {
		return `{"policyRevision":"` + v.PolicyRevision + `","previousPolicySha256":"` + v.PreviousPolicySHA256 + `","hosts":["*.eastmoney.com","qt.gtimg.cn"],"dailyBytes":1073741824` + extra + `}`
	}
	for _, tc := range []struct {
		name, body, contentType string
		header                  map[string]string
		want                    int
	}{
		{"cross-origin", body(""), "application/json", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"wrong content type", body(""), "text/plain", nil, 415},
		{"unknown key", body(`,"x":1`), "application/json", nil, 400},
		{"duplicate key", strings.Replace(body(""), `"hosts":[`, `"hosts":[],"hosts":[`, 1), "application/json", nil, 400},
		{"numeric revision", strings.Replace(body(""), `"policyRevision":"`+v.PolicyRevision+`"`, `"policyRevision":`+v.PolicyRevision, 1), "application/json", nil, 400},
		{"null hosts", strings.Replace(body(""), `"hosts":["*.eastmoney.com","qt.gtimg.cn"]`, `"hosts":null`, 1), "application/json", nil, 400},
		{"public suffix", strings.Replace(body(""), `"*.eastmoney.com"`, `"*.com"`, 1), "application/json", nil, 400},
		{"budget above the platform's", strings.Replace(body(""), `1073741824`, `2147483649`, 1), "application/json", nil, 400},
		{"string budget", strings.Replace(body(""), `1073741824`, `"1073741824"`, 1), "application/json", nil, 400},
		{"missing budget", strings.Replace(body(""), `,"dailyBytes":1073741824`, ``, 1), "application/json", nil, 400},
	} {
		if w := p.do("POST", planPath, tc.body, tc.contentType, tc.header); w.Code != tc.want {
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
	if w := p.do("POST", planPath, body(""), "application/json", nil); w.Code != 403 {
		t.Fatal("non-admin plan", w.Code, w.Body.String())
	}
	if w := p.do("GET", p.root(), "", "", nil); w.Code != 403 {
		t.Fatal("non-admin view", w.Code)
	}
	mustExec(t, p.s.st, `UPDATE users SET role='admin' WHERE username=?`, actorName)
	// The plan, then its consume.
	w := p.do("POST", planPath, body(""), "application/json", nil)
	if w.Code != 200 {
		t.Fatal("plan", w.Code, w.Body.String())
	}
	var plan struct {
		PlanID     string `json:"plan_id"`
		PlanSHA256 string `json:"plan_sha256"`
		ExpiresMS  string `json:"expires_ms"`
		Diff       struct {
			Added []struct {
				Entry    string `json:"entry"`
				Wildcard bool   `json:"wildcard"`
			} `json:"added"`
			Removed    []any `json:"removed"`
			DailyBytes struct {
				Old int64 `json:"old"`
				New int64 `json:"new"`
			} `json:"daily_bytes"`
		} `json:"diff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || plan.PlanID == "" || len(plan.Diff.Added) != 2 || !plan.Diff.Added[0].Wildcard || plan.Diff.Added[1].Wildcard ||
		plan.Diff.Added[0].Entry != "*.eastmoney.com" || len(plan.Diff.Removed) != 0 || plan.Diff.DailyBytes.Old != appplatform.ProxyDailyBytesMax || plan.Diff.DailyBytes.New != 1<<30 {
		t.Fatal("plan response", w.Body.String(), err)
	}
	consumePath := "/api/admin/app-platform/network-proxy-reviews/" + plan.PlanID
	if w := p.do("POST", consumePath, `{"sha256":"`+strings.Repeat("a", 64)+`"}`, "application/json", nil); w.Code != 409 {
		t.Fatal("consume with another digest", w.Code, w.Body.String())
	}
	if w := p.do("POST", consumePath, `{"sha256":"`+plan.PlanSHA256+`","x":1}`, "application/json", nil); w.Code != 400 {
		t.Fatal("consume with an unknown key", w.Code)
	}
	if w := p.do("POST", consumePath, `{"sha256":"`+plan.PlanSHA256+`"}`, "application/json", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal("consume", w.Code, w.Body.String())
	}
	after := p.view(t)
	if !after.Granted || after.Policy == nil || strings.Join(after.Policy.Hosts, ",") != "*.eastmoney.com,qt.gtimg.cn" || after.Policy.DailyBytes != 1<<30 || after.Today.Budget != 1<<30 || after.PolicyRevision == v.PolicyRevision {
		t.Fatalf("view after: %+v", after)
	}
	// Reports in the audit log show up in the view: usage for today, rollups merged per hour and host.
	now := time.Now().UTC()
	hour := now.Add(-2 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)
	old := now.Add(-30 * time.Hour).Truncate(time.Hour).Format(time.RFC3339)
	row := func(action string, detail map[string]any) {
		raw, _ := json.Marshal(detail)
		mustExec(t, p.s.st, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_installation',?,?,'')`, now.Format(time.RFC3339), action, p.installation, string(raw))
	}
	row(platformProxyAuditRollup, map[string]any{"hour": hour, "host": "qt.gtimg.cn", "tunnels": 2, "bytes_in": 100, "bytes_out": 10, "closes": map[string]int{"eof": 2}})
	row(platformProxyAuditRollup, map[string]any{"hour": hour, "host": "qt.gtimg.cn", "tunnels": 1, "bytes_in": 5, "bytes_out": 1, "closes": map[string]int{"idle": 1}})
	row(platformProxyAuditRollup, map[string]any{"hour": old, "host": "qt.gtimg.cn", "tunnels": 9, "bytes_in": 9, "bytes_out": 9, "closes": map[string]int{"eof": 9}})
	row(platformProxyAuditRollup, map[string]any{"hour": hour, "host": "push2.eastmoney.com", "tunnels": 4, "bytes_in": 1, "bytes_out": 1, "closes": map[string]int{}})
	row(platformProxyAuditUsage, map[string]any{"day": "2000-01-01", "bytes": 1, "budget": 1 << 30})
	withOld := p.view(t)
	if len(withOld.Rollups) != 2 {
		t.Fatalf("rollups: %+v", withOld.Rollups)
	}
	merged := withOld.Rollups[1]
	if merged.Host != "qt.gtimg.cn" || merged.Tunnels != 3 || merged.BytesIn != 105 || merged.BytesOut != 11 || merged.Closes["eof"] != 2 || merged.Closes["idle"] != 1 {
		t.Fatalf("merged rollup: %+v", merged)
	}
	if withOld.Rollups[0].Host != "push2.eastmoney.com" {
		t.Fatalf("rollups are not ordered by hour and host: %+v", withOld.Rollups)
	}
	row(platformProxyAuditUsage, map[string]any{"day": now.Format("2006-01-02"), "bytes": 4242, "budget": 1 << 30})
	today := p.view(t)
	if !today.Today.Reported || today.Today.Bytes != 4242 || today.Today.Day != now.Format("2006-01-02") {
		t.Fatalf("today: %+v", today.Today)
	}
	// Withdrawing is the same two calls with no hosts.
	w = p.do("POST", planPath, `{"policyRevision":"`+today.PolicyRevision+`","previousPolicySha256":"`+today.PreviousPolicySHA256+`","hosts":[],"dailyBytes":0}`, "application/json", nil)
	if w.Code != 200 {
		t.Fatal("withdraw plan", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil || len(plan.Diff.Removed) != 2 {
		t.Fatal("withdraw diff", w.Body.String())
	}
	if w := p.do("POST", "/api/admin/app-platform/network-proxy-reviews/"+plan.PlanID, `{"sha256":"`+plan.PlanSHA256+`"}`, "application/json", nil); w.Code != 200 {
		t.Fatal("withdraw consume", w.Code, w.Body.String())
	}
	if gone := p.view(t); gone.Granted || gone.Policy != nil {
		t.Fatalf("view after withdrawal: %+v", gone)
	}
}

func TestPlatformProxyHTTP(t *testing.T)         { t.Parallel(); testPlatformProxyHTTP(t, false) }
func TestPlatformProxyHTTPPostgres(t *testing.T) { t.Parallel(); testPlatformProxyHTTP(t, true) }

// The console shows a grant only when the session loader would treat it as one: a row that no consumed review
// backs is shown as not granted.
func TestPlatformProxyViewGrantedMatchesTheSessionLoader(t *testing.T) {
	t.Parallel()
	p := platformProxyHTTPEnv(t, false)
	platformProxySeedGrant(t, p.s, p.installation, []string{"qt.gtimg.cn"}, appplatform.ProxyDailyBytesMax)
	if v := p.view(t); !v.Granted || v.Policy == nil {
		t.Fatalf("a reviewed grant: %+v", v)
	}
	other, _ := appplatform.CanonicalProxyPolicy(appplatform.ProxyPolicy{Hosts: []string{"other.example.com"}, DailyBytes: appplatform.ProxyDailyBytesMax})
	mustExec(t, p.s.st, `UPDATE app_platform_capability_grants SET policy_json=? WHERE installation_id=? AND capability='network.proxy'`, string(other), p.installation)
	if v := p.view(t); v.Granted || v.Policy != nil {
		t.Fatalf("a row no review backs is shown as granted: %+v", v)
	}
}

func TestPlatformProxyOldRunnerIs409OverHTTP(t *testing.T) {
	t.Parallel()
	p := platformProxyHTTPEnv(t, false)
	proxyRunnerOf(p.s).fail(transport.ErrProxyUnsupported)
	v := p.view(t)
	body := `{"policyRevision":"` + v.PolicyRevision + `","previousPolicySha256":"` + v.PreviousPolicySHA256 + `","hosts":["qt.gtimg.cn"],"dailyBytes":1073741824}`
	w := p.do("POST", p.root()+"/plan", body, "application/json", nil)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "app_platform_runner_unsupported") {
		t.Fatal("old runner", w.Code, w.Body.String())
	}
}
