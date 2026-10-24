package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// brokenTreeReader seeds an account whose OU chain cannot be walked: either its parent names a
// deleted row (dangling) or the chain loops (cyclic). Only the dangling chain also makes the
// enrolment policy unreadable; the wall walks past a cycle, so a cyclic account reaches the
// forecast gate itself.
func brokenTreeReader(t *testing.T, s *Server, kind string) string {
	t.Helper()
	user := "broken-" + kind
	mustAddUser(t, s, user, false)
	var leaf int64
	switch kind {
	case "dangling":
		missing := seedOU(t, s.st, 0, nil)
		leaf = seedOU(t, s.st, missing, scopeAll)
		if _, err := s.st.exec("DELETE FROM user_groups WHERE id=?", missing); err != nil {
			t.Fatal(err)
		}
	case "cyclic":
		a := seedOU(t, s.st, 0, scopeAll)
		b := seedOU(t, s.st, a, nil)
		if _, err := s.st.exec("UPDATE user_groups SET parent_id=? WHERE id=?", b, a); err != nil {
			t.Fatal(err)
		}
		leaf = b
	default:
		t.Fatalf("unknown kind %q", kind)
	}
	if _, err := s.st.exec("UPDATE users SET group_id=? WHERE username=?", leaf, user); err != nil {
		t.Fatal(err)
	}
	return user
}

// TestBrokenOUChainAnswersForecastUnavailableOnForecastReads: an account whose OU chain is broken
// resolves to none (dangling_group / cyclic_chain), and every forecast read answers that decision
// (403 forecast_unavailable). For a dangling chain that replaces the generic "sign-in policy
// unreadable" 503 which the same chain produces in the enrolment wall; for a cyclic chain the wall
// lets the request through and the forecast gate refuses it. Both are refusals; the forecast one is
// the specific answer the access table promises for a none subject.
//
// The exception is narrow by design: any other route still gets the wall's 503 for a dangling
// chain, so a broken tree keeps the portal closed everywhere the forecast decision has no say.
func TestBrokenOUChainAnswersForecastUnavailableOnForecastReads(t *testing.T) {
	s := tokenServer(t)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	paths := map[string]string{
		"GET /api/forecast/groups":               "/api/forecast/groups",
		"GET /api/forecast/groups/members":       "/api/forecast/groups/members?label=2025-06&checkpoint=d5",
		"GET /api/report/{id}/predictions":       "/api/report/1/predictions",
		"GET /api/predictions":                   "/api/predictions?symbol=600160",
		"GET /api/v1/reports/{id}/predictions":   "/api/v1/reports/1/predictions",
		"GET /api/v1/predictions":                "/api/v1/predictions?symbol=600160",
		"GET /api/home (outside the forecast)":   "/api/home",
		"GET /api/stock/{symbol} (not forecast)": "/api/stock/600160",
	}
	for _, kind := range []string{"dangling", "cyclic"} {
		user := brokenTreeReader(t, s, kind)
		if d := s.forecastAccess(user); d.Scope != scopeNone || d.Reason != map[string]string{"dangling": "dangling_group", "cyclic": "cyclic_chain"}[kind] {
			t.Fatalf("%s: fixture does not resolve to a structural none: %+v", kind, d)
		}
		for label, path := range paths {
			t.Run(kind+" "+label, func(t *testing.T) {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, sessionForPath(t, s, user, "", path))
				if strings.Contains(label, "outside") || strings.Contains(label, "not forecast") {
					if kind == "cyclic" {
						// The enrolment policy walks past a cycle without error, so there is no wall
						// refusal to compare against here; the forecast gate alone refuses a cyclic
						// account, and the forecast routes are what this case asserts.
						return
					}
					if w.Code != http.StatusServiceUnavailable || forecastMatrixErrorCode(t, w.Body.Bytes()) != "enrolment_policy_unavailable" {
						t.Fatalf("the wall stopped failing closed off the forecast routes: %d %s", w.Code, w.Body.String())
					}
					return
				}
				if w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
					t.Fatalf("got %d %s; want 403 forecast_unavailable", w.Code, w.Body.String())
				}
			})
		}
	}
}

// TestForecastReadPatternsMatchTheRouteTable keeps the list the wall consults honest: every pattern
// on it is a registered GET route, every forecast-gated row is on it, and the prediction card and
// history reads in both the session and the v1 classes are on it.
func TestForecastReadPatternsMatchTheRouteTable(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	registered := map[string]routeRow{}
	for _, row := range s.routeTable {
		registered[row.pattern] = row
	}
	for pattern := range forecastReadPatterns {
		if _, ok := registered[pattern]; !ok {
			t.Errorf("%s is listed as a forecast read but is not a registered route", pattern)
		}
	}
	for _, row := range s.routeTable {
		if row.gate == gateForecastScope && !forecastReadPatterns[row.pattern] {
			t.Errorf("%s is forecast-gated but missing from forecastReadPatterns", row.pattern)
		}
	}
	for _, pattern := range []string{"GET /api/report/{id}/predictions", "GET /api/predictions", "GET /api/v1/reports/{id}/predictions", "GET /api/v1/predictions"} {
		if !forecastReadPatterns[pattern] {
			t.Errorf("%s reads the ledger for a reader but is missing from forecastReadPatterns", pattern)
		}
	}
}
