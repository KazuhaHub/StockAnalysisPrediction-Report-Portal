package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Restoring across a migration boundary.
//
// The runbook tells an operator to take a backup before upgrading. That backup was written by the
// release they are leaving, so it does not carry the columns the new release adds — and until this
// work the restore refused exactly that ("the backup's user_groups is missing ..."), which would have
// turned the instruction into a trap. These tests pin the rule that replaces it: a dump is loadable
// when every step it does not carry is one that says an older dump is fine.

// preMigrationDump is a dump written by the release before this one: the accepted baseline shape,
// a group row, and no migration ledger. It is built from the schema declarations themselves so the
// fixture cannot drift from the shape it is imitating.
func preMigrationDump(t *testing.T) []byte {
	t.Helper()
	db := rawStoreDB(t)
	st := &Store{db: db, driver: "sqlite"}
	for _, stmt := range st.baseSchemaStmts() {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding the previous release's schema: %v", err)
		}
	}
	execAll(t, db,
		`INSERT INTO meta(k,v) VALUES('schema_version','2')`,
		`INSERT INTO meta(k,v) VALUES('site_title','旧站点')`,
		`INSERT INTO user_groups(id,name,description,created_at,weight,urgent_unlimited,is_default)
			VALUES(1,'安禅内部','', '2026-01-01T00:00:00Z',0,0,1)`,
	)
	return dumpOf(t, st)
}

// withHeaderMigrations rewrites the first line's migration list, which is how a hand-edited or
// corrupted dump is reproduced.
func withHeaderMigrations(t *testing.T, dump []byte, ids []string) []byte {
	t.Helper()
	line, rest, ok := strings.Cut(string(dump), "\n")
	if !ok {
		t.Fatal("the dump has no header line")
	}
	var header map[string]any
	if err := json.Unmarshal([]byte(line), &header); err != nil {
		t.Fatal(err)
	}
	header["migrations"] = ids
	edited, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(string(edited) + "\n" + rest)
}

// TestADumpFromBeforeAStepLoadsWithoutItsColumns is the upgrade story end to end: an old dump loads
// into a new build, the new columns come back empty (which is what "inherit" is), and the ledger is
// rebuilt so the next start does not think the database was written by a stranger.
func TestADumpFromBeforeAStepLoadsWithoutItsColumns(t *testing.T) {
	dump := preMigrationDump(t)
	dst := newTestStore(t)

	rep, err := dst.restoreFrom(bytes.NewReader(dump), true)
	if err != nil {
		t.Fatalf("a dump from before 0001 must load: %v", err)
	}
	if !rep.Applied {
		t.Fatal("the restore did not report itself applied")
	}
	for _, c := range []string{"totp_enroll", "passkey_enroll", "require_2fa"} {
		if !dst.columnExists("user_groups", c) {
			t.Errorf("column %s is missing after the restore", c)
		}
	}
	if got := scalar[string](t, dst, `SELECT name FROM user_groups WHERE id=1`); got != "安禅内部" {
		t.Errorf("restored group name = %q", got)
	}
	if got := scalar[string](t, dst, `SELECT v FROM meta WHERE k='site_title'`); got != "旧站点" {
		t.Errorf("restored meta = %q", got)
	}
	// The ledger must be rebuilt as part of the restore, not left for the next start: a restore that
	// ends with no ledger is a database whose next start re-runs every step.
	for _, m := range dst.migrations() {
		if !recorded(dst, m.id) {
			t.Errorf("step %s was not recorded by the restore", m.id)
		}
	}
}

// The declared list has to be a contiguous prefix of the steps this build knows: it names a point in
// the sequence, and a list with a hole, a repeat, an unknown id or a different order names no such
// point. This drives the rule itself — whether the content bears the claim out is the next test's job.
func TestADumpMigrationListMustBeAContiguousPrefix(t *testing.T) {
	steps := newTestStore(t).migrations() // this build's list; 0001 today
	known := []string{}
	for _, m := range steps {
		known = append(known, m.id)
	}
	if len(known) < 1 {
		t.Fatal("no migrations to test the rule against")
	}
	cases := []struct {
		name string
		ids  []string
		ok   bool
	}{
		{"none declared, which is what the previous release wrote", nil, true},
		{"the one step this build has", []string{known[0]}, true},
		{"a repeat", []string{known[0], known[0]}, false},
		{"an unknown step", []string{"0099"}, false},
		{"the known step and then an unknown one", append(append([]string{}, known...), "0099"), false},
		{"an unknown step before a known one", []string{"0099", known[0]}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDumpMigrations(tc.ids, steps)
			if tc.ok && err != nil {
				t.Fatalf("must be acceptable: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("must be refused")
			}
		})
	}
}

