package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestPlatformCredentialManagementHTTP(t *testing.T) {
	s, mux, actor, _, cookie := platformManagementFixture(t, false)
	value := base64.StdEncoding.EncodeToString([]byte("disposable-http-value"))
	create := sessionTestRequest(mux, "POST", "/api/admin/app-platform/credentials", cookie, fmt.Sprintf(`{"label":"HTTP fixture","value_base64":%q}`, value))
	if create.Code != 200 || strings.Contains(create.Body.String(), value) || strings.Contains(create.Body.String(), "sealed_value") {
		t.Fatal("credential create", create.Code, create.Body.String())
	}
	var credential platformCredentialMetadata
	if err := json.Unmarshal(create.Body.Bytes(), &credential); err != nil || !platformValidUUID(credential.ID) || credential.Revision != 1 {
		t.Fatal("credential response", err, create.Body.String())
	}
	list := sessionTestRequest(mux, "GET", "/api/admin/app-platform/credentials?limit=10", cookie, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), credential.ID) || strings.Contains(list.Body.String(), value) {
		t.Fatal("credential list", list.Code, list.Body.String())
	}
	preview := sessionTestRequest(mux, "POST", "/api/admin/app-platform/credentials/"+credential.ID+"/preview", cookie, fmt.Sprintf(`{"action":"replace","revision":"1","value_revision":"1"}`))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"direct":true`) {
		t.Fatal("direct credential preview", preview.Code, preview.Body.String())
	}
	replacement := base64.StdEncoding.EncodeToString([]byte("replacement-http-value"))
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/credentials/"+credential.ID+"/commit", cookie, fmt.Sprintf(`{"action":"replace","revision":"1","value_revision":"1","value_base64":%q,"plan_id":"","plan_sha256":""}`, replacement))
	if commit.Code != 200 || !strings.Contains(commit.Body.String(), `"value_revision":2`) {
		t.Fatal("direct credential replacement", commit.Code, commit.Body.String())
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/credentials", cookie, ""); got.Code != 403 {
		t.Fatal("ordinary user enumerated credentials", got.Code)
	}
}

func TestPlatformSecretBindingManagementHTTP(t *testing.T) {
	s, admin, installation, credential := platformSecretBindingFixture(t, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	path := "/api/admin/app-platform/apps/" + appID + "/secret-bindings"
	plan := sessionTestRequest(mux, "POST", path+"/plan", cookie, fmt.Sprintf(`{"name":"upstream-api","credential_id":%q,"operation":"http_bearer"}`, credential.ID))
	if plan.Code != 200 || strings.Contains(plan.Body.String(), "disposable-upstream-value") {
		t.Fatal("binding review HTTP", plan.Code, plan.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil || !platformValidUUID(review.ID) || !platformValidSHA(review.SHA) {
		t.Fatal("binding review response", err, plan.Body.String())
	}
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/secret-binding-reviews/"+review.ID, cookie, fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q}`, review.ID, review.SHA))
	if commit.Code != 200 || !strings.Contains(commit.Body.String(), `"state":"active"`) {
		t.Fatal("binding commit HTTP", commit.Code, commit.Body.String())
	}
	binding := scalar[string](t, s.st, `SELECT id FROM app_platform_secret_bindings WHERE installation_id=? AND name='upstream-api'`, installation)
	list := sessionTestRequest(mux, "GET", path, cookie, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), binding) || strings.Contains(list.Body.String(), "sealed_value") {
		t.Fatal("binding list HTTP", list.Code, list.Body.String())
	}
	revoke := sessionTestRequest(mux, "POST", "/api/admin/app-platform/secret-bindings/"+binding+"/revoke", cookie, `{"revision":"1"}`)
	if revoke.Code != 200 || !strings.Contains(revoke.Body.String(), `"state":"revoked"`) {
		t.Fatal("binding revoke HTTP", revoke.Code, revoke.Body.String())
	}
}
