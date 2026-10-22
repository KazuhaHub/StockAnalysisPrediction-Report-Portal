package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"
)

func seedReadableMembershipReport(t *testing.T, s *Server, g gsSeed, title string) (int64, int64) {
	t.Helper()
	reportID, _, err := s.st.UpsertReport(Rep{Symbol: g.Symbol, Date: g.MadeOn, RType: predTestRtype,
		Title: title, MD: "# original"})
	if err != nil {
		t.Fatal(err)
	}
	setID := seedGroupSet(t, s, g)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,body_sha256=? WHERE id=?`, reportID, sha256Hex([]byte("# original\x00")), setID); err != nil {
		t.Fatal(err)
	}
	return reportID, setID
}

func TestReadSetSelectionSurvivesRevisionLimit(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertReadSelectionBeyondLimit(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "prediction_purges", "reports")
		if _, err := st.exec(`DELETE FROM meta WHERE k=?`, setForecastPopulationFrom); err != nil {
			t.Fatal(err)
		}
		assertReadSelectionBeyondLimit(t, &Server{st: st})
	})
}

func assertReadSelectionBeyondLimit(t *testing.T, s *Server) {
	t.Helper()
	g := gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852"}
	report, first := seedReadableMembershipReport(t, s, g, "Many revisions")
	if _, err := s.st.exec(`UPDATE prediction_sets SET body_sha256=? WHERE id=?`, sha256Hex([]byte("# old body\x00")), first); err != nil {
		t.Fatal(err)
	}
	var matched int64
	for i := 1; i <= 220; i++ {
		id := seedGroupSet(t, s, g)
		hash := sha256Hex([]byte("# different body\x00"))
		if i == 1 {
			matched, hash = id, sha256Hex([]byte("# original\x00"))
		}
		if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,payload_sha256=?,body_sha256=? WHERE id=?`, report, fmt.Sprint("revision-", i), hash, id); err != nil {
			t.Fatal(err)
		}
	}
	_, out := readSemanticReport(t, s, report, nil)
	if out["display_set_id"] != float64(matched) || out["counted_set_id"] != float64(first) {
		t.Fatalf("pagination silently changed display/counting decisions: %v", out)
	}
	sets := out["sets"].([]any)
	if len(sets) > 200 {
		t.Fatalf("selection expanded the response past its cap: %d", len(sets))
	}
	seen := map[int64]bool{}
	for _, raw := range sets {
		seen[int64(raw.(map[string]any)["id"].(float64))] = true
	}
	if !seen[first] || !seen[matched] {
		t.Fatal("selected sets are not present for the reading card")
	}
}

func TestSelectedPredictionLookupCannotCrossReports(t *testing.T) {
	s := newV1Server(t)
	g := gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852"}
	readable, _ := seedReadableMembershipReport(t, s, g, "Readable")
	g.Symbol = "600002"
	_, hidden := seedReadableMembershipReport(t, s, g, "Not readable")
	if err := s.st.AddReportViewer(readable, g.MadeOn, "reader", 0); err != nil {
		t.Fatal(err)
	}
	sc := &ownerScope{self: userPrincipal("reader"), principals: []string{userPrincipal("reader")}, versOwner: []string{"default"}}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{ReportID: readable}, sc)
	if err != nil || len(sets) != 1 {
		t.Fatalf("readable report fixture: %+v / %v", sets, err)
	}
	for _, scope := range []*ownerScope{nil, sc} {
		if extra, err := s.st.includeSelectedPredictionSets(readable, sets, hidden, 0, false, scope); err == nil || extra != nil {
			// Even an unrestricted reader must not attach another report's set to this card.
			t.Fatalf("exact lookup crossed report identity: %+v / %v", extra, err)
		}
	}
}

func TestMembershipLoadErrorOmitsMetadataButKeepsReport(t *testing.T) {
	s := newV1Server(t)
	report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852"}, "Decision")
	if err := s.st.SetSetting(setForecastPopulationFrom, "damaged-instant"); err != nil {
		t.Fatal(err)
	}
	data, out := readSemanticReport(t, s, report, nil)
	if len(out["sets"].([]any)) != 1 || out["display_set_id"] == nil {
		t.Fatalf("optional membership failure suppressed the report's prediction: %s", data)
	}
	if bytes.Contains(data, []byte(`"membership"`)) || bytes.Contains(data, []byte(`"counted_set_id"`)) {
		t.Fatalf("load failure fabricated a global membership result: %s", data)
	}
}

func TestReadUpsideBPTruncatesWithoutOverflow(t *testing.T) {
	for _, tc := range []struct {
		terminal, base int64
		want           *int64
	}{
		{20000, 30000, ip(-3333)}, {40000, 30000, ip(3333)}, {30000, 30000, ip(0)},
		{math.MaxInt64, math.MaxInt64 - 1, ip(0)}, {1, math.MaxInt64, ip(-9999)},
		{math.MaxInt64, 1, nil}, {1, 0, nil}, {-1, 100, nil},
	} {
		got := readUpsideBP(tc.terminal, tc.base)
		if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
			t.Errorf("upside(%d,%d) = %v, want %v", tc.terminal, tc.base, got, tc.want)
		}
	}
}

func readSemanticReport(t *testing.T, s *Server, reportID int64, sc *ownerScope) ([]byte, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/report/"+itoa(reportID)+"/predictions", nil)
	r.SetPathValue("id", itoa(reportID))
	s.serveReportPredictions(w, r, sc)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("report prediction response: %d %s / %v", w.Code, w.Body.String(), err)
	}
	return append([]byte(nil), w.Body.Bytes()...), out
}

