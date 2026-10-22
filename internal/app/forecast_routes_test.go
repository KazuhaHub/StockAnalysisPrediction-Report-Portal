package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestForecastReaderRoutesServeAllowedSessions(t *testing.T) {
	for _, scope := range []string{scopeAll, scopeOwn} {
		t.Run(scope, func(t *testing.T) {
			s := newV1Server(t)
			s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
			ou := seedForecastReader(t, s, "reader", scope, false)
			sampleMembers(t, s, 32, nil)
			if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", ou); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			s.wireRoutes(mux)
			for _, row := range forecastGatedRows(t, s) {
				path := strings.TrimPrefix(row.pattern, "GET ")
				if strings.HasSuffix(path, "/members") {
					path += "?label=2025-06&checkpoint=d5"
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, sessionForPath(t, s, "reader", row.pattern, path))
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusOK || out["ok"] != true {
					t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
				}
				if _, leaked := out["unrecorded"]; leaked {
					t.Fatal("reader route leaked admin unrecorded count")
				}
			}
			for _, path := range []string{"/api/admin/forecast/groups", "/api/admin/forecast/groups/members"} {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, sessionForPath(t, s, "reader", "GET "+path, path))
				if w.Code != http.StatusForbidden {
					t.Fatalf("non-admin session reached admin alias: %d %s", w.Code, w.Body.String())
				}
			}
			// Revoking scope must take effect even after this session has populated the memo.
			if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeNone, ou); err != nil {
				t.Fatal(err)
			}
			for _, row := range forecastGatedRows(t, s) {
				path := strings.TrimPrefix(row.pattern, "GET ")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, sessionForPath(t, s, "reader", row.pattern, path))
				if w.Code != http.StatusForbidden || !hasCode(w.Body.Bytes(), "forecast_unavailable") {
					t.Fatalf("revoked scope reused cached access: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func forecastGatedRows(t *testing.T, s *Server) []routeRow {
	t.Helper()
	var rows []routeRow
	for _, row := range s.routeTable {
		if row.gate == gateForecastScope {
			rows = append(rows, row)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("expected two forecast-gated routes, got %d", len(rows))
	}
	return rows
}

func TestForecastRoutesAreSessionRoutesBehindTheWall(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	for _, row := range s.routeTable {
		reader := strings.HasPrefix(row.pattern, "GET /api/forecast/")
		if reader != (row.gate == gateForecastScope) {
			t.Errorf("incorrect forecast annotation: %+v", row)
		}
		if row.gate != "" && !reader {
			t.Errorf("unexpected gate outside forecast reader routes: %+v", row)
		}
	}
	for _, row := range forecastGatedRows(t, s) {
		if row.class != authSession || enrolmentPaths[row.pattern] {
			t.Errorf("forecast statistics bypass the session/enrolment wall: %+v", row)
		}
	}
}

func TestForecastGatedRoutesDenyANoneUser(t *testing.T) {
	env := mandateServer(t)
	s := env.s
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	for _, row := range forecastGatedRows(t, s) {
		path := strings.TrimPrefix(row.pattern, "GET ")
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct {
				user   string
				code   string
				status int
			}{{"", "", 401}, {env.free, "forecast_unavailable", 403}, {env.must, "enrolment_required", 403}} {
				r := httptest.NewRequest("GET", path, nil)
				if tc.user != "" {
					r = sessionForPath(t, s, tc.user, row.pattern, path)
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				if w.Code != tc.status || tc.code != "" && !hasCode(w.Body.Bytes(), tc.code) {
					t.Errorf("user %q: %d %s", tc.user, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestForecastStatsNotReachableByBearer(t *testing.T) {
	s := routeTableServer(t)
	s.appTok = newAppTokens(time.Minute)
	bridge := s.appTok.mint([]string{"query"}, time.Now())
	if err := s.st.CreateToken("forecast-machine", "test", "query", ""); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	for _, row := range forecastGatedRows(t, s) {
		for _, token := range []string{bridge, "forecast-machine"} {
			r := httptest.NewRequest("GET", strings.TrimPrefix(row.pattern, "GET "), nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s accepted a bearer credential: %d %s", row.pattern, w.Code, w.Body.String())
			}
		}
	}
}
