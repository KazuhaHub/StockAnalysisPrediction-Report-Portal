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
		if !ok || got["scope"] != tc.scope || got["external"] != tc.external {
			t.Fatalf("%s: navigation hint does not match strict scope: %#v", tc.user, got)
		}
		// Only an own reader gets a population name: the other scopes have nothing to name, and the
		// admin hint carries exactly the scope and the external flag.
		if _, named := got["ou"]; named != (tc.scope == scopeOwn) {
			t.Fatalf("%s: unexpected population name presence: %#v", tc.user, got)
		}
		if tc.user == "admin" && len(got) != 2 {
			t.Fatalf("admin hint: %#v", got)
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

// The population line names the OU the subtree is rooted at (the reader's own unit), not the
// ancestor that happened to decide the scope: naming the ancestor would claim a wider population
// than the one the statistics cover.
func TestMeForecastHintNamesTheOwnPopulationRoot(t *testing.T) {
	s := newV1Server(t)
	parent := seedOU(t, s.st, 0, scopeOwn)
	child := seedOU(t, s.st, parent, nil)
	mustAddUser(t, s, "child-reader", false)
	if _, err := s.st.exec("UPDATE users SET group_id=? WHERE username=?", child, "child-reader"); err != nil {
		t.Fatal(err)
	}
	var childName, parentName string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", child).Scan(&childName); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", parent).Scan(&parentName); err != nil {
		t.Fatal(err)
	}
	got, ok := s.meJSON("child-reader")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeOwn {
		t.Fatalf("inherited own hint: %#v", got)
	}
	if got["ou"] != childName || got["ou"] == parentName {
		t.Fatalf("population root name = %#v, want %q (not the deciding ancestor %q)", got["ou"], childName, parentName)
	}
}

// An unassigned own reader sits in the Default OU, which is the root of their population.
func TestMeForecastHintNamesDefaultForUnassignedOwnReader(t *testing.T) {
	s := newV1Server(t)
	def := s.st.DefaultGroupID()
	if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeOwn, def); err != nil {
		t.Fatal(err)
	}
	mustAddUser(t, s, "unassigned", false)
	var defName string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", def).Scan(&defName); err != nil {
		t.Fatal(err)
	}
	got, ok := s.meJSON("unassigned")["forecast"].(map[string]any)
	if !ok || got["scope"] != scopeOwn || got["ou"] != defName {
		t.Fatalf("unassigned own hint: %#v (want ou %q)", got, defName)
	}
}
