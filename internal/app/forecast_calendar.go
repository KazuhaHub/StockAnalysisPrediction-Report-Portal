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

// predSessionMemo holds the session lists one read derives from each start date. Both lists depend
// only on that date and the static closure table, and a read asks for the same few dates once per set
// and again for every mark: memoising them for the call turns thousands of date formats per request
// into a few hundred. It lives for a single call, so a test that edits the closure table between calls
// still sees fresh lists. Callers only read the slices it returns.
type predSessionMemo struct {
	weekdayLists map[string][]string
	openLists    map[string][]string
}

func newPredSessionMemo() *predSessionMemo {
	return &predSessionMemo{weekdayLists: map[string][]string{}, openLists: map[string][]string{}}
}

// weekdays is predProjectSessions(from, 400).
func (m *predSessionMemo) weekdays(from string) []string {
	if s, ok := m.weekdayLists[from]; ok {
		return s
	}
	s := predProjectSessions(from, 400)
	m.weekdayLists[from] = s
	return s
}

// openSessions is the next 400 dates from `from` on which the exchange is open.
func (m *predSessionMemo) openSessions(from string) []string {
	if s, ok := m.openLists[from]; ok {
		return s
	}
	day, _ := time.ParseInLocation("2006-01-02", from, chinaZone)
	s := make([]string, 0, 400)
	for len(s) < 400 {
		date := day.Format("2006-01-02")
		if !predExchangeClosed(date) {
			s = append(s, date)
		}
		day = day.AddDate(0, 0, 1)
	}
	m.openLists[from] = s
	return s
}

// predReadProjection returns an expected date only when the entire relevant interval is covered.
// Missing years or a contradictory frozen baseline fall back to an unqualified weekday lower bound.
// Expected does not mean observed: it must never shorten a retry/finality deadline or settle a mark.
func predReadProjection(cp string, madeAt time.Time, horizonStd bool, frozenS0 string) (predSchedule, bool) {
	return predReadProjectionWith(newPredSessionMemo(), cp, madeAt, horizonStd, frozenS0)
}

// predReadProjectionWith is predReadProjection over a memo shared by the rest of the read.
func predReadProjectionWith(memo *predSessionMemo, cp string, madeAt time.Time, horizonStd bool, frozenS0 string) (predSchedule, bool) {
	madeOn := madeAt.In(chinaZone).Format("2006-01-02")
	from := shiftDays(madeOn, -1)
	fallback := func() (predSchedule, bool) {
		return predScheduleFor(cp, madeAt, horizonStd, frozenS0, memo.weekdays(from)), false
	}
	if _, ok := predExchangeClosures[madeAt.In(chinaZone).Year()]; !ok {
		return fallback()
	}
	if frozenS0 != "" && (!validReportDate(frozenS0) || predExchangeClosed(frozenS0)) {
		return fallback()
	}
	sessions := memo.openSessions(from)
	// Preserving an open frozen date is not enough: it must also reproduce the receipt rule.
	// A contradictory stored baseline stays immutable, but cannot qualify a reading estimate.
	if frozenS0 != "" {
		derived, ok := predS0(madeAt, sessions)
		if !ok || derived != frozenS0 {
			return fallback()
		}
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
