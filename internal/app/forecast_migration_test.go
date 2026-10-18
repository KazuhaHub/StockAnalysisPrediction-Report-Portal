package app

import (
	"bytes"
	"database/sql"
	"testing"
)

// The forecast-verification ledger's schema step (ADR 0029, plan PR 1).
//
// Every case here has a Postgres twin in forecast_pg_test.go, because the two drivers disagree about
// exactly the things this step depends on: the identity-column spelling, and whether NULLs collide in
// a unique index. A step proven on one driver is half a step.

// predictionTables is every table the step declares, in the order the migration lists them.
var predictionTables = []string{"prediction_sets", "prediction_claims", "prediction_marks"}

// predictionIndices is every index the step declares. verifyMigrationProducts checks each by name, so
// a typo here is a startup failure rather than a slow query nobody notices.
var predictionIndices = []string{
	"idx_pred_sets_idem", "idx_pred_sets_group", "idx_pred_sets_symbol",
	"idx_pred_claims_key", "idx_pred_marks_cp", "idx_pred_marks_due",
}

// windBackToBeforeTheForecastStep removes what the forecast step adds, leaving a database the release
// before it could have written. cleanup_runs is a BASELINE table, so winding back means dropping the
// column the step added to it as well as the tables it created — leave the column and the database
// still carries this step's shape while claiming not to have run it.
func windBackToBeforeTheForecastStep(t *testing.T, st *Store) {
	t.Helper()
	for _, tbl := range predictionTables {
		if _, err := st.exec(`DROP TABLE IF EXISTS ` + tbl); err != nil {
			t.Fatalf("winding back %s: %v", tbl, err)
		}
	}
	if _, err := st.exec(`ALTER TABLE cleanup_runs DROP COLUMN IF EXISTS predictions_deleted`); err != nil {
		t.Fatalf("winding back cleanup_runs: %v", err)
	}
	if _, err := st.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey(predictionMigrationID)); err != nil {
		t.Fatal(err)
	}
}

// seedReport inserts one report and returns its id. The forecast tests need a real reports row for
// the two delete paths to act on, and for the cascade to have a link to null.
func seedReport(t *testing.T, st *Store, symbol, date string) int64 {
	t.Helper()
	id, _, err := st.UpsertReport(Rep{
		Symbol: symbol, Date: date, RType: predTestRtype, Kind: "投资决策",
		Title: "投资决策建议", MD: "body",
	})
	if err != nil {
		t.Fatalf("seed report: %v", err)
	}
	return id
}

