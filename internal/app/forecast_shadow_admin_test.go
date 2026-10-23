package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForecastShadowAdminDistinguishesMissingAndDamagedTelemetry(t *testing.T) {
	s := newV1Server(t)
	read := func() map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		s.apiForecastSettings(w, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 {
			t.Fatalf("settings: %d %s %v", w.Code, w.Body.String(), err)
		}
		diagnostics, ok := body["diagnostics"].(map[string]any)
		if !ok {
			t.Fatal("missing diagnostic status")
		}
		return diagnostics
	}
	if got := read(); got["status"] != "empty" || got["max_reads"] != float64(2) || got["max_http_attempts"] != float64(4) {
		t.Fatalf("missing telemetry looks measured: %+v", got)
	}
	run := shadowStateFixture(time.Date(2026, 10, 22, 16, 0, 0, 0, time.UTC))
	if err := s.st.saveForecastDiagnostics(context.Background(), run, []forecastCallObservation{{Source: quoteSourceSina, Calls: 1, ShadowHTTPAttempts: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["status"] != "ready" || got["shadow"] == nil || got["calls"] == nil {
		t.Fatalf("saved telemetry missing: %+v", got)
	}
	if err := s.st.SetSetting(setForecastStateCalls, "private-corrupt-body"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got["status"] != "unavailable" || got["shadow"] != nil || got["calls"] != nil {
		t.Fatalf("damaged telemetry fabricated or leaked: %+v", got)
	}
	if got := s.st.GetSetting(setForecastStateCalls, ""); got != "private-corrupt-body" {
		t.Fatal("reading damaged evidence overwrote it")
	}
}

func TestForecastShadowAdminRouteRequiresAdministrator(t *testing.T) {
	s := tenancyServer(t)
	s.st.UpsertUser(User{Username: "alice", PasswordHash: "h", Role: "user"})
	s.st.UpsertUser(User{Username: "root", PasswordHash: "h", Role: "admin"})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	for _, route := range []struct{ method, body string }{{"GET", ""}, {"POST", `{"fallback":"shadow"}`}} {
		w := sessionTestRequest(mux, route.method, "/api/admin/forecast", managedTestCookie(t, s, "alice", "Chrome/130.0", time.Hour), route.body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("reader reached diagnostics or controls: %s %d %s", route.method, w.Code, w.Body.String())
		}
	}
	w := sessionTestRequest(mux, "GET", "/api/admin/forecast", managedTestCookie(t, s, "root", "Chrome/130.0", time.Hour), "")
	if w.Code != http.StatusOK {
		t.Fatalf("administrator denied: %d %s", w.Code, w.Body.String())
	}
}
