package app

// Checkpoint scheduling (P1, ADR 0029 §4).
//
// This file is the ONE statement of the checkpoint arithmetic. The evaluator calls it with the
// benchmark's COMPLETED sessions; the read path prefers a published completed-calendar snapshot,
// otherwise a verified exchange-schedule estimate or a weekday lower bound. Estimates are never
// observed settlement evidence. Nothing else computes a
// checkpoint date, so "when is this mark due" cannot drift between the settlement and the display.
//
// S0 follows the 15:00 rule (predS0); dN is the Nth session AFTER S0.

import (
	"time"
)

// predSchedule is one checkpoint's dates. Known is false while the calendar cannot yet decide.
type predSchedule struct {
	S0     string // the start session
	Record string // the session whose close is recorded
	Due    string // the session after whose completion the mark may settle
	Known  bool
}

// predS0 is the first session whose close is after made_at. `sessions` are completed, ascending.
//
// Received before 15:00:00 on session T (pre-open and the lunch break included) → T. Received at or
// after 15:00:00, or on a day that is not a session → the next session. A date is known NOT to be a
// session only once a LATER session is on the calendar (vendors publish in order), so "today has no
// bar yet" is never read as "today is a holiday".
func predS0(madeAt time.Time, sessions []string) (string, bool) {
	t := madeAt.In(chinaZone)
	d := t.Format("2006-01-02")
	close := time.Date(t.Year(), t.Month(), t.Day(), predSessionCloseMinute/60, predSessionCloseMinute%60, 0, 0, chinaZone)
	beforeClose := t.Before(close)
	for _, s := range sessions {
		switch {
		case s < d:
			continue
		case s == d:
			if beforeClose {
				return d, true
			}
			continue // received after the close: the next session
		default:
			return s, true // the first session after d
		}
	}
	return "", false
}

// predScheduleFor maps a checkpoint to its sessions. `sessions` are COMPLETED sessions, ascending;
// `frozenS0` is the start session already stored on the set, if any. S0 comes from the stored value
// when present; otherwise it follows the 15:00 rule above.
func predScheduleFor(cp string, madeAt time.Time, horizonStd bool, frozenS0 string, sessions []string) predSchedule {
	madeOn := madeAt.In(chinaZone).Format("2006-01-02")
	s0 := frozenS0
	if s0 == "" {
		var ok bool
		s0, ok = predS0(madeAt, sessions)
		if !ok {
			return predSchedule{}
		}
	}
	i0 := indexOfDate(sessions, s0)
	if i0 < 0 && frozenS0 != "" {
		// The stored start session is not on this calendar (a vendor gap). Fall back to the
		// receipt-based session for SCHEDULING only; the stored baseline still governs the score.
		if alt, ok := predS0(madeAt, sessions); ok {
			s0 = alt
			i0 = indexOfDate(sessions, s0)
		}
	}
	if i0 < 0 {
		return predSchedule{}
	}
	sch := predSchedule{S0: s0}
	if cp == "h12" {
		if !horizonStd {
			return predSchedule{}
		}
		target := addMonths(madeOn, 12)
		for _, s := range sessions[i0:] {
			if s >= target {
				sch.Record, sch.Due, sch.Known = s, s, true
				return sch
			}
		}
		// The target date is a lower bound until the calendar shows it.
		sch.Record, sch.Due, sch.Known = target, target, false
		return sch
	}
	n, ok := predCheckpointSessions[cp]
	if !ok {
		return predSchedule{S0: s0}
	}
	// dN is the Nth session AFTER S0 (the F04/F05 rule). Record and Due are the same session: the mark
	// may settle once that session has completed, and nothing is deferred a further session.
	idx := i0 + n
	if idx >= len(sessions) {
		return predSchedule{S0: s0}
	}
	sch.Record, sch.Due, sch.Known = sessions[idx], sessions[idx], true
	return sch
}

// indexOfDate returns the position of a date in an ascending session list, or -1.
func indexOfDate(dates []string, date string) int {
	for i, d := range dates {
		if d == date {
			return i
		}
		if d > date {
			return -1
		}
	}
	return -1
}

// projectedS0 is the weekday lower bound of S0: the receipt date itself when received before 15:00,
// otherwise the next weekday. Holidays only delay, so this is never later than the real session.
func projectedS0(madeAt time.Time) string {
	t := madeAt.In(chinaZone)
	from := t.Format("2006-01-02")
	close := time.Date(t.Year(), t.Month(), t.Day(), predSessionCloseMinute/60, predSessionCloseMinute%60, 0, 0, chinaZone)
	if !t.Before(close) {
		from = shiftDays(from, 1)
	}
	if p := predProjectSessions(from, 1); len(p) > 0 {
		return p[0]
	}
	return ""
}

