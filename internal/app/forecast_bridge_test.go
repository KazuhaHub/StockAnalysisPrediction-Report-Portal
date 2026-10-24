package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// App-bridge tokens get no forecast reads. The in-memory app-token registry carries no user, so the
// portal cannot tell which account's scope a bridge token would stand for; until the shared token
// binding lands, the only safe answer is a refusal. These tests pin that refusal and the two
// credentials that must keep working around it.

// mintForecastBridge mints a bridge token through the real mint path (apiAppToken), exactly as the
// host page does when a user opens an installed app.
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
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	mux.ServeHTTP(w, r)
	return w
}

// bridgeLedger seeds one accepted report-backed set, one rejected set and one orphaned set for the
// same symbol, and returns the paths of both v1 prediction reads.
func bridgeLedger(t *testing.T, s *Server) (report, accepted int64, paths []string) {
	t.Helper()
	report, accepted = seedLedgerFor(t, s, "600160", "2026-10-17", "# accepted")
	_, rejected := seedLedgerFor(t, s, "600160", "2026-10-18", "# rejected")
	if _, err := s.st.exec(`UPDATE prediction_sets SET state='rejected',errors='[{"reason":"fixture"}]' WHERE id=?`, rejected); err != nil {
		t.Fatal(err)
	}
	orphan, _ := seedLedgerFor(t, s, "600160", "2026-10-19", "# orphan")
	if _, err := s.st.DeleteReport(orphan); err != nil {
		t.Fatal(err)
	}
	return report, accepted, []string{"/api/v1/reports/" + itoa64(report) + "/predictions?include=payload", "/api/v1/predictions?symbol=600160&include=payload"}
}

// TestBridgeTokenGetsNoForecastReads: whatever account minted it, a bridge token holding the query
// scope is refused on both v1 prediction reads, and the refusal carries nothing but the error.
func TestBridgeTokenGetsNoForecastReads(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	for _, scope := range []string{scopeAll, scopeOwn, scopeNone} {
		seedForecastReader(t, s, scope, scope, false)
	}
	mustAddUser(t, s, "bridge-admin", true)
	_, _, paths := bridgeLedger(t, s)
	tokens := map[string]string{"direct-mint": s.appTok.mint([]string{"query"}, time.Now())}
	for _, user := range []string{scopeAll, scopeOwn, scopeNone, "bridge-admin"} {
		tokens["minted-by-"+user] = mintForecastBridge(t, s, user)
	}
	for name, token := range tokens {
		for _, path := range paths {
			t.Run(name+path, func(t *testing.T) {
				w := bridgePredictionRequest(s, token, path)
				if w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
					t.Fatalf("bridge read: %d %s", w.Code, w.Body.String())
				}
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if _, leaked := out["sets"]; leaked || len(out) != 2 {
					t.Fatalf("refusal carries more than the error: %v", out)
				}
			})
		}
	}
}

// TestBridgeDenialReadsNothingFromTheLedger: with the ledger table gone, a persistent token fails
// on the read it attempts (500), while a bridge token is still refused at the credential (403). A
// refusal that happened after a ledger query would surface the same 500.
func TestBridgeDenialReadsNothingFromTheLedger(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	seedForecastReader(t, s, scopeAll, scopeAll, false)
	_, _, paths := bridgeLedger(t, s)
	bridge := mintForecastBridge(t, s, scopeAll)
	if _, err := s.st.exec("DROP TABLE prediction_sets"); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if w := bridgePredictionRequest(s, "tok-all", path); w.Code != http.StatusInternalServerError {
			t.Fatalf("control: the persistent token should have reached the missing table: %d %s", w.Code, w.Body.String())
		}
		w := bridgePredictionRequest(s, bridge, path)
		if w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
			t.Fatalf("bridge read touched the ledger: %d %s", w.Code, w.Body.String())
		}
	}
}

// TestBridgeDenialSurvivesStoreError: a closed store neither admits nor crashes a bridge request.
func TestBridgeDenialSurvivesStoreError(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	seedForecastReader(t, s, scopeAll, scopeAll, false)
	token := mintForecastBridge(t, s, scopeAll)
	s.st.Close()
	for _, path := range []string{"/api/v1/reports/1/predictions", "/api/v1/predictions?symbol=600160"} {
		w := bridgePredictionRequest(s, token, path)
		if w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
			t.Fatalf("store error changed the bridge answer: %d %s", w.Code, w.Body.String())
		}
	}
}

