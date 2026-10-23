package app

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// Business-path acceptance for purge ownership evidence (ADR 0039): a retention pass snapshots who
// owned the sets it deletes, and deleting an OU carries that evidence to wherever the OU's sets go.
// The primitives are tested in forecast_purge_owners_test.go; these tests drive the real callers.

// retentionCutoff separates the months these tests purge (all before it) from the live month (after).
var retentionCutoff = time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)

const retentionLiveMonth = "2025-10"

var retentionTables = []string{"prediction_marks", "prediction_claims", "prediction_sets",
	"prediction_purges", "prediction_purge_owners", "users", "user_groups"}

// retentionServer is a server over st with the routes wired, the evidence table present and empty,
// and the Default OU in place.
func retentionServer(t *testing.T, st *Store) *Server {
	t.Helper()
	ensurePurgeEvidenceTable(t, st)
	if _, err := st.exec(`DELETE FROM prediction_purge_owners`); err != nil {
		t.Fatal(err)
	}
	st.EnsureDefaultGroup()
	if err := st.EnsureDefaultForecastScope(); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st, cfg: &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"},
		loginThr: newLoginThrottle(), v1Rate: newRateLimiter()}
	s.forecastEval.now = func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) }
	return s
}

// retentionDrivers runs one scenario on SQLite and, when configured, on Postgres.
func retentionDrivers(t *testing.T, run func(t *testing.T, s *Server)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, retentionServer(t, newTestStore(t))) })
	t.Run("postgres", func(t *testing.T) { run(t, retentionServer(t, pgStore(t, retentionTables...))) })
}

// seedRetentionSet inserts one orphaned, settled set in a month, attributed to ou (0 leaves it
// unattributed). It is purgeable once the cutoff has passed the month.
func seedRetentionSet(t *testing.T, st *Store, cohort string, ou int64, tag string) int64 {
	t.Helper()
	var owner any
	if ou != 0 {
		owner = ou
	}
	id, err := st.insertID(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256, attributed_ou)
		VALUES(NULL,?,?,?,?,?,?,?)`, predTestRtype, cohort+"-05T14:40:12Z", cohort+"-05", cohort, predStateAccepted,
		"retention-"+cohort+"-"+tag, owner)
	if err != nil {
		t.Fatalf("seed set in %s: %v", cohort, err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at) VALUES(?,'d5','resolved',?)`,
		id, cohort+"-28T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	return id
}

// evidence is the ownership table as sorted "cohort/ou" strings, so a test states it in one line.
func evidence(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.query(`SELECT cohort, attributed_ou FROM prediction_purge_owners ORDER BY cohort, attributed_ou`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var cohort string
		var ou int64
		if err := rows.Scan(&cohort, &ou); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s/%d", cohort, ou))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func evidenceSet(entries ...string) []string {
	if entries == nil {
		return []string{}
	}
	sort.Strings(entries)
	return entries
}

func ev(cohort string, ou int64) string { return fmt.Sprintf("%s/%d", cohort, ou) }

// failStatements makes every INSERT (or DELETE) on table abort until the test ends, so a test can
// prove that what ran earlier in the same transaction was rolled back with it.
func failStatements(t *testing.T, st *Store, op, table string) {
	t.Helper()
	name := "rp_test_fail_" + table
	if st.driver == "postgres" {
		for _, q := range []string{
			`CREATE OR REPLACE FUNCTION rp_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$`,
			fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s FOR EACH STATEMENT EXECUTE FUNCTION rp_test_fail()`, name, op, table),
		} {
			if _, err := st.exec(q); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = st.exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table))
			_, _ = st.exec(`DROP FUNCTION IF EXISTS rp_test_fail()`)
		})
		return
	}
	if _, err := st.exec(fmt.Sprintf(`CREATE TRIGGER %s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT, 'injected failure'); END`, name, op, table)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = st.exec(`DROP TRIGGER IF EXISTS ` + name) })
}

func queryCount(t *testing.T, st *Store, q string, args ...any) int {
	t.Helper()
	return scalar[int](t, st, q, args...)
}

// ---------- retention ----------

func TestRetentionSnapshotsOwnershipEvidenceBeforeItDeletesSets(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		a := seedOU(t, st, 0, scopeOwn)
		c := seedOU(t, st, a, nil) // a descendant of a
		b := seedOU(t, st, 0, scopeOwn)

		// 2025-03: two owners and a set nobody owns. 2025-04: one owner, a duplicate, and a set
		// attributed to an OU that no longer exists, which proves nothing about anyone.
		seedRetentionSet(t, st, "2025-03", a, "1")
		seedRetentionSet(t, st, "2025-03", a, "2")
		seedRetentionSet(t, st, "2025-03", c, "3")
		seedRetentionSet(t, st, "2025-03", 0, "4")
		seedRetentionSet(t, st, "2025-04", b, "1")
		seedRetentionSet(t, st, "2025-04", b, "2")
		seedRetentionSet(t, st, "2025-04", 987654, "3")
		// 2025-05 holds an unsettled set, so the whole month is protected and nothing is captured.
		kept := seedRetentionSet(t, st, "2025-05", a, "1")
		if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status) VALUES(?,'d20','pending')`, kept); err != nil {
			t.Fatal(err)
		}
		seedRetentionSet(t, st, "2025-05", b, "2")

		n, err := st.DeletePredictionsBefore(retentionCutoff)
		if err != nil || n != 7 {
			t.Fatalf("retention deleted %d sets, %v; want 7", n, err)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", a), ev("2025-03", c), ev("2025-04", b)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence = %v; want %v", got, wantRows)
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_purges`); got != 2 {
			t.Fatalf("purge markers = %d; want 2", got)
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_sets WHERE cohort='2025-05'`); got != 2 {
			t.Fatalf("the protected month lost sets: %d left", got)
		}
		// A pass with nothing eligible leaves the evidence alone.
		if n, err := st.DeletePredictionsBefore(retentionCutoff); err != nil || n != 0 {
			t.Fatalf("second pass deleted %d, %v", n, err)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", a), ev("2025-03", c), ev("2025-04", b)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("a no-op pass changed the evidence: %v", got)
		}
	})
}