// predProjectSessions returns the weekday dates (Mon–Fri, no holidays) from `from` onward. A holiday
// only delays a session, so a projected date is never later than the real one: a lower bound.
func predProjectSessions(from string, n int) []string {
	if n <= 0 {
		return nil
	}
	t, err := time.ParseInLocation("2006-01-02", from, chinaZone)
	if err != nil {
		return nil
	}
	out := make([]string, 0, n)
	for len(out) < n {
		switch t.Weekday() {
		case time.Saturday, time.Sunday:
			// skip
		default:
			out = append(out, t.Format("2006-01-02"))
		}
		t = t.AddDate(0, 0, 1)
	}
	return out
}

// predWakeAt is the earliest instant a mark could be due: the projected due date at 15:30 Shanghai.
// While the set's baseline is unsettled it is the earlier of that and S0's projected 15:30, because
// the baseline has to be resolved before any checkpoint can be.
func predWakeAt(set forecastWorkSet, cp string) time.Time {
	madeAt, ok := setMadeAt(set)
	if !ok {
		return time.Time{}
	}
	s0 := set.S0Date
	if s0 == "" {
		s0 = projectedS0(madeAt)
	}
	if s0 == "" {
		return time.Time{}
	}
	due := ""
	if cp == "h12" {
		if !set.HorizonStd {
			return time.Time{}
		}
		target := addMonths(set.MadeOn, 12)
		if p := predProjectSessions(target, 1); len(p) > 0 {
			due = p[0]
		}
	} else if n, ok := predCheckpointSessions[cp]; ok {
		// dN is the Nth session after S0: project N weekdays starting the day after S0.
		if p := predProjectSessions(shiftDays(s0, 1), n); len(p) >= n {
			due = p[n-1]
		}
	}
	if due == "" {
		return time.Time{}
	}
	wake := dateAtClose(due)
	if set.BaseCheck == baseCheckPending || set.BaseCheck == baseRefused {
		if w := dateAtClose(s0); w.Before(wake) {
			wake = w
		}
	}
	return wake
}

// setMadeAt parses the set's receipt instant, falling back to noon on its civil date. The fallback is
// only for a malformed row; a real set always carries an RFC3339 made_at.
func setMadeAt(set forecastWorkSet) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, set.MadeAt); err == nil {
		return t, true
	}
	if t, err := time.ParseInLocation("2006-01-02", set.MadeOn, chinaZone); err == nil {
		return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, chinaZone), true
	}
	return time.Time{}, false
}

// dateAtClose is the session's close (15:30 Asia/Shanghai) on a civil date.
func dateAtClose(date string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", date, chinaZone)
	if err != nil {
		return time.Time{}
	}
	return time.Date(t.Year(), t.Month(), t.Day(),
		predSessionStampMinute/60, predSessionStampMinute%60, 0, 0, chinaZone)
}

// predNotYetShown is the next wake when a woken mark's due session is not yet on the real calendar.
// +10 minutes until 18:00 CST; after 18:00, 09:00 CST on the next weekday; a wake in the 09:00 hour
// moves to 15:30 the same day rather than re-waking every ten minutes through the session.
func predNotYetShown(now time.Time) time.Time {
	t := now.In(chinaZone)
	if t.Hour() < 18 {
		if t.Hour() == 9 {
			return time.Date(t.Year(), t.Month(), t.Day(), 15, 30, 0, 0, chinaZone)
		}
		return now.Add(10 * time.Minute)
	}
	d := t.AddDate(0, 0, 1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, 1)
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, chinaZone)
}

// ---------- refusal classes (V26) ----------

const (
	refusalMarkSpecific = "mark"
	refusalSource       = "source"

	// predFinalMinAttempts and predRetryDeadlineTicks are the two halves of V26: a mark becomes final
	// only after this many failed ATTEMPTS and this many completed sessions past its due session.
	predFinalMinAttempts = 4
)

// predRefusalClass separates a fault in the data from a fault in the delivery. A source failure (the
// vendor, the calendar, the frozen code) is not this mark's attempt: it leaves attempts unchanged and
// never counts toward finality, so an outage cannot finalise a whole cohort on the first tick.
var predRefusalClass = map[string]string{
	// A fault in this mark's own data.
	"no_bar":            refusalMarkSpecific,
	"stale_bar":         refusalMarkSpecific,
	"unverifiable":      refusalMarkSpecific,
	"anchor_mismatch":   refusalMarkSpecific,
	"calendar_mismatch": refusalMarkSpecific, // ONE stock's dates are not on the index calendar
	"bad_window":        refusalMarkSpecific,
	"no_base":           refusalMarkSpecific,
	"no_schedule":       refusalMarkSpecific,
	// A fault in the delivery or the instrument, never this mark's attempt (V26).
	"fetch_failed": refusalSource,
	"not_index":    refusalSource,
	"calendar_bad": refusalSource, // index-wide: zero volume, or a stored s0_date the calendar drops
	"no_bench":     refusalSource,
	"no_source":    refusalSource,
	"calendar_lag": refusalSource, // the stale valve tripped: vendor-wide lag, nothing counts
}

