package app

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExternalAllHasNoVolume(t *testing.T) {
	n := 42
	v := .4
	cell := &forecastCell{State: "mature", N: &n, Resolved: &n, Total: &n, Refused: &n, CorpAction: &n,
		SpreadBp: &v, Hit: &v, HitN: &n, IC: &v, ICN: &n, Band: &v, BandN: &n, Calib: &v, CalibN: &n,
		Slices: []forecastSlice{{PolicyVersion: "policy", forecastCell: forecastCell{State: "mature", N: &n, HitN: &n, Hit: &v}}}}
	resp := forecastGroupsResponse{OK: true, Unrecorded: n, Groups: []forecastGroup{{
		Coverage: forecastCoverage{Reports: n, Member: n}, Cells: map[string]*forecastCell{
			"d5": cell, "d20": {State: "pending", Resolved: &n, Total: &n}, "d60": {State: "small", N: &n},
		},
	}}}
	before, _ := json.Marshal(resp)
	marshal := func(d forecastDecision) map[string]any {
		t.Helper()
		b, err := json.Marshal(forecastReaderGroups(resp, d))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	external := marshal(forecastDecision{Scope: scopeAll, External: true})
	if _, ok := external["unrecorded"]; ok {
		t.Fatal("reader received unrecorded")
	}
	g := external["groups"].([]any)[0].(map[string]any)
	if _, ok := g["coverage"]; ok {
		t.Fatal("external all received coverage")
	}
	cells := g["cells"].(map[string]any)
	var assertNoCounts func(map[string]any)
	assertNoCounts = func(c map[string]any) {
		t.Helper()
		for _, key := range []string{"n", "resolved", "total", "refused", "corp_action", "ic_n", "hit_n", "band_n", "calib_n"} {
			if _, ok := c[key]; ok {
				t.Errorf("external all received %s", key)
			}
		}
		if slices, ok := c["slices"].([]any); ok {
			for _, slice := range slices {
				assertNoCounts(slice.(map[string]any))
			}
		}
	}
	for _, raw := range cells {
		assertNoCounts(raw.(map[string]any))
	}
	if c := cells["d5"].(map[string]any); c["hit"] != v || c["min_n_met"] != true {
		t.Fatal(c)
	}
	for _, key := range []string{"d20", "d60"} {
		if c := cells[key].(map[string]any); len(c) != 1 {
			t.Fatalf("non-mature cell has more than state: %v", c)
		}
	}
	for _, d := range []forecastDecision{{Scope: scopeOwn, External: true}, {Scope: scopeAll}} {
		m := marshal(d)
		g := m["groups"].([]any)[0].(map[string]any)
		if _, ok := g["coverage"]; !ok {
			t.Fatal("own or internal reader lost coverage")
		}
		c := g["cells"].(map[string]any)["d5"].(map[string]any)
		if c["n"] != float64(n) {
			t.Fatal("own or internal reader lost n")
		}
	}
	after, _ := json.Marshal(resp)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("redaction mutated shared cached cells")
	}
}
