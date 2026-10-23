package app

// P38: the access schema (step 0006), the strict OU index, frozen attribution, and the OU delete's
// re-attribution. The rules here decide who can see a forecast, so each test fails in the direction
// that matters: an error, never a lenient answer; a frozen value, never a recomputed one.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// ---------- helpers ----------

var ouNameSeq int

func seedOU(t *testing.T, st *Store, parent int64, scope any) int64 {
	t.Helper()
	ouNameSeq++
	name := fmt.Sprintf("ou-%d-%d", time.Now().UnixNano(), ouNameSeq)
	var p any
	if parent != 0 {
		p = parent
	}
	id, err := st.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,
		is_default,parent_id,forecast_scope) VALUES(?,?,?,0,0,0,?,?)`, name, "", nowStr(), p, scope)
	if err != nil {
		t.Fatalf("seed OU: %v", err)
	}
	return id
}

func attributedOU(t *testing.T, st *Store, setID int64) sql.NullInt64 {
	t.Helper()
	var v sql.NullInt64
	if err := st.queryRow("SELECT attributed_ou FROM prediction_sets WHERE id=?", setID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func ingestWithToken(t *testing.T, s *Server, symbol, token string) (int64, int64) {
	t.Helper()
	body := ingestBody(string(predWire(nil)))
	body = replaceOnce(body, `"symbol":"600160"`, `"symbol":"`+symbol+`"`)
	if token != "" {
		body = strings.TrimSuffix(body, "}") + `,"owner_token":"` + token + `"}`
	}
	code, m := ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, m)
	}
	reportID, _ := m["id"].(float64)
	var setID int64
	if err := s.st.queryRow("SELECT id FROM prediction_sets WHERE report_id=? ORDER BY id DESC LIMIT 1",
		int64(reportID)).Scan(&setID); err != nil {
		t.Fatalf("read set: %v", err)
	}
	return int64(reportID), setID
}

// tokenServer is a test server with a signing key, for the tests that mint owner tokens.
func tokenServer(t *testing.T) *Server {
	t.Helper()
	s := newV1Server(t)
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	return s
}

// ---------- migration 0006 ----------

func TestMigration0006(t *testing.T) {
	path := fileStore(t, "p38.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []colRef{{"user_groups", "forecast_scope"}, {"prediction_sets", "attributed_ou"}} {
		if !st.columnExists(c.table, c.name) {
			t.Errorf("%s.%s missing after the step", c.table, c.name)
		}
	}
	if !st.indexExists("idx_pred_sets_ou") {
		t.Error("idx_pred_sets_ou missing after the step")
	}
	if !recorded(st, "0006") {
		t.Error("step 0006 not recorded")
	}

	// Idempotent: a second open changes nothing, and a hand-added column is accepted.
	first := ledgerPicture(t, st)
	st.Close()
	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	if second := ledgerPicture(t, st2); second != first {
		t.Errorf("the second open rewrote the ledger:\n%s\n%s", first, second)
	}

	// A hand-dropped column is refused at the next start by the product check.
	if _, err := st2.exec(`ALTER TABLE user_groups DROP COLUMN forecast_scope`); err != nil {
		t.Skipf("this SQLite cannot drop a column: %v", err)
	}
	if err := st2.verifyMigrationProducts(st2.poolExec(), st2.migrations()); err == nil {
		t.Error("a dropped column was not refused")
	}
}

func TestMigration0006OlderDumpRestoresWithNulls(t *testing.T) {
	src := newTestStore(t)
	// One set exists before the step, so the dump has a row to come back NULL.
	seedPredictionSet(t, src, sql.NullInt64{}, "p38-older")
	// Wind back to a database the release before P38 could have written: the index goes before the
	// column (SQLite refuses to drop a column an index still names), then the columns, then the row.
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_pred_sets_ou`,
		`ALTER TABLE prediction_sets DROP COLUMN attributed_ou`,
		`ALTER TABLE user_groups DROP COLUMN forecast_scope`,
	} {
		if _, err := src.exec(q); err != nil {
			t.Fatalf("cannot wind back: %v", err)
		}
	}
	if _, err := src.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("0006")); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if got := scalar[int](t, dst, `SELECT COUNT(*) FROM user_groups WHERE is_default=1 AND forecast_scope IS NULL`); got != 0 {
		t.Errorf("%d Default rows are still NULL after the restore normalisation", got)
	}
	if got := scalar[string](t, dst, `SELECT forecast_scope FROM user_groups WHERE is_default=1`); got != scopeNone {
		t.Errorf("Default scope = %q, want none", got)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM prediction_sets WHERE attributed_ou IS NOT NULL`); n != 0 {
		t.Errorf("%d restored sets have an attribution; an older dump has none", n)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM prediction_sets`); n == 0 {
		t.Error("the seeded set did not survive the restore")
	}
}

