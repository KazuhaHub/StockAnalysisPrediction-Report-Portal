package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// refusalCase drives one real refusal through the mux and names the reason code it must carry.
type refusalCase struct {
	name, reason string
	run          func(t *testing.T, s *Server, mux *http.ServeMux)
}

func forecastRefusalCases() []refusalCase {
	get := func(t *testing.T, s *Server, mux *http.ServeMux, user, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, user, "", path))
		return w
	}
	want403 := func(t *testing.T, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusForbidden {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	return []refusalCase{
		{"an OU held at none", "ou_none", func(t *testing.T, s *Server, mux *http.ServeMux) {
			seedForecastReader(t, s, "refused-none", scopeNone, false)
			want403(t, get(t, s, mux, "refused-none", "/api/forecast/groups"))
		}},
		{"an OU chain that never sets a value", "default_null", func(t *testing.T, s *Server, mux *http.ServeMux) {
			mustAddUser(t, s, "refused-null", false)
			ou := seedOU(t, s.st, 0, nil)
			if _, err := s.st.exec("UPDATE users SET group_id=? WHERE username=?", ou, "refused-null"); err != nil {
				t.Fatal(err)
			}
			// The Default OU is normalised to none at boot; clearing it leaves a chain nothing sets.
			if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=NULL WHERE is_default=1"); err != nil {
				t.Fatal(err)
			}
			want403(t, get(t, s, mux, "refused-null", "/api/forecast/groups/members?label=2025-06&checkpoint=d5"))
		}},
		{"a stored value nothing defines", "unknown_value", func(t *testing.T, s *Server, mux *http.ServeMux) {
			seedForecastReader(t, s, "refused-unknown", "everyone", false)
			want403(t, get(t, s, mux, "refused-unknown", "/api/forecast/groups"))
		}},
		{"a dangling parent", "dangling_group", func(t *testing.T, s *Server, mux *http.ServeMux) {
			want403(t, get(t, s, mux, brokenTreeReader(t, s, "dangling"), "/api/forecast/groups"))
		}},
		{"a cyclic chain", "cyclic_chain", func(t *testing.T, s *Server, mux *http.ServeMux) {
			want403(t, get(t, s, mux, brokenTreeReader(t, s, "cyclic"), "/api/forecast/groups"))
		}},
		{"a custom range for an external reader", "custom_not_allowed", func(t *testing.T, s *Server, mux *http.ServeMux) {
			seedForecastReader(t, s, "refused-external", scopeAll, true)
			want403(t, get(t, s, mux, "refused-external", "/api/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30"))
		}},
		{"the reading stage held at off", "stage_off", func(t *testing.T, s *Server, mux *http.ServeMux) {
			seedForecastReader(t, s, "refused-stage", scopeAll, false)
			if err := s.st.SetSetting(setForecastReader, "off"); err != nil {
				t.Fatal(err)
			}
			want403(t, get(t, s, mux, "refused-stage", "/api/report/1/predictions"))
		}},
		{"an app-bridge token", "bridge_token", func(t *testing.T, s *Server, mux *http.ServeMux) {
			s.appTok = newAppTokens(time.Hour)
			seedForecastReader(t, s, "refused-bridge", scopeAll, false)
			want403(t, bridgePredictionRequest(s, mintForecastBridge(t, s, "refused-bridge"), "/api/v1/predictions?symbol=600160"))
		}},
	}
}

func TestForecastRefusalsAreCountedByReason(t *testing.T) {
	for _, tc := range forecastRefusalCases() {
		t.Run(tc.reason, func(t *testing.T) {
			s := tokenServer(t)
			s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
			mux := http.NewServeMux()
			s.wireRoutes(mux)
			tc.run(t, s, mux)
			got := signalFlush(t, s)
			if got[sigRefusedPrefix+tc.reason] != 1 {
				t.Fatalf("refusal %q was not counted once: %v", tc.reason, got)
			}
			for name, n := range got {
				if strings.HasPrefix(name, sigRefusedPrefix) && name != sigRefusedPrefix+tc.reason && n != 0 {
					t.Errorf("an unrelated reason was counted too: %s=%d", name, n)
				}
			}
		})
	}
}

