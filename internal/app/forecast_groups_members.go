package app

// The member list behind a matrix cell's drawer (plan §8.1).
//
// It is the same request as the matrix, plus a group label and a checkpoint, so the drawer cannot
// show a different population from the cell it was opened from: the membership rule is the same
// function the matrix uses.

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
)

type forecastMembersResponse struct {
	OK             bool                  `json:"ok"`
	Label          string                `json:"label"`
	Start          string                `json:"start"`
	End            string                `json:"end"`
	Bench          string                `json:"bench"`
	Checkpoint     string                `json:"checkpoint"`
	State          string                `json:"state"`
	Population     string                `json:"population,omitempty"`
	PurgedAt       string                `json:"purged_at,omitempty"`
	ExcludedMonths []forecastPurgedMonth `json:"excluded_months,omitempty"`
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
	SetID          int64  `json:"set_id"`
	ReportID       *int64 `json:"report_id"`
	Symbol         string `json:"symbol"`
	Name           string `json:"name,omitempty"`
	Kind           string `json:"kind,omitempty"`
	ReportDate     string `json:"report_date"`
	MadeAt         string `json:"made_at"`
	MadeOn         string `json:"made_on"`
	Stance         string `json:"stance"`
	Status         string `json:"status"`
	ExcludedReason string `json:"excluded_reason,omitempty"`

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
	s.serveForecastGroupMembers(w, r, user, forecastDecision{Scope: scopeAll, Reason: "admin"}, true)
}

func (s *Server) apiReaderForecastGroupMembers(w http.ResponseWriter, r *http.Request, user string) {
	d, ok := s.requireForecastAccess(w, r, user)
	if !ok {
		return
	}
	if !s.forecastCompute.allowMembers(user, s.settingInt(setForecastMembersPerMin, 60, 0, 600), s.forecastEval.clock()) {
		s.forecastSignal(sigRateLimited)
		forecastRateError(w)
		return
	}
	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.serveForecastGroupMembers(rec, r, user, d, false)
	if rec.status == http.StatusOK {
		s.forecastSignal(sigDrawerOpens)
	}
}

