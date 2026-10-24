package app

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestReadingPredictionLinksCarryTheReportKind(t *testing.T) {
	run := func(t *testing.T, st *Store) {
		report := seedReport(t, st, "600519", "2026-10-20")
		seedPredictionSet(t, st, sql.NullInt64{Int64: report, Valid: true}, "reading-link")
		for _, kind := range []string{"Stock research", ""} {
			if _, err := st.exec(`UPDATE reports SET kind=? WHERE id=?`, kind, report); err != nil {
				t.Fatal(err)
			}
			sets, err := st.QueryPredictionSets(PredictionFilter{ReportID: report}, nil)
			if err != nil || len(sets) != 1 {
				t.Fatalf("sets=%v err=%v", sets, err)
			}
			if want := repKind(Rep{Kind: kind, RType: sets[0].RType}); sets[0].ReportKind != want {
				t.Fatalf("report kind=%q want=%q", sets[0].ReportKind, want)
			}
		}
	}
	t.Run("sqlite", func(t *testing.T) { run(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		run(t, pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "reports"))
	})
}

func TestReadingBoundariesMatchSettlement(t *testing.T) {
	data, err := os.ReadFile("testdata/forecast/reading_boundaries.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Completeness []struct {
			Date     string `json:"date"`
			AsOf     string `json:"as_of"`
			Now      string `json:"now"`
			Complete bool   `json:"complete"`
		} `json:"completeness"`
		Windows []struct {
			From, To string
			Bars     []struct {
				D string
				L int64
			}
			Low *int64
		} `json:"windows"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Completeness {
		now, err := time.Parse(time.RFC3339, row.Now)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := newestBarFinal(QuoteBar{Date: row.Date}, row.AsOf, now)
		if got != row.Complete {
			t.Errorf("%+v: final=%v", row, got)
		}
	}
	for _, row := range fixture.Windows {
		var bars []QuoteBar
		for _, b := range row.Bars {
			bars = append(bars, QuoteBar{Date: b.D, Low: b.L, High: b.L})
		}
		low, _, ok := lowHighBetween(bars, row.From, row.To)
		if ok != (row.Low != nil) || ok && low != *row.Low {
			t.Errorf("%+v: low=%d present=%v", row, low, ok)
		}
	}
}
