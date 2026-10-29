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

// A Postgres database that lacks step 0009 gains it at normal startup, the optional offline tool
// inspects it without writing and applies it with the same runner, and the dumps that carry the
// evidence cross drivers in both directions.
func TestPostgresPurgeOwnershipAppliesAtStartup(t *testing.T) {
	dsn := pgDSN(t)
	st := pgStore(t)
	windBackPostgresPurgeEvidence(t, st)

	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf("db_driver: postgres\ndb_dsn: %q\n", dsn)), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := SchemaUpgradeCommand(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[0009]") {
		t.Fatalf("Postgres inspection did not list the pending step: %q", out)
	}
	if st.tableExists("prediction_purge_owners") || recorded(st, "0009") {
		t.Fatal("Postgres inspection wrote the schema or ledger")
	}
	if _, err := SchemaUpgradeCommand(cfg, []string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if !st.tableExists("prediction_purge_owners") || !recorded(st, "0009") {
		t.Fatal("Postgres --apply did not create and record the evidence table")
	}

	// The same database again, this time through normal startup and nothing else.
	windBackPostgresPurgeEvidence(t, st)
	st.Close()
	opened, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("Postgres startup must apply step 0009 itself: %v", err)
	}
	defer opened.Close()
	if !opened.tableExists("prediction_purge_owners") || !recorded(opened, "0009") {
		t.Fatal("Postgres startup did not create and record the evidence table")
	}
	if n := scalar[int](t, opened, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table was created with %d rows, want none", n)
	}
	if err := opened.verifyMigrationProducts(opened.poolExec(), opened.migrations()); err != nil {
		t.Fatal(err)
	}
}

