package app

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

// platformRewindToPre0024 winds a current store back to the shape a database has before step 0024: no
// quota_inodes column, the 0021 action list, the legacy stage list and no ledger row.
// rewindPlatformQuotaStep undoes what step 0024 changed in the schema: the two widened lists and the new column.
func rewindPlatformQuotaStep(e migExec) error {
	if err := rewindPlatformQuotaChecks(e); err != nil {
		return err
	}
	if _, err := e.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey("0024")); err != nil {
		return err
	}
	if !e.columnExists(platformPrefix+"data_namespaces", "quota_inodes") {
		return nil
	}
	_, err := e.exec(`ALTER TABLE app_platform_data_namespaces DROP COLUMN quota_inodes`)
	return err
}

func platformRewindToPre0024(t *testing.T, st *Store) {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	if err := rewindPlatformQuotaStep(e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func testMigration0024Schema(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	for i, action := range []string{"set_data_quota", "import_data"} {
		if p, o := platformInsertActionRows(st, 10+i, action); p != nil || o != nil {
			t.Fatalf("%s refused: %v %v", action, p, o)
		}
	}
	if p, _ := platformInsertActionRows(st, 20, "invented"); p == nil {
		t.Fatal("an invented action was admitted")
	}
	// A fresh namespace row defaults to today's 65,536 inodes; the new column is NOT NULL.
	mustExec(t, st, `INSERT INTO app_platform_data_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms)
		VALUES('`+platformFixtureID(70)+`','`+platformFixtureID(1)+`','`+platformFixtureID(1)+`','active',1,268435456,0,1,1,NULL,NULL,1,1)`)
	if scalar[int](t, st, `SELECT quota_inodes FROM app_platform_data_namespaces`) != 65536 {
		t.Fatal("quota_inodes does not default to 65536")
	}
	if _, err := st.exec(`UPDATE app_platform_data_namespaces SET quota_inodes=NULL`); err == nil {
		t.Fatal("quota_inodes admitted NULL")
	}
	for _, bad := range []string{"65535", "1000001", "0", "-1"} {
		if _, err := st.exec(`UPDATE app_platform_data_namespaces SET quota_inodes=` + bad); err == nil {
			t.Fatalf("quota_inodes=%s admitted", bad)
		}
	}
	mustExec(t, st, `UPDATE app_platform_data_namespaces SET quota_inodes=1000000`)
	mustExec(t, st, `UPDATE app_platform_data_namespaces SET quota_inodes=65536`)
	if err := migration0024().verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	if pg {
		// A different default is a different contract.
		mustExec(t, st, `ALTER TABLE app_platform_data_namespaces ALTER COLUMN quota_inodes SET DEFAULT 70000`)
		if err := migration0024().verify(st.poolExec()); err == nil {
			t.Fatal("the verifier accepted a changed default")
		}
		mustExec(t, st, `ALTER TABLE app_platform_data_namespaces ALTER COLUMN quota_inodes SET DEFAULT 65536`)
	}
	// The import stage is allowed by the schema; the executor is a later phase.
	mustExec(t, st, `UPDATE app_platform_operations SET stage='import',completed_ms=NULL WHERE id='`+platformFixtureID(2010)+`'`)
	if _, err := st.exec(`UPDATE app_platform_operations SET stage='invented',completed_ms=NULL WHERE id='` + platformFixtureID(2010) + `'`); err == nil {
		t.Fatal("an invented stage was admitted")
	}
}

func TestMigration0024SchemaSQLite(t *testing.T)   { testMigration0024Schema(t, false) }
func TestMigration0024SchemaPostgres(t *testing.T) { testMigration0024Schema(t, true) }

func testMigration0024Upgrade(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	step := migration0024()
	if len(step.tables) != 0 || len(step.columns) != 1 || step.columns[0].name != "quota_inodes" || !step.restore.olderDumpOK {
		t.Fatalf("0024 declares %v %v olderDumpOK=%v", step.tables, step.columns, step.restore.olderDumpOK)
	}
	platformSeedActionFixtures(t, st)
	mustExec(t, st, `INSERT INTO app_platform_data_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms)
		VALUES('`+platformFixtureID(70)+`','`+platformFixtureID(1)+`','`+platformFixtureID(1)+`','active',1,268435456,0,1,1,NULL,NULL,1,1)`)
	if p, o := platformInsertActionRows(st, 1, "restore_import"); p != nil || o != nil {
		t.Fatal(p, o)
	}
	platformRewindToPre0024(t, st)
	if err := step.verify(st.poolExec()); err == nil {
		t.Fatal("0024 verifier accepted a store before the step")
	}
	if p, _ := platformInsertActionRows(st, 2, "set_data_quota"); p == nil {
		t.Fatal("a pre-0024 store admitted set_data_quota")
	}
	if err := st.runMigrations(); err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0024") {
		t.Fatal("0024 not recorded after upgrade")
	}
	if scalar[int](t, st, `SELECT quota_inodes FROM app_platform_data_namespaces`) != 65536 || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE action='restore_import'`) != 1 {
		t.Fatal("existing rows did not keep their values")
	}
	if p, o := platformInsertActionRows(st, 3, "import_data"); p != nil || o != nil {
		t.Fatalf("upgraded store refused import_data: %v %v", p, o)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0024UpgradeSQLite(t *testing.T)   { testMigration0024Upgrade(t, false) }
func TestMigration0024UpgradePostgres(t *testing.T) { testMigration0024Upgrade(t, true) }

// withoutColumn removes one column from a table's section of a dump, as a dump written before the step
// that added the column would not have it.
func withoutColumn(t *testing.T, dump []byte, table, column string) []byte {
	t.Helper()
	var out bytes.Buffer
	drop := -1
	for _, line := range bytes.Split(bytes.TrimRight(dump, "\n"), []byte("\n")) {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		if raw, ok := m["table"]; ok {
			var name string
			json.Unmarshal(raw, &name)
			drop = -1
			if name == table {
				var cols []string
				json.Unmarshal(m["columns"], &cols)
				drop = slices.Index(cols, column)
				if drop < 0 {
					t.Fatalf("%s has no column %s", table, column)
				}
				cols = slices.Delete(cols, drop, drop+1)
				m["columns"], _ = json.Marshal(cols)
				line, _ = json.Marshal(m)
			}
		} else if raw, ok := m["row"]; ok && drop >= 0 {
			var row []any
			json.Unmarshal(raw, &row)
			m["row"], _ = json.Marshal(slices.Delete(row, drop, drop+1))
			line, _ = json.Marshal(m)
		}
		out.Write(line)
		out.WriteByte('\n')
	}
	return out.Bytes()
}

func TestMigration0024IsTheLastStepAndAnOlderDumpRestoresWithDefaultInodes(t *testing.T) {
	steps := newTestStore(t).migrations()
	if steps[len(steps)-1].id != "0024" {
		t.Fatalf("last step = %s", steps[len(steps)-1].id)
	}
	src := newTestStore(t)
	platformSeedActionFixtures(t, src)
	mustExec(t, src, `INSERT INTO app_platform_data_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,writer_fence,revision,retained_ms,deleted_ms,created_ms,updated_ms,quota_inodes)
		VALUES('`+platformFixtureID(70)+`','`+platformFixtureID(1)+`','`+platformFixtureID(1)+`','active',1,268435456,0,1,1,NULL,NULL,1,1,100000)`)
	dump := withoutSections(t, dumpOf(t, src), nil, []string{"0024"})
	dump = withoutColumn(t, dump, "app_platform_data_namespaces", "quota_inodes")
	if bytes.Contains(dump, []byte("quota_inodes")) || bytes.Contains(dump, []byte(`"0024"`)) {
		t.Fatal("fixture still describes 0024")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if !recorded(dst, "0024") || scalar[int](t, dst, `SELECT quota_inodes FROM app_platform_data_namespaces`) != 65536 {
		t.Fatal("restored store lacks the default quota_inodes")
	}
}
