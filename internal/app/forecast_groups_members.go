package app

// The member list behind a matrix cell's drawer (plan §8.1).
//
// It is the same request as the matrix, plus a group label and a checkpoint, so the drawer cannot
// show a different population from the cell it was opened from: the membership rule is the same
// function the matrix uses.

import (
	"net/http"
	"strings"
	"time"
)

type forecastMembersResponse struct {
	OK         bool   `json:"ok"`
	Label      string `json:"label"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Bench      string `json:"bench"`
	Checkpoint string `json:"checkpoint"`
	State      string `json:"state"`
	// The gate's own counts, so the drawer's headline matches the matrix without rescoring.
	Resolved   int              `json:"resolved"`
	Total      int              `json:"total"`
	Refused    int              `json:"refused"`
	CorpAction int              `json:"corp_action"`
	N          int              `json:"n"`
	Members    []forecastMember `json:"members"`
}

// forecastMember is one member of a cell with the numbers the drawer shows. ReportID is nil for a
// purged set, which is how the UI knows not to link it.
type forecastMember struct {
	SetID      int64  `json:"set_id"`
	ReportID   *int64 `json:"report_id"`
	Symbol     string `json:"symbol"`
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ReportDate string `json:"report_date"`
	MadeAt     string `json:"made_at"`
	MadeOn     string `json:"made_on"`
	Stance     string `json:"stance"`
	Status     string `json:"status"`

	CloseFen    *int64   `json:"close_fen,omitempty"`
	RetBp       *int32   `json:"ret_bp,omitempty"`
	BenchRetBp  *int32   `json:"bench_ret_bp,omitempty"`
	ExcessBp    *int32   `json:"excess_bp,omitempty"`
	S0Fen       *int64   `json:"s0_fen,omitempty"`
	TerminalFen *int64   `json:"terminal_fen,omitempty"`
	Upside      *float64 `json:"upside,omitempty"`
	// Direction is the ONE hit verdict (agree|disagree|flat|untested), from predHitClass. It is sent
	// only for a mature cell; a non-mature cell carries identity and status only.
	Direction string `json:"direction,omitempty"`
}

func (s *Server) apiForecastGroupMembers(w http.ResponseWriter, r *http.Request, user string) {
	q := r.URL.Query()
	mode := q.Get("mode")
	if mode == "" {
		mode = "month"
	}
	rtype := q.Get("rtype")
	if rtype == "" {
		rtype = predTestRtype
	}
	if !predictionCovered(rtype) {
		jsonErrorCode(w, http.StatusBadRequest, "bad_rtype", "rtype is not covered by the ledger")
		return
	}
	label := q.Get("label")
	bench := strings.TrimSpace(q.Get("bench"))
	// A custom group that spanned two benchmarks was split into labels like "2026-10 · sh000906".
	// Accept either the suffixed label or the base label plus bench=, so the drawer follows the
	// matrix row it was opened from.
	if i := strings.LastIndex(label, " · "); i > 0 {
		if bench == "" {
			bench = label[i+len(" · "):]
		}
		label = label[:i]
	}
	cp := q.Get("checkpoint")
	if !containsStr(predCheckpoints, cp) {
		jsonErrorCode(w, http.StatusBadRequest, "bad_checkpoint", "checkpoint is not one of d5, d20, d60, d120, h12")
		return
	}
	if label == "" {
		jsonErrorCode(w, http.StatusBadRequest, "bad_label", "label is required")
		return
	}

	now := time.Now()
	var periods []forecastPeriod
	switch mode {
	case "month":
		if q.Get("from") != "" || q.Get("to") != "" || q.Get("group") != "" {
			jsonErrorCode(w, http.StatusBadRequest, "month_takes_no_range", "month mode takes no from, to or group")
			return
		}
		earliest, err := s.st.forecastEarliestMadeOn(rtype)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		periods = monthlyPeriods(earliest, now)
	case "custom":
		from, to := q.Get("from"), q.Get("to")
		group := q.Get("group")
		if group == "" {
			group = "month"
		}
		if code, ok := validateForecastRange(from, to, group); !ok {
			jsonErrorCode(w, http.StatusBadRequest, code, forecastRangeMessage(code))
			return
		}
		periods = forecastPeriods(group, from, to)
	default:
		jsonErrorCode(w, http.StatusBadRequest, "bad_mode", "mode must be month or custom")
		return
	}

	var period *forecastPeriod
	for i := range periods {
		if periods[i].Label == label {
			period = &periods[i]
			break
		}
	}
	if period == nil {
		jsonErrorCode(w, http.StatusNotFound, "label_not_found", "no such group in this range")
		return
	}

	sets, err := s.st.loadForecastGroupSets(rtype, period.Start, period.End)
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	populationFrom, _ := s.st.ForecastPopulationFrom()
	// Use the matrix's ledger-wide answer, not the loaded slice: the slice can start after a pre-live
	// set or omit part of the cohort, and then the drawer would show a different population.
	benchOf := map[string]string{}
	for _, gs := range sets {
		if _, ok := benchOf[gs.Cohort]; ok {
			continue
		}
		b, _ := s.st.earliestCohortBench(rtype, gs.Cohort)
		benchOf[gs.Cohort] = b
	}
	if bench == "" {
		distinct := map[string]bool{}
		for _, gs := range sets {
			if benchOf[gs.Cohort] != "" {
				distinct[benchOf[gs.Cohort]] = true
			}
		}
		if len(distinct) == 1 {
			for b := range distinct {
				bench = b
			}
		} else if len(sets) > 0 {
			bench = benchOf[sets[0].Cohort]
		}
	}
	if bench != "" {
		filtered := make([]*groupSet, 0, len(sets))
		for _, gs := range sets {
			if benchOf[gs.Cohort] == bench {
				filtered = append(filtered, gs)
			}
		}
		sets = filtered
	}
	members, pending, _ := forecastMembership(sets, populationFrom, bench)
	today := now.In(chinaZone).Format("2006-01-02")
	// The gate sees the same population the matrix scores: h12 is a standard-horizon column.
	if cp == "h12" {
		members = filterHorizonStd(members)
		pending = filterHorizonStd(pending)
	}
	// The STATE comes from the gate, never the scorer: opening a drawer must not cost 10 000
	// permutations to learn what the matrix already showed.
	state, _, _, _, _, _ := forecastCellGate(members, pending, cp, period.End, today)
	// An official month's matured cell is frozen in the matrix memo; the drawer must show that same
	// state, even if a backdated set has since been loaded into the month.
	if mode == "month" {
		if c, _ := s.cachedForecastCell(forecastGroupCellKey(rtype, period.Start, period.End, bench, cp)); c != nil {
			state = c.State
		}
	}
	mature := state == "mature"

	out := forecastMembersResponse{
		OK:         true,
		Label:      period.Label,
		Start:      period.Start,
		End:        period.End,
		Bench:      bench,
		Checkpoint: cp,
		State:      state,
		Members:    []forecastMember{},
	}
	var ids []int64
	for _, gs := range members {
		m := gs.Marks[cp]
		// The drawer lists the members the cell scored: a refused or corporate-action mark is not
		// one of them, and listing it beside the numbers would mix populations.
		if m.Status != markResolved || m.CorpAction {
			continue
		}
		reportDate := gs.ReportDate
		if reportDate == "" {
			reportDate = gs.MadeOn
		}
		fm := forecastMember{
			SetID: gs.ID, ReportID: reportIDPtr(gs), Symbol: gs.Symbol,
			ReportDate: reportDate, MadeAt: gs.MadeAt, MadeOn: gs.MadeOn,
			Stance: gs.Stance, Status: m.Status,
		}
		if mature {
			// A non-mature cell has no outcomes to show: even a resolved mark is not a scored member
			// until the whole cell passes its gates, and sending the numbers invites a reader to
			// average a cell the matrix refuses to.
			fm.CloseFen, fm.RetBp, fm.BenchRetBp = m.CloseFen, m.RetBp, m.BenchRetBp
			fm.S0Fen, fm.TerminalFen = gs.S0Fen, gs.TerminalFen
			if m.RetBp != nil && m.BenchRetBp != nil {
				v := *m.RetBp - *m.BenchRetBp
				fm.ExcessBp = &v
			}
			if gs.S0Fen != nil && *gs.S0Fen > 0 && gs.TerminalFen != nil {
				u := float64(*gs.TerminalFen)/float64(*gs.S0Fen) - 1
				fm.Upside = &u
				// No benchmark excess means the verdict is unknowable, not `flat`: flat is a tested
				// row that ended level with its index.
				if fm.ExcessBp != nil {
					fm.Direction = predHitClass(*gs.TerminalFen, *gs.S0Fen, *fm.ExcessBp)
				} else {
					fm.Direction = dirUntested
				}
			}
		}
		if gs.ReportID != nil {
			ids = append(ids, *gs.ReportID)
		}
		out.Members = append(out.Members, fm)
	}
	if len(ids) > 0 {
		info, err := s.st.reportKindsFor(ids)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		for i := range out.Members {
			if out.Members[i].ReportID == nil {
				continue
			}
			if v, ok := info[*out.Members[i].ReportID]; ok {
				out.Members[i].Kind = v.Kind
				out.Members[i].Name = v.Name
			}
		}
	}
	writeJSON(w, out)
}

// reportIDPtr copies the set's report id, so a member row is independent of the loaded set.
func reportIDPtr(gs *groupSet) *int64 {
	if gs.ReportID == nil {
		return nil
	}
	v := *gs.ReportID
	return &v
}