// seedPredictionSet inserts one set row, returning its id. Only the columns the idempotency index and
// the cascade tests need are set; everything else takes its declared default.
func seedPredictionSet(t *testing.T, st *Store, reportID sql.NullInt64, payloadSHA string) int64 {
	t.Helper()
	v := any(nil)
	if reportID.Valid {
		v = reportID.Int64
	}
	// rtype is set because the purge unit is (rtype, cohort): a set with an empty rtype would sit in a
	// month of its own and never be purged with the reports it came from.
	res, err := st.insertID(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256)
		VALUES(?,?,?,?,?,?,?)`, v, "投资决策建议", "2026-10-16T14:40:12Z", "2026-10-16", "2026-10", predStateAccepted, payloadSHA)
	if err != nil {
		t.Fatalf("seed prediction set: %v", err)
	}
	return res
}

// TestMigration0004AppliesOnBaseline is the upgrade path that matters in production: a database at
// the accepted baseline, written by the previous release, which has no forecast tables at all. It
// must open, gain the step, and record it — and reopening must not run anything again.
func TestMigration0004AppliesOnBaseline(t *testing.T) {
	path := fileStore(t, "pre-forecast.db")
	backToTheBaseline(t, path)

	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("a baseline database must be accepted and migrated: %v", err)
	}
	for _, tbl := range predictionTables {
		if !st.tableExists(tbl) {
			t.Errorf("table %s is missing after the migration", tbl)
		}
	}
	for _, idx := range predictionIndices {
		if !st.indexExists(idx) {
			t.Errorf("index %s is missing after the migration", idx)
		}
	}
	if !recorded(st, predictionMigrationID) {
		t.Errorf("step %s ran but was not recorded", predictionMigrationID)
	}
	// scenario_basis is the one contract field the ledger carries as a column rather than inside the
	// payload, because it is a grouping key the statistics will filter and exclude on — the same class
	// as probability_method. It is DECLARED (as a column of the table this step creates, which the
	// migration verifies by name), and it must be there even when empty.
	if !st.columnExists("prediction_sets", predColScenarioBasis) {
		t.Errorf("prediction_sets.%s is missing after the migration", predColScenarioBasis)
	}
	first := ledgerPicture(t, st)
	st.Close()

	st2, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopening a migrated database: %v", err)
	}
	defer st2.Close()
	if second := ledgerPicture(t, st2); second != first {
		t.Errorf("the second open rewrote the ledger:\n--- first ---\n%s--- second ---\n%s", first, second)
	}
}

// TestMigration0004OlderDumpRestores: the runbook tells an operator to back up before upgrading, so
// that dump predates this step and cannot carry its tables. It must load, and the tables must exist
// and be empty afterwards — a pre-step dump has no rows to bring.
func TestMigration0004OlderDumpRestores(t *testing.T) {
	st := newTestStore(t)
	dump := preMigrationDump(t)
	// A dump from before this step is written by a release that knew only 0001..0003, so it must not
	// claim 0004.
	if _, err := st.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("a dump from before this step must restore: %v", err)
	}
	for _, tbl := range predictionTables {
		if !st.tableExists(tbl) {
			t.Errorf("table %s is missing after the restore", tbl)
		}
		if n := scalar[int](t, st, `SELECT COUNT(*) FROM `+tbl); n != 0 {
			t.Errorf("%s has %d rows after a pre-step restore, want 0", tbl, n)
		}
	}
	for _, m := range st.migrations() {
		if !recorded(st, m.id) {
			t.Errorf("step %s was not recorded by the restore", m.id)
		}
	}
}

// TestBackupIncludesPredictionTables: a backup round-trip must carry rows in all three tables. A step
// that adds a table but is not listed in the dump silently loses that table's data on every backup —
// the kind of loss nobody notices until a restore.
func TestBackupIncludesPredictionTables(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	setID := seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	if _, err := st.exec(`INSERT INTO prediction_claims(set_id, claim_key, kind, stance, weight_bp)
		VALUES(?,'stance','stance','long',1000)`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, session_date)
		VALUES(?,'d5','pending','2026-10-26')`, setID); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if _, _, err := st.dumpTo(&buf); err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, err := st.restoreFrom(bytes.NewReader(buf.Bytes()), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("prediction_sets rows after the round trip = %d, want 1", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_claims`); n != 1 {
		t.Errorf("prediction_claims rows after the round trip = %d, want 1", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_marks`); n != 1 {
		t.Errorf("prediction_marks rows after the round trip = %d, want 1", n)
	}
}

// TestDeleteReportNullsPredictionSets is the ADR 0029 §8 cascade on the API/manual path: the report
// row goes, the ledger stays, and the link is nulled. Nulled rather than deleted, because a set
// outlives its report by design (the reports floor is 365 days and h12 resolves at 12 months).
func TestDeleteReportNullsPredictionSets(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	setID := seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	if _, err := st.exec(`INSERT INTO prediction_claims(set_id, claim_key, kind) VALUES(?,'stance','stance')`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint) VALUES(?,'d5')`, setID); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DeleteReport(reportID); err != nil {
		t.Fatalf("DeleteReport: %v", err)
	}

	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("prediction_sets rows after DeleteReport = %d, want 1 (survives)", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id IS NULL`); n != 1 {
		t.Errorf("prediction_sets with a NULL report_id = %d, want 1", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_claims`); n != 1 {
		t.Errorf("prediction_claims rows = %d, want 1", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_marks`); n != 1 {
		t.Errorf("prediction_marks rows = %d, want 1", n)
	}
}

