package app

// The ledger's read routes (ADR 0029 §5, plan §4.1).
//
// Three routes over one store query:
//
//   GET /api/v1/reports/{id}/predictions   machine, per report
//   GET /api/v1/predictions                machine, by symbol and date range
//   GET /api/report/{id}/predictions       browser session, per report
//
// The browser route exists because the machine surface is not where a browser call belongs: /api/v1
// is classed authBearer, so it is outside sessionRoutes(). Its read handlers do admit a cookie
// session through canQuery, and the forced-enrolment wall is applied explicitly (v1CanRead) rather
// than by the session policy — but the SPA still uses this route and never /api/v1.

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type forecastPredictionView struct {
	scope        *ownerScope
	protected    bool
	reader       bool
	noMembership bool
}

func (s *Server) forecastCookiePredictionView(w http.ResponseWriter, user string) (forecastPredictionView, bool) {
	d := s.forecastAccess(user)
	if d.Scope == scopeNone {
		jsonErrorCode(w, http.StatusForbidden, "forecast_unavailable", "forecast access is unavailable")
		return forecastPredictionView{}, false
	}
	sc := s.viewerScope(user)
	return forecastPredictionViewFor(d, sc), true
}

func forecastPredictionViewFor(d forecastDecision, sc *ownerScope) forecastPredictionView {
	// A deletion or policy change between these reads must not turn an external decision into
	// nil scope (unrestricted). Match the external member drawer's fail-closed rule.
	if d.External && sc == nil {
		sc = &ownerScope{denyAll: true}
	}
	return forecastPredictionView{scope: sc, protected: d.External || sc != nil, reader: d.Reason != "admin", noMembership: d.Scope == scopeOwn}
}

