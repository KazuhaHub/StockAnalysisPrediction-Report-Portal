package app

import (
	"database/sql"
	"testing"
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
		Symbol: symbol, Date: date, RType: predictionTestRtype, Kind: "投资决策",
		Title: "投资决策建议 pg", MD: "body",
	})
	if err != nil {
		t.Fatalf("seed report: %v", err)
	}
	return id
}
