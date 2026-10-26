package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func platformHTTPFixture(t *testing.T) (*Server, *http.ServeMux, platformActorSession, string, string) {
	t.Helper()
	s, reader, id := platformLaunchFixture(t, false)
	platformGrantLaunchUser(t, s, reader, id)
	cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, mux, reader, id, cookie
}
func TestPlatformHTTPDocumentCurrentUserAndPolicy(t *testing.T) {
	s, mux, reader, id, cookie := platformHTTPFixture(t)
	launch := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+id+"/launch", cookie, "")
	if launch.Code != 200 {
		t.Fatalf("launch: %d %s", launch.Code, launch.Body.String())
	}
	var payload struct {
		Source  string `json:"source"`
		Binding struct {
			Generation string `json:"generation"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(launch.Body.Bytes(), &payload); err != nil || payload.Source == "" || payload.Binding.Generation != "1" {
		t.Fatalf("bad launch metadata: %s %v", launch.Body.String(), err)
	}
	result := sessionTestRequest(mux, "GET", payload.Source, cookie, "")
	if result.Code != 200 || !strings.Contains(result.Body.String(), "Fixture") {
		t.Fatalf("document: %d %s", result.Code, result.Body.String())
	}
	for key, want := range map[string]string{"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Content-Type": "text/html; charset=utf-8"} {
		if result.Header().Get(key) != want {
			t.Fatalf("missing %s", key)
		}
	}
	if !strings.Contains(result.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(result.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatal("missing opaque document sandbox")
	}
	conditional := httptest.NewRequest("GET", payload.Source, nil)
	conditional.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	conditional.Header.Set("If-None-Match", "*")
	conditional.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, conditional)
	if w.Code != 200 || w.Body.String() != result.Body.String() || w.Header().Get("ETag") != "" {
		t.Fatal("conditional or range request bypassed full response policy")
	}
	head := sessionTestRequest(mux, "HEAD", payload.Source, cookie, "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("HEAD policy differs")
	}
	for _, source := range []string{strings.Replace(payload.Source, "/1/1/", "/2/1/", 1), payload.Source + "?token=fixture", strings.Replace(payload.Source, "index.html", "%69ndex.html", 1)} {
		if got := sessionTestRequest(mux, "GET", source, cookie, ""); got.Code != 404 || strings.Contains(got.Body.String(), "Fixture") {
			t.Fatalf("invalid binding/path exposed content: %s %d", source, got.Code)
		}
	}
	if got := sessionTestRequest(mux, "GET", payload.Source, "", ""); got.Code != 401 {
		t.Fatal("anonymous document read")
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, id)
	if got := sessionTestRequest(mux, "GET", payload.Source, cookie, ""); got.Code != 404 || strings.Contains(got.Body.String(), "Fixture") {
		t.Fatal("old URL survived audience revocation")
	}
	mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, reader.sessionID)
	if got := sessionTestRequest(mux, "GET", payload.Source, cookie, ""); got.Code != 401 {
		t.Fatal("revoked cookie opened document")
	}
}
func TestPlatformHTTPDocumentEnrolmentAndBudget(t *testing.T) {
	s, mux, _, id, cookie := platformHTTPFixture(t)
	path := "/api/app-platform/installations/" + id + "/launch"
	// An existing Default OU security mandate must cover the newly registered route.
	var defaultID int64
	if err := s.st.queryRow(`SELECT id FROM user_groups WHERE is_default=1`).Scan(&defaultID); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetGroupRequire2FA(defaultID, true); err != nil {
		t.Fatal(err)
	}
	if got := sessionTestRequest(mux, "GET", path, cookie, ""); got.Code != 403 {
		t.Fatalf("enrolment wall skipped: %d %s", got.Code, got.Body.String())
	}
	if err := s.st.SetGroupRequire2FA(defaultID, false); err != nil {
		t.Fatal(err)
	}
	release1, ok := s.platformDocuments.acquire()
	if !ok {
		t.Fatal("first slot unavailable")
	}
	defer release1()
	release2, ok := s.platformDocuments.acquire()
	if !ok {
		t.Fatal("second slot unavailable")
	}
	defer release2()
	if got := sessionTestRequest(mux, "GET", path, cookie, ""); got.Code != 429 || got.Header().Get("Retry-After") != "1" {
		t.Fatalf("unbounded compilation admission: %d", got.Code)
	}
	if got := sessionTestRequest(mux, "GET", path, "", ""); got.Code != 401 {
		t.Fatal("budget replaced authentication wall")
	}
}

func TestPlatformHTTPDocumentCounterAndIdentityRefusals(t *testing.T) {
	s, mux, reader, id, cookie := platformHTTPFixture(t)
	binding, err := s.inspectPlatformLaunchAccess(reader, id)
	if err != nil {
		t.Fatal(err)
	}
	source := platformDocumentSource(binding)
	for _, changed := range []string{strings.Replace(source, "/1/1/", "/01/1/", 1), strings.Replace(source, "/1/1/", "/+1/1/", 1), strings.Replace(source, "/1/1/", "/9223372036854775808/1/", 1), strings.Replace(source, binding.releaseSHA, strings.Repeat("b", 64), 1), strings.Replace(source, binding.frontendSHA, strings.Repeat("c", 64), 1)} {
		got := sessionTestRequest(mux, "GET", changed, cookie, "")
		if got.Code != 404 || strings.Contains(got.Body.String(), "Fixture") {
			t.Fatalf("invalid identity received document: %d", got.Code)
		}
		if got.Header().Get("Cache-Control") != "no-store" || got.Header().Get("Content-Security-Policy") == "" {
			t.Fatal("error response lacked policy")
		}
	}
	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, id)
	if got := sessionTestRequest(mux, "GET", source, cookie, ""); got.Code != 404 {
		t.Fatal("disabled installation reopened")
	}
}

func TestPlatformHTTPReaderPinsSignedSessionLifetime(t *testing.T) {
	s, _, reader, _, cookie := platformHTTPFixture(t)
	request := httptest.NewRequest("GET", "/", nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	captured, err := s.platformHTTPReader(request, reader.username)
	if err != nil || !captured.expires.Equal(reader.expires) {
		t.Fatalf("signed lifetime changed: %+v %v", captured, err)
	}
	mustExec(t, s.st, `UPDATE user_sessions SET expires_at=expires_at+3600 WHERE id=?`, reader.sessionID)
	if _, err := s.platformHTTPReader(request, reader.username); err == nil {
		t.Fatal("old cookie inherited a mutable longer session lifetime")
	}
}