// TestBridgeCredentialsThatAreNotQueryTokensStayUnauthorized: the 403 is for a live query-scoped
// bridge token only. A token that is expired, unknown or lacks the query scope is still a 401, so a
// caller can tell a dead credential from one the portal declines to honour.
func TestBridgeCredentialsThatAreNotQueryTokensStayUnauthorized(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	_, _, paths := bridgeLedger(t, s)
	for name, token := range map[string]string{
		"ingest-only": s.appTok.mint([]string{"ingest"}, time.Now()),
		"expired":     s.appTok.mint([]string{"query"}, time.Now().Add(-2*time.Hour)),
		"unknown":     "not-a-token",
		"none":        "",
	} {
		for _, path := range paths {
			if w := bridgePredictionRequest(s, token, path); w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s: %d %s", name, path, w.Code, w.Body.String())
			}
		}
	}
}

// TestPersistentQueryTokenKeepsTheMachineView: the order is cookie, then persistent token, then
// every other Bearer. A persistent query token (or an all-scope one) is the documented machine
// credential and keeps its unscoped diagnostic contract.
func TestPersistentQueryTokenKeepsTheMachineView(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	if err := s.st.CreateToken("forecast-machine", "test", "query", ""); err != nil {
		t.Fatal(err)
	}
	report, accepted, paths := bridgeLedger(t, s)
	for _, token := range []string{"forecast-machine", "tok-all"} {
		for _, path := range paths {
			w := bridgePredictionRequest(s, token, path)
			if w.Code != http.StatusOK {
				t.Fatalf("%s %s: %d %s", token, path, w.Code, w.Body.String())
			}
			var out struct {
				Sets []map[string]any `json:"sets"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			states, payloads, errs := map[any]int{}, 0, 0
			for _, set := range out.Sets {
				states[set["state"]]++
				if p, _ := set["payload"].(string); p != "" {
					payloads++
				}
				if e, ok := set["errors"].([]any); ok && len(e) > 0 {
					errs++
				}
				if set["id"] == float64(accepted) && set["report_id"] != float64(report) {
					t.Fatalf("machine shape lost the report link: %v", set)
				}
			}
			if path == paths[1] && (states[predStateAccepted] != 2 || states[predStateRejected] != 1) {
				t.Fatalf("machine list is not the unscoped ledger view: %v", states)
			}
			if payloads == 0 {
				t.Fatalf("machine view lost include=payload: %s", w.Body.String())
			}
			if path == paths[1] && errs == 0 {
				t.Fatalf("machine view lost the rejected set's errors: %s", w.Body.String())
			}
		}
	}
}

// TestCookieReaderStaysScopedOnV1Reads: a cookie user's own decision applies, and a cookie wins over
// an accompanying Bearer token, whichever way that decision goes.
func TestCookieReaderStaysScopedOnV1Reads(t *testing.T) {
	s := tokenServer(t)
	s.appTok = newAppTokens(time.Hour)
	seedForecastReader(t, s, "reader", scopeAll, false)
	seedForecastReader(t, s, "denied", scopeNone, false)
	seedForecastReader(t, s, "external", scopeAll, true)
	report, accepted, paths := bridgeLedger(t, s)
	// The external account reads only what it has been granted: the accepted report.
	if err := s.st.SaveVersion(ReportVersion{Name: "default", Visibility: VisibilityOwner}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("default", []string{userPrincipal("external")}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.AddReportViewer(report, "2026-10-17", "external", 0); err != nil {
		t.Fatal(err)
	}
	bridge := mintForecastBridge(t, s, "reader")
	for _, path := range paths {
		for _, header := range []string{"", "Bearer " + bridge, "Bearer tok-all"} {
			get := func(user string) *httptest.ResponseRecorder {
				r := sessionForPath(t, s, user, "", path)
				if header != "" {
					r.Header.Set("Authorization", header)
				}
				w := httptest.NewRecorder()
				mux := http.NewServeMux()
				s.wireRoutes(mux)
				mux.ServeHTTP(w, r)
				return w
			}
			if w := get("denied"); w.Code != http.StatusForbidden || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
				t.Fatalf("%q let a Bearer override the denied cookie: %d %s", header, w.Code, w.Body.String())
			}
			for _, user := range []string{"reader", "external"} {
				w := get(user)
				if w.Code != http.StatusOK {
					t.Fatalf("%s %q: %d %s", user, header, w.Code, w.Body.String())
				}
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				forecastMatrixForbid(t, out, "$", []string{"errors", "attributed_ou"})
				if user == "external" {
					forecastMatrixForbid(t, out, "$", []string{"payload", "counted_set_id", "counted_in_group"})
				}
				sets := out["sets"].([]any)
				if user == "external" && (len(sets) != 1 || sets[0].(map[string]any)["id"] != float64(accepted)) {
					t.Fatalf("external cookie reader is not scoped to accepted report-backed sets: %v", sets)
				}
				for _, raw := range sets {
					if raw.(map[string]any)["state"] != predStateAccepted {
						t.Fatalf("cookie reader saw a non-accepted set: %v", raw)
					}
				}
			}
		}
	}
}
