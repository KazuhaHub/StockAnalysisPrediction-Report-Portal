package app

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func TestNonMatureCellsCarryPendingReasons(t *testing.T) {
	row := func(id int64, made, s0, status, session string) *groupSet {
		return &groupSet{ID: id, MadeOn: made, MadeAt: made + "T16:00:00+08:00", S0Date: s0, HorizonStd: true,
			Marks: map[string]PredictionMark{"d5": {Checkpoint: "d5", Status: status, SessionDate: session}}}
	}
	members := []*groupSet{
		row(1, "2026-10-18", "2026-10-19", markResolved, ""),
		row(2, "2026-10-18", "2026-10-19", markRefusedFinal, ""),
		row(3, "2026-10-23", "2026-10-26", markPending, ""),
		row(4, "2026-10-18", "2026-10-19", markRefused, "2026-10-26"),
		row(5, "2026-08-31", "2026-09-01", markPending, ""),
		row(6, "2026-10-18", "2026-10-19", markPending, ""),
	}
	pending := []*groupSet{row(7, "2026-10-27", "", markPending, "")}
	cell := &forecastCell{State: "pending"}
	before, _ := json.Marshal([]any{cell, members, pending})
	got := forecastCellWithPendingReasons(cell, members, pending, "d5", dateAtClose("2026-10-26"), nil)
	for _, phase := range []string{predPhaseWaitingS0, predPhaseWaiting, predPhaseSettling, predPhaseRetrying, predPhaseOverdue, predPhaseResolved, predPhaseFinal} {
		if got.PendingReasons[phase] != 1 {
			t.Errorf("%s count = %d, want 1", phase, got.PendingReasons[phase])
		}
	}
	if got.FailedCapExceeded == nil || !*got.FailedCapExceeded || got.NotBefore == "" {
		t.Fatalf("missing explanation: %+v", got)
	}
	after, _ := json.Marshal([]any{cell, members, pending})
	if !reflect.DeepEqual(before, after) || got == cell {
		t.Fatal("explanation mutated shared statistics or its population")
	}
	for _, state := range []string{"mature", "purged"} {
		if got := forecastCellWithPendingReasons(&forecastCell{State: state}, members, pending, "d5", time.Now(), nil); got.PendingReasons != nil || got.FailedCapExceeded != nil || got.NotBefore != "" {
			t.Fatalf("%s cell acquired pending metadata: %+v", state, got)
		}
	}
}

// Frozen state oracle from the pre-explanation gate. Keep it independent of the live gate.
func explainFrozenCellState(members, pending []*groupSet, cp, end, today string) string {
	if end >= today {
		return "open"
	}
	resolved, refused, corp := 0, 0, 0
	blocked := false
	for _, gs := range members {
		m, ok := gs.Marks[cp]
		if !ok || m.Status == "pending" || m.Status == "refused" {
			blocked = true
		}
		if m.Status == "resolved" {
			resolved++
			if m.CorpAction {
				corp++
			}
		}
		if m.Status == "refused_final" {
			refused++
		}
	}
	if blocked || len(pending) > 0 || len(members) > 0 && float64(refused)*100 > 5*float64(len(members)) {
		return "pending"
	}
	if resolved-corp < 30 {
		return "small"
	}
	return "mature"
}

func TestPendingReasonsDoNotChangeState(t *testing.T) {
	rng := rand.New(rand.NewSource(1701))
	seen := map[string]int{}
	for fixture := 0; fixture < 500; fixture++ {
		var members, pending []*groupSet
		for i, n := 0, rng.Intn(45); i < n; i++ {
			status := []string{markResolved, markPending, markRefused, markRefusedFinal}[rng.Intn(4)]
			if fixture%4 == 0 {
				status = markResolved
			}
			gs := &groupSet{ID: int64(i + 1), MadeOn: "2026-10-18", MadeAt: "2026-10-18T16:00:00+08:00", S0Date: "2026-10-19", HorizonStd: true,
				Marks: map[string]PredictionMark{"d5": {Checkpoint: "d5", Status: status, CorpAction: rng.Intn(5) == 0}}}
			if fixture%4 == 0 {
				gs.Marks["d5"] = PredictionMark{Checkpoint: "d5", Status: markResolved}
			}
			if fixture%4 != 0 && rng.Intn(15) == 0 {
				pending = append(pending, gs)
			} else {
				members = append(members, gs)
			}
		}
		end := "2026-09-30"
		if fixture%5 == 0 {
			end = "2026-10-31"
		}
		want := explainFrozenCellState(members, pending, "d5", end, "2026-10-26")
		seen[want]++
		state, _, _, _, _, _ := forecastCellGate(members, pending, "d5", end, "2026-10-26")
		cell := &forecastCell{State: state}
		got := forecastCellWithPendingReasons(cell, members, pending, "d5", dateAtClose("2026-10-26"), nil)
		if state != want || got.State != want || cell.State != want {
			t.Fatalf("fixture %d changed state: %s/%s/%s, want %s", fixture, state, got.State, cell.State, want)
		}
	}
	for _, state := range []string{"open", "pending", "small", "mature"} {
		if seen[state] == 0 {
			t.Errorf("randomized oracle never exercised %s", state)
		}
	}
}

