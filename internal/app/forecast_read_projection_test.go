package app

import (
	"testing"
	"time"
)

func projectionReadSet(cp string) []PredictionSet {
	return []PredictionSet{{BenchCode: "sh000852", MadeAt: "2026-09-17T16:00:00+08:00", MadeOn: "2026-09-17", S0Date: "2026-09-18", HorizonStd: true,
		Marks: []PredictionMark{{Checkpoint: cp, Status: markPending}}}}
}

func TestProjectedClosureDatesKeepConservativeDeadline(t *testing.T) {
	due := dateAtClose("2026-10-26")
	lowerBound := dateAtClose("2026-10-16")
	for _, tc := range []struct {
		now   time.Time
		phase string
	}{{lowerBound.Add(-time.Nanosecond), predPhaseWaiting}, {lowerBound, predPhaseSettling}, {due.Add(48 * time.Hour), predPhaseSettling}, {lowerBound.Add(21*24*time.Hour - time.Nanosecond), predPhaseSettling}, {lowerBound.Add(21 * 24 * time.Hour), predPhaseOverdue}} {
		sets := projectionReadSet("d20")
		annotateReadPhases(sets, tc.now)
		if got := sets[0].Marks[0]; got.Phase != tc.phase || !got.Expected || got.NotBefore != "2026-10-26" {
			t.Fatalf("scheduled closures became observed evidence: %+v", got)
		}
	}
}

func TestRecordedSessionOutranksClosureProjection(t *testing.T) {
	sets := projectionReadSet("d5")
	sets[0].Marks[0].Status, sets[0].Marks[0].SessionDate = markRefused, "2026-09-25"
	annotateReadPhases(sets, dateAtClose("2026-09-25"))
	if got := sets[0].Marks[0]; got.Phase != predPhaseRetrying || got.Expected || got.NotBefore != "2026-09-25" {
		t.Fatalf("recorded evidence lost to a scheduled closure: %+v", got)
	}
}

func TestPublishedCalendarOutranksClosureProjection(t *testing.T) {
	sets := projectionReadSet("d5")
	cal := map[string][]string{"sh000852": {"2026-09-17", "2026-09-18", "2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-25"}}
	annotateReadPhasesWithCalendars(sets, dateAtClose("2026-09-27"), cal)
	if got := sets[0].Marks[0]; got.Phase != predPhaseOverdue || got.Expected || got.NotBefore != "2026-09-25" {
		t.Fatalf("published due lost to a scheduled closure: %+v", got)
	}
}

func TestPublishedS0OutranksClosureProjection(t *testing.T) {
	sets := []PredictionSet{{BenchCode: "sh000852", MadeAt: "2026-09-24T16:00:00+08:00", MadeOn: "2026-09-24", HorizonStd: true,
		Marks: []PredictionMark{{Checkpoint: "d5", Status: markPending}}}}
	cal := map[string][]string{"sh000852": {"2026-09-24", "2026-09-25"}}
	annotateReadPhasesWithCalendars(sets, dateAtClose("2026-09-25"), cal)
	if got := sets[0].Marks[0]; got.Phase != predPhaseWaiting || got.Expected {
		t.Fatalf("observed start lost to a scheduled closure: %+v", got)
	}
}