// forecastV1PredictionView decides which credential a v1 prediction read is answered for. The order
// is fixed: a cookie session first (its own forecast decision and read scope), then a persistent
// query token (the documented machine contract, unscoped), and then nothing else. v1CanRead has
// already admitted the request, so a Bearer that is neither is an app-bridge token. The bridge
// registry records no user, so there is no account whose scope the token could stand for; it is
// refused without reading the ledger, until the shared token binding exists to scope it.
func (s *Server) forecastV1PredictionView(w http.ResponseWriter, r *http.Request) (forecastPredictionView, bool) {
	if !s.v1CanRead(w, r) {
		return forecastPredictionView{}, false
	}
	if user := s.currentActiveUser(r); user != "" {
		return s.forecastCookiePredictionView(w, user)
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if s.st.TokenValid(token, "query") {
		// Persistent machine reads retain their diagnostic contract.
		return forecastPredictionView{}, true
	}
	jsonErrorCode(w, http.StatusForbidden, "forecast_unavailable", "forecast access is unavailable")
	return forecastPredictionView{}, false
}

func protectedPredictionSets(sets []PredictionSet, view forecastPredictionView) []PredictionSet {
	if !view.protected && !view.reader {
		return sets
	}
	out := make([]PredictionSet, 0, len(sets))
	for _, set := range sets {
		if set.State != predStateAccepted || view.protected && set.ReportID == nil {
			continue
		}
		set.Errors = nil
		if view.protected {
			set.Payload = ""
		}
		if view.protected || view.noMembership {
			set.CountedInGroup = ""
		}
		out = append(out, set)
	}
	return out
}

// v1ReportPredictions serves every set ever written for one report, oldest first.
func (s *Server) v1ReportPredictions(w http.ResponseWriter, r *http.Request) {
	view, ok := s.forecastV1PredictionView(w, r)
	if !ok {
		return
	}
	s.serveReportPredictionsView(w, r, view)
}

// apiReportPredictions is the browser twin of the route above, under the session class.
func (s *Server) apiReportPredictions(w http.ResponseWriter, r *http.Request, user string) {
	if view, ok := s.forecastCookiePredictionView(w, user); ok {
		s.serveReportPredictionsView(w, r, view)
	}
}

// serveReportPredictions is the one implementation both routes share, so a shape or a scope rule
// cannot apply to one credential and not the other.
func (s *Server) serveReportPredictions(w http.ResponseWriter, r *http.Request, sc *ownerScope) {
	s.serveReportPredictionsView(w, r, forecastPredictionView{scope: sc, protected: sc != nil})
}

func (s *Server) serveReportPredictionsView(w http.ResponseWriter, r *http.Request, view forecastPredictionView) {
	sc := view.scope
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "id must be a positive integer")
		return
	}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{
		ReportID:       id,
		Limit:          200,
		IncludePayload: !view.protected && includePayload(r, sc),
		AcceptedOnly:   view.protected || view.reader,
		RequireReport:  view.protected,
	}, sc)
	if err != nil {
		jsonErrorCode(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	// A report the caller cannot read answers 404, not an empty list: an empty list would say the
	// report exists and made no predictions, which is a different and false claim. It matches what
	// reading the report itself does.
	if len(sets) == 0 {
		if !s.canReadReport(id, sc) {
			jsonErrorCode(w, http.StatusNotFound, "not_found", "no such report")
			return
		}
	}
	annotateReadPhases(sets, s.forecastNow())
	sets = protectedPredictionSets(sets, view)
	// Oldest first here, unlike the list route: a report is read in the order it was written, and its
	// revisions only make sense against the set they revised.
	reverseSets(sets)
	out := map[string]any{"ok": true, "sets": sets}
	if len(sets) > 0 && !view.protected && !view.noMembership {
		populationFrom, _ := s.st.ForecastPopulationFrom()
		// The cohort's benchmark, per set, so the "counted" pointer never lands on a set the statistics
		// excluded as off-benchmark (P4). A report's revisions can span cohorts, so each candidate is
		// judged against its own.
		benchOf := map[string]string{}
		benchFor := func(cohort string) string {
			if b, ok := benchOf[cohort]; ok {
				return b
			}
			b := ""
			if len(sets) > 0 {
				b, _ = s.st.earliestCohortBench(sets[0].RType, cohort)
			}
			benchOf[cohort] = b
			return b
		}
		out["counted_set_id"] = countedSetIDFor(sets, populationFrom, benchFor)
	}
	writeJSON(w, out)
}

// v1QueryPredictions serves sets by symbol and date range, newest first.
func (s *Server) v1QueryPredictions(w http.ResponseWriter, r *http.Request) {
	view, ok := s.forecastV1PredictionView(w, r)
	if !ok {
		return
	}
	s.serveQueryPredictionsView(w, r, view)
}

// apiQueryPredictions is the browser twin, for the reading page's "other predictions of this
// stock" list. The SPA never calls /api/v1: that machine surface is classed authBearer, and the SPA
// route is separately scope- and enrolment-gated with the session it already carries.
func (s *Server) apiQueryPredictions(w http.ResponseWriter, r *http.Request, user string) {
	if view, ok := s.forecastCookiePredictionView(w, user); ok {
		s.serveQueryPredictionsView(w, r, view)
	}
}

func (s *Server) serveQueryPredictions(w http.ResponseWriter, r *http.Request, sc *ownerScope) {
	s.serveQueryPredictionsView(w, r, forecastPredictionView{scope: sc, protected: sc != nil})
}

func (s *Server) serveQueryPredictionsView(w http.ResponseWriter, r *http.Request, view forecastPredictionView) {
	sc := view.scope
	q := r.URL.Query()
	f := PredictionFilter{
		Symbol:        strings.TrimSpace(q.Get("symbol")),
		RType:         strings.TrimSpace(firstNonEmpty(q.Get("rtype"), q.Get("subtype"))),
		From:          strings.TrimSpace(q.Get("from")),
		To:            strings.TrimSpace(q.Get("to")),
		AcceptedOnly:  view.protected || view.reader,
		RequireReport: view.protected,
	}
	if f.Symbol == "" {
		v1err(w, http.StatusBadRequest, "missing_param", "symbol is required")
		return
	}
	if f.From != "" && !validReportDate(f.From) {
		v1err(w, http.StatusBadRequest, "invalid_param", "from must be a valid YYYY-MM-DD")
		return
	}
	if f.To != "" && !validReportDate(f.To) {
		v1err(w, http.StatusBadRequest, "invalid_param", "to must be a valid YYYY-MM-DD")
		return
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.IncludePayload = !view.protected && includePayload(r, sc)
	sets, err := s.st.QueryPredictionSets(f, sc)
	if err != nil {
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	annotateReadPhases(sets, s.forecastNow())
	sets = protectedPredictionSets(sets, view)
	writeJSON(w, map[string]any{"ok": true, "sets": sets})
}

// canReadReport reports whether the caller may read this report at all. Used only to choose between
// "no such report" and "a report with no predictions" on the per-report route.
func (s *Server) canReadReport(id int64, sc *ownerScope) bool {
	where := []string{"id = ?"}
	args := []any{id}
	if frag, fargs := sc.where(""); frag != "" {
		where = append(where, frag)
		args = append(args, fargs...)
	}
	var n int
	if err := s.st.queryRow("SELECT COUNT(*) FROM reports WHERE "+strings.Join(where, " AND "), args...).Scan(&n); err != nil {
		return false // unreadable is unreadable; a query failure must not grant access
	}
	return n > 0
}

// countedSetIDFor picks the set the group statistics count for a report, or 0.
//
// Membership is "the earliest accepted set per (symbol, group) decides" (D4), and a group is a month
// of made_on. A report can hold more than one set — a re-ingest with a changed number is a revision —
// so the member is the earliest ACCEPTED one, and a report can also hold sets that are not members at
// all (absent, rejected, backtest, or accepted but superseded by an earlier set for the same symbol).
//
// A set made before the ledger start (P5) is never a member, so it is skipped here too: otherwise the
// reading page would point at a set the statistics do not count.
func countedSetIDFor(sets []PredictionSet, populationFrom time.Time, benchFor func(cohort string) string) int64 {
	var best PredictionSet
	found := false
	for _, s := range sets {
		if s.State != predStateAccepted {
			continue
		}
		if !populationFrom.IsZero() && madeAtBefore(s.MadeAt, populationFrom) {
			continue
		}
		if benchFor != nil {
			if bench := benchFor(s.Cohort); bench != "" && s.BenchCode != bench {
				continue
			}
		}
		if !found || s.MadeAt < best.MadeAt || (s.MadeAt == best.MadeAt && s.ID < best.ID) {
			best, found = s, true
		}
	}
	if !found {
		return 0
	}
	return best.ID
}

func reverseSets(sets []PredictionSet) {
	for i, j := 0, len(sets)-1; i < j; i, j = i+1, j-1 {
		sets[i], sets[j] = sets[j], sets[i]
	}
}

// includePayload answers whether the verbatim predictions JSON may be returned. It is a diagnostic
// for an unrestricted caller; a scoped reader gets every scored field without the producer's raw
// contract, which is not theirs to read (C5).
func includePayload(r *http.Request, sc *ownerScope) bool {
	return r.URL.Query().Get("include") == "payload" && sc == nil
}
