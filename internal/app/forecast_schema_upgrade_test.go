package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgresPurgeOwnershipExplicitUpgrade(t *testing.T) {
	dsn := pgDSN(t)
	st, err := openStoreForMode("postgres", dsn, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TABLE prediction_purge_owners`, `DELETE FROM meta WHERE k='mig:0009'`} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	t.Cleanup(func() {
		repaired, err := openStoreForMode("postgres", dsn, true)
		if err != nil {
			t.Error(err)
			return
		}
		repaired.Close()
	})
	if opened, err := OpenStore("postgres", dsn); err == nil {
		opened.Close()
		t.Fatal("Postgres startup implicitly applied the explicit schema step")
	}
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("db_driver: postgres\ndb_dsn: %q\n", dsn)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SchemaUpgradeCommand(cfg, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := connectStore("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if raw.tableExists("prediction_purge_owners") || recorded(raw, "0009") {
		t.Fatal("Postgres dry run wrote the schema or ledger")
	}
	raw.Close()
	if _, err := SchemaUpgradeCommand(cfg, []string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner := seedOU(t, st, 0, scopeOwn)
	if _, err := st.exec(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`, predTestRtype, "1902-01", owner); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, st)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("Postgres to SQLite evidence restore: %v", err)
	}
	if got := scalar[int64](t, dst, `SELECT attributed_ou FROM prediction_purge_owners WHERE cohort='1902-01'`); got != owner {
		t.Fatalf("cross-driver owner = %d, want %d", got, owner)
	}
	if _, err := st.restoreFrom(bytes.NewReader(dumpOf(t, dst)), true); err != nil {
		t.Fatalf("SQLite to Postgres evidence restore: %v", err)
	}
	if got := scalar[int64](t, st, `SELECT attributed_ou FROM prediction_purge_owners WHERE cohort='1902-01'`); got != owner {
		t.Fatalf("reverse cross-driver owner = %d, want %d", got, owner)
	}
}

func TestPostgresPurgeOwnershipRequiresValidatedCheck(t *testing.T) {
	st := pgStore(t)
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: st, ex: tx}
	for _, q := range []string{
		`DROP TABLE prediction_purge_owners`,
		`CREATE TABLE prediction_purge_owners(rtype TEXT NOT NULL, cohort TEXT NOT NULL,
		attributed_ou BIGINT NOT NULL, PRIMARY KEY(rtype,cohort,attributed_ou))`,
		`INSERT INTO prediction_purge_owners VALUES('fixture','1900-01',0)`,
		`ALTER TABLE prediction_purge_owners ADD CONSTRAINT positive_owner CHECK(attributed_ou>0) NOT VALID`,
	} {
		if _, err := e.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifyPredictionPurgeOwners(e); err == nil {
		t.Fatal("unvalidated positive-owner check certified invalid historic rows")
	}
	if _, err := e.exec(`UPDATE prediction_purge_owners SET attributed_ou=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec(`ALTER TABLE prediction_purge_owners VALIDATE CONSTRAINT positive_owner`); err != nil {
		t.Fatal(err)
	}
	if err := verifyPredictionPurgeOwners(e); err != nil {
		t.Fatalf("validated positive control: %v", err)
	}
}

func TestPurgeOwnershipRejectsMalformedShape(t *testing.T) {
	for _, shape := range []string{
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL CHECK(attributed_ou>0)`,
		`rtype TEXT, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL CHECK(attributed_ou>0), PRIMARY KEY(rtype,cohort,attributed_ou)`,
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou REAL NOT NULL CHECK(attributed_ou>0), PRIMARY KEY(rtype,cohort,attributed_ou)`,
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL, PRIMARY KEY(rtype,cohort,attributed_ou)`,
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL CHECK(attributed_ou>0 OR 1=1), PRIMARY KEY(rtype,cohort,attributed_ou)`,
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL /* CHECK(attributed_ou>0) */, PRIMARY KEY(rtype,cohort,attributed_ou)`,
		`rtype TEXT NOT NULL, cohort TEXT NOT NULL, attributed_ou BIGINT NOT NULL CHECK("attributed_ou > 0"), PRIMARY KEY(rtype,cohort,attributed_ou)`,
	} {
		t.Run(shape, func(t *testing.T) {
			st := newTestStore(t)
			if _, err := st.exec(`DROP TABLE prediction_purge_owners`); err != nil {
				t.Fatal(err)
			}
			if _, err := st.exec(`CREATE TABLE prediction_purge_owners(` + shape + `)`); err != nil {
				t.Fatal(err)
			}
			if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err == nil {
				t.Fatal("recorded migration accepted malformed ownership evidence")
			}
		})
	}
}

func TestPurgeOwnershipExplicitUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portal.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if !recorded(st, "0009") || !st.tableExists("prediction_purge_owners") {
		t.Fatal("fresh initialization did not create and record ownership evidence")
	}
	if _, err := st.exec(`DROP TABLE prediction_purge_owners`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k IN ('mig:0005','mig:0009')`); err != nil {
		t.Fatal(err)
	}
	before := describe(t, st.db)
	st.Close()
	if opened, err := OpenStore("sqlite", path); err == nil {
		opened.Close()
		t.Fatal("startup implicitly upgraded an existing database")
	} else if !strings.Contains(err.Error(), "schema-upgrade --apply") {
		t.Fatalf("refusal omitted upgrade instructions: %v", err)
	}
	raw, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if after := describe(t, raw.db); after != before {
		t.Fatal("refused startup mutated the old database")
	}
	raw.Close()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("db_driver: sqlite\ndb_path: "+path+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := SchemaUpgradeCommand(cfg, nil); err != nil || !strings.Contains(out, "dry run") {
		t.Fatalf("upgrade dry run: %q, %v", out, err)
	}
	raw, err = connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if after := describe(t, raw.db); after != before {
		t.Fatal("upgrade dry run mutated the database")
	}
	raw.Close()
	if _, err := SchemaUpgradeCommand(cfg, []string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	st, err = OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first := ledgerPicture(t, st)
	if _, err := SchemaUpgradeCommand(cfg, []string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if after := ledgerPicture(t, st); after != first {
		t.Fatal("repeated explicit upgrade rewrote the ledger")
	}
	// The declared table must survive logical backup and restore without changing ownership.
	owner := seedOU(t, st, 0, scopeOwn)
	if _, err := st.exec(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`, predTestRtype, "2020-01", owner); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, st)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if got := scalar[int64](t, dst, `SELECT attributed_ou FROM prediction_purge_owners`); got != owner {
		t.Fatalf("restored owner = %d, want %d", got, owner)
	}
	missingHeader := withoutPurgeEvidence(t, dump, true)
	parts := bytes.SplitN(missingHeader, []byte("\n"), 2)
	var header backupHeader
	if err := json.Unmarshal(parts[0], &header); err != nil {
		t.Fatal(err)
	}
	header.Tables = append(header.Tables, "prediction_purge_owners")
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	missingSection := append(append(encoded, '\n'), parts[1]...)
	for _, malformed := range [][]byte{missingHeader, missingSection} {
		for _, force := range []bool{false, true} {
			before := describe(t, dst.db)
			if _, err := dst.restoreFrom(bytes.NewReader(malformed), force); err == nil {
				t.Fatal("backup claiming 0009 without evidence table was accepted")
			}
			if after := describe(t, dst.db); after != before {
				t.Fatal("refused evidence-less backup changed the target")
			}
		}
	}
	if _, err := dst.restoreFrom(bytes.NewReader(withoutPurgeEvidence(t, dump, false)), true); err != nil {
		t.Fatalf("legitimate pre-0009 backup: %v", err)
	}
	if n := scalar[int](t, dst, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatal("old backup retained or guessed ownership evidence")
	}
}

