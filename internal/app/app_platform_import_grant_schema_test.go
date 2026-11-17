package app

import (
	"fmt"
	"strings"
	"testing"
)

// platformRewindToPre0021 winds a current store back to the shape a database has before step 0021:
// no import-grant table, no ledger row, and the legacy action list on the two checked tables.
func platformRewindToPre0021(t *testing.T, st *Store) {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	if _, err := e.exec(`DROP TABLE app_platform_import_grants`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("0021")); err != nil {
		t.Fatal(err)
	}
	if err := setPlatformActionChecks(e, platformActions(), platformLegacyActions()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func platformInsertActionRows(st *Store, n int, action string) (planErr, opErr error) {
	hash := strings.Repeat("a", 64)
	_, planErr = st.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,
		source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,
		actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,
		consumed_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,NULL,NULL,1,1,1,?,?,NULL,'',0,?,?,'{}',1,NULL,1,1)`,
		platformFixtureID(1000+n), platformFixtureID(1), "fixture-app", "fixture-publisher", action,
		platformFixtureID(99), hash, hash, hash)
	if planErr != nil {
		return planErr, nil
	}
	_, opErr = st.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,
		idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,
		row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,1,1,2,'complete',4,1,NULL,NULL,0,?,NULL,1,1,1)`,
		platformFixtureID(2000+n), platformFixtureID(1), platformFixtureID(1000+n), hash,
		fmt.Sprintf("%064d", n), hash, action, platformFixtureID(99))
	return nil, opErr
}

func platformSeedActionFixtures(t *testing.T, st *Store) {
	t.Helper()
	platformSeedPublisher(t, st.poolExec(), "fixture-publisher")
	if err := platformSeedInstallation(t, st.poolExec(), platformFixtureID(1), "fixture-publisher", "fixture-app"); err != nil {
		t.Fatal(err)
	}
}

