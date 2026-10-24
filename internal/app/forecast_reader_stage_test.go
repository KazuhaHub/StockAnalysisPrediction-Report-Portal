package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestReaderStageNeverWidensScope(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "stage-reader", scopeAll, false)
	seedForecastReader(t, s, "stage-none", scopeNone, false)
	mustAddUser(t, s, "stage-admin", true)
	for _, stage := range []string{"off", "card", "full", "broken", ""} {
		if err := s.st.SetSetting(setForecastReader, stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"stage-reader", "stage-none", "stage-admin"} {
			w := httptest.NewRecorder()
			_, ok := s.forecastCookiePredictionView(w, user)
			want := user == "stage-admin" || user == "stage-reader" && (stage == "card" || stage == "full")
			if ok != want {
				t.Errorf("stage=%q user=%s allowed=%v, want %v", stage, user, ok, want)
			}
			if user == "stage-none" && (w.Code != 403 || !hasCode(w.Body.Bytes(), "forecast_unavailable")) {
				t.Fatalf("stage widened none: %d %s", w.Code, w.Body.String())
			}
		}
	}
	if err := s.st.DeleteSetting(setForecastReader); err != nil {
		t.Fatal(err)
	}
	if stage := s.forecastReaderStage(s.forecastAccess("stage-reader")); stage != "card" {
		t.Fatalf("unset stage=%q, want card", stage)
	}
}

