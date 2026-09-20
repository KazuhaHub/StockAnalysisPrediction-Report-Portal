package app

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The migration framework (ADR 0034 amendment). The base schema is the ACCEPTANCE contract — a
// database must already satisfy it to be opened — and it is frozen for this compatibility window.
// Everything a release adds after it is an ordered, forward-only, idempotent step, applied once and
// recorded in the ledger.
//
// These tests are written against the framework's contract rather than against 0001, because 0001 is
// the easy case: three nullable columns, no data, nothing to back-fill. A framework that only
// happens to work for that is the failure this file exists to catch.

// probeStep is a synthetic step. The framework tests drive runMigrationSteps directly with steps of
// their own, so the contract is tested without shipping a fake migration in the real list.
//
// It creates a scratch table with one column per call, declares both, and optionally fails — which is
// how "the process died after the DDL and before the ledger row" is reproduced.
func probeStep(id, col string, fail error) migration {
	return migration{
		id:      id,
		tables:  []string{"mig_probe"},
		columns: []colRef{{table: "mig_probe", name: col}},
		up: func(e migExec) error {
			if !e.tableExists("mig_probe") {
				if _, err := e.exec(`CREATE TABLE IF NOT EXISTS mig_probe(id INTEGER PRIMARY KEY AUTOINCREMENT)`); err != nil {
					return err
				}
			}
			if !e.columnExists("mig_probe", col) {
				if _, err := e.exec(`ALTER TABLE mig_probe ADD COLUMN ` + col + ` TEXT`); err != nil {
					return err
				}
			}
			return fail
		},
	}
}

// recorded reports whether the ledger holds a row for a step.
func recorded(st *Store, id string) bool { return st.GetSetting(migrationLedgerKey(id), "") != "" }

