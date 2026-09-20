package app

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
)

// The migration ledger on Postgres.
//
// Everything here has a SQLite twin, and the reason to repeat it is that the two drivers disagree
// about exactly the things this machinery depends on: SQLite serialises writers with one connection
// and one lock, Postgres takes several at once and needs the advisory lock to decide who migrates; a
// DROP COLUMN and an ADD COLUMN are spelled the same but are not the same operation under a
// transaction. A framework proven on one driver is half a framework.

func pgDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	return dsn
}

// windBackToThePreviousRelease removes this release's additions, leaving a database the release
// before it could have written.
func windBackToThePreviousRelease(t *testing.T, st *Store) {
	t.Helper()
	for _, c := range []string{"totp_enroll", "passkey_enroll", "require_2fa"} {
		if _, err := st.exec(`ALTER TABLE user_groups DROP COLUMN IF EXISTS ` + c); err != nil {
			t.Fatalf("winding back %s: %v", c, err)
		}
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k LIKE 'mig:%'`); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresMigrationsCatchUpAndStayPut is the upgrade path on Postgres: a database at the
// baseline, which the previous release wrote, gains the steps once and is left alone afterwards.
func TestPostgresMigrationsCatchUpAndStayPut(t *testing.T) {
	dsn := pgDSN(t)
	st := pgStore(t)
	windBackToThePreviousRelease(t, st)
	st.Close()

	migrated, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("a baseline database must be accepted and migrated: %v", err)
	}
	for _, c := range []string{"totp_enroll", "passkey_enroll", "require_2fa"} {
		if !migrated.columnExists("user_groups", c) {
			t.Errorf("column %s is missing after the migration", c)
		}
	}
	first := ledgerPicture(t, migrated)
	migrated.Close()

	again, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	if second := ledgerPicture(t, again); second != first {
		t.Errorf("the second open rewrote the ledger:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// Two processes starting against one database must not both migrate it. On Postgres that is the
// advisory lock's job, and without it the loser would either duplicate the DDL or fail on a conflict
// it should have waited out.
func TestPostgresConcurrentMigrationsSerialise(t *testing.T) {
	dsn := pgDSN(t)
	first := pgStore(t)
	// Cleanups run last-registered-first, so the ledger is cleaned while the stores are still open
	// (a plain `defer` would run before them and find a closed database).
	t.Cleanup(func() { first.Close() })
	second, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })

	steps := func() []migration {
		return []migration{
			{id: "9300", up: func(e migExec) error { return nil }},
			{id: "9301", up: func(e migExec) error { return nil }},
			{id: "9302", up: func(e migExec) error { return nil }},
		}
	}
	// Clean the ledger for these ids so both runners have work to do, and clean it again afterwards:
	// this database is shared with every other Postgres test, and an unknown ledger row left behind
	// makes the NEXT test's OpenStore refuse the database — which is the framework working, not a
	// flake, so the test that writes those rows is the test that has to take them away.
	ids := []string{"9300", "9301", "9302"}
	clearLedger := func() {
		for _, id := range ids {
			if _, err := first.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey(id)); err != nil {
				t.Fatalf("cleaning the ledger for %s: %v", id, err)
			}
		}
	}
	clearLedger()
	t.Cleanup(clearLedger)

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
			t.Errorf("migrator %d: %v", i, err)
		}
	}
	for _, id := range []string{"9300", "9301", "9302"} {
		if n := ledgerRowsFor(t, first, id); n != 1 {
			t.Errorf("ledger rows for %s = %d, want 1", id, n)
		}
	}
}

// A dump written by the previous release — baseline shape, no ledger — must load into a Postgres
// deployment too. The dump is portable by design, and this is the migration boundary crossed with a
// driver change on top of it.
func TestPostgresRestoresADumpFromBeforeAMigration(t *testing.T) {
	st := pgStore(t)
	defer st.Close()

	dump := preMigrationDump(t) // sqlite-shaped dump: the format is one format
	if _, err := st.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	for _, c := range []string{"totp_enroll", "passkey_enroll", "require_2fa"} {
		if !st.columnExists("user_groups", c) {
			t.Errorf("column %s is missing after the restore", c)
		}
	}
	if got := scalar[string](t, st, `SELECT name FROM user_groups WHERE id=1`); got != "安禅内部" {
		t.Errorf("restored group name = %q", got)
	}
	for _, m := range st.migrations() {
		if !recorded(st, m.id) {
			t.Errorf("step %s was not recorded by the restore", m.id)
		}
	}
}

// A step that fails must not leave a half-applied schema on Postgres either: the DDL is transactional
// there too, so the rollback takes the column back out.
func TestPostgresFailedStepRollsBack(t *testing.T) {
	st := pgStore(t)
	defer st.Close()
	if _, err := st.exec(`ALTER TABLE user_groups DROP COLUMN IF EXISTS mig_probe_col`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("9400")); err != nil {
		t.Fatal(err)
	}

	step := migration{
		id:      "9400",
		columns: []colRef{{table: "user_groups", name: "mig_probe_col"}},
		up: func(e migExec) error {
			if _, err := e.exec(`ALTER TABLE user_groups ADD COLUMN mig_probe_col INTEGER`); err != nil {
				return err
			}
			return errors.New("injected: failed after the DDL")
		},
	}
	if err := st.runMigrationSteps([]migration{step}); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if st.columnExists("user_groups", "mig_probe_col") {
		t.Error("the failed step's column survived its rollback")
	}
	if recorded(st, "9400") {
		t.Error("a failed step was recorded")
	}
	if err := st.runMigrationSteps([]migration{{
		id:      "9400",
		columns: []colRef{{table: "user_groups", name: "mig_probe_col"}},
		up: func(e migExec) error {
			if e.columnExists("user_groups", "mig_probe_col") {
				return nil
			}
			_, err := e.exec(`ALTER TABLE user_groups ADD COLUMN mig_probe_col INTEGER`)
			return err
		},
	}}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if !st.columnExists("user_groups", "mig_probe_col") {
		t.Error("the retried step did not produce its column")
	}
	// Leave the shared test database as it was found.
	if _, err := st.exec(`ALTER TABLE user_groups DROP COLUMN IF EXISTS mig_probe_col`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("9400")); err != nil {
		t.Fatal(err)
	}
}