// The Postgres twin of the SQLite test below for a database whose ledger ends at 0006: all later
// steps apply at ordinary startup in merge order, and the evidence table starts empty next to a
// historic global marker.
func TestPostgresExistingDatabaseAtStep0006UpgradesAtNormalStartup(t *testing.T) {
	dsn := pgDSN(t)
	st := pgStore(t)
	for _, q := range []string{
		`ALTER TABLE announcements DROP COLUMN IF EXISTS popup_dismissible`,
		`ALTER TABLE announcements RENAME COLUMN strip_dismissible TO dismissible`,
		`DROP TABLE IF EXISTS prediction_purge_owners`,
		`DELETE FROM meta WHERE k IN ('mig:0010','mig:0009')`,
	} {
		if _, err := st.exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for _, q := range rewindNotificationAndBackendStatements() {
		if _, err := st.exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := st.exec(`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES(?,?,?,?)`,
		predTestRtype, "1903-01", "2026-10-01T00:00:00Z", 3); err != nil {
		t.Fatal(err)
	}
	// The database is shared with other tests, which re-stamp middle steps of the ledger, so only what
	// this startup writes is checked: it must not be older than anything that was there before.
	floor := newestLedgerStamp(st)
	st.Close()

	opened, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("an existing supported Postgres database must upgrade at normal startup: %v", err)
	}
	t.Cleanup(func() {
		if _, err := opened.exec(`DELETE FROM prediction_purges WHERE cohort='1903-01'`); err != nil {
			t.Error(err)
		}
		opened.Close()
	})
	requireStepsRecordedSince(t, opened, floor, "0010", "0009", "0007", "0008")
	if !opened.tableExists("prediction_purge_owners") || !opened.columnExists("announcements", "popup_dismissible") {
		t.Fatal("startup recorded the steps without their products")
	}
	if n := scalar[int](t, opened, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows after the upgrade; historic markers must not be backfilled", n)
	}
	if n := scalar[int](t, opened, `SELECT COUNT(*) FROM prediction_purges WHERE cohort='1903-01'`); n != 1 {
		t.Fatalf("the historic purge marker count = %d, want 1", n)
	}
	if err := opened.verifyMigrationProducts(opened.poolExec(), opened.migrations()); err != nil {
		t.Fatal(err)
	}
}

// The Postgres twin of TestBaselineDatabaseUpgradesAtNormalStartup: a database with no ledger at all,
// wound back past every step including 0010 and 0009, is carried forward by ordinary startup. Every
// ledger row is rewritten by this one startup, so the whole ledger can be checked against list order.
func TestPostgresBaselineDatabaseUpgradesAtNormalStartup(t *testing.T) {
	dsn := pgDSN(t)
	st := pgStore(t)
	stmts := []string{
		`DELETE FROM meta WHERE k LIKE 'mig:%'`,
		`ALTER TABLE user_groups DROP COLUMN IF EXISTS totp_enroll`,
		`ALTER TABLE user_groups DROP COLUMN IF EXISTS passkey_enroll`,
		`ALTER TABLE user_groups DROP COLUMN IF EXISTS require_2fa`,
		`ALTER TABLE user_groups DROP COLUMN IF EXISTS forecast_scope`,
		`ALTER TABLE users DROP COLUMN IF EXISTS last_used_version`,
		`DROP TABLE IF EXISTS user_sessions`,
		`DROP TABLE IF EXISTS login_history`,
		`ALTER TABLE users DROP COLUMN IF EXISTS login_history_keep`,
		`ALTER TABLE users DROP COLUMN IF EXISTS login_history_initialized`,
		`ALTER TABLE cleanup_runs DROP COLUMN IF EXISTS predictions_deleted`,
		`ALTER TABLE announcements DROP COLUMN IF EXISTS popup_dismissible`,
		`DROP TABLE IF EXISTS prediction_purge_owners`,
	}
	for _, tbl := range predictionTables {
		stmts = append(stmts, `DROP TABLE IF EXISTS `+tbl)
	}
	if st.columnExists("announcements", "strip_dismissible") {
		stmts = append(stmts, `ALTER TABLE announcements RENAME COLUMN strip_dismissible TO dismissible`)
	}
	for _, q := range stmts {
		if _, err := st.exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if st.tableExists("prediction_purge_owners") || recorded(st, "0001") || recorded(st, "0009") || recorded(st, "0010") ||
		st.columnExists("announcements", "popup_dismissible") || !st.columnExists("announcements", "dismissible") {
		t.Fatal("the fixture is not a ledger-less baseline database")
	}
	st.Close()

	opened, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("a baseline Postgres database must upgrade at normal startup: %v", err)
	}
	defer opened.Close()
	requireStepsRecordedInListOrder(t, opened)
	if !opened.tableExists("prediction_purge_owners") || !opened.columnExists("announcements", "popup_dismissible") ||
		opened.columnExists("announcements", "dismissible") {
		t.Fatal("startup recorded the steps without their products")
	}
	if n := scalar[int](t, opened, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows after the upgrade", n)
	}
	if err := opened.verifyMigrationProducts(opened.poolExec(), opened.migrations()); err != nil {
		t.Fatal(err)
	}
}

func windBackPostgresPurgeEvidence(t *testing.T, st *Store) {
	t.Helper()
	for _, q := range []string{`DROP TABLE IF EXISTS prediction_purge_owners`, `DELETE FROM meta WHERE k='mig:0009'`} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPostgresPurgeEvidenceCrossDriverRestore(t *testing.T) {
	st := pgStore(t)
	owner := seedOU(t, st, 0, scopeOwn)
	if _, err := st.exec(`INSERT INTO prediction_purge_owners(rtype,cohort,attributed_ou) VALUES(?,?,?)`, predTestRtype, "1902-01", owner); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := st.exec(`DELETE FROM prediction_purge_owners WHERE cohort='1902-01'`); err != nil {
			t.Error(err)
		}
	})
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

// ---------- fixtures ----------

// writeSQLiteConfig writes the infrastructure config the schema-upgrade command reads.
func writeSQLiteConfig(t *testing.T, dbPath string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfg, []byte("db_driver: sqlite\ndb_path: "+dbPath+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// backBeforePurgeEvidence winds a migrated SQLite file back to what a release without step 0009
// leaves: no evidence table and no ledger row for it.
func backBeforePurgeEvidence(t *testing.T, path string) {
	t.Helper()
	mutateFile(t, path, `DROP TABLE prediction_purge_owners`, `DELETE FROM meta WHERE k='mig:0009'`)
}

// backToStep0006 winds a migrated SQLite file back to a database whose ledger ends at step 0006: the
// shape the release before 0010 and 0009 left, with a historic global purge marker already in it.
func backToStep0006(t *testing.T, path string) {
	t.Helper()
	backBeforeStep0010(t, path)
	backBeforePurgeEvidence(t, path)
	mutateFile(t, path, rewindNotificationAndBackendStatements()...)
	mutateFile(t, path, fmt.Sprintf(
		`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES('%s','2025-06','2026-10-01T00:00:00Z',3)`,
		predTestRtype))
}

// Later notification/backend products cannot remain in a step-0006 fixture.
func rewindNotificationAndBackendStatements() []string {
	var statements []string
	for _, step := range []migration{migration0013(), migration0012(), migration0011(), migration0008(), migration0007()} {
		for i := len(step.tables) - 1; i >= 0; i-- {
			statements = append(statements, "DROP TABLE IF EXISTS "+step.tables[i])
		}
		statements = append(statements, "DELETE FROM meta WHERE k='"+migrationLedgerKey(step.id)+"'")
	}
	return statements
}

// dirListing is the names, sizes and modification times in a directory, so a test can prove a command
// left no file behind and rewrote none.
func dirListing(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %d %d\n", e.Name(), info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}

// ledgerFor is the ledger rows of the named steps as one comparable string.
func ledgerFor(t *testing.T, st *Store, ids ...string) string {
	t.Helper()
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(id + "=" + st.GetSetting(migrationLedgerKey(id), "") + "\n")
	}
	return b.String()
}

// requireStepsRecordedInListOrder checks that every step this build knows is recorded, and that the
// record order never runs against the list order (the stamps are RFC 3339 instants, so they sort).
func requireStepsRecordedInListOrder(t *testing.T, st *Store) {
	t.Helper()
	previous := ""
	for _, m := range st.migrations() {
		stamp := st.GetSetting(migrationLedgerKey(m.id), "")
		if stamp == "" {
			t.Fatalf("step %s is not recorded after startup", m.id)
		}
		if stamp < previous {
			t.Fatalf("step %s was recorded at %s, before the step ahead of it in the list (%s)", m.id, stamp, previous)
		}
		previous = stamp
	}
}

// newestLedgerStamp is the latest ledger instant of any step this build knows, or "" when none is
// recorded. RFC 3339 instants in UTC sort as strings.
func newestLedgerStamp(st *Store) string {
	newest := ""
	for _, m := range st.migrations() {
		if stamp := st.GetSetting(migrationLedgerKey(m.id), ""); stamp > newest {
			newest = stamp
		}
	}
	return newest
}

// requireStepsRecordedSince checks only the named steps: each is recorded, none is older than floor,
// and the stamps never run against the order the ids are given. For a database other tests also write
// to, where the stamps of steps this test never applied say nothing about it. The stamps have
// one-second resolution, so steps applied in the same second tie; TestPendingStepsApplyInListOrder is
// what proves the order itself.
func requireStepsRecordedSince(t *testing.T, st *Store, floor string, ids ...string) {
	t.Helper()
	previous := floor
	for _, id := range ids {
		stamp := st.GetSetting(migrationLedgerKey(id), "")
		if stamp == "" {
			t.Fatalf("step %s is not recorded after startup", id)
		}
		if stamp < previous {
			t.Fatalf("step %s was recorded at %s, before %s", id, stamp, previous)
		}
		previous = stamp
	}
}

// ---------- upgrading at normal startup ----------

// A database a release before 0010 and 0009 left, with its ledger at 0006, is carried forward by
// ordinary startup: all later steps are applied and recorded, nothing is asked of an operator, and the
// evidence table starts empty. A historic global purge marker proves nothing about OU membership,
// so it is neither copied nor altered.
func TestExistingDatabaseAtStep0006UpgradesAtNormalStartup(t *testing.T) {
	path := fileStore(t, "at-0006.db")
	backToStep0006(t, path)

	raw, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.tableExists("prediction_purge_owners") || recorded(raw, "0009") || recorded(raw, "0010") ||
		raw.columnExists("announcements", "popup_dismissible") || !recorded(raw, "0006") {
		t.Fatal("the fixture is not a database whose ledger ends at 0006")
	}
	earlier := ledgerFor(t, raw, "0001", "0002", "0003", "0004", "0005", "0006")
	raw.Close()

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("an existing supported database must upgrade at normal startup: %v", err)
	}
	defer st.Close()
	requireStepsRecordedInListOrder(t, st)
	if !st.tableExists("prediction_purge_owners") || !st.columnExists("announcements", "popup_dismissible") {
		t.Fatal("startup recorded the steps without their products")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows after the upgrade; historic markers must not be backfilled", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purges`); n != 1 {
		t.Fatalf("historic purge markers = %d, want the one that was there", n)
	}
	if got := ledgerFor(t, st, "0001", "0002", "0003", "0004", "0005", "0006"); got != earlier {
		t.Fatalf("the upgrade rewrote the ledger of steps that were already applied:\n%s\nbecame\n%s", earlier, got)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
	first := ledgerPicture(t, st)
	st.Close()

	again, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening an upgraded database: %v", err)
	}
	defer again.Close()
	if second := ledgerPicture(t, again); second != first {
		t.Fatal("the second start rewrote the ledger")
	}
}

// A database at the frozen baseline, with no ledger at all, takes the same path through every step,
// 0010 and 0009 included.
func TestBaselineDatabaseUpgradesAtNormalStartup(t *testing.T) {
	path := fileStore(t, "baseline.db")
	backToTheBaseline(t, path)
	backBeforeStep0010(t, path)
	backBeforePurgeEvidence(t, path)

	raw, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if raw.tableExists("prediction_purge_owners") || recorded(raw, "0001") {
		t.Fatal("the fixture is not a ledger-less baseline database")
	}
	raw.Close()

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("a baseline database must upgrade at normal startup: %v", err)
	}
	defer st.Close()
	requireStepsRecordedInListOrder(t, st)
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows after the upgrade", n)
	}
	if err := st.verifyMigrationProducts(st.poolExec(), st.migrations()); err != nil {
		t.Fatal(err)
	}
}