func TestReaderStageChangesAreValidatedAndAudited(t *testing.T) {
	s := newV1Server(t)
	for _, body := range []string{`{"reader":"broken","mode":"dry"}`, `{"reader":false}`, `{"reader":null}`} {
		if w := saveForecastSettings(t, s, body); w.Code != 400 {
			t.Fatalf("invalid stage accepted: %s: %d", body, w.Code)
		}
	}
	if s.st.GetSetting(setForecastAuto, "0") != "0" || s.st.GetSetting(setForecastReader, "card") != "card" {
		t.Fatal("invalid reader input partially changed settings")
	}
	for _, stage := range []string{"off", "off", "card", "full"} {
		if w := saveForecastSettings(t, s, `{"reader":"`+stage+`"}`); w.Code != 200 {
			t.Fatalf("stage %s: %d %s", stage, w.Code, w.Body.String())
		}
	}
	rows, _ := s.st.ListAudit(AuditFilter{Action: AuditForecastReader})
	if len(rows) != 3 {
		t.Fatalf("stage audit rows=%d", len(rows))
	}
	for _, row := range rows {
		var detail map[string]any
		if err := json.Unmarshal([]byte(row.Detail), &detail); err != nil || detail["from"] == nil || detail["to"] == nil || detail["from"] == detail["to"] {
			t.Fatalf("audit has no exact stage transition: %s", row.Detail)
		}
	}
	w := httptest.NewRecorder()
	s.apiForecastSettings(w, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
	var settings map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil || settings["reader"] != "full" {
		t.Fatalf("stored stage not exposed: %s, %v", w.Body.String(), err)
	}
}

func TestReaderStageAndShadowControlsValidateBeforeEitherWrite(t *testing.T) {
	s := newV1Server(t)
	for _, body := range []string{`{"reader":"broken","fallback":"shadow"}`, `{"reader":"full","fallback":"on"}`} {
		if w := saveForecastSettings(t, s, body); w.Code != 400 {
			t.Fatalf("mixed invalid controls accepted: %s => %d %s", body, w.Code, w.Body.String())
		}
		if s.st.GetSetting(setForecastReader, "card") != "card" || s.st.GetSetting(setForecastFallback, "off") != "off" {
			t.Fatal("invalid mixed control request partially persisted")
		}
	}
	if w := saveForecastSettings(t, s, `{"reader":"full","fallback":"shadow"}`); w.Code != 200 {
		t.Fatalf("independent valid controls rejected: %d %s", w.Code, w.Body.String())
	}
	if s.st.GetSetting(setForecastReader, "") != "full" || s.st.GetSetting(setForecastFallback, "") != "shadow" {
		t.Fatal("merged setting fields lost a control")
	}
	for _, action := range []string{AuditForecastReader, AuditForecastFallback} {
		if _, total := s.st.ListAudit(AuditFilter{Action: action}); total != 1 {
			t.Fatalf("merged audit vocabulary lost %s: %d", action, total)
		}
	}
}

// TestReaderStageEverywhere pins the stage switch on every session read surface: off, and a value
// the readers do not recognise, refuse the cookie reads (session routes and the cookie form of both
// v1 reads) with forecast_unavailable, while a persistent query token is unchanged and the record
// does not depend on the stage at all.
func TestReaderStageEverywhere(t *testing.T) {
	eachReaderServer(t, func(t *testing.T, s *Server) {
		s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
		seedForecastReader(t, s, "stage-reader", scopeAll, false)
		t.Cleanup(func() { _ = s.st.DeleteSetting(setForecastReader) })
		report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2025-06-10", S0Fen: ip(10000)}, "stage fixture")
		for _, stage := range []string{"off", "broken", "card", "full"} {
			if err := s.st.SetSetting(setForecastReader, stage); err != nil {
				t.Fatal(err)
			}
			want := 200
			if stage == "off" || stage == "broken" {
				want = 403
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "/api/report/x/predictions", nil)
			r.SetPathValue("id", itoa64(report))
			s.apiReportPredictions(w, r, "stage-reader")
			if w.Code != want || want == 403 && !hasCode(w.Body.Bytes(), "forecast_unavailable") {
				t.Fatalf("stage %s session report status=%d %s", stage, w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			s.apiQueryPredictions(w, httptest.NewRequest("GET", "/api/predictions?symbol=600001", nil), "stage-reader")
			if w.Code != want {
				t.Fatalf("stage %s session query status=%d", stage, w.Code)
			}
			w = httptest.NewRecorder()
			r = sessionForPath(t, s, "stage-reader", "GET /api/v1/reports/{id}/predictions", "/api/v1/reports/"+itoa64(report)+"/predictions")
			r.SetPathValue("id", itoa64(report))
			s.v1ReportPredictions(w, r)
			if w.Code != want {
				t.Fatalf("stage %s v1 cookie status=%d", stage, w.Code)
			}
			w = httptest.NewRecorder()
			r = httptest.NewRequest("GET", "/api/v1/reports/x/predictions", nil)
			r.SetPathValue("id", itoa64(report))
			r.Header.Set("Authorization", "Bearer tok-all")
			s.v1ReportPredictions(w, r)
			if w.Code != 200 {
				t.Fatalf("stage %s changed persistent token: %d %s", stage, w.Code, w.Body.String())
			}
			_ = readerGroups(t, s, "stage-reader", "")
		}
	})
}

func TestMeForecastReaderStageTracksCurrentPolicy(t *testing.T) {
	s := newV1Server(t)
	for _, scope := range []string{scopeAll, scopeOwn, scopeNone} {
		seedForecastReader(t, s, "stage-"+scope, scope, false)
	}
	seedForecastReader(t, s, "stage-external", scopeOwn, true)
	mustAddUser(t, s, "stage-admin", true)
	for _, stage := range []string{"off", "card", "full", "unknown", ""} {
		if err := s.st.SetSetting(setForecastReader, stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"stage-all", "stage-own", "stage-none", "stage-external", "stage-admin"} {
			want := stage
			if !validForecastReaderStage(stage) || user == "stage-none" {
				want = "off"
			}
			if user == "stage-admin" {
				want = "full"
			}
			out := s.meJSON(user)
			if out["forecast_reader"] != want {
				t.Errorf("stage=%q user=%s hint=%v, want %s", stage, user, out["forecast_reader"], want)
			}
			if out["forecast"].(map[string]any)["scope"] != s.forecastAccess(user).Scope {
				t.Fatal("reading stage changed the independent record hint")
			}
		}
	}
}
