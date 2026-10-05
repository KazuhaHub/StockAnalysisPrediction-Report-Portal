package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func historyEntry(user string) AuditEntry {
	return AuditEntry{Actor: user, Action: AuditLogin, TargetType: "user", TargetID: user, At: "2026-10-04T00:00:00Z", IP: "198.51.100.1", Detail: `{"method":"password"}`}
}

func TestLoginHistoryRetentionDeletesPersonalRowsWithoutAuditOrSessionLoss(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "admin"})
	s.st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	for i := 0; i < 110; i++ {
		s.st.WriteAudit(historyEntry("alice"))
	}
	for i := 0; i < 5; i++ {
		s.st.WriteAudit(historyEntry("bob"))
	}
	items, total, keep, err := s.st.listLoginHistory("alice", 20)
	if err != nil || total != 100 || keep != 100 || len(items) != 20 {
		t.Fatalf("default retention: %d %d %d %v", len(items), total, keep, err)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE actor='alice'`); n != 110 {
		t.Fatalf("audit records lost: %d", n)
	}
	a := managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	b := managedTestCookie(t, s, "bob", "Firefox/128.0", time.Hour)
	for _, method := range []string{"GET", "PUT"} {
		if w := sessionTestRequest(mux, method, "/api/admin/login-activity/retention", b, `{"keep":3}`); w.Code != 403 {
			t.Fatalf("non-admin %s: %d", method, w.Code)
		}
	}
	if w := sessionTestRequest(mux, "PUT", "/api/me/login-activity/retention", b, `{"keep":3}`); w.Code != 404 && w.Code != 405 {
		t.Fatalf("personal retention still writable: %d", w.Code)
	}
	if w := sessionTestRequest(mux, "PUT", "/api/admin/login-activity/retention", a, `{"keep":3}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := sessionTestRequest(mux, "GET", "/api/admin/login-activity/retention", a, ""); w.Code != 200 || w.Body.String() != "{\"keep\":3}\n" {
		t.Fatalf("global policy read: %d %s", w.Code, w.Body.String())
	}
	_, bobTotal, bobKeep, bobErr := s.st.listLoginHistory("bob", 20)
	if bobErr != nil || bobTotal != 3 || bobKeep != 3 {
		t.Fatalf("global pruning missed another account: %d %d %v", bobTotal, bobKeep, bobErr)
	}
	if s.currentActiveUser(reqWith(a)) != "alice" {
		t.Fatal("history cleanup revoked a session")
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE actor='alice' AND action=? AND target_type='login_history_retention'`, AuditLoginHistoryRetention); n != 1 {
		t.Fatalf("global retention change not audited: %d", n)
	}
	_, total, keep, err = s.st.listLoginHistory("alice", 20)
	if err != nil || total != 3 || keep != 3 {
		t.Fatalf("saved retention: %d %d %v", total, keep, err)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE actor='alice' AND action='auth.login'`); n != 110 {
		t.Fatalf("admin audit was pruned: %d", n)
	}
	if w := sessionTestRequest(mux, "PUT", "/api/admin/login-activity/retention", a, `{"keep":10}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, total, _, err = s.st.listLoginHistory("alice", 20)
	if err != nil || total != 3 {
		t.Fatal("raising the cap resurrected deleted history")
	}
	s.st.WriteAudit(historyEntry("alice"))
	_, total, _, err = s.st.listLoginHistory("alice", 20)
	if err != nil || total != 4 {
		t.Fatalf("new login not retained: %d %v", total, err)
	}
	_, total, _, err = s.st.listLoginHistory("bob", 20)
	if err != nil || total != 3 {
		t.Fatal("raising the global cap restored another account's deleted history")
	}
	for _, body := range []string{`{}`, `{"keep":null}`, `{"keep":0}`, `{"keep":-1}`, `{"keep":10001}`, `{"keep":1.5}`} {
		if w := sessionTestRequest(mux, "PUT", "/api/admin/login-activity/retention", a, body); w.Code != 400 {
			t.Fatalf("accepted invalid %s: %d", body, w.Code)
		}
	}
}

func TestLegacyHistoryBackfillRunsOnceAndUsesNewestIDs(t *testing.T) {
	st := newTestStore(t)
	st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	for i := 0; i < 5; i++ {
		st.exec(`INSERT INTO audit_log(at,actor,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,?,?,?)`, "2026-10-04T00:00:00Z", "alice", AuditLogin, "user", "alice", "{}", "")
	}
	st.SetSetting(setLoginHistoryKeep, "2")
	st.exec(`UPDATE users SET login_history_initialized=0 WHERE username='alice'`)
	items, total, _, err := st.listLoginHistory("alice", 20)
	if err != nil || total != 2 || items[0].ID <= items[1].ID {
		t.Fatalf("backfill: %+v %d %v", items, total, err)
	}
	st.exec(`DELETE FROM login_history WHERE username='alice'`)
	_, total, _, err = st.listLoginHistory("alice", 20)
	if err != nil || total != 0 {
		t.Fatal("cleaned history was backfilled again")
	}
	if err := st.DeleteUser("alice"); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM login_history WHERE username='alice'`); n != 0 {
		t.Fatal("deleted user's history survived")
	}
	st.UpsertUser(User{Username: "alice", PasswordHash: "new", Role: "user"})
	_, total, _, err = st.listLoginHistory("alice", 20)
	if err != nil || total != 0 {
		t.Fatal("new holder inherited the previous account's login history")
	}
}

