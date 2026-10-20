package app

// The ledger's write path (ADR 0029 §2, plan §3.3).
//
// One function inserts a set with its claims and its pending marks. It is a transaction of its own,
// deliberately: v1Ingest writes the report through the pool, and the store's SQLite side is a single
// connection (store.maxOpenConns == 1), so reusing the report's transaction would deadlock the moment
// anything inside it reached for the pool. That is also why the classifier runs before any of this:
// the alternative is a write that a later refusal has to undo.

import (
	"database/sql"
	"log"
)

// InsertPredictionSet writes one set row, its claims and its pending marks, and reports whether this
// payload had already been stored for this report.
//
// The idempotency key is (report_id, payload_sha256): a retried ingest — the producer's HTTP client
// re-sending after a timeout it did not see the answer to — must not double the row. A payload that
// genuinely changed hashes differently and gets its own set, which is the revision the ledger keeps.
func (s *Store) InsertPredictionSet(reportID int64, set *predSetRow, claims []predClaimRow) (setID int64, dup bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed

	// The conflict is checked rather than caught, because LastInsertId is the only portable way to
	// read the id back on SQLite and it is undefined after a no-op insert.
	var existing int64
	err = tx.QueryRow(s.bind(
		`SELECT id FROM prediction_sets WHERE report_id=? AND payload_sha256=?`),
		reportID, set.PayloadSHA).Scan(&existing)
	switch {
	case err == nil:
		return existing, true, nil
	case err != sql.ErrNoRows:
		return 0, false, err
	}

	// report_id is nullable in the schema because a purge nulls it. On the write path it is always a
	// real report, so it goes in as a plain value.
	var setPtr any = reportID
	setID, err = s.insertIDTx(tx, s.bind(`INSERT INTO prediction_sets(
			report_id, symbol, rtype, version, title, report_date, made_at, made_on, cohort, zone,
			state, errors, payload, payload_truncated, payload_sha256, body_sha256,
			emitter_app, emitter_version, calculator, schema_name,
			policy_version, probability_method, probability_transform, scenario_basis,
			decision_status, decision_quality, information_cutoff,
			horizon_end, horizon_std,
			emitted_base_fen, emitted_base_as_of, emitted_base_source, bench_code, attributed_ou)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		setPtr, set.Symbol, set.Rtype, set.Version, set.Title, set.ReportDate,
		set.MadeAt, set.MadeOn, set.Cohort, set.Zone, set.State, set.Errors,
		set.Payload, set.Truncated, set.PayloadSHA, set.BodySHA,
		set.EmitterApp, set.EmitterVersion, set.Calculator, set.SchemaName,
		set.PolicyVersion, set.ProbabilityMethod, set.ProbabilityTransform, set.ScenarioBasis,
		set.DecisionStatus, set.DecisionQuality, set.InformationCutoff,
		set.HorizonEnd, set.HorizonStd,
		nullableFen(set.EmittedBaseFen), set.EmittedBaseAsOf, set.EmittedBaseSrc, set.BenchCode,
		nullableFen(set.AttributedOU))
	if err != nil {
		return 0, false, err
	}

	for _, c := range claims {
		if _, err := tx.Exec(s.bind(`INSERT INTO prediction_claims(
				set_id, claim_key, kind, stance, label, price_fen, prob_bp, weight_bp)
			VALUES(?,?,?,?,?,?,?,?)`),
			setID, c.ClaimKey, c.Kind, c.Stance, c.Label,
			nullableFen(c.PriceFen), nullableInt32(c.ProbBp), nullableInt32(c.WeightBp)); err != nil {
			return 0, false, err
		}
	}

	// Marks are created with the set, one per checkpoint, pending. A set with nothing resolved is
	// then visible as "waiting" rather than absent from every query that joins marks.
	if set.State == predStateAccepted {
		for _, cp := range predMarkCheckpoints(set.HorizonStd == 1) {
			if _, err := tx.Exec(s.bind(
				`INSERT INTO prediction_marks(set_id, checkpoint, status, bench_code) VALUES(?,?,?,?)`),
				setID, cp, markPending, set.BenchCode); err != nil {
				return 0, false, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return setID, false, nil
}

// insertIDTx is insertID against a caller's transaction rather than the pool.
//
// It exists because the pool-level version would open a second connection to read LastInsertId, and on
// SQLite (a single-connection pool) that is a deadlock against the transaction already in progress —
// the exact failure this file's header describes. Same branch, different executor.
func (s *Store) insertIDTx(tx *sql.Tx, q string, args ...any) (int64, error) {
	if s.driver == "postgres" {
		var id int64
		err := tx.QueryRow(q+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// nullableFen / nullableInt64 / nullableInt32 map a Go pointer to the driver's NULL when it is nil.
// The columns are nullable because "the producer did not send this" and "the producer sent zero" are
// different claims: a target weight of 0 is a decision, and a missing one is not.
func nullableFen(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt32(v *int32) any {
	if v == nil {
		return nil
	}
	return *v
}

// logPredictionsStoreFailed records a ledger write that could not be made. It is logged rather than
// returned as a 5xx because the report is already stored: failing the request would make the producer
// retry a whole run whose report exists, and the coverage page is where this shows up instead.
func logPredictionsStoreFailed(reportID int64, err error) {
	log.Printf("forecast: ledger write failed for report %d: %v", reportID, err)
}
