package app

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// platformRewindToPre0023 winds a current store back to the shape a database has before step 0023: no
// machine token table and no ledger row.
func platformRewindToPre0023(t *testing.T, st *Store) {
	t.Helper()
	mustExec(t, st, `DROP TABLE app_platform_machine_tokens`)
	mustExec(t, st, `DELETE FROM meta WHERE k='`+migrationLedgerKey("0023")+`'`)
}

// docMachineDDL reads the two CREATE TABLE statements and the index statements that doc 183 section
// 10 gives, so the generated DDL is compared with the approved text rather than with itself.
func docMachineDDL(t *testing.T) (postgres, sqlite string, indexes []string) {
	t.Helper()
	raw, err := os.ReadFile("../../docs/app-platform/183-machine-read-api.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	block := func(after string) string {
		i := strings.Index(text, after)
		if i < 0 {
			t.Fatalf("doc 183 has no %q", after)
		}
		rest := text[i:]
		start := strings.Index(rest, "```sql\n")
		end := strings.Index(rest[start+7:], "```")
		return rest[start+7 : start+7+end]
	}
	squash := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, ",\n", ", ")), " ") }
	postgres, sqlite = squash(block("\nPostgreSQL:\n")), squash(block("\nSQLite (the generator"))
	for _, line := range strings.Split(block("\nIndexes (both dialects):"), "\n") {
		if strings.TrimSpace(line) != "" {
			indexes = append(indexes, strings.TrimSpace(line))
		}
	}
	return postgres, sqlite, indexes
}

func TestMigration0023MatchesTheApprovedDDL(t *testing.T) {
	postgres, sqlite, indexes := docMachineDDL(t)
	table := platformMachineTokenTables()[0]
	// The doc lays the statement out over lines and ends it with a semicolon; only that differs.
	norm := func(s string) string { return strings.TrimSuffix(strings.Join(strings.Fields(s), ""), ";") }
	if got := norm(table.ddl("postgres")); got != norm(postgres) {
		t.Fatalf("PostgreSQL DDL differs from doc 183 section 10:\n got %s\nwant %s", got, postgres)
	}
	if got := norm(table.ddl("sqlite")); got != norm(sqlite) {
		t.Fatalf("SQLite DDL differs from doc 183 section 10:\n got %s\nwant %s", got, sqlite)
	}
	var got []string
	for _, i := range platformMachineTokenIndexes() {
		got = append(got, i.ddl()+";")
	}
	if strings.Join(got, "\n") != strings.Join(indexes, "\n") {
		t.Fatalf("indexes differ from doc 183 section 10:\n got %v\nwant %v", got, indexes)
	}
}

func machineTokenInsert(st *Store, id, installation, handle, secret, state string, revoked any, expires int64) error {
	_, err := st.exec(`INSERT INTO app_platform_machine_tokens(id,installation_id,handle,label,secret_sha256,routes_json,per_minute,source_cidrs_json,state,policy_revision,created_by,revision,expires_ms,created_ms,updated_ms,revoked_ms,last_used_ms)
		VALUES(?,?,?,?,?,'["a"]',60,'[]',?,1,'admin',1,?,10,10,?,NULL)`, id, installation, handle, "Dify", secret, state, expires, revoked)
	return err
}

