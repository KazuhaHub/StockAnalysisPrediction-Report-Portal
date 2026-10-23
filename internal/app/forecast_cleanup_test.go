package app

import (
	"database/sql"
	"testing"
	"time"
)

// The forecast ledger's cleanup target (ADR 0029 §8, revised 2026-10-17).
//
// The safety of this target is structural rather than numeric, and that is what the predicate tests
// below exist to pin. A set still awaiting settlement has a pending mark, so it never matches; a set
// whose report still exists has a non-NULL report_id, so it never matches. Neither of those is a
// function of how many days an operator typed — which is the whole reason the floor here can be 180
// days when the reports floor is 365.

// seedFinishedPredictionSet inserts a set that the cleanup predicate should match: no report, every
// mark terminal, and the newest resolved_at at the given instant.
func seedFinishedPredictionSet(t *testing.T, st *Store, resolvedAt string) int64 {
	t.Helper()
	setID := seedPredictionSet(t, st, sql.NullInt64{}, "sha-"+resolvedAt)
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d5','resolved',?)`, setID, resolvedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d20','refused_final',?)`, setID, resolvedAt); err != nil {
		t.Fatal(err)
	}
	return setID
}

// TestCleanupPredictionPredicateNeverTouchesALiveReport is the first structural guarantee: a set whose
// report still exists is not eligible, whatever its marks say and however old it is.
func TestCleanupPredictionPredicateNeverTouchesALiveReport(t *testing.T) {
	st := newTestStore(t)
	reportID := seedReport(t, st, "600519", "2020-01-01")
	setID := seedPredictionSet(t, st, sql.NullInt64{Int64: reportID, Valid: true}, "sha-live")
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d5','resolved','2000-01-01T00:00:00Z')`, setID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC() // everything is "older than now"

	n, err := st.CountPredictionsBefore(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("a set whose report still exists counted as eligible: %d", n)
	}
	if _, err := st.DeletePredictionsBefore(cutoff); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("a live report's set was deleted")
	}
}