func TestRetentionEvidenceAndDeletionShareOneTransaction(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		a := seedOU(t, st, 0, scopeOwn)
		seedRetentionSet(t, st, "2025-03", a, "1")
		seedRetentionSet(t, st, "2025-04", a, "2")
		failStatements(t, st, "INSERT", "prediction_purges")

		if _, err := st.DeletePredictionsBefore(retentionCutoff); err == nil {
			t.Fatal("the injected failure did not surface")
		}
		if got := evidence(t, st); len(got) != 0 {
			t.Fatalf("evidence survived a rolled-back pass: %v", got)
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_sets`); got != 2 {
			t.Fatalf("sets were deleted by a rolled-back pass: %d left", got)
		}
	})
}

// ---------- OU deletion ----------

func TestDeletingAnOUCarriesPurgeEvidenceToTheParent(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		def := st.DefaultGroupID()
		parent := seedOU(t, st, 0, scopeOwn)
		child := seedOU(t, st, parent, nil)
		quiet := seedOU(t, st, parent, nil) // owns nothing and has no evidence
		root := seedOU(t, st, 0, scopeOwn)

		// 2025-03: child only (a plain transfer). 2025-04: parent and child (a collision to deduplicate).
		// 2025-05: the root alone (a root's evidence goes to the Default OU).
		seedRetentionSet(t, st, "2025-03", child, "1")
		seedRetentionSet(t, st, "2025-04", parent, "1")
		seedRetentionSet(t, st, "2025-04", child, "2")
		seedRetentionSet(t, st, "2025-05", root, "1")
		if _, err := st.DeletePredictionsBefore(retentionCutoff); err != nil {
			t.Fatal(err)
		}
		// An OU can hold evidence while holding no set at all: retention already took them.
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_sets WHERE attributed_ou IN (?,?,?)`, child, root, quiet); got != 0 {
			t.Fatalf("fixture still holds %d sets", got)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", child), ev("2025-04", parent), ev("2025-04", child), ev("2025-05", root)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence before deletion = %v; want %v", got, wantRows)
		}

		if _, target, err := st.DeleteUserGroup(child); err != nil || target != parent {
			t.Fatalf("delete child: target %d, %v; want %d", target, err, parent)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", parent), ev("2025-04", parent), ev("2025-05", root)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence after deleting the child = %v; want %v", got, wantRows)
		}
		if _, target, err := st.DeleteUserGroup(root); err != nil || target != def {
			t.Fatalf("delete root: target %d, %v; want the Default OU %d", target, err, def)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", parent), ev("2025-04", parent), ev("2025-05", def)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence after deleting the root = %v; want %v", got, wantRows)
		}
		// An OU with no evidence changes nothing.
		if _, _, err := st.DeleteUserGroup(quiet); err != nil {
			t.Fatal(err)
		}
		if got := evidence(t, st); len(got) != 3 {
			t.Fatalf("deleting an OU with no evidence changed the table: %v", got)
		}
		// No evidence may name an OU that is gone: its id could be handed to a new one.
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_purge_owners o
			WHERE NOT EXISTS (SELECT 1 FROM user_groups g WHERE g.id=o.attributed_ou)`); got != 0 {
			t.Fatalf("%d evidence rows name a deleted OU", got)
		}
	})
}

func TestDeletingAnOUMovesItsSetsAndItsEvidenceTogether(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		parent := seedOU(t, st, 0, scopeOwn)
		child := seedOU(t, st, parent, nil)
		seedRetentionSet(t, st, "2025-03", child, "1")
		if _, err := st.DeletePredictionsBefore(retentionCutoff); err != nil {
			t.Fatal(err)
		}
		// A set written after the purge, in the same OU: it moves to the parent with the evidence.
		live := seedRetentionSet(t, st, retentionLiveMonth, child, "live")

		moved, target, err := st.DeleteUserGroup(child)
		if err != nil || moved != 1 || target != parent {
			t.Fatalf("delete: moved %d to %d, %v", moved, target, err)
		}
		if got := attributedOU(t, st, live); !got.Valid || got.Int64 != parent {
			t.Fatalf("set attribution = %v; want %d", got, parent)
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", parent)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence = %v; want %v", got, wantRows)
		}
	})
}

func TestDeletingAnOURollsBackItsEvidenceTransferWithTheRest(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		parent := seedOU(t, st, 0, scopeOwn)
		child := seedOU(t, st, parent, nil)
		seedRetentionSet(t, st, "2025-03", child, "1")
		if _, err := st.DeletePredictionsBefore(retentionCutoff); err != nil {
			t.Fatal(err)
		}
		live := seedRetentionSet(t, st, retentionLiveMonth, child, "live")
		failStatements(t, st, "DELETE", "user_groups")

		if _, _, err := st.DeleteUserGroup(child); err == nil {
			t.Fatal("the injected failure did not surface")
		}
		if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", child)); !reflect.DeepEqual(got, wantRows) {
			t.Fatalf("evidence moved without the OU being deleted: %v", got)
		}
		if got := attributedOU(t, st, live); !got.Valid || got.Int64 != child {
			t.Fatalf("set attribution moved without the OU being deleted: %v", got)
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM user_groups WHERE id=?`, child); got != 1 {
			t.Fatal("the OU was deleted despite the failure")
		}
	})
}

