package app

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestReadCalendarConcurrentPublication(t *testing.T) {
	e := &forecastEvaluator{}
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				e.publishReadCalendar("sh000852", []string{"2026-10-16", "2026-10-19"})
				copy := e.readCalendarSnapshot()
				if len(copy["sh000852"]) != 2 {
					t.Error("incomplete calendar publication")
					return
				}
				copy["sh000852"][0] = "2099-01-01"
			}
		}()
	}
	wg.Wait()
	if got := e.readCalendarSnapshot()["sh000852"][0]; got != "2026-10-16" {
		t.Fatalf("snapshot mutation escaped: %s", got)
	}
}

func TestNotBeforeIsALowerBound(t *testing.T) {
	data, err := os.ReadFile("testdata/forecast_exchange_closures_2023_2026.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Years []struct {
			Year     int
			Source   string
			Closures [][2]string
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Years) != 4 {
		t.Fatal("four calendar years required")
	}
	closed := map[string]bool{}
	for i, year := range fixture.Years {
		if year.Year != 2023+i || year.Source == "" {
			t.Fatalf("invalid calendar provenance: %+v", year)
		}
		for _, span := range year.Closures {
			from, err := time.Parse("2006-01-02", span[0])
			if err != nil {
				t.Fatal(err)
			}
			to, err := time.Parse("2006-01-02", span[1])
			if err != nil || to.Before(from) {
				t.Fatalf("invalid closure: %v", span)
			}
			for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
				closed[d.Format("2006-01-02")] = true
			}
		}
	}
	var sessions []string
	start := shanghai(2023, time.January, 1, 0, 0)
	end := shanghai(2026, time.December, 31, 0, 0)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		if day.Weekday() != time.Saturday && day.Weekday() != time.Sunday && !closed[day.Format("2006-01-02")] {
			sessions = append(sessions, day.Format("2006-01-02"))
		}
	}
	checks := 0
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		for _, minute := range []int{14*60 + 59, 15 * 60, 21 * 60} {
			made := day.Add(time.Duration(minute) * time.Minute)
			date := day.Format("2006-01-02")
			i0 := sort.SearchStrings(sessions, date)
			if i0 < len(sessions) && sessions[i0] == date && minute >= 15*60 {
				i0++
			}
			if i0 >= len(sessions) {
				continue
			}
			for _, frozen := range []string{"", sessions[i0]} {
				for _, cp := range predCheckpoints {
					index := i0
					if cp == "h12" {
						y, m, d := day.Date()
						// Preserve the existing normalized calendar-month arithmetic, including
						// February 29 rolling into March in a non-leap target year.
						index = sort.SearchStrings(sessions, time.Date(y+1, m, d, 0, 0, 0, 0, chinaZone).Format("2006-01-02"))
					} else {
						index += map[string]int{"d5": 5, "d20": 20, "d60": 60, "d120": 120}[cp]
					}
					if index >= len(sessions) {
						continue
					}
					sets := []PredictionSet{{MadeAt: made.Format(time.RFC3339), MadeOn: date, S0Date: frozen, HorizonStd: true, Marks: []PredictionMark{{Checkpoint: cp, Status: markPending}}}}
					annotateReadPhases(sets, shanghai(2030, time.January, 1, 16, 0))
					bound := sets[0].Marks[0].NotBefore
					if bound == "" || bound > sessions[index] {
						t.Fatalf("%s %s frozen=%s: bound %s exceeds calendar due %s", made, cp, frozen, bound, sessions[index])
					}
					checks++
				}
			}
		}
	}
	if checks < 25000 {
		t.Fatalf("insufficient calendar coverage: %d", checks)
	}
	t.Logf("checked %d receipt/checkpoint/start combinations against four sourced calendars", checks)
}