func TestMembershipOnReadEqualsGroupStatistics(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		if st.driver == "postgres" {
			// This suite's Postgres database is shared sequentially; unrelated ledger fixtures
			// must not decide this test's cohort benchmark or collide with its report IDs.
			for _, table := range []string{"prediction_marks", "prediction_claims", "prediction_sets", "prediction_purges"} {
				if _, err := st.exec("DELETE FROM " + table); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.exec(`DELETE FROM meta WHERE k=?`, setForecastPopulationFrom); err != nil {
				t.Fatal(err)
			}
		}
		s := &Server{st: st}
		g := gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852", S0Fen: ip(30000), TerminalFen: ip(20000)}
		firstReport, firstSet := seedReadableMembershipReport(t, s, g, "First decision")
		g.MadeOn = "2026-01-06"
		secondReport, secondSet := seedReadableMembershipReport(t, s, g, "Later decision")
		sets, err := st.loadForecastGroupSets(predTestRtype, "2026-01-01", "2026-01-31", forecastPopulation{Key: "all"})
		if err != nil {
			t.Fatal(err)
		}
		members, _, _ := forecastMembership(sets, time.Time{}, "sh000852")
		if len(members) != 1 || members[0].ID != firstSet {
			t.Fatal("fixture does not have exactly the first set as its member")
		}
		for _, tc := range []struct {
			report, set int64
			membership  string
			counted     int64
		}{
			{firstReport, firstSet, "member", firstSet}, {secondReport, secondSet, "duplicate", 0},
		} {
			_, out := readSemanticReport(t, s, tc.report, nil)
			if out["counted_set_id"] != float64(tc.counted) || out["display_set_id"] != float64(tc.set) {
				t.Fatalf("display/counted identity disagrees with full cohort: %v", out)
			}
			got := out["sets"].([]any)[0].(map[string]any)
			if got["membership"] != tc.membership || got["report_date"] == nil || got["upside_bp"] != float64(-3333) {
				t.Fatalf("missing or incorrect read semantics: %v", got)
			}
		}
		w := httptest.NewRecorder()
		s.serveQueryPredictions(w, httptest.NewRequest("GET", "/api/predictions?symbol=600001&limit=1", nil), nil)
		var latest struct {
			Sets []PredictionSet `json:"sets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &latest); err != nil || w.Code != 200 || len(latest.Sets) != 1 ||
			latest.Sets[0].ID != secondSet || latest.Sets[0].Membership != membershipDuplicate {
			t.Fatalf("paginated symbol list changed cohort membership: %d %s / %v", w.Code, w.Body.String(), err)
		}
	})
}

func TestDisplayAndCountedSetIDs(t *testing.T) {
	s := newV1Server(t)
	g := gsSeed{Symbol: "600001", MadeOn: "2026-01-05", Bench: "sh000852"}
	report, first := seedReadableMembershipReport(t, s, g, "Decision")
	g.MadeOn = "2026-01-06"
	second := seedGroupSet(t, s, g)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,body_sha256=?,payload_sha256='second-revision' WHERE id=?`, report, sha256Hex([]byte("# revision\x00")), second); err != nil {
		t.Fatal(err)
	}
	_, got := readSemanticReport(t, s, report, nil)
	if got["display_set_id"] != float64(first) || got["counted_set_id"] != float64(first) {
		t.Fatalf("matching older body was not selected: %v", got)
	}
	if _, err := s.st.exec(`UPDATE reports SET body_md='# unrelated' WHERE id=?`, report); err != nil {
		t.Fatal(err)
	}
	_, got = readSemanticReport(t, s, report, nil)
	if got["display_set_id"] != float64(second) || got["counted_set_id"] != float64(first) {
		t.Fatalf("body-changed fallback replaced the counted decision: %v", got)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET base_check=? WHERE id=?`, baseMismatch, first); err != nil {
		t.Fatal(err)
	}
	_, got = readSemanticReport(t, s, report, nil)
	if got["counted_set_id"] != float64(0) {
		t.Fatalf("later revision replaced a mismatched first decision: %v", got)
	}
}

func TestRestrictedViewerGetsNoMembership(t *testing.T) {
	s := newV1Server(t)
	g := gsSeed{Symbol: "600001", MadeOn: "2026-01-06", Bench: "sh000852"}
	report, _ := seedReadableMembershipReport(t, s, g, "Readable decision")
	if err := s.st.AddReportViewer(report, g.MadeOn, "outsider", 0); err != nil {
		t.Fatal(err)
	}
	sc := &ownerScope{self: userPrincipal("outsider"), principals: []string{userPrincipal("outsider")}, versOwner: []string{"default"}}
	before, _ := readSemanticReport(t, s, report, sc)
	g.MadeOn = "2026-01-05"
	seedReadableMembershipReport(t, s, g, "Other tenant's earlier decision")
	after, _ := readSemanticReport(t, s, report, sc)
	if !bytes.Equal(before, after) {
		t.Fatal("another tenant's earlier prediction changed a restricted response")
	}
	for _, key := range [][]byte{[]byte(`"membership"`), []byte(`"counted_set_id"`)} {
		if bytes.Contains(after, key) {
			t.Fatalf("restricted response carries global metadata: %s", after)
		}
	}
}
