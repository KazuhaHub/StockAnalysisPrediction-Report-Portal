package app

// Reading the ledger (ADR 0029 §5, plan §4).
//
// Two access paths, one store query, and the difference between them is the scope they pass in:
//
//   - the machine routes take queryScope(r) — nil for a Bearer caller;
//   - the browser route takes viewerScope(user) — nil for anyone unrestricted.
//
// A set whose report still exists is scoped by JOINING to that report, the way tracking is. A set
// whose report is GONE cannot be scoped that way at all, so it is visible only to an unrestricted
// caller: a restricted reader cannot prove access to a row that no longer has an owner, and guessing
// in their favour is the one direction this must never fail in.
//
// owner_group is not consulted anywhere here, deliberately. ADR 0024 retired it as a read key, and
// the comment on ownerScope says why in as many words: "the security-critical filter must not have
// two spellings".

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// PredictionSet is one set with its claims and marks, as the read API returns it.
type PredictionSet struct {
	ID        int64  `json:"id"`
	ReportID  *int64 `json:"report_id"`
	State     string `json:"state"`
	Symbol    string `json:"symbol"`
	RType     string `json:"rtype"`
	Title     string `json:"title"`
	Version   string `json:"version"`
	MadeAt    string `json:"made_at"`
	MadeOn    string `json:"made_on"`
	Cohort    string `json:"cohort"`
	Zone      string `json:"zone"`
	BenchCode string `json:"bench_code"`

	DecisionStatus    string `json:"decision_status,omitempty"`
	DecisionQuality   string `json:"decision_quality,omitempty"`
	ProbabilityMethod string `json:"probability_method,omitempty"`
	PolicyVersion     string `json:"policy_version,omitempty"`
	HorizonEnd        string `json:"horizon_end,omitempty"`
	HorizonStd        bool   `json:"horizon_std"`

	BaselineEmittedFen *int64 `json:"baseline_emitted_fen,omitempty"`
	BaselineAsOf       string `json:"baseline_as_of,omitempty"`
	BaselineSource     string `json:"baseline_source,omitempty"`
	BaselineCheck      string `json:"baseline_check"`
	BaselineBar        string `json:"baseline_bar,omitempty"`
	BaselineFen        *int64 `json:"baseline_fen,omitempty"`

	S0Date string `json:"s0_date,omitempty"`
	S0Fen  *int64 `json:"s0_fen,omitempty"`

	// BodyChanged is true when the report's current body differs from the one this set was written
	// with. UpsertReport overwrites the body in place on re-ingest and writes no revision for a
	// workflow report, so without this the panel would show a set's numbers beside a body that has
	// since printed different ones.
	BodyChanged bool `json:"body_changed"`

	Errors []predError       `json:"errors,omitempty"`
	Claims []PredictionClaim `json:"claims"`
	Marks  []PredictionMark  `json:"marks"`
	// Payload is the verbatim predictions JSON. Only returned on the per-report route with
	// include=payload: it is 2 KiB of contract on every row of a list otherwise.
	Payload string `json:"payload,omitempty"`
	// CountedInGroup is the cohort whose member this set is, or "" when it is not one. Filled by the
	// group statistics, not by this query — the field exists here so one shape serves both callers.
	CountedInGroup string `json:"counted_in_group,omitempty"`
}

// PredictionClaim is one claim of a set.
type PredictionClaim struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Stance   string `json:"stance,omitempty"`
	Label    string `json:"label,omitempty"`
	PriceFen *int64 `json:"price_fen,omitempty"`
	ProbBp   *int32 `json:"prob_bp,omitempty"`
	WeightBp *int32 `json:"weight_bp,omitempty"`
}

// PredictionMark is one checkpoint of a set.
type PredictionMark struct {
	Checkpoint  string `json:"checkpoint"`
	Status      string `json:"status"`
	Refusal     string `json:"refusal,omitempty"`
	SessionDate string `json:"session_date,omitempty"`
	BarDate     string `json:"bar_date,omitempty"`
	Halted      bool   `json:"halted"`
	CorpAction  bool   `json:"corp_action"`
	CloseFen    *int64 `json:"close_fen,omitempty"`
	LowFen      *int64 `json:"low_fen,omitempty"`
	HighFen     *int64 `json:"high_fen,omitempty"`
	RetBp       *int32 `json:"ret_bp,omitempty"`
	BenchRetBp  *int32 `json:"bench_ret_bp,omitempty"`
	Attempts    int    `json:"attempts"`
}

