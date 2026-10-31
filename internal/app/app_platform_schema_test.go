package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
)

func TestAppPlatformControlSchemaProducts(t *testing.T) {
	st := newTestStore(t)
	step := migration0015()
	if len(step.tables) != 14 || len(step.indices) != 24 {
		t.Fatalf("wrong approved products: %d tables, %d indexes", len(step.tables), len(step.indices))
	}
	if !recorded(st, "0015") {
		t.Fatal("control migration not recorded")
	}
	if err := step.verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	if len(step.columns) < 100 {
		t.Fatal("column products not declared")
	}
}

func TestAppPlatformSqliteForeignKeysSurviveConnectionReplacement(t *testing.T) {
	st, err := OpenStore("sqlite", filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for range 2 {
		var enabled int
		if err := st.queryRow("PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
			t.Fatalf("foreign keys disabled: %d %v", enabled, err)
		}
		st.db.SetMaxIdleConns(0)
		conn, err := st.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		st.db.SetMaxIdleConns(1)
	}
}

func TestPostgresAppPlatformControlSchema(t *testing.T) {
	st := pgStore(t)
	if err := migration0015().verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
}

func TestAppPlatformSchemaComparisonPreservesMeaning(t *testing.T) {
	if compactPlatformSQL("CHECK(public.octet_length(id)=36)") == compactPlatformSQL("CHECK(octet_length(id)=36)") {
		t.Fatal("qualified function identity erased")
	}
	if compactPlatformSQL("CHECK(octet_ifnotexistslength(id)=36)") == compactPlatformSQL("CHECK(octet_length(id)=36)") {
		t.Fatal("function identifier bytes erased")
	}
	for _, suffix := range []string{"::text", "public.", "ondeletenoaction", "ifnotexists"} {
		original := "phase='ready" + suffix + "'"
		if compactPlatformSQL(original) == compactPlatformSQL("phase='ready'") {
			t.Fatalf("literal bytes erased: %s", suffix)
		}
	}
	left := canonicalPlatformBoolean("(a=1) AND ((b=2) OR (c=3))")
	right := canonicalPlatformBoolean("((a=1) AND (b=2)) OR (c=3)")
	if left == right {
		t.Fatal("boolean grouping erased")
	}
	if canonicalPlatformBoolean("((a=1) AND (b=2)) AND (c=3)") != canonicalPlatformBoolean("(a=1) AND ((b=2) AND (c=3))") {
		t.Fatal("equivalent conjunctions differ")
	}
}

func platformFixtureID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func platformSeedPublisher(t *testing.T, e migExec, name string) {
	t.Helper()
	if _, err := e.exec(`INSERT INTO app_platform_publishers(id,namespaces_json,enabled,revision,created_ms,updated_ms) VALUES(?,'[]',0,1,1,1)`, name); err != nil {
		t.Fatal(err)
	}
}
func platformSeedInstallation(t *testing.T, e migExec, id, publisher, appID string) error {
	t.Helper()
	_, err := e.exec(`INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms)
    VALUES(?,?,?,NULL,'enabled','pending',1,1,1,0,?,'fixture-envelope',1,1)`, id, appID, publisher, platformFixtureID(99))
	return err
}
func platformTestStore(t *testing.T, postgres bool) *Store {
	t.Helper()
	if !postgres {
		return newTestStore(t)
	}
	st := pgStore(t)
	clean := func() {
		tx, err := st.db.Begin()
		if err != nil {
			t.Error(err)
			return
		}
		defer tx.Rollback()
		tables := append(migration0015().tables, migration0016().tables...)
		tables = append(tables, migration0017().tables...)
		sort.Strings(tables)
		for _, name := range tables {
			if _, err := tx.Exec("DELETE FROM " + name); err != nil {
				t.Error(err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Error(err)
		}
	}
	clean()
	t.Cleanup(clean)
	return st
}

func testPlatformConstraints(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	platformSeedPublisher(t, st.poolExec(), "fixture-publisher")
	if err := platformSeedInstallation(t, st.poolExec(), platformFixtureID(1), "fixture-publisher", "fixture-app"); err != nil {
		t.Fatal(err)
	}
	if err := platformSeedInstallation(t, st.poolExec(), platformFixtureID(2), "fixture-publisher", "fixture-app"); err == nil {
		t.Fatal("two live installations admitted")
	}
	if _, err := st.exec(`UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, platformFixtureID(1)); err != nil {
		t.Fatal(err)
	}
	if err := platformSeedInstallation(t, st.poolExec(), platformFixtureID(2), "fixture-publisher", "fixture-app"); err != nil {
		t.Fatal("removed tombstone blocked fresh identity:", err)
	}
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	e := migExec{s: st, ex: tx}
	if err := platformSeedInstallation(t, e, platformFixtureID(3), "late-publisher", "late-app"); err != nil {
		tx.Rollback()
		t.Fatal("foreign key checked before transaction completion:", err)
	}
	platformSeedPublisher(t, e, "late-publisher")
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := platformSeedInstallation(t, migExec{s: st, ex: tx}, platformFixtureID(4), "missing-publisher", "missing-app"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("orphan reference committed")
	}
	if _, err := st.exec(`UPDATE app_platform_installations SET phase='ready' WHERE id=?`, platformFixtureID(2)); err != nil {
		t.Fatal("connection unusable after failed commit:", err)
	}
	if !postgres {
		if _, err := st.exec(`UPDATE app_platform_installations SET generation=1.5`); err == nil {
			t.Fatal("SQLite stored a fractional generation")
		}
	}
	for _, statement := range []string{
		`UPDATE app_platform_installations SET generation=0`,
		`UPDATE app_platform_installations SET phase='invented'`,
		`UPDATE app_platform_installations SET revision=NULL`,
		`UPDATE app_platform_installations SET generation='invalid'`,
	} {
		if _, err := st.exec(statement); err == nil {
			t.Fatalf("invalid state accepted: %s", statement)
		}
	}
}
func TestAppPlatformConstraints(t *testing.T)         { testPlatformConstraints(t, false) }
func TestPostgresAppPlatformConstraints(t *testing.T) { testPlatformConstraints(t, true) }

func testPlatformSchemaTamper(t *testing.T, postgres bool) {
	st := platformTestStore(t, postgres)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP INDEX uq_ap_installation_live`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE INDEX uq_ap_installation_live ON app_platform_installations(app_id)`); err != nil {
		t.Fatal(err)
	}
	if err := migration0015().verify(migExec{s: st, ex: tx}); err == nil {
		t.Fatal("weakened uniqueness accepted under the same product name")
	}
}
func TestAppPlatformSchemaRejectsTamperedIndex(t *testing.T) { testPlatformSchemaTamper(t, false) }
func TestPostgresAppPlatformSchemaRejectsTamperedIndex(t *testing.T) {
	testPlatformSchemaTamper(t, true)
}

func TestPostgresAppPlatformSchemaRejectsWeakenedCheck(t *testing.T) {
	st := platformTestStore(t, true)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE app_platform_installations DROP CONSTRAINT app_platform_installations_ck_1`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`ALTER TABLE app_platform_installations ADD CONSTRAINT app_platform_installations_ck_1 CHECK(octet_length(id)<=36)`); err != nil {
		t.Fatal(err)
	}
	if err := migration0015().verify(migExec{s: st, ex: tx}); err == nil {
		t.Fatal("weakened length constraint accepted")
	}
}

func TestAppPlatformSchemaRejectsDisabledForeignKeys(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	if err := migration0015().verify(st.poolExec()); err == nil {
		t.Fatal("disabled enforcement accepted")
	}
}