// TestCleanupPredictionPredicateNeverTouchesAnUnsettledSet is the second structural guarantee: a set
// with any non-terminal mark is not eligible. This is what stops a retention pass from shortening the
// record on exactly the sets whose answers are still coming.
func TestCleanupPredictionPredicateNeverTouchesAnUnsettledSet(t *testing.T) {
	st := newTestStore(t)
	setID := seedPredictionSet(t, st, sql.NullInt64{}, "sha-pending")
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d5','resolved','2000-01-01T00:00:00Z')`, setID); err != nil {
		t.Fatal(err)
	}
	// One mark still retrying: the set's inputs can still move, so the set is not history yet.
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status)
		VALUES(?,'d20','refused')`, setID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC()

	if n, _ := st.CountPredictionsBefore(cutoff); n != 0 {
		t.Errorf("an unsettled set counted as eligible: %d", n)
	}
	if _, err := st.DeletePredictionsBefore(cutoff); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("an unsettled set was deleted")
	}
}

// TestCleanupPredictionPredicateMatchesAnOrphanedTerminalSet is the positive case, and it is
// deliberately narrow: every set in the month is orphaned AND fully terminal AND older than the cutoff.
func TestCleanupPredictionPredicateMatchesAnOrphanedTerminalSet(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	setID := seedFinishedPredictionSet(t, st, old)
	if _, err := st.exec(`INSERT INTO prediction_claims(set_id, claim_key, kind) VALUES(?,'stance','stance')`, setID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)

	if n, err := st.CountPredictionsBefore(cutoff); err != nil || n != 1 {
		t.Fatalf("eligible sets = %d (err=%v), want 1", n, err)
	}
	n, err := st.DeletePredictionsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeletePredictionsBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	// The children go with the set: an orphaned claim or mark is a row nothing can ever reach.
	for _, tbl := range []string{"prediction_sets", "prediction_claims", "prediction_marks"} {
		if c := scalar[int](t, st, `SELECT COUNT(*) FROM `+tbl); c != 0 {
			t.Errorf("%s still has %d rows after the purge", tbl, c)
		}
	}
	// And the month is marked purged, so the page can say so rather than recompute it as empty.
	purged := st.PurgedPredictionMonths()
	if _, ok := purged[[2]string{predTestRtype, "2026-10"}]; !ok {
		t.Errorf("no purge marker was written; got %v", purged)
	}
}

// TestCleanupPredictionsNeverDeletesPartOfAMonth is the review's first hole, and it is the reason the
// unit of deletion is a month rather than a set.
//
// Membership is "the earliest accepted set per (symbol, group) decides" (D4). If a pass could remove
// the earliest set of a symbol while a later rerun of the same symbol stayed, that rerun would be
// PROMOTED to member — precisely the selection-by-rerun D4 exists to forbid. And deleting part of a
// month would move that month's n and every metric on it while the page kept the same label, breaking
// the immutability a mature cell is cited for.
func TestCleanupPredictionsNeverDeletesPartOfAMonth(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)

	// A symbol with two terminal orphaned sets: the earliest (the member) and a later rerun.
	seedFinishedPredictionSet(t, st, old)
	seedFinishedPredictionSet(t, st, old)

	// A second symbol in the SAME month whose set is still unsettled, so the whole month is protected.
	unsettled := seedPredictionSet(t, st, sql.NullInt64{}, "sha-active")
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status)
		VALUES(?,'d5','pending')`, unsettled); err != nil {
		t.Fatal(err)
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)
	if n, _ := st.CountPredictionsBefore(cutoff); n != 0 {
		t.Errorf("a month with one unsettled set counted %d eligible sets, want 0", n)
	}
	if _, err := st.DeletePredictionsBefore(cutoff); err != nil {
		t.Fatal(err)
	}
	// One set survives with its report gone, so the D4 member is still the same set it always was.
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets`); n != 3 {
		t.Errorf("sets after the pass = %d, want 3 (nothing deleted from a partly-eligible month)", n)
	}
	if n := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purges`); n != 0 {
		t.Errorf("a month that was not purged got a marker")
	}
}

// TestCleanupPredictionsAgesSetsWithNoMarks is the second hole: absent / rejected / backtest sets carry
// no marks at all, so "every mark is terminal" is vacuously true and MAX(resolved_at) is NULL. Under a
// bare comparison against the cutoff those rows are never eligible and grow for ever; under a
// COALESCE to a constant they would be deleted the moment their report was purged. Their own made_on
// is the age that makes both cases right.
func TestCleanupPredictionsAgesSetsWithNoMarks(t *testing.T) {
	st := newTestStore(t)
	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)

	// Two mark-less sets in one month, aged by made_on. Both orphaned, both in the same cohort.
	for _, madeOn := range []string{"2022-03-04", "2022-03-19"} {
		if _, err := st.exec(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256)
			VALUES(NULL,'"+predTestRtype+"',?,?,?,?,?)`, madeOn+"T14:40:12Z", madeOn, "2022-03", predStateRejected, "sha-"+madeOn); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := st.CountPredictionsBefore(cutoff); err != nil || n != 2 {
		t.Fatalf("mark-less sets eligible = %d (err=%v), want 2", n, err)
	}

	// A mark-less set from last week is NOT eligible: purging its report must not make it fair game.
	if _, err := st.exec(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256)
		VALUES(NULL,'"+predTestRtype+"',?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format("2006-01-02"),
		time.Now().UTC().Format("2006-01"), predStateAbsent, "sha-fresh"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountPredictionsBefore(cutoff); n != 2 {
		t.Errorf("eligible = %d after adding a fresh mark-less set, want 2 (the fresh one must not age out)", n)
	}
}

// TestCleanupPredictionsRefusedFinalCountsAsSettled: a mark that ended refused_final is terminal and
// stamps resolved_at, so a set whose every mark landed that way ages out normally rather than being
// stuck un-scored for ever.
func TestCleanupPredictionsRefusedFinalCountsAsSettled(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	setID := seedPredictionSet(t, st, sql.NullInt64{}, "sha-refused")
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d5','refused_final',?)`, setID, old); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)
	if n, _ := st.CountPredictionsBefore(cutoff); n != 1 {
		t.Errorf("a fully refused_final set counted %d, want 1 (it is terminal)", n)
	}
}

