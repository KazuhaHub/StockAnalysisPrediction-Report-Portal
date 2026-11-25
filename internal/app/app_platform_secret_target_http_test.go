package app

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The review shows the target origin it binds, and the optional target field
// names the target by id.
func TestPlatformSecretBindingTargetHTTP(t *testing.T) {
	t.Parallel()
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	path := "/api/admin/app-platform/apps/" + appID + "/secret-bindings/plan"
	plan := func(operation, extra string) (int, string) {
		resp := sessionTestRequest(mux, "POST", path, cookie, fmt.Sprintf(`{"name":"upstream-api","credential_id":%q,"operation":%q%s}`, credential.ID, operation, extra))
		return resp.Code, resp.Body.String()
	}
	code, body := plan("http_bearer", "")
	if code != 200 || !strings.Contains(body, `"network_target":"https://api.example.com"`) || !strings.Contains(body, `"network_target_sha256":"`) {
		t.Fatal("review without a target field", code, body)
	}
	if code, body := plan("http_bearer", `,"target":"default"`); code != 200 || !strings.Contains(body, `"network_target":"https://api.example.com"`) {
		t.Fatal("review of the default target", code, body)
	}
	for _, bad := range []string{`,"target":"nope"`, `,"target":""`, `,"target":"Bad Id"`, `,"target":null`, `,"target":7`} {
		if code, body := plan("http_bearer", bad); code == 200 {
			t.Fatal("accepted", bad, body)
		}
	}
	if code, _ := plan("raw_read", `,"target":"default"`); code != 400 {
		t.Fatal("a raw read named a target", code)
	}
	// The app's binding list names the grant's https targets for the review form,
	// and a committed binding's target.
	list := sessionTestRequest(mux, "GET", "/api/admin/app-platform/apps/"+appID+"/secret-bindings", cookie, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"targets":[{"id":"default","origin":"https://api.example.com"}]`) {
		t.Fatal("binding list lacks the targets", list.Code, list.Body.String())
	}
	platformTargetBind(t, s, admin, installation, "api-a", credential.ID, "", "")
	list = sessionTestRequest(mux, "GET", "/api/admin/app-platform/apps/"+appID+"/secret-bindings", cookie, "")
	if !strings.Contains(list.Body.String(), `"network_target":"https://api.example.com"`) || !strings.Contains(list.Body.String(), `"network_target_sha256":"`) {
		t.Fatal("binding list lacks the binding's target", list.Body.String())
	}
}
