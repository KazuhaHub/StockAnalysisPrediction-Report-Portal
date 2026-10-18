package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionCookieSecurityForHTTPSAndTrustedProxies(t *testing.T) {
	_, trusted, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{trustedNets: []*net.IPNet{trusted}}
	for _, tc := range []struct {
		name, url, remote, forwarded string
		secure                       bool
	}{
		{"TLS", "https://portal.example/", "192.0.2.1:1234", "", true},
		{"trusted proxy", "http://portal.example/", "10.0.0.1:1234", "https", true},
		{"untrusted proxy", "http://portal.example/", "192.0.2.1:1234", "https", false},
		{"plain HTTP", "http://portal.example/", "192.0.2.1:1234", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.url, nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			w := httptest.NewRecorder()
			s.writeSessionCookie(w, r, "signed-value", time.Now().Add(time.Hour).Unix())
			issued := w.Result().Cookies()[0]
			w = httptest.NewRecorder()
			s.clearSessionCookie(w, r)
			cleared := w.Result().Cookies()[0]
			if issued.Secure != tc.secure || cleared.Secure != tc.secure || !issued.HttpOnly || !cleared.HttpOnly {
				t.Fatalf("cookie flags: issued=%+v cleared=%+v", issued, cleared)
			}
			if cleared.Value != "" || cleared.MaxAge >= 0 || issued.SameSite != http.SameSiteLaxMode || cleared.SameSite != issued.SameSite || cleared.Path != issued.Path {
				t.Fatalf("cookie deletion: issued=%+v cleared=%+v", issued, cleared)
			}
		})
	}
}

func managedTestCookie(t *testing.T, s *Server, user, agent string, ttl time.Duration) string {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("User-Agent", agent)
	w := httptest.NewRecorder()
	if !s.setSessionCookieFor(w, r, *s.st.GetUser(user), ttl) {
		t.Fatal(w.Body.String())
	}
	return w.Result().Cookies()[0].Value
}