// QueryPredictionSets returns sets matching the filters, newest first, with their claims and marks.
//
// scopes: pass the CALLER's scope. A nil scope means unrestricted, matching ownerScope.where's
// convention everywhere else in this package.
func (s *Store) QueryPredictionSets(f PredictionFilter, sc *ownerScope) ([]PredictionSet, error) {
	var where []string
	var args []any
	// The report join is needed for two reasons at once — to filter by the report it belongs to, and
	// to scope a restricted reader — so it is built once and used by both.
	join := ""
	if f.ReportID > 0 {
		join = " JOIN reports r ON r.id = ps.report_id"
		where = append(where, "ps.report_id = ?")
		args = append(args, f.ReportID)
	}
	if frag, fargs := sc.where("r."); frag != "" {
		if join == "" {
			// A scope on its own still needs the join: an orphaned set (report_id NULL) cannot satisfy
			// it, which is what makes a purged set invisible to a restricted reader.
			join = " JOIN reports r ON r.id = ps.report_id"
		}
		where = append(where, frag)
		args = append(args, fargs...)
	}
	if f.Symbol != "" {
		where = append(where, "ps.symbol = ?")
		args = append(args, f.Symbol)
	}
	if f.RType != "" {
		where = append(where, "ps.rtype = ?")
		args = append(args, f.RType)
	}
	if f.From != "" {
		where = append(where, "ps.made_on >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "ps.made_on <= ?")
		args = append(args, f.To)
	}
	if len(where) == 0 {
		// Unfiltered would be a full scan of the ledger, and the same rule as v1QueryReports applies:
		// a query names something, or it is refused. The caller enforces this too; this is the store's
		// own guard against a future caller that forgets.
		return nil, fmt.Errorf("no filter")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	// The report join is optional, so the body columns are spelled to match. Without the join there is
	// no body to compare against, and body_changed stays false — which is the honest answer for a set
	// whose report is gone rather than a wrong one.
	bodyCols := "'' AS body_md, '' AS body_html"
	if join != "" {
		bodyCols = "COALESCE(r.body_md,'') AS body_md, COALESCE(r.body_html,'') AS body_html"
	}
	q := fmt.Sprintf(`SELECT ps.id, ps.report_id, ps.state, ps.symbol, ps.rtype, ps.title, ps.version,
			ps.made_at, ps.made_on, ps.cohort, ps.zone, ps.bench_code,
			ps.decision_status, ps.decision_quality, ps.probability_method, ps.policy_version,
			ps.horizon_end, ps.horizon_std,
			ps.emitted_base_fen, ps.emitted_base_as_of, ps.emitted_base_source,
			ps.base_check, ps.s0_date, ps.s0_fen,
			COALESCE(ps.errors,''), ps.payload, ps.body_sha256,
			`+bodyCols+`
		FROM prediction_sets ps%s
		WHERE %s ORDER BY ps.made_at DESC, ps.id DESC LIMIT %d`,
		join, strings.Join(where, " AND "), limit)

	rows, err := s.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PredictionSet
	var ids []int64
	for rows.Next() {
		var (
			ps         PredictionSet
			reportID   sql.NullInt64
			emittedFen sql.NullInt64
			s0Fen      sql.NullInt64
			horizonStd int
			errorsJSON string
			payload    string
			bodySHA    string
			bodyMD     string
			bodyHTML   string
			title, ver sql.NullString
		)
		if err := rows.Scan(&ps.ID, &reportID, &ps.State, &ps.Symbol, &ps.RType, &title, &ver,
			&ps.MadeAt, &ps.MadeOn, &ps.Cohort, &ps.Zone, &ps.BenchCode,
			&ps.DecisionStatus, &ps.DecisionQuality, &ps.ProbabilityMethod, &ps.PolicyVersion,
			&ps.HorizonEnd, &horizonStd,
			&emittedFen, &ps.BaselineAsOf, &ps.BaselineSource,
			&ps.BaselineCheck, &ps.S0Date, &s0Fen,
			&errorsJSON, &payload, &bodySHA, &bodyMD, &bodyHTML); err != nil {
			return nil, err
		}
		if reportID.Valid {
			v := reportID.Int64
			ps.ReportID = &v
		}
		ps.Title, ps.Version = title.String, ver.String
		ps.HorizonStd = horizonStd == 1
		if emittedFen.Valid {
			ps.BaselineEmittedFen = &emittedFen.Int64
		}
		if s0Fen.Valid {
			ps.S0Fen = &s0Fen.Int64
		}
		ps.Errors = unmarshalPredErrors(errorsJSON)
		// body_changed compares the hash of what arrived with the hash of what is there now. The
		// report's own hashing is the same function the ingest path used, so the two are comparable.
		ps.BodyChanged = bodySHA != "" && bodySHA != sha256Hex([]byte(bodyMD+"\x00"+bodyHTML))
		if f.IncludePayload {
			ps.Payload = payload
		}
		ps.Claims, ps.Marks = []PredictionClaim{}, []PredictionMark{}
		out = append(out, ps)
		ids = append(ids, ps.ID)
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
	for i := range out {
		if c, ok := claims[out[i].ID]; ok {
			out[i].Claims = c
		}
		if m, ok := marks[out[i].ID]; ok {
			out[i].Marks = m
		}
	}
	return out, nil
}

// PredictionFilter is the query shape both read paths share.
type PredictionFilter struct {
	ReportID       int64
	Symbol         string
	RType          string
	From, To       string // made_on range, inclusive, YYYY-MM-DD
	Limit          int
	IncludePayload bool
}

// predictionClaimsFor loads claims for a batch of set ids. One round trip rather than one per set:
// a list of fifty sets is otherwise a hundred and one queries.
func (s *Store) predictionClaimsFor(setIDs []int64) (map[int64][]PredictionClaim, error) {
	ph := placeholders(len(setIDs))
	rows, err := s.query(`SELECT set_id, claim_key, kind, stance, label, price_fen, prob_bp, weight_bp
		FROM prediction_claims WHERE set_id IN (`+ph+`) ORDER BY claim_key`,
		int64Args(setIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]PredictionClaim{}
	for rows.Next() {
		var setID int64
		var c PredictionClaim
		var stance, label sql.NullString
		var price, prob, weight sql.NullInt64
		if err := rows.Scan(&setID, &c.Key, &c.Kind, &stance, &label, &price, &prob, &weight); err != nil {
			return nil, err
		}
		c.Stance, c.Label = stance.String, label.String
		if price.Valid {
			c.PriceFen = &price.Int64
		}
		if prob.Valid {
			v := int32(prob.Int64)
			c.ProbBp = &v
		}
		if weight.Valid {
			v := int32(weight.Int64)
			c.WeightBp = &v
		}
		out[setID] = append(out[setID], c)
	}
	return out, rows.Err()
}

// predictionMarksFor loads marks for a batch of set ids, in checkpoint order.
func (s *Store) predictionMarksFor(setIDs []int64) (map[int64][]PredictionMark, error) {
	ph := placeholders(len(setIDs))
	rows, err := s.query(`SELECT set_id, checkpoint, status, refusal, session_date, bar_date,
			halted, corp_action, close_fen, low_fen, high_fen, ret_bp, bench_ret_bp, attempts
		FROM prediction_marks WHERE set_id IN (`+ph+`)
		ORDER BY set_id, `+predCheckpointOrderSQL(),
		int64Args(setIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]PredictionMark{}
	for rows.Next() {
		var setID int64
		var m PredictionMark
		var halted, corp int
		var closeF, lowF, highF, ret, bench sql.NullInt64
		if err := rows.Scan(&setID, &m.Checkpoint, &m.Status, &m.Refusal, &m.SessionDate, &m.BarDate,
			&halted, &corp, &closeF, &lowF, &highF, &ret, &bench, &m.Attempts); err != nil {
			return nil, err
		}
		m.Halted, m.CorpAction = halted == 1, corp == 1
		if closeF.Valid {
			m.CloseFen = &closeF.Int64
		}
		if lowF.Valid {
			m.LowFen = &lowF.Int64
		}
		if highF.Valid {
			m.HighFen = &highF.Int64
		}
		if ret.Valid {
			v := int32(ret.Int64)
			m.RetBp = &v
		}
		if bench.Valid {
			v := int32(bench.Int64)
			m.BenchRetBp = &v
		}
		out[setID] = append(out[setID], m)
	}
	return out, rows.Err()
}

// predCheckpointOrderSQL orders marks by the vocabulary's checkpoint order rather than alphabetically,
// so d5 comes before d120 and h12 is last. Stored as a CASE rather than relied on from insertion
// order: a row's id is not an API guarantee, and the column's meaning is the order.
func predCheckpointOrderSQL() string {
	var b strings.Builder
	b.WriteString("CASE checkpoint")
	for i, cp := range predCheckpoints {
		fmt.Fprintf(&b, " WHEN '%s' THEN %d", cp, i)
	}
	b.WriteString(" ELSE 99 END")
	return b.String()
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(v []int64) []any {
	out := make([]any, len(v))
	for i, x := range v {
		out[i] = x
	}
	return out
}

// unmarshalPredErrors reads the stored error list, and never fails: a set whose errors cannot be
// parsed is still a set, and a read path that returned an error for it would hide the row entirely.
func unmarshalPredErrors(raw string) []predError {
	if raw == "" {
		return nil
	}
	var out []predError
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []predError{{Reason: "unreadable", Detail: raw}}
	}
	return out
}