func testMigration0023Table(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	platformSeedActionFixtures(t, st)
	inst := platformFixtureID(1)
	sha := func(c string) string { return strings.Repeat(c, 64) }
	if err := machineTokenInsert(st, platformFixtureID(51), inst, "abcdefghijkl", sha("a"), "active", nil, 20); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"duplicate handle":           machineTokenInsert(st, platformFixtureID(52), inst, "abcdefghijkl", sha("b"), "active", nil, 20),
		"duplicate secret digest":    machineTokenInsert(st, platformFixtureID(53), inst, "bbbbbbbbbbbb", sha("a"), "active", nil, 20),
		"unknown state":              machineTokenInsert(st, platformFixtureID(54), inst, "cccccccccccc", sha("c"), "review_required", nil, 20),
		"revoked without a time":     machineTokenInsert(st, platformFixtureID(55), inst, "dddddddddddd", sha("d"), "revoked", nil, 20),
		"active with a revoked time": machineTokenInsert(st, platformFixtureID(56), inst, "eeeeeeeeeeee", sha("e"), "active", 15, 20),
		"expiry not after creation":  machineTokenInsert(st, platformFixtureID(57), inst, "ffffffffffff", sha("f"), "active", nil, 10),
		"short digest":               machineTokenInsert(st, platformFixtureID(58), inst, "gggggggggggg", "abc", "active", nil, 20),
		"long handle":                machineTokenInsert(st, platformFixtureID(59), inst, strings.Repeat("h", 17), sha("g"), "active", nil, 20),
	} {
		if err == nil {
			t.Fatalf("%s was admitted", name)
		}
	}
	if _, err := st.exec(`UPDATE app_platform_machine_tokens SET per_minute=601`); err == nil {
		t.Fatal("601 per minute admitted")
	}
	if _, err := st.exec(`UPDATE app_platform_machine_tokens SET per_minute=0`); err == nil {
		t.Fatal("0 per minute admitted")
	}
	if _, err := st.exec(`UPDATE app_platform_machine_tokens SET updated_ms=9`); err == nil {
		t.Fatal("updated before created admitted")
	}
	if err := machineTokenInsert(st, platformFixtureID(60), inst, "iiiiiiiiiiii", sha("i"), "revoked", 15, 20); err != nil {
		t.Fatal(err)
	}
	// A token never outlives its installation.
	mustExec(t, st, `DELETE FROM app_platform_machine_tokens`)
	if err := machineTokenInsert(st, platformFixtureID(61), inst, "jjjjjjjjjjjj", sha("j"), "active", nil, 20); err != nil {
		t.Fatal(err)
	}
	mustExec(t, st, `DELETE FROM app_platform_installations WHERE id='`+inst+`'`)
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_machine_tokens`) != 0 {
		t.Fatal("deleting the installation left its tokens")
	}
}

func TestMigration0023TableSQLite(t *testing.T)   { testMigration0023Table(t, false) }
func TestMigration0023TablePostgres(t *testing.T) { testMigration0023Table(t, true) }

func testMigration0023Upgrade(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	step := migration0023()
	if strings.Join(step.tables, ",") != "app_platform_machine_tokens" || len(step.indices) != 4 || !step.restore.olderDumpOK {
		t.Fatalf("0023 declares %v, %d indexes, olderDumpOK=%v", step.tables, len(step.indices), step.restore.olderDumpOK)
	}
	if err := step.verify(st.poolExec()); err != nil {
		t.Fatal(err)
	}
	platformRewindToPre0023(t, st)
	if err := step.verify(st.poolExec()); err == nil {
		t.Fatal("0023 verifier accepted a store without the table")
	}
	if err := st.runMigrations(); err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0023") {
		t.Fatal("0023 not recorded after upgrade")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0023UpgradeSQLite(t *testing.T)   { testMigration0023Upgrade(t, false) }
func TestMigration0023UpgradePostgres(t *testing.T) { testMigration0023Upgrade(t, true) }

func TestMigration0023IsTheLastStepAndAnOlderDumpRestoresWithAnEmptyTable(t *testing.T) {
	steps := newTestStore(t).migrations()
	if steps[len(steps)-1].id != "0023" {
		t.Fatalf("last step = %s", steps[len(steps)-1].id)
	}
	src := newTestStore(t)
	dump := withoutSections(t, dumpOf(t, src), migration0023().tables, []string{"0023"})
	if bytes.Contains(dump, []byte("app_platform_machine_tokens")) || bytes.Contains(dump, []byte(`"0023"`)) {
		t.Fatal("fixture still describes 0023")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if !recorded(dst, "0023") || scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_machine_tokens`) != 0 {
		t.Fatal("restored store lacks an empty 0023 table")
	}
}