// TestCleanupPredictionsPurgeMarkerIsWhatMakesItVisible: without the marker a purged month reads as a
// month in which nothing was ever predicted.
func TestCleanupPredictionsPurgeMarkerIsWhatMakesItVisible(t *testing.T) {
	s := routeTableServer(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	seedFinishedPredictionSet(t, s.st, old)

	if res := s.runCleanup("manual", false, cleanupTargets{Predictions: true}); !res.OK || res.Predictions != 1 {
		t.Fatalf("pass: ok=%v n=%d err=%s", res.OK, res.Predictions, res.Error)
	}
	purged := s.st.PurgedPredictionMonths()
	if len(purged) != 1 {
		t.Fatalf("purge markers = %v, want exactly one month", purged)
	}
	for month, at := range purged {
		if month[0] != predTestRtype || month[1] == "" {
			t.Errorf("marker names %v, want the (rtype, cohort) of the purged month", month)
		}
		if at == "" {
			t.Error("the purge marker carries no date, so the page cannot say when")
		}
	}
}

// TestCleanupPredictionPredicateRespectsTheCutoff: terminal and orphaned is not enough — the newest
// resolved_at must be older than the cutoff. A set settled yesterday is inside a three-year window.
func TestCleanupPredictionPredicateRespectsTheCutoff(t *testing.T) {
	st := newTestStore(t)
	recent := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	seedFinishedPredictionSet(t, st, recent)
	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)

	if n, _ := st.CountPredictionsBefore(cutoff); n != 0 {
		t.Errorf("a recently settled set counted as eligible: %d", n)
	}
}

// TestCleanupPredictionsFloorIsClampedOnRead: a hand-edited meta value cannot drive the retention
// below its floor. The floor's job is narrow — it stops "set the days to 0 and empty the ledger in
// one pass" — but it has to hold on read, not only on save, or editing meta bypasses it.
func TestCleanupPredictionsFloorIsClampedOnRead(t *testing.T) {
	s := routeTableServer(t)
	s.st.SetSetting("cleanup_predictions_days", "1")
	if got := s.cleanupConfigLoad().PredictionsDays; got != minPredictionsRetentionDays {
		t.Errorf("PredictionsDays = %d, want it clamped to %d", got, minPredictionsRetentionDays)
	}
}

// TestCleanupPredictionsShipsDisabled: nothing starts deleting on upgrade. Every other target ships
// off for this reason, and this one deletes something that cannot be rebuilt at all.
func TestCleanupPredictionsShipsDisabled(t *testing.T) {
	s := routeTableServer(t)
	c := s.cleanupConfigLoad()
	if c.PredictionsEnabled {
		t.Error("the predictions target defaulted to enabled")
	}
	if c.PredictionsDays != predDefaultPredictionsDays {
		t.Errorf("PredictionsDays default = %d, want %d", c.PredictionsDays, predDefaultPredictionsDays)
	}
}

// TestCleanupPredictionsSaveRefusesBelowFloor: refused, not silently clamped. A retention an admin
// typed and did not get is a record they think is kept at one number and is kept at another.
func TestCleanupPredictionsSaveRefusesBelowFloor(t *testing.T) {
	s := routeTableServer(t)
	code, out := call(t, s.apiCleanupConfigSave, `{"predictions_days":100}`, "admin")
	if code == 200 {
		t.Fatalf("saving below the floor was accepted: %v", out)
	}
	if out["code"] != "retention_too_short" {
		t.Errorf("code = %v, want retention_too_short", out["code"])
	}
	if got := s.cleanupConfigLoad().PredictionsDays; got == 100 {
		t.Error("the refused value reached the config")
	}
}

