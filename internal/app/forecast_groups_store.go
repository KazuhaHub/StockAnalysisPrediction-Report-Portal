package app

// Loading what the group statistics need, in one pass per table.
//
// The read route's QueryPredictionSets caps its LIMIT at 200 and joins the report body, neither of
// which suits a computation over up to 36 months of sets. This loader is separate for that reason,
// and it selects only the columns the statistics read: no payload, no body, no errors.

import "database/sql"

// groupSet is one ledger row with the claims and marks the statistics read flattened out, so the
// metric code never has to search a claim slice per member.
type groupSet struct {
	ID         int64
	ReportID   *int64
	Symbol     string
	ReportDate string
	State      string

	MadeAt string
	MadeOn string

	DecisionQuality      string
	BaseCheck            string
	PolicyVersion        string
	ProbabilityMethod    string
	ProbabilityTransform string

	HorizonStd bool
	S0Fen      *int64

	Marks map[string]PredictionMark

	// Flattened claims.
	Stance      string
	TerminalFen *int64
	PProfitBp   *int32
	EntryFen    *int64
	BearFen     *int64
	BaseFen     *int64
	BullFen     *int64
}

// loadForecastGroupSets reads every set of one covered type in a made_on range, oldest first within
// each symbol so "the earliest accepted set" is a slice scan rather than a sort.
func (s *Store) loadForecastGroupSets(rtype, from, to string) ([]*groupSet, error) {
	rows, err := s.query(`SELECT ps.id, ps.report_id, ps.symbol, ps.report_date, ps.state,
			ps.made_at, ps.made_on, ps.decision_quality, ps.base_check, ps.horizon_std, ps.s0_fen,
			ps.policy_version, ps.probability_method, ps.probability_transform
		FROM prediction_sets ps
		WHERE ps.rtype = ? AND ps.made_on >= ? AND ps.made_on <= ?
		ORDER BY ps.symbol, ps.made_at, ps.id`, rtype, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*groupSet
	var ids []int64
	for rows.Next() {
		var (
			gs         groupSet
			reportID   sql.NullInt64
			horizonStd int
			s0         sql.NullInt64
		)
		if err := rows.Scan(&gs.ID, &reportID, &gs.Symbol, &gs.ReportDate, &gs.State, &gs.MadeAt, &gs.MadeOn,
			&gs.DecisionQuality, &gs.BaseCheck, &horizonStd, &s0,
			&gs.PolicyVersion, &gs.ProbabilityMethod, &gs.ProbabilityTransform); err != nil {
			return nil, err
		}
		if reportID.Valid {
			v := reportID.Int64
			gs.ReportID = &v
		}
		gs.HorizonStd = horizonStd == 1
		if s0.Valid {
			v := s0.Int64
			gs.S0Fen = &v
		}
		gs.Marks = map[string]PredictionMark{}
		out = append(out, &gs)
		ids = append(ids, gs.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}

	claims, err := s.predictionClaimsFor(ids)
	if err != nil {
		return nil, err
	}
	marks, err := s.predictionMarksFor(ids)
	if err != nil {
		return nil, err
	}
	for _, gs := range out {
		for _, c := range claims[gs.ID] {
			switch c.Kind {
			case kindStance:
				gs.Stance = c.Stance
			case kindTerminal:
				gs.TerminalFen = c.PriceFen
				gs.PProfitBp = c.ProbBp
			case kindEntry:
				gs.EntryFen = c.PriceFen
			case kindScenario:
				switch c.Label {
				case "bear":
					gs.BearFen = c.PriceFen
				case "base":
					gs.BaseFen = c.PriceFen
				case "bull":
					gs.BullFen = c.PriceFen
				}
			}
		}
		for _, m := range marks[gs.ID] {
			gs.Marks[m.Checkpoint] = m
		}
	}
	return out, nil
}

// countForecastUnrecorded counts live reports of a covered type that have no set row at all. It is
// the one number that catches store_failed: a covered ingest is supposed to leave a row in every
// outcome, so a report with none means the ledger never heard about it.
//
// It cannot be per group: reports.sent_at is caller-supplied and not uniformly UTC, so it cannot
// place a report in a civil month. The page shows it once.
func (s *Store) countForecastUnrecorded(rtype string) (int, error) {
	var n int
	err := s.queryRow(`SELECT COUNT(*) FROM reports r
		WHERE r.rtype = ?
		  AND NOT EXISTS (SELECT 1 FROM prediction_sets ps WHERE ps.report_id = r.id)`, rtype).Scan(&n)
	return n, err
}

// forecastEarliestMadeOn is where monthly mode starts: the first month the ledger has anything for.
// It returns "" when the ledger is empty.
func (s *Store) forecastEarliestMadeOn(rtype string) (string, error) {
	var v sql.NullString
	if err := s.queryRow(`SELECT MIN(made_on) FROM prediction_sets WHERE rtype = ?`, rtype).Scan(&v); err != nil {
		return "", err
	}
	return v.String, nil
}

// reportKindName is the pair the member drawer needs to build a stock link. kind is the report's
// TOP-LEVEL kind (e.g. 投资决策), not the subtype: apiStock matches it against repKind and silently
// falls back to the first kind on a miss, so passing the rtype would open the wrong tab.
type reportKindName struct {
	Kind string
	Name string
}

func (s *Store) reportKindsFor(ids []int64) (map[int64]reportKindName, error) {
	uniq := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			uniq = append(uniq, id)
		}
	}
	out := make(map[int64]reportKindName, len(uniq))
	if len(uniq) == 0 {
		return out, nil
	}
	rows, err := s.query(`SELECT id, COALESCE(kind,''), COALESCE(name,'') FROM reports WHERE id IN (`+
		placeholders(len(uniq))+`)`, int64Args(uniq)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var v reportKindName
		if err := rows.Scan(&id, &v.Kind, &v.Name); err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, rows.Err()
}