func sessionTestRequest(mux *http.ServeMux, method, path, cookie, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestManagedSessionsRevokeOnlyTheirOwnerAndRejectCookieReplay(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	b := managedTestCookie(t, s, "alice", "Firefox/128.0", time.Hour)
	other := managedTestCookie(t, s, "bob", "Chrome/130.0", time.Hour)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	w := sessionTestRequest(mux, "GET", "/api/me/sessions", a, "")
	var list struct {
		Items []AccountSession `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != 2 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	id := sessionID(reqWith(b))
	if w := sessionTestRequest(mux, "DELETE", "/api/me/sessions/"+id, other, ""); w.Code != 404 {
		t.Fatalf("cross-account delete: %d", w.Code)
	}
	if w := sessionTestRequest(mux, "DELETE", "/api/me/sessions/"+id, a, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := sessionTestRequest(mux, "GET", "/api/me/sessions", b, ""); w.Code != 401 {
		t.Fatalf("revoked cookie still works: %d", w.Code)
	}
	if s.currentActiveUser(reqWith(a)) != "alice" || s.currentActiveUser(reqWith(other)) != "bob" {
		t.Fatal("unrelated sessions were revoked")
	}
	if w := sessionTestRequest(mux, "DELETE", "/api/me/sessions/"+sessionID(reqWith(a)), a, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"signed_out":true`) {
		t.Fatalf("current revoke: %s", w.Body.String())
	}
	if s.currentActiveUser(reqWith(a)) != "" {
		t.Fatal("current cookie survived revoke")
	}
}

func TestBulkSessionRevocationIncludesLegacyAndPreservesCurrentExpiry(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	b := managedTestCookie(t, s, "alice", "Firefox/128.0", time.Hour)
	legacy := s.sign("alice")
	var before int64
	s.st.queryRow(`SELECT expires_at FROM user_sessions WHERE id=?`, sessionID(reqWith(a))).Scan(&before)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	w := sessionTestRequest(mux, "POST", "/api/me/sessions/revoke", a, `{"scope":"others"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	fresh := w.Result().Cookies()[0].Value
	if s.currentActiveUser(reqWith(fresh)) != "alice" {
		t.Fatal("current session was not retained")
	}
	for _, cookie := range []string{a, b, legacy} {
		if s.currentActiveUser(reqWith(cookie)) != "" {
			t.Fatal("old cookie survived bulk revoke")
		}
	}
	var after int64
	s.st.queryRow(`SELECT expires_at FROM user_sessions WHERE id=?`, sessionID(reqWith(fresh))).Scan(&after)
	if before != after {
		t.Fatalf("lifetime extended: %d -> %d", before, after)
	}
	w = sessionTestRequest(mux, "POST", "/api/me/sessions/revoke", fresh, `{"scope":"all"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"signed_out":true`) {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(fresh)) != "" {
		t.Fatal("logout all retained current session")
	}
}

func TestLegacyMigrationCannotResurrectAKickedSession(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	legacy := s.sign("alice")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	w := sessionTestRequest(mux, "GET", "/api/me", legacy, "")
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("migration: %d %s", w.Code, w.Body.String())
	}
	fresh := w.Result().Cookies()[0].Value
	if sessionID(reqWith(fresh)) != sessionID(reqWith(legacy)) {
		t.Fatal("migration lost legacy identity")
	}
	if w := sessionTestRequest(mux, "DELETE", "/api/me/sessions/"+sessionID(reqWith(fresh)), fresh, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, cookie := range []string{fresh, legacy} {
		if w := sessionTestRequest(mux, "GET", "/api/me", cookie, ""); w.Code != 401 {
			t.Fatalf("revoked legacy login returned: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestLogoutRevokesServerSessionAndIgnoresForgedCookie(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	// Keep the encoded id but remove the signature; logout cannot revoke it.
	forged := strings.SplitN(a, ".", 2)[0] + ".bad"
	if w := sessionTestRequest(mux, "POST", "/api/logout", forged, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(a)) != "alice" {
		t.Fatal("forged cookie revoked another session")
	}
	if w := sessionTestRequest(mux, "POST", "/api/logout", a, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(a)) != "" {
		t.Fatal("logout only cleared the browser cookie")
	}
}

func TestSessionExpiryRevisionAndSweep(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	id := sessionID(reqWith(a))
	if s.currentActiveUser(reqWith(s.managedCookie(*s.st.GetUser("alice"), "unknown", time.Now().Add(time.Hour).Unix()))) != "" {
		t.Fatal("unpersisted signed session was accepted")
	}
	s.st.exec(`UPDATE user_sessions SET last_seen=? WHERE id=?`, time.Now().Add(-time.Hour).Unix(), id)
	s.currentActiveUser(reqWith(a))
	var last int64
	s.st.queryRow(`SELECT last_seen FROM user_sessions WHERE id=?`, id).Scan(&last)
	if last < time.Now().Add(-time.Minute).Unix() {
		t.Fatal("last activity was not updated")
	}
	s.st.BumpSessionRev("alice")
	if s.currentActiveUser(reqWith(a)) != "" {
		t.Fatal("revision change did not invalidate session")
	}
	if err := s.st.purgeSessions(time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_sessions`); n != 0 {
		t.Fatal("stale sessions survived sweep")
	}
	b := managedTestCookie(t, s, "alice", "Firefox/128.0", time.Hour)
	s.st.exec(`UPDATE user_sessions SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Unix(), sessionID(reqWith(b)))
	if s.currentActiveUser(reqWith(b)) != "" {
		t.Fatal("expired session authenticated")
	}
	if err := s.st.purgeSessions(time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsAndPersonalRetentionSurviveRestartAndBackup(t *testing.T) {
	s := tenancyServer(t)
	st, err := OpenStore("sqlite", filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.st = st
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	b := managedTestCookie(t, s, "alice", "Firefox/128.0", time.Hour)
	s.st.exec(`UPDATE user_sessions SET revoked=1 WHERE id=?`, sessionID(reqWith(b)))
	s.st.WriteAudit(historyEntry("alice"))
	s.st.SetSetting(setLoginHistoryKeep, "7")
	path := filepath.Join(t.TempDir(), "restored.db")
	dump := dumpOf(t, s.st)
	s.st.Close()
	reopened, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.restoreFrom(bytes.NewReader(dump), true); err != nil {
		reopened.Close()
		t.Fatal(err)
	}
	reopened.Close()
	s.st, err = OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.st.Close()
	if s.currentActiveUser(reqWith(a)) != "alice" || s.currentActiveUser(reqWith(b)) != "" {
		t.Fatal("backup/restart lost session or revocation state")
	}
	_, total, keep, err := s.st.listLoginHistory("alice", 20)
	if err != nil || total != 1 || keep != 7 {
		t.Fatalf("history after restore: %d %d %v", total, keep, err)
	}
}

func TestAStaleLegacyRequestCannotMigrateAfterBulkLogout(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	r := reqWith(s.sign("alice"))
	if s.currentActiveUser(r) != "alice" {
		t.Fatal("not authenticated before logout")
	}
	s.st.BumpSessionRev("alice")
	w := httptest.NewRecorder()
	if s.ensureManagedSession(w, r, "alice") || w.Code != 401 {
		t.Fatal("in-flight legacy request bypassed bulk revocation")
	}
}

func TestPostgresManagedSessionRevocation(t *testing.T) {
	st := pgStore(t)
	s := tenancyServer(t)
	s.st = st
	if err := st.UpsertUser(User{Username: "pg-sessions", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	a := managedTestCookie(t, s, "pg-sessions", "Chrome/130.0", time.Hour)
	b := managedTestCookie(t, s, "pg-sessions", "Firefox/128.0", time.Hour)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	if w := sessionTestRequest(mux, "DELETE", "/api/me/sessions/"+sessionID(reqWith(b)), a, ""); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(b)) != "" {
		t.Fatal("Postgres revoked session survived")
	}
	w := sessionTestRequest(mux, "POST", "/api/me/sessions/revoke", a, `{"scope":"others"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	fresh := w.Result().Cookies()[0].Value
	if s.currentActiveUser(reqWith(fresh)) != "pg-sessions" || s.currentActiveUser(reqWith(a)) != "" {
		t.Fatal("Postgres revision change did not retain only the new current cookie")
	}
	if w := sessionTestRequest(mux, "POST", "/api/me/sessions/revoke", fresh, `{"scope":"all"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(fresh)) != "" {
		t.Fatal("Postgres logout-all retained a session")
	}
}

// The synthetic one-node IPv4 MMDB maps both branches to US / Test State / Testville.
// It exercises read-time resolution without a remote service or production database.
func TestSessionsResolveLocationAtReadTime(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	cookie := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	if _, err := s.st.exec(`UPDATE user_sessions SET ip=? WHERE username=?`, "192.0.2.1", "alice"); err != nil {
		t.Fatal(err)
	}
	s.geo = newGeoService(t.TempDir())
	s.geo.st = s.st
	t.Cleanup(func() {
		if s.geo.reader != nil {
			s.geo.reader.Close()
		}
	})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	read := func() map[string]any {
		t.Helper()
		w := sessionTestRequest(mux, "GET", "/api/me/sessions", cookie, "")
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatalf("sessions: %d %s", w.Code, w.Body.String())
		}
		return out.Items[0]
	}
	if got := read(); got["geo"] != nil {
		t.Fatalf("uninstalled database: %+v", got)
	}
	raw, err := base64.StdEncoding.DecodeString("AAARAAARAAAAAAAAAAAAAAAAAAAAAONHY291bnRyeeJIaXNvX2NvZGVCVVNFbmFtZXPhQmVuTVVuaXRlZCBTdGF0ZXNEY2l0eeFFbmFtZXPhQmVuSVRlc3R2aWxsZUxzdWJkaXZpc2lvbnMBBOFFbmFtZXPhQmVuSlRlc3QgU3RhdGWrze9NYXhNaW5kLmNvbelKbm9kZV9jb3VudMEBS3JlY29yZF9zaXplwRhKaXBfdmVyc2lvbsEETWRhdGFiYXNlX3R5cGVMU2Vzc2lvbi1UZXN0SWxhbmd1YWdlcwEEQmVuW2JpbmFyeV9mb3JtYXRfbWFqb3JfdmVyc2lvbsECW2JpbmFyeV9mb3JtYXRfbWlub3JfdmVyc2lvbsEAS2J1aWxkX2Vwb2NowQFLZGVzY3JpcHRpb27hQmVuT1Nlc3Npb24gZml4dHVyZQ==")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.geo.Dir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.geo.Dir(), "fixture.mmdb"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	got := read()
	geo, ok := got["geo"].(map[string]any)
	if !ok || geo["country_code"] != "US" || geo["region"] != "Test State" || geo["city"] != "Testville" || got["ip"] != "192.0.2.1" {
		t.Fatalf("resolved session: %+v", got)
	}
	s.st.SetSetting(setGeoEnabled, "0")
	if got := read(); got["geo"] != nil {
		t.Fatalf("disabled lookup: %+v", got)
	}
}