func TestReadUsesPublishedCompletedCalendarWithoutFetching(t *testing.T) {
	sets := func() []PredictionSet {
		return []PredictionSet{{BenchCode: "sh000852", MadeAt: "2026-10-18T13:00:00Z", MadeOn: "2026-10-18", S0Date: "2026-10-19", HorizonStd: true,
			Marks: []PredictionMark{{Checkpoint: "d5", Status: markPending}}}}
	}
	cal := []string{"2026-10-16", "2026-10-19", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-26", "2026-10-27"}
	s := &Server{}
	e := &s.forecastEval
	e.fetch = func(context.Context, string, string, string, string) (*QuoteResp, error) {
		t.Fatal("read fetched market data")
		return nil, nil
	}
	e.publishReadCalendar("sh000852", cal)
	cal[0] = "2099-01-01"
	snapshot := e.readCalendarSnapshot()
	snapshot["sh000852"][0] = "2099-01-02"
	rows := sets()
	s.annotateReadPhases(rows, dateAtClose("2026-10-29"))
	if m := rows[0].Marks[0]; m.Phase != "overdue" || m.NotBefore != "2026-10-27" {
		t.Fatalf("published due = %+v", m)
	}
	// Missing coverage and contradictory starts fall back to the conservative deadline.
	for _, replacement := range [][]string{
		{"2026-10-21", "2026-10-22", "2026-10-23", "2026-10-26", "2026-10-27"},
		{"2026-10-16", "2026-10-20", "2026-10-21", "2026-10-22", "2026-10-23", "2026-10-26", "2026-10-27", "2026-10-28"},
	} {
		e.publishReadCalendar("sh000852", replacement)
		rows = sets()
		s.annotateReadPhases(rows, dateAtClose("2026-10-29"))
		if m := rows[0].Marks[0]; m.Phase != "settling" || m.NotBefore != "2026-10-26" {
			t.Fatalf("incomplete calendar claimed exact due: %+v", m)
		}
	}
}

func TestEvaluatorPublishesOnlyValidatedCompletedCalendar(t *testing.T) {
	now := shanghai(2026, time.October, 26, 14, 0)
	dates := tradingDays("2026-10-16", 10)
	index := barsFor(dates, 4000)
	e := evalStub(&now, index, nil, nil, nil)
	s := routeTableServer(t)
	spec, _ := benchSpecFor("sh000852")
	_, fault := e.benchCalendar(context.Background(), s, spec, []forecastWorkSet{{MadeOn: "2026-10-18", MadeAt: "2026-10-18T13:00:00Z"}})
	if fault != nil {
		t.Fatal(fault)
	}
	cal := e.readCalendarSnapshot()[spec.Code]
	if len(cal) == 0 || cal[len(cal)-1] != "2026-10-23" {
		t.Fatalf("intraday/future publication: %v", cal)
	}
	// A broken index must not replace the last validated snapshot.
	index[0].Volume = 0
	_, fault = e.benchCalendar(context.Background(), s, spec, []forecastWorkSet{{MadeOn: "2026-10-18"}})
	if fault == nil {
		t.Fatal("invalid index accepted")
	}
	if got := e.readCalendarSnapshot()[spec.Code]; len(got) != len(cal) || got[len(got)-1] != cal[len(cal)-1] {
		t.Fatalf("invalid calendar replaced snapshot: %v", got)
	}
}

func TestReadPhaseBoundaries(t *testing.T) {
	due := dateAtClose("2026-10-26")
	for _, tc := range []struct {
		name, session, status, want string
		now                         time.Time
	}{
		{"before close", "", markPending, "waiting", due.Add(-time.Nanosecond)},
		{"at lower bound", "", markPending, "settling", due},
		{"malformed session is not evidence", "not-a-date", markRefused, "settling", due},
		{"contradictory early session", "2026-10-19", markRefused, "settling", due},
		{"future recorded due", "2026-10-27", markRefused, "waiting", due},
		{"before lower bound deadline", "", markRefused, "settling", due.Add(21*24*time.Hour - time.Nanosecond)},
		{"lower bound deadline", "", markPending, "overdue", due.Add(21 * 24 * time.Hour)},
		{"recorded attempt", "2026-10-26", markRefused, "retrying", due},
		{"before exact deadline", "2026-10-26", markRefused, "retrying", due.Add(48*time.Hour - time.Nanosecond)},
		{"exact deadline", "2026-10-26", markRefused, "overdue", due.Add(48 * time.Hour)},
		{"resolved wins", "2026-10-26", markResolved, "resolved", due.Add(60 * 24 * time.Hour)},
		{"final wins", "2026-10-26", markRefusedFinal, "final", due.Add(60 * 24 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sets := []PredictionSet{{MadeAt: "2026-10-18T13:00:00Z", MadeOn: "2026-10-18", S0Date: "2026-10-19", HorizonStd: true,
				Marks: []PredictionMark{{Checkpoint: "d5", Status: tc.status, SessionDate: tc.session}}}}
			annotateReadPhases(sets, tc.now)
			if got := sets[0].Marks[0].Phase; got != tc.want {
				t.Fatalf("phase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadPhaseAnnotationClearsPreviousDerivedValues(t *testing.T) {
	sets := []PredictionSet{{MadeAt: "2026-10-18T13:00:00Z", MadeOn: "2026-10-18", S0Date: "2026-10-19", HorizonStd: true,
		Marks: []PredictionMark{{Checkpoint: "d5", Status: markResolved, Phase: "waiting", NotBefore: "2099-01-01"}}}}
	annotateReadPhases(sets, time.Now())
	if mark := sets[0].Marks[0]; mark.Phase != "resolved" || mark.NotBefore != "" {
		t.Fatalf("stale derived values: %+v", mark)
	}
}
