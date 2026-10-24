package app

import (
	"database/sql"
	"strings"
	"time"
)

// reportInstantLayouts are the timestamp formats seen in reports.sent_at, tried in order. sent_at is
// heterogeneous in production: the v1 path stamps Go's RFC3339 (`...Z`), but the bulk of rows are a
// timezone-less microsecond form (`2006-01-02T15:04:05.575420`, e.g. from a Python isoformat ingest
// path). A layout with no zone is parsed as UTC (the system's on-the-wire convention for instants).
// The space-separated variants are defensive. A date-only or empty value matches none and is kept
// (fail-closed) — it carries no precise instant to age against.
var reportInstantLayouts = []string{
	time.RFC3339Nano,                // zoned, optional fraction (covers RFC3339 too)
	"2006-01-02T15:04:05.999999999", // no zone + fraction  → UTC
	"2006-01-02T15:04:05",           // no zone, no fraction → UTC
	"2006-01-02 15:04:05.999999999", // space + fraction     → UTC (defensive)
	"2006-01-02 15:04:05",           // space, no fraction   → UTC (defensive)
}

// parseReportInstant parses a reports.sent_at value into a UTC instant, trying each known layout.
// ok=false for a value that is empty, date-only, or otherwise not a full instant (kept, fail-closed).
func parseReportInstant(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range reportInstantLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// Storage-cleanup store layer (docs/adr/0017-storage-cleanup.md). Retention deletes are set-based
// and re-assert their terminal predicate so they stay race-safe against the scheduler/reconcile
// paths; reports are handled fail-closed (only rows whose sent_at parses to a real instant
// are ever eligible). Config for the feature lives in the meta k/v table; only the audit history
// (cleanup_runs) is a table.

// Retention floors are Go consts (not settings) so a hand-edited meta value can never drop a
// target below its safety floor — they are clamped on both save (cleanup_api) and read
// (cleanupConfigLoad). Reports are core content, hence the year floor.
const (
	minBatchRetentionDays   = 7
	minReportsRetentionDays = 365
	// The audit log's floor is deliberately short: an operator who wants a 30-day trail should get
	// one rather than be forced to keep a year. The protection that matters here is the opposite
	// one — the target ships OFF, so nothing is ever deleted until someone chooses it.
	minAuditRetentionDays = 30
	// A revision is a recoverable copy of something that still exists, not the thing itself, so its
	// floor is the short one for the same reason the audit log's is: an operator who wants a
	// fortnight of undo should get a fortnight. What protects the history is that the target ships
	// off, and that the per-report cap — the bound most portals will actually use — is separate and
	// unlimited by default.
	minRevisionsRetentionDays = 14
	// The forecast ledger's floor (ADR 0029 §8). Short, and deliberately so: the safety of this target
	// is STRUCTURAL, not numeric. A set still awaiting settlement has a non-terminal mark and never
	// matches the predicate; a set whose report still exists has a non-null report_id and never
	// matches either. Neither depends on how many days an operator typed. What this floor defends is
	// the one thing the predicate cannot: an operator setting the days to 0 and emptying the ledger in
	// a single pass. 180 days is three settlement quarters of margin past the point where every
	// checkpoint but h12 has long resolved.
	minPredictionsRetentionDays = 180
	// predDefaultPredictionsDays is the shipped retention when the key is unset: three years, past h12
	// for every set that can be fully settled, so the default never truncates a live horizon.
	predDefaultPredictionsDays = 1095
)

// cleanupRunsKeep bounds the audit ring buffer: only the most recent N cleanup_runs rows are kept.
//
// It is also the number the console asks for, deliberately — a stored bound larger than the readable
// one is rows nothing can ever show, and the two had drifted to 200 kept against a hard-coded read of
// 50. How many appear at once is the table's business, not this constant's: the console paginates.
const cleanupRunsKeep = 200

// ---------- Target A: batch history ----------

// batchTerminalPred selects terminal batch jobs (finished/cancelled/expired) with a real
// finished_at strictly before the cutoff. finished_at is written by nowStr() (local
// "2006-01-02 15:04:05"), so a lexical "< cutoff" is a correct chronological compare, and the
// non-empty guard keeps a just-finalized or requeued job (finished_at cleared by RequeueItems)
// out of range.
const batchTerminalPred = `status IN ('finished','cancelled','expired') AND finished_at <> '' AND finished_at < ?`

// CountFinishedJobsBefore counts terminal batch jobs older than cutoff (dry-run / usage).
func (s *Store) CountFinishedJobsBefore(cutoff string) (int64, error) {
	var n int64
	err := s.queryRow("SELECT COUNT(*) FROM batch_jobs WHERE "+batchTerminalPred, cutoff).Scan(&n)
	return n, err
}

// DeleteFinishedJobsBefore deletes terminal batch jobs older than cutoff and their batch_items in
// one transaction, as a single set-based DELETE that re-asserts the terminal predicate (NOT a
// select-then-delete loop). A job requeued between a count and this delete simply no longer matches
// the predicate, so it is never removed mid-flight — the race-safe primitive (ADR 0017). Deleting
// at job granularity means an unreconciled 'untracked' item is only ever removed with its
// already-terminal parent job. Returns how many jobs were deleted.
func (s *Store) DeleteFinishedJobsBefore(cutoff string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := clearJobNotificationEvidence(migExec{s: s, ex: tx}, batchTerminalPred, cutoff); err != nil {
		return 0, err
	}
	// Children first: items whose parent job matches the terminal+age predicate.
	if _, err := tx.Exec(s.bind("DELETE FROM batch_items WHERE job_id IN (SELECT id FROM batch_jobs WHERE "+batchTerminalPred+")"), cutoff); err != nil {
		tx.Rollback()
		return 0, err
	}
	res, err := tx.Exec(s.bind("DELETE FROM batch_jobs WHERE "+batchTerminalPred), cutoff)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// ---------- Target B: expired API tokens ----------

// tokensExpiredPred selects tokens with a non-empty expires_at strictly before the cutoff.
// expires_at is stored in the same local "2006-01-02 15:04:05" basis TokenValid compares against,
// so a lexical compare is correct; a NULL/empty expires_at (never-expiring token) is excluded.
const tokensExpiredPred = `expires_at IS NOT NULL AND expires_at <> '' AND expires_at < ?`

// CountExpiredTokensBefore counts tokens expired before cutoff (dry-run / usage).
func (s *Store) CountExpiredTokensBefore(cutoff string) (int64, error) {
	var n int64
	err := s.queryRow("SELECT COUNT(*) FROM api_tokens WHERE "+tokensExpiredPred, cutoff).Scan(&n)
	return n, err
}

// DeleteExpiredTokensBefore deletes tokens whose expiry is older than cutoff. Returns rows deleted.
func (s *Store) DeleteExpiredTokensBefore(cutoff string) (int64, error) {
	res, err := s.exec("DELETE FROM api_tokens WHERE "+tokensExpiredPred, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ---------- Target C: reports (core content, fail-closed) ----------

// reportsIngestedBefore returns the ids of reports whose sent_at parses (via parseReportInstant)
// to a UTC instant strictly before cutoff. sent_at is the last-write instant of a report (it is
// overwritten on every re-ingest), and there is no server-stamped created_at, so it is the age
// signal. It is heterogeneous — zoned RFC3339 on the v1 path, timezone-less microsecond form on
// another — hence the multi-layout parse; a value with no precise instant (date-only/empty) is
// SKIPPED (fail-closed: never delete a report we cannot prove is old). This biases to under-cleaning,
// the safe failure mode for core content.
type reportKey struct {
	id   int64
	sent string
}

// reportsDeleteChunk bounds how many reports one transaction deletes, so a large purge releases the
// single SQLite writer between chunks instead of freezing all reads/writes until commit.
const reportsDeleteChunk = 500

func (s *Store) reportsIngestedBefore(cutoff time.Time) ([]reportKey, error) {
	rows, err := s.query("SELECT id, sent_at FROM reports")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []reportKey
	for rows.Next() {
		var id sql.NullInt64
		var sent sql.NullString
		if err := rows.Scan(&id, &sent); err != nil {
			return nil, err
		}
		if !id.Valid || id.Int64 == 0 || !sent.Valid {
			continue
		}
		t, ok := parseReportInstant(sent.String)
		if !ok {
			continue // date-only / empty / malformed → keep
		}
		if t.Before(cutoff) {
			keys = append(keys, reportKey{id: id.Int64, sent: sent.String})
		}
	}
	return keys, rows.Err()
}

// CountReportsIngestedBefore counts reports eligible for retention deletion at cutoff (dry-run / usage).
func (s *Store) CountReportsIngestedBefore(cutoff time.Time) (int64, error) {
	keys, err := s.reportsIngestedBefore(cutoff)
	return int64(len(keys)), err
}

// DeleteReportsIngestedBefore deletes reports whose sent_at parses to an instant older than cutoff,
// cascading their tracking_items. The scan captures each row's exact sent_at and the delete
// RE-ASSERTS it (WHERE id=? AND sent_at=?): a report re-ingested between scan and delete has a fresh
// sent_at, so it no longer matches and is preserved — keeping the fail-closed guarantee across the
// scan/delete window. Runs in bounded chunks (each its own tx) so a big purge doesn't hold the single
// SQLite connection for the whole run. Returns how many reports were deleted (partial count on error).
func (s *Store) DeleteReportsIngestedBefore(cutoff time.Time) (int64, error) {
	keys, err := s.reportsIngestedBefore(cutoff)
	if err != nil || len(keys) == 0 {
		return 0, err
	}
	var total int64
	for i := 0; i < len(keys); i += reportsDeleteChunk {
		end := i + reportsDeleteChunk
		if end > len(keys) {
			end = len(keys)
		}
		n, err := s.deleteReportChunk(keys[i:end])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// predictionPurgeMonths returns the (rtype, cohort) months eligible for retention deletion at cutoff.
//
// The unit is a WHOLE MONTH OF ONE REPORT TYPE, not a set, and that is the load-bearing decision:
//
//   - A mature cell must never change (ADR 0029 §9), and the monthly page is citable precisely because
//     of that. Deleting some sets of a month would move that month's n and every metric on it while the
//     page kept showing the same label.
//   - Worse, membership is "the earliest accepted set per (symbol, group) decides" (D4). Deleting only
//     the earliest set of a symbol would PROMOTE a later rerun as the member — exactly the
//     selection-by-rerun that D4 exists to forbid.
//
// So a month is eligible only when EVERY set in it is eligible. The month's numbers then disappear as a
// unit and are marked purged, rather than quietly changing.
//
// Per set, the predicate is:
//
//	report_id IS NULL     the report is gone, so no live link points at this set
//	no non-terminal mark  nothing is pending or retryable, so the set can never change again
//	settled < cutoff      when the set became final (see below)
//
// settled is substr(COALESCE(MAX(resolved_at), made_on), 1, 10) — the DATE a set became final — rather
// than MAX(resolved_at) alone, because two of the four set shapes have no resolved_at at all:
//
//   - absent / rejected / backtest carry no marks, so "every mark is terminal" is vacuously true and
//     MAX(resolved_at) is NULL. Under a bare `<` those rows would never be eligible and would grow for
//     ever; under a COALESCE to a constant they would be eligible the moment their report was purged.
//   - accepted sets whose every mark ended refused_final DO have marks, and resolved_at is stamped on
//     that transition — the evaluator writes it whenever a mark becomes terminal, whichever way it
//     landed (ADR 0029 §6.3).
//
// Falling back to made_on gives those shapes their own age, so purging a report never makes its sets
// immediately eligible. The truncation to 10 characters is what makes the two spellings comparable:
// resolved_at is an RFC3339 instant and made_on is a civil date, and comparing a full instant against
// a date cutoff would put everything inside the cutoff day on the wrong side.
//
// The per-set columns are computed in a subquery rather than inside the aggregate because a correlated
// subquery on ps.id is not valid in HAVING — ps.id is not functionally dependent on (rtype, cohort).
func (s *Store) predictionPurgeMonths(cutoff time.Time) ([][2]string, error) {
	rows, err := s.query(`SELECT t.rtype, t.cohort FROM (
			SELECT ps.rtype AS rtype, ps.cohort AS cohort, ps.id AS id,
			  CASE WHEN ps.report_id IS NULL THEN 1 ELSE 0 END AS orphan,
			  CASE WHEN EXISTS (SELECT 1 FROM prediction_marks m
			        WHERE m.set_id = ps.id AND m.status NOT IN ('resolved','refused_final'))
			       THEN 1 ELSE 0 END AS live,
			  substr(COALESCE(
			    (SELECT MAX(m.resolved_at) FROM prediction_marks m WHERE m.set_id = ps.id),
			    ps.made_on), 1, 10) AS settled
			FROM prediction_sets ps
		) t
		GROUP BY t.rtype, t.cohort
		HAVING SUM(t.orphan) = COUNT(*) AND SUM(t.live) = 0 AND MAX(t.settled) < ?
		ORDER BY t.rtype, t.cohort`, cutoff.UTC().Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var rtype, cohort string
		if err := rows.Scan(&rtype, &cohort); err != nil {
			return nil, err
		}
		out = append(out, [2]string{rtype, cohort})
	}
	return out, rows.Err()
}

// monthIsPurgeable re-asserts the whole-month predicate for one (rtype, cohort) inside a transaction.
// The scan above ran on its own snapshot, so a set whose report came back in the gap must not be
// deleted on a stale read.
func (s *Store) monthIsPurgeable(tx *sql.Tx, rtype, cohort, cutoffDate string) (bool, error) {
	var all, orphan, live int64
	var newest sql.NullString
	err := tx.QueryRow(s.bind(`SELECT COUNT(*),
		  SUM(CASE WHEN ps.report_id IS NULL THEN 1 ELSE 0 END),
		  SUM(CASE WHEN EXISTS (SELECT 1 FROM prediction_marks m
		        WHERE m.set_id = ps.id AND m.status NOT IN ('resolved','refused_final'))
		       THEN 1 ELSE 0 END),
		  MAX(substr(COALESCE(
		    (SELECT MAX(m.resolved_at) FROM prediction_marks m WHERE m.set_id = ps.id),
		    ps.made_on), 1, 10))
		FROM prediction_sets ps WHERE ps.rtype=? AND ps.cohort=?`), rtype, cohort).
		Scan(&all, &orphan, &live, &newest)
	if err != nil {
		return false, err
	}
	if all == 0 {
		return false, nil // nothing left to purge
	}
	if orphan != all || live != 0 {
		return false, nil
	}
	return newest.Valid && newest.String < cutoffDate, nil
}

// CountPredictionsBefore counts the ledger SETS a purge at cutoff would remove (dry-run / usage). The
// unit of a pass is a month, but the number an admin sees is rows, matching every other target.
func (s *Store) CountPredictionsBefore(cutoff time.Time) (int64, error) {
	months, err := s.predictionPurgeMonths(cutoff)
	if err != nil || len(months) == 0 {
		return 0, err
	}
	var total int64
	for _, m := range months {
		var n int64
		if err := s.queryRow(`SELECT COUNT(*) FROM prediction_sets WHERE rtype=? AND cohort=?`,
			m[0], m[1]).Scan(&n); err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// DeletePredictionsBefore purges eligible months and records a marker for each.
//
// One transaction for the whole pass rather than the chunked shape the reports target uses: a month is
// the atomic unit, and a pass that deleted half a month would produce exactly the state this design
// exists to prevent.
//
// The transaction takes the ownership lock before it reads any set (ADR 0039). Before a month's sets
// are deleted it snapshots which OUs owned them as purge evidence, in the same transaction as the
// deletion and the marker, so own-scoped readers can later be told about exactly the months their
// subtree owned. Deleting an OU takes the same lock, so the two serialize: whichever commits second
// sees the other's result, and no evidence can name an OU that is gone.
//
// The candidate scan above runs outside the transaction (the single-connection rule) and only
// nominates months; every nominated month is verified again under the lock, from fresh reads.
func (s *Store) DeletePredictionsBefore(cutoff time.Time) (int64, error) {
	months, err := s.predictionPurgeMonths(cutoff)
	if err != nil || len(months) == 0 {
		return 0, err
	}
	cutoffDate := cutoff.UTC().Format("2006-01-02")
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	e := migExec{s: s, ex: tx}
	if err := lockPredictionOwnership(e); err != nil {
		return 0, err
	}
	var total int64
	for _, m := range months {
		rtype, cohort := m[0], m[1]
		ok, err := s.monthIsPurgeable(tx, rtype, cohort, cutoffDate)
		if err != nil {
			return total, err
		}
		if !ok {
			continue // the month changed under us — leave it whole
		}
		// Evidence first, while the sets still exist to be read: a set that is gone proves nothing.
		if err := capturePredictionPurgeOwners(e, rtype, cohort); err != nil {
			return total, err
		}
		// Children first, so a failure cannot leave a claim or a mark pointing at a set that is gone.
		if _, err := tx.Exec(s.bind(`DELETE FROM prediction_claims WHERE set_id IN
			(SELECT id FROM prediction_sets WHERE rtype=? AND cohort=?)`), rtype, cohort); err != nil {
			return total, err
		}
		if _, err := tx.Exec(s.bind(`DELETE FROM prediction_marks WHERE set_id IN
			(SELECT id FROM prediction_sets WHERE rtype=? AND cohort=?)`), rtype, cohort); err != nil {
			return total, err
		}
		res, err := tx.Exec(s.bind(`DELETE FROM prediction_sets WHERE rtype=? AND cohort=?`), rtype, cohort)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		// The marker is what makes the deletion visible. Without it a purged month reads as a month in
		// which nothing was ever predicted, which is a different and far better-looking claim.
		if _, err := tx.Exec(s.bind(`INSERT INTO prediction_purges(rtype, cohort, purged_at, sets_removed)
			VALUES(?,?,?,?)`), rtype, cohort, nowStr(), n); err != nil {
			return total, err
		}
		total += n
	}
	if err := tx.Commit(); err != nil {
		return total, err
	}
	return total, nil
}

// PurgedPredictionMonths returns the months a pass has purged, keyed by (rtype, cohort), so the monthly
// page can label them "purged" instead of recomputing them as months with no predictions.
func (s *Store) PurgedPredictionMonths() map[[2]string]string {
	out, _ := s.loadPredictionPurgeMarkers()
	return out
}

// loadPredictionPurgeMarkers preserves errors for read routes: a failed marker lookup must never
// authorize serving a stale mature cell as though no retention pass had happened.
func (s *Store) loadPredictionPurgeMarkers() (map[[2]string]string, error) {
	out := map[[2]string]string{}
	rows, err := s.query(`SELECT rtype, cohort, purged_at FROM prediction_purges`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rtype, cohort, at string
		if err := rows.Scan(&rtype, &cohort, &at); err != nil {
			return nil, err
		}
		out[[2]string{rtype, cohort}] = at
	}
	return out, rows.Err()
}

// deleteReportChunk deletes one chunk of reports (+ their tracking_items) in a single transaction,
// re-asserting sent_at so a concurrently re-ingested report is skipped. tracking_items are removed
// only for reports whose row actually matched (so a preserved report keeps its items), and so are
// the viewer rows — a viewer row pointing at a deleted report is an access grant with nothing on the
// other end (ADR 0024) — and so is its edit history, which is whole prior copies of a body that
// would otherwise stay on disk under an id nothing points at (ADR 0026).
//
// prediction_sets is the one child that is NULLED rather than deleted (ADR 0029 §8), and the reason
// is the same id-reuse argument DeleteReport states in full: ids are reassigned, so a row left
// pointing at a reused id would count another report's claims as this one's. Deleting them instead
// would be worse in the other direction — a set outlives its report by design, because the reports
// retention floor is 365 days while a set's h12 mark resolves twelve months after it was made. So the
// link goes and the ledger stays, on the same guarded match as the tables above it.
func (s *Store) deleteReportChunk(keys []reportKey) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	var n int64
	for _, k := range keys {
		res, err := tx.Exec(s.bind("DELETE FROM reports WHERE id=? AND sent_at=?"), k.id, k.sent)
		if err != nil {
			tx.Rollback()
			return n, err
		}
		c, _ := res.RowsAffected()
		if c == 0 {
			continue // re-ingested (sent_at changed) or already gone — keep its tracking_items
		}
		if _, err := tx.Exec(s.bind("DELETE FROM report_viewers WHERE report_id=?"), k.id); err != nil {
			tx.Rollback()
			return n, err
		}
		if _, err := tx.Exec(s.bind("DELETE FROM tracking_items WHERE report_id=?"), k.id); err != nil {
			tx.Rollback()
			return n, err
		}
		if _, err := tx.Exec(s.bind("DELETE FROM report_revisions WHERE report_id=?"), k.id); err != nil {
			tx.Rollback()
			return n, err
		}
		if _, err := tx.Exec(s.bind("UPDATE prediction_sets SET report_id=NULL WHERE report_id=?"), k.id); err != nil {
			tx.Rollback()
			return n, err
		}
		n += c
	}
	if err := tx.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// ---------- Storage usage (analysis screen) ----------

// usageCount returns the row count of an internal (non-user-supplied) table name.
func (s *Store) usageCount(table string) int64 {
	var n int64
	s.queryRow("SELECT COUNT(*) FROM " + table).Scan(&n)
	return n
}

// usageBytes returns COALESCE(SUM(expr),0) over a table — an approximate storage footprint from
// its large TEXT columns. table and expr are internal constants (never user input).
func (s *Store) usageBytes(table, expr string) int64 {
	var n int64
	s.queryRow("SELECT COALESCE(SUM(" + expr + "),0) FROM " + table).Scan(&n)
	return n
}

// usageSpan returns the oldest/newest non-empty value of a timestamp column, for the usage display.
func (s *Store) usageSpan(table, col string) (oldest, newest string) {
	var o, nw sql.NullString
	s.queryRow("SELECT MIN("+col+"), MAX("+col+") FROM "+table+" WHERE "+col+" IS NOT NULL AND "+col+" <> ''").Scan(&o, &nw)
	return o.String, nw.String
}

// DBSizeBytes returns the approximate total database size in bytes — sqlite page_count*page_size,
// postgres pg_database_size(current_database()). Best-effort: returns 0 on any error.
func (s *Store) DBSizeBytes() int64 {
	if s.driver == "postgres" {
		var n int64
		if s.queryRow("SELECT pg_database_size(current_database())").Scan(&n) == nil {
			return n
		}
		return 0
	}
	var pageCount, pageSize int64
	s.queryRow("PRAGMA page_count").Scan(&pageCount)
	s.queryRow("PRAGMA page_size").Scan(&pageSize)
	return pageCount * pageSize
}

// ---------- Audit history (cleanup_runs) ----------

// CleanupRun is one recorded cleanup pass, surfaced in the admin console's run history.
type CleanupRun struct {
	ID             int64  `json:"id"`
	RanAt          string `json:"ran_at"`
	Trigger        string `json:"trigger"` // "schedule" | "manual"
	DryRun         bool   `json:"dry_run"`
	OK             bool   `json:"ok"`
	Error          string `json:"error"`
	BatchDeleted   int64  `json:"batch_deleted"`
	TokensDeleted  int64  `json:"tokens_deleted"`
	ReportsDeleted int64  `json:"reports_deleted"`
	// The fourth target (ADR 0017). Same shape as the other three rather than a JSON blob, because
	// the history table is read as columns and one target does not justify migrating the other three.
	AuditDeleted int64 `json:"audit_deleted"`
	// The fifth (ADR 0026), same shape again for the same reason.
	RevisionsDeleted int64 `json:"revisions_deleted"`
	// The sixth (ADR 0029), same shape again for the same reason.
	PredictionsDeleted int64 `json:"predictions_deleted"`
	// Bytes handed back to the filesystem by the pass. Not a target — it is the consequence of the
	// others, and the number that answers "did this actually free any disk?".
	BytesReclaimed int64 `json:"bytes_reclaimed"`
	DurationMs     int64 `json:"duration_ms"`
}

// InsertCleanupRun appends an audit row and trims the ring buffer to the most recent
// cleanupRunsKeep rows.
func (s *Store) InsertCleanupRun(c CleanupRun) (int64, error) {
	id, err := s.insertID(`INSERT INTO cleanup_runs(ran_at,trigger,dry_run,ok,error,batch_deleted,tokens_deleted,reports_deleted,audit_deleted,revisions_deleted,predictions_deleted,bytes_reclaimed,duration_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.RanAt, c.Trigger, boolInt(c.DryRun), boolInt(c.OK), c.Error,
		c.BatchDeleted, c.TokensDeleted, c.ReportsDeleted, c.AuditDeleted, c.RevisionsDeleted,
		c.PredictionsDeleted, c.BytesReclaimed, c.DurationMs)
	if err != nil {
		return 0, err
	}
	s.trimCleanupRuns()
	return id, nil
}

// trimCleanupRuns keeps only the most recent cleanupRunsKeep audit rows. Idempotency of the
// scheduler is NOT derived from this table (it lives in the meta key cleanup_last_run_period), so
// trimming can never cause a re-fire.
func (s *Store) trimCleanupRuns() {
	s.exec("DELETE FROM cleanup_runs WHERE id NOT IN (SELECT id FROM cleanup_runs ORDER BY id DESC LIMIT ?)", cleanupRunsKeep)
}

// ListCleanupRuns returns recent audit rows, newest first (capped at cleanupRunsKeep).
func (s *Store) ListCleanupRuns(limit int) ([]CleanupRun, error) {
	if limit <= 0 || limit > cleanupRunsKeep {
		limit = cleanupRunsKeep
	}
	rows, err := s.query(`SELECT id,ran_at,trigger,dry_run,ok,error,batch_deleted,tokens_deleted,reports_deleted,
		COALESCE(audit_deleted,0),COALESCE(revisions_deleted,0),COALESCE(predictions_deleted,0),COALESCE(bytes_reclaimed,0),duration_ms
		FROM cleanup_runs
		-- A successful pass that deleted nothing is not history, and builds before v0.4.20 recorded
		-- one per scheduled run. They are left in the table rather than deleted -- a retention log
		-- is the last thing to go rewriting -- but they are not shown: on a portal whose targets
		-- never match, they are the entire list.
		WHERE NOT (ok <> 0 AND batch_deleted = 0 AND tokens_deleted = 0
			AND COALESCE(audit_deleted,0) = 0 AND COALESCE(revisions_deleted,0) = 0
			AND COALESCE(predictions_deleted,0) = 0
			AND reports_deleted = 0)
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CleanupRun
	for rows.Next() {
		var c CleanupRun
		var dry, ok int64
		if err := rows.Scan(&c.ID, &c.RanAt, &c.Trigger, &dry, &ok, &c.Error, &c.BatchDeleted, &c.TokensDeleted,
			&c.ReportsDeleted, &c.AuditDeleted, &c.RevisionsDeleted, &c.PredictionsDeleted,
			&c.BytesReclaimed, &c.DurationMs); err != nil {
			return nil, err
		}
		c.DryRun = dry != 0
		c.OK = ok != 0
		out = append(out, c)
	}
	return out, rows.Err()
}
