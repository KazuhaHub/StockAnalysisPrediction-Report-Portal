package app

import "net/url"

type forecastReadingEdition struct {
	ReportID int64  `json:"report_id"`
	SetID    int64  `json:"set_id"`
	Version  string `json:"version"`
	Cohort   string `json:"cohort"`
	URL      string `json:"url"`
}

// Editions share symbol/date/type, not title. Readability is applied before considering accepted
// predictions; a private edition cannot create even an empty fallback link. Keep older readable
// reports eligible so a later generator output cannot erase the edition that actually counted.
func (s *Server) forecastReadingEditionLinks(report Rep, view forecastPredictionView, membership bool, cohort string) (*forecastReadingEdition, *forecastReadingEdition, error) {
	membership = membership && !view.protected && !view.noMembership
	q := `SELECT r.id, r.version, COALESCE(r.kind,'') FROM reports r
		WHERE r.symbol=? AND r.rdate=? AND r.rtype=? AND r.version<>?
		AND EXISTS (SELECT 1 FROM prediction_sets ps WHERE ps.report_id=r.id AND ps.state=? AND ps.rtype=r.rtype)`
	args := []any{report.Symbol, report.Date, report.RType, report.Version, predStateAccepted}
	if fragment, scopeArgs := view.scope.where("r."); fragment != "" {
		q += " AND " + fragment
		args = append(args, scopeArgs...)
	}
	rows, err := s.st.query(q+" ORDER BY r.id DESC", args...)
	if err != nil {
		return nil, nil, err
	}
	var candidates []Rep
	for rows.Next() {
		candidate := Rep{Symbol: report.Symbol, Date: report.Date, RType: report.RType}
		if err := rows.Scan(&candidate.ID, &candidate.Version, &candidate.Kind); err != nil {
			rows.Close()
			return nil, nil, err
		}
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	type editionSets struct {
		report  Rep
		sets    []PredictionSet
		display int64
	}
	var loaded []editionSets
	var all []PredictionSet
	for _, candidate := range candidates {
		identities, err := s.st.reportPredictionIdentities(candidate.ID, view.scope)
		if err != nil {
			return nil, nil, err
		}
		accepted := identities[:0]
		for _, identity := range identities {
			if identity.RType == report.RType && identity.State == predStateAccepted {
				accepted = append(accepted, identity)
			}
		}
		display, _ := readPredictionIDs(accepted)
		if display == 0 {
			continue
		}
		loaded = append(loaded, editionSets{candidate, accepted, display})
		all = append(all, accepted...)
	}
	statuses := map[int64]string{}
	if membership {
		if err := s.st.annotateForecastReadMembership(all); err != nil {
			return nil, nil, err
		}
		for _, identity := range all {
			statuses[identity.ID] = identity.Membership
		}
	}
	var sibling, counted *forecastReadingEdition
	for _, edition := range loaded {
		for _, set := range edition.sets {
			if sibling == nil && set.ID == edition.display {
				sibling = readingEditionLink(edition.report, set)
			}
			if counted == nil && statuses[set.ID] == membershipMember && (cohort == "" || set.Cohort == cohort) {
				counted = readingEditionLink(edition.report, set)
			}
		}
	}
	return sibling, counted, nil
}

func readingEditionLink(report Rep, set PredictionSet) *forecastReadingEdition {
	query := url.Values{"date": {report.Date}, "kind": {repKind(report)}, "r": {itoa64(report.ID)}}
	return &forecastReadingEdition{ReportID: report.ID, SetID: set.ID, Version: report.Version, Cohort: set.Cohort,
		URL: "/stock/" + url.PathEscape(report.Symbol) + "?" + query.Encode() + "#forecast"}
}

// Card-stage session reads need the same edition links as full-stage summaries. The credential
// view opts in explicitly; persistent tokens and app-bridge reads keep their existing contract.
func (s *Server) addReadingEditionLinks(out map[string]any, reportID int64, identities []PredictionSet, display, counted int64, membership bool, view forecastPredictionView) {
	cohort, displayMembership := "", ""
	for _, identity := range identities {
		if identity.ID == display {
			cohort, displayMembership = identity.Cohort, identity.Membership
			break
		}
	}
	if display != 0 && (!membership || counted != 0 && displayMembership == membershipMember) {
		return
	}
	report, err := s.st.GetNew(reportID, view.scope)
	if err != nil || report == nil || !predictionCovered(report.RType) {
		return
	}
	sibling, member, err := s.forecastReadingEditionLinks(*report, view, membership, cohort)
	if err != nil {
		return
	}
	if display == 0 && sibling != nil {
		out["sibling"] = sibling
	}
	if member != nil {
		out["counted_edition"] = member
	}
}