func (s *Server) serveForecastGroupMembers(w http.ResponseWriter, r *http.Request, user string, d forecastDecision, adminAlias bool) {
	pop := populationFor(d)
	q := r.URL.Query()
	population := q.Get("population")
	if population != "" && population != "scored" && population != "all" {
		jsonErrorCode(w, http.StatusBadRequest, "bad_population", "population must be scored or all")
		return
	}
	limit, cursor := 100, int64(0)
	if !adminAlias {
		if raw := q.Get("limit"); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil || v < 1 || v > 200 {
				jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "limit must be between 1 and 200")
				return
			}
			limit = v
		}
		if raw := q.Get("cursor"); raw != "" {
			b, err := base64.RawURLEncoding.DecodeString(raw)
			if err == nil {
				cursor, err = strconv.ParseInt(string(b), 10, 64)
			}
			if err != nil || cursor <= 0 {
				jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "invalid member cursor")
				return
			}
		}
	}
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

	now := s.forecastNow()
	purges, err := s.loadForecastPurgeView(pop)
	if err != nil {
		writeForecastPurgeViewError(w, err)
		return
	}
	var periods []forecastPeriod
	switch mode {
	case "month":
		if q.Get("from") != "" || q.Get("to") != "" || q.Get("group") != "" {
			jsonErrorCode(w, http.StatusBadRequest, "month_takes_no_range", "month mode takes no from, to or group")
			return
		}
		earliest, err := s.st.forecastEarliestMadeOnForPopulation(rtype, pop)
		if err != nil {
			jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
			return
		}
		periods = monthlyPeriods(forecastEarliestWithPurges(earliest, rtype, purges.disclosed), now)
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
	if key := [2]string{rtype, period.Start[:7]}; mode == "month" {
		if at, purged := purges.disclosed[key]; purged {
			out := forecastMembersResponse{OK: true, Label: period.Label, Start: period.Start, End: period.End,
				Checkpoint: cp, State: "purged", PurgedAt: at, Population: population, Members: []forecastMember{}}
			if adminAlias {
				writeJSON(w, out)
			} else {
				writeJSON(w, forecastReaderMembers(out, d, ""))
			}
			return
		}
	}

	sets, err := s.st.loadForecastGroupSets(rtype, period.Start, period.End, pop)
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	sets = forecastWithoutPurgedSets(sets, rtype, purges.filter)
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
	// The gate always receives the scored checkpoint population. Membership mode additionally
	// explains candidates blocked by the baseline or this checkpoint, never later duplicate runs.
	drawerMembers := members
	baselinePending := map[int64]bool{}
	if population == "all" {
		drawerMembers = nil
		for _, v := range classifyMembership(sets, populationFrom, benchOf) {
			if v.Status == membershipMember || v.Status == membershipPendingBaseline {
				drawerMembers = append(drawerMembers, v.Set)
				baselinePending[v.Set.ID] = v.Status == membershipPendingBaseline
			}
		}
	}
	today := now.In(chinaZone).Format("2006-01-02")
	// The gate sees the same population the matrix scores: h12 is a standard-horizon column.
	if cp == "h12" {
		members = filterHorizonStd(members)
		pending = filterHorizonStd(pending)
	}
	if population != "all" {
		drawerMembers = members
	}
	// The STATE comes from the gate, never the scorer: opening a drawer must not cost 10 000
	// permutations to learn what the matrix already showed.
	state, n, refused, corp, resolved, total := forecastCellGate(members, pending, cp, period.End, today)
	// An official month's matured cell is frozen in the matrix memo; the drawer must show that same
	// state, even if a backdated set has since been loaded into the month.
	if mode == "month" {
		// A month retired for the site but not disclosed to this population answers as an ordinary
		// month with no sets: the sets were filtered above, and the tag keeps a cell memoised before
		// the purge from describing it.
		key := pop.cellKey(rtype, period.Start, period.End, bench, cp) + purges.undisclosedTag(rtype, *period)
		if c, _ := s.cachedForecastCell(key); c != nil {
			state = c.State
			if c.State == "mature" {
				n, refused, corp = 0, 0, 0
				if c.N != nil {
					n = *c.N
				}
				if c.Refused != nil {
					refused = *c.Refused
				}
				if c.CorpAction != nil {
					corp = *c.CorpAction
				}
				// A mature cell has only resolved or finally refused members. Its stored n
				// excludes corporate actions; reconstruct the two gate counts from that record.
				resolved = n + corp
				total = resolved + refused
			}
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
	if mode == "custom" {
		out.ExcludedMonths = forecastExcludedMonths(rtype, period.Start, period.End, purges.disclosed)
	}
	if population == "all" {
		out.Population = population
	}
	if !adminAlias {
		out.N, out.Refused, out.CorpAction, out.Resolved, out.Total = n, refused, corp, resolved, total
	}
	sc := s.viewerScope(user)
	if d.External {
		// A decision that identified an external reader cannot become an unscoped read if a
		// later account/tree read changed or failed. Retrying the request resolves it afresh.
		if sc == nil {
			sc = &ownerScope{denyAll: true}
		}
	}
	var ids []int64
	for _, gs := range drawerMembers {
		if d.External && (gs.ReportID == nil || !s.canReadReport(*gs.ReportID, sc)) {
			continue
		}
		m := gs.Marks[cp]
		reason := forecastMemberExclusion(gs, cp, baselinePending[gs.ID])
		if population != "all" && reason != "" {
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
		if population == "all" {
			fm.ExcludedReason = reason
		}
		if mature && reason == "" {
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
	nextCursor := ""
	if !adminAlias {
		start := 0
		if cursor > 0 {
			found := false
			for i, member := range out.Members {
				if member.SetID == cursor {
					start, found = i+1, true
					break
				}
			}
			if !found {
				jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "invalid member cursor")
				return
			}
		}
		end := start + limit
		if end < len(out.Members) {
			nextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(out.Members[end-1].SetID, 10)))
		} else {
			end = len(out.Members)
		}
		out.Members = out.Members[start:end]
		ids = nil
		for _, member := range out.Members {
			if member.ReportID != nil {
				ids = append(ids, *member.ReportID)
			}
		}
	}
	if len(ids) > 0 {
		info, err := s.st.reportKindsFor(ids, sc)
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
			} else {
				out.Members[i].ReportID = nil
			}
		}
	}
	if adminAlias {
		writeJSON(w, out)
	} else {
		writeJSON(w, forecastReaderMembers(out, d, nextCursor))
	}
}

// Exclusions describe eligibility, not outcomes. They can be shown before the cell matures.
func forecastMemberExclusion(gs *groupSet, cp string, pendingBaseline bool) string {
	if pendingBaseline {
		return membershipPendingBaseline
	}
	if cp == "h12" && !gs.HorizonStd {
		return "non_standard_horizon"
	}
	m, ok := gs.Marks[cp]
	if !ok {
		return "missing_mark"
	}
	switch m.Status {
	case markResolved:
		if m.CorpAction {
			return "corp_action"
		}
		return ""
	case markPending, markRefused, markRefusedFinal:
		return m.Status
	default:
		return "unknown_mark_status"
	}
}

// reportIDPtr copies the set's report id, so a member row is independent of the loaded set.
func reportIDPtr(gs *groupSet) *int64 {
	if gs.ReportID == nil {
		return nil
	}
	v := *gs.ReportID
	return &v
}
