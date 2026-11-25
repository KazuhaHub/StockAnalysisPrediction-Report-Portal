package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlatformCredentialManagementHTTP(t *testing.T) {
	t.Parallel()
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
	preview := sessionTestRequest(mux, "POST", "/api/admin/app-platform/credentials/"+credential.ID+"/preview", cookie, `{"action":"replace","revision":"1","value_revision":"1"}`)
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
	t.Parallel()
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

// An ACS3 binding is reviewed through the same endpoints, with the allowed (action, version) pairs
// in the request (doc 177 section 4); the review response echoes them and never a secret.
func TestPlatformSecretBindingManagementHTTPACS3(t *testing.T) {
	t.Parallel()
	s, admin, installation, credential := acs3Fixture(t, false, acs3FixtureToken)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	appID := scalar[string](t, s.st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	path := "/api/admin/app-platform/apps/" + appID + "/secret-bindings"
	plan := func(body string) *httptest.ResponseRecorder {
		return sessionTestRequest(mux, "POST", path+"/plan", cookie, body)
	}
	good := fmt.Sprintf(`{"name":"qwen-billing","credential_id":%q,"operation":"acs3_hmac_sha256","principals":["service"],"acs3_actions":[{"action":"QueryAccountBalance","version":"2017-12-14"}]}`, credential.ID)
	for name, body := range map[string]string{
		"no actions":              fmt.Sprintf(`{"name":"qwen-billing","credential_id":%q,"operation":"acs3_hmac_sha256"}`, credential.ID),
		"empty actions":           strings.Replace(good, `[{"action":"QueryAccountBalance","version":"2017-12-14"}]`, `[]`, 1),
		"null actions":            strings.Replace(good, `[{"action":"QueryAccountBalance","version":"2017-12-14"}]`, `null`, 1),
		"unknown pair member":     strings.Replace(good, `"version":"2017-12-14"`, `"version":"2017-12-14","x":1`, 1),
		"duplicate pair member":   strings.Replace(good, `"action":"QueryAccountBalance"`, `"action":"QueryAccountBalance","action":"x"`, 1),
		"bad version":             strings.Replace(good, `2017-12-14`, `2017`, 1),
		"actions on a bearer":     strings.Replace(good, `acs3_hmac_sha256`, `http_bearer`, 1),
		"unsorted pairs":          strings.Replace(good, `[{"action":"QueryAccountBalance","version":"2017-12-14"}]`, `[{"action":"B","version":"2017-12-14"},{"action":"A","version":"2017-12-14"}]`, 1),
		"actions that are a text": strings.Replace(good, `[{"action":"QueryAccountBalance","version":"2017-12-14"}]`, `"QueryAccountBalance"`, 1),
	} {
		if got := plan(body); got.Code == 200 {
			t.Errorf("%s: accepted %s", name, got.Body.String())
		}
	}
	got := plan(good)
	if got.Code != 200 {
		t.Fatal("acs3 review", got.Code, got.Body.String())
	}
	for _, secret := range []string{acs3FixtureSecret, acs3FixtureToken, acs3FixtureKeyID} {
		if strings.Contains(got.Body.String(), secret) {
			t.Fatal("review response carries a secret")
		}
	}
	var review struct {
		ID     string `json:"plan_id"`
		SHA    string `json:"plan_sha256"`
		Review struct {
			Operation string               `json:"operation"`
			Origin    string               `json:"network_origin"`
			Actions   []platformACS3Action `json:"acs3_actions"`
		} `json:"review"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &review); err != nil || review.Review.Operation != "acs3_hmac_sha256" || review.Review.Origin != "https://api.example.com" || len(review.Review.Actions) != 1 || review.Review.Actions[0].Action != "QueryAccountBalance" {
		t.Fatal("review response", err, got.Body.String())
	}
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/secret-binding-reviews/"+review.ID, cookie, fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q}`, review.ID, review.SHA))
	if commit.Code != 200 || !strings.Contains(commit.Body.String(), `"operation":"acs3_hmac_sha256"`) || !strings.Contains(commit.Body.String(), `"acs3_actions":[{"action":"QueryAccountBalance","version":"2017-12-14"}]`) {
		t.Fatal("acs3 commit", commit.Code, commit.Body.String())
	}
	list := sessionTestRequest(mux, "GET", path, cookie, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"acs3_actions"`) || strings.Contains(list.Body.String(), acs3FixtureSecret) {
		t.Fatal("acs3 binding list", list.Code, list.Body.String())
	}
}