// A dump whose header lies about its own shape is refused, whatever the header says: the list is a
// claim the content has to bear out, and this is the half that makes it one.
func TestADumpClaimingAStepMustHaveLoadableContent(t *testing.T) {
	dump := preMigrationDump(t)
	dst := newTestStore(t)
	before := describe(t, dst.db)

	edit := withHeaderMigrations(t, dump, []string{"0001"})
	if _, err := dst.restoreFrom(bytes.NewReader(edit), true); err == nil {
		t.Fatal("a dump claiming 0001 without its columns must be refused")
	}
	if after := describe(t, dst.db); after != before {
		t.Errorf("a refused restore modified the database:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
	// And the same dump without the claim loads, which is what makes the check above about the claim
	// rather than about the file.
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("the file itself must load: %v", err)
	}
}

// A step whose data work cannot be re-derived is not one this build may load an older dump for. There
// is no generic data-upgrade framework, and guessing is how a restore silently produces rows that were
// never upgraded — so the answer is no, with the operator told to restore with the release that wrote
// it.
func TestARestoreRequiringADataUpgradeIsRefused(t *testing.T) {
	steps := []migration{
		{id: "0001", restore: restoreRule{olderDumpOK: true}},
		{id: "0002", columns: []colRef{{table: "t", name: "c"}}, restore: restoreRule{needsDataUpgrade: true}},
	}
	if err := checkDumpMigrations([]string{"0001"}, steps); err == nil {
		t.Fatal("a dump that predates a data-upgrading step must be refused")
	}
	if err := checkDumpMigrations([]string{"0001", "0002"}, steps); err != nil {
		t.Errorf("a dump that carries the step must load: %v", err)
	}
	// The same step is fine to load FROM as long as the dump includes it.
	if err := checkDumpMigrations([]string{"0001", "0002"}, steps); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

// A restore is one transaction: clearing, loading, verifying and re-recording either all happen or
// none of them do. A dump that fails halfway must leave the database — and its ledger — exactly as it
// was, rather than empty or half-loaded.
func TestARestoreIsAtomic(t *testing.T) {
	dst := newTestStore(t)
	seedForBackup(t, dst)
	before := describe(t, dst.db)
	ledgerBefore := ledgerPicture(t, dst)

	header := `{"format":"report-portal-backup","version":1,"schema_version":2,"created_at":"2026-01-01T00:00:00Z","driver":"sqlite","app_version":"test","tables":["meta"]}`
	good := `{"table":"meta","columns":["k","v"],"row":["restored","1"]}`
	bad := `{"table":"no_such_table","columns":["a"],"row":["b"]}`
	dump := header + "\n" + good + "\n" + bad + "\n"

	if _, err := dst.restoreFrom(strings.NewReader(dump), true); err == nil {
		t.Fatal("a dump naming a table this build has no schema for must be refused")
	}
	if after := describe(t, dst.db); after != before {
		t.Errorf("a failed restore left the database changed:\n--- before ---\n%s--- after ---\n%s", before, after)
	}
	if after := ledgerPicture(t, dst); after != ledgerBefore {
		t.Errorf("a failed restore left the ledger changed:\n--- before ---\n%s--- after ---\n%s", ledgerBefore, after)
	}
}

// After a restore, the next start must find nothing to do: the ledger it rebuilt says so.
func TestASuccessfulRestoreLeavesNothingForTheNextStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.restoreFrom(bytes.NewReader(preMigrationDump(t)), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	after := ledgerPicture(t, st)
	st.Close()

	reopened, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening after a restore: %v", err)
	}
	defer reopened.Close()
	if got := ledgerPicture(t, reopened); got != after {
		t.Errorf("the next start rewrote the ledger:\n--- restored ---\n%s--- after ---\n%s", after, got)
	}
}

// A table a step creates must be in every dump from then on. Otherwise a future step that adds a table
// silently leaves that table's data out of backups, which is the kind of data loss nobody notices
// until a restore.
func TestBackupCoversTablesDeclaredByMigrations(t *testing.T) {
	steps := []migration{
		{id: "0001", columns: []colRef{{table: "user_groups", name: "c"}}},
		{id: "0002", tables: []string{"security_events"}},
	}
	got := backupTableNames([]string{"meta", "user_groups"}, steps)
	want := []string{"meta", "security_events", "user_groups"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("backup tables = %v, want %v", got, want)
	}
	// And a table declared twice is listed once.
	got = backupTableNames([]string{"meta"}, []migration{{id: "0003", tables: []string{"meta", "extra"}}})
	if strings.Join(got, ",") != "extra,meta" {
		t.Errorf("duplicate tables = %v, want [extra meta]", got)
	}
}
