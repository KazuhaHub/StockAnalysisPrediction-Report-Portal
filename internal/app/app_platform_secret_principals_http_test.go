package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPlatformSecretBindingPrincipalsHTTP(t *testing.T) {
	t.Parallel()
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	path := "/api/admin/app-platform/apps/" + appID + "/secret-bindings"
	plan := func(extra string) (int, string) {
		resp := sessionTestRequest(mux, "POST", path+"/plan", cookie, fmt.Sprintf(`{"name":"upstream-api","credential_id":%q,"operation":"http_bearer"%s}`, credential.ID, extra))
		return resp.Code, resp.Body.String()
	}
	// Without the field the review is viewer-only, as before.
	code, body := plan("")
	if code != 200 || !strings.Contains(body, `"principals":["viewer"]`) {
		t.Fatal("default principals", code, body)
	}
	for _, bad := range []string{`,"principals":[]`, `,"principals":["viewer","service"]`, `,"principals":["admin"]`, `,"principals":"service"`, `,"principals":["service","service"]`, `,"principals":null`} {
		if code, body := plan(bad); code != 400 {
			t.Fatal("accepted", bad, code, body)
		}
	}
	for extra, want := range map[string]string{
		`,"principals":["viewer"]`:           `["viewer"]`,
		`,"principals":["service"]`:          `["service"]`,
		`,"principals":["service","viewer"]`: `["service","viewer"]`,
	} {
		code, body := plan(extra)
		if code != 200 || !strings.Contains(body, `"principals":`+want) {
			t.Fatal("plan", extra, code, body)
		}
		var review struct {
			ID  string `json:"plan_id"`
			SHA string `json:"plan_sha256"`
		}
		if err := json.Unmarshal([]byte(body), &review); err != nil {
			t.Fatal(err)
		}
		commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/secret-binding-reviews/"+review.ID, cookie, fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q}`, review.ID, review.SHA))
		if commit.Code != 200 || !strings.Contains(commit.Body.String(), `"principals":`+want) {
			t.Fatal("commit", extra, commit.Code, commit.Body.String())
		}
		list := sessionTestRequest(mux, "GET", path, cookie, "")
		if list.Code != 200 || !strings.Contains(list.Body.String(), `"principals":`+want) {
			t.Fatal("list", extra, list.Code, list.Body.String())
		}
	}
}