func refusalClassOf(code string) string {
	if class, ok := predRefusalClass[code]; ok {
		return class
	}
	// An unknown code is treated as a source fault: retrying forever is recoverable, finalising on a
	// fault nobody classified is not.
	return refusalSource
}

const (
	predPhaseWaitingS0 = "waiting_s0"
	predPhaseWaiting   = "waiting"
	predPhaseSettling  = "settling"
	predPhaseRetrying  = "retrying"
	predPhaseOverdue   = "overdue"
	predPhaseResolved  = "resolved"
	predPhaseFinal     = "final"
)

// annotateReadPhases is the conservative fallback without a published completed calendar.
func annotateReadPhases(sets []PredictionSet, now time.Time) {
	annotateReadPhasesWithCalendars(sets, now, nil)
}

// Reads never fetch a calendar. Published completed sessions and recorded due sessions are
// evidence; scheduled exchange closures qualify estimates but never shorten the overdue deadline.
func annotateReadPhasesWithCalendars(sets []PredictionSet, now time.Time, calendars map[string][]string) {
	memo := newPredSessionMemo()
	for i := range sets {
		set := &sets[i]
		for j := range set.Marks {
			set.Marks[j].Phase, set.Marks[j].NotBefore, set.Marks[j].Expected = "", "", false
			switch set.Marks[j].Status {
			case markResolved:
				set.Marks[j].Phase = predPhaseResolved
			case markRefusedFinal:
				set.Marks[j].Phase = predPhaseFinal
			}
		}
		madeAt, ok := setMadeAt(forecastWorkSet{MadeAt: set.MadeAt, MadeOn: set.MadeOn})
		if !ok {
			continue
		}
		madeOn := madeAt.In(chinaZone).Format("2006-01-02")
		baseline, baselineExpected := predReadProjectionWith(memo, "d5", madeAt, set.HorizonStd, set.S0Date)
		s0 := set.S0Date
		s0Projected := s0 == ""
		if s0Projected {
			s0 = baseline.S0
		}
		cal := calendars[set.BenchCode]
		calendarUsable := len(cal) > 0 && cal[0] <= madeOn && (set.S0Date == "" || containsDate(cal, set.S0Date))
		projectionS0 := set.S0Date
		if calendarUsable && s0Projected {
			if observed, known := predS0(madeAt, cal); known {
				s0, projectionS0, baselineExpected = observed, observed, false
			}
		}
		s0Ready := s0 != "" && readSessionCompleted(s0, now)
		weekdays := memo.weekdays(shiftDays(madeOn, -1))
		for j := range set.Marks {
			m := &set.Marks[j]
			if m.Status == markResolved || m.Status == markRefusedFinal {
				continue
			}
			if s0Projected && !s0Ready {
				m.Phase, m.NotBefore, m.Expected = predPhaseWaitingS0, s0, baselineExpected
				continue
			}
			sch, expected := predReadProjectionWith(memo, m.Checkpoint, madeAt, set.HorizonStd, projectionS0)
			exact := false
			if calendarUsable {
				actual := predScheduleFor(m.Checkpoint, madeAt, set.HorizonStd, set.S0Date, cal)
				if actual.Known && actual.Due != "" {
					sch, exact, expected = actual, true, false
				}
			}
			if sch.Due == "" {
				m.Phase = predPhaseWaiting
				continue
			}
			// An observed attempt may contradict a scheduled closure. Validate against the
			// weekday lower bound, or an already observed calendar due, not the estimate.
			evidenceBound := sch.Due
			if !exact {
				evidenceBound = predScheduleFor(m.Checkpoint, madeAt, set.HorizonStd, projectionS0, weekdays).Due
			}
			if validReportDate(m.SessionDate) && evidenceBound != "" && m.SessionDate >= evidenceBound {
				sch.Due, exact, expected = m.SessionDate, true, false
			}
			due := dateAtClose(sch.Due)
			if !exact {
				// A better display estimate cannot postpone the conservative state clock.
				due = dateAtClose(evidenceBound)
			}
			if due.IsZero() || now.Before(due) {
				m.Phase, m.NotBefore, m.Expected = predPhaseWaiting, sch.Due, expected
				continue
			}
			deadline := 21 * 24 * time.Hour
			if exact {
				deadline = 48 * time.Hour
			}
			switch {
			case !now.Before(due.Add(deadline)):
				m.Phase = predPhaseOverdue
			case validReportDate(m.SessionDate) && m.SessionDate >= sch.Due:
				m.Phase = predPhaseRetrying
			default:
				m.Phase = predPhaseSettling
			}
			m.NotBefore, m.Expected = sch.Due, expected
		}
	}
}

// readSessionCompleted reports whether a session's close has passed. It is the same 15:30 rule the
// evaluator uses, so the panel and the loop agree about "today".
func readSessionCompleted(date string, now time.Time) bool {
	today := now.In(chinaZone).Format("2006-01-02")
	if date < today {
		return true
	}
	return date == today && predSessionStamp(now)
}
