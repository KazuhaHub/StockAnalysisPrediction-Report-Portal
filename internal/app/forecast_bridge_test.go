package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mintForecastBridge(t *testing.T, s *Server, user string) string {
	t.Helper()
	if err := s.st.InstallApp(App{ID: "forecast-reader", Name: "Forecast reader", Entry: "index.html", Scopes: []string{"query"}},
		map[string]AppFile{"index.html": {Ctype: "text/html", Content: []byte("test")}}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/apps/forecast-reader/token", nil)
	r.SetPathValue("id", "forecast-reader")
	s.apiAppToken(w, r, user)
	var out struct {
		Token  string   `json:"token"`
		Scopes []string `json:"scopes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out.Token == "" || !containsStr(out.Scopes, "query") {
		t.Fatalf("bridge mint: %d %s", w.Code, w.Body.String())
	}
	return out.Token
}

func bridgePredictionRequest(s *Server, token, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	mux.ServeHTTP(w, r)
	return w
}

func TestBridgeTokenCarriesItsUser(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	for _, scope := range []string{scopeAll, scopeOwn, scopeNone} {
		seedForecastReader(t, s, scope, scope, false)
	}
	mustAddUser(t, s, "bridge-admin", true)
	report, accepted := seedLedgerFor(t, s, "600160", "2026-10-17", "# accepted")
	_, rejected := seedLedgerFor(t, s, "600160", "2026-10-18", "# rejected")
	if _, err := s.st.exec(`UPDATE prediction_sets SET state='rejected',errors='[{"reason":"fixture"}]' WHERE id=?`, rejected); err != nil {
		t.Fatal(err)
	}
	orphan, _ := seedLedgerFor(t, s, "600160", "2026-10-19", "# orphan")
	if _, err := s.st.DeleteReport(orphan); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/v1/reports/" + itoa64(report) + "/predictions?include=payload", "/api/v1/predictions?symbol=600160&include=payload"}
	for _, user := range []string{scopeNone, scopeAll, scopeOwn, "bridge-admin"} {
		token := mintForecastBridge(t, s, user)
		for _, path := range paths {
			t.Run(user+path, func(t *testing.T) {
				w := bridgePredictionRequest(s, token, path)
				if user == scopeNone {
					if w.Code != 403 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
						t.Fatalf("none bridge: %d %s", w.Code, w.Body.String())
					}
					return
				}
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 {
					t.Fatalf("allowed bridge: %d %s", w.Code, w.Body.String())
				}
				forecastMatrixForbid(t, out, "$", []string{"payload", "errors", "membership", "counted_set_id", "counted_in_group", "attributed_ou"})
				sets := out["sets"].([]any)
				if len(sets) != 1 || sets[0].(map[string]any)["id"] != float64(accepted) {
					t.Fatalf("bridge accepted/report-backed filter: %v", sets)
				}
			})
		}
	}
	// A legacy/unbound app token must not be mistaken for a persistent machine credential.
	for _, path := range paths {
		w := bridgePredictionRequest(s, s.appTok.mint([]string{"query"}, time.Now()), path)
		if w.Code != 403 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
			t.Fatalf("unbound bridge: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestBridgePredictionRechecksCurrentAccount(t *testing.T) {
	for _, change := range []string{"none", "inactive", "deleted", "revision", "recreated", "expired", "mandated"} {
		t.Run(change, func(t *testing.T) {
			s := tokenServer(t)
			s.appTok = newAppTokens(time.Hour)
			ou := seedForecastReader(t, s, "reader", scopeAll, false)
			token := mintForecastBridge(t, s, "reader")
			report, _ := seedLedgerFor(t, s, "600160", "2026-10-17", "# accepted")
			want, code := 403, "forecast_unavailable"
			var err error
			switch change {
			case "none":
				_, err = s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeNone, ou)
			case "inactive":
				err = s.st.SetUserActive("reader", false)
			case "deleted":
				_, err = s.st.exec("DELETE FROM users WHERE username=?", "reader")
			case "revision":
				_, err = s.st.exec("UPDATE users SET session_rev=session_rev+1 WHERE username=?", "reader")
			case "recreated":
				// Same name, active and all, but a distinct account incarnation.
				_, err = s.st.exec("UPDATE users SET created_at=? WHERE username=?", "2099-01-01T00:00:00Z", "reader")
			case "expired":
				err = s.st.SetUserExpiry("reader", "2000-01-01")
			case "mandated":
				err = s.st.SetGroupRequire2FA(ou, true)
				code = "enrolment_required"
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"/api/v1/reports/" + itoa64(report) + "/predictions", "/api/v1/predictions?symbol=600160"} {
				w := bridgePredictionRequest(s, token, path)
				if w.Code != want || forecastMatrixErrorCode(t, w.Body.Bytes()) != code {
					t.Fatalf("old bridge after %s: %d %s", change, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestBridgePredictionBecomesRestricted(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	ou := seedForecastReader(t, s, "reader", scopeAll, false)
	token := mintForecastBridge(t, s, "reader")
	if err := s.st.SaveVersion(ReportVersion{Name: "default", Visibility: VisibilityOwner}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("default", []string{userPrincipal("reader")}); err != nil {
		t.Fatal(err)
	}
	readable, set := seedLedgerFor(t, s, "600160", "2026-10-17", "# readable")
	hidden, _ := seedLedgerFor(t, s, "600160", "2026-10-18", "# hidden")
	if err := s.st.AddReportViewer(readable, "2026-10-17", "reader", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetGroupRestricted(ou, true); err != nil {
		t.Fatal(err)
	}
	w := bridgePredictionRequest(s, token, "/api/v1/predictions?symbol=600160&include=payload&limit=1")
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 {
		t.Fatalf("restricted bridge: %d %s", w.Code, w.Body.String())
	}
	sets := out["sets"].([]any)
	if len(sets) != 1 || sets[0].(map[string]any)["id"] != float64(set) {
		t.Fatalf("new restriction ignored or filtering followed LIMIT: %v", sets)
	}
	forecastMatrixForbid(t, out, "$", []string{"payload", "errors", "counted_set_id", "counted_in_group"})
	w = bridgePredictionRequest(s, token, "/api/v1/reports/"+itoa64(hidden)+"/predictions")
	if w.Code != 404 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "not_found" {
		t.Fatalf("old token bypassed new report scope: %d %s", w.Code, w.Body.String())
	}
}

func TestBridgePredictionUsesLiveScopeAndCookiePrecedence(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	ou := seedForecastReader(t, s, "reader", scopeNone, false)
	seedForecastReader(t, s, "denied-cookie", scopeNone, false)
	token := mintForecastBridge(t, s, "reader")
	seedLedgerFor(t, s, "600160", "2026-10-17", "# accepted")
	path := "/api/v1/predictions?symbol=600160"
	w := bridgePredictionRequest(s, token, path)
	if w.Code != 403 {
		t.Fatalf("none at mint was allowed: %d", w.Code)
	}
	if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeAll, ou); err != nil {
		t.Fatal(err)
	}
	w = bridgePredictionRequest(s, token, path)
	if w.Code != 200 {
		t.Fatalf("scope was frozen at mint: %d %s", w.Code, w.Body.String())
	}
	// As on the other v1 reads, a valid cookie wins over an accompanying Bearer token.
	r := sessionForPath(t, s, "denied-cookie", "GET /api/v1/predictions", path)
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.v1QueryPredictions(w, r)
	if w.Code != 403 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
		t.Fatalf("Bearer token overrode denied cookie: %d %s", w.Code, w.Body.String())
	}
}

func TestBridgePredictionLookupBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	at := newAppTokens(time.Minute)
	user := &User{Username: "reader", SessionRev: 7, CreatedAt: "2026-01-01T00:00:00Z"}
	token := at.mintForApp([]string{"query"}, now, "reader-app", user)
	entry, ok := at.lookup(token, now)
	if !ok || entry.username != user.Username || entry.sessionRev != user.SessionRev || entry.accountCreated != user.CreatedAt || entry.appID != "reader-app" {
		t.Fatalf("binding lost: %+v %v", entry, ok)
	}
	if _, ok := at.lookup(token, now.Add(time.Minute-time.Nanosecond)); !ok {
		t.Fatal("token expired before its deadline")
	}
	for _, offset := range []time.Duration{time.Minute, time.Minute + time.Nanosecond} {
		if _, ok := at.lookup(token, now.Add(offset)); ok {
			t.Fatal("lookup accepted token at or after expiry")
		}
	}
	for _, token := range []string{"", "unknown", at.mint([]string{"query"}, now), at.mintForApp([]string{"ingest"}, now, "reader-app", user), at.mintForApp([]string{"query"}, now, "reader-app", nil)} {
		if _, ok := at.lookup(token, now); ok {
			t.Fatal("unknown, unbound or non-query token acquired prediction identity")
		}
	}
	var absent *appTokens
	if _, ok := absent.lookup(token, now); ok {
		t.Fatal("absent registry granted access")
	}
}

func TestBridgePredictionDeniesStoreError(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	seedForecastReader(t, s, "reader", scopeAll, false)
	token := mintForecastBridge(t, s, "reader")
	s.st.Close()
	for _, path := range []string{"/api/v1/reports/1/predictions", "/api/v1/predictions?symbol=600160"} {
		w := bridgePredictionRequest(s, token, path)
		if w.Code != 403 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
			t.Fatalf("store error granted bridge access: %d %s", w.Code, w.Body.String())
		}
	}
}
