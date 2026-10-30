package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

func extensionAdminCall(s *Server, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/admin/apps/status/backend", strings.NewReader(body))
	r.SetPathValue("id", "status")
	w := httptest.NewRecorder()
	s.apiExtensionBackend(w, r, "admin")
	return w
}
func extensionFixture(t *testing.T) *Server {
	t.Helper()
	s := secretServer(t)
	s.appTok = newAppTokens(time.Minute)
	s.st.EnsureDefaultGroup()
	if err := s.st.UpsertUser(User{Username: "admin", Role: "admin", PasswordHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertUser(User{Username: "viewer", Role: "user", PasswordHash: "hash"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.InstallApp(App{ID: "status", Name: "Status", Entry: "index.html", Scopes: []string{"extension.read", "extension.manage"}}, nil); err != nil {
		t.Fatal(err)
	}
	return s
}
func extensionSaveBody(revision int64, key string) string {
	raw, _ := json.Marshal(map[string]any{"revision": revision, "label": "Status", "base_url": "https://status.example", "audience": "status", "enabled": true, "key_id": "key-one", "signing_key": key, "policy": map[string]any{"version": 1, "routes": []map[string]string{{"method": "GET", "prefix": "/v1/components"}, {"method": "PUT", "prefix": "/v1/components"}}, "ports": []int{443}, "networks": []string{}}})
	return string(raw)
}
func TestExtensionAdminSealsAndRotatesKeys(t *testing.T) {
	s := extensionFixture(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key))
	if w.Code != 200 {
		t.Fatalf("save=%d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), key) {
		t.Fatal("response exposed key")
	}
	c, err := s.st.extensionConnection("status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Secret, key) {
		t.Fatal("plaintext key stored")
	}
	sec, err := s.extensionSecret(c)
	if err != nil {
		t.Fatal(err)
	}
	if w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key)); w.Code != 409 {
		t.Fatalf("CAS=%d", w.Code)
	}
	next := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{43}, 32))
	w = extensionAdminCall(s, "PUT", extensionSaveBody(1, next))
	if w.Code != 200 {
		t.Fatalf("rotate=%d %s", w.Code, w.Body.String())
	}
	c, _ = s.st.extensionConnection("status")
	rotated, err := s.extensionSecret(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sec.IdentitySeed, rotated.IdentitySeed) || bytes.Equal(sec.SigningKey, rotated.SigningKey) {
		t.Fatal("rotation changed identities or kept old key")
	}
	foreign := c
	foreign.InstanceID = "foreign"
	if _, err := s.extensionSecret(foreign); err == nil {
		t.Fatal("cross-instance ciphertext opened")
	}
	for _, bad := range []string{strings.Replace(extensionSaveBody(2, next), "https://status.example", "http://127.0.0.1", 1), strings.Replace(extensionSaveBody(2, next), `"version":1`, `"version":2`, 1)} {
		if w := extensionAdminCall(s, "PUT", bad); w.Code != 400 {
			t.Fatalf("invalid policy=%d %s", w.Code, w.Body.String())
		}
	}
}

type extensionFakeBackend struct {
	signer         protocol.Signer
	calls          int
	responseStatus int
	responseBody   []byte
	viewer         protocol.Viewer
	beforeSend     func()
}