// ledgerRowsFor counts the ledger rows naming one step.
func ledgerRowsFor(t *testing.T, st *Store, id string) int {
	t.Helper()
	var n int
	if err := st.queryRow(`SELECT COUNT(*) FROM meta WHERE k=?`, migrationLedgerKey(id)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A step that loses the write to another process must be retried, not failed. On SQLite the loser of
// a write race gets a busy error rather than a wait, and a restart that overlapped its predecessor is
// an ordinary event rather than a broken database.
func TestAContendedStepIsRetried(t *testing.T) {
	st := newTestStore(t)
	attempts := 0
	step := migration{id: "9006", up: func(e migExec) error {
		attempts++
		if attempts < 3 {
			return errContention // what the driver reports when another writer holds the database
		}
		return nil
	}}
	if err := st.runMigrationSteps([]migration{step}); err != nil {
		t.Fatalf("a contended step must be retried rather than failed: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	if !recorded(st, "9006") {
		t.Error("the retried step was not recorded")
	}
}

// ledgerPicture is the ledger as one comparable string: id and recorded timestamp per step.
func ledgerPicture(t *testing.T, st *Store) string {
	t.Helper()
	rows, err := st.query(`SELECT k, v FROM meta WHERE k LIKE ? ORDER BY k`, migrationLedgerPrefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		b.WriteString(k + "=" + v + "\n")
	}
	return b.String()
}

// ---------- the ledger ----------

// TestAFreshDatabaseRunsAndRecordsEveryStep is the create path: a new database is the baseline plus
// every step, and each step is recorded as applied. Nothing is pre-stamped — the record has to follow
// the work, or a step that silently failed to run would look applied for ever.
func TestAFreshDatabaseRunsAndRecordsEveryStep(t *testing.T) {
	st := newTestStore(t)
	steps := st.migrations()
	if len(steps) == 0 {
		t.Fatal("no migrations declared: this release's 0001 must be in the list")
	}
	for _, m := range steps {
		if !recorded(st, m.id) {
			t.Errorf("step %s ran on a fresh database but was not recorded", m.id)
		}
	}
	for _, c := range steps[0].columns {
		if !st.columnExists(c.table, c.name) {
			t.Errorf("0001 is recorded but %s.%s is missing", c.table, c.name)
		}
	}
}

// TestAnAcceptedDatabaseWithoutTheLedgerIsMigratedOnce is the upgrade path, and the one that matters
// in production: a database at the accepted baseline, written by the previous release, which knows
// nothing about migrations. It must open, gain the steps, and record them.
func TestAnAcceptedDatabaseWithoutTheLedgerIsMigratedOnce(t *testing.T) {
	path := fileStore(t, "pre-migration.db")
	// Wind the database back to what the previous release left: the baseline shape, no ledger. This
	// is the fixture the whole upgrade story is about.
	mutateFile(t, path,
		`DELETE FROM meta WHERE k LIKE 'mig:%'`,
		`ALTER TABLE user_groups DROP COLUMN totp_enroll`,
		`ALTER TABLE user_groups DROP COLUMN passkey_enroll`,
		`ALTER TABLE user_groups DROP COLUMN require_2fa`)

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("a baseline database must be accepted and migrated: %v", err)
	}
	first := ledgerPicture(t, st)
	if !st.columnExists("user_groups", "totp_enroll") {
		t.Fatal("the step did not run on the accepted database")
	}
	st.Close()

	// Reopening must not run anything again: the ledger is the record, and a step that re-runs is a
	// step whose second run nobody reviewed.
	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening a migrated database: %v", err)
	}
	defer st2.Close()
	if second := ledgerPicture(t, st2); second != first {
		t.Errorf("the second open rewrote the ledger:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// TestAStepRunsAtMostOnce drives the framework directly: an applied step is never applied twice.
func TestAStepRunsAtMostOnce(t *testing.T) {
	st := newTestStore(t)
	runs := 0
	step := probeStep("9001", "col_a", nil)
	inner := step.up
	step.up = func(e migExec) error { runs++; return inner(e) }

	if err := st.runMigrationSteps([]migration{step}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := st.runMigrationSteps([]migration{step}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if runs != 1 {
		t.Errorf("step ran %d times, want 1", runs)
	}
	if n := ledgerRowsFor(t, st, "9001"); n != 1 {
		t.Errorf("ledger rows = %d, want 1", n)
	}
}

// A step whose column is already there — a database restored from a dump written by a build that had
// no ledger, or carried over before the ledger existed — must record itself rather than fail on a
// duplicate column. It does not re-run the DDL.
func TestAColumnAlreadyThereIsRecordedWithoutReRunningTheDDL(t *testing.T) {
	st := newTestStore(t)
	step := probeStep("9004", "col_c", nil)
	if err := st.runMigrationSteps([]migration{step}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// Forget only the record, as a restore does when it rewrites `meta`.
	if _, err := st.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("9004")); err != nil {
		t.Fatal(err)
	}
	if err := st.runMigrationSteps([]migration{step}); err != nil {
		t.Fatalf("a step whose column already exists must be a no-op, not a failure: %v", err)
	}
	if !recorded(st, "9004") {
		t.Error("the step ran but was not recorded")
	}
}

// An unknown ledger row means the database was written by a release that knows steps this build does
// not. Refusing is the same rule the schema generation marker already applies: this build cannot know
// what those steps changed, so it must not operate on the result.
func TestAnUnknownLedgerRowIsRefusedAtStartup(t *testing.T) {
	path := fileStore(t, "from-the-future.db")
	mutateFile(t, path, `INSERT INTO meta(k,v) VALUES('mig:9999','2026-01-01T00:00:00Z')`)

	before := describeFile(t, path)
	_, err := OpenStore("sqlite", path)
	if err == nil {
		t.Fatal("a database with an unknown migration record must not open")
	}
	if !strings.Contains(err.Error(), "9999") {
		t.Errorf("error %q does not name the unknown step", err)
	}
	if after := describeFile(t, path); after != before {
		t.Errorf("a refused database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

// ---------- products ----------

// A step declares what it guarantees, and the declaration is checked for EVERY step — including the
// ones the ledger says are applied. "Recorded" is not evidence: a column dropped by hand, or by a
// restore from a hand-edited dump, would otherwise start up looking migrated.
func TestADeclaredProductMustExist(t *testing.T) {
	st := newTestStore(t)
	liar := migration{
		id:      "9002",
		columns: []colRef{{table: "mig_probe", name: "never_created"}},
		up:      func(e migExec) error { return nil },
	}
	err := st.runMigrationSteps([]migration{liar})
	if err == nil {
		t.Fatal("a step whose declared product is missing must fail")
	}
	if !strings.Contains(err.Error(), "never_created") {
		t.Errorf("error %q does not name the missing product", err)
	}
	if recorded(st, "9002") {
		t.Error("a step that failed its own verification was recorded anyway")
	}
}

func TestARecordedStepWhoseProductWasDroppedIsRefused(t *testing.T) {
	path := fileStore(t, "product-dropped.db")
	// The ledger still says 0001 ran; the column is gone.
	mutateFile(t, path, `ALTER TABLE user_groups DROP COLUMN totp_enroll`)

	before := describeFile(t, path)
	_, err := OpenStore("sqlite", path)
	if err == nil {
		t.Fatal("a recorded step whose product was dropped must be refused")
	}
	if !strings.Contains(err.Error(), "totp_enroll") {
		t.Errorf("error %q does not name the missing product", err)
	}
	if after := describeFile(t, path); after != before {
		t.Errorf("a refused database was modified:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
}

// ---------- interruption ----------

// A step does its DDL and then fails before the ledger row — the window an interrupted process leaves.
// Because the step's transaction covers the DDL, the product check and the record together, that
// window closes completely: the rollback takes the DDL with it, so a failed step leaves NOTHING to
// half-exist, and the retry applies the step once from scratch.
//
// (The idempotence guard still matters, and is tested separately: a restore rewrites `meta`, so a
// column can be present with no ledger row for it — see TestAColumnAlreadyThereIsRecordedWithoutReRunningTheDDL.)
func TestAFailedStepRollsBackCompletelyAndIsRetried(t *testing.T) {
	st := newTestStore(t)
	boom := errors.New("injected: failed after the DDL, before the ledger row")
	step := probeStep("9003", "col_b", boom)

	if err := st.runMigrationSteps([]migration{step}); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if recorded(st, "9003") {
		t.Fatal("a step that returned an error was recorded as applied")
	}
	// The DDL ran inside the step and the rollback took it back out. This is the assertion that says
	// "no half-applied step": the table the step created is not there.
	if st.tableExists("mig_probe") {
		t.Error("the failed step's table survived its rollback — a half-applied step")
	}

	retry := probeStep("9003", "col_b", nil)
	if err := st.runMigrationSteps([]migration{retry}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !recorded(st, "9003") {
		t.Error("the retried step was not recorded")
	}
	if !st.columnExists("mig_probe", "col_b") {
		t.Error("the retried step did not produce its column")
	}
	if n := ledgerRowsFor(t, st, "9003"); n != 1 {
		t.Errorf("ledger rows for 9003 = %d, want 1", n)
	}
}

// A step that fails before it touches anything must leave the database exactly as it was — including
// its ledger — and the same step must succeed on a later run.
func TestAStepThatFailsBeforeAnyDDLLeavesNothing(t *testing.T) {
	st := newTestStore(t)
	before := ledgerPicture(t, st)
	step := migration{id: "9005", up: func(e migExec) error { return errors.New("injected: before any DDL") }}

	if err := st.runMigrationSteps([]migration{step}); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if after := ledgerPicture(t, st); after != before {
		t.Errorf("a failed step changed the ledger:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
	if st.tableExists("mig_probe") {
		t.Error("a step that failed before any DDL created something anyway")
	}
}

// ---------- concurrency ----------

// Two processes starting against one database is the ordinary consequence of a restart that overlapped
// its predecessor, and of running two instances by mistake. Both must end up with every step applied
// exactly once, and neither may fail for it: the ledger is re-read inside each step's transaction, so
// the loser sees the winner's row and does nothing.
func TestConcurrentMigrationsApplyEachStepOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	first, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	steps := func() []migration {
		var out []migration
		for i, name := range []string{"race_a", "race_b", "race_c"} {
			out = append(out, probeStep("920"+string(rune('0'+i)), name, nil))
		}
		return out
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, st := range []*Store{first, second} {
		wg.Add(1)
		go func(i int, st *Store) {
			defer wg.Done()
			errs[i] = st.runMigrationSteps(steps())
		}(i, st)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("migrator %d: %v (two starts must serialise, not fail)", i, err)
		}
	}
	for i := range []int{0, 1, 2} {
		id := "920" + string(rune('0'+i))
		if n := ledgerRowsFor(t, first, id); n != 1 {
			t.Errorf("ledger rows for %s = %d, want 1", id, n)
		}
	}
}
