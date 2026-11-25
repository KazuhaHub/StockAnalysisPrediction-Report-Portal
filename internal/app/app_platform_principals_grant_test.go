package app

import (
	"context"
	"errors"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// grantRoute posts to the administrator-only enable and withdraw route of doc 181 section 4.
func (p *platformPrincipalEnv) grantRoute(t *testing.T, cookie, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/admin/app-platform/installations/"+p.installation+"/principals-read", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		r.Header.Set(k, v)
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	p.mux.ServeHTTP(w, r)
	return w
}

func (p *platformPrincipalEnv) policyRevision(t *testing.T) string {
	t.Helper()
	return scalar[string](t, p.s.st, `SELECT CAST(policy_revision AS TEXT) FROM app_platform_installations WHERE id=?`, p.installation)
}

func (p *platformPrincipalEnv) granted(t *testing.T) int {
	t.Helper()
	return scalar[int](t, p.s.st, `SELECT granted FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read'`, p.installation)
}

// Doc 181 section 4: the grant is off after install, an administrator enables it, and withdrawal
// takes effect on the very next directory call.
func TestPlatformPrincipalsGrantEnableAndWithdrawAreImmediateAndAudited(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		mustExec(t, p.s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='principals.read'`, p.installation)
		if w := p.post(t, "search", map[string]any{"kind": "ou"}); w.Code != http.StatusForbidden {
			t.Fatal("the directory answered before the grant was enabled", w.Code)
		}
		revision := p.policyRevision(t)
		body := func(enabled string) string { return `{"enabled":` + enabled + `,"policyRevision":"` + revision + `"}` }
		if w := p.grantRoute(t, p.cookie, body("true"), nil); w.Code != 200 {
			t.Fatal("enable", w.Code, w.Body.String())
		}
		if p.granted(t) != 1 {
			t.Fatal("enable did not set the grant")
		}
		if w := p.post(t, "search", map[string]any{"kind": "ou"}); w.Code != 200 {
			t.Fatal("the next directory call after enabling", w.Code, w.Body.String())
		}
		// Repeating an enable is not another grant event.
		if w := p.grantRoute(t, p.cookie, body("true"), nil); w.Code != 200 {
			t.Fatal("repeat enable", w.Code)
		}
		if w := p.grantRoute(t, p.cookie, body("false"), nil); w.Code != 200 {
			t.Fatal("withdraw", w.Code, w.Body.String())
		}
		if p.granted(t) != 0 {
			t.Fatal("withdraw did not clear the grant")
		}
		if w := p.post(t, "search", map[string]any{"kind": "ou"}); w.Code != http.StatusForbidden {
			t.Fatal("the next directory call after withdrawal was answered", w.Code)
		}
		for action, want := range map[string]int{"app_platform.capability_granted": 1, "app_platform.capability_withdrawn": 1} {
			rows, total := p.s.st.ListAudit(AuditFilter{Action: action})
			if total != want || len(rows) != want || rows[0].Actor != p.admin.username || rows[0].TargetID != p.installation || !strings.Contains(rows[0].Detail, `"principals.read"`) {
				t.Fatal("audit", action, total, rows)
			}
		}
	})
}

func TestPlatformPrincipalsGrantRefusesEveryOtherCaller(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		p := platformPrincipalEnvironment(t, pg)
		mustExec(t, p.s.st, `UPDATE app_platform_capability_grants SET granted=0 WHERE installation_id=? AND capability='principals.read'`, p.installation)
		revision := p.policyRevision(t)
		good := `{"enabled":true,"policyRevision":"` + revision + `"}`
		alice := platformReportSession(t, p.s, "alice")
		aliceCookie := p.s.managedCookie(*p.s.st.GetUser("alice"), alice.sessionID, alice.expires.Unix())
		for name, tc := range map[string]struct {
			cookie, body string
			header       map[string]string
			code         int
		}{
			"anonymous":         {"", good, nil, http.StatusUnauthorized},
			"non administrator": {aliceCookie, good, nil, http.StatusForbidden},
			"cross origin":      {p.cookie, good, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, http.StatusForbidden},
			"stale revision":    {p.cookie, `{"enabled":true,"policyRevision":"99"}`, nil, http.StatusConflict},
			"unknown field":     {p.cookie, `{"enabled":true,"policyRevision":"` + revision + `","x":1}`, nil, http.StatusBadRequest},
			"missing enabled":   {p.cookie, `{"policyRevision":"` + revision + `"}`, nil, http.StatusBadRequest},
			"string enabled":    {p.cookie, `{"enabled":"true","policyRevision":"` + revision + `"}`, nil, http.StatusBadRequest},
			"bad revision":      {p.cookie, `{"enabled":true,"policyRevision":"0"}`, nil, http.StatusBadRequest},
		} {
			if w := p.grantRoute(t, tc.cookie, tc.body, tc.header); w.Code != tc.code {
				t.Fatal(name, w.Code, w.Body.String())
			}
			if p.granted(t) != 0 {
				t.Fatal(name, "changed the grant")
			}
		}
		// An installation that does not declare the capability has no row to enable.
		mustExec(t, p.s.st, `DELETE FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read'`, p.installation)
		if w := p.grantRoute(t, p.cookie, good, nil); w.Code != http.StatusConflict {
			t.Fatal("undeclared capability", w.Code, w.Body.String())
		}
		if _, total := p.s.st.ListAudit(AuditFilter{Action: "app_platform.capability_granted"}); total != 0 {
			t.Fatal("a refused enable was audited as a grant")
		}
	})
}

