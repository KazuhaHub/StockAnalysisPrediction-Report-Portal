package app

import "time"

// Projection evidence only. Settlement and evaluator wake scheduling must not use this table.
// SSE annual notice (2025-12-22):
// https://www.sse.com.cn/disclosure/announcement/general/c/c_20251222_10802507.shtml
// Unknown years deliberately remain absent until their exchange notices are verified.
var predExchangeClosures = map[int][][2]string{
	2026: {
		{"2026-01-01", "2026-01-03"},
		{"2026-02-15", "2026-02-23"},
		{"2026-04-04", "2026-04-06"},
		{"2026-05-01", "2026-05-05"},
		{"2026-06-19", "2026-06-21"},
		{"2026-09-25", "2026-09-27"},
		{"2026-10-01", "2026-10-07"},
	},
}

func predExchangeClosed(date string) bool {
	day, err := time.ParseInLocation("2006-01-02", date, chinaZone)
	if err != nil || day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		return true
	}
	for _, span := range predExchangeClosures[day.Year()] {
		if date >= span[0] && date <= span[1] {
			return true
		}
	}
	return false
}

// predReadProjection returns an expected date only when the entire relevant interval is covered.
// Missing years or a contradictory frozen baseline fall back to an unqualified weekday lower bound.
// Expected does not mean observed: it must never shorten a retry/finality deadline or settle a mark.
func predReadProjection(cp string, madeAt time.Time, horizonStd bool, frozenS0 string) (predSchedule, bool) {
	madeOn := madeAt.In(chinaZone).Format("2006-01-02")
	from := shiftDays(madeOn, -1)
	fallback := func() (predSchedule, bool) {
		return predScheduleFor(cp, madeAt, horizonStd, frozenS0, predProjectSessions(from, 400)), false
	}
	if _, ok := predExchangeClosures[madeAt.In(chinaZone).Year()]; !ok {
		return fallback()
	}
	if frozenS0 != "" && (!validReportDate(frozenS0) || predExchangeClosed(frozenS0)) {
		return fallback()
	}
	day, _ := time.ParseInLocation("2006-01-02", from, chinaZone)
	sessions := make([]string, 0, 400)
	for len(sessions) < 400 {
		date := day.Format("2006-01-02")
		if !predExchangeClosed(date) {
			sessions = append(sessions, date)
		}
		day = day.AddDate(0, 0, 1)
	}
	sch := predScheduleFor(cp, madeAt, horizonStd, frozenS0, sessions)
	if !sch.Known || sch.Due == "" || frozenS0 != "" && sch.S0 != frozenS0 {
		return fallback()
	}
	start := madeAt.In(chinaZone).Year()
	if frozenS0 != "" && frozenS0 < madeOn {
		frozen, _ := time.Parse("2006-01-02", frozenS0)
		start = frozen.Year()
	}
	due, _ := time.Parse("2006-01-02", sch.Due)
	for year := start; year <= due.Year(); year++ {
		if _, ok := predExchangeClosures[year]; !ok {
			return fallback()
		}
	}
	return sch, true
}