func TestSchemaUpgradeInspectionDoesNotCreateFiles(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "absent.yaml")
	if _, err := SchemaUpgradeCommand(cfg, []string{"--unexpected"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("unexpected argument was not refused: %v", err)
	}
	if _, err := SchemaUpgradeCommand(cfg, nil); err == nil {
		t.Fatal("inspection accepted missing config")
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("inspection created config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "absent.db")
	if err := os.WriteFile(cfg, []byte("db_driver: sqlite\ndb_path: "+path+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := SchemaUpgradeCommand(cfg, nil); err == nil {
		t.Fatal("inspection accepted missing database")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("inspection created database: %v", err)
	}
}

func withoutPurgeEvidence(t *testing.T, dump []byte, claim bool) []byte {
	t.Helper()
	removedSteps := map[string]bool{}
	removedTables := map[string]bool{"prediction_purge_owners": true}
	pastPurge := false
	for _, step := range newTestStore(t).migrations() {
		pastPurge = pastPurge || step.id == "0009"
		if !claim && pastPurge {
			removedSteps[step.id] = true
			for _, name := range step.tables {
				removedTables[name] = true
			}
		}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	table := ""
	for i, line := range bytes.Split(bytes.TrimSpace(dump), []byte("\n")) {
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			tables := []any{}
			for _, name := range value["tables"].([]any) {
				if !removedTables[name.(string)] {
					tables = append(tables, name)
				}
			}
			value["tables"] = tables
			if !claim {
				// A historical dump ends before 0009, including every later appended step.
				migrations := []any{}
				for _, id := range value["migrations"].([]any) {
					if !removedSteps[id.(string)] {
						migrations = append(migrations, id)
					}
				}
				value["migrations"] = migrations
			}
		}
		if name, ok := value["table"].(string); ok {
			table = name
		}
		if removedTables[table] {
			continue
		}
		if !claim && table == "meta" {
			if row, ok := value["row"].([]any); ok && len(row) > 0 && removedSteps[strings.TrimPrefix(fmt.Sprint(row[0]), migrationLedgerPrefix)] {
				continue
			}
		}
		if err := enc.Encode(value); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

// runMigrations is the entry point the startup path and the other migration tests share: it applies
// the automatic steps and holds back an explicit one that the database has not recorded.
func TestRunMigrationsHoldsBackAnUnrecordedExplicitStep(t *testing.T) {
	st := newTestStore(t)
	if err := st.runMigrations(); err != nil {
		t.Fatalf("a fully upgraded database: %v", err)
	}
	for _, q := range []string{`DROP TABLE prediction_purge_owners`, `DELETE FROM meta WHERE k='mig:0009'`} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	err := st.runMigrations()
	if err == nil || !strings.Contains(err.Error(), "schema-upgrade --apply") {
		t.Fatalf("an unrecorded explicit step was not refused with the upgrade instructions: %v", err)
	}
	// The operator who meets this message is told where the whole procedure is written down, and
	// that path has to exist.
	const runbook = "docs/forecast-purge-ownership-upgrade.md"
	if !strings.Contains(err.Error(), runbook) {
		t.Fatalf("the refusal does not name the runbook %s: %v", runbook, err)
	}
	if _, statErr := os.Stat(filepath.Join("..", "..", runbook)); statErr != nil {
		t.Fatalf("the refusal names a runbook that does not exist: %v", statErr)
	}
	if st.tableExists("prediction_purge_owners") || recorded(st, "0009") {
		t.Fatal("the refused run created the explicit step")
	}
}

// The migration list is in merge order, not id order. The ids 0007-0009 were reserved by branches that
// were still open when 0010 reached main, so each reserved step is appended after whatever has already
// shipped. The explicit purge evidence step (0009) therefore follows 0010.
func TestPurgeEvidenceFollowsReleasedPrefixAndIsTheOnlyExplicitStep(t *testing.T) {
	steps := newTestStore(t).migrations()
	ids := make([]string, len(steps))
	seen := map[string]bool{}
	for i, m := range steps {
		ids[i] = m.id
		if seen[m.id] {
			t.Fatalf("steps %v: %s appears twice", ids[:i+1], m.id)
		}
		seen[m.id] = true
		if m.explicit != (m.id == "0009") {
			t.Errorf("step %s explicit = %v; only 0009 may be explicit", m.id, m.explicit)
		}
	}
	if len(ids) < 8 || ids[6] != "0010" || ids[7] != "0009" {
		t.Fatalf("steps %v: 0009 must follow the released 0010 prefix", ids)
	}
	for i := 0; i < 6; i++ {
		if want := fmt.Sprintf("%04d", i+1); ids[i] != want {
			t.Fatalf("steps %v: position %d is %s, want %s", ids, i+1, ids[i], want)
		}
	}
	if err := checkDumpMigrations(ids[:7], steps); err != nil {
		t.Fatalf("a dump written without 0009 is not a prefix of the list %v: %v", ids, err)
	}
}
