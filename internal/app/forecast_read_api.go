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
)

// v1ReportPredictions serves every set ever written for one report, oldest first.
func (s *Server) v1ReportPredictions(w http.ResponseWriter, r *http.Request) {
	if !s.v1CanRead(w, r) {
		return
	}
	s.serveReportPredictions(w, r, s.queryScope(r))
}

// apiReportPredictions is the browser twin of the route above, under the session class.
func (s *Server) apiReportPredictions(w http.ResponseWriter, r *http.Request, user string) {
	s.serveReportPredictions(w, r, s.viewerScope(user))
}

// serveReportPredictions is the one implementation both routes share, so a shape or a scope rule
// cannot apply to one credential and not the other.
func (s *Server) serveReportPredictions(w http.ResponseWriter, r *http.Request, sc *ownerScope) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		jsonErrorCode(w, http.StatusBadRequest, "invalid_param", "id must be a positive integer")
		return
	}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{
		ReportID:       id,
		Limit:          200,
		IncludePayload: includePayload(r, sc),
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
	identities, identityErr := s.st.reportPredictionIdentities(id, sc)
	membershipReady := sc == nil && identityErr == nil && s.st.annotateForecastReadMembership(identities) == nil
	display, counted := readPredictionIDs(identities)
	selected, selectionErr := s.st.includeSelectedPredictionSets(id, sets, display, counted, includePayload(r, sc), sc)
	if selectionErr == nil {
		sets = selected
	}
	if membershipReady {
		statuses := make(map[int64]string, len(identities))
		for _, set := range identities {
			statuses[set.ID] = set.Membership
		}
		for i := range sets {
			sets[i].Membership = statuses[sets[i].ID]
		}
	}
	s.annotateReadPhases(sets, s.forecastNow())
	// Oldest first here, unlike the list route: a report is read in the order it was written, and its
	// revisions only make sense against the set they revised.
	reverseSets(sets)
	out := map[string]any{"ok": true, "sets": sets}
	if identityErr == nil && selectionErr == nil {
		out["display_set_id"] = display
	}
	if membershipReady && selectionErr == nil {
		out["counted_set_id"] = counted
	}
	writeJSON(w, out)
}

// v1QueryPredictions serves sets by symbol and date range, newest first.
func (s *Server) v1QueryPredictions(w http.ResponseWriter, r *http.Request) {
	if !s.v1CanRead(w, r) {
		return
	}
	s.serveQueryPredictions(w, r, s.queryScope(r))
}

// apiQueryPredictions is the browser twin, for the reading page's "other predictions of this
// stock" list. The SPA never calls /api/v1: that machine surface is classed authBearer, and the SPA
// route is separately scope- and enrolment-gated with the session it already carries.
func (s *Server) apiQueryPredictions(w http.ResponseWriter, r *http.Request, user string) {
	s.serveQueryPredictions(w, r, s.viewerScope(user))
}

func (s *Server) serveQueryPredictions(w http.ResponseWriter, r *http.Request, sc *ownerScope) {
	q := r.URL.Query()
	f := PredictionFilter{
		Symbol: strings.TrimSpace(q.Get("symbol")),
		RType:  strings.TrimSpace(firstNonEmpty(q.Get("rtype"), q.Get("subtype"))),
		From:   strings.TrimSpace(q.Get("from")),
		To:     strings.TrimSpace(q.Get("to")),
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
	f.IncludePayload = includePayload(r, sc)
	sets, err := s.st.QueryPredictionSets(f, sc)
	if err != nil {
		v1err(w, http.StatusInternalServerError, "db_error", "database error")
		return
	}
	s.annotateReadPhases(sets, s.forecastNow())
	if sc == nil {
		_ = s.st.annotateForecastReadMembership(sets)
	}
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