// The runner applies what is pending in the order the list gives, so 0010 goes in before 0009 even
// though its id is higher.
func TestPendingStepsApplyInListOrder(t *testing.T) {
	st := newTestStore(t)
	// A later recorded step must retain its original time when an earlier step is replayed.
	if _, err := st.exec(`UPDATE meta SET v='2000-01-01T00:00:00Z' WHERE k IN ('mig:0007','mig:0008')`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`ALTER TABLE announcements DROP COLUMN popup_dismissible`,
		`ALTER TABLE announcements RENAME COLUMN strip_dismissible TO dismissible`,
		`DROP TABLE prediction_purge_owners`,
		`DELETE FROM meta WHERE k IN ('mig:0010','mig:0009')`,
	} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var applied []string
	steps := st.migrations()
	var preserved []string
	for _, step := range steps {
		if step.id != "0010" && step.id != "0009" {
			preserved = append(preserved, step.id)
		}
	}
	before, floor := ledgerFor(t, st, preserved...), newestLedgerStamp(st)
	for i := range steps {
		id, up := steps[i].id, steps[i].up
		steps[i].up = func(e migExec) error {
			applied = append(applied, id)
			return up(e)
		}
	}
	if err := st.runMigrationSteps(steps); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(applied, ","); got != "0010,0009" {
		t.Fatalf("steps applied = %s, want 0010,0009", got)
	}
	requireStepsRecordedSince(t, st, floor, "0010", "0009")
	if after := ledgerFor(t, st, preserved...); after != before {
		t.Fatal("replaying pending steps rewrote existing ledger timestamps")
	}
}

