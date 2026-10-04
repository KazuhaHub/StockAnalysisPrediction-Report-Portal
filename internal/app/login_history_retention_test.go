package app

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

func historyEntry(user string) AuditEntry {
	return AuditEntry{Actor: user, Action: AuditLogin, TargetType: "user", TargetID: user, At: "2026-10-04T00:00:00Z", IP: "198.51.100.1", Detail: `{"method":"password"}`}
}

func TestLoginHistoryRetentionDeletesPersonalRowsWithoutAuditOrSessionLoss(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.UpsertUser(User{Username: "bob", PasswordHash: "h", Role: "user"})
	for i := 0; i < 110; i++ {
		s.st.WriteAudit(historyEntry("alice"))
	}
	s.st.WriteAudit(historyEntry("bob"))
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
	if w := sessionTestRequest(mux, "PUT", "/api/me/login-activity/retention", a, `{"keep":3}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.currentActiveUser(reqWith(a)) != "alice" {
		t.Fatal("history cleanup revoked a session")
	}
	_, total, keep, err = s.st.listLoginHistory("alice", 20)
	if err != nil || total != 3 || keep != 3 {
		t.Fatalf("saved retention: %d %d %v", total, keep, err)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE actor='alice' AND action='auth.login'`); n != 110 {
		t.Fatalf("admin audit was pruned: %d", n)
	}
	if w := sessionTestRequest(mux, "PUT", "/api/me/login-activity/retention", a, `{"keep":10}`); w.Code != 200 {
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
	if err != nil || total != 1 {
		t.Fatal("another account's history changed")
	}
	for _, body := range []string{`{}`, `{"keep":null}`, `{"keep":0}`, `{"keep":-1}`, `{"keep":10001}`, `{"keep":1.5}`} {
		if w := sessionTestRequest(mux, "PUT", "/api/me/login-activity/retention", a, body); w.Code != 400 {
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
	st.exec(`UPDATE users SET login_history_keep=2,login_history_initialized=0 WHERE username='alice'`)
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
	if _, err := st.exec(`UPDATE users SET login_history_keep=7 WHERE username='parallel-history'`); err != nil {
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
