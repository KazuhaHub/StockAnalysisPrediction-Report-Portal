package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// serveReportPredictionsFor runs the per-report read under an explicit view, the way the browser
// route does after it has decided who the reader is.
func serveReportPredictionsFor(t *testing.T, s *Server, reportID int64, view forecastPredictionView) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/report/"+itoa(reportID)+"/predictions", nil)
	r.SetPathValue("id", itoa(reportID))
	s.serveReportPredictionsView(w, r, view)
	return w
}

// The reading card must be able to tell a covered report that made no forecast from a report type
// the ledger never covers: the first earns the explicit empty state, the second earns nothing.
func TestReportPredictionsSayWhetherTheLedgerCoversTheReport(t *testing.T) {
	eachReaderServer(t, func(t *testing.T, s *Server) {
		covered, _, err := s.st.UpsertReport(Rep{Symbol: "600001", Date: "2026-09-17", RType: predTestRtype, Title: "Covered", MD: "# covered"})
		if err != nil {
			t.Fatal(err)
		}
		uncovered, _, err := s.st.UpsertReport(Rep{Symbol: "600001", Date: "2026-09-17", RType: "Weekly digest", Title: "Uncovered", MD: "# uncovered"})
		if err != nil {
			t.Fatal(err)
		}
		views := map[string]forecastPredictionView{
			"administrator": {editions: true},
			"reader":        {reader: true, editions: true},
		}
		for name, view := range views {
			for _, tc := range []struct {
				report int64
				want   bool
			}{{covered, true}, {uncovered, false}} {
				w := serveReportPredictionsFor(t, s, tc.report, view)
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
					t.Fatalf("%s view of report %d: %d %s / %v", name, tc.report, w.Code, w.Body.String(), err)
				}
				got, present := out["covered"].(bool)
				if !present || got != tc.want {
					t.Errorf("%s view of report %d: covered = %v (present %v), want %v: %s", name, tc.report, got, present, tc.want, w.Body.String())
				}
			}
		}
		// The persistent machine view keeps its existing shape.
		w := serveReportPredictionsFor(t, s, covered, forecastPredictionView{})
		if w.Code != 200 || bytes.Contains(w.Body.Bytes(), []byte(`"covered"`)) {
			t.Errorf("the machine view must not carry coverage: %d %s", w.Code, w.Body.String())
		}
	})
}

// Coverage is a fact about a report the caller may read. A report the caller cannot read is still a
// 404, and the refusal must not leak its type.
func TestReportPredictionsCoverageIsNotDisclosedForAnUnreadableReport(t *testing.T) {
	eachReaderServer(t, func(t *testing.T, s *Server) {
		report, _, err := s.st.UpsertReport(Rep{Symbol: "600001", Date: "2026-09-17", RType: predTestRtype, Title: "Hidden", MD: "# hidden"})
		if err != nil {
			t.Fatal(err)
		}
		w := serveReportPredictionsFor(t, s, report, forecastPredictionView{scope: &ownerScope{denyAll: true}, protected: true})
		if w.Code != 404 || bytes.Contains(w.Body.Bytes(), []byte(`"covered"`)) {
			t.Fatalf("an unreadable report must be a bare 404, got %d %s", w.Code, w.Body.String())
		}
	})
}