func TestStep0006FixtureHasOnlyPrefixProductsAndLedger(t *testing.T) {
	path := fileStore(t, "prefix-0006.db")
	backToStep0006(t, path)
	st, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	afterPrefix := false
	for _, step := range st.migrations() {
		if afterPrefix {
			if recorded(st, step.id) {
				t.Errorf("fixture retained later ledger step %s", step.id)
			}
			for _, table := range step.tables {
				if st.tableExists(table) {
					t.Errorf("fixture retained later product %s", table)
				}
			}
		}
		if step.id == "0006" {
			afterPrefix = true
		}
	}
}

// The startup entry point itself has no step it holds back.
func TestRunMigrationsAppliesAnUnrecordedPurgeEvidenceStep(t *testing.T) {
	st := newTestStore(t)
	if err := st.runMigrations(); err != nil {
		t.Fatalf("a fully upgraded database: %v", err)
	}
	for _, q := range []string{`DROP TABLE prediction_purge_owners`, `DELETE FROM meta WHERE k='mig:0009'`} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.runMigrations(); err != nil {
		t.Fatalf("an unrecorded step must be applied, not refused: %v", err)
	}
	if !st.tableExists("prediction_purge_owners") || !recorded(st, "0009") {
		t.Fatal("the step was not created and recorded")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows, want none", n)
	}
}

