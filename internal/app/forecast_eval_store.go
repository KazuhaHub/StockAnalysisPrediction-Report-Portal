package app

// The evaluator's database side (ADR 0029 §6, plan §6.3).
//
// Every write here is GUARDED by the row's current state, and that is the load-bearing property of the
// whole feature: `resolved` and `refused_final` are terminal, so a mature cell's inputs cannot change
// after the fact. Without the guard, a second evaluator pass — from an overlapping tick, a restart, or
// a retry — could rewrite a conclusion, and a cell that promised to be immutable would quietly move.

import "time"

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
	Marks      []forecastMark
}

// forecastMark is one pending or retryable checkpoint.
type forecastMark struct {
	ID         int64
	Checkpoint string
	Status     string
	Attempts   int
}

// ForecastSetsNeedingWork returns the sets with work due, oldest first, bounded by limit.
//
// "Due" is derived from the rows every time rather than held in memory, so a restart resumes rather
// than forgets — and the selection is deliberately narrow: only accepted sets, and only those with a
// mark that is pending, overdue for a retry, or a baseline check that has not been settled.
func (s *Store) ForecastSetsNeedingWork(now time.Time, limit int) ([]forecastWorkSet, error) {
	if limit <= 0 {
		limit = predEvalBatch
	}
	nowStr := now.UTC().Format(time.RFC3339)
	rows, err := s.query(`SELECT ps.id, ps.symbol, ps.made_on, ps.made_at, ps.horizon_end, ps.horizon_std,
			ps.bench_code, ps.base_check
		FROM prediction_sets ps
		WHERE ps.state = ?
		  AND (ps.base_check IN ('pending','refused')
		    OR EXISTS (SELECT 1 FROM prediction_marks m WHERE m.set_id = ps.id
		         AND (m.status = ?
		           OR (m.status = ? AND (m.next_attempt_at = '' OR m.next_attempt_at <= ?)))))
		ORDER BY ps.made_on, ps.id LIMIT ?`,
		predStateAccepted, markPending, markRefused, nowStr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []forecastWorkSet
	var ids []int64
	for rows.Next() {
		var w forecastWorkSet
		var horizonStd int
		if err := rows.Scan(&w.SetID, &w.Symbol, &w.MadeOn, &w.MadeAt, &w.HorizonEnd, &horizonStd,
			&w.BenchCode, &w.BaseCheck); err != nil {
			return nil, err
		}
		w.HorizonStd = horizonStd == 1
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

	marks, err := s.forecastWorkMarks(ids, nowStr)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Marks = marks[out[i].SetID]
	}
	return out, nil
}

func (s *Store) forecastWorkMarks(setIDs []int64, nowStr string) (map[int64][]forecastMark, error) {
	rows, err := s.query(`SELECT set_id, id, checkpoint, status, attempts FROM prediction_marks
		WHERE set_id IN (`+placeholders(len(setIDs))+`)
		  AND (status = ? OR (status = ? AND (next_attempt_at = '' OR next_attempt_at <= ?)))
		ORDER BY set_id, `+predCheckpointOrderSQL(),
		append(int64Args(setIDs), markPending, markRefused, nowStr)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]forecastMark{}
	for rows.Next() {
		var setID int64
		var m forecastMark
		if err := rows.Scan(&setID, &m.ID, &m.Checkpoint, &m.Status, &m.Attempts); err != nil {
			return nil, err
		}
		out[setID] = append(out[setID], m)
	}
	return out, rows.Err()
}

// DeferForecastSet records a retryable failure for every non-terminal mark of a set.
//
// Used when a dependency shared by the whole set failed — the benchmark calendar, or the stock's own
// series. The mark is not resolved and not refused outright: the backoff is what turns "the vendor
// blipped" into one retry rather than a permanent hole.
func (s *Store) DeferForecastSet(set forecastWorkSet, reason string, cause error) error {
	next := time.Now().UTC().Add(predRetryBackoff(1)).Format(time.RFC3339)
	for _, m := range set.Marks {
		if m.Status == markResolved || m.Status == markRefusedFinal {
			continue
		}
		if _, err := s.exec(s.bind(`UPDATE prediction_marks
			SET status = ?, refusal = ?, attempts = attempts + 1, next_attempt_at = ?
			WHERE id = ? AND status <> ? AND status <> ?`),
			markRefused, reason, next, m.ID, markResolved, markRefusedFinal); err != nil {
			return err
		}
	}
	if cause != nil {
		return cause
	}
	return nil
}

// SettleForecastMark writes a conclusion.
//
// The guard is the entire contract: `WHERE status <> resolved AND status <> refused_final` means a
// second pass cannot rewrite a settled mark, whatever raced it. Zero rows affected is a normal
// outcome (someone else got there first), not an error.
func (s *Store) SettleForecastMark(markID int64, res markResult) error {
	_, err := s.exec(s.bind(`UPDATE prediction_marks
		SET status = ?, refusal = ?, session_date = ?, bar_date = ?, halted = ?, corp_action = ?,
		    close_fen = ?, low_fen = ?, high_fen = ?, ret_bp = ?, bench_ret_bp = ?,
		    resolved_at = ?, source = ?
		WHERE id = ? AND status <> ? AND status <> ?`),
		res.Status, res.Refusal, res.Session, res.BarDate, boolInt(res.Halted), boolInt(res.CorpAction),
		nullableFen(res.CloseFen), nullableFen(res.LowFen), nullableFen(res.HighFen),
		nullableInt32(res.RetBp), nullableInt32(res.BenchRetBp),
		time.Now().UTC().Format(time.RFC3339), quoteSourceTencent,
		markID, markResolved, markRefusedFinal)
	return err
}

// MarkRefusedFinal ends the retries on a mark that has been refused long enough.
func (s *Store) MarkRefusedFinal(markID int64) error {
	_, err := s.exec(s.bind(`UPDATE prediction_marks
		SET status = ?, resolved_at = ?
		WHERE id = ? AND status = ?`),
		markRefusedFinal, time.Now().UTC().Format(time.RFC3339), markID, markRefused)
	return err
}

// FinishForecastBaseline fills the set's own four evaluator-owned columns, once.
//
// Guarded by base_check, so the write happens exactly once and a later pass cannot move the starting
// point a set's whole return series was measured from.
func (s *Store) FinishForecastBaseline(set forecastWorkSet, s0, baseDate string, s0Fen int64, index []QuoteBar) error {
	check := baseMismatch
	if baseBar := barOn(index, s0); baseBar != nil {
		check = baseMatch
	}
	if _, err := s.exec(s.bind(`UPDATE prediction_sets
		SET s0_date = ?, base_date = ?, s0_fen = ?, base_check = ?
		WHERE id = ? AND base_check IN (?, ?)`),
		s0, baseDate, s0Fen, check, set.SetID, baseCheckPending, baseRefused); err != nil {
		return err
	}
	return nil
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
