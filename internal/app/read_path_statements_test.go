package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
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

// Audit records stay durable: the report-read row is the ordinary audit insert, one statement, and no
// synchronous_commit setting is used for it, not even a statement-scoped one.
func TestReadPathReportReadAuditIsOneSynchronousInsert(t *testing.T) {
	f := newReadPathFixture(t)
	f.spy.mu.Lock()
	f.spy.tracing, f.spy.trace = true, nil
	f.spy.mu.Unlock()
	f.s.st.WriteReportReadAudit(AuditEntry{Actor: "reader", ActorOU: 7, Action: AuditReportRead, TargetType: "report", TargetID: "424242",
		Detail: `{"symbol":"600000"}`, IP: "203.0.113.9"})
	f.spy.mu.Lock()
	f.spy.tracing = false
	var statements []string
	for _, line := range f.spy.trace {
		if !strings.HasPrefix(line, "--") {
			statements = append(statements, line)
		}
	}
	f.spy.mu.Unlock()
	if len(statements) != 1 || !strings.Contains(statements[0], "INSERT INTO audit_log") {
		t.Fatalf("statements = %v", statements)
	}
	for _, s := range statements {
		if strings.Contains(strings.ToLower(s), "synchronous_commit") || strings.Contains(strings.ToLower(s), "set_config") {
			t.Fatalf("the audit insert changed the commit mode: %s", s)
		}
	}
	if got := scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id='424242' AND actor_ou=7`, AuditReportRead); got != 1 {
		t.Fatalf("%d rows", got)
	}
}

// The same on PostgreSQL: the row is whole, and neither the pooled connections nor the database see any
// synchronous_commit other than the server's own.
func TestReadPathReportReadAuditLeavesTheCommitModeAloneOnPostgres(t *testing.T) {
	st := pgStore(t)
	var before string
	if err := st.db.QueryRow(`SHOW synchronous_commit`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	st.WriteReportReadAudit(AuditEntry{Actor: "reader", ActorOU: 7, Action: AuditReportRead, TargetType: "report", TargetID: "424243",
		Detail: `{"symbol":"600000"}`, IP: "203.0.113.9"})
	t.Cleanup(func() { st.exec(`DELETE FROM audit_log WHERE target_id='424243' AND action=?`, AuditReportRead) })
	var actor, ip string
	var ou int64
	if err := st.queryRow(`SELECT actor,actor_ou,ip FROM audit_log WHERE target_id='424243' AND action=?`, AuditReportRead).Scan(&actor, &ou, &ip); err != nil {
		t.Fatal(err)
	}
	if actor != "reader" || ou != 7 || ip != "203.0.113.9" {
		t.Fatalf("row = %q %d %q", actor, ou, ip)
	}
	for i := 0; i < 20; i++ {
		var after string
		if err := st.db.QueryRow(`SHOW synchronous_commit`).Scan(&after); err != nil || after != before {
			t.Fatalf("synchronous_commit is %q, was %q: %v", after, before, err)
		}
	}
}

// One account snapshot per page load. The enrolment gate's policy, the viewer scope and the forecast access of
// one request are decided from the same account row and organisation tree, read once in one snapshot; the soak's
// trace showed the stock page loading them five times over (four statements each on PostgreSQL) and home twice.
func TestReadPathAccountSnapshotIsReadOncePerPage(t *testing.T) {
	f := newReadPathFixture(t)
	if _, _, err := f.s.st.UpsertReport(Rep{Symbol: "600010", Name: "Forecast Co", Date: "2026-11-13", RType: predTestRtype, Title: "covered", MD: "body"}); err != nil {
		t.Fatal(err)
	}
	const tree = `FROM user_groups$`
	for _, path := range []string{"/api/home", "/api/stock/600010", "/api/stock/600000"} {
		f.get(t, path) // warm the caches that are not under test
		code, statements := f.get(t, path)
		if code != http.StatusOK {
			t.Fatalf("%s: %d", path, code)
		}
		if n := countMatching(statements, tree); n != 1 {
			t.Fatalf("%s read the organisation tree %d times, want 1: %v", path, n, statements)
		}
	}
}

// The snapshot lives for one request: a policy change between two requests is seen by the second.
func TestReadPathAccountSnapshotIsNotSharedBetweenRequests(t *testing.T) {
	f := newReadPathFixture(t)
	_, first := f.get(t, "/api/home")
	_, second := f.get(t, "/api/home")
	if countMatching(first, `FROM user_groups$`) != 1 || countMatching(second, `FROM user_groups$`) != 1 {
		t.Fatalf("each request must read the tree itself: %v / %v", first, second)
	}
}

// Only a safe request (GET or HEAD) carries the snapshot, so a request that writes policy never decides from a
// snapshot taken before its write; and the snapshot answers only for the account it was made for.
func TestRequestAccountSnapshotOnlyForSafeRequestsAndItsOwnAccount(t *testing.T) {
	f := newReadPathFixture(t)
	user := f.session.username
	mustExec(t, f.s.st, `INSERT INTO users(username,password_hash,role,restricted) VALUES(?,?,?,1)`, "someone-else", "h", "user")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		r := withRequestAccount(httptest.NewRequest(method, "/x", nil), user)
		if requestAccountOf(r.Context(), user) != nil {
			t.Fatalf("%s carries an account snapshot", method)
		}
	}
	r := withRequestAccount(httptest.NewRequest(http.MethodGet, "/x", nil), user)
	if requestAccountOf(r.Context(), user) == nil {
		t.Fatal("a GET carries no account snapshot")
	}
	if requestAccountOf(r.Context(), "someone-else") != nil {
		t.Fatal("the snapshot answered for another account")
	}
	f.spy.mu.Lock()
	f.spy.tracing, f.spy.trace = true, nil
	f.spy.mu.Unlock()
	for i := 0; i < 3; i++ {
		_ = f.s.viewerScopeFor(r.Context(), user)
		_, _ = f.s.securitySettingsFor(r.Context(), user)
		_ = f.s.forecastAccessFor(r.Context(), user)
	}
	other := f.s.viewerScopeFor(r.Context(), "someone-else")
	f.spy.mu.Lock()
	n := countMatching(f.spy.trace, `FROM user_groups$`)
	f.spy.tracing = false
	f.spy.mu.Unlock()
	if n != 2 {
		t.Fatalf("tree reads = %d, want 1 for the request's account and 1 for the other", n)
	}
	if other == nil || !reflect.DeepEqual(other, f.s.viewerScope("someone-else")) {
		t.Fatalf("the other account's scope = %+v, want its own restricted scope %+v", other, f.s.viewerScope("someone-else"))
	}
}

// What the snapshot decides equals what each loader decides on its own, for the three kinds of account: an
// administrator and an unrestricted account are settled by the tree alone (one read), a restricted account's scope
// still comes from its own full snapshot, and is scoped, never widened.
func TestRequestAccountSnapshotDecidesAsTheLoadersDo(t *testing.T) {
	f := newReadPathFixture(t)
	mustExec(t, f.s.st, `INSERT INTO users(username,password_hash,role) VALUES(?,?,?)`, "an-admin", "h", "admin")
	mustExec(t, f.s.st, `INSERT INTO users(username,password_hash,role) VALUES(?,?,?)`, "a-reader", "h", "user")
	mustExec(t, f.s.st, `INSERT INTO users(username,password_hash,role,restricted) VALUES(?,?,?,1)`, "a-tenant", "h", "user")
	// An OU that grants every forecast, so the forecast comparison covers a granted scope too.
	ou, err := f.s.st.CreateUserGroup("forecasters", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.s.st, `UPDATE user_groups SET forecast_scope='all' WHERE id=?`, ou)
	mustExec(t, f.s.st, `INSERT INTO users(username,password_hash,role,group_id) VALUES(?,?,?,?)`, "a-forecaster", "h", "user", ou)
	if f.s.forecastAccess("a-forecaster").Scope != scopeAll {
		t.Fatalf("fixture: the forecaster's OU grants %+v", f.s.forecastAccess("a-forecaster"))
	}
	for _, c := range []struct {
		user      string
		scoped    bool
		treeReads int
	}{{"an-admin", false, 1}, {"a-reader", false, 1}, {"a-forecaster", false, 1}, {"a-tenant", true, 2}} {
		r := withRequestAccount(httptest.NewRequest(http.MethodGet, "/x", nil), c.user)
		f.spy.mu.Lock()
		f.spy.tracing, f.spy.trace = true, nil
		f.spy.mu.Unlock()
		scope := f.s.viewerScopeFor(r.Context(), c.user)
		settings, err := f.s.securitySettingsFor(r.Context(), c.user)
		forecast := f.s.forecastAccessFor(r.Context(), c.user)
		_ = f.s.viewerScopeFor(r.Context(), c.user)
		f.spy.mu.Lock()
		reads := countMatching(f.spy.trace, `FROM user_groups$`)
		f.spy.tracing = false
		f.spy.mu.Unlock()
		if reads != c.treeReads {
			t.Errorf("%s: tree reads = %d, want %d", c.user, reads, c.treeReads)
		}
		if (scope != nil) != c.scoped || !reflect.DeepEqual(scope, f.s.viewerScope(c.user)) {
			t.Errorf("%s: scope = %+v, want %+v", c.user, scope, f.s.viewerScope(c.user))
		}
		want, wantErr := f.s.st.SecuritySettings(c.user)
		if err != nil || wantErr != nil || !reflect.DeepEqual(settings, want) {
			t.Errorf("%s: settings = %+v (%v), want %+v (%v)", c.user, settings, err, want, wantErr)
		}
		if !reflect.DeepEqual(forecast, f.s.forecastAccess(c.user)) {
			t.Errorf("%s: forecast = %+v, want %+v", c.user, forecast, f.s.forecastAccess(c.user))
		}
	}
}
