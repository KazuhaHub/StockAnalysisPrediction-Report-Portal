package app

import (
	"bytes"
	"strings"
	"testing"
)

// Step 0014 (ADR 0048): app ordering and per-OU app access.

func TestMigration0014AddsExactlyTheTwoApprovedTables(t *testing.T) {
	path := fileStore(t, "m14.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := migration0014()
	if got := strings.Join(m.tables, ","); got != "app_order,app_ou_access" {
		t.Fatalf("tables = %s", got)
	}
	var cols []string
	for _, c := range m.columns {
		cols = append(cols, c.table+"."+c.name)
	}
	if got, want := strings.Join(cols, ","), "app_order.app_key,app_order.position,app_ou_access.app_key,app_ou_access.group_id,app_ou_access.enabled"; got != want {
		t.Fatalf("columns = %s, want %s", got, want)
	}
	if !m.restore.olderDumpOK {
		t.Error("a step that writes no data must restore from an older dump")
	}
	if !recorded(st, "0014") {
		t.Fatal("0014 not in the ledger after a normal open")
	}
	for _, c := range m.columns {
		if !st.columnExists(c.table, c.name) {
			t.Errorf("%s.%s missing", c.table, c.name)
		}
	}
	for i := 0; i < 2; i++ { // a re-run after a crash between the DDL and the ledger row
		if err := m.up(st.poolExec()); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{m}); err != nil {
		t.Fatal(err)
	}
	// No rows by default: every app open, ordered as before.
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM app_order`); n != 0 {
		t.Fatalf("app_order has %d rows on a fresh database", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM app_ou_access`); n != 0 {
		t.Fatalf("app_ou_access has %d rows on a fresh database", n)
	}
	for _, stmt := range st.baseSchemaStmts() {
		if strings.Contains(stmt, "app_order") || strings.Contains(stmt, "app_ou_access") {
			t.Fatalf("the frozen baseline declares the step's tables: %s", stmt)
		}
	}
}

func TestMigration0014RefusesADatabaseMissingItsTables(t *testing.T) {
	path := fileStore(t, "m14-missing.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	mutateFile(t, path, `DROP TABLE app_ou_access`)
	if st, err := OpenStore("sqlite", path); err == nil {
		st.Close()
		t.Fatal("a database missing a table the ledger says exists was accepted")
	} else if !strings.Contains(err.Error(), "0014") {
		t.Fatalf("refusal does not name the step: %v", err)
	}
}

func TestMigration0014OlderDumpRestoresWithEmptyTables(t *testing.T) {
	src := newTestStore(t)
	// A database before the step: no ledger row. A dump carries the build's tables, not the ledger's,
	// so they are empty.
	if _, err := src.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("0014")); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	if _, err := dst.exec(`INSERT INTO app_order(app_key,position) VALUES('builtin:batch',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM app_order`); n != 0 {
		t.Fatalf("the restore left %d rows in app_order", n)
	}
	if !recorded(dst, "0014") {
		t.Fatal("0014 is not recorded after restoring an older dump")
	}
}

func TestBackupRoundTripCarriesAppOrderAndAccess(t *testing.T) {
	src := newTestStore(t)
	ou := seedOU(t, src, 0, nil)
	if _, err := src.exec(`INSERT INTO app_order(app_key,position) VALUES('builtin:quotes',1),('hello',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := src.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES('hello',?,0)`, ou); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n := scalar[int](t, dst, `SELECT position FROM app_order WHERE app_key='builtin:quotes'`); n != 1 {
		t.Fatalf("position = %d", n)
	}
	if n := scalar[int](t, dst, `SELECT enabled FROM app_ou_access WHERE app_key='hello' AND group_id=?`, ou); n != 0 {
		t.Fatalf("enabled = %d", n)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM app_order`); n != 2 {
		t.Fatalf("app_order rows = %d", n)
	}
}

func TestPostgresAppLayoutStore(t *testing.T) {
	st := pgStore(t, "app_ou_access", "app_order")
	defer func() {
		st.exec(`DELETE FROM app_ou_access`)
		st.exec(`DELETE FROM app_order`)
	}()
	if err := st.ReplaceAppOrder([]string{"builtin:quotes", "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceAppOrder([]string{"hello"}); err != nil {
		t.Fatal(err)
	}
	order, err := st.appOrderMap()
	if err != nil || len(order) != 1 || order["hello"] != 1 {
		t.Fatalf("order = %v err %v", order, err)
	}
	ou := st.DefaultGroupID()
	closed := false
	if err := st.setAppAccessRow(st.poolExec(), "hello", ou, &closed); err != nil {
		t.Fatal(err)
	}
	if err := st.setAppAccessRow(st.poolExec(), "hello", ou, &closed); err != nil { // upsert
		t.Fatal(err)
	}
	rows, err := st.appAccessRows()
	if err != nil || rows["hello"][ou] != false || len(rows["hello"]) != 1 {
		t.Fatalf("rows = %v err %v", rows, err)
	}
	if err := st.setAppAccessRow(st.poolExec(), "hello", ou, nil); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM app_ou_access`); n != 0 {
		t.Fatalf("null did not delete the row: %d", n)
	}
}
