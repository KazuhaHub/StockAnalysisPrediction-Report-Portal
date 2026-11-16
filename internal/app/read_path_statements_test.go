package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// What a page load costs the database. On a host whose round trips are slow (the reference VM, a KVM guest),
// a page's latency is its statements times the round trip, and a synchronous commit adds a disk flush; the soak
// measured home at 13 PostgreSQL transactions and the stock page at one more with a write. These tests hold the
// read paths to their budgets (doc 171).

type readPathFixture struct {
	s       *Server
	spy     *spyDriver
	mux     *http.ServeMux
	cookie  string
	session platformActorSession
}

func newReadPathFixture(t *testing.T) *readPathFixture {
	t.Helper()
	st, spy := newSpyTestStore(t)
	s, session := platformActorTestServerWithStore(t, st)
	s.names = LoadNames(t.TempDir(), st)
	session.expires = time.Unix(time.Now().Add(time.Hour).Unix(), 0)
	platformSeedActorSession(t, s.st, session)
	if _, _, err := s.st.UpsertReport(Rep{Symbol: "600000", Name: "Soak Co", Date: "2026-11-13", RType: "x", Title: "soak report", MD: "body"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return &readPathFixture{s: s, spy: spy, mux: mux, session: session,
		cookie: s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())}
}

// get serves one authenticated GET and returns the statements it issued (connection bookkeeping removed).
func (f *readPathFixture) get(t *testing.T, path string) (int, []string) {
	t.Helper()
	f.spy.mu.Lock()
	f.spy.tracing, f.spy.trace = true, nil
	f.spy.mu.Unlock()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(&http.Cookie{Name: cookieName, Value: f.cookie})
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	f.spy.mu.Lock()
	defer f.spy.mu.Unlock()
	f.spy.tracing = false
	var out []string
	for _, line := range f.spy.trace {
		if !strings.HasPrefix(line, "--") {
			out = append(out, strings.Join(strings.Fields(line), " "))
		}
	}
	return w.Code, out
}

func countMatching(statements []string, pattern string) int {
	re := regexp.MustCompile(pattern)
	n := 0
	for _, s := range statements {
		if re.MatchString(s) {
			n++
		}
	}
	return n
}

// The activity stamp of a session is due once an interval, and the answer is remembered in memory, so the
// requests in between do not even read it.
func TestReadPathSessionActivityIsNotReadOnEveryRequest(t *testing.T) {
	f := newReadPathFixture(t)
	if code, first := f.get(t, "/api/home"); code != 200 || countMatching(first, `^SELECT last_seen FROM user_sessions`) != 1 {
		t.Fatalf("first request: %d, %d reads of the stamp", code, countMatching(first, `^SELECT last_seen FROM user_sessions`))
	}
	for _, path := range []string{"/api/home", "/api/stock/600000", "/api/home"} {
		if _, later := f.get(t, path); countMatching(later, `^SELECT last_seen FROM user_sessions`) != 0 {
			t.Fatalf("%s read the stamp again: %v", path, later)
		}
	}
	// A restart, or a stamp older than the interval, reads and stamps again.
	f.s.sessionStamps.reset()
	mustExec(t, f.s.st, `UPDATE user_sessions SET last_seen=1 WHERE id=?`, f.session.sessionID)
	if _, again := f.get(t, "/api/home"); countMatching(again, `^SELECT last_seen FROM user_sessions`) != 1 || countMatching(again, `^UPDATE user_sessions SET last_seen`) != 1 {
		t.Fatalf("a stale session was not stamped: %v", again)
	}
	if got := scalar[int64](t, f.s.st, `SELECT last_seen FROM user_sessions WHERE id=?`, f.session.sessionID); got < time.Now().Unix()-5 {
		t.Fatalf("last_seen = %d", got)
	}
}

func TestReadPathFacetsAreOneStatementAndEqualTheThreeListsTheyReplace(t *testing.T) {
	f := newReadPathFixture(t)
	for i, v := range []struct{ symbol, rtype, version string }{{"600001", "alpha", "internal"}, {"600002", "beta", "external"}, {"600003", "alpha", ""}} {
		if _, _, err := f.s.st.UpsertReport(Rep{Symbol: v.symbol, Name: "N", Date: "2026-11-1" + string(rune('0'+i)), RType: v.rtype, Title: "t" + v.symbol, MD: "b", Version: v.version}); err != nil {
			t.Fatal(err)
		}
	}
	scopes := map[string]*ownerScope{"unscoped": nil, "deny all": {denyAll: true}}
	for name, sc := range scopes {
		types, kinds, versions := f.s.st.ReportFacets(sc)
		want := func(got, expected []string, what string) {
			if strings.Join(got, "|") != strings.Join(expected, "|") {
				t.Errorf("%s %s = %v, want %v", name, what, got, expected)
			}
		}
		want(types, f.s.st.NewTypes(sc), "types")
		want(kinds, f.s.st.ReportKinds(sc), "kinds")
		want(versions, f.s.st.ReportVersionsPresent(sc), "versions")
	}
	if types, _, _ := f.s.st.ReportFacets(nil); len(types) < 3 {
		t.Fatalf("the facets are empty: %v", types)
	}
	_, statements := f.get(t, "/api/home")
	if n := countMatching(statements, `SELECT DISTINCT`); n != 0 {
		t.Fatalf("home still issued %d DISTINCT statements: %v", n, statements)
	}
}

// The entry buttons, their groups and the kind colours are admin configuration read on every home load. They are
// kept in memory and dropped by every write to them, with a short time limit behind that for another process.
func TestReadPathHomeConfigurationIsCachedAndEveryWriterDropsIt(t *testing.T) {
	f := newReadPathFixture(t)
	f.get(t, "/api/home") // warm
	_, warm := f.get(t, "/api/home")
	if n := countMatching(warm, `FROM links|FROM link_groups|FROM kind_config`); n != 0 {
		t.Fatalf("a warm home read its configuration %d times: %v", n, warm)
	}
	home := func() string {
		r := httptest.NewRequest(http.MethodGet, "/api/home", nil)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: f.cookie})
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		return w.Body.String()
	}
	if err := f.s.st.AddLink("Fresh button", "https://example.test/x", "", true, 0, 0); err != nil {
		t.Fatal(err)
	}
	id := scalar[int64](t, f.s.st, `SELECT id FROM links WHERE label='Fresh button'`)
	if !strings.Contains(home(), "Fresh button") {
		t.Fatal("a new link did not show at once")
	}
	if err := f.s.st.DeleteLink(id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(home(), "Fresh button") {
		t.Fatal("a deleted link still shows")
	}
	if err := f.s.st.SetKindColor("预测", "gold"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(home(), `"gold"`) {
		t.Fatal("a new kind colour did not show at once")
	}
	// The time limit backs the in-memory drop for a write made by another process.
	mustExec(t, f.s.st, `UPDATE kind_config SET color='cyan' WHERE kind='预测'`)
	f.s.st.homeConfigNow = func() time.Time { return time.Now().Add(homeConfigTTL + time.Second) }
	if !strings.Contains(home(), `"cyan"`) {
		t.Fatal("a write by another process was not seen after the time limit")
	}
}

// On PostgreSQL the report-read audit row is one statement whose commit does not wait for the WAL flush; the
// row is whole and visible at once and the setting never leaks to the pooled connection.
func TestReadPathReportReadAuditIsOneAsyncCommitStatementOnPostgres(t *testing.T) {
	st := pgStore(t)
	entry := AuditEntry{Actor: "reader", ActorOU: 7, Action: AuditReportRead, TargetType: "report", TargetID: "424242",
		Detail: `{"symbol":"600000"}`, IP: "203.0.113.9"}
	st.WriteReportReadAudit(entry)
	t.Cleanup(func() { st.exec(`DELETE FROM audit_log WHERE target_id='424242' AND action=?`, AuditReportRead) })
	var actor, action, target, detail, ip, at string
	var ou int64
	if err := st.queryRow(`SELECT actor,actor_ou,action,target_id,detail,ip,at FROM audit_log WHERE target_id='424242' AND action=?`, AuditReportRead).
		Scan(&actor, &ou, &action, &target, &detail, &ip, &at); err != nil {
		t.Fatal(err)
	}
	if actor != "reader" || ou != 7 || target != "424242" || detail != `{"symbol":"600000"}` || ip != "203.0.113.9" || at == "" {
		t.Fatalf("row = %q %d %q %q %q %q", actor, ou, target, detail, ip, at)
	}
	for i := 0; i < 20; i++ { // every pooled connection
		var setting string
		if err := st.db.QueryRow(`SHOW synchronous_commit`).Scan(&setting); err != nil || setting != "on" {
			t.Fatalf("synchronous_commit leaked to the pool: %q %v", setting, err)
		}
	}
}
