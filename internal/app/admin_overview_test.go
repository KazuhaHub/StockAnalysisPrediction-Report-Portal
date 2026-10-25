package app

import (
	"fmt"
	"testing"
)

func TestReportOverviewAggregatesReportsAndExcludesInternal(t *testing.T) {
	st := newTestStore(t)
	for _, r := range []Rep{
		{Symbol: "A", RType: "analysis", Kind: "Research", Date: "2026-10-24"},
		{Symbol: "B", RType: "analysis", Kind: "Research", Date: "2026-10-25"},
		{Symbol: "C", RType: "analysis", Kind: "", Date: "2026-10-20"},
		{Symbol: "D", RType: "analysis", Kind: "Earlier", Date: "2026-10-01"},
	} {
		if _, _, err := st.UpsertReport(r); err != nil {
			t.Fatal(err)
		}
	}
	for typ := range internalTypes {
		if _, _, err := st.UpsertReport(Rep{Symbol: "X", RType: typ, Date: "2026-10-25"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := st.reportOverview("2026-10-25")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 4 || result.Today != 1 || result.Recent != 3 {
		t.Fatalf("unexpected totals: %+v", result)
	}
	if len(result.Days) != 7 || result.Days[0].Key != "2026-10-19" || result.Days[6].Count != 1 {
		t.Fatalf("unexpected days: %+v", result.Days)
	}
	if len(result.Kinds) != 3 || result.Kinds[0].Key != "Research" || result.Kinds[0].Count != 2 {
		t.Fatalf("unexpected categories: %+v", result.Kinds)
	}
}

func TestReportOverviewRollingPeriodsExcludeFutureDates(t *testing.T) {
	st := newTestStore(t)
	for i, date := range []string{"2026-10-25", "2026-10-24", "2026-09-26", "2025-10-26", "2025-10-25", "2026-10-26"} {
		if _, _, err := st.UpsertReport(Rep{Symbol: fmt.Sprint(i), RType: "analysis", Date: date}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.reportOverview("2026-10-25")
	if err != nil {
		t.Fatal(err)
	}
	if got.Yesterday != 1 || got.Month != 3 || got.Year != 4 {
		t.Fatalf("rolling period counts: %+v", got)
	}
}

func TestReportOverviewAnnualSeriesAndCategoryRanges(t *testing.T) {
	st := newTestStore(t)
	for i, date := range []string{"2026-10-25", "2026-10-01", "2026-08-01", "2025-10-26", "2025-10-25", "2026-10-26"} {
		if _, _, err := st.UpsertReport(Rep{Symbol: fmt.Sprint(i), RType: "analysis", Kind: "Research", Date: date}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.reportOverviewWindow("2026-10-25", 365)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 365 || got.Days[0].Key != "2025-10-26" || got.Recent != 1 {
		t.Fatalf("unexpected annual series: %+v", got)
	}
	for span, count := range map[string]int64{"7": 1, "30": 2, "90": 3, "365": 4} {
		if len(got.KindPeriods[span]) != 1 || got.KindPeriods[span][0].Count != count {
			t.Fatalf("%s-day category counts: %+v", span, got.KindPeriods[span])
		}
	}
}
