package app

import (
	"bytes"
	"strings"
	"testing"
)

// The owner approved exactly this table (doc 191): the statement below is the approved text and the
// step must create nothing else. Fails if a column, type, nullability or key differs.
const approvedAppAdminsDDL = `CREATE TABLE IF NOT EXISTS app_platform_app_admins(
  installation_id TEXT NOT NULL,
  username TEXT NOT NULL,
  account_created TEXT NOT NULL,
  granted_by TEXT NOT NULL,
  granted_at BIGINT NOT NULL,
  PRIMARY KEY(installation_id, username))`

func TestMigration0025MatchesTheApprovedTable(t *testing.T) {
	norm := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if norm(platformAppAdminsDDL) != norm(approvedAppAdminsDDL) {
		t.Fatalf("DDL differs from the approved table:\n got %s", platformAppAdminsDDL)
	}
	step := migration0025()
	if strings.Join(step.tables, ",") != "app_platform_app_admins" || len(step.indices) != 0 || !step.restore.olderDumpOK || len(step.columns) != 5 {
		t.Fatalf("0025 declares %v %v olderDumpOK=%v", step.tables, step.indices, step.restore.olderDumpOK)
	}
}

func TestMigration0025UpgradeAndOlderDump(t *testing.T) {
	st := newTestStore(t)
	step := migration0025()
	mustExec(t, st, `DROP TABLE app_platform_app_admins`)
	mustExec(t, st, `DELETE FROM meta WHERE k='`+migrationLedgerKey("0025")+`'`)
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{step}); err == nil {
		t.Fatal("verifier accepted a store without the table")
	}
	if err := st.runMigrations(); err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0025") {
		t.Fatal("0025 not recorded after upgrade")
	}
	mustExec(t, st, `INSERT INTO app_platform_app_admins(installation_id,username,account_created,granted_by,granted_at) VALUES('i','u','c','a',1)`)
	if _, err := st.exec(`INSERT INTO app_platform_app_admins(installation_id,username,account_created,granted_by,granted_at) VALUES('i','u','c2','a',2)`); err == nil {
		t.Fatal("duplicate (installation, username) admitted")
	}
	// A dump from before the step restores with an empty table.
	src := newTestStore(t)
	dump := withoutSections(t, dumpOf(t, src), step.tables, []string{"0025"})
	if bytes.Contains(dump, []byte("app_platform_app_admins")) {
		t.Fatal("fixture still describes 0025")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if !recorded(dst, "0025") || scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_app_admins`) != 0 {
		t.Fatal("restored store lacks an empty 0025 table")
	}
}
