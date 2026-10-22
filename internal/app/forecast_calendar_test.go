package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProjectionMatchesKnownCalendars(t *testing.T) {
	made := shanghai(2026, 9, 17, 16, 0)
	for cp, want := range map[string]string{"d5": "2026-09-28", "d20": "2026-10-26", "d60": "2026-12-21"} {
		sch, expected := predReadProjection(cp, made, true, "")
		if !expected || sch.S0 != "2026-09-18" || sch.Due != want {
			t.Errorf("%s: schedule=%+v expected=%v, want %s", cp, sch, expected, want)
		}
	}
	for _, date := range []string{"2026-01-04", "2026-02-14", "2026-02-28", "2026-05-09", "2026-09-20", "2026-10-10"} {
		if !predExchangeClosed(date) {
			t.Errorf("make-up working weekend became a trading day: %s", date)
		}
	}
}

func TestProjectionRespectsReceiptCloseAndHolidayStarts(t *testing.T) {
	for _, tc := range []struct{ receipt, s0, d5 string }{
		{"2026-09-17T14:59:59+08:00", "2026-09-17", "2026-09-24"},
		{"2026-09-17T15:00:00+08:00", "2026-09-18", "2026-09-28"},
		{"2026-02-13T16:00:00+08:00", "2026-02-24", "2026-03-03"},
		{"2026-05-01T12:00:00+08:00", "2026-05-06", "2026-05-13"},
		{"2026-09-25T12:00:00+08:00", "2026-09-28", "2026-10-12"},
	} {
		made, err := time.Parse(time.RFC3339, tc.receipt)
		if err != nil {
			t.Fatal(err)
		}
		sch, expected := predReadProjection("d5", made, true, "")
		if !expected || sch.S0 != tc.s0 || sch.Due != tc.d5 {
			t.Errorf("%s: got %+v/%v, want S0=%s d5=%s", tc.receipt, sch, expected, tc.s0, tc.d5)
		}
	}
}

func TestMissingYearDegradesToOnOrAfter(t *testing.T) {
	for _, tc := range []struct{ made, cp, frozen string }{
		{"2027-01-04T08:00:00+08:00", "d5", ""},
		{"2026-12-28T16:00:00+08:00", "d20", ""},
		{"2026-09-17T16:00:00+08:00", "h12", ""},
		{"2026-09-17T16:00:00+08:00", "d5", "2026-09-25"},
		{"2026-09-17T16:00:00+08:00", "d5", "2026-09-01"},
	} {
		made, err := time.Parse(time.RFC3339, tc.made)
		if err != nil {
			t.Fatal(err)
		}
		got, expected := predReadProjection(tc.cp, made, true, tc.frozen)
		want := predScheduleFor(tc.cp, made, true, tc.frozen, predProjectSessions(shiftDays(made.In(chinaZone).Format("2006-01-02"), -1), 400))
		if expected || got != want {
			t.Errorf("%+v: got %+v/%v, want lower bound %+v", tc, got, expected, want)
		}
	}
}

func TestReadProjectionQualifiesDatesWithoutChangingEvaluatorWake(t *testing.T) {
	sets := []PredictionSet{{MadeAt: "2026-09-17T16:00:00+08:00", MadeOn: "2026-09-17", HorizonStd: true,
		Marks: []PredictionMark{{Checkpoint: "d20", Status: markPending}, {Checkpoint: "h12", Status: markPending}}}}
	wake := predWakeAt(forecastWorkSet{MadeAt: sets[0].MadeAt, MadeOn: sets[0].MadeOn}, "d20")
	annotateReadPhases(sets, shanghai(2026, 9, 19, 12, 0))
	if mark := sets[0].Marks[0]; !mark.Expected || mark.NotBefore != "2026-10-26" {
		t.Fatalf("calendar-backed read = %+v", mark)
	}
	if mark := sets[0].Marks[1]; mark.Expected || mark.NotBefore == "" {
		t.Fatalf("unknown-year read = %+v", mark)
	}
	if wake != dateAtClose("2026-10-16") {
		t.Fatalf("evaluator lower-bound wake changed: %s", wake)
	}
	sets[0].Marks[0].Status = markResolved
	annotateReadPhases(sets, shanghai(2026, 9, 19, 12, 0))
	if mark := sets[0].Marks[0]; mark.Expected || mark.NotBefore != "" {
		t.Fatalf("resolved mark retained a projection qualifier: %+v", mark)
	}
}

func TestSettlementNeverReadsTheTable(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "forecast_calendar.go" {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name.Name == "annotateReadPhasesWithCalendars" {
				continue
			}
			location := "package declaration"
			if ok {
				location = fn.Name.Name
			}
			ast.Inspect(decl, func(node ast.Node) bool {
				id, ok := node.(*ast.Ident)
				if ok && (id.Name == "predReadProjection" || strings.HasPrefix(id.Name, "predExchange")) {
					t.Errorf("projection-only calendar used by %s in %s", location, name)
				}
				return true
			})
		}
	}
}
