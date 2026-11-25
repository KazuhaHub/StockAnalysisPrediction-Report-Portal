package app

import (
	"strconv"
	"strings"
	"testing"
)

// platformRewindToPre0022 winds a current store back to the shape a database has
// before step 0022: no ledger row and the frozen three-value operation list.
func platformRewindToPre0022(t *testing.T, st *Store) {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	if _, err := e.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("0022")); err != nil {
		t.Fatal(err)
	}
	if err := setPlatformSecretBindingOperationCheck(e, platformSecretBindingOperations(), platformLegacySecretBindingOperations()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func platformSetBindingOperation(st *Store, operation string) error {
	_, err := st.exec(`UPDATE app_platform_secret_bindings SET operation=?`, operation)
	return err
}

func testMigration0022WidensOperationCheck(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedCredentialRecords(t, st)
	for _, operation := range []string{"http_bearer", "http_api_key", "raw_read", "acs3_hmac_sha256", "pg_password", "ingress_hmac_sha256"} {
		if err := platformSetBindingOperation(st, operation); err != nil {
			t.Fatalf("fresh store refused operation %s: %v", operation, err)
		}
	}
	for _, operation := range []string{"invented", "ACS3_HMAC_SHA256", "acs3_hmac_sha256 ", ""} {
		if err := platformSetBindingOperation(st, operation); err == nil {
			t.Fatalf("fresh store admitted operation %q", operation)
		}
	}
	// Upgrade path: a populated pre-0022 shape keeps its rows and gains the values.
	if err := platformSetBindingOperation(st, "http_api_key"); err != nil {
		t.Fatal(err)
	}
	platformRewindToPre0022(t, st)
	if err := platformSetBindingOperation(st, "acs3_hmac_sha256"); err == nil {
		t.Fatal("rewound legacy shape still admits acs3_hmac_sha256")
	}
	if err := migration0016().verify(st.poolExec()); err != nil {
		t.Fatalf("0016 verifier refused the legacy shape: %v", err)
	}
	if err := migration0022().verify(st.poolExec()); err == nil {
		t.Fatal("0022 verifier accepted the legacy shape")
	}
	if err := st.runMigrations(); err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0022") {
		t.Fatal("0022 not recorded after upgrade")
	}
	if got := scalar[string](t, st, `SELECT operation FROM app_platform_secret_bindings`); got != "http_api_key" {
		t.Fatalf("upgrade rewrote the stored row: %s", got)
	}
	for _, operation := range []string{"acs3_hmac_sha256", "pg_password", "ingress_hmac_sha256"} {
		if err := platformSetBindingOperation(st, operation); err != nil {
			t.Fatalf("upgraded store refused %s: %v", operation, err)
		}
	}
	if err := platformSetBindingOperation(st, "invented"); err == nil {
		t.Fatal("upgraded store admitted an invented operation")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
	if !pg {
		if got := scalar[string](t, st, `PRAGMA integrity_check`); got != "ok" {
			t.Fatalf("integrity_check = %s", got)
		}
		rows, err := st.db.Query(`PRAGMA foreign_key_check`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		if rows.Next() {
			t.Fatal("foreign_key_check reported a violation after the rewrite")
		}
	}
}

func TestMigration0022WidensOperationCheckSQLite(t *testing.T) {
	t.Parallel()
	testMigration0022WidensOperationCheck(t, false)
}
func TestMigration0022WidensOperationCheckPostgres(t *testing.T) {
	t.Parallel()
	testMigration0022WidensOperationCheck(t, true)
}

// The constraint names are derived from the declaration; the approved change names them, so the
// derivation is pinned to the documented values for each driver.
func TestMigration0022ConstraintNames(t *testing.T) {
	t.Parallel()
	var table platformTable
	for _, candidate := range platformCredentialTables() {
		if candidate.name == "secret_bindings" {
			table = candidate
		}
	}
	for driver, want := range map[string]string{"postgres": "app_platform_secret_bindings_ck_15", "sqlite": "app_platform_secret_bindings_ck_29"} {
		got := ""
		for i, check := range table.checkExpressions(driver) {
			if check == platformSecretBindingOperations() {
				got = platformPrefix + table.name + "_ck_" + strconv.Itoa(i+1)
			}
		}
		if got != want {
			t.Fatalf("%s: operation check is %q, want %q", driver, got, want)
		}
	}
	name, err := platformSecretBindingOperationCheckName()
	if err != nil || name != "app_platform_secret_bindings_ck_15" {
		t.Fatalf("PostgreSQL constraint name = %q, %v", name, err)
	}
}

// A widened check that admits more than the three approved values is a weakened contract.
func testMigration0022VerifyRefusesAWeakenedCheck(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	e := migExec{s: st, ex: tx}
	weakened := platformSecretBindingOperations() + " OR (operation='anything')"
	if err := setPlatformSecretBindingOperationCheck(e, platformSecretBindingOperations(), weakened); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The PostgreSQL database is shared by the tests: put the declared list back.
	defer func() {
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := setPlatformSecretBindingOperationCheck(migExec{s: st, ex: tx}, weakened, platformSecretBindingOperations()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}()
	for _, m := range []migration{migration0016(), migration0022()} {
		if err := m.verify(st.poolExec()); err == nil {
			t.Fatalf("step %s verifier accepted a weakened operation check", m.id)
		}
	}
}
func TestMigration0022VerifyRefusesAWeakenedCheckSQLite(t *testing.T) {
	t.Parallel()
	testMigration0022VerifyRefusesAWeakenedCheck(t, false)
}
func TestMigration0022VerifyRefusesAWeakenedCheckPostgres(t *testing.T) {
	t.Parallel()
	testMigration0022VerifyRefusesAWeakenedCheck(t, true)
}

func testMigration0022Declaration(t *testing.T, pg bool) {
	step := migration0022()
	if step.id != "0022" || len(step.tables) != 0 || len(step.indices) != 0 || len(step.columns) != 0 || !step.restore.olderDumpOK {
		t.Fatalf("0022 must add no table, column or index and accept an older dump: %+v", step)
	}
	// The frozen step still creates the three-value list.
	if platformLegacySecretBindingOperations() != platformEnum("operation", "http_bearer", "http_api_key", "raw_read") {
		t.Fatal("0016's frozen operation list changed")
	}
}
func TestMigration0022DeclarationSQLite(t *testing.T) {
	t.Parallel()
	testMigration0022Declaration(t, false)
}

// PostgreSQL must find the constraint it is about to replace.
func TestSetPlatformSecretBindingOperationCheckPostgresRefusesAMissingConstraint(t *testing.T) {
	t.Parallel()
	st := platformTestStore(t, true)
	name, err := platformSecretBindingOperationCheckName()
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	if _, err := e.exec("ALTER TABLE app_platform_secret_bindings DROP CONSTRAINT " + name); err != nil {
		t.Fatal(err)
	}
	if err := setPlatformSecretBindingOperationCheck(e, platformSecretBindingOperations(), platformSecretBindingOperations()); err == nil {
		t.Fatal("a missing operation constraint was re-created silently")
	}
}

func TestSchemaUpgradeCLIAcceptsPre0022Database(t *testing.T) {
	t.Parallel()
	path := fileStore(t, "pre0022.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	platformRewindToPre0022(t, st)
	st.Close()
	cfg := writeSQLiteConfig(t, path)
	before := describeFile(t, path)
	out, err := SchemaUpgradeCommand(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[0022]") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("inspection did not report 0022 as the only pending step: %q", out)
	}
	if describeFile(t, path) != before {
		t.Fatal("inspection changed the database")
	}
	out, err = SchemaUpgradeCommand(cfg, []string{"--apply"})
	if err != nil || !strings.Contains(out, "[0022]") {
		t.Fatalf("--apply: %q %v", out, err)
	}
	upgraded, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if !recorded(upgraded, "0022") {
		t.Fatal("0022 not recorded")
	}
	platformSeedCredentialRecords(t, upgraded)
	if err := platformSetBindingOperation(upgraded, "acs3_hmac_sha256"); err != nil {
		t.Fatalf("upgraded file refused acs3_hmac_sha256 after reopen: %v", err)
	}
}
