package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A backend app launches through the writable/managed read path (withPlatformWritableLaunchReadContext),
// not the static one. The directory must answer through it the same way: off until an administrator
// enables the optional grant, the same principals the viewer later presents, and immediate withdrawal.
func TestPlatformPrincipalDirectoryServesABackendApp(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		platformManagedFixtureOptional = []string{"principals.read"}
		t.Cleanup(func() { platformManagedFixtureOptional = nil })
		s, actor, lease := platformWritableAccessFixture(t, pg)
		mustExec(t, s.st, `UPDATE users SET role='admin' WHERE username=?`, actor.username)
		t.Cleanup(func() {
			mustExec(t, s.st, `DELETE FROM audit_log WHERE action LIKE 'app_platform.%principal%' OR action LIKE 'app_platform.capability_%'`)
		})
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE id=? AND runtime_id IS NOT NULL`, lease.installationID) != 1 {
			t.Fatal("the fixture is not a backend installation")
		}
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND granted=0`, lease.installationID) != 1 {
			t.Fatal("install must leave the optional grant off")
		}
		cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
		mux := http.NewServeMux()
		s.wireRoutes(mux)
		search := func() *httptest.ResponseRecorder {
			binding, err := s.inspectPlatformLaunchAccess(actor, lease.installationID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"kind": "user", "binding": map[string]string{"generation": binding.generation, "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}})
			return sessionTestRequest(mux, "POST", "/api/app-platform/installations/"+lease.installationID+"/principals/search", cookie, string(raw))
		}
		grant := func(enabled string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("POST", "/api/admin/app-platform/installations/"+lease.installationID+"/principals-read", strings.NewReader(`{"enabled":`+enabled+`,"policyRevision":"1"}`))
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			return w
		}
		if w := search(); w.Code != http.StatusForbidden {
			t.Fatal("answered before an administrator enabled the grant", w.Code, w.Body.String())
		}
		if w := grant("true"); w.Code != 200 {
			t.Fatal("enable", w.Code, w.Body.String())
		}
		w := search()
		items, _ := decodePlatformPrincipals(t, w)
		viewer, err := s.readPlatformLaunchViewerContext(context.Background(), actor, lease.installationID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range items {
			found = found || item.Principal == "user:"+viewer.Subject && item.Label == actor.username
		}
		if !found {
			t.Fatal("the backend app's directory does not list the viewer's own principal", w.Body.String())
		}
		if w := grant("false"); w.Code != 200 {
			t.Fatal("withdraw", w.Code, w.Body.String())
		}
		if w := search(); w.Code != http.StatusForbidden {
			t.Fatal("answered after withdrawal", w.Code, w.Body.String())
		}
	})
}
