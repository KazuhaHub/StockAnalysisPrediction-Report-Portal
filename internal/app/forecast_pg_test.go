package app

import (
	"bytes"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// The forecast ledger on Postgres.
//
// Everything here has a SQLite twin in forecast_migration_test.go, and the reason to repeat it is that
// the two drivers disagree about exactly the things this step depends on: the identity column is
// `GENERATED ALWAYS AS IDENTITY` there and `AUTOINCREMENT` here, and whether NULLs collide in a unique
// index is a property of the index implementation rather than of SQL. A step proven on one driver is
// half a step, and the half that is missing is the half production runs.

// TestPostgresForecastStepApplies: a Postgres database at the accepted baseline gains the step, and
// reopening does not run it again.
func TestPostgresForecastStepApplies(t *testing.T) {
	st := pgStore(t)
	windBackToBeforeTheForecastStep(t, st)
	st.Close()

	migrated, err := OpenStore("postgres", pgDSN(t))
	if err != nil {
		t.Fatalf("a baseline database must be accepted and migrated: %v", err)
	}
	for _, tbl := range predictionTables {
		if !migrated.tableExists(tbl) {
			t.Errorf("table %s is missing after the migration", tbl)
		}
	}
	for _, idx := range predictionIndices {
		if !migrated.indexExists(idx) {
			t.Errorf("index %s is missing after the migration", idx)
		}
	}
	if !recorded(migrated, predictionMigrationID) {
		t.Errorf("step %s ran but was not recorded", predictionMigrationID)
	}
	if !migrated.columnExists("prediction_sets", predColScenarioBasis) {
		t.Errorf("prediction_sets.%s is missing after the migration", predColScenarioBasis)
	}
	first := ledgerPicture(t, migrated)
	migrated.Close()

	again, err := OpenStore("postgres", pgDSN(t))
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	defer again.Close()
	if second := ledgerPicture(t, again); second != first {
		t.Errorf("the second open rewrote the ledger:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// TestPostgresPredSetIdemIndexAllowsManyNullReportIDs is the one property that is a genuine
// driver difference rather than a spelling: whether many NULL report_ids may share one payload hash.
// They must, because two runs of one symbol produce identical payloads and both sets are purged
// eventually. If Postgres treated NULLs as equal here, the second purged set would be refused and the
// failure would appear only in production, long after the ingest that wrote it.
func TestPostgresPredSetIdemIndexAllowsManyNullReportIDs(t *testing.T) {
	st := pgStore(t, "prediction_sets")
	seedPredictionSet(t, st, sql.NullInt64{}, "same-payload")
	seedPredictionSet(t, st, sql.NullInt64{}, "same-payload")
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 2 {
		t.Errorf("two purged sets with one payload hash = %d rows, want 2", n)
	}
}

// TestPostgresPredSetIdemIndexRejectsADoubleIngest: the other half of the same index. A retried ingest
// of one set against one report is one row.
func TestPostgresPredSetIdemIndexRejectsADoubleIngest(t *testing.T) {
	st := pgStore(t, "prediction_sets")
	reportID := seedPgReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "same-payload")
	_, err := st.exec(`INSERT INTO prediction_sets(report_id, made_at, made_on, cohort, state, payload_sha256)
		VALUES(?,?,?,?,?,?)`, reportID, "2026-10-16T14:40:12Z", "2026-10-16", "2026-10", predStateAccepted, "same-payload")
	if err == nil {
		t.Fatal("the idempotency index admitted a duplicate (report_id, payload_sha256)")
	}
}

// TestPostgresCleanupChunkNullsPredictionSets mirrors the cascade on the driver production runs.
func TestPostgresCleanupChunkNullsPredictionSets(t *testing.T) {
	st := pgStore(t, "prediction_sets", "reports")
	reportID := seedPgReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	sent := scalar[string](t, st, `SELECT sent_at FROM reports WHERE id=?`, reportID)

	if _, err := st.deleteReportChunk([]reportKey{{id: reportID, sent: sent}}); err != nil {
		t.Fatalf("deleteReportChunk: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("prediction_sets rows after the chunk delete = %d, want 1 (survives)", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id IS NULL`); n != 1 {
		t.Errorf("prediction_sets with a NULL report_id = %d, want 1", n)
	}
}

// TestPostgresDeleteReportNullsPredictionSets is the API/manual path on Postgres.
func TestPostgresDeleteReportNullsPredictionSets(t *testing.T) {
	st := pgStore(t, "prediction_sets", "reports")
	reportID := seedPgReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")

	if _, err := st.DeleteReport(reportID); err != nil {
		t.Fatalf("DeleteReport: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id IS NULL`); n != 1 {
		t.Errorf("prediction_sets with a NULL report_id = %d, want 1", n)
	}
}

// seedPgReport inserts one report on the integration database and returns its id. Postgres shares one
// database across the whole suite, so this takes a symbol the other tests do not use.
func seedPgReport(t *testing.T, st *Store, symbol, date string) int64 {
	t.Helper()
	id, _, err := st.UpsertReport(Rep{
		Symbol: symbol, Date: date, RType: predTestRtype, Kind: "投资决策",
		Title: "投资决策建议 pg", MD: "body",
	})
	if err != nil {
		t.Fatalf("seed report: %v", err)
	}
	return id
}

// TestPostgresLedgerStartSaveAndRestoreRule covers P5's store paths on the driver production runs:
// the transactional save (including the INSERT ... ON CONFLICT the SQLite path cannot prove) and the
// restore rule that carries the write-once key from live when a dump lacks it.
func TestPostgresLedgerStartSaveAndRestoreRule(t *testing.T) {
	// Clean only the key this test writes: Postgres shares ONE database across the whole suite, and
	// pgStore's `tables` argument DELETEs every row — clearing `meta` would drop the schema_version
	// marker the next OpenStore needs.
	st := pgStore(t, "prediction_sets")
	clearForecastPopulation(t, st)

	reportID := seedPgReport(t, st, "600160", "2026-10-18")
	setID := seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "ledger-start-pg")
	// Pin the receipt before the boundary so the exclusion is deterministic.
	if _, err := st.exec(`UPDATE prediction_sets SET made_at='2026-10-18T13:00:00Z', made_on='2026-10-18' WHERE id=?`, setID); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, time.October, 18, 16, 0, 0, 0, time.UTC)
	excluded, byState, code, err := st.SaveForecastPopulationFrom(from, time.Now().UTC())
	if err != nil || code != "" {
		t.Fatalf("save: code=%q err=%v", code, err)
	}
	if len(excluded) != 1 || excluded[0] != setID {
		t.Errorf("excluded = %v, want [%d]", excluded, setID)
	}
	if byState[predStateAccepted] != 1 {
		t.Errorf("counts_by_state = %v, want one accepted", byState)
	}
	if got, ok := st.ForecastPopulationFrom(); !ok || !got.Equal(from) {
		t.Fatalf("stored = %v (ok=%v)", got, ok)
	}
	// Write-once: a second save is refused.
	if _, _, code, _ := st.SaveForecastPopulationFrom(from.Add(time.Hour), time.Now().UTC()); code != "forecast_population_set" {
		t.Errorf("second save code = %q, want forecast_population_set", code)
	}

	// The restore rule: a dump without the key inherits the live value.
	dump := dumpOf(t, st)
	dst := pgStore(t, "prediction_sets")
	clearForecastPopulation(t, dst)
	seedLedgerStart(t, dst, "2026-10-18T16:00:00Z")
	if _, err := dst.restoreFrom(bytes.NewReader(dropMetaRow(t, dump, setForecastPopulationFrom)), true); err != nil {
		t.Fatalf("restore without the key: %v", err)
	}
	if got, ok := dst.ForecastPopulationFrom(); !ok || !got.Equal(from) {
		t.Fatalf("carried value = %v (ok=%v)", got, ok)
	}
}

// clearForecastPopulation removes only the ledger-start key, leaving the shared database's schema
// marker and every other meta row intact.
func clearForecastPopulation(t *testing.T, st *Store) {
	t.Helper()
	if _, err := st.exec(`DELETE FROM meta WHERE k=?`, setForecastPopulationFrom); err != nil {
		t.Fatalf("clean %s: %v", setForecastPopulationFrom, err)
	}
}

// TestPostgresUnrecordedFloorAndRestore covers P2's floor on Postgres: captured at save, counted with
// the same exclusions, and re-derived through a restore that lacks the keys.
func TestPostgresUnrecordedFloorAndRestore(t *testing.T) {
	st := pgStore(t, "prediction_sets", "reports", "audit_log")
	for _, k := range []string{setForecastPopulationFrom, setForecastUnrecordedFloorID, setForecastUnrecordedFloorAt, setForecastUnrecordedFloorMethod} {
		if _, err := st.exec(`DELETE FROM meta WHERE k=?`, k); err != nil {
			t.Fatalf("clean %s: %v", k, err)
		}
	}
	from := time.Date(2020, time.June, 8, 0, 0, 0, 0, time.UTC)
	// Two pre-boundary covered reports, with audit rows before the boundary.
	for i := 0; i < 2; i++ {
		id := seedReportOnly(t, st, "603"+pad3(i), "2020-01-05", "")
		seedIngestAudit(t, st, id, from.Add(-time.Hour))
	}
	if _, _, code, err := st.SaveForecastPopulationFrom(from, time.Now().UTC()); err != nil || code != "" {
		t.Fatalf("save: code=%q err=%v", code, err)
	}
	if _, ok := st.forecastUnrecordedFloor(); !ok {
		t.Fatal("the floor was not captured on Postgres")
	}
	// A post-boundary covered report with no set counts 1. It still has a created ingest audit row,
	// as every ingest does (the audit is written before the ledger write).
	postID := seedReportOnly(t, st, "603999", "2020-06-09", "")
	seedIngestAudit(t, st, postID, from.Add(time.Hour))
	if n, _, ok, err := st.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 1 {
		t.Fatalf("count = %d ok=%v err=%v, want 1", n, ok, err)
	}

	// A dump without the floor keys, restored into a database with the boundary, re-derives them.
	dump := dropMetaRow(t, dumpOf(t, st), setForecastUnrecordedFloorID)
	dump = dropMetaRow(t, dump, setForecastUnrecordedFloorAt)
	dump = dropMetaRow(t, dump, setForecastUnrecordedFloorMethod)
	dst := pgStore(t, "prediction_sets", "reports", "audit_log")
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, ok := dst.forecastUnrecordedFloor(); !ok {
		t.Fatal("the restore did not re-derive the floor")
	}
	if n, _, ok, err := dst.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 1 {
		t.Fatalf("count after restore = %d ok=%v err=%v, want 1", n, ok, err)
	}
}

// TestPostgresDueSelectionHealAndPlan covers the P1 write path on the driver production runs. The
// selection, the one-shot heal and the plan's transaction are all SQL this package writes, and the
// SQLite tests would not catch a Postgres-only spelling or locking mistake.
func TestPostgresDueSelectionHealAndPlan(t *testing.T) {
	st := pgStore(t, "prediction_marks", "prediction_sets", "audit_log")
	if _, err := st.exec("DELETE FROM meta WHERE k=?", setForecastRepairPremature); err != nil {
		t.Fatal(err)
	}

	var setID int64
	if err := st.queryRow(`INSERT INTO prediction_sets(
		symbol, rtype, made_at, made_on, cohort, state, bench_code, base_check)
		VALUES(?,?,?,?,?,?,?,?) RETURNING id`,
		"600160", predTestRtype, "2026-10-16T13:00:00Z", "2026-10-16", "2026-10",
		predStateAccepted, "sh000852", baseCheckPending).Scan(&setID); err != nil {
		t.Fatalf("insert set: %v", err)
	}
	var markID int64
	if err := st.queryRow(`INSERT INTO prediction_marks(set_id, checkpoint, status, bench_code)
		VALUES(?,?,?,?) RETURNING id`, setID, "d5", markPending, "sh000852").Scan(&markID); err != nil {
		t.Fatalf("insert mark: %v", err)
	}

	sets, err := st.ForecastSetsDue(time.Now(), 1000)
	if err != nil {
		t.Fatalf("selection: %v", err)
	}
	if len(sets) != 1 || sets[0].SetID != setID || len(sets[0].Marks) != 1 {
		t.Fatalf("selection = %+v, want the seeded set with one mark", sets)
	}

	// The plan applies in one transaction on Postgres too.
	if err := st.ApplyForecastPlan(forecastPlan{setID: setID, settles: []forecastSettle{
		{markID: markID, res: markResult{Status: markResolved, Session: "2026-10-26"}},
	}}, time.Now()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var status string
	if err := st.queryRow("SELECT status FROM prediction_marks WHERE id=?", markID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != markResolved {
		t.Errorf("status = %q, want resolved", status)
	}

	// The one-shot heal on the pre-P1 signature, audited once by the server wrapper.
	if _, err := st.exec(`UPDATE prediction_marks SET status=?, refusal='no_bar', session_date='',
		resolved_at='', attempts=1 WHERE id=?`, markRefused, markID); err != nil {
		t.Fatal(err)
	}
	srv := &Server{st: st}
	srv.runForecastRepair()
	if err := st.queryRow("SELECT status FROM prediction_marks WHERE id=?", markID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != markPending {
		t.Errorf("after heal status = %q, want pending", status)
	}
	var audited int
	if err := st.queryRow("SELECT COUNT(*) FROM audit_log WHERE action=?", AuditForecastRepair).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Errorf("repair audit rows = %d, want 1", audited)
	}
}

func TestPostgresDueSelectionUsesTheDueIndex(t *testing.T) {
	st := pgStore(t)
	// SET LOCAL is scoped to the transaction (and its connection), so the plan is read on the same
	// connection that disabled the sequential scan.
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only
	if _, err := tx.Exec("SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(st.bind("EXPLAIN "+forecastSetsDueQuery),
		markPending, markRefused, "2026-01-01T00:00:00Z", predStateAccepted, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if !strings.Contains(plan, "idx_pred_marks_due") {
		t.Errorf("selection does not use idx_pred_marks_due:\n%s", plan)
	}
}

// TestPostgresAccessSchemaAndReattribution is the Postgres twin of step 0006 and the OU delete's
// re-attribution: the step's DDL, the index, and the one UPDATE that moves a deleted OU's sets.
func TestPostgresAccessSchemaAndReattribution(t *testing.T) {
	st := pgStore(t, "prediction_sets", "user_groups")
	if err := st.EnsureDefaultForecastScope(); err != nil {
		t.Fatal(err)
	}
	def := st.EnsureDefaultGroup()
	for _, c := range []colRef{{"user_groups", "forecast_scope"}, {"prediction_sets", "attributed_ou"}} {
		if !st.columnExists(c.table, c.name) {
			t.Errorf("%s.%s missing on Postgres", c.table, c.name)
		}
	}
	if !st.indexExists("idx_pred_sets_ou") {
		t.Error("idx_pred_sets_ou missing on Postgres")
	}

	parent := seedOU(t, st, 0, scopeAll)
	child := seedOU(t, st, parent, scopeAll)
	setID := seedPredictionSet(t, st, sql.NullInt64{}, "p38-pg")
	if _, err := st.exec(`UPDATE prediction_sets SET attributed_ou=? WHERE id=?`, child, setID); err != nil {
		t.Fatal(err)
	}
	moved, target, err := st.DeleteUserGroup(child)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 || target != parent {
		t.Errorf("moved/target = %d/%d, want 1/%d", moved, target, parent)
	}
	var got sql.NullInt64
	if err := st.queryRow(`SELECT attributed_ou FROM prediction_sets WHERE id=?`, setID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != parent {
		t.Errorf("attributed_ou = %v, want %d", got, parent)
	}
	if _, _, err := st.DeleteUserGroup(parent); err != nil {
		t.Fatal(err)
	}
	if err := st.queryRow(`SELECT attributed_ou FROM prediction_sets WHERE id=?`, setID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.Int64 != def {
		t.Errorf("a root's set = %v, want Default %d", got, def)
	}
}

// TestPostgresMigration0006OlderDumpRestores: a dump written before step 0006 loads into a build that
// has it, both columns come back NULL, and the Default OU then reads none.
func TestPostgresMigration0006OlderDumpRestores(t *testing.T) {
	st := pgStore(t, "prediction_sets", "user_groups")
	st.EnsureDefaultGroup() // pgStore emptied user_groups; the dump needs a Default to normalise
	seedPredictionSet(t, st, sql.NullInt64{}, "p38-pg-older")
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_pred_sets_ou`,
		`ALTER TABLE prediction_sets DROP COLUMN IF EXISTS attributed_ou`,
		`ALTER TABLE user_groups DROP COLUMN IF EXISTS forecast_scope`,
		// Every step after 0006 goes too: a dump's migration list has to be a contiguous prefix.
		`ALTER TABLE announcements DROP COLUMN IF EXISTS popup_dismissible`,
		`ALTER TABLE announcements RENAME COLUMN strip_dismissible TO dismissible`,
	} {
		if _, err := st.exec(q); err != nil {
			t.Fatalf("winding back 0006: %v", err)
		}
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k IN (?,?)`, migrationLedgerKey("0006"), migrationLedgerKey("0010")); err != nil {
		t.Fatal(err)
	}
	dump := dumpOf(t, st)
	st.Close()

	fresh, err := OpenStore("postgres", pgDSN(t))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer fresh.Close()
	if _, err := fresh.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if !fresh.columnExists("prediction_sets", "attributed_ou") || !fresh.columnExists("user_groups", "forecast_scope") {
		t.Error("the step's columns are missing after the restore")
	}
	if got := scalar[string](t, fresh, `SELECT forecast_scope FROM user_groups WHERE is_default=1`); got != scopeNone {
		t.Errorf("Default scope = %q, want none", got)
	}
	if n := scalar[int](t, fresh, `SELECT COUNT(*) FROM prediction_sets WHERE attributed_ou IS NOT NULL`); n != 0 {
		t.Errorf("%d restored sets have an attribution", n)
	}
}
