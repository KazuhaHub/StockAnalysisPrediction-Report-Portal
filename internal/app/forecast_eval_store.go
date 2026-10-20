package app

// The evaluator's database side (ADR 0029 §6, plan §6.3).
//
// Every write here is GUARDED by the row's current state, and that is the load-bearing property of the
// whole feature: `resolved` and `refused_final` are terminal, so a mature cell's inputs cannot change
// after the fact. Without the guard, a second evaluator pass — from an overlapping tick, a restart, or
// a retry — could rewrite a conclusion, and a cell that promised to be immutable would quietly move.

import (
	"database/sql"
	"errors"
	"log"
	"strconv"
	"time"
)

// forecastWorkSet is one set with the facts its marks need, in one row.
type forecastWorkSet struct {
	SetID      int64
	Symbol     string
	Market     string
	MadeOn     string
	MadeAt     string
	HorizonEnd string
	HorizonStd bool
	BenchCode  string
	BaseCheck  string
	S0Date     string
	BaseDate   string
	S0Fen      *int64
	EmitFen    *int64
	Marks      []forecastMark
}

// forecastMark is one pending or retryable checkpoint.
type forecastMark struct {
	ID            int64
	Checkpoint    string
	Status        string
	Attempts      int
	Refusal       string
	NextAttemptAt string
	SessionDate   string
}

// forecastSetsDueQuery is the one selection statement. The tests EXPLAIN this exact text, so the plan
// they pin is the plan production runs — a copy in a test could drift and keep passing.
const forecastSetsDueQuery = `SELECT ps.id, ps.symbol, ps.made_on, ps.made_at, ps.horizon_end, ps.horizon_std,
		ps.bench_code, ps.base_check, ps.s0_date, ps.base_date, ps.s0_fen,
		ps.emitted_base_fen
	FROM prediction_sets ps
	JOIN (SELECT DISTINCT set_id FROM prediction_marks
	      WHERE status IN (?,?) AND (next_attempt_at = '' OR next_attempt_at <= ?)) due
	  ON due.set_id = ps.id
	WHERE ps.state = ?
	ORDER BY ps.made_on, ps.id
	LIMIT ?`