// ---------- verification still holds the line ----------

const counterfeitPurgeOwners = `CREATE TABLE prediction_purge_owners(rtype TEXT NOT NULL, cohort TEXT NOT NULL,
	attributed_ou BIGINT NOT NULL, PRIMARY KEY(rtype,cohort,attributed_ou))`

// Startup does not trust a ledger row: a recorded step whose table lost its positive-owner check is
// refused.
func TestStartupRefusesARecordedButDamagedEvidenceTable(t *testing.T) {
	path := fileStore(t, "damaged-recorded.db")
	mutateFile(t, path, `DROP TABLE prediction_purge_owners`, counterfeitPurgeOwners)
	if opened, err := OpenStore("sqlite", path); err == nil {
		opened.Close()
		t.Fatal("startup accepted a recorded step with a counterfeit evidence table")
	} else if !strings.Contains(err.Error(), "prediction_purge_owners") {
		t.Fatalf("the refusal does not name the damaged table: %v", err)
	}
}

// A lookalike table in front of an unrecorded step is not adopted: the step's transaction verifies it
// before recording anything, so the refusal leaves the ledger unrecorded and the table untouched.
func TestStartupRefusesADamagedEvidenceTableWithoutRecordingTheStep(t *testing.T) {
	path := fileStore(t, "damaged-unrecorded.db")
	mutateFile(t, path, `DROP TABLE prediction_purge_owners`, counterfeitPurgeOwners, `DELETE FROM meta WHERE k='mig:0009'`)
	before := describeFile(t, path)
	if opened, err := OpenStore("sqlite", path); err == nil {
		opened.Close()
		t.Fatal("startup adopted a counterfeit evidence table")
	} else if !strings.Contains(err.Error(), "prediction_purge_owners") {
		t.Fatalf("the refusal does not name the damaged table: %v", err)
	}
	raw, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if recorded(raw, "0009") {
		t.Fatal("the refused step was recorded")
	}
	if after := describeFile(t, path); after != before {
		t.Fatal("the refused startup changed the database")
	}
}

// ---------- backup and restore ----------

