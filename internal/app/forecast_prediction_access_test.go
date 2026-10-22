package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestInternalPredictionReaderDiagnosticsAndMembership(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	for _, scope := range []string{scopeAll, scopeOwn} {
		seedForecastReader(t, s, scope, scope, false)
	}
	mustAddUser(t, s, "admin", true)
	report, accepted := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")
	_, rejected := seedLedgerFor(t, s, "600160", "2026-10-18", "# rejected")
	if _, err := s.st.exec(`UPDATE prediction_sets SET errors='[{"reason":"test"}]' WHERE id=?`, accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec("UPDATE prediction_sets SET state=? WHERE id=?", predStateRejected, rejected); err != nil {
		t.Fatal(err)
	}
	orphan, _ := seedLedgerFor(t, s, "600160", "2026-10-19", "# orphan")
	if _, err := s.st.DeleteReport(orphan); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{scopeAll, scopeOwn, "admin"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/report/x/predictions?include=payload", nil)
		r.SetPathValue("id", itoa64(report))
		s.apiReportPredictions(w, r, user)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		set := out["sets"].([]any)[0].(map[string]any)
		if _, ok := set["payload"]; !ok {
			t.Fatal("internal diagnostic payload missing")
		}
		_, hasErrors := set["errors"]
		if hasErrors != (user == "admin") {
			t.Errorf("%s error diagnostics mismatch", user)
		}
		_, hasMember := out["counted_set_id"]
		if hasMember != (user != scopeOwn) {
			t.Errorf("%s membership mismatch", user)
		}
		w = httptest.NewRecorder()
		s.apiQueryPredictions(w, httptest.NewRequest("GET", "/api/predictions?symbol=600160", nil), user)
		var list struct {
			Sets []PredictionSet `json:"sets"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		want := 2
		if user == "admin" {
			want = 3
		}
		if len(list.Sets) != want {
			t.Errorf("%s nonaccepted rows: %d want %d", user, len(list.Sets), want)
		}
		// The same policy must apply to a cookie on the otherwise machine-classed surface.
		vw := httptest.NewRecorder()
		vr := sessionForPath(t, s, user, "GET /api/v1/reports/{id}/predictions", "/api/v1/reports/"+itoa64(report)+"/predictions?include=payload")
		vr.SetPathValue("id", itoa64(report))
		s.v1ReportPredictions(vw, vr)
		var vout map[string]any
		if err := json.Unmarshal(vw.Body.Bytes(), &vout); err != nil {
			t.Fatal(err)
		}
		if vw.Code != 200 || !reflect.DeepEqual(out, vout) {
			t.Fatalf("%s v1 cookie shape diverged: %d %s", user, vw.Code, vw.Body.String())
		}
		vw = httptest.NewRecorder()
		s.v1QueryPredictions(vw, sessionForPath(t, s, user, "GET /api/v1/predictions", "/api/v1/predictions?symbol=600160"))
		var vl struct {
			Sets []PredictionSet `json:"sets"`
		}
		if err := json.Unmarshal(vw.Body.Bytes(), &vl); err != nil {
			t.Fatal(err)
		}
		if vw.Code != 200 || !reflect.DeepEqual(list, vl) {
			t.Fatalf("%s v1 cookie list diverged: %d %s", user, vw.Code, vw.Body.String())
		}
	}
}

func TestExternalPredictionScopeChangeFailsClosed(t *testing.T) {
	for _, change := range []string{"delete", "unrestrict"} {
		t.Run(change, func(t *testing.T) {
			s := newV1Server(t)
			seedForecastReader(t, s, "external", scopeAll, true)
			report, _ := seedLedgerFor(t, s, "600160", "2026-10-17", "# unreadable")
			decision := s.forecastAccess("external")
			if !decision.External || s.canReadReport(report, s.viewerScope("external")) {
				t.Fatal("fixture must be external and unable to read the report")
			}
			query := "DELETE FROM users WHERE username=?"
			if change == "unrestrict" {
				query = "UPDATE users SET restricted=0 WHERE username=?"
			}
			if _, err := s.st.exec(query, "external"); err != nil {
				t.Fatal(err)
			}
			// Deterministic interleaving between the strict decision and the legacy report scope.
			scope := s.viewerScope("external")
			if scope != nil {
				t.Fatal("interleaving did not produce an unscoped legacy read")
			}
			view := forecastPredictionViewFor(decision, scope)
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/report/x/predictions", nil)
			r.SetPathValue("id", itoa(report))
			s.serveReportPredictionsView(w, r, view)
			if w.Code != http.StatusNotFound {
				t.Errorf("external decision became unscoped report read: %d %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			s.serveQueryPredictionsView(w, httptest.NewRequest("GET", "/api/predictions?symbol=600160", nil), view)
			var out struct {
				Sets []PredictionSet `json:"sets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || len(out.Sets) != 0 {
				t.Errorf("external decision became unscoped list read: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestProtectedPredictionFilterBeforeLimit(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			st := newTestStore(t)
			if driver == "postgres" {
				st = pgStore(t, "prediction_sets", "reports")
			}
			report, _, err := st.UpsertReport(Rep{Symbol: "600160", Date: "2026-10-17", RType: predTestRtype, MD: "body"})
			if err != nil {
				t.Fatal(err)
			}
			accepted := seedPredictionSet(t, st, sql.NullInt64{Int64: report, Valid: true}, "accepted")
			rejected := seedPredictionSet(t, st, sql.NullInt64{Int64: report, Valid: true}, "rejected")
			if _, err := st.exec("UPDATE prediction_sets SET state=? WHERE id=?", predStateRejected, rejected); err != nil {
				t.Fatal(err)
			}
			seedPredictionSet(t, st, sql.NullInt64{}, "orphan")
			sets, err := st.QueryPredictionSets(PredictionFilter{RType: predTestRtype, Limit: 1, AcceptedOnly: true, RequireReport: true}, nil)
			if err != nil || len(sets) != 1 || sets[0].ID != accepted {
				t.Fatalf("protected filtering after LIMIT: %#v %v", sets, err)
			}
			all, err := st.QueryPredictionSets(PredictionFilter{RType: predTestRtype, Limit: 1}, nil)
			if err != nil || len(all) != 1 || all[0].ReportID != nil {
				t.Fatalf("unrestricted control: %#v %v", all, err)
			}
		})
	}
}

func TestPredictionCookieRoutesRequireForecastScope(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	for _, scope := range []string{scopeNone, scopeAll, scopeOwn} {
		seedForecastReader(t, s, scope, scope, false)
	}
	report, _ := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	for _, path := range []string{"/api/report/" + itoa(report) + "/predictions", "/api/predictions?symbol=600160", "/api/v1/reports/" + itoa(report) + "/predictions", "/api/v1/predictions?symbol=600160"} {
		for _, scope := range []string{scopeNone, scopeAll, scopeOwn} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, sessionForPath(t, s, scope, "GET "+path, path))
			if scope == scopeNone {
				if w.Code != http.StatusForbidden || !hasCode(w.Body.Bytes(), "forecast_unavailable") {
					t.Errorf("%s admitted none: %d %s", path, w.Code, w.Body.String())
				}
			} else if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"state":"accepted"`) {
				t.Errorf("%s refused %s: %d %s", path, scope, w.Code, w.Body.String())
			}
		}
	}
}

func TestExternalPredictionReadsFilterBeforeLimitAndHideDiagnostics(t *testing.T) {
	s := newV1Server(t)
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	seedForecastReader(t, s, "external", scopeAll, true)
	if err := s.st.SaveVersion(ReportVersion{Name: "default", Visibility: VisibilityAll}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("default", []string{userPrincipal("external")}); err != nil {
		t.Fatal(err)
	}
	report, accepted := seedLedgerFor(t, s, "600160", "2026-10-17", "# accepted")
	if _, err := s.st.exec(`UPDATE prediction_sets SET errors='[{"reason":"test"}]' WHERE id=?`, accepted); err != nil {
		t.Fatal(err)
	}
	_, rejected := seedLedgerFor(t, s, "600160", "2026-10-18", "# rejected")
	if _, err := s.st.exec(`UPDATE prediction_sets SET state='rejected', errors='[{"reason":"test"}]' WHERE id=?`, rejected); err != nil {
		t.Fatal(err)
	}
	orphan, _ := seedLedgerFor(t, s, "600160", "2026-10-19", "# orphan")
	if _, err := s.st.DeleteReport(orphan); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	for _, path := range []string{"/api/report/" + itoa(report) + "/predictions?include=payload", "/api/predictions?symbol=600160&include=payload&limit=1", "/api/v1/reports/" + itoa(report) + "/predictions?include=payload", "/api/v1/predictions?symbol=600160&include=payload&limit=1"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, "external", "GET "+path, path))
		var out struct {
			Sets    []map[string]any `json:"sets"`
			Counted *int64           `json:"counted_set_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(out.Sets) != 1 || out.Sets[0]["id"] != float64(accepted) {
			t.Fatalf("%s lost readable accepted row behind later rejected/orphan rows: %d %s", path, w.Code, w.Body.String())
		}
		if out.Counted != nil {
			t.Errorf("%s leaked membership pointer", path)
		}
		for _, field := range []string{"payload", "errors", "counted_in_group", "membership", "attributed_ou"} {
			if _, ok := out.Sets[0][field]; ok {
				t.Errorf("%s leaked %s", path, field)
			}
		}
	}
	// Persistent machine callers retain diagnostic states and purged sets.
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/predictions?symbol=600160&include=payload", nil)
	r.Header.Set("Authorization", "Bearer tok-all")
	mux.ServeHTTP(w, r)
	var machine struct {
		Sets []map[string]any `json:"sets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &machine); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(machine.Sets) != 3 {
		t.Fatalf("machine shape changed: %d %s", w.Code, w.Body.String())
	}
	if _, ok := machine.Sets[0]["payload"]; !ok {
		t.Fatal("machine lost diagnostic payload")
	}
}