// TestDefaultScopeNormalisedAtBoot: the hook is in OpenStore, not only in the helper.
func TestDefaultScopeNormalisedAtBoot(t *testing.T) {
	path := fileStore(t, "p38-boot.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	def := st.EnsureDefaultGroup()
	if _, err := st.exec(`UPDATE user_groups SET forecast_scope=NULL WHERE id=?`, def); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if got := scalar[string](t, st2, `SELECT forecast_scope FROM user_groups WHERE id=?`, def); got != scopeNone {
		t.Errorf("boot left the Default scope %q, want none", got)
	}
}

func TestDefaultOUScopeIsNormalisedToNone(t *testing.T) {
	st := newTestStore(t)
	def := st.EnsureDefaultGroup()
	if err := st.EnsureDefaultForecastScope(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, st, `SELECT forecast_scope FROM user_groups WHERE id=?`, def); got != scopeNone {
		t.Errorf("scope = %q, want none", got)
	}
	// Idempotent in the sense that matters: it fills NULLs and never overwrites a decision.
	if _, err := st.exec(`UPDATE user_groups SET forecast_scope='all' WHERE id=?`, def); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureDefaultForecastScope(); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, st, `SELECT forecast_scope FROM user_groups WHERE id=?`, def); got != scopeAll {
		t.Errorf("a second call overwrote the value: %q", got)
	}
}

// ---------- attribution ----------

func TestSetAttributionIsFrozenAtInsert(t *testing.T) {
	s := tokenServer(t)
	ouA := seedOU(t, s.st, 0, scopeAll)
	ouB := seedOU(t, s.st, 0, scopeAll)
	tok := s.mintOwnerToken(ouA, "asker")

	_, firstSet := ingestWithToken(t, s, "600170", tok)
	if got := attributedOU(t, s.st, firstSet); !got.Valid || got.Int64 != ouA {
		t.Fatalf("attributed_ou = %v, want %d", got, ouA)
	}
	// Move the token's user, change A's parent and A's scope: the frozen value must not move.
	if _, err := s.st.exec(`UPDATE user_groups SET parent_id=?, forecast_scope='own' WHERE id=?`, ouB, ouA); err != nil {
		t.Fatal(err)
	}
	if got := attributedOU(t, s.st, firstSet); !got.Valid || got.Int64 != ouA {
		t.Errorf("attribution moved after an OU change: %v", got)
	}
	// A re-ingest with a DIFFERENT payload by a user in B creates a new set at B; the first stays.
	tokB := s.mintOwnerToken(ouB, "other")
	body := ingestBody(string(predWire(func(m map[string]any) {
		claim(m, "expected_terminal", func(c map[string]any) { c["price"] = "12.34" })
	})))
	body = replaceOnce(body, `"symbol":"600160"`, `"symbol":"600170"`)
	body = strings.TrimSuffix(body, "}") + `,"owner_token":"` + tokB + `"}`
	if code, m := ingestPost(t, s, body); code != http.StatusOK {
		t.Fatalf("re-ingest: %d %v", code, m)
	}
	if got := attributedOU(t, s.st, firstSet); !got.Valid || got.Int64 != ouA {
		t.Errorf("the first set moved after a re-ingest: %v", got)
	}
	if got := scalar[sql.NullInt64](t, s.st, `SELECT attributed_ou FROM prediction_sets ORDER BY id DESC LIMIT 1`); !got.Valid || got.Int64 != ouB {
		t.Errorf("the new set = %v, want %d", got, ouB)
	}
}

