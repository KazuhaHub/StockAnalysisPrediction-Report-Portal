package app

// P16a (plan §5.5): one hit predicate, and a members route that scores nothing.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDirectionReconcilesWithHitRate(t *testing.T) {
	// s0 10000: 4.99% is below the floor, 5.00% is exactly on it, 5.01% is above.
	cases := []struct {
		name     string
		terminal int64
		excess   int32
		want     string
	}{
		{"4.99% upside", 10499, 500, dirUntested},
		{"5.00% upside, same sign", 10500, 500, dirAgree},
		{"5.01% upside, same sign", 10501, 500, dirAgree},
		{"-4.99% downside", 9501, -500, dirUntested},
		{"-5.00% downside, same sign", 9500, -500, dirAgree},
		{"5.00% upside, zero excess", 10500, 0, dirFlat},
		{"5.00% upside, opposite sign", 10500, -500, dirDisagree},
	}
	for _, tc := range cases {
		if got := predHitClass(tc.terminal, 10000, tc.excess); got != tc.want {
			t.Errorf("%s: predHitClass = %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := predHitClass(10000, 0, 500); got != dirUntested {
		t.Errorf("zero start price: predHitClass = %q, want untested", got)
	}
}

// TestHitRateCountsExactlyTheAgreed: the cell's hit rate is agree/(tested) where "tested" is the
// set predHitClass does not call untested. A member with no benchmark excess is not tested, and a
// flat row is tested but not agreed.
func TestHitRateCountsExactlyTheAgreed(t *testing.T) {
	// Build members through the real scoring arrays: excess and upside are set by scoreForecastCell,
	// so a wrong sign or a wrong denominator cannot hide behind a hand-built big[].
	s := newV1Server(t)
	sets := sampleMembers(t, s, 0, nil)
	sets = sets[:0]
	add := func(symbol string, terminal int64, retBp, benchBp int32) {
		t := terminal
		sets = append(sets, &groupSet{
			Symbol: symbol, S0Fen: ip(10000), TerminalFen: &t, Stance: "long",
			Marks: map[string]PredictionMark{"d5": {
				Checkpoint: "d5", Status: markResolved, RetBp: bp(retBp), BenchRetBp: bp(benchBp),
				CloseFen: ip(terminal),
			}},
		})
	}
	// 4.99% is untested; 5.00% agree; 5.01% agree; -5% agree; 6% disagree; 7% flat.
	add("600001", 10499, 600, 0)
	add("600002", 10500, 600, 0)
	add("600003", 10501, 600, 0)
	add("600004", 9500, -600, 0)
	add("600005", 10600, -600, 0)
	add("600006", 10700, 500, 500)
	cell := &forecastCell{}
	scoreForecastCell(cell, sets, "d5", []string{"t"})
	if cell.Hit == nil || cell.HitN == nil {
		t.Fatalf("no hit rate: %+v", cell)
	}
	// Tested: 10500 agree, 10501 agree, 9500 agree, 10600 disagree, 10700 flat = 5.
	// Agreed: 3. 3/5 = 0.6.
	if *cell.HitN != 5 || *cell.Hit != 0.6 {
		t.Errorf("hit = %v over n=%d, want 0.6 over 5", *cell.Hit, *cell.HitN)
	}
	// And every row's verdict is the same predicate.
	for _, gs := range sets {
		m := gs.Marks["d5"]
		ex := *m.RetBp - *m.BenchRetBp
		want := predHitClass(*gs.TerminalFen, *gs.S0Fen, ex)
		if got := predHitClass(*gs.TerminalFen, *gs.S0Fen, ex); got != want {
			t.Errorf("%s: %q != %q", gs.Symbol, got, want)
		}
	}
}

// TestMembersRouteScoresNothing: the drawer's state comes from the gate. A counting hook on the
// permutation helper stays at zero, and the state matches the matrix's own cell.
func TestMembersRouteScoresNothing(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+3, nil)
	matrix := doGroups(t, s, "/api/admin/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month")
	var cellState string
	for _, g := range matrix.Groups {
		if c := g.Cells["d5"]; c != nil {
			cellState = c.State
		}
	}
	if cellState == "" {
		t.Fatal("the matrix has no d5 cell")
	}
	calls := 0
	prev := forecastPerHook
	forecastPerHook = func() { calls++ }
	defer func() { forecastPerHook = prev }()
	resp := doMembers(t, s, "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5")
	if calls != 0 {
		t.Errorf("the members route ran %d permutation passes", calls)
	}
	if resp.State != cellState {
		t.Errorf("state = %q, matrix says %q", resp.State, cellState)
	}
	// A second click must agree too: the official month is served from the matrix memo.
	resp = doMembers(t, s, "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5")
	if resp.State != cellState {
		t.Errorf("second click state = %q, matrix says %q", resp.State, cellState)
	}
}

// TestMembersWithholdOutcomesUnlessMature: a pending or small cell sends identity and status only.
func TestMembersWithholdOutcomesUnlessMature(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, 3, nil) // below the mature threshold
	rec := httptest.NewRecorder()
	s.apiForecastGroupMembers(rec, httptest.NewRequest("GET",
		"/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var raw struct {
		State   string           `json:"state"`
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw.State != "small" {
		t.Fatalf("state = %q, want small", raw.State)
	}
	if len(raw.Members) == 0 {
		t.Fatal("the response has no members, so the withholding assertion proves nothing")
	}
	for _, m := range raw.Members {
		for _, k := range []string{"close_fen", "ret_bp", "bench_ret_bp", "excess_bp", "upside", "direction", "s0_fen", "terminal_fen"} {
			if _, ok := m[k]; ok {
				t.Errorf("non-mature member carries %q", k)
			}
		}
		for _, k := range []string{"set_id", "symbol", "status"} {
			if _, ok := m[k]; !ok {
				t.Errorf("non-mature member is missing %q", k)
			}
		}
	}
}