// ForecastSetsDue returns the sets with work due, oldest first, bounded by limit.
//
// It drives from the marks' due index, not from prediction_sets: a set with no mark whose
// next_attempt_at has passed costs nothing, and a set 300 rows later cannot be starved by a
// correlated EXISTS that never uses the index. A new mark has next_attempt_at = ”, which sorts
// before every RFC3339 instant, so it is selected once and scheduled without a vendor call.
func (s *Store) ForecastSetsDue(now time.Time, limit int) ([]forecastWorkSet, error) {
	if limit <= 0 {
		limit = predEvalBatch
	}
	nowStr := now.UTC().Format(time.RFC3339)
	rows, err := s.query(forecastSetsDueQuery,
		markPending, markRefused, nowStr, predStateAccepted, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []forecastWorkSet
	var ids []int64
	for rows.Next() {
		var w forecastWorkSet
		var horizonStd int
		var s0, emit sql.NullInt64
		if err := rows.Scan(&w.SetID, &w.Symbol, &w.MadeOn, &w.MadeAt, &w.HorizonEnd, &horizonStd,
			&w.BenchCode, &w.BaseCheck, &w.S0Date, &w.BaseDate, &s0, &emit); err != nil {
			return nil, err
		}
		w.HorizonStd = horizonStd == 1
		if s0.Valid {
			v := s0.Int64
			w.S0Fen = &v
		}
		if emit.Valid {
			v := emit.Int64
			w.EmitFen = &v
		}
		w.Market = marketPrefix(w.Symbol)
		if w.Market == "" {
			w.Market = "sh"
		}
		out = append(out, w)
		ids = append(ids, w.SetID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	marks, err := s.forecastWorkMarks(ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Marks = marks[out[i].SetID]
	}
	return out, nil
}

func (s *Store) forecastWorkMarks(setIDs []int64) (map[int64][]forecastMark, error) {
	rows, err := s.query(`SELECT set_id, id, checkpoint, status, attempts, refusal,
			next_attempt_at, session_date
		FROM prediction_marks
		WHERE set_id IN (`+placeholders(len(setIDs))+`)
		  AND status IN (?,?)
		ORDER BY set_id, `+predCheckpointOrderSQL(),
		append(int64Args(setIDs), markPending, markRefused)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]forecastMark{}
	for rows.Next() {
		var setID int64
		var m forecastMark
		if err := rows.Scan(&setID, &m.ID, &m.Checkpoint, &m.Status, &m.Attempts,
			&m.Refusal, &m.NextAttemptAt, &m.SessionDate); err != nil {
			return nil, err
		}
		out[setID] = append(out[setID], m)
	}
	return out, rows.Err()
}

// forecastBaseline is the set's resolved start point and the benchmark series it came from.
type forecastBaseline struct {
	S0       string
	BaseDate string
	S0Fen    int64
	Check    string
	Index    []QuoteBar
}

// forecastDeferGroup is one deferral written against a group of marks sharing a code and session.
type forecastDeferGroup struct {
	marks   []forecastMark
	code    string
	session string
}

type forecastSettle struct {
	markID int64
	res    markResult
}

type forecastFinal struct {
	markID int64
	code   string
}

// forecastPlan is one set's whole outcome for a tick, applied in one transaction so a crash cannot
// leave half a set settled and the other half still pending with a stale wake.
type forecastPlan struct {
	setID            int64
	wakes            map[int64]time.Time
	defers           []forecastDeferGroup
	settles          []forecastSettle
	finals           []forecastFinal
	baseline         *forecastBaseline
	terminalBaseline bool
}

func (p *forecastPlan) deferMark(m forecastMark, code, session string) {
	for i := range p.defers {
		if p.defers[i].code == code && p.defers[i].session == session {
			p.defers[i].marks = append(p.defers[i].marks, m)
			return
		}
	}
	p.defers = append(p.defers, forecastDeferGroup{marks: []forecastMark{m}, code: code, session: session})
}

// ApplyForecastPlan applies a set's outcome in one transaction. The baseline is written first: every
// mark of a set is measured from it, and a half-written plan must not leave marks pointing at an
// unresolved start.
func (s *Store) ApplyForecastPlan(p forecastPlan, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed

	op := 0
	check := func() error {
		op++
		if s.onForecastApply != nil {
			return s.onForecastApply(op)
		}
		return nil
	}
	if p.baseline != nil {
		if err := check(); err != nil {
			return err
		}
		written, err := s.finishForecastBaselineTx(tx, p.setID, *p.baseline)
		if err != nil {
			return err
		}
		if !written {
			// Another pass made the baseline terminal between selection and this write. Settling
			// with the values this pass computed would contradict the stored baseline, so abort and
			// let the next tick read the stored one.
			return errForecastBaselineRaced
		}
	}
	for _, d := range p.defers {
		if err := check(); err != nil {
			return err
		}
		if err := s.DeferForecastMarks(tx, d.marks, d.code, d.session, now); err != nil {
			return err
		}
	}
	for _, st := range p.settles {
		if err := check(); err != nil {
			return err
		}
		if err := s.SettleForecastMark(tx, st.markID, st.res, now); err != nil {
			return err
		}
	}
	for _, f := range p.finals {
		if err := check(); err != nil {
			return err
		}
		if err := s.MarkRefusedFinal(tx, f.markID, f.code, now); err != nil {
			return err
		}
	}
	for id, wake := range p.wakes {
		if err := check(); err != nil {
			return err
		}
		if _, err := tx.Exec(s.bind(`UPDATE prediction_marks SET next_attempt_at=?
			WHERE id=? AND status IN (?,?)`),
			wake.UTC().Format(time.RFC3339), id, markPending, markRefused); err != nil {
			return err
		}
	}
	if p.terminalBaseline {
		if err := check(); err != nil {
			return err
		}
		if err := s.finishForecastBaselineTerminalTx(tx, p.setID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeferForecastMarks records a failed attempt on DUE marks only, with the session and the evaluator
// clock. The refusal class decides whether this is an attempt at all: a source failure leaves
// attempts unchanged and never counts toward finality (V26).
func (s *Store) DeferForecastMarks(tx *sql.Tx, marks []forecastMark, code, session string, now time.Time) error {
	class := refusalClassOf(code)
	for _, m := range marks {
		next := now.UTC().Add(time.Hour)
		columns := `status=?, refusal=?, session_date=?, next_attempt_at=?`
		if class == refusalMarkSpecific {
			next = now.UTC().Add(predRetryBackoff(m.Attempts + 1))
			columns = `status=?, refusal=?, session_date=?, attempts=attempts+1, next_attempt_at=?`
		} else if m.Refusal != code {
			// The first source failure of this code retries quickly; a fault that persists backs off.
			next = now.UTC().Add(10 * time.Minute)
		}
		if _, err := tx.Exec(s.bind(`UPDATE prediction_marks SET `+columns+`
			WHERE id=? AND status IN (?,?)`),
			markRefused, code, session, next.Format(time.RFC3339),
			m.ID, markPending, markRefused); err != nil {
			return err
		}
	}
	return nil
}

// SettleForecastMark writes a conclusion. The guard is the contract: resolved and refused_final are
// terminal, so a second pass cannot rewrite a settled mark, whatever raced it.
func (s *Store) SettleForecastMark(tx *sql.Tx, markID int64, res markResult, now time.Time) error {
	_, err := tx.Exec(s.bind(`UPDATE prediction_marks
		SET status = ?, refusal = ?, session_date = ?, bar_date = ?, halted = ?, corp_action = ?,
		    close_fen = ?, low_fen = ?, high_fen = ?, ret_bp = ?, bench_ret_bp = ?,
		    resolved_at = ?, source = ?
		WHERE id = ? AND status <> ? AND status <> ?`),
		res.Status, res.Refusal, res.Session, res.BarDate, boolInt(res.Halted), boolInt(res.CorpAction),
		nullableFen(res.CloseFen), nullableFen(res.LowFen), nullableFen(res.HighFen),
		nullableInt32(res.RetBp), nullableInt32(res.BenchRetBp),
		now.UTC().Format(time.RFC3339), sourceOr(res.Source),
		markID, markResolved, markRefusedFinal)
	return err
}

// sourceOr names the answering vendor, falling back to the only one this build writes.
func sourceOr(source string) string {
	if source == "" {
		return quoteSourceTencent
	}
	return source
}

// MarkRefusedFinal ends retries on a mark. It accepts a pending mark too: finality is the result of a
// failed attempt, and that attempt's deferral may have been written in this same plan.
func (s *Store) MarkRefusedFinal(tx *sql.Tx, markID int64, code string, now time.Time) error {
	// The final marker is the result of a failed attempt, so it records that attempt: a row that says
	// refused_final with attempts=3 would misreport why it stopped.
	_, err := tx.Exec(s.bind(`UPDATE prediction_marks
		SET status = ?, refusal = ?, attempts = attempts + 1, resolved_at = ?
		WHERE id = ? AND status IN (?, ?)`),
		markRefusedFinal, code, now.UTC().Format(time.RFC3339), markID, markPending, markRefused)
	return err
}

// errForecastBaselineRaced means another pass made the baseline terminal before this plan wrote it.
// The plan is rolled back; the next tick reads the stored baseline instead of settling on its own.
var errForecastBaselineRaced = errors.New("forecast: baseline was written by another pass")

// finishForecastBaselineTx fills the set's four evaluator-owned columns, once. It reports whether
// this call was the one that wrote them.
func (s *Store) finishForecastBaselineTx(tx *sql.Tx, setID int64, b forecastBaseline) (bool, error) {
	check := b.Check
	if check == "" {
		check = baseMismatch
		if barOn(b.Index, b.S0) != nil {
			check = baseMatch
		}
	}
	res, err := tx.Exec(s.bind(`UPDATE prediction_sets
		SET s0_date = ?, base_date = ?, s0_fen = ?, base_check = ?
		WHERE id = ? AND base_check IN (?, ?)`),
		b.S0, b.BaseDate, b.S0Fen, check, setID, baseCheckPending, baseRefused)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// finishForecastBaselineTerminal gives up on a baseline that cannot be resolved: the set's start is
// unknowable, so every non-terminal mark ends refused_final with no_base rather than retrying forever.
func (s *Store) finishForecastBaselineTerminalTx(tx *sql.Tx, setID int64, now time.Time) error {
	if _, err := tx.Exec(s.bind(`UPDATE prediction_sets SET base_check = ?
		WHERE id = ? AND base_check IN (?, ?)`),
		baseRefusedFinal, setID, baseCheckPending, baseRefused); err != nil {
		return err
	}
	_, err := tx.Exec(s.bind(`UPDATE prediction_marks
		SET status = ?, refusal = ?, resolved_at = ?
		WHERE set_id = ? AND status IN (?, ?)`),
		markRefusedFinal, "no_base", now.UTC().Format(time.RFC3339),
		setID, markPending, markRefused)
	return err
}

// predRetryBackoff is the wait before the next attempt, by attempt number: an hour, then six, then a
// day, then a day. A vendor outage is a retry rather than a permanent hole, and a permanent problem
// stops hammering.
func predRetryBackoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Hour
	case attempts == 2:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

// setForecastRepairPremature records that the one-shot heal has run, with its count. Its presence is
// what makes the heal idempotent across restarts: the SQL predicate alone is idempotent against new
// rows, but a second boot must not re-audit.
const setForecastRepairPremature = "forecast_state_repair_premature"

// healPrematureRefusals repairs the refusals written by the pre-P1 evaluator: every first tick before
// S0 refused every mark `no_bar` with no session and no real attempt. The predicate is that exact
// signature. After P1 every recorded attempt writes session_date, so it never matches a real attempt.
func (s *Store) healPrematureRefusals() (count int64, setIDs []int64, err error) {
	if s.GetSetting(setForecastRepairPremature, "") != "" {
		return 0, nil, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed

	rows, err := tx.Query(s.bind(`SELECT DISTINCT set_id FROM prediction_marks
		WHERE status = ? AND refusal = ? AND session_date = '' AND resolved_at = ''`),
		markRefused, "no_bar")
	if err != nil {
		return 0, nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		setIDs = append(setIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}

	res, err := tx.Exec(s.bind(`UPDATE prediction_marks
		SET status = ?, attempts = 0, refusal = '', next_attempt_at = ''
		WHERE status = ? AND refusal = ? AND session_date = '' AND resolved_at = ''`),
		markPending, markRefused, "no_bar")
	if err != nil {
		return 0, nil, err
	}
	count, _ = res.RowsAffected()
	if _, err := tx.Exec(s.bind(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`),
		setForecastRepairPremature, strconv.FormatInt(count, 10)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return count, setIDs, nil
}

// runForecastRepair performs the one-shot heal and records it. The audit uses the pool AFTER the
// heal's transaction commits — a pool call inside the transaction would deadlock on SQLite's single
// connection.
func (s *Server) runForecastRepair() {
	n, ids, err := s.st.healPrematureRefusals()
	if err != nil {
		log.Printf("forecast: premature-refusal repair: %v", err)
		return
	}
	if n == 0 {
		return
	}
	log.Printf("forecast: repaired %d premature refusals (%d sets)", n, len(ids))
	s.recordChange(nil, "system", AuditForecastRepair, "settings", setForecastRepairPremature,
		map[string]any{"count": n, "set_ids": ids})
}
