package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestEmptyPredictionQueryReturnsAnArray(t *testing.T) {
	s := newV1Server(t)
	w := httptest.NewRecorder()
	s.serveQueryPredictions(w, httptest.NewRequest("GET", "/?symbol=699999", nil), nil)
	var out struct {
		Sets json.RawMessage `json:"sets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || string(out.Sets) != "[]" {
		t.Fatalf("empty result = %d %s", w.Code, w.Body.String())
	}
}

func TestReadMarkSourceAndSharedDirection(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		s := &Server{st: st}
		for i, tc := range []struct {
			name, status, want string
			terminal           *int64
			ret, bench         *int32
			corp               bool
		}{
			{"agree", markResolved, dirAgree, ip(11000), bp(200), bp(100), false},
			{"disagree", markResolved, dirDisagree, ip(9000), bp(200), bp(100), false},
			{"flat", markResolved, dirFlat, ip(11000), bp(100), bp(100), false},
			{"small upside", markResolved, dirUntested, ip(10499), bp(200), bp(100), false},
			{"exact threshold", markResolved, dirAgree, ip(10500), bp(200), bp(100), false},
			{"no terminal", markResolved, dirUntested, nil, bp(200), bp(100), false},
			{"no benchmark", markResolved, dirUntested, ip(11000), bp(200), nil, false},
			{"corporate action", markResolved, dirUntested, ip(11000), bp(200), bp(100), true},
			{"pending", markPending, "", ip(11000), bp(200), bp(100), false},
			{"final", markRefusedFinal, "", ip(11000), bp(200), bp(100), false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				id := seedGroupSet(t, s, gsSeed{Symbol: ingestSymbol(i), MadeOn: "2026-01-05", S0Fen: ip(10000), TerminalFen: tc.terminal, Marks: map[string]gsMark{"d5": {Status: tc.status, RetBp: tc.ret, BenchRetBp: tc.bench, CorpAction: tc.corp}}})
				if _, err := st.exec(`UPDATE prediction_marks SET source='captured-test-source' WHERE set_id=?`, id); err != nil {
					t.Fatal(err)
				}
				sets, err := st.QueryPredictionSets(PredictionFilter{ID: id}, nil)
				if err != nil || len(sets) != 1 || len(sets[0].Marks) != 1 {
					t.Fatalf("read = %+v / %v", sets, err)
				}
				m := sets[0].Marks[0]
				if m.Source != "captured-test-source" || m.Direction != tc.want {
					t.Fatalf("mark = %+v, want direction %q", m, tc.want)
				}
			})
		}
	})
}

func TestReadSettlementPausedTracksEvaluatorMode(t *testing.T) {
	s := newV1Server(t)
	report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-01-05"}, "Paused settlement")
	for _, mode := range []string{forecastModeOff, forecastModeDry, forecastModeOn, "invalid"} {
		if err := s.st.SetSetting(setForecastAuto, mode); err != nil {
			t.Fatal(err)
		}
		_, out := readSemanticReport(t, s, report, nil)
		if got, ok := out["settlement_paused"].(bool); !ok || got != (mode != forecastModeOn) {
			t.Fatalf("mode %s: response = %v", mode, out)
		}
		w := httptest.NewRecorder()
		s.serveQueryPredictions(w, httptest.NewRequest("GET", "/?symbol=600001", nil), nil)
		var query map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &query); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || query["settlement_paused"] != out["settlement_paused"] {
			t.Fatalf("query route disagrees: %d %s", w.Code, w.Body.String())
		}
	}
}