func TestForecastRefusalLogCarriesTheReasonAndNoIdentity(t *testing.T) {
	for _, tc := range forecastRefusalCases() {
		t.Run(tc.reason, func(t *testing.T) {
			s := tokenServer(t)
			s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
			mux := http.NewServeMux()
			s.wireRoutes(mux)
			out := captureLog(t, func() { tc.run(t, s, mux) })
			var line string
			for _, l := range strings.Split(out, "\n") {
				if strings.Contains(l, "forecast access: refused") {
					line = l
				}
			}
			if !strings.Contains(line, "reason="+tc.reason+" ") && !strings.HasSuffix(line, "reason="+tc.reason) {
				t.Fatalf("no refusal line for %q in %q", tc.reason, out)
			}
			for _, name := range []string{"refused-", "broken-"} {
				if strings.Contains(line, name) {
					t.Fatalf("the refusal line names an account or credential: %q", line)
				}
			}
			if strings.ContainsAny(line, "?&") || strings.Contains(line, "/api/") {
				t.Fatalf("the refusal line carries a request path or query: %q", line)
			}
		})
	}
}

func TestForecastRefusalLogIsThrottledPerReason(t *testing.T) {
	s := tokenServer(t)
	clock := dateAtClose("2026-10-22")
	s.forecastEval.now = func() time.Time { return clock }
	seedForecastReader(t, s, "refused-throttle", scopeNone, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	refuse := func() {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, "refused-throttle", "", "/api/forecast/groups"))
		if w.Code != http.StatusForbidden {
			t.Fatal(w.Code)
		}
	}
	lines := func(out string) []string {
		var found []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "forecast access: refused reason=ou_none") {
				found = append(found, l)
			}
		}
		return found
	}
	out := captureLog(t, func() { refuse(); refuse(); refuse() })
	first := lines(out)
	if len(first) != 1 || !strings.HasSuffix(first[0], " n=1") {
		t.Fatalf("three refusals inside a minute logged %d lines: %q", len(first), out)
	}
	clock = clock.Add(61 * time.Second)
	out = captureLog(t, func() { refuse() })
	second := lines(out)
	if len(second) != 1 || !strings.HasSuffix(second[0], " n=3") {
		t.Fatalf("the next minute's line must say how many refusals it stands for: %q", out)
	}
	if got := signalFlush(t, s)[sigRefusedPrefix+"ou_none"]; got != 4 {
		t.Fatalf("throttling the log lost counts: %d, want 4", got)
	}
}

// The persisted counters and every log line the new signals write carry no account name, no hash
// and no salt: only closed-vocabulary names and integers.
func TestForecastSignalsStoreNoIdentities(t *testing.T) {
	s := tokenServer(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	s.forecastSlowGroups = time.Nanosecond
	const named = "identity-bearing-account-name"
	seedForecastReader(t, s, named, scopeAll, false)
	seedForecastReader(t, s, "identity-bearing-none", scopeNone, false)
	sampleMembers(t, s, 32, nil)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	out := captureLog(t, func() {
		for _, req := range []struct{ user, path string }{
			{named, "/api/forecast/groups"},
			{named, "/api/forecast/groups/members?label=2025-06&checkpoint=d5"},
			{"identity-bearing-none", "/api/forecast/groups"},
			{named, "/api/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30"},
		} {
			mux.ServeHTTP(httptest.NewRecorder(), sessionForPath(t, s, req.user, "", req.path))
		}
	})
	row := signalFlush(t, s)
	if len(row) == 0 {
		t.Fatal("no counters were recorded")
	}
	for name, n := range row {
		if !forecastSignalVocabulary[name] {
			t.Errorf("%q is stored but not in the closed vocabulary", name)
		}
		if n <= 0 {
			t.Errorf("%s = %d", name, n)
		}
	}
	rows, err := s.st.query("SELECT k, v FROM meta WHERE k LIKE 'forecast_signals_%'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		seen++
		if !regexp.MustCompile(`^forecast_signals_\d{4}-\d{2}-\d{2}$`).MatchString(k) {
			t.Errorf("unexpected key %q", k)
		}
		if strings.Contains(k+v, "identity-bearing") {
			t.Errorf("the row names an account: %s=%s", k, v)
		}
	}
	if seen != 1 {
		t.Fatalf("expected one signal row for the test day, found %d", seen)
	}
	for _, l := range strings.Split(string(dumpOf(t, s.st)), "\n") {
		if strings.Contains(l, "forecast_signals") && strings.Contains(l, "identity-bearing") {
			t.Fatalf("a backup line carries a signal row and an account name: %q", l)
		}
	}
	if strings.Contains(out, "identity-bearing") {
		t.Fatalf("a signal log line names an account: %q", out)
	}
}
