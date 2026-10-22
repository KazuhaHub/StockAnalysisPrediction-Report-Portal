package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGroupMetadataRoundTripsThroughTypedResponses(t *testing.T) {
	meta := &ForecastGroupsMetadata{Summary: forecastGroupsSummary{Sets: 3, MarksByPhase: forecastPhaseCounts()},
		MinN: 30, GeneratedAt: "2026-10-22T00:00:00Z", Capabilities: forecastCapabilities{Monthly: true}}
	for _, reader := range []bool{false, true} {
		var source any = forecastGroupsResponse{OK: true, ForecastGroupsMetadata: meta}
		if reader {
			source = forecastReaderGroupsResponse{OK: true, ForecastGroupsMetadata: meta}
		}
		wire, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		if reader {
			var got forecastReaderGroupsResponse
			if err := json.Unmarshal(wire, &got); err != nil || got.ForecastGroupsMetadata == nil || got.Summary.Sets != 3 {
				t.Fatalf("reader metadata decode: %+v, %v", got, err)
			}
		} else {
			var got forecastGroupsResponse
			if err := json.Unmarshal(wire, &got); err != nil || got.ForecastGroupsMetadata == nil || got.Summary.Sets != 3 {
				t.Fatalf("admin metadata decode: %+v, %v", got, err)
			}
		}
	}
}

func TestCapabilitiesMirrorRouteGates(t *testing.T) {
	s := newV1Server(t)
	for i, tc := range []struct {
		scope    string
		external bool
		admin    bool
	}{{scopeAll, false, false}, {scopeOwn, false, false}, {scopeNone, false, false}, {scopeAll, true, false}, {scopeOwn, true, false}, {scopeNone, true, false}, {scopeNone, true, true}} {
		user := fmt.Sprintf("capability-reader-%d", i)
		ou := seedForecastReader(t, s, user, tc.scope, tc.external)
		if tc.admin {
			if _, err := s.st.exec(`UPDATE users SET role='admin' WHERE username=?`, user); err != nil {
				t.Fatal(err)
			}
		}
		id := seedGroupSet(t, s, gsSeed{Symbol: fmt.Sprintf("600%03d", i), MadeOn: "2025-06-10", S0Fen: ip(10000)})
		if _, err := s.st.exec(`UPDATE prediction_sets SET attributed_ou=? WHERE id=?`, ou, id); err != nil {
			t.Fatal(err)
		}
		caps := forecastCapabilitiesFor(s.forecastAccess(user))
		for mode, allowed := range map[string]bool{"month": caps.Monthly, "custom": caps.Custom} {
			w := httptest.NewRecorder()
			_, ok := s.requireForecastAccess(w, httptest.NewRequest("GET", "/?mode="+mode, nil), user)
			if ok != allowed {
				t.Errorf("%+v mode=%s: route allowed=%v, capability=%v", tc, mode, ok, allowed)
			}
		}
		w := httptest.NewRecorder()
		s.apiReaderForecastGroupMembers(w, httptest.NewRequest("GET", "/?mode=month&label=2025-06&checkpoint=d5", nil), user)
		if (w.Code == 200) != caps.Members {
			t.Errorf("%+v members: status=%d capability=%v", tc, w.Code, caps.Members)
		}
	}
}

