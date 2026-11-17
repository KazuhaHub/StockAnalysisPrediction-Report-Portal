package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The administrator's installation settings carry serviceEgress {lastSession,
// lastSummary}, read from audit_log (doc 174 F9): the last session event (opened
// or closed) and the last summary row, of that installation only.
func testPlatformServiceEgressSettings(t *testing.T, pg bool) {
	s, admin, reader, installation := platformObjectsFixturePG(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	path := "/api/admin/app-platform/installations/" + installation + "/settings"
	read := func() map[string]any {
		t.Helper()
		response := sessionTestRequest(mux, "GET", path, cookie, "")
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var body struct {
			ServiceEgress map[string]any `json:"serviceEgress"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.ServiceEgress == nil {
			t.Fatal("settings lack serviceEgress", err, response.Body.String())
		}
		return body.ServiceEgress
	}
	// Nothing recorded yet: both are present and null.
	if got := read(); len(got) != 2 || got["lastSession"] != nil || got["lastSummary"] != nil {
		t.Fatal("empty serviceEgress", got)
	}
	// Rows of another installation never show.
	other := platformFixtureID(1998)
	at := time.Now()
	for _, row := range []struct {
		action, installation string
		detail               map[string]any
	}{
		{platformServiceEgressAuditOpened, other, map[string]any{"session_id": "x"}},
		{platformServiceEgressAuditSummary, other, map[string]any{"session_id": "x"}},
		{platformServiceEgressAuditOpened, installation, map[string]any{"session_id": "s1", "resources": []string{"network.http"}}},
		{platformServiceEgressAuditSummary, installation, map[string]any{"session_id": "s1", "counts": map[string]int{"ok": 3}}},
		{platformServiceEgressAuditDenied, installation, map[string]any{"method": "GET", "origin": "https://api.example.com", "session_id": "s1"}},
	} {
		if err := s.writePlatformServiceEgressAudit(row.action, row.installation, at, row.detail); err != nil {
			t.Fatal(err)
		}
	}
	got := read()
	session, _ := got["lastSession"].(map[string]any)
	summary, _ := got["lastSummary"].(map[string]any)
	if session == nil || session["event"] != "opened" || session["detail"].(map[string]any)["session_id"] != "s1" ||
		summary == nil || summary["detail"].(map[string]any)["counts"].(map[string]any)["ok"] != float64(3) {
		t.Fatal("serviceEgress after opening", got)
	}
	if _, err := time.Parse(time.RFC3339, session["at"].(string)); err != nil {
		t.Fatal("event time", err)
	}
	// The latest session event wins, and a denied row is neither.
	if err := s.writePlatformServiceEgressAudit(platformServiceEgressAuditClosed, installation, at, map[string]any{"session_id": "s1", "reason": "renewed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.writePlatformServiceEgressAudit(platformServiceEgressAuditDenied, installation, at, map[string]any{"method": "GET"}); err != nil {
		t.Fatal(err)
	}
	got = read()
	session, _ = got["lastSession"].(map[string]any)
	if session["event"] != "closed" || session["detail"].(map[string]any)["reason"] != "renewed" {
		t.Fatal("serviceEgress after closing", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "api.example.com") {
		t.Fatal("a denied row leaked into the diagnostics")
	}
	// Only administrators read it.
	readerCookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	if forbidden := sessionTestRequest(mux, "GET", path, readerCookie, ""); forbidden.Code != http.StatusForbidden {
		t.Fatal("non-admin read the diagnostics", forbidden.Code)
	}
}

func TestPlatformServiceEgressSettingsSQLite(t *testing.T) {
	testPlatformServiceEgressSettings(t, false)
}
func TestPlatformServiceEgressSettingsPostgres(t *testing.T) {
	testPlatformServiceEgressSettings(t, true)
}
