package app

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"
)

// Identity selection scans only compact metadata. The report's body is loaded once, not repeated
// per revision, and the payload/claims/marks response remains bounded to 200 sets.
func (s *Store) reportPredictionIdentities(reportID int64, sc *ownerScope) ([]PredictionSet, error) {
	return s.reportPredictionIdentitiesWithBodyHash(reportID, sc, "")
}

// A reading response may already hold its selected body while an ingest replaces the database
// row. Keep that response's hash for display selection, but recheck current report visibility.
func (s *Store) reportPredictionIdentitiesWithBodyHash(reportID int64, sc *ownerScope, bodyHash string) ([]PredictionSet, error) {
	report, err := s.GetNew(reportID, sc)
	if err != nil {
		return nil, err
	}
	if report == nil {
		return nil, errors.New("report is no longer readable")
	}
	if bodyHash == "" {
		bodyHash = sha256Hex([]byte(report.MD + "\x00" + report.HTML))
	}
	rows, err := s.query(`SELECT id,state,rtype,made_at,cohort,body_sha256 FROM prediction_sets WHERE report_id=?`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PredictionSet
	for rows.Next() {
		var set PredictionSet
		var hash string
		if err := rows.Scan(&set.ID, &set.State, &set.RType, &set.MadeAt, &set.Cohort, &hash); err != nil {
			return nil, err
		}
		set.bodyMatches = hash != "" && hash == bodyHash
		set.BodyChanged = hash != "" && hash != bodyHash
		out = append(out, set)
	}
	return out, rows.Err()
}

func (s *Store) includeSelectedPredictionSets(reportID int64, sets []PredictionSet, display, counted int64, payload bool, sc *ownerScope) ([]PredictionSet, error) {
	selected := map[int64]bool{}
	for _, id := range []int64{display, counted} {
		if id > 0 {
			selected[id] = true
		}
	}
	seen := map[int64]bool{}
	for _, set := range sets {
		seen[set.ID] = true
	}
	for id := range selected {
		if seen[id] {
			continue
		}
		extra, err := s.QueryPredictionSets(PredictionFilter{ReportID: reportID, ID: id, IncludePayload: payload}, sc)
		if err != nil {
			return nil, err
		}
		if len(extra) != 1 {
			return nil, errors.New("selected prediction changed during load")
		}
		sets = append(sets, extra[0])
	}
	sort.Slice(sets, func(i, j int) bool {
		return sets[i].MadeAt > sets[j].MadeAt || sets[i].MadeAt == sets[j].MadeAt && sets[i].ID > sets[j].ID
	})
	out := make([]PredictionSet, 0, min(200, len(sets)))
	others := 0
	for _, set := range sets {
		if !selected[set.ID] {
			if others >= 200-len(selected) {
				continue
			}
			others++
		}
		out = append(out, set)
	}
	return out, nil
}

// annotateForecastReadMembership classifies complete monthly populations, not just the report's
// own revisions or a paginated symbol response. Metadata is published only after every load
// succeeds. Restricted callers must not invoke this global-population helper.
func (s *Store) annotateForecastReadMembership(sets []PredictionSet) error {
	var raw string
	err := s.queryRow(`SELECT v FROM meta WHERE k=?`, setForecastPopulationFrom).Scan(&raw)
	var from time.Time
	if err == nil {
		from, err = time.Parse(time.RFC3339, raw)
	} else if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return fmt.Errorf("read membership ledger start: %w", err)
	}
	loaded := map[[2]string]bool{}
	statuses := map[int64]string{}
	for _, set := range sets {
		key := [2]string{set.RType, set.Cohort}
		if loaded[key] {
			continue
		}
		month, err := time.ParseInLocation("2006-01", set.Cohort, chinaZone)
		if err != nil {
			return fmt.Errorf("read membership cohort: %w", err)
		}
		all, err := s.loadForecastGroupSets(set.RType, month.Format("2006-01-02"), month.AddDate(0, 1, -1).Format("2006-01-02"), forecastPopulation{Key: "all"})
		if err != nil {
			return err
		}
		var earliest *groupSet
		for _, candidate := range all {
			if candidate.State != predStateAccepted || !from.IsZero() && madeAtBefore(candidate.MadeAt, from) {
				continue
			}
			if earliest == nil || candidate.MadeAt < earliest.MadeAt || candidate.MadeAt == earliest.MadeAt && candidate.ID < earliest.ID {
				earliest = candidate
			}
		}
		bench := ""
		if earliest != nil {
			bench = earliest.BenchCode
		}
		for _, v := range classifyMembership(all, from, map[string]string{set.Cohort: bench}) {
			statuses[v.Set.ID] = v.Status
		}
		loaded[key] = true
	}
	for _, set := range sets {
		if _, ok := statuses[set.ID]; !ok {
			return errors.New("read membership population changed during load")
		}
	}
	for i := range sets {
		sets[i].Membership = statuses[sets[i].ID]
	}
	return nil
}

// readPredictionIDs separates the set matching the current body from the immutable decision
// counted in the monthly record. Its input order is irrelevant, including timestamp ties.
func readPredictionIDs(sets []PredictionSet) (display, counted int64) {
	var matched, latest, member *PredictionSet
	newer := func(a, b *PredictionSet) bool {
		return b == nil || a.MadeAt > b.MadeAt || a.MadeAt == b.MadeAt && a.ID > b.ID
	}
	for i := range sets {
		s := &sets[i]
		if s.State != predStateAccepted {
			continue
		}
		if newer(s, latest) {
			latest = s
		}
		if s.bodyMatches && newer(s, matched) {
			matched = s
		}
		if s.Membership == membershipMember && (member == nil || newer(member, s)) {
			member = s
		}
	}
	if matched != nil {
		display = matched.ID
	} else if latest != nil {
		display = latest.ID
	}
	if member != nil {
		counted = member.ID
	}
	return
}

// readUpsideBP truncates toward zero, unlike settlement returns, which round half away from zero.
// Use wide intermediates so even corrupt/extreme prices cannot wrap into a plausible signed value.
func readUpsideBP(terminal, base int64) *int64 {
	if terminal < 0 || base <= 0 {
		return nil
	}
	n := new(big.Int).Sub(big.NewInt(terminal), big.NewInt(base))
	n.Mul(n, big.NewInt(10000))
	n.Quo(n, big.NewInt(base))
	if !n.IsInt64() {
		return nil
	}
	v := n.Int64()
	return &v
}