// ---------- the reader, through the real mux ----------

// retentionRoutes is every route of the server on one mux.
func retentionRoutes(s *Server) *http.ServeMux {
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return mux
}

func serveSession(t *testing.T, s *Server, mux *http.ServeMux, user, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	carrier := sessionForPath(t, s, user, "GET /api/home", "/api/home")
	r := httptest.NewRequest(method, path, nil)
	for _, c := range carrier.Cookies() {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// purgedMonths is what a reader's monthly record says retention emptied, by label.
func purgedMonths(t *testing.T, s *Server, mux *http.ServeMux, user string) []string {
	t.Helper()
	w := serveSession(t, s, mux, user, "GET", "/api/forecast/groups")
	if w.Code != http.StatusOK {
		t.Fatalf("%s groups: %d %s", user, w.Code, w.Body.String())
	}
	return purgedMonthsOf(decodeReaderGroups(t, w))
}

func decodeReaderGroups(t *testing.T, w *httptest.ResponseRecorder) forecastReaderGroupsResponse {
	t.Helper()
	var out forecastReaderGroupsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func readerMonths(t *testing.T, s *Server, mux *http.ServeMux, user string) map[string]forecastReaderGroup {
	t.Helper()
	w := serveSession(t, s, mux, user, "GET", "/api/forecast/groups")
	if w.Code != http.StatusOK {
		t.Fatalf("%s groups: %d %s", user, w.Code, w.Body.String())
	}
	return readerGroupLabels(decodeReaderGroups(t, w))
}

// seedRetentionLive adds a mature-capable live month of n sets attributed to ou, settled after the cutoff.
func seedRetentionLive(t *testing.T, s *Server, month string, n int, ou int64) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := seedGroupSet(t, s, gsSeed{
			Symbol: symbolFor(i), MadeOn: month + "-10",
			S0Fen: ip(10000), TerminalFen: ip(int64(11000 + i*10)), EntryFen: ip(10500),
			BearFen: ip(8000), BullFen: ip(14000), PProfitBp: bp(int32(6000 + i%10)),
			Marks: map[string]gsMark{
				"d5": {Status: markResolved, Close: ip(int64(10600 + i*10)), Low: ip(10300),
					RetBp: bp(int32(200 + i*5)), BenchRetBp: bp(100)},
			},
		})
		if _, err := s.st.exec(`UPDATE prediction_sets SET attributed_ou=? WHERE id=?`, ou, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.st.exec(`UPDATE prediction_marks SET resolved_at=? WHERE set_id=?`, month+"-28T00:00:00Z", id); err != nil {
			t.Fatal(err)
		}
	}
}

func placeReader(t *testing.T, s *Server, user string, ou int64) {
	t.Helper()
	mustAddUser(t, s, user, false)
	if ou != 0 {
		if _, err := s.st.exec(`UPDATE users SET group_id=? WHERE username=?`, ou, user); err != nil {
			t.Fatal(err)
		}
	}
}

// After a real retention pass an own reader is told about exactly the purged months its current
// subtree owned sets in; another OU's months, and months nobody can prove, are ordinary empty months.
func TestAnOwnReaderLearnsOnlyItsSubtreesPurgedMonthsAfterARetentionPass(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		p := seedOU(t, st, 0, scopeOwn)
		c := seedOU(t, st, p, nil) // inherits own from p
		u := seedOU(t, st, 0, scopeOwn)
		all := seedOU(t, st, 0, scopeAll)
		placeReader(t, s, "reader-p", p)
		placeReader(t, s, "reader-c", c)
		placeReader(t, s, "reader-u", u)
		placeReader(t, s, "reader-all", all)

		for i := 0; i < 3; i++ {
			seedRetentionSet(t, st, "2025-03", u, fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-05", c, fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-06", p, fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-06", u, "u"+fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-08", 0, fmt.Sprint(i)) // nobody's: a marker no one can prove
			seedRetentionSet(t, st, "2025-09", u, fmt.Sprint(i))
		}
		seedRetentionLive(t, s, retentionLiveMonth, 35, p)

		if n, err := st.DeletePredictionsBefore(retentionCutoff); err != nil || n != 18 {
			t.Fatalf("retention deleted %d sets, %v; want 18", n, err)
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_sets WHERE cohort=?`, retentionLiveMonth); got != 35 {
			t.Fatalf("the live month lost sets: %d left", got)
		}

		mux := retentionRoutes(s)
		for user, wantMonths := range map[string][]string{
			"reader-p":   {"2025-05", "2025-06"},
			"reader-c":   {"2025-05"},
			"reader-u":   {"2025-03", "2025-06", "2025-09"},
			"reader-all": {"2025-03", "2025-05", "2025-06", "2025-08", "2025-09"},
		} {
			if got := purgedMonths(t, s, mux, user); !reflect.DeepEqual(got, wantMonths) {
				t.Errorf("%s is told about %v; want %v", user, got, wantMonths)
			}
		}
		// A month inside a reader's calendar that retention emptied, but which it may not be told
		// about, looks like any month the reader simply holds no sets in.
		months := readerMonths(t, s, mux, "reader-p")
		for _, month := range []string{"2025-08", "2025-09"} {
			requireOrdinaryEmpty(t, "reader-p", months, month, "2025-12")
		}
		months = readerMonths(t, s, mux, "reader-c")
		for _, month := range []string{"2025-06", "2025-08", "2025-09"} {
			requireOrdinaryEmpty(t, "reader-c", months, month, "2025-12")
		}
		months = readerMonths(t, s, mux, "reader-u")
		for _, month := range []string{"2025-05", "2025-08"} {
			requireOrdinaryEmpty(t, "reader-u", months, month, "2025-12")
		}
	})
}

// Deleting an OU through the admin route keeps the history of its purged months with the OUs that
// now own its sets: the parent for a child, the Default OU for a root.
func TestDeletingAnOUKeepsItsPurgedMonthsVisibleToTheOUsThatInheritItsSets(t *testing.T) {
	retentionDrivers(t, func(t *testing.T, s *Server) {
		st := s.st
		def := st.DefaultGroupID()
		p := seedOU(t, st, 0, scopeOwn)
		c := seedOU(t, st, p, nil)
		d := seedOU(t, st, p, nil)
		u := seedOU(t, st, 0, scopeOwn)
		r := seedOU(t, st, 0, scopeOwn)
		placeReader(t, s, "reader-p", p)
		placeReader(t, s, "reader-c", c)
		placeReader(t, s, "reader-u", u)
		placeReader(t, s, "reader-def", 0) // no primary group: the Default OU
		mustAddUser(t, s, "admin", true)

		// 2025-05: c alone. 2025-06: p, u and d together (the parent already holds evidence).
		// 2025-07: the root r alone.
		for i := 0; i < 3; i++ {
			seedRetentionSet(t, st, "2025-05", c, fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-06", p, fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-06", u, "u"+fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-06", d, "d"+fmt.Sprint(i))
			seedRetentionSet(t, st, "2025-07", r, fmt.Sprint(i))
		}
		seedRetentionLive(t, s, retentionLiveMonth, 35, p)
		if _, err := st.DeletePredictionsBefore(retentionCutoff); err != nil {
			t.Fatal(err)
		}

		mux := retentionRoutes(s)
		told := func(user string, months ...string) {
			t.Helper()
			if months == nil {
				months = []string{}
			}
			if got := purgedMonths(t, s, mux, user); !reflect.DeepEqual(got, months) {
				t.Errorf("%s is told about %v; want %v", user, got, months)
			}
		}
		told("reader-p", "2025-05", "2025-06")
		told("reader-c", "2025-05")
		told("reader-u", "2025-06")

		// c and d are deleted: their evidence is carried to p, and the collision on 2025-06 is absorbed.
		for _, ou := range []int64{c, d} {
			if w := serveSession(t, s, mux, "admin", "DELETE", "/api/admin/groups/"+itoa64(ou)); w.Code != http.StatusOK {
				t.Fatalf("delete OU %d: %d %s", ou, w.Code, w.Body.String())
			}
		}
		told("reader-p", "2025-05", "2025-06")
		told("reader-u", "2025-06")
		if w := serveSession(t, s, mux, "reader-c", "GET", "/api/forecast/groups"); w.Code != http.StatusForbidden || !hasCode(w.Body.Bytes(), "forecast_unavailable") {
			t.Errorf("a reader whose OU was deleted falls to the Default OU, which reads none: %d %s", w.Code, w.Body.String())
		}
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_purge_owners WHERE cohort='2025-06' AND attributed_ou=?`, p); got != 1 {
			t.Errorf("the collision on 2025-06 left %d rows for the parent", got)
		}

		// The root r is deleted: its evidence goes to the Default OU, as its sets do. The Default OU
		// is made readable only now, so the first read shows it held nothing of r's before.
		if _, err := st.exec(`UPDATE user_groups SET forecast_scope=? WHERE id=?`, scopeOwn, def); err != nil {
			t.Fatal(err)
		}
		told("reader-def")
		if w := serveSession(t, s, mux, "admin", "DELETE", "/api/admin/groups/"+itoa64(r)); w.Code != http.StatusOK {
			t.Fatalf("delete root: %d %s", w.Code, w.Body.String())
		}
		told("reader-def", "2025-07")
		told("reader-p", "2025-05", "2025-06")
		told("reader-u", "2025-06")
		if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_purge_owners o
			WHERE NOT EXISTS (SELECT 1 FROM user_groups g WHERE g.id=o.attributed_ou)`); got != 0 {
			t.Errorf("%d evidence rows name a deleted OU", got)
		}
	})
}

func purgedMonthsOf(resp forecastReaderGroupsResponse) []string {
	out := []string{}
	for _, g := range resp.Groups {
		if g.PurgedAt != "" {
			out = append(out, g.Label)
		}
	}
	sort.Strings(out)
	return out
}

// ---------- the single-connection rule ----------

// On SQLite a pool call made while a transaction is open waits for the only connection, which the
// transaction holds. Two seconds turns that deadlock into a failure instead of a hung test run.
func TestPurgeEvidenceWritesNeverCallThePoolInsideATransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	st := newTestStore(t)
	retentionServer(t, st)
	parent := seedOU(t, st, 0, scopeOwn)
	child := seedOU(t, st, parent, nil)
	seedRetentionSet(t, st, "2025-03", child, "1")
	seedRetentionSet(t, st, "2025-03", child, "2")
	seedRetentionSet(t, st, "2025-04", parent, "1")
	live := seedRetentionSet(t, st, retentionLiveMonth, child, "live")

	within := func(name string, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() { fn(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			// The leaked call still waits for the only connection, which the transaction holds, and
			// the cleanups registered by the fixtures use that connection too. Closing the store
			// fails every waiter, so the run reports this failure instead of hanging until the
			// package timeout and aborting every test after it.
			_ = st.Close()
			t.Fatalf("%s deadlocked: a pool call inside the transaction", name)
		}
	}
	var retained int64
	var retentionErr error
	within("retention", func() { retained, retentionErr = st.DeletePredictionsBefore(retentionCutoff) })
	if retentionErr != nil || retained != 3 {
		t.Fatalf("retention deleted %d, %v", retained, retentionErr)
	}
	if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", child), ev("2025-04", parent)); !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("evidence = %v; want %v", got, wantRows)
	}
	var moved, target int64
	var deleteErr error
	within("OU deletion", func() { moved, target, deleteErr = st.DeleteUserGroup(child) })
	if deleteErr != nil || moved != 1 || target != parent {
		t.Fatalf("OU deletion: moved %d to %d, %v", moved, target, deleteErr)
	}
	if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", parent), ev("2025-04", parent)); !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("evidence after the deletion = %v; want %v", got, wantRows)
	}
	if got := attributedOU(t, st, live); !got.Valid || got.Int64 != parent {
		t.Fatalf("the live set = %v; want %d", got, parent)
	}
}

// ---------- Postgres: the two business transactions serialize ----------

// The lock views are cluster wide and other test runs share the cluster, so every observation below
// is restricted to this test's own database.
const (
	ownDatabaseLocks = `FROM pg_locks WHERE locktype='advisory' AND classid=1380994896 AND objid=2
		AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`
	// ownershipLockQueued: some backend is waiting for the ownership advisory lock.
	ownershipLockQueued = `SELECT COUNT(*) ` + ownDatabaseLocks + ` AND NOT granted`
	// ownershipLockHeld: some backend holds it.
	ownershipLockHeld = `SELECT COUNT(*) ` + ownDatabaseLocks + ` AND granted`
	// blockedOnPrefix counts backends of this database waiting on a row lock in a statement that
	// matches the LIKE pattern appended to it.
	blockedOnPrefix = `SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database()
		AND wait_event_type='Lock' AND query LIKE `
)

func waitUntil(t *testing.T, st *Store, what, query string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if scalar[int](t, st, query) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type serializeFixture struct {
	st                  *Store
	parent, doomed, kid int64
}

// newSerializeFixture builds the shape both orders use: parent > doomed > kid, with two purgeable
// months. 2025-03 belongs to doomed alone; 2025-04 to parent and doomed (a collision to absorb).
// Four sets in all, three of them doomed's.
func newSerializeFixture(t *testing.T) (*serializeFixture, []int64) {
	t.Helper()
	st := retentionServer(t, pgStore(t, retentionTables...)).st
	f := &serializeFixture{st: st}
	f.parent = seedOU(t, st, 0, scopeOwn)
	f.doomed = seedOU(t, st, f.parent, nil)
	f.kid = seedOU(t, st, f.doomed, nil)
	ids := []int64{
		seedRetentionSet(t, st, "2025-03", f.doomed, "1"),
		seedRetentionSet(t, st, "2025-03", f.doomed, "2"),
		seedRetentionSet(t, st, "2025-04", f.parent, "1"),
		seedRetentionSet(t, st, "2025-04", f.doomed, "2"),
	}
	return f, ids
}

type retentionResult struct {
	deleted int64
	err     error
}

type deletionResult struct {
	moved, target int64
	err           error
}

// requireSerialized checks the end state that only a serialized pair of transactions can produce.
func (f *serializeFixture) requireSerialized(t *testing.T, retention retentionResult, deletion deletionResult) {
	t.Helper()
	if retention.err != nil || retention.deleted != 4 {
		t.Fatalf("retention deleted %d sets, %v; want 4", retention.deleted, retention.err)
	}
	if deletion.err != nil || deletion.target != f.parent {
		t.Fatalf("OU deletion: to %d, %v; want %d", deletion.target, deletion.err, f.parent)
	}
	if got, wantRows := evidence(t, f.st), evidenceSet(ev("2025-03", f.parent), ev("2025-04", f.parent)); !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("evidence = %v; want %v. Evidence naming the deleted OU %d was lost to a race", got, wantRows, f.doomed)
	}
	if got := queryCount(t, f.st, `SELECT COUNT(*) FROM prediction_purge_owners o
		WHERE NOT EXISTS (SELECT 1 FROM user_groups g WHERE g.id=o.attributed_ou)`); got != 0 {
		t.Fatalf("%d evidence rows name an OU that no longer exists, so its id could be reused", got)
	}
	if got := queryCount(t, f.st, `SELECT COUNT(*) FROM user_groups WHERE id=?`, f.doomed); got != 0 {
		t.Fatal("the OU was not deleted")
	}
	if got := queryCount(t, f.st, `SELECT COUNT(*) FROM user_groups WHERE id=? AND parent_id=?`, f.kid, f.parent); got != 1 {
		t.Fatal("the grandchild was not moved to the parent")
	}
	if got := queryCount(t, f.st, `SELECT COUNT(*) FROM prediction_sets`); got != 0 {
		t.Fatalf("%d sets survived the pass", got)
	}
	if got := queryCount(t, f.st, `SELECT COUNT(*) FROM prediction_purges`); got != 2 {
		t.Fatalf("purge markers = %d; want 2", got)
	}
}

func TestPostgresRetentionAndOUDeletionSerializeOnTheOwnershipLock(t *testing.T) {
	// Retention holds the lock, blocked on a row that only this test can release; the OU deletion
	// arrives meanwhile. Without the lock it would read the sets, move them and delete the OU while
	// retention's evidence is still uncommitted, and the evidence would name a deleted OU.
	t.Run("retention first", func(t *testing.T) {
		f, ids := newSerializeFixture(t)
		hold, err := f.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Rollback() //nolint:errcheck // released explicitly below; this covers a failed assertion
		if _, err := hold.Exec(f.st.bind(`SELECT id FROM prediction_marks WHERE set_id=? FOR UPDATE`), ids[0]); err != nil {
			t.Fatal(err)
		}
		retention := make(chan retentionResult, 1)
		go func() {
			n, err := f.st.DeletePredictionsBefore(retentionCutoff)
			retention <- retentionResult{n, err}
		}()
		waitUntil(t, f.st, "retention to block on the held row", blockedOnPrefix+`'DELETE FROM prediction_marks%'`)
		if got := queryCount(t, f.st, ownershipLockHeld); got != 1 {
			t.Fatalf("retention holds the ownership lock %d times; want once, taken before it read a set", got)
		}
		deletion := make(chan deletionResult, 1)
		go func() {
			moved, target, err := f.st.DeleteUserGroup(f.doomed)
			deletion <- deletionResult{moved, target, err}
		}()
		waitUntil(t, f.st, "the OU deletion to queue on the ownership lock", ownershipLockQueued)
		select {
		case r := <-deletion:
			t.Fatalf("the OU deletion finished while retention held the lock: %+v", r)
		default:
		}
		if err := hold.Rollback(); err != nil {
			t.Fatal(err)
		}
		r, d := <-retention, <-deletion
		if d.moved != 0 {
			t.Fatalf("the OU deletion moved %d sets that retention had already deleted", d.moved)
		}
		f.requireSerialized(t, r, d)
	})

	// The OU deletion holds the lock, blocked on a row that only this test can release; retention
	// arrives meanwhile. Without the lock it would still see the sets attributed to the doomed OU,
	// snapshot that id as evidence and commit it just before the OU disappears.
	t.Run("OU deletion first", func(t *testing.T) {
		f, _ := newSerializeFixture(t)
		hold, err := f.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Rollback() //nolint:errcheck // released explicitly below; this covers a failed assertion
		if _, err := hold.Exec(f.st.bind(`SELECT id FROM user_groups WHERE id=? FOR UPDATE`), f.kid); err != nil {
			t.Fatal(err)
		}
		deletion := make(chan deletionResult, 1)
		go func() {
			moved, target, err := f.st.DeleteUserGroup(f.doomed)
			deletion <- deletionResult{moved, target, err}
		}()
		waitUntil(t, f.st, "the OU deletion to block on the held row", blockedOnPrefix+`'UPDATE user_groups SET parent_id%'`)
		retention := make(chan retentionResult, 1)
		go func() {
			n, err := f.st.DeletePredictionsBefore(retentionCutoff)
			retention <- retentionResult{n, err}
		}()
		waitUntil(t, f.st, "retention to queue on the ownership lock", ownershipLockQueued)
		select {
		case r := <-retention:
			t.Fatalf("retention finished while the OU deletion held the lock: %+v", r)
		default:
		}
		if err := hold.Rollback(); err != nil {
			t.Fatal(err)
		}
		d, r := <-deletion, <-retention
		if d.moved != 3 {
			t.Fatalf("the OU deletion moved %d sets; want the 3 that belonged to the doomed OU", d.moved)
		}
		f.requireSerialized(t, r, d)
	})
}

// holdOwnershipLock takes the ownership lock in a transaction of the test's own, standing in for
// whichever business transaction is running when the one under test starts.
func holdOwnershipLock(t *testing.T, st *Store) *sql.Tx {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if err := lockPredictionOwnership(migExec{s: st, ex: tx}); err != nil {
		t.Fatal(err)
	}
	return tx
}

// A transaction that has to wait for the lock must not have looked at anything first: what it reads
// after the wait is what the other transaction committed. Reading before the wait would act on a
// state that no longer exists, which is why the lock comes before the first read and not before the
// first write.
func TestPostgresRetentionReadsOnlyAfterItHasTheOwnershipLock(t *testing.T) {
	st := retentionServer(t, pgStore(t, retentionTables...)).st
	p := seedOU(t, st, 0, scopeOwn)
	x := seedOU(t, st, 0, scopeOwn)
	// 2025-03 looks purgeable until the other transaction adds a pending mark to it.
	protected := seedRetentionSet(t, st, "2025-03", x, "1")
	// 2025-04 is purgeable, but the other transaction moves its sets from x to p first.
	seedRetentionSet(t, st, "2025-04", x, "2")

	other := holdOwnershipLock(t, st)
	result := make(chan retentionResult, 1)
	go func() {
		n, err := st.DeletePredictionsBefore(retentionCutoff)
		result <- retentionResult{n, err}
	}()
	waitUntil(t, st, "retention to queue on the ownership lock", ownershipLockQueued)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO prediction_marks(set_id, checkpoint, status) VALUES(?,'d20','pending')`, []any{protected}},
		{`UPDATE prediction_sets SET attributed_ou=? WHERE cohort='2025-04'`, []any{p}},
	} {
		if _, err := other.Exec(st.bind(q.sql), q.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil || r.deleted != 1 {
		t.Fatalf("retention deleted %d sets, %v; want only 2025-04's", r.deleted, r.err)
	}
	if got := queryCount(t, st, `SELECT COUNT(*) FROM prediction_sets WHERE cohort='2025-03'`); got != 1 {
		t.Fatalf("retention deleted a month that was no longer purgeable once it held the lock")
	}
	if got, wantRows := evidence(t, st), evidenceSet(ev("2025-04", p)); !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("evidence = %v; want %v, from the attribution committed while retention waited", got, wantRows)
	}
}

func TestPostgresOUDeletionReadsOnlyAfterItHasTheOwnershipLock(t *testing.T) {
	st := retentionServer(t, pgStore(t, retentionTables...)).st
	first := seedOU(t, st, 0, scopeOwn)
	second := seedOU(t, st, 0, scopeOwn)
	doomed := seedOU(t, st, first, nil)
	seedRetentionSet(t, st, "2025-03", doomed, "1")
	if _, err := st.DeletePredictionsBefore(retentionCutoff); err != nil {
		t.Fatal(err)
	}
	live := seedRetentionSet(t, st, retentionLiveMonth, doomed, "live")

	other := holdOwnershipLock(t, st)
	result := make(chan deletionResult, 1)
	go func() {
		moved, target, err := st.DeleteUserGroup(doomed)
		result <- deletionResult{moved, target, err}
	}()
	waitUntil(t, st, "the OU deletion to queue on the ownership lock", ownershipLockQueued)
	// While it waits, the OU is moved under another parent. Its destination is whatever is committed
	// when it finally reads, not what was true when it started.
	if _, err := other.Exec(st.bind(`UPDATE user_groups SET parent_id=? WHERE id=?`), second, doomed); err != nil {
		t.Fatal(err)
	}
	if err := other.Commit(); err != nil {
		t.Fatal(err)
	}
	r := <-result
	if r.err != nil || r.target != second || r.moved != 1 {
		t.Fatalf("OU deletion: moved %d to %d, %v; want 1 to the parent committed while it waited (%d)", r.moved, r.target, r.err, second)
	}
	if got := attributedOU(t, st, live); !got.Valid || got.Int64 != second {
		t.Fatalf("set attribution = %v; want %d", got, second)
	}
	if got, wantRows := evidence(t, st), evidenceSet(ev("2025-03", second)); !reflect.DeepEqual(got, wantRows) {
		t.Fatalf("evidence = %v; want %v", got, wantRows)
	}
}
