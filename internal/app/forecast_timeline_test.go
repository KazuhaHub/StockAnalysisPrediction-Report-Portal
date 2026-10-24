package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStockTimelinePredictionsUseReadableAcceptedReports(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertForecastTimeline(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "users")
		s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
		s.names.fetch = func(string) string { return "" }
		assertForecastTimeline(t, s)
	})
}

func assertForecastTimeline(t *testing.T, s *Server) {
	t.Helper()
	seedForecastReader(t, s, "timeline-reader", scopeAll, false)
	seedForecastReader(t, s, "timeline-own", scopeOwn, false)
	seedForecastReader(t, s, "timeline-external", scopeAll, true)
	seedForecastReader(t, s, "timeline-none", scopeNone, false)
	mustAddUser(t, s, "timeline-admin", true)
	if err := s.st.SaveVersion(ReportVersion{Name: "timeline-fixture", Visibility: VisibilityGroup}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteVersion("timeline-fixture"); err != nil {
			t.Error(err)
		}
		if err := s.st.DeleteSetting(setForecastReader); err != nil {
			t.Error(err)
		}
	})
	if err := s.st.SetVersionGrants("timeline-fixture", []string{userPrincipal("timeline-external")}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		report, set := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: date}, fmt.Sprintf("Visible %d", i))
		if err := s.st.SetReportViewers(report, date, []string{userPrincipal("timeline-external")}); err != nil {
			t.Fatal(err)
		}
		want[date] = i%3 == 0
		if !want[date] {
			if _, err := s.st.exec(`UPDATE prediction_sets SET state=? WHERE id=?`, predStateRejected, set); err != nil {
				t.Fatal(err)
			}
			// An accepted but unreadable report on the same day must not mark this viewer's day.
			seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: date}, fmt.Sprintf("Hidden %d", i))
		}
		if i%10 == 0 {
			want[date] = false
			if _, err := s.st.exec(`UPDATE prediction_sets SET rtype=? WHERE symbol=? AND made_on=?`, "uncovered-fixture", "600001", date); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.st.exec(`UPDATE reports SET version=? WHERE symbol=?`, "timeline-fixture", "600001"); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"off", "card", "full", "unknown"} {
		if err := s.st.SetSetting(setForecastReader, stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"timeline-external", "timeline-reader", "timeline-own", "timeline-none", "timeline-admin"} {
			for _, prefetch := range []bool{false, true} {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/api/stock/600001", nil)
				r.SetPathValue("symbol", "600001")
				if prefetch {
					r.Header.Set("X-Prefetch", "1")
				}
				s.apiStock(w, r, user)
				var out struct {
					Timeline []struct {
						Date      string `json:"date"`
						Predicted *bool  `json:"predicted"`
					} `json:"timeline"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || len(out.Timeline) != 60 {
					t.Fatalf("timeline status=%d days=%d: %v", w.Code, len(out.Timeline), err)
				}
				for _, day := range out.Timeline {
					enabled := user == "timeline-admin" || user != "timeline-none" && stage == "full"
					if !enabled {
						if day.Predicted != nil {
							t.Fatalf("stage=%s user=%s disclosed timeline flags", stage, user)
						}
						continue
					}
					index := int(dateAtClose(day.Date).Sub(dateAtClose("2026-06-01")).Hours() / 24)
					predicted := want[day.Date]
					if user != "timeline-external" {
						predicted = index%10 != 0
					}
					if day.Predicted == nil || *day.Predicted != predicted {
						t.Fatalf("stage=%s user=%s prefetch=%v date=%s predicted=%v, want %v", stage, user, prefetch, day.Date, day.Predicted, predicted)
					}
				}
			}
		}
	}
}

func TestStockTimelineLedgerFailureLeavesReportReadable(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "timeline-reader", scopeAll, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-06-01"}, "Readable despite ledger failure")
	if _, err := s.st.exec(`DROP TABLE prediction_sets`); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/stock/600001", nil)
	r.SetPathValue("symbol", "600001")
	w := httptest.NewRecorder()
	s.apiStock(w, r, "timeline-reader")
	var out struct {
		Rep      struct{ ID int64 } `json:"rep"`
		Timeline []map[string]any   `json:"timeline"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Rep.ID != report {
		t.Fatalf("ledger failure hid report: %d %s: %v", w.Code, w.Body.String(), err)
	}
	if len(out.Timeline) != 1 {
		t.Fatal("ledger failure removed report navigation")
	}
	if _, ok := out.Timeline[0]["predicted"]; ok {
		t.Fatal("failed ledger read claimed a known prediction state")
	}
}