func TestInvalidOrMissingTokenLeavesSetsUnattributed(t *testing.T) {
	s := tokenServer(t)
	ou := seedOU(t, s.st, 0, scopeAll)
	valid := s.mintOwnerToken(ou, "asker")
	expiredMsg := fmt.Sprintf("%s|%d|%s|%d", ownerTokenPrefix, ou, "asker", time.Now().Add(-time.Hour).Unix())
	expired := encodeSessionMessage(expiredMsg) + "." + s.hmac(expiredMsg)
	tampered := valid[:len(valid)-1] + "x"
	foreign := encodeSessionMessage("notowner|1|x|9999999999") + "." + s.hmac("notowner|1|x|9999999999")

	for i, tok := range []string{"", valid + "junk", tampered, expired, foreign, "garbage.sig"} {
		symbol := fmt.Sprintf("60%04d", 1700+i)
		_, setID := ingestWithToken(t, s, symbol, tok)
		if got := attributedOU(t, s.st, setID); got.Valid {
			t.Errorf("token %d: attributed_ou = %d, want NULL", i, got.Int64)
		}
	}
}

func TestTokenForADeletedOULeavesTheSetUnattributed(t *testing.T) {
	s := tokenServer(t)
	ou := seedOU(t, s.st, 0, scopeAll)
	tok := s.mintOwnerToken(ou, "x") // minted while the OU existed
	if _, _, err := s.st.DeleteUserGroup(ou); err != nil {
		t.Fatal(err)
	}
	_, setID := ingestWithToken(t, s, "600180", tok)
	if got := attributedOU(t, s.st, setID); got.Valid {
		t.Errorf("set attributed to a deleted OU: %d", got.Int64)
	}
}

func TestAttributionNeverSerialised(t *testing.T) {
	s := tokenServer(t)
	ou := seedOU(t, s.st, 0, scopeAll)
	reportID, _ := ingestWithToken(t, s, "600190", s.mintOwnerToken(ou, "asker"))

	for _, path := range []string{
		fmt.Sprintf("/api/report/%d/predictions", reportID),
		"/api/predictions?symbol=600190",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.SetPathValue("id", strconv.FormatInt(reportID, 10))
		if strings.Contains(path, "/api/report/") {
			s.serveReportPredictions(rec, req, nil)
		} else {
			s.serveQueryPredictions(rec, req, nil)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "attributed_ou") {
			t.Errorf("%s serialised attributed_ou", path)
		}
	}
}

func TestNothingElseWritesAttributedOU(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	literals := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "attributed_ou") {
			literals[name] = string(body)
		}
	}
	// The INSERT that freezes attribution, the step that creates the column, and the single
	// sanctioned re-attribution UPDATE on OU delete. The population loader may name the column
	// exactly once, as its read-only filter; it must never acquire another attribution writer.
	// Purge evidence may snapshot the column but must never mutate prediction_sets.
	var names []string
	for name := range literals {
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"forecast_groups_store.go", "forecast_purge_owners.go", "forecast_store.go", "migrate_steps.go", "user_admin.go"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files naming attributed_ou = %v, want %v", names, want)
	}
	if !strings.Contains(literals["forecast_store.go"], "INSERT INTO prediction_sets") {
		t.Error("forecast_store.go no longer holds the freezing INSERT")
	}
	reader := literals["forecast_groups_store.go"]
	if strings.Count(reader, "attributed_ou") != 1 || !strings.Contains(reader, `" AND ps.attributed_ou IN ("`) {
		t.Error("population loader names attribution outside its single read-only predicate")
	}
	for _, write := range []string{"INSERT ", "UPDATE ", "DELETE ", "REPLACE "} {
		if strings.Contains(strings.ToUpper(reader), write) {
			t.Errorf("population loader contains a write: %s", write)
		}
	}
	for _, write := range []string{"INSERT INTO PREDICTION_SETS", "UPDATE PREDICTION_SETS", "DELETE FROM PREDICTION_SETS", "REPLACE INTO PREDICTION_SETS"} {
		if strings.Contains(strings.ToUpper(literals["forecast_purge_owners.go"]), write) {
			t.Errorf("purge evidence mutates frozen set attribution: %s", write)
		}
	}
	updates := strings.Count(literals["user_admin.go"], "UPDATE prediction_sets SET attributed_ou")
	if updates != 2 {
		t.Errorf("re-attribution UPDATE statements = %d, want 2", updates)
	}
}