func testMigration0021RelaxesActionCheck(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	if p, o := platformInsertActionRows(st, 1, "restore_import"); p != nil || o != nil {
		t.Fatalf("fresh store refused restore_import: %v %v", p, o)
	}
	if p, o := platformInsertActionRows(st, 2, "invented"); p == nil {
		t.Fatalf("review_plans admitted an invented action: %v", o)
	}
	// An operation row must refuse it too, independently of the plan table.
	if _, err := st.exec(`UPDATE app_platform_operations SET action='invented'`); err == nil {
		t.Fatal("operations admitted an invented action")
	}
	if _, err := st.exec(`UPDATE app_platform_review_plans SET action='invented'`); err == nil {
		t.Fatal("review_plans admitted an invented action by update")
	}
	// Upgrade path: a populated pre-0021 shape keeps its rows and gains the new action.
	if p, o := platformInsertActionRows(st, 3, "restore"); p != nil || o != nil {
		t.Fatal(p, o)
	}
	mustExec(t, st, `DELETE FROM app_platform_operations WHERE action='restore_import'`)
	mustExec(t, st, `DELETE FROM app_platform_review_plans WHERE action='restore_import'`)
	platformRewindToPre0021(t, st)
	if p, _ := platformInsertActionRows(st, 4, "restore_import"); p == nil {
		t.Fatal("rewound legacy shape still admits restore_import")
	}
	if err := migration0015().verify(st.poolExec()); err != nil {
		t.Fatalf("0015 verifier refused the legacy shape: %v", err)
	}
	if err := migration0021().verify(st.poolExec()); err == nil {
		t.Fatal("0021 verifier accepted the legacy shape")
	}
	if err := st.runMigrations(); err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0021") {
		t.Fatal("0021 not recorded after upgrade")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_operations WHERE action='restore'`) != 1 ||
		scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE action='restore'`) != 1 {
		t.Fatal("upgrade lost or rewrote existing rows")
	}
	if p, o := platformInsertActionRows(st, 5, "restore_import"); p != nil || o != nil {
		t.Fatalf("upgraded store refused restore_import: %v %v", p, o)
	}
	if p, _ := platformInsertActionRows(st, 6, "invented"); p == nil {
		t.Fatal("upgraded store admitted an invented action")
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

func TestMigration0021RelaxesActionCheckSQLite(t *testing.T) {
	testMigration0021RelaxesActionCheck(t, false)
}
func TestMigration0021RelaxesActionCheckPostgres(t *testing.T) {
	testMigration0021RelaxesActionCheck(t, true)
}

func testMigration0021VerifyOnUpgradedAndFresh(t *testing.T, pg bool) {
	fresh := platformTestStore(t, pg)
	for _, m := range []migration{migration0015(), migration0021()} {
		if err := m.verify(fresh.poolExec()); err != nil {
			t.Fatalf("fresh store, step %s: %v", m.id, err)
		}
	}
	step := migration0021()
	if got := strings.Join(step.tables, ","); got != "app_platform_import_grants" {
		t.Fatalf("0021 tables = %s", got)
	}
	if len(step.indices) != 3 || !step.restore.olderDumpOK {
		t.Fatalf("0021 declares %d indexes, olderDumpOK=%v", len(step.indices), step.restore.olderDumpOK)
	}
	platformRewindToPre0021(t, fresh)
	if err := fresh.runMigrations(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []migration{migration0015(), migration0021()} {
		if err := m.verify(fresh.poolExec()); err != nil {
			t.Fatalf("upgraded store, step %s: %v", m.id, err)
		}
	}
	if err := fresh.verifyMigrationProducts(fresh.poolExec(), fresh.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0021VerifyOnUpgradedAndFreshSQLite(t *testing.T) {
	testMigration0021VerifyOnUpgradedAndFresh(t, false)
}
func TestMigration0021VerifyOnUpgradedAndFreshPostgres(t *testing.T) {
	testMigration0021VerifyOnUpgradedAndFresh(t, true)
}

// A widened check that admits more than restore_import is a weakened contract, not an upgrade.
func TestMigration0021VerifyRefusesAWeakenedActionCheck(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	e := migExec{s: st, ex: tx}
	if err := setPlatformActionChecks(e, platformActions(), platformActions()+" OR (action='anything')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []migration{migration0015(), migration0021()} {
		if err := m.verify(st.poolExec()); err == nil {
			t.Fatalf("step %s verifier accepted a weakened action check", m.id)
		}
	}
}

func TestMigration0021ImportGrantConstraints(t *testing.T) {
	st := newTestStore(t)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// Foreign keys are deferred, so parents need not exist for the immediate constraints to be tested.
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	insert := func(id, set, runtime, state string, consumed any) error {
		_, err := e.exec(`INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,1,1)`, id, set, runtime, platformFixtureID(99), state, consumed)
		return err
	}
	set1, set2, rt1, rt2 := platformFixtureID(11), platformFixtureID(12), platformFixtureID(21), platformFixtureID(22)
	if err := insert(platformFixtureID(31), set1, rt1, "issued", nil); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"second issued grant for one recovery set": insert(platformFixtureID(32), set1, rt2, "issued", nil),
		"same recovery set and runtime twice":      insert(platformFixtureID(33), set1, rt1, "consumed", 5),
		"consumed without a timestamp":             insert(platformFixtureID(34), set2, rt1, "consumed", nil),
		"issued with a timestamp":                  insert(platformFixtureID(35), set2, rt1, "issued", 5),
		"revoked with a timestamp":                 insert(platformFixtureID(36), set2, rt1, "revoked", 5),
		"unknown state":                            insert(platformFixtureID(37), set2, rt1, "pending", nil),
	} {
		if err == nil {
			t.Fatalf("%s was admitted", name)
		}
	}
	if err := insert(platformFixtureID(38), set2, rt1, "consumed", 5); err != nil {
		t.Fatal(err)
	}
	if err := insert(platformFixtureID(39), set1, rt2, "revoked", nil); err != nil {
		t.Fatal("a revoked grant must not block another runtime:", err)
	}
	// A revoked grant is history: it coexists with the live one for the same recovery set and runtime.
	if err := insert(platformFixtureID(40), set1, rt1, "revoked", nil); err != nil {
		t.Fatal("a revoked grant must not block a retry on the same runtime:", err)
	}
}

func TestSchemaUpgradeCLIAcceptsPre0021Database(t *testing.T) {
	path := fileStore(t, "pre0021.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	platformRewindToPre0021(t, st)
	st.Close()
	cfg := writeSQLiteConfig(t, path)
	before := describeFile(t, path)

	out, err := SchemaUpgradeCommand(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[0021]") || !strings.Contains(out, "nothing was changed") {
		t.Fatalf("inspection did not report 0021 as the only pending step: %q", out)
	}
	if describeFile(t, path) != before {
		t.Fatal("inspection changed the database")
	}
	out, err = SchemaUpgradeCommand(cfg, []string{"--apply"})
	if err != nil || !strings.Contains(out, "[0021]") {
		t.Fatalf("--apply: %q %v", out, err)
	}
	upgraded, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if !recorded(upgraded, "0021") {
		t.Fatal("0021 not recorded")
	}
	platformSeedActionFixtures(t, upgraded)
	if p, o := platformInsertActionRows(upgraded, 1, "restore_import"); p != nil || o != nil {
		t.Fatalf("upgraded file refused restore_import after reopen: %v %v", p, o)
	}
}

// PostgreSQL must find the constraint it is about to replace. A missing one means the table is not the
// shape this step expects, and re-adding a constraint over it would hide that.
func TestSetPlatformActionChecksPostgresRefusesAMissingConstraint(t *testing.T) {
	st := platformTestStore(t, true)
	name, err := platformActionCheckName("review_plans")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	if _, err := e.exec("ALTER TABLE app_platform_review_plans DROP CONSTRAINT " + name); err != nil {
		t.Fatal(err)
	}
	if err := setPlatformActionChecks(e, platformActions(), platformActions()); err == nil {
		t.Fatal("a missing action constraint was re-created silently")
	}
}