func testConcurrentHistoryCap(t *testing.T, st *Store) {
	t.Helper()
	if err := st.UpsertUser(User{Username: "parallel-history", PasswordHash: "h", Role: "user"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(setLoginHistoryKeep, "7"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); st.WriteAudit(historyEntry("parallel-history")) }()
	}
	wg.Wait()
	_, total, keep, err := st.listLoginHistory("parallel-history", 20)
	if err != nil || total != 7 || keep != 7 {
		t.Fatalf("concurrent retention: %d %d %v", total, keep, err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE actor='parallel-history' AND action='auth.login'`); n != 50 {
		t.Fatalf("concurrent audit writes lost: %d", n)
	}
}

func TestConcurrentPersonalHistoryWritesRespectTheCap(t *testing.T) {
	testConcurrentHistoryCap(t, newTestStore(t))
}

func TestPostgresConcurrentPersonalHistoryWritesRespectTheCap(t *testing.T) {
	testConcurrentHistoryCap(t, pgStore(t))
}

func testGlobalPolicyDuringSignIns(t *testing.T, st *Store) {
	t.Helper()
	s := tenancyServer(t)
	s.st = st
	for _, user := range []string{"global-a", "global-b"} {
		if err := st.UpsertUser(User{Username: user, PasswordHash: "h", Role: "user"}); err != nil {
			t.Fatal(err)
		}
		// The retired per-user cap must no longer control sign-in writes.
		st.exec(`UPDATE users SET login_history_keep=1 WHERE username=?`, user)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st.WriteAudit(historyEntry("global-a"))
			st.WriteAudit(historyEntry("global-b"))
		}()
	}
	w := httptest.NewRecorder()
	s.apiLoginHistoryRetention(w, httptest.NewRequest("PUT", "/api/admin/login-activity/retention", strings.NewReader(`{"keep":3}`)), "admin")
	wg.Wait()
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, user := range []string{"global-a", "global-b"} {
		// Inspect storage before a read can enforce pruning: writers must respect the saved cap.
		if n := scalar[int](t, st, `SELECT COUNT(*) FROM login_history WHERE username=?`, user); n != 3 {
			t.Fatalf("%s exceeded saved global cap: %d", user, n)
		}
		if n := scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE actor=? AND action='auth.login'`, user); n != 20 {
			t.Fatalf("%s lost concurrent sign-ins: %d", user, n)
		}
	}
}

func TestGlobalHistoryPolicyDuringSignIns(t *testing.T) {
	testGlobalPolicyDuringSignIns(t, newTestStore(t))
}

func TestPostgresGlobalHistoryPolicyDuringSignIns(t *testing.T) {
	testGlobalPolicyDuringSignIns(t, pgStore(t))
}