// ---------- the OU index ----------

func TestOUIndexSurfacesErrors(t *testing.T) {
	st := newTestStore(t)
	st.Close()
	if _, err := st.loadOUIndex(); err == nil {
		t.Fatal("a closed store returned an empty index")
	}
}

func TestOUChainRefusesADanglingOrCyclicParent(t *testing.T) {
	st := newTestStore(t)
	def := st.EnsureDefaultGroup()

	// A root with a NULL parent is not an error and gets the Default appended.
	root := seedOU(t, st, 0, nil)
	ix, err := st.loadOUIndex()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := ix.chain(root)
	if err != nil {
		t.Fatalf("a root chain: %v", err)
	}
	if len(chain) != 2 || chain[0] != root || chain[1] != def {
		t.Errorf("root chain = %v, want [%d %d]", chain, root, def)
	}

	// A dangling parent: the row names an id that does not exist.
	if _, err := st.exec(`INSERT INTO user_groups(id,name,description,created_at,weight,urgent_unlimited,is_default,parent_id)
		VALUES(9001,'dangling','',?,0,0,0,987654)`, nowStr()); err != nil {
		t.Fatal(err)
	}
	ix, _ = st.loadOUIndex()
	if _, err := ix.chain(9001); err != errDanglingOU {
		t.Errorf("dangling chain error = %v, want errDanglingOU", err)
	}
	if _, err := ix.restrictedEffective(9001); err != errDanglingOU {
		t.Errorf("dangling restriction error = %v, want errDanglingOU", err)
	}

	// A two-OU cycle.
	a := seedOU(t, st, 0, nil)
	b := seedOU(t, st, a, nil)
	if _, err := st.exec(`UPDATE user_groups SET parent_id=? WHERE id=?`, b, a); err != nil {
		t.Fatal(err)
	}
	ix, _ = st.loadOUIndex()
	if _, err := ix.chain(a); err != errCyclicOU {
		t.Errorf("cycle error = %v, want errCyclicOU", err)
	}

	// A chain deeper than 64.
	parent := int64(0)
	for i := 0; i < 70; i++ {
		parent = seedOU(t, st, parent, nil)
	}
	ix, _ = st.loadOUIndex()
	if _, err := ix.chain(parent); err != errCyclicOU {
		t.Errorf("deep chain error = %v, want errCyclicOU", err)
	}
}

func TestOUSubtreeAndScopeWalk(t *testing.T) {
	st := newTestStore(t)
	def := st.EnsureDefaultGroup()
	root := seedOU(t, st, 0, scopeAll)
	child := seedOU(t, st, root, nil)
	grand := seedOU(t, st, child, scopeOwn)
	ix, err := st.loadOUIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.subtree(root); len(got) != 3 {
		t.Errorf("subtree = %v, want root, child, grandchild", got)
	}
	if got, err := ix.effectiveScope(grand); err != nil || got != scopeOwn {
		t.Errorf("effective scope = %q/%v, want own", got, err)
	}
	if got, err := ix.effectiveScope(child); err != nil || got != scopeAll {
		t.Errorf("inherited scope = %q/%v, want all", got, err)
	}
	// Default's NULL reads as none.
	if got, err := ix.effectiveScope(def); err != nil || got != scopeNone {
		t.Errorf("Default scope = %q/%v, want none", got, err)
	}
}

// ---------- deleting an OU ----------

func TestDeletingOUMovesChildrenToItsParent(t *testing.T) {
	st := newTestStore(t)
	root := seedOU(t, st, 0, nil)
	parent := seedOU(t, st, root, nil)
	child := seedOU(t, st, parent, nil)
	if _, _, err := st.DeleteUserGroup(parent); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, st, `SELECT parent_id FROM user_groups WHERE id=?`, child); got != root {
		t.Errorf("child parent = %d, want %d", got, root)
	}
}

