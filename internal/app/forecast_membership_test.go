package app

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestClassifierPreservesMemberOrder(t *testing.T) {
	sets := []*groupSet{
		{ID: 9, Symbol: "600002", State: predStateAccepted, MadeAt: "2026-01-02T01:00:00Z", Cohort: "2026-01", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
		{ID: 3, Symbol: "600001", State: predStateAccepted, MadeAt: "2026-01-01T01:00:00Z", Cohort: "2026-01", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
		{ID: 7, Symbol: "600001", State: predStateAccepted, MadeAt: "2026-01-01T01:00:00Z", Cohort: "2026-01", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
	}
	before := append([]*groupSet(nil), sets...)
	got := classifyMembership(sets, time.Time{}, map[string]string{"2026-01": "sh000852"})
	var ids []int64
	var states []string
	for _, v := range got {
		ids = append(ids, v.Set.ID)
		states = append(states, v.Status)
	}
	if !reflect.DeepEqual(ids, []int64{3, 7, 9}) || !reflect.DeepEqual(states, []string{"member", "duplicate", "member"}) {
		t.Fatalf("unstable classifier order: %v / %v", ids, states)
	}
	if !reflect.DeepEqual(before, sets) {
		t.Fatal("classifier mutated its caller's slice")
	}
	members, _, _ := forecastMembership(sets, time.Time{}, "sh000852")
	if len(members) != 2 || members[0].ID != 3 || members[1].ID != 9 {
		t.Fatalf("statistics permutation input order changed: %+v", members)
	}
}

// Keep the old algorithm as an independent oracle for the valid, loader-ordered domain. P16
// adds unknown accounting but must not change the members or their permutation input order.
func legacyMembershipForTest(sets []*groupSet, from time.Time, bench string) (members, pending []*groupSet, cov forecastCoverage) {
	cov.Reports = len(sets)
	firstBench := map[string]string{}
	if bench != "" {
		for _, gs := range sets {
			if !from.IsZero() && madeAtBefore(gs.MadeAt, from) || gs.State != predStateAccepted {
				continue
			}
			if _, ok := firstBench[gs.Symbol]; !ok {
				firstBench[gs.Symbol] = gs.BenchCode
			}
		}
	}
	bySymbol := map[string][]*groupSet{}
	for _, gs := range sets {
		if !from.IsZero() && madeAtBefore(gs.MadeAt, from) {
			cov.PreLive++
			continue
		}
		switch gs.State {
		case predStateAbsent:
			cov.Absent++
		case predStateRejected:
			cov.Rejected++
		case predStateBacktest:
			cov.Backtest++
		case predStateTest:
			cov.Test++
		case predStateAccepted:
			if bench != "" && (gs.BenchCode != bench || firstBench[gs.Symbol] != bench) {
				cov.OffBenchmark++
				continue
			}
			bySymbol[gs.Symbol] = append(bySymbol[gs.Symbol], gs)
		}
	}
	var symbols []string
	for symbol := range bySymbol {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		list := bySymbol[symbol]
		cov.Duplicate += len(list) - 1
		first := list[0]
		switch {
		case first.BaseCheck == baseCheckPending || first.BaseCheck == baseRefused:
			cov.PendingBaseline++
			pending = append(pending, first)
		case first.DecisionQuality != predQualityNormal:
			cov.Degraded++
		case first.BaseCheck == baseMismatch:
			cov.Mismatch++
		default:
			cov.Member++
			members = append(members, first)
		}
	}
	return
}

func TestClassifierMatchesLegacyStatistics(t *testing.T) {
	rng := newPredRand(8675309)
	states := []string{predStateAccepted, predStateAccepted, predStateAbsent, predStateRejected, predStateBacktest, predStateTest}
	checks := []string{baseMatch, baseMismatch, baseCheckPending, baseRefused, baseRefusedFinal}
	for trial := 0; trial < 500; trial++ {
		var sets []*groupSet
		for i, n := 0, 1+rng.below(150); i < n; i++ {
			quality := predQualityNormal
			if rng.below(4) == 0 {
				quality = "degraded"
			}
			sets = append(sets, &groupSet{ID: int64(i + 1), Symbol: symbolFor(rng.below(24)),
				MadeAt: fmt.Sprintf("2026-01-%02dT01:00:00Z", 1+rng.below(28)), Cohort: "2026-01",
				BenchCode: []string{"sh000852", "sh000906", ""}[rng.below(3)],
				State:     states[rng.below(uint64(len(states)))], DecisionQuality: quality,
				BaseCheck: checks[rng.below(uint64(len(checks)))]})
		}
		sort.Slice(sets, func(i, j int) bool {
			if sets[i].Symbol != sets[j].Symbol {
				return sets[i].Symbol < sets[j].Symbol
			}
			if sets[i].MadeAt != sets[j].MadeAt {
				return sets[i].MadeAt < sets[j].MadeAt
			}
			return sets[i].ID < sets[j].ID
		})
		from := time.Time{}
		if trial%2 == 0 {
			from = time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
		}
		bench := []string{"", "sh000852", "sh000906"}[trial%3]
		wm, wp, wc := legacyMembershipForTest(sets, from, bench)
		gm, gp, gc := forecastMembership(sets, from, bench)
		if !reflect.DeepEqual(wm, gm) || !reflect.DeepEqual(wp, gp) || wc != gc {
			t.Fatalf("valid statistics changed at trial %d: want %+v, got %+v", trial, wc, gc)
		}
	}
}

func TestUnknownStateIsCountedNotDropped(t *testing.T) {
	s := newV1Server(t)
	seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-01-05", State: "future-state"})
	sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-01-01", "2026-01-31", forecastPopulation{Key: "all"})
	if err != nil {
		t.Fatal(err)
	}
	verdicts := classifyMembership(sets, time.Time{}, nil)
	if len(verdicts) != 1 || verdicts[0].Status != "unknown" {
		t.Fatalf("unknown state silently disappeared: %+v", verdicts)
	}
	members, pending, cov := forecastMembership(sets, time.Time{}, "")
	if len(members) != 0 || len(pending) != 0 || cov.Unknown != 1 || cov.Reports != 1 {
		t.Fatalf("unknown state was promoted or lost from coverage: %+v", cov)
	}
	data, err := json.Marshal(cov)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil || wire["unknown"] != float64(1) {
		t.Fatalf("unknown coverage is not observable: %s / %v", data, err)
	}
}

func TestMembershipClassificationPrecedence(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base := groupSet{State: predStateAccepted, MadeAt: "2026-01-05T01:00:00Z", Cohort: "2026-01", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch}
	var sets []*groupSet
	add := func(id int64, symbol string, mutate func(*groupSet)) {
		s := base
		s.ID, s.Symbol = id, symbol
		if mutate != nil {
			mutate(&s)
		}
		sets = append(sets, &s)
	}
	add(1, "600001", func(s *groupSet) { s.MadeAt = "2025-12-31T01:00:00Z" })
	add(2, "600001", nil)
	add(3, "600002", func(s *groupSet) { s.State = predStateTest })
	add(4, "600002", nil)
	add(5, "600003", func(s *groupSet) { s.DecisionQuality = "degraded" })
	add(6, "600003", nil)
	add(7, "600004", func(s *groupSet) { s.BaseCheck = baseMismatch })
	add(8, "600004", nil)
	add(9, "600005", func(s *groupSet) { s.BaseCheck = baseCheckPending; s.DecisionQuality = "degraded" })
	add(10, "600005", nil)
	add(11, "600006", func(s *groupSet) { s.BenchCode = "sh000906" })
	add(12, "600006", nil)
	add(13, "600007", func(s *groupSet) { s.State = predStateAbsent })
	add(14, "600008", func(s *groupSet) { s.State = predStateRejected })
	add(15, "600009", func(s *groupSet) { s.State = predStateBacktest })
	add(16, "600010", func(s *groupSet) { s.State = "future-state" })
	add(17, "600010", nil)
	want := []string{"pre_live", "member", "test", "member", "degraded", "duplicate", "mismatch", "duplicate", "pending_baseline", "duplicate", "off_benchmark", "off_benchmark", "absent", "rejected", "backtest", "unknown", "member"}
	got := classifyMembership(sets, from, map[string]string{"2026-01": "sh000852"})
	if len(got) != len(want) {
		t.Fatalf("classifier dropped rows: %d", len(got))
	}
	for i, v := range got {
		if v.Status != want[i] || v.Set.ID != int64(i+1) {
			t.Errorf("verdict %d = %d/%s, want %s", i, v.Set.ID, v.Status, want[i])
		}
	}
	_, _, cov := forecastMembership(sets, from, "sh000852")
	sum := cov.Member + cov.Duplicate + cov.Degraded + cov.Mismatch + cov.PendingBaseline + cov.PreLive + cov.OffBenchmark + cov.Test + cov.Backtest + cov.Absent + cov.Rejected + cov.Unknown
	if sum != cov.Reports || sum != len(sets) {
		t.Fatalf("classification coverage is not exhaustive: %+v", cov)
	}
}

func TestClassifierUsesEachCohortsFrozenIntent(t *testing.T) {
	sets := []*groupSet{
		{ID: 1, Symbol: "600001", State: predStateAccepted, MadeAt: "2026-10-01T01:00:00Z", Cohort: "2026-10", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
		{ID: 2, Symbol: "600002", State: predStateAccepted, MadeAt: "2026-11-01T01:00:00Z", Cohort: "2026-11", BenchCode: "sh000906", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
		{ID: 3, Symbol: "600003", State: predStateAccepted, MadeAt: "2026-11-01T01:00:00Z", Cohort: "2026-11", BenchCode: "sh000852", DecisionQuality: predQualityNormal, BaseCheck: baseMatch},
	}
	got := classifyMembership(sets, time.Time{}, map[string]string{"2026-10": "sh000852", "2026-11": "sh000906"})
	if len(got) != 3 || got[0].Status != "member" || got[1].Status != "member" || got[2].Status != "off_benchmark" {
		t.Fatalf("one cohort's intent was reused for another: %+v", got)
	}
}
