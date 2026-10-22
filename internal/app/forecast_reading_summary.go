package app

import "net/http"

type forecastReadingSummary struct {
	DisplaySetID   int64                      `json:"display_set_id"`
	Stance         string                     `json:"stance"`
	WeightBP       *int32                     `json:"weight_bp,omitempty"`
	UpsideBP       *int64                     `json:"upside_bp,omitempty"`
	BodyChanged    bool                       `json:"body_changed"`
	NextCheckpoint *forecastReadingCheckpoint `json:"next_checkpoint,omitempty"`
	Membership     string                     `json:"membership,omitempty"`
	CountedSetID   *int64                     `json:"counted_set_id,omitempty"`
}

type forecastReadingCheckpoint struct {
	Checkpoint string `json:"checkpoint"`
	Phase      string `json:"phase,omitempty"`
	NotBefore  string `json:"not_before,omitempty"`
	Expected   bool   `json:"expected"`
}

// Reading summaries use the same identity selection as the report card, not the newest page
// of ledger revisions. Their explicit allowlist never includes payloads or diagnostics.
func (s *Server) forecastReadingSummary(r *http.Request, user string, report *Rep) *forecastReadingSummary {
	if report == nil || !predictionCovered(report.RType) || r.Header.Get("X-Prefetch") == "1" {
		return nil
	}
	d := s.forecastAccess(user)
	if s.forecastReaderStage(d) != "full" {
		return nil
	}
	view := forecastPredictionViewFor(d, s.viewerScope(user))
	bodyHash := sha256Hex([]byte(report.MD + "\x00" + report.HTML))
	identities, err := s.st.reportPredictionIdentitiesWithBodyHash(report.ID, view.scope, bodyHash)
	if err != nil {
		return nil
	}
	matching := identities[:0]
	for _, identity := range identities {
		if identity.RType == report.RType {
			matching = append(matching, identity)
		}
	}
	identities = matching
	canSeeMembership := !view.protected && !view.noMembership
	if canSeeMembership && s.st.annotateForecastReadMembership(identities) != nil {
		return nil
	}
	display, counted := readPredictionIDs(identities)
	if display == 0 {
		return nil
	}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{ReportID: report.ID, ID: display, AcceptedOnly: true, Limit: 1}, view.scope)
	if err != nil || len(sets) != 1 {
		return nil
	}
	set := &sets[0]
	for _, identity := range identities {
		if identity.ID == display {
			set.BodyChanged = identity.BodyChanged
			break
		}
	}
	present := map[string]bool{}
	for _, mark := range set.Marks {
		present[mark.Checkpoint] = true
	}
	for _, checkpoint := range predCheckpoints {
		if !present[checkpoint] {
			set.Marks = append(set.Marks, PredictionMark{Checkpoint: checkpoint, Status: markPending})
		}
	}
	s.annotateReadPhases(sets, s.forecastNow())
	out := &forecastReadingSummary{DisplaySetID: set.ID, UpsideBP: set.UpsideBP, BodyChanged: set.BodyChanged}
	for _, claim := range set.Claims {
		if claim.Kind == kindStance {
			out.Stance, out.WeightBP = claim.Stance, claim.WeightBp
		}
	}
	for _, checkpoint := range predCheckpoints {
		for _, mark := range set.Marks {
			if mark.Checkpoint == checkpoint && mark.Phase != predPhaseResolved && mark.Phase != predPhaseFinal {
				out.NextCheckpoint = &forecastReadingCheckpoint{Checkpoint: checkpoint, Phase: mark.Phase, NotBefore: mark.NotBefore, Expected: mark.Expected}
				break
			}
		}
		if out.NextCheckpoint != nil {
			break
		}
	}
	if canSeeMembership {
		out.CountedSetID = &counted
		for _, identity := range identities {
			if identity.ID == display {
				out.Membership = identity.Membership
				break
			}
		}
	}
	return out
}

// forecastTimelineDates intersects ledger presence with the caller's already-scoped reports.
// A ledger failure is optional reading metadata, never a reason to hide the report itself.
func (s *Server) forecastTimelineDates(user, symbol string, readable []Rep) map[string]bool {
	if s.forecastReaderStage(s.forecastAccess(user)) != "full" {
		return nil
	}
	rows, err := s.st.query(`SELECT DISTINCT report_id, rtype FROM prediction_sets
		WHERE symbol=? AND state=? AND report_id IS NOT NULL`, symbol, predStateAccepted)
	if err != nil {
		return nil
	}
	defer rows.Close()
	accepted := map[int64]bool{}
	for rows.Next() {
		var report int64
		var rtype string
		if err := rows.Scan(&report, &rtype); err != nil {
			return nil
		}
		if predictionCovered(rtype) {
			accepted[report] = true
		}
	}
	if rows.Err() != nil {
		return nil
	}
	dates := map[string]bool{}
	for _, report := range readable {
		if predictionCovered(report.RType) && accepted[report.ID] {
			dates[report.Date] = true
		}
	}
	return dates
}