func TestDeletingOUWithADanglingParentKeepsChildrenAsRoots(t *testing.T) {
	st := newTestStore(t)
	ghost := seedOU(t, st, 0, nil)
	parent := seedOU(t, st, ghost, nil)
	child := seedOU(t, st, parent, nil)
	// Make the parent's parent vanish out from under it (a corrupted/restored tree).
	if _, err := st.exec(`DELETE FROM user_groups WHERE id=?`, ghost); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.DeleteUserGroup(parent); err != nil {
		t.Fatal(err)
	}
	var parentCol sql.NullInt64
	if err := st.queryRow(`SELECT parent_id FROM user_groups WHERE id=?`, child).Scan(&parentCol); err != nil {
		t.Fatal(err)
	}
	if parentCol.Valid {
		t.Errorf("child parent = %d, want NULL (a root, not the Default's child)", parentCol.Int64)
	}
}

func TestDeletingRootOUKeepsChildrenAsRoots(t *testing.T) {
	st := newTestStore(t)
	root := seedOU(t, st, 0, nil)
	child := seedOU(t, st, root, nil)
	if _, _, err := st.DeleteUserGroup(root); err != nil {
		t.Fatal(err)
	}
	var parent sql.NullInt64
	if err := st.queryRow(`SELECT parent_id FROM user_groups WHERE id=?`, child).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent.Valid {
		t.Errorf("child parent = %d, want NULL", parent.Int64)
	}
}

func TestDeletingOUReattributesSetsToParent(t *testing.T) {
	s := tokenServer(t)
	a := seedOU(t, s.st, 0, scopeAll)
	b := seedOU(t, s.st, a, scopeAll)
	c := seedOU(t, s.st, b, scopeAll)
	_, set1 := ingestWithToken(t, s, "600200", s.mintOwnerToken(c, "x"))
	// A second set at C with a different payload (a different symbol is simplest).
	_, set2 := ingestWithToken(t, s, "600201", s.mintOwnerToken(c, "x"))
	_, sibling := ingestWithToken(t, s, "600202", s.mintOwnerToken(b, "x"))

	moved, target, err := s.st.DeleteUserGroup(c)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 || target != b {
		t.Errorf("moved/target = %d/%d, want 2/%d", moved, target, b)
	}
	for _, id := range []int64{set1, set2} {
		if got := attributedOU(t, s.st, id); !got.Valid || got.Int64 != b {
			t.Errorf("set %d = %v, want %d", id, got, b)
		}
	}
	if got := attributedOU(t, s.st, sibling); !got.Valid || got.Int64 != b {
		t.Errorf("the sibling's set moved: %v", got)
	}
}

func TestDeletingRootOUReattributesSetsToDefault(t *testing.T) {
	s := tokenServer(t)
	def := s.st.EnsureDefaultGroup()
	root := seedOU(t, s.st, 0, scopeAll)
	_, setID := ingestWithToken(t, s, "600210", s.mintOwnerToken(root, "x"))
	if _, _, err := s.st.DeleteUserGroup(root); err != nil {
		t.Fatal(err)
	}
	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != def {
		t.Errorf("set = %v, want Default %d", got, def)
	}
}

