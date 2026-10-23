package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func seedForecastReader(t *testing.T, s *Server, user, scope string, external bool) int64 {
	t.Helper()
	mustAddUser(t, s, user, false)
	if scope == scopeOwn {
		ensurePurgeEvidenceTable(t, s.st)
	}
	ou := seedOU(t, s.st, 0, scope)
	if _, err := s.st.exec("UPDATE users SET group_id=?,restricted=? WHERE username=?", ou, boolInt(external), user); err != nil {
		t.Fatal(err)
	}
	return ou
}

func readerGroups(t *testing.T, s *Server, user, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	s.apiReaderForecastGroups(w, httptest.NewRequest("GET", "/api/forecast/groups"+query, nil), user)
	if w.Code != http.StatusOK {
		t.Fatalf("reader groups: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReaderAndAdminGetIdenticalAllRecords(t *testing.T) {
	s := newV1Server(t)
	s.forecastEval = forecastEvaluator{now: func() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }}
	seedForecastReader(t, s, "reader", scopeAll, false)
	sampleMembers(t, s, 32, nil)
	w := httptest.NewRecorder()
	s.apiForecastGroups(w, httptest.NewRequest("GET", "/api/admin/forecast/groups", nil), "admin")
	var admin map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	delete(admin, "unrecorded")
	got := readerGroups(t, s, "reader", "")
	if !reflect.DeepEqual(admin, got) {
		t.Fatal("reader and admin all responses diverged")
	}
}

func TestReaderGroupsOwnUsesIsolatedCache(t *testing.T) {
	s := newV1Server(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	ou := seedForecastReader(t, s, "reader", scopeOwn, false)
	sampleMembers(t, s, 32, nil)
	if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", ou); err != nil {
		t.Fatal(err)
	}
	first := readerGroups(t, s, "reader", "")
	// A different population's earlier set must not alter this reader's periods or membership.
	seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: "2024-01-02", S0Fen: ip(10000)})
	second := readerGroups(t, s, "reader", "")
	if !reflect.DeepEqual(first, second) {
		t.Fatal("other population changed own record")
	}
	if len(s.forecastGroupsCache) == 0 {
		t.Fatal("own mature record was not memoized")
	}
	pop := populationFor(s.forecastAccess("reader"))
	if c, _ := s.cachedForecastCell(pop.cellKey(predTestRtype, "2025-06-01", "2025-06-30", "", "d5")); c == nil || c.State != "mature" {
		t.Fatal("own cell used wrong cache identity")
	}
	if c, _ := s.cachedForecastCell(forecastGroupCellKey(predTestRtype, "2025-06-01", "2025-06-30", "", "d5")); c != nil {
		t.Fatal("own cell polluted all cache")
	}
}

func TestExternalCustomRangeRefused(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "external", scopeAll, true)
	seedForecastReader(t, s, "none", scopeNone, false)
	for _, handler := range []handler{s.apiReaderForecastGroups, s.apiReaderForecastGroupMembers} {
		for _, tc := range []struct{ user, code string }{{"external", "forecast_custom_not_allowed"}, {"none", "forecast_unavailable"}} {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest("GET", "/api/forecast/groups?mode=custom", nil), tc.user)
			if w.Code != http.StatusForbidden || !hasCode(w.Body.Bytes(), tc.code) {
				t.Fatalf("%s: %d %s", tc.user, w.Code, w.Body.String())
			}
		}
	}
}