// Grant rows are evidence for installs, enables and updates (the install resource count, the
// frontend activation checks and the grants digest), so the grant may only change while the
// installation is ready and nothing is unfinished.
func TestPlatformPrincipalsGrantIsRefusedMidInstallAndTheInstallStillCompletes(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		platformManagedFixtureOptional.set(t, []string{"principals.read"})
		s, actor, lease := platformActivationFixture(t, pg, true)
		for _, enabled := range []bool{true, false} {
			if err := s.setPlatformPrincipalsGrant(context.Background(), actor, lease.installationID, enabled, "1"); !errors.Is(err, errPlatformPrincipalsGrantBusy) {
				t.Fatal("grant change during an unrun install was not refused", enabled, err)
			}
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND granted=0`, lease.installationID) != 1 {
			t.Fatal("a refused change touched the grant")
		}
		result, err := s.runPlatformStaticInstall(context.Background(), lease, platformRuntimeEndpoint{})
		if err != nil || result.activation.snapshot.Stage != appplatform.StageComplete {
			t.Fatal("the install no longer completes", err)
		}
		if err := s.setPlatformPrincipalsGrant(context.Background(), actor, lease.installationID, true, "1"); err != nil {
			t.Fatal("a ready installation refused the grant", err)
		}
	})
}

func TestPlatformPrincipalsGrantIsRefusedWhileAnOperationIsUnfinishedOrNotReady(t *testing.T) {
	t.Parallel()
	for name, arrange := range map[string]func(t *testing.T, p *platformPrincipalEnv){
		"install in flight": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='activate',completed_ms=NULL WHERE installation_id=?`, p.installation)
		},
		"enable in flight": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET action='enable',stage='activate',completed_ms=NULL WHERE installation_id=?`, p.installation)
		},
		"update in flight": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET action='update',stage='activate',completed_ms=NULL WHERE installation_id=?`, p.installation)
		},
		"recovery required": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='recovery_required' WHERE installation_id=?`, p.installation)
		},
		"disabled": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, p.installation)
		},
		"not ready yet": func(t *testing.T, p *platformPrincipalEnv) {
			mustExec(t, p.s.st, `UPDATE app_platform_installations SET phase='pending' WHERE id=?`, p.installation)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := platformPrincipalEnvironment(t, false)
			arrange(t, p)
			before := p.granted(t)
			revision := p.policyRevision(t)
			for _, enabled := range []string{"true", "false"} {
				w := p.grantRoute(t, p.cookie, `{"enabled":`+enabled+`,"policyRevision":"`+revision+`"}`, nil)
				if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "app_platform_installation_not_ready") {
					t.Fatal(enabled, w.Code, w.Body.String())
				}
			}
			if p.granted(t) != before {
				t.Fatal("a refused change touched the grant")
			}
		})
	}
}

func TestPlatformInstallationSettingsReportAPendingOperation(t *testing.T) {
	t.Parallel()
	p := platformPrincipalEnvironment(t, false)
	read := func() bool {
		settings, err := p.s.platformInstallationSettings(p.admin, p.installation)
		if err != nil {
			t.Fatal(err)
		}
		return settings.OperationPending
	}
	if read() {
		t.Fatal("a settled installation reported a pending operation")
	}
	mustExec(t, p.s.st, `UPDATE app_platform_operations SET stage='activate',completed_ms=NULL WHERE installation_id=?`, p.installation)
	if !read() {
		t.Fatal("an unfinished operation was not reported")
	}
}