// TestCleanupPredictionsConfigRoundTrips: the switch and the days survive a save and a reload.
func TestCleanupPredictionsConfigRoundTrips(t *testing.T) {
	s := routeTableServer(t)
	if code, _ := call(t, s.apiCleanupConfigSave, `{"predictions_enabled":true,"predictions_days":500}`, "admin"); code != 200 {
		t.Fatalf("save → %d", code)
	}
	c := s.cleanupConfigLoad()
	if !c.PredictionsEnabled || c.PredictionsDays != 500 {
		t.Errorf("after the round trip: enabled=%v days=%d, want true/500", c.PredictionsEnabled, c.PredictionsDays)
	}
}

// TestCleanupPredictionsDryRunWritesNothing: a preview counts and changes nothing — no rows, no
// cleanup_runs row, no last-result blob.
func TestCleanupPredictionsDryRunWritesNothing(t *testing.T) {
	s := routeTableServer(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	seedFinishedPredictionSet(t, s.st, old)

	res := s.runCleanup("preview", true, cleanupTargets{Predictions: true})
	if !res.OK {
		t.Fatalf("preview failed: %s", res.Error)
	}
	if res.Predictions != 1 {
		t.Errorf("preview counted %d, want 1", res.Predictions)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM prediction_sets`); n != 1 {
		t.Errorf("a preview deleted %d sets", 1-n)
	}
	if n := scalar[int](t, s.st, `SELECT COUNT(*) FROM cleanup_runs`); n != 0 {
		t.Errorf("a preview wrote %d cleanup_runs rows", n)
	}
}

// TestCleanupPredictionsOnlyPassAppearsInHistory is the one that is easy to get wrong and invisible
// when you do: cleanup_runs is read through a WHERE clause that hides successful no-op passes. A pass
// that deleted ONLY the ledger writes a row with every other counter at zero, so unless
// predictions_deleted is part of that filter, the pass is recorded and then never shown — the history
// silently omits exactly the passes this target exists to make auditable.
func TestCleanupPredictionsOnlyPassAppearsInHistory(t *testing.T) {
	s := routeTableServer(t)
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	seedFinishedPredictionSet(t, s.st, old)

	res := s.runCleanup("manual", false, cleanupTargets{Predictions: true})
	if !res.OK || res.Predictions != 1 {
		t.Fatalf("pass: ok=%v predictions=%d err=%s", res.OK, res.Predictions, res.Error)
	}

	runs, err := s.st.ListCleanupRuns(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("a ledger-only pass produced %d history rows, want 1", len(runs))
	}
	if runs[0].PredictionsDeleted != 1 {
		t.Errorf("history row predictions_deleted = %d, want 1", runs[0].PredictionsDeleted)
	}
}

// TestCleanupRunRoundTripCarriesPredictions: the column survives insert and read, which is where a
// column-count mismatch in the INSERT would surface as a scan error rather than a wrong number.
func TestCleanupRunRoundTripCarriesPredictions(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.InsertCleanupRun(CleanupRun{RanAt: nowStr(), Trigger: "manual", OK: true, PredictionsDeleted: 7}); err != nil {
		t.Fatalf("InsertCleanupRun: %v", err)
	}
	runs, err := st.ListCleanupRuns(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].PredictionsDeleted != 7 {
		t.Errorf("read back %+v, want one row with predictions_deleted=7", runs)
	}
}

// TestMigration0004AddsCleanupRunsColumn: cleanup_runs is a baseline table, so the new column cannot
// be declared in baseSchemaStmts() — that shape is the acceptance contract and adding to it would
// refuse every existing database. It is a step instead, and an older dump must still load.
func TestMigration0004AddsCleanupRunsColumn(t *testing.T) {
	path := fileStore(t, "pre-cleanup-pred.db")
	// backToTheBaseline already winds back this step's tables and its cleanup_runs column.
	backToTheBaseline(t, path)

	st, err := openStoreForMode("sqlite", path, true)
	if err != nil {
		t.Fatalf("a baseline database must be accepted and migrated: %v", err)
	}
	defer st.Close()
	if !st.columnExists("cleanup_runs", "predictions_deleted") {
		t.Error("the step did not add cleanup_runs.predictions_deleted")
	}
}

// TestPostgresCleanupPredictionsPredicate mirrors the month predicate on the driver production runs:
// a month whose every set is orphaned, terminal and old is purged whole, and a month holding anything
// still live or still unsettled is left alone.
func TestPostgresCleanupPredictionsPredicate(t *testing.T) {
	st := pgStore(t, "prediction_sets", "prediction_claims", "prediction_marks", "prediction_purges")
	old := time.Now().UTC().AddDate(0, 0, -(predDefaultPredictionsDays + 100)).Format(time.RFC3339)
	cutoff := time.Now().UTC().AddDate(0, 0, -predDefaultPredictionsDays)

	// A month of the report type that is entirely purged, aged past the cutoff.
	gone := seedFinishedPredictionSet(t, st, old)

	// A DIFFERENT month of the same report type, holding one unsettled set: the whole month is protected.
	// (The unit is the month, so a single live set shields its siblings — that is the point.)
	settled := seedPredictionSetForKey(t, st, "2025-01", sql.NullInt64{}, "sha-pg-pending")
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status) VALUES(?,'d5','pending')`, settled); err != nil {
		t.Fatal(err)
	}
	protected := seedFinishedPredictionSetForKey(t, st, "2025-01", old)

	if n, err := st.CountPredictionsBefore(cutoff); err != nil || n != 1 {
		t.Fatalf("eligible = %d (err=%v), want 1 (only the fully eligible month)", n, err)
	}
	n, err := st.DeletePredictionsBefore(cutoff)
	if err != nil {
		t.Fatalf("DeletePredictionsBefore: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted = %d, want 1", n)
	}
	if c := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE id=?`, gone); c != 0 {
		t.Error("the eligible month was not purged")
	}
	if c := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_sets WHERE id IN (?,?)`, settled, protected); c != 2 {
		t.Error("a protected month's set was deleted")
	}
	if c := scalar[int](t, st, `SELECT COUNT(*) FROM prediction_purges`); c != 1 {
		t.Errorf("purge markers = %d, want 1", c)
	}
}