// The evidence survives a logical backup; a header that claims 0009 without its table is refused;
// a dump written before 0009 restores with an empty table.
func TestPurgeEvidenceBackupAndRestore(t *testing.T) {
	path := fileStore(t, "evidence-restore.db")
	backToStep0006(t, path)
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
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

// ---------- the optional offline tool ----------

// With no arguments the command lists what is pending and changes nothing: not the schema, not the
// ledger, not the directory.
func TestSchemaUpgradeInspectionListsPendingStepsReadOnly(t *testing.T) {
	path := fileStore(t, "inspect.db")
	backToStep0006(t, path)
	cfg := writeSQLiteConfig(t, path)
	before, files := describeFile(t, path), dirListing(t, filepath.Dir(path))

	out, err := SchemaUpgradeCommand(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[0010, 0009, 0007, 0008, 0011, 0012, 0013]") || !strings.Contains(out, "next start") {
		t.Fatalf("inspection did not list the pending steps in list order and say startup applies them: %q", out)
	}
	for _, stale := range []string{"refuse", "required", "requires"} {
		if strings.Contains(strings.ToLower(out), stale) {
			t.Fatalf("inspection still tells the operator the command is %q: %q", stale, out)
		}
	}
	if after := describeFile(t, path); after != before {
		t.Fatal("inspection changed the database")
	}
	if after := dirListing(t, filepath.Dir(path)); after != files {
		t.Fatalf("inspection left or rewrote files:\n%s\nbecame\n%s", files, after)
	}
	raw, err := connectStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if recorded(raw, "0010") || recorded(raw, "0009") {
		t.Fatal("inspection recorded a step")
	}
}

// --apply is the same runner as startup, run offline: it applies what is pending, verifies it, and a
// second run finds nothing to do and rewrites nothing.
func TestSchemaUpgradeApplyUpgradesOfflineWithTheNormalRunner(t *testing.T) {
	path := fileStore(t, "apply.db")
	backToStep0006(t, path)
	cfg := writeSQLiteConfig(t, path)

	out, err := SchemaUpgradeCommand(cfg, []string{"--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[0010, 0009, 0007, 0008, 0011, 0012, 0013]") {
		t.Fatalf("--apply did not report the steps it applied: %q", out)
	}
	for _, stale := range []string{"refuse", "required", "requires"} {
		if strings.Contains(strings.ToLower(out), stale) {
			t.Fatalf("--apply output still says %q: %q", stale, out)
		}
	}
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	requireStepsRecordedInListOrder(t, st)
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purge_owners`); n != 0 {
		t.Fatalf("the evidence table holds %d rows after --apply", n)
	}
	first := ledgerPicture(t, st)
	again, err := SchemaUpgradeCommand(cfg, []string{"--apply"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(again, "no pending") {
		t.Fatalf("a second --apply should find nothing pending: %q", again)
	}
	if after := ledgerPicture(t, st); after != first {
		t.Fatal("a repeated --apply rewrote the ledger")
	}
	if inspected, err := SchemaUpgradeCommand(cfg, nil); err != nil || !strings.Contains(inspected, "no pending") {
		t.Fatalf("inspection after --apply: %q, %v", inspected, err)
	}
}

// The tool only works on a database that exists; it never creates a config or a database.
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

// The migration list is in merge order, not id order. The ids 0007-0009 were reserved by branches that
// were still open when 0010 reached main, so each reserved step is appended after whatever has already
// shipped. The purge evidence step (0009) therefore follows 0010.
func TestPurgeEvidenceIsAppendedLast(t *testing.T) {
	steps := newTestStore(t).migrations()
	ids := make([]string, len(steps))
	seen := map[string]bool{}
	for i, m := range steps {
		ids[i] = m.id
		if seen[m.id] {
			t.Fatalf("steps %v: %s appears twice", ids[:i+1], m.id)
		}
		seen[m.id] = true
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
