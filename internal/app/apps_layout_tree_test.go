package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tree-shaped revocation, the principals directory's 401, and the documented 401 (ADR 0048).

func TestClosingAParentOUThatClosesAChildRevokes(t *testing.T) {
	e := newAppsEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	mustAddUser(t, e.s, "cora", false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username='cora'`, child); err != nil {
		t.Fatal(err)
	}
	tok := e.mint("cora", "alpha")
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("before = %d", code)
	}
	e.mustSetAccess("alpha", e.ou, "false") // the child has no row of its own and inherits the closing
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("closing a parent did not revoke the token of a user in the child")
	}
}

func TestAChildReinheritingAClosedParentRevokes(t *testing.T) {
	e := newAppsEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	mustAddUser(t, e.s, "cora", false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username='cora'`, child); err != nil {
		t.Fatal(err)
	}
	e.mustSetAccess("alpha", e.ou, "false")
	e.mustSetAccess("alpha", child, "true") // the child opens what its parent closed
	tok := e.mint("cora", "alpha")
	if code := e.bridgeGet(tok); code != http.StatusOK {
		t.Fatalf("an open override = %d", code)
	}
	e.mustSetAccess("alpha", child, "null") // back to inheriting the closed parent: that closes it
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("re-inheriting a closed parent did not revoke the token")
	}
}

func TestAUserWithNoGroupUnderAClosedDefaultIsClosed(t *testing.T) {
	e := newAppsEnv(t)
	tok := e.mint("bob", "alpha") // bob has no OU
	e.mustSetAccess("alpha", e.s.st.DefaultGroupID(), "false")
	if code := e.bridgeGet(tok); code == http.StatusOK {
		t.Fatal("a user with no group kept an app the Default OU closed")
	}
	if w := e.do("bob", "POST", "/api/apps/alpha/token", ""); w.Code != http.StatusForbidden {
		t.Fatalf("mint = %d", w.Code)
	}
	if _, err := e.s.st.exec(`DELETE FROM app_ou_access`); err != nil {
		t.Fatal(err)
	}
	if !e.s.st.appOpenQuiet("bob", "alpha") {
		t.Fatal("no rows must mean open")
	}
	if _, err := e.s.st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('alpha',?,0)`, e.s.st.DefaultGroupID()); err != nil {
		t.Fatal(err)
	}
	if e.s.st.appOpenQuiet("bob", "alpha") {
		t.Fatal("a closed Default OU must close an unassigned user")
	}
}

func principalsRequest(s *Server, token string) *http.Response {
	r := httptest.NewRequest("GET", "/api/v1/extensions/status/principals?kind=ou", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	r.SetPathValue("id", "status")
	w := httptest.NewRecorder()
	s.v1ExtensionPrincipals(w, r)
	return w.Result()
}

func TestAMissingAppTokenIsA401AtThePrincipalsDirectoryToo(t *testing.T) {
	s := extensionFixture(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	if w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	token := extensionMint(t, s, "admin")
	s.appTok.revokeApp("status")
	for name, tok := range map[string]string{"revoked": token, "unknown": "no-such-token"} {
		resp := principalsRequest(s, tok)
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		if resp.StatusCode != http.StatusUnauthorized || body.Error.Code != "unauthorized" {
			t.Fatalf("%s token = %d %q, want 401 unauthorized", name, resp.StatusCode, body.Error.Code)
		}
	}
	// A live token that is refused stays a 403.
	viewerTok := extensionMint(t, s, "viewer")
	if resp := principalsRequest(s, viewerTok); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a live non-admin token = %d, want 403", resp.StatusCode)
	}
}

func TestTheProxyAnswersARevokedTokenWithTheCommonUnauthorizedCode(t *testing.T) {
	s := extensionFixture(t)
	token := extensionMint(t, s, "viewer")
	s.appTok.revokeApp("status")
	w := extensionRequest(s, token, "GET", "status")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"unauthorized"`) {
		t.Fatalf("proxy = %d %s", w.Code, w.Body.String())
	}
}

func TestOpenAPIDocumentsTheExtensionRoutes401(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(openapiJSON, &doc); err != nil {
		t.Fatal(err)
	}
	for path, method := range map[string]string{
		"/api/v1/extensions/{id}/request":    "post",
		"/api/v1/extensions/{id}/principals": "get",
	} {
		resp, ok := doc.Paths[path][method].Responses["401"]
		if !ok || !strings.Contains(resp.Description, "mint a new token") {
			t.Errorf("%s documents no 401 that tells the host page to mint a new token: %+v", path, resp)
		}
		if d := doc.Paths[path][method].Responses["403"].Description; !strings.Contains(d, "app_closed") && path == "/api/v1/extensions/{id}/request" {
			t.Errorf("%s: the 403 does not name app_closed: %q", path, d)
		}
	}
}