// seedFinishedPredictionSetForKey is seedFinishedPredictionSet in an explicit (rtype, cohort) month, so
// a test can put two months of one report type side by side and assert that only the fully eligible one
// is purged.
func seedFinishedPredictionSetForKey(t *testing.T, st *Store, cohort string, resolvedAt string) int64 {
	t.Helper()
	res, err := st.insertID(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256)
		VALUES(NULL,'"+predTestRtype+"',?,?,?,?,?)`, cohort+"-05T14:40:12Z", cohort+"-05", cohort, predStateAccepted, "sha-"+cohort+"-"+resolvedAt)
	if err != nil {
		t.Fatalf("seed set for %s: %v", cohort, err)
	}
	if _, err := st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, resolved_at)
		VALUES(?,'d5','resolved',?)`, res, resolvedAt); err != nil {
		t.Fatal(err)
	}
	return res
}

// seedPredictionSetForKey is seedPredictionSet in an explicit (rtype, cohort) month.
func seedPredictionSetForKey(t *testing.T, st *Store, cohort string, reportID sql.NullInt64, payloadSHA string) int64 {
	t.Helper()
	v := any(nil)
	if reportID.Valid {
		v = reportID.Int64
	}
	res, err := st.insertID(`INSERT INTO prediction_sets(report_id, rtype, made_at, made_on, cohort, state, payload_sha256)
		VALUES(?,'"+predTestRtype+"',?,?,?,?,?)`, v, cohort+"-05T14:40:12Z", cohort+"-05", cohort, predStateAccepted, payloadSHA)
	if err != nil {
		t.Fatalf("seed set for %s: %v", cohort, err)
	}
	return res
}