func (f *extensionFakeBackend) Handshake(context.Context, protocol.Signer, time.Time) (string, error) {
	if f.beforeSend != nil {
		f.beforeSend()
	}
	return "boot-one", nil
}
func (f *extensionFakeBackend) Do(_ context.Context, method, path string, body []byte, assertion string) (protocol.BackendResponse, error) {
	f.calls++
	v := protocol.NewVerifier(protocol.VerifyConfig{Issuer: f.signer.Issuer, Audience: f.signer.Audience, AppID: f.signer.AppID, InstanceID: f.signer.InstanceID, BootID: "boot-one", Keys: map[string][]byte{f.signer.KeyID: f.signer.Key}})
	claims, err := v.Verify(assertion, method, path, body, time.Now())
	if err != nil {
		return protocol.BackendResponse{}, err
	}
	f.viewer = claims.Viewer
	status := f.responseStatus
	if status == 0 {
		status = 200
	}
	raw := f.responseBody
	if raw == nil {
		raw = []byte(`{"ok":true,"items":[]}`)
	}
	return protocol.BackendResponse{Status: status, Body: raw}, nil
}
func (f *extensionFakeBackend) Close() {}
func extensionMint(t *testing.T, s *Server, user string) string {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/apps/status/token", nil)
	r.SetPathValue("id", "status")
	w := httptest.NewRecorder()
	s.apiAppToken(w, r, user)
	if w.Code != 200 {
		t.Fatalf("mint=%d %s", w.Code, w.Body.String())
	}
	var out struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &out)
	return out.Token
}
func extensionRequest(s *Server, token, method, app string) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(map[string]string{"method": method, "path": "/v1/components"})
	r := httptest.NewRequest("POST", "/api/v1/extensions/"+app+"/request", bytes.NewReader(raw))
	r.SetPathValue("id", app)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.v1ExtensionRequest(w, r)
	return w
}
func TestExtensionProxyEnforcesCurrentAuthority(t *testing.T) {
	for _, mode := range []string{"read", "manage", "machine", "wrong-app", "demoted", "inactive", "expired", "session", "reinstall", "disabled", "changed-before-send", "OU", "app-closed"} {
		t.Run(mode, func(t *testing.T) {
			s := extensionFixture(t)
			key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
			w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key))
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			c, _ := s.st.extensionConnection("status")
			secret, _ := s.extensionSecret(c)
			issuer, _ := s.st.extensionIssuer()
			backend := &extensionFakeBackend{signer: protocol.Signer{Issuer: issuer, Audience: c.Audience, AppID: c.AppID, InstanceID: c.InstanceID, KeyID: secret.KeyID, Key: secret.SigningKey}}
			s.extensions.clientForTest = func(extensionConnection, extensionPolicy) (extensionBackend, error) { return backend, nil }
			user := "admin"
			if mode == "read" || mode == "manage" || mode == "OU" || mode == "app-closed" {
				user = "viewer"
			}
			token := extensionMint(t, s, user)
			method := "GET"
			app := "status"
			switch mode {
			case "manage":
				method = "PUT"
			case "machine":
				token = s.appTok.mint([]string{"extension.read"}, time.Now())
			case "wrong-app":
				app = "other"
			case "demoted":
				method = "PUT"
				s.st.SetUserRole("admin", "user")
			case "inactive":
				s.st.SetUserActive(user, false)
			case "expired":
				s.st.SetUserExpiry(user, "2000-01-01")
			case "session":
				s.st.SetUserPassword(user, "new")
			case "reinstall":
				a, _ := s.st.GetApp("status")
				s.st.InstallApp(a, nil)
			case "disabled":
				s.st.exec(`UPDATE app_backend_connections SET enabled=0,revision=revision+1 WHERE app_id='status'`)
			case "changed-before-send":
				backend.beforeSend = func() {
					s.st.exec(`UPDATE app_backend_connections SET enabled=0,revision=revision+1 WHERE app_id='status'`)
				}
			case "app-closed":
				// Closed for the viewer's OU after the token was minted (ADR 0048): the next request is refused.
				def := s.st.DefaultGroupID()
				if _, err := s.st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('status',?,0)`, def); err != nil {
					t.Fatal(err)
				}
			case "OU":
				id, err := s.st.CreateUserGroup("New OU", "", 1)
				if err != nil {
					t.Fatal(err)
				}
				s.st.SetPrimaryGroup(user, id)
			}
			w = extensionRequest(s, token, method, app)
			allowed := mode == "read" || mode == "OU"
			if allowed {
				if w.Code != 200 || backend.calls != 1 || backend.viewer.Subject == "" || len(backend.viewer.OUs) == 0 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("read=%d %s calls=%d viewer=%+v", w.Code, w.Body.String(), backend.calls, backend.viewer)
				}
			} else if w.Code < 400 || backend.calls != 0 {
				t.Fatalf("%s forwarded: %d calls=%d", mode, w.Code, backend.calls)
			}
		})
	}
}

func TestExtensionPrincipalDirectoryRequiresCurrentAdmin(t *testing.T) {
	s := extensionFixture(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, user := range []string{"viewer", "admin"} {
		token := extensionMint(t, s, user)
		r := httptest.NewRequest("GET", "/api/v1/extensions/status/principals?kind=ou", nil)
		r.SetPathValue("id", "status")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.v1ExtensionPrincipals(w, r)
		if user == "admin" {
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"principal":"ou:`) {
				t.Fatalf("directory=%d %s", w.Code, w.Body.String())
			}
		} else if w.Code != 403 {
			t.Fatalf("reader directory=%d", w.Code)
		}
	}
}

func TestExtensionProxyReturnsSafeConflictWithoutBackendDetails(t *testing.T) {
	s := extensionFixture(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, 32))
	w := extensionAdminCall(s, "PUT", extensionSaveBody(0, key))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	c, _ := s.st.extensionConnection("status")
	secret, _ := s.extensionSecret(c)
	issuer, _ := s.st.extensionIssuer()
	backend := &extensionFakeBackend{signer: protocol.Signer{Issuer: issuer, Audience: c.Audience, AppID: c.AppID, InstanceID: c.InstanceID, KeyID: secret.KeyID, Key: secret.SigningKey}, responseStatus: 409, responseBody: []byte(`{"error":"private-upstream-secret"}`)}
	s.extensions.clientForTest = func(extensionConnection, extensionPolicy) (extensionBackend, error) { return backend, nil }
	w = extensionRequest(s, extensionMint(t, s, "admin"), "PUT", "status")
	if w.Code != 409 || strings.Contains(w.Body.String(), "private-upstream-secret") {
		t.Fatalf("unsafe conflict=%d %s", w.Code, w.Body.String())
	}
}
