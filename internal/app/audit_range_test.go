package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The console's time range is two instants. A bare date would be read against UTC stamps, so a
// panel eight hours east of UTC would lose the first eight hours of its first day.
func TestAuditTimeRangeIsBetweenInstants(t *testing.T) {
	s := auditServer(t)
	for _, at := range []string{"2026-10-30T10:00:00Z", "2026-10-30T12:00:00Z", "2026-10-30T14:00:00Z"} {
		s.st.WriteAudit(AuditEntry{At: at, Action: AuditLogin, TargetType: "user", TargetID: at})
	}
	ids := func(f AuditFilter) []string {
		rows, total := s.st.ListAudit(f)
		if total != len(rows) {
			t.Fatalf("total %d, rows %d", total, len(rows))
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.TargetID)
		}
		return out
	}
	// 19:00 in UTC+8 is 11:00Z; 21:00 in UTC+8 is 13:00Z.
	from, _ := time.Parse(time.RFC3339, "2026-10-30T19:00:00+08:00")
	to, _ := time.Parse(time.RFC3339, "2026-10-30T21:00:00+08:00")
	if got := ids(AuditFilter{From: from, To: to}); len(got) != 1 || got[0] != "2026-10-30T12:00:00Z" {
		t.Errorf("19:00..21:00 +08 = %v, want only the 12:00Z row", got)
	}
	if got := ids(AuditFilter{From: from}); len(got) != 2 {
		t.Errorf("from 11:00Z = %v, want the 12:00Z and 14:00Z rows", got)
	}
	// Both ends inclusive: a range ending on a row's own second keeps it.
	end, _ := time.Parse(time.RFC3339, "2026-10-30T10:00:00Z")
	if got := ids(AuditFilter{To: end}); len(got) != 1 || got[0] != "2026-10-30T10:00:00Z" {
		t.Errorf("to 10:00Z = %v, want the 10:00Z row", got)
	}
}

// Rows from before v0.4.15 are the host's local wall clock; each format is compared in its own
// terms, as the retention predicate does.
func TestAuditTimeRangeReadsLegacyStampsInLocalTime(t *testing.T) {
	s := auditServer(t)
	local := time.Date(2026, 8, 1, 9, 0, 0, 0, time.Local)
	s.st.WriteAudit(AuditEntry{At: local.Format("2006-01-02 15:04:05"), Action: AuditLogin, TargetType: "user", TargetID: "legacy"})
	rows, _ := s.st.ListAudit(AuditFilter{From: local.Add(-time.Minute), To: local.Add(time.Minute)})
	if len(rows) != 1 {
		t.Fatalf("legacy row inside the range: got %d rows", len(rows))
	}
	rows, _ = s.st.ListAudit(AuditFilter{From: local.Add(time.Minute)})
	if len(rows) != 0 {
		t.Fatalf("legacy row before the range: got %d rows", len(rows))
	}
}

func TestAuditNegatedFilters(t *testing.T) {
	s := auditServer(t)
	s.st.WriteAudit(AuditEntry{Actor: "admin", Action: AuditLogin, IP: "10.0.0.1", TargetType: "user", TargetID: "a"})
	s.st.WriteAudit(AuditEntry{Actor: "kazuha", Action: AuditLogout, IP: "10.0.0.2", TargetType: "user", TargetID: "b"})
	s.st.WriteAudit(AuditEntry{Action: AuditLoginFailed, TargetType: "user", TargetID: "c"}) // no actor, no IP
	count := func(f AuditFilter) int {
		_, total := s.st.ListAudit(f)
		return total
	}
	if n := count(AuditFilter{NotActor: "admin"}); n != 2 {
		t.Errorf("actor is not admin = %d, want 2 (a row with no actor is not admin either)", n)
	}
	if n := count(AuditFilter{NotIP: "10.0.0.1"}); n != 2 {
		t.Errorf("ip is not 10.0.0.1 = %d, want 2", n)
	}
	if n := count(AuditFilter{NotAction: AuditLogin}); n != 2 {
		t.Errorf("action is not login = %d, want 2", n)
	}
	if n := count(AuditFilter{NotTargetID: "a"}); n != 2 {
		t.Errorf("object is not a = %d, want 2", n)
	}
}

func TestAuditAPIParsesTheRangeAndRefusesAMalformedOne(t *testing.T) {
	s := auditServer(t)
	s.st.UpsertUser(User{Username: "admin", PasswordHash: mustHash("admin-password-here"), Role: "admin"})
	s.st.WriteAudit(AuditEntry{At: "2026-10-30T12:00:00Z", Action: AuditLogin, TargetType: "user", TargetID: "x"})

	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiAdminAudit(rec, httptest.NewRequest(http.MethodGet, "/api/admin/audit?"+q, nil), "admin")
		return rec
	}
	if rec := get("from=2026-10-30T11:00:00Z&to=2026-10-30T13:00:00Z"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Errorf("range around the row: %d %s", rec.Code, rec.Body.String())
	}
	if rec := get("from=yesterday"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed from answered %d, want 400 rather than silently the whole log", rec.Code)
	}
	if rec := get("to=2026-10-30"); rec.Code != http.StatusBadRequest {
		t.Errorf("a bare date as to answered %d, want 400: to is an instant", rec.Code)
	}
	// The older date-only parameter still works, for links written before the range existed.
	if rec := get("since=2026-10-30"); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Errorf("since=date: %d %s", rec.Code, rec.Body.String())
	}
}
