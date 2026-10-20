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

// TestDirectionReconcilesWithHitRateAtTheCellLevel: the cell's hit rate is agree/(tested), and it
// must equal what the per-row verdicts imply, at the boundary where the old float rule disagreed.
func TestHitRateCountsExactlyTheAgreed(t *testing.T) {
	// Excess is +1bp for every member, so agreement is decided by the upside sign.
	terminal := []int64{10499, 10500, 10501, 9501, 9500, 10000}
	ups := make([]float64, len(terminal))
	big := make([]bool, len(terminal))
	signUp := make([]int8, len(terminal))
	signEx := make([]int8, len(terminal))
	agree, tested := 0, 0
	for i, term := range terminal {
		cls := predHitClass(term, 10000, 1)
		ups[i] = float64(term)/10000 - 1
		big[i] = cls != dirUntested
		signUp[i] = signOfInt64(term - 10000)
		signEx[i] = 1
		if big[i] {
			tested++
			if cls == dirAgree {
				agree++
			}
		}
	}
	hit, _, n, ok := hitStats(big, signUp, signEx, nil)
	if !ok || n != tested {
		t.Fatalf("n = %d, want %d", n, tested)
	}
	if want := float64(agree) / float64(tested); hit != want {
		t.Errorf("hit = %v, want %v: the row verdicts and the hit rate disagree", hit, want)
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
