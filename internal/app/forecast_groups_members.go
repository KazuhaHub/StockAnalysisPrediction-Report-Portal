package app

// The member list behind a matrix cell's drawer (plan §8.1).
//
// It is the same request as the matrix, plus a group label and a checkpoint, so the drawer cannot
// show a different population from the cell it was opened from: the membership rule is the same
// function the matrix uses.

import (
	"net/http"
	"time"
)

type forecastMembersResponse struct {
	OK         bool             `json:"ok"`
	Label      string           `json:"label"`
	Start      string           `json:"start"`
	End        string           `json:"end"`
	Checkpoint string           `json:"checkpoint"`
	State      string           `json:"state"`
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
	Agree       *bool    `json:"agree,omitempty"`
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
	members, pending, _ := forecastMembership(sets)
	today := now.In(chinaZone).Format("2006-01-02")
	cell := computeForecastCell(members, pending, rtype, period.Start, period.End, cp, today)
	if cp == "h12" {
		members = filterHorizonStd(members)
	}

	out := forecastMembersResponse{
		OK:         true,
		Label:      period.Label,
		Start:      period.Start,
		End:        period.End,
		Checkpoint: cp,
		State:      cell.State,
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
			CloseFen: m.CloseFen, RetBp: m.RetBp, BenchRetBp: m.BenchRetBp,
			S0Fen: gs.S0Fen, TerminalFen: gs.TerminalFen,
		}
		if m.RetBp != nil && m.BenchRetBp != nil {
			v := *m.RetBp - *m.BenchRetBp
			fm.ExcessBp = &v
		}
		if gs.S0Fen != nil && *gs.S0Fen > 0 && gs.TerminalFen != nil {
			u := float64(*gs.TerminalFen)/float64(*gs.S0Fen) - 1
			fm.Upside = &u
			if fm.ExcessBp != nil && u != 0 {
				agree := (*fm.ExcessBp > 0 && u > 0) || (*fm.ExcessBp < 0 && u < 0)
				fm.Agree = &agree
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
