package app

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