func TestExternalAllGetsNoExplainVolume(t *testing.T) {
	flag := true
	resp := forecastGroupsResponse{Groups: []forecastGroup{{Cells: map[string]*forecastCell{
		"d5":  {State: "mature", PendingReasons: map[string]int{predPhaseResolved: 32}, FailedCapExceeded: &flag, NotBefore: "2026-10-26", Expected: true},
		"d20": {State: "pending", PendingReasons: map[string]int{predPhaseWaiting: 32}, FailedCapExceeded: &flag, NotBefore: "2026-11-26", Expected: true},
	}}}}
	resp.ForecastGroupsMetadata = &ForecastGroupsMetadata{Summary: forecastGroupsSummary{Sets: 32}, MinN: 30, GeneratedAt: "2026-10-22T00:00:00Z"}
	before, _ := json.Marshal(resp)
	protected := forecastReaderGroups(resp, forecastDecision{Scope: scopeAll, External: true})
	wire, err := json.Marshal(protected)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"summary", "rtypes", "min_n", "limits", "generated_at", "ledger_start", "capabilities", "evaluator"} {
		if _, present := fields[key]; present {
			t.Errorf("external all received response-level %s", key)
		}
	}
	for _, group := range protected.Groups {
		for _, cell := range group.Cells {
			if cell.PendingReasons != nil || cell.FailedCapExceeded != nil || cell.NotBefore != "" || cell.Expected {
				t.Fatalf("external all received explanation volume: %+v", cell)
			}
		}
	}
	own := forecastReaderGroups(resp, forecastDecision{Scope: scopeOwn, External: true})
	if own.ForecastGroupsMetadata == nil || own.Summary.Sets != 32 {
		t.Fatal("own reader lost response metadata")
	}
	if own.Groups[0].Cells["d20"].PendingReasons[predPhaseWaiting] != 32 {
		t.Fatal("own reader lost its explanation")
	}
	after, _ := json.Marshal(resp)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("explanation shaping changed the source response")
	}
}

func TestPendingReasonsAccountForUnreadableDates(t *testing.T) {
	sets := []*groupSet{{MadeAt: "broken", MadeOn: "broken", Marks: map[string]PredictionMark{"d5": {Checkpoint: "d5", Status: markPending}}}}
	got := forecastCellWithPendingReasons(&forecastCell{State: "pending"}, sets, nil, "d5", time.Now(), nil)
	if got.PendingReasons["unknown"] != 1 || got.NotBefore != "" {
		t.Fatalf("unreadable timing was silently dropped or invented: %+v", got)
	}
}

func TestPendingReasonsUseFrozenS0FromBothStores(t *testing.T) {
	check := func(t *testing.T, s *Server) {
		t.Helper()
		id := seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-10-18", S0Fen: ip(10000), Marks: map[string]gsMark{"d5": {Status: markPending}}})
		if _, err := s.st.exec(`UPDATE prediction_sets SET s0_date=? WHERE id=?`, "2026-10-20", id); err != nil {
			t.Fatal(err)
		}
		sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31", forecastPopulation{Key: "all"})
		if err != nil || len(sets) != 1 {
			t.Fatalf("load: %d rows, %v", len(sets), err)
		}
		got := forecastCellWithPendingReasons(&forecastCell{State: "pending"}, sets, nil, "d5", dateAtClose("2026-10-26"), nil)
		if got.NotBefore != "2026-10-27" || got.PendingReasons[predPhaseWaiting] != 1 {
			t.Fatalf("frozen baseline was replaced: %+v", got)
		}
	}
	t.Run("sqlite", func(t *testing.T) { check(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "prediction_purges", "reports")
		check(t, &Server{st: st})
	})
}

func TestPendingReasonsH12OnlyStandardHorizons(t *testing.T) {
	standard := &groupSet{MadeOn: "2026-10-18", MadeAt: "2026-10-18T16:00:00+08:00", S0Date: "2026-10-19", HorizonStd: true,
		Marks: map[string]PredictionMark{"h12": {Checkpoint: "h12", Status: markPending}}}
	nonstandard := &groupSet{MadeOn: standard.MadeOn, MadeAt: standard.MadeAt, S0Date: standard.S0Date,
		Marks: map[string]PredictionMark{"h12": {Checkpoint: "h12", Status: markRefusedFinal}}}
	got := forecastCellWithPendingReasons(&forecastCell{State: "pending"}, []*groupSet{standard, nonstandard}, []*groupSet{nonstandard}, "h12", dateAtClose("2026-10-26"), nil)
	total := 0
	for _, n := range got.PendingReasons {
		total += n
	}
	if total != 1 || got.PendingReasons[predPhaseFinal] != 0 || got.FailedCapExceeded == nil || *got.FailedCapExceeded {
		t.Fatalf("nonstandard horizon changed the explanation: %+v", got)
	}
}

func TestPendingReasonsUseClassifiedRoutePopulation(t *testing.T) {
	s := routeTableServer(t)
	sampleMembers(t, s, 35, nil)
	seedGroupSet(t, s, gsSeed{Symbol: symbolFor(0), MadeOn: "2025-06-11", S0Fen: ip(10000)})
	seedGroupSet(t, s, gsSeed{Symbol: "600099", MadeOn: "2025-06-11", BaseCheck: baseCheckPending})
	group := findGroup(t, doGroups(t, s, "/?mode=month"), "2025-06")
	cell := group.Cells["d20"]
	total := 0
	for _, n := range cell.PendingReasons {
		total += n
	}
	if total != 36 || group.Coverage.Duplicate != 1 || group.Coverage.PendingBaseline != 1 {
		t.Fatalf("explanation does not use classified members and baseline candidates: count=%d, group=%+v", total, group)
	}
}