func TestGroupExplainMetadataUsesTheScopedRetainedRange(t *testing.T) {
	s := newV1Server(t)
	own := seedForecastReader(t, s, "metadata-own", scopeOwn, true)
	other := seedOU(t, s.st, 0, scopeAll)
	for i, g := range []gsSeed{
		{Symbol: "600001", MadeOn: "2025-06-10", S0Fen: ip(10000), HorizonStd: true, Marks: map[string]gsMark{"d5": {Status: markResolved}}},
		{Symbol: "600001", MadeOn: "2025-06-11", S0Fen: ip(10000), HorizonStd: true},
		{Symbol: "600002", MadeOn: "2025-06-12", State: predStateRejected},
		{Symbol: "600003", MadeOn: "2025-05-01", S0Fen: ip(10000)},
	} {
		id := seedGroupSet(t, s, g)
		ou := own
		if i == 3 {
			ou = other
		}
		if _, err := s.st.exec(`UPDATE prediction_sets SET attributed_ou=? WHERE id=?`, ou, id); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{setForecastAuto: forecastModeDry, setForecastPopulationFrom: "2025-06-01T00:00:00Z", setForecastSlotsOfficial: "3", setForecastSlotsExplore: "1", setForecastWaitSec: "7", setForecastCustomPerMin: "11", setForecastMembersPerMin: "13"} {
		if err := s.st.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	got := readerGroups(t, s, "metadata-own", "")
	summary, ok := got["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary missing: %+v", got)
	}
	if summary["sets"] != float64(3) || summary["accepted"] != float64(2) || summary["stocks"] != float64(2) || summary["since"] != "2025-06-10" {
		t.Fatalf("summary escaped the own retained range: %+v", summary)
	}
	phases := summary["marks_by_phase"].(map[string]any)
	count := float64(0)
	for _, v := range phases {
		count += v.(float64)
	}
	if count != 5 || phases[predPhaseResolved] != float64(1) {
		t.Fatalf("summary counted duplicate/rejected checkpoint obligations: %+v", phases)
	}
	if got["min_n"] != float64(30) || got["ledger_start"] != "2025-06-01T00:00:00Z" || got["evaluator"].(map[string]any)["mode"] != "dry" {
		t.Fatalf("missing record metadata: %+v", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, got["generated_at"].(string)); err != nil {
		t.Fatal(err)
	}
	limits := got["limits"].(map[string]any)
	for key, want := range map[string]float64{"max_range_months": 36, "max_week_groups": 26, "official_slots": 3, "explore_slots": 1, "wait_sec": 7, "custom_per_min": 11, "members_per_min": 13} {
		if limits[key] != want {
			t.Errorf("limit %s=%v, want %v", key, limits[key], want)
		}
	}
	seedForecastReader(t, s, "metadata-external-all", scopeAll, true)
	protected := readerGroups(t, s, "metadata-external-all", "")
	for _, key := range []string{"summary", "rtypes", "min_n", "limits", "generated_at", "ledger_start", "capabilities", "evaluator"} {
		if _, exists := protected[key]; exists {
			t.Errorf("external all received %s", key)
		}
	}
}

func TestGroupSummaryReconcilesMatureAndPendingCells(t *testing.T) {
	n, corp, refused := 30, 2, 1
	groups := []forecastGroup{{Cells: map[string]*forecastCell{
		"d5": {State: "mature", N: &n, CorpAction: &corp, Refused: &refused,
			PendingReasons: map[string]int{predPhaseResolved: 999}, NotBefore: "2020-01-01", Expected: true},
		"d20":  {State: "pending", PendingReasons: map[string]int{predPhaseWaiting: 1, predPhaseFinal: 1}, NotBefore: "2026-10-26", Expected: true},
		"d60":  {State: "pending", PendingReasons: map[string]int{predPhaseWaiting: 1}, NotBefore: "2026-10-26"},
		"d120": {State: "purged", PendingReasons: map[string]int{predPhaseResolved: 999}, NotBefore: "2019-01-01"},
	}}}
	got := forecastGroupsSummaryFor(nil, groups)
	if got.MarksByPhase[predPhaseResolved] != 32 || got.MarksByPhase[predPhaseFinal] != 2 || got.MarksByPhase[predPhaseWaiting] != 2 || got.NextResultNotBefore != "2026-10-26" || got.Expected {
		t.Fatalf("summary mixed counts, stale metadata or estimate qualifiers: %+v", got)
	}
}

func TestGroupSummaryExcludesPurgedMonthsAndRespectsCustomRange(t *testing.T) {
	s := routeTableServer(t)
	for _, month := range []string{"2025-06", "2025-07", "2025-08"} {
		seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: month + "-10", S0Fen: ip(10000), HorizonStd: true,
			Marks: map[string]gsMark{"d5": {Status: markResolved}}})
	}
	if _, err := s.st.exec(`INSERT INTO prediction_purges(rtype,cohort,purged_at,sets_removed) VALUES(?,?,?,?)`, predTestRtype, "2025-06", "2026-10-22T00:00:00Z", 1); err != nil {
		t.Fatal(err)
	}
	// A late row in a purged month must not resurrect the month in summary scalars or phases.
	monthly := doGroups(t, s, "/?mode=month")
	if monthly.ForecastGroupsMetadata == nil || monthly.Summary.Sets != 2 || monthly.Summary.Since != "2025-07-10" || monthly.Summary.MarksByPhase[predPhaseResolved] != 2 {
		t.Fatalf("purged month reappeared in summary: %+v", monthly.ForecastGroupsMetadata)
	}
	custom := doGroups(t, s, "/?mode=custom&from=2025-06-01&to=2025-07-31&group=all")
	if custom.Summary.Sets != 1 || custom.Summary.Accepted != 1 || custom.Summary.Stocks != 1 || custom.Summary.Since != "2025-07-10" || custom.Summary.MarksByPhase[predPhaseResolved] != 1 || len(custom.ExcludedMonths) != 1 {
		t.Fatalf("custom summary escaped retained range: %+v", custom.ForecastGroupsMetadata)
	}
	for _, phaseCounts := range []map[string]int{monthly.Summary.MarksByPhase, custom.Summary.MarksByPhase} {
		if phaseCounts["unknown"] != 0 {
			t.Fatal("valid retained population lost its timing")
		}
	}
}
