package app

// Checkpoint scheduling (P1, ADR 0029 §4).
//
// This file is the ONE statement of the checkpoint arithmetic. The evaluator calls it with the
// benchmark's COMPLETED sessions; the read path (P10/P16) calls it with a weekday projection, where
// every date is a lower bound because holidays only ever delay a session. Nothing else computes a
// checkpoint date, so "when is this mark due" cannot drift between the settlement and the display.
//
// P1 ships TODAY's rules unchanged — S0 is the first session strictly after the receipt date, and dN
// records sessions[N-1] and is only due once N+1 sessions exist. P6 changes the definitions inside
// predScheduleFor and the tests in this file move with it.

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

// predScheduleFor maps a checkpoint to its sessions. `sessions` are completed sessions strictly after
// the receipt date, oldest first; `frozenS0` is the start session already stored on the set, if any.
func predScheduleFor(cp string, madeAt time.Time, horizonStd bool, frozenS0 string, sessions []string) predSchedule {
	madeOn := madeAt.In(chinaZone).Format("2006-01-02")
	s0 := frozenS0
	if s0 == "" && len(sessions) > 0 {
		s0 = sessions[0]
	}
	if s0 == "" {
		return predSchedule{}
	}
	sch := predSchedule{S0: s0}
	if cp == "h12" {
		if !horizonStd {
			return predSchedule{}
		}
		target := addMonths(madeOn, 12)
		for _, s := range sessions {
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
		return predSchedule{}
	}
	// Today's rule: the Nth session after the receipt date, due once N+1 sessions have completed.
	// The extra one is the F04 off-by-one P6 fixes; P1 pins it rather than changing two things.
	if len(sessions) <= n {
		return predSchedule{S0: s0}
	}
	// Today's rule records the Nth session but may settle only once the NEXT one has completed:
	// `Due` is the session after which the mark may settle, so `sessionsPastDue` starts at zero.
	sch.Record, sch.Due, sch.Known = sessions[n-1], sessions[n], true
	return sch
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
	madeOn := set.MadeOn
	due := ""
	if cp == "h12" {
		if !set.HorizonStd {
			return time.Time{}
		}
		target := addMonths(madeOn, 12)
		if p := predProjectSessions(target, 1); len(p) > 0 {
			due = p[0]
		}
	} else if n, ok := predCheckpointSessions[cp]; ok {
		// Match predScheduleFor's "N+1 completed" rule: the wake is the projected (N+1)th session.
		if p := predProjectSessions(shiftDays(madeOn, 1), n+1); len(p) > n {
			due = p[n]
		}
	}
	if due == "" {
		return time.Time{}
	}
	wake := dateAtClose(due)
	if set.BaseCheck == baseCheckPending || set.BaseCheck == baseRefused {
		s0 := set.S0Date
		if s0 == "" {
			if p := predProjectSessions(shiftDays(madeOn, 1), 1); len(p) > 0 {
				s0 = p[0]
			}
		}
		if s0 != "" {
			if w := dateAtClose(s0); w.Before(wake) {
				wake = w
			}
		}
	}
	return wake
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
	"no_bar":       refusalMarkSpecific,
	"no_base":      refusalMarkSpecific,
	"no_schedule":  refusalMarkSpecific,
	"fetch_failed": refusalSource,
	"not_index":    refusalSource,
	"calendar_bad": refusalSource,
	"no_bench":     refusalSource,
}

func refusalClassOf(code string) string {
	if class, ok := predRefusalClass[code]; ok {
		return class
	}
	// An unknown code is treated as a source fault: retrying forever is recoverable, finalising on a
	// fault nobody classified is not.
	return refusalSource
}
