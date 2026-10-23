package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMeForecastMatchesTheGate: the navigation hint on /api/me is the same decision the forecast
// route enforces. For every kind of subject the scope on /api/me agrees with the outcome of the
// record route (200 for all and own, 403 forecast_unavailable for none), and with the strict
// resolver the gate itself uses.
func TestMeForecastMatchesTheGate(t *testing.T) {
	s := tokenServer(t)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	mustAddUser(t, s, "admin", true)
	subjects := []struct {
		user, scope string
		external    bool
	}{
		{"all-reader", scopeAll, false}, {"own-reader", scopeOwn, false},
		{"external-reader", scopeAll, true}, {"external-own", scopeOwn, true},
		{"none-reader", scopeNone, false}, {"external-none", scopeNone, true},
		{"admin", scopeAll, false},
	}
	for _, tc := range subjects {
		if tc.user != "admin" {
			seedForecastReader(t, s, tc.user, tc.scope, tc.external)
		}
		got, ok := s.meJSON(tc.user)["forecast"].(map[string]any)
		if !ok || len(got) != 2 || got["scope"] != tc.scope || got["external"] != tc.external {
			t.Fatalf("%s: navigation hint does not match strict scope: %#v", tc.user, got)
		}
		// Through the real mux: the hint the SPA reads and the gate the server enforces.
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, tc.user, "", "/api/me"))
		var me struct {
			Forecast map[string]any `json:"forecast"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK {
			t.Fatalf("%s /api/me: %d %s %v", tc.user, w.Code, w.Body.String(), err)
		}
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, tc.user, "", "/api/forecast/groups"))
		allowed := w.Code == http.StatusOK
		if allowed != (me.Forecast["scope"] != scopeNone) {
			t.Fatalf("%s: /api/me says %v but the record route answered %d %s", tc.user, me.Forecast, w.Code, w.Body.String())
		}
		if !allowed && (w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable") {
			t.Fatalf("%s: refused with %d %s", tc.user, w.Code, w.Body.String())
		}
		if d := s.forecastAccess(tc.user); d.Scope != me.Forecast["scope"] || d.External != me.Forecast["external"] {
			t.Fatalf("%s: /api/me %v differs from the resolver %+v", tc.user, me.Forecast, d)
		}
	}
}