func TestDeletingOUWritesAuditCounts(t *testing.T) {
	s := tokenServer(t)
	parent := seedOU(t, s.st, 0, scopeAll)
	ou := seedOU(t, s.st, parent, scopeAll)
	ingestWithToken(t, s, "600220", s.mintOwnerToken(ou, "x"))

	req := httptest.NewRequest("DELETE", "/api/admin/groups/"+strconv.FormatInt(ou, 10), nil)
	req.SetPathValue("id", strconv.FormatInt(ou, 10))
	rec := httptest.NewRecorder()
	s.apiGroupDelete(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rows := auditRows(t, s, AuditGroupDelete)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(rows[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["forecast_sets_reattributed"] != float64(1) || detail["forecast_sets_to_ou"] != float64(parent) {
		t.Errorf("audit detail = %s", rows[0].Detail)
	}

	// No sets: both keys are still present, as zero.
	other := seedOU(t, s.st, 0, scopeAll)
	req = httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("id", strconv.FormatInt(other, 10))
	rec = httptest.NewRecorder()
	s.apiGroupDelete(rec, req, "admin")
	rows = auditRows(t, s, AuditGroupDelete)
	if len(rows) != 2 {
		t.Fatalf("audit rows = %d", len(rows))
	}
	var zero string
	for _, r := range rows {
		if strings.Contains(r.Detail, `"forecast_sets_reattributed":0`) {
			zero = r.Detail
		}
	}
	if zero == "" || !strings.Contains(zero, `"forecast_sets_to_ou":0`) {
		t.Errorf("the zero case must report 0 for both keys; rows = %s / %s", rows[0].Detail, rows[1].Detail)
	}
}

func TestDeleteOUReattributionIsAtomic(t *testing.T) {
	s := tokenServer(t)
	parent := seedOU(t, s.st, 0, scopeAll)
	ou := seedOU(t, s.st, parent, scopeAll)
	_, setID := ingestWithToken(t, s, "600230", s.mintOwnerToken(ou, "x"))

	// A trigger that aborts the final DELETE: the UPDATE has already run at that point.
	if _, err := s.st.exec(`CREATE TRIGGER p38_abort BEFORE DELETE ON user_groups
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.st.DeleteUserGroup(ou); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM user_groups WHERE id=?`, ou); got != 1 {
		t.Error("the OU was deleted despite the failure")
	}
	if got := attributedOU(t, s.st, setID); !got.Valid || got.Int64 != ou {
		t.Errorf("attribution was not rolled back: %v", got)
	}
}

func TestAComputationAcrossAPurgeIsNotCached(t *testing.T) {
	s := newV1Server(t)
	_, gen := s.cachedForecastCell("k")
	cell := &forecastCell{State: "mature"}
	s.storeForecastCell("k", cell, gen)
	if c, _ := s.cachedForecastCell("k"); c == nil {
		t.Fatal("a fresh generation was not stored")
	}
	s.purgeForecastGroups()
	// A computation that began before the purge must not repopulate the cache with a pre-delete cell.
	s.storeForecastCell("k", cell, gen)
	if c, _ := s.cachedForecastCell("k"); c != nil {
		t.Error("a cell computed across a purge was cached")
	}
}

func TestDeletingOUPurgesForecastCache(t *testing.T) {
	s := newV1Server(t)
	root := seedOU(t, s.st, 0, scopeAll)
	s.forecastGroupsMu.Lock()
	s.forecastGroupsCache = map[string]*forecastCell{"cached": {}}
	s.forecastGroupsMu.Unlock()
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("id", strconv.FormatInt(root, 10))
	rec := httptest.NewRecorder()
	s.apiGroupDelete(rec, req, "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if s.forecastGroupsCache != nil {
		t.Error("the group cache survived an OU delete")
	}
}

func TestDeletedOUIdIsNeverReferencedBySets(t *testing.T) {
	s := tokenServer(t)
	a := seedOU(t, s.st, 0, scopeAll)
	b := seedOU(t, s.st, a, scopeAll)
	ingestWithToken(t, s, "600240", s.mintOwnerToken(b, "x"))
	ingestWithToken(t, s, "600241", s.mintOwnerToken(a, "x"))
	if _, _, err := s.st.DeleteUserGroup(b); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.st.DeleteUserGroup(a); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM prediction_sets ps
		WHERE ps.attributed_ou IS NOT NULL
		  AND ps.attributed_ou NOT IN (SELECT id FROM user_groups)`); n != 0 {
		t.Errorf("%d sets reference a missing OU", n)
	}
}

func TestReusedOUIdCarriesNoScope(t *testing.T) {
	st := newTestStore(t)
	def := st.EnsureDefaultGroup()
	old := seedOU(t, st, 0, scopeAll)
	if _, _, err := st.DeleteUserGroup(old); err != nil {
		t.Fatal(err)
	}
	// An id reassigned to a new OU (the restore path resets identities, so an id above the dump's
	// maximum can come back). The new row has no scope of its own.
	if _, err := st.exec(`INSERT INTO user_groups(id,name,description,created_at,weight,urgent_unlimited,is_default)
		VALUES(?,'reused','',?,0,0,0)`, old, nowStr()); err != nil {
		t.Fatal(err)
	}
	ix, err := st.loadOUIndex()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ix.effectiveScope(old); err != nil || got != scopeNone {
		t.Errorf("a reused id resolved to %q/%v, want none inherited from the Default", got, err)
	}
	if def == 0 {
		t.Fatal("no Default")
	}
}