// TestCleanupChunkNullsPredictionSets is the same cascade on the retention path (ADR 0017). Missing
// either site is not wasted rows — a set left pointing at a reused id is another report's claims.
func TestCleanupChunkNullsPredictionSets(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	sent := scalar[string](t, st, `SELECT sent_at FROM reports WHERE id=?`, reportID)

	if _, err := st.deleteReportChunk([]reportKey{{id: reportID, sent: sent}}); err != nil {
		t.Fatalf("deleteReportChunk: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM reports WHERE id=?`, reportID); n != 0 {
		t.Fatalf("the report survived the chunk delete")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("prediction_sets rows after the chunk delete = %d, want 1 (survives)", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id IS NULL`); n != 1 {
		t.Errorf("prediction_sets with a NULL report_id = %d, want 1", n)
	}
}

// TestCleanupChunkSkipsPredictionSetsForAReingestedReport: the chunk delete re-asserts sent_at, so a
// report re-ingested between the scan and the delete is skipped. Its ledger must be skipped with it —
// nulling the link for a report that still exists would strip a live report of its own sets.
func TestCleanupChunkSkipsPredictionSetsForAReingestedReport(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")

	// The report was re-ingested after the scan, so the caller's key carries a stale sent_at.
	if _, err := st.deleteReportChunk([]reportKey{{id: reportID, sent: "2000-01-01T00:00:00Z"}}); err != nil {
		t.Fatalf("deleteReportChunk: %v", err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM reports WHERE id=?`, reportID); n != 1 {
		t.Fatal("the re-ingested report was deleted")
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id IS NULL`); n != 0 {
		t.Errorf("a surviving report's set was stripped of its link")
	}
}

// TestAPurgedSetIsNotInheritedByANewReport: ids are reassigned, so the nulled link is what keeps a
// later report from inheriting another report's claims. The row that proves it is the unique index
// below; this test states the intent in the terms the cascade is argued in.
func TestAPurgedSetIsNotInheritedByANewReport(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	if _, err := st.DeleteReport(reportID); err != nil {
		t.Fatal(err)
	}

	// The next report takes the same id and reads its own sets, which is none.
	newID := seedReport(t, st, "000001", "2026-10-17")
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE report_id=?`, newID); n != 0 {
		t.Errorf("a new report inherited %d sets from a purged one", n)
	}
}

// TestPredSetIdemIndexAllowsManyNullReportIDs: the unique index is (report_id, payload_sha256), and
// its idempotency guarantee must not turn purged sets into a mutual collision. NULLs are distinct in
// a unique index on both drivers, so any number of purged sets can carry the same payload hash — and
// they will, because two runs of one symbol produce identical payloads.
func TestPredSetIdemIndexAllowsManyNullReportIDs(t *testing.T) {
	st := newTestStore(t)
	seedPredictionSet(t, st, sql.NullInt64{}, "same-payload")
	seedPredictionSet(t, st, sql.NullInt64{}, "same-payload")
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 2 {
		t.Errorf("two purged sets with one payload hash = %d rows, want 2", n)
	}
}

// TestPredSetIdemIndexRejectsADoubleIngest: the same set posted twice against one report is one row.
// It is the retry key, and Dify does retry.
func TestPredSetIdemIndexRejectsADoubleIngest(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "same-payload")
	_, err := st.exec(`INSERT INTO prediction_sets(report_id, made_at, made_on, cohort, state, payload_sha256)
		VALUES(?,?,?,?,?,?)`, reportID, "2026-10-16T14:40:12Z", "2026-10-16", "2026-10", predStateAccepted, "same-payload")
	if err == nil {
		t.Fatal("the idempotency index admitted a duplicate (report_id, payload_sha256)")
	}
}

// TestPredictionSetIsInsertOnlyOnItsEvaluatorColumns: the evaluator fills s0_date, base_date, s0_fen,
// base_check and bench_code, and only while base_check is pending or refused. Resolved is terminal —
// a mark whose inputs can still move is a cell whose numbers can still move.
func TestPredictionSetIsInsertOnlyOnItsEvaluatorColumns(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2026-10-16")
	id := seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")

	fill := func(check string) int64 {
		res, err := st.exec(`UPDATE prediction_sets SET s0_date='2026-10-19', base_date='2026-10-19',
			s0_fen=1850, base_check=? WHERE id=? AND base_check IN ('pending','refused')`, check, id)
		if err != nil {
			t.Fatalf("guarded update to %s: %v", check, err)
		}
		n, _ := res.RowsAffected()
		return n
	}
	if n := fill(baseMatch); n != 1 {
		t.Errorf("the first fill affected %d rows, want 1", n)
	}
	if n := fill(baseMatch); n != 0 {
		t.Errorf("a settled base_check was overwritten: %d rows affected, want 0", n)
	}
}

// TestStorageUsageListsPredictions: the console must show the ledger's footprint, with no cleanup
// target behind it — v1 has none, so an eligible count would be a promise nothing keeps.
func TestStorageUsageListsPredictions(t *testing.T) {
	s := routeTableServer(t)
	reportID := seedReport(t, s.st, "600519", "2026-10-16")
	setID := seedPredictionSet(t, s.st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-set-1")
	if _, err := s.st.exec(`INSERT INTO prediction_claims(set_id, claim_key, kind) VALUES(?,'stance','stance')`, setID); err != nil {
		t.Fatal(err)
	}

	code, out := call(t, s.apiCleanupUsage, ``, "admin")
	if code != 200 {
		t.Fatalf("apiCleanupUsage → %d", code)
	}
	cats, _ := out["categories"].([]any)
	var pred map[string]any
	for _, raw := range cats {
		c := raw.(map[string]any)
		if c["key"] == "predictions" {
			pred = c
		}
	}
	if pred == nil {
		t.Fatal("the usage list has no predictions category")
	}
	if pred["rows"] != float64(2) {
		t.Errorf("predictions rows = %v, want 2 (the set row plus its claim)", pred["rows"])
	}
	if pred["eligible"] != float64(0) {
		t.Errorf("predictions eligible = %v, want 0: v1 has no ledger cleanup target", pred["eligible"])
	}
}

// fileStore and mutateFile come from migrate_boundary_test.go: a temp-path database the real binary
// created, so the shape on disk can be wound back before reopening.

// backToTheBaseline winds a database back to the accepted baseline: no migration ledger, and none of
// the columns or tables the steps after it add.
func backToTheBaseline(t *testing.T, path string) {
	t.Helper()
	mutateFile(t, path,
		`DELETE FROM meta WHERE k LIKE 'mig:%'`,
		`ALTER TABLE user_groups DROP COLUMN totp_enroll`,
		`ALTER TABLE user_groups DROP COLUMN passkey_enroll`,
		`ALTER TABLE user_groups DROP COLUMN require_2fa`,
		`ALTER TABLE users DROP COLUMN last_used_version`,
		`DROP TABLE IF EXISTS user_sessions`,
		`DROP TABLE IF EXISTS login_history`,
		`ALTER TABLE users DROP COLUMN login_history_keep`,
		`ALTER TABLE users DROP COLUMN login_history_initialized`,
		// Step 0004's tables and its column on the baseline table cleanup_runs.
		`DROP TABLE IF EXISTS prediction_sets`,
		`DROP TABLE IF EXISTS prediction_claims`,
		`DROP TABLE IF EXISTS prediction_marks`,
		`ALTER TABLE cleanup_runs DROP COLUMN predictions_deleted`)
}
