package app

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestForecastDemoClockPreservesScopedPreview(t *testing.T) {
	t.Setenv(forecastDemoClockEnv, forecastDemoInstant)
	path := filepath.Join(t.TempDir(), "portal.db")
	if _, err := writeForecastDemoDB(t, path); err != nil {
		t.Fatal(err)
	}
	s := demoPreviewServer(t, path)
	operational := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s.forecastEval.now = func() time.Time { return operational }
	group := func(out map[string]any, label string) map[string]any {
		t.Helper()
		if out["range"].(map[string]any)["to"] != "2027-06-30" {
			t.Fatal("reader periods ignored the demo read clock")
		}
		for _, raw := range out["groups"].([]any) {
			g := raw.(map[string]any)
			if g["label"] == label {
				return g
			}
		}
		t.Fatalf("missing seeded cohort %s", label)
		return nil
	}
	own := group(readerGroups(t, s, "own", ""), "2026-01")
	ownCell := own["cells"].(map[string]any)["d5"].(map[string]any)
	if ownCell["state"] != "small" || ownCell["n"] != float64(10) {
		t.Fatalf("demo clock widened the own/sub-OU population: %v", ownCell)
	}
	external := group(readerGroups(t, s, "external-all", ""), "2026-01")
	externalCell := external["cells"].(map[string]any)["d5"].(map[string]any)
	if externalCell["state"] != "mature" || externalCell["spread_bp"] == nil {
		t.Fatalf("external all preview lost its published metrics: %v", externalCell)
	}
	if _, exists := external["coverage"]; exists {
		t.Fatal("demo clock exposed external all coverage")
	}
	if _, exists := externalCell["n"]; exists {
		t.Fatal("demo clock exposed external all sample size")
	}
	w := httptest.NewRecorder()
	s.apiReaderForecastGroups(w, httptest.NewRequest("GET", "/api/forecast/groups", nil), "none")
	if w.Code != http.StatusForbidden || !hasCode(w.Body.Bytes(), "forecast_unavailable") {
		t.Fatalf("demo clock bypassed none scope: %d %s", w.Code, w.Body.String())
	}
	if err := s.forecastViewers.flush(s.st, operational); err != nil {
		t.Fatal(err)
	}
	if got := s.st.GetSetting(forecastViewerWeekKey(operational), ""); got != "2" {
		t.Fatalf("demo read clock moved adoption writes or counted a denied viewer: %q", got)
	}
}

func TestForecastDemoClockDoesNotResetReaderLimits(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "reader", scopeAll, false)
	operational := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s.forecastEval.now = func() time.Time { return operational }
	for _, key := range []string{setForecastCustomPerMin, setForecastMembersPerMin} {
		if err := s.st.SetSetting(key, "1"); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		h    handler
	}{{"custom", s.apiReaderForecastGroups}, {"members", s.apiReaderForecastGroupMembers}} {
		t.Run(tc.name, func(t *testing.T) {
			for i, instant := range []string{forecastDemoInstant, "2027-06-20T07:02:00Z"} {
				t.Setenv(forecastDemoClockEnv, instant)
				w := httptest.NewRecorder()
				tc.h(w, httptest.NewRequest("GET", "/api/forecast/groups?mode=custom", nil), "reader")
				if i == 0 && w.Code != http.StatusBadRequest {
					t.Fatalf("first invalid query did not exercise the limiter: %d %s", w.Code, w.Body.String())
				}
				if i == 1 && (w.Code != http.StatusTooManyRequests || !hasCode(w.Body.Bytes(), "forecast_rate_limited")) {
					t.Fatalf("moving the demo clock replenished a security limit: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}
