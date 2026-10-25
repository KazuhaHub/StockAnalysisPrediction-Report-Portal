package app

// P9: running the evaluator as a service. Everything here is seam-driven: a counting store wrapper
// for the "off does one read" rule, and a direct heartbeat for the readiness and watchdog rules.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// satisfyForecastReadiness makes every §1.4 item hold, so a test that is about something else can
// switch the evaluator on. It seeds a dry pass heartbeat, the ledger start, a gate for every code in
// use, and the alert verification stamp P41 will own.
func satisfyForecastReadiness(t *testing.T, s *Server) {
	t.Helper()
	if _, _, code, err := s.st.SaveForecastPopulationFrom(
		time.Now().UTC().Add(-time.Hour), time.Now().UTC()); err != nil || code != "" {
		t.Fatalf("population: %v %s", err, code)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
		Mode:       forecastModeDry,
		LastTickAt: time.Now().UTC().Format(time.RFC3339),
		Pass: passStats{
			Kind: "tick", StartedAt: time.Now().UTC().Format(time.RFC3339),
			FinishedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatal(err)
	}
	codes, err := s.st.ForecastGateCodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		if err := s.st.RecordForecastDryGate(code, forecastGate{
			At: time.Now().UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
			t.Fatal(err)
		}
	}
	// Item 5 needs a configured channel as well as a recent verified test.
	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAlertTestOKAt, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

// TestOffStepDoesExactlyOneRead: "off" must mean off, not quiet. The counting observer sees one meta
// read and the test store's write counter stays at zero.
func TestOffStepDoesExactlyOneRead(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOff); err != nil {
		t.Fatal(err)
	}
	var reads []string
	s.st.onSettingRead = func(k string) { reads = append(reads, k) }
	defer func() { s.st.onSettingRead = nil }()
	applies := 0
	s.st.onForecastApply = func(int) error { applies++; return nil }
	defer func() { s.st.onForecastApply = nil }()
	fetches := 0
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(context.Context, string, string, string, string) (*QuoteResp, error) {
		fetches++
		return nil, fmt.Errorf("off must not fetch")
	}
	e.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		fetches++
		return nil, fmt.Errorf("off must not fetch")
	}
	e.step(s, "tick")

	if len(reads) != 1 || reads[0] != setForecastAuto {
		t.Fatalf("off step read %v, want exactly [%s]", reads, setForecastAuto)
	}
	if applies != 0 {
		t.Errorf("off step made %d ledger writes", applies)
	}
	if fetches != 0 {
		t.Errorf("off step made %d vendor fetches", fetches)
	}
	if _, ok := s.st.ForecastHeartbeat(); ok {
		t.Error("an off step wrote a heartbeat")
	}
}

// opsFixture seeds one accepted set and returns an evaluator whose two vendor calls are answered by
// a flat stock and index. The clock is late enough for d5 and d20 to be due.
func opsFixture(t *testing.T, s *Server) (int64, *forecastEvaluator) {
	t.Helper()
	setID := seedAcceptedSet(t, s, "600160", "2026-10-17")
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_on='2026-10-16',
		made_at='2026-10-16T13:00:00Z', emitted_base_fen=1000 WHERE id=?`, setID); err != nil {
		t.Fatal(err)
	}
	dates := tradingDays("2026-10-19", 22)
	calDates := append([]string{"2026-10-15", "2026-10-16"}, dates...)
	index := barsFor(calDates, 4000)
	stock := barsFor(calDates, 1000)
	now := shanghai(2026, time.November, 20, 16, 0)
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.adjust = adjustStub(stock)
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		switch code {
		case "000852":
			return &QuoteResp{Bars: index, Kind: quoteKindIndex, BarsSource: "tencent",
				Snapshot: QuoteSnapshot{AsOf: "2026-11-20T15:00:01+08:00"}}, nil
		case "600160":
			return &QuoteResp{Bars: stock, Kind: quoteKindStock, BarsSource: "tencent",
				Snapshot: QuoteSnapshot{AsOf: "2026-11-20T15:00:01+08:00"}}, nil
		}
		return nil, fmt.Errorf("unexpected code %q", code)
	}
	return setID, e
}

// forecastChecksum is a cheap fingerprint of every column a pass can write. A dry pass must leave it
// byte-identical; "no resolved marks" would not catch a baseline write.
func forecastChecksum(t *testing.T, s *Server) string {
	t.Helper()
	var marks, sets string
	if err := s.st.queryRow(`SELECT COALESCE(GROUP_CONCAT(line, ';'), '') FROM (
		SELECT id||','||status||','||refusal||','||attempts||','||session_date||','||source||','||
			next_attempt_at||','||COALESCE(close_fen,-1)||','||COALESCE(low_fen,-1)||','||
			COALESCE(high_fen,-1)||','||COALESCE(ret_bp,0)||','||COALESCE(bench_ret_bp,0) AS line
		FROM prediction_marks ORDER BY id)`).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT COALESCE(GROUP_CONCAT(line, ';'), '') FROM (
		SELECT id||','||state||','||base_check||','||s0_date||','||base_date||','||
			COALESCE(s0_fen,-1) AS line
		FROM prediction_sets ORDER BY id)`).Scan(&sets); err != nil {
		t.Fatal(err)
	}
	return marks + "|" + sets
}

// TestDryRunWritesNothing: the ledger is untouched, and the sample names the vendor that would have
// answered.
func TestDryRunWritesNothing(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	before := forecastChecksum(t, s)

	e.step(s, "manual")

	if after := forecastChecksum(t, s); after != before {
		t.Errorf("dry pass changed the ledger: %s -> %s", before, after)
	}
	hb, ok := s.st.ForecastHeartbeat()
	if !ok || hb.Mode != forecastModeDry {
		t.Fatalf("heartbeat = %+v, ok=%v", hb, ok)
	}
	if len(hb.DrySample) == 0 {
		t.Fatal("dry pass recorded no sample")
	}
	for _, sample := range hb.DrySample {
		if sample.Market == "" || sample.Code == "" || sample.Source != "tencent" {
			t.Errorf("sample lacks the fetch triple: %+v", sample)
		}
	}
}

// TestHealthzForecastIsStatusOnly: 200 or 503, and exactly one key either way.
func TestHealthzForecastIsStatusOnly(t *testing.T) {
	s := newV1Server(t)
	for _, tc := range []struct {
		name string
		mode string
		hb   *forecastHeartbeat
		want int
	}{
		{"off", forecastModeOff, nil, http.StatusOK},
		{"fresh", forecastModeOn, &forecastHeartbeat{LastTickAt: time.Now().UTC().Format(time.RFC3339)}, http.StatusOK},
		{"stale", forecastModeOn, &forecastHeartbeat{LastTickAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}, http.StatusServiceUnavailable},
		{"missing", forecastModeOn, nil, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.st.SetSetting(setForecastAuto, tc.mode); err != nil {
				t.Fatal(err)
			}
			if tc.hb == nil {
				s.st.DeleteSetting(setForecastEvalState)
			} else if err := s.st.saveForecastHeartbeat(*tc.hb); err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			s.handleHealthzForecast(rec, httptest.NewRequest("GET", "/healthz/forecast", nil))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 1 {
				t.Errorf("body has %d keys, want exactly one: %s", len(body), rec.Body.String())
			}
		})
	}
}

// The source-policy gate is appended without changing the first five item keys.
func TestReadinessIncludesLedgerChainItem(t *testing.T) {
	s := newV1Server(t)
	items := s.forecastReadiness(time.Now())
	if len(items) != 6 {
		t.Fatalf("readiness items = %d, want 6", len(items))
	}
	want := []string{"binary", "population", "dry_pass", "dry_gate", "alerts", "ledger_chain"}
	for i, key := range want {
		if items[i].Key != key {
			t.Errorf("item %d = %q, want %q", i, items[i].Key, key)
		}
	}
}

// TestModeOneRequiresReadiness: every item gates the switch, and each failure is named. The
// dry_pass case with a mode-1 heartbeat is the one a naive "errors == 0" check misses.
func TestModeOneRequiresReadiness(t *testing.T) {
	cases := []struct {
		name    string
		disable func(t *testing.T, s *Server)
	}{
		{"population", func(t *testing.T, s *Server) {
			if err := s.st.DeleteSetting(setForecastPopulationFrom); err != nil {
				t.Fatal(err)
			}
		}},
		{"dry_pass", func(t *testing.T, s *Server) {
			if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
				Mode: forecastModeDry, LastTickAt: time.Now().UTC().Format(time.RFC3339),
				Pass: passStats{Kind: "tick", StartedAt: time.Now().UTC().Format(time.RFC3339),
					FinishedAt: time.Now().UTC().Format(time.RFC3339), Errors: 1},
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"dry_pass from a mode-1 pass", func(t *testing.T, s *Server) {
			if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
				Mode: forecastModeOn, LastTickAt: time.Now().UTC().Format(time.RFC3339),
				Pass: passStats{Kind: "tick", StartedAt: time.Now().UTC().Format(time.RFC3339),
					FinishedAt: time.Now().UTC().Format(time.RFC3339)},
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"dry_gate", func(t *testing.T, s *Server) {
			if err := s.st.DeleteSetting(setForecastDryGate); err != nil {
				t.Fatal(err)
			}
		}},
		{"alerts", func(t *testing.T, s *Server) {
			if err := s.st.DeleteSetting(setForecastAlertTestOKAt); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newV1Server(t)
			satisfyForecastReadiness(t, s)
			if items := readinessFailure(s.forecastReadiness(time.Now())); len(items) != 0 {
				t.Fatalf("seeded readiness still fails: %+v", items)
			}
			tc.disable(t, s)
			rec := saveForecastSettings(t, s, `{"mode":"1"}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "forecast_not_ready") {
				t.Errorf("refusal has no code: %s", rec.Body.String())
			}
			if got := s.st.GetSetting(setForecastAuto, "0"); got == forecastModeOn {
				t.Error("a refused switch was persisted")
			}
		})
	}
}

// TestModeOneWithoutGateWritesNothingForThatBenchmark: a set frozen with an unchecked code is planned
// and listed; a checked code is written.
func TestModeOneWithoutGateWritesNothingForThatBenchmark(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	before := forecastChecksum(t, s)

	e.step(s, "manual")

	if after := forecastChecksum(t, s); after != before {
		t.Error("mode 1 wrote for a benchmark with no recorded dry-run check")
	}
	hb, _ := s.st.ForecastHeartbeat()
	if len(hb.AwaitingGate) == 0 || hb.AwaitingGate[0] != "sh000852" {
		t.Errorf("awaiting_gate = %v, want [sh000852]", hb.AwaitingGate)
	}
	// A check for a DIFFERENT benchmark must not unlock this set: planWrites asks about the set's
	// frozen code, not whether any gate exists.
	if err := s.st.RecordForecastDryGate("sh000300", forecastGate{
		At: time.Now().UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
		t.Fatal(err)
	}
	e.step(s, "manual")
	if after := forecastChecksum(t, s); after != before {
		t.Error("a gate for a different benchmark unlocked this set")
	}
	// With the right check recorded, the same pass writes.
	if err := s.st.RecordForecastDryGate("sh000852", forecastGate{
		At: time.Now().UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
		t.Fatal(err)
	}
	e.step(s, "manual")
	if after := forecastChecksum(t, s); after == before {
		t.Error("mode 1 wrote nothing after the dry-run check was recorded")
	}
}

// TestWatchdogDetectsStalledEvaluator: one event when on and stale, nothing at all when off.
func TestWatchdogDetectsStalledEvaluator(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOff); err != nil {
		t.Fatal(err)
	}
	// Off: no alert state is written.
	s.forecastWatchdogOnce(time.Now())
	if raw := s.st.GetSetting(setForecastAlertState, ""); raw != "" {
		t.Errorf("watchdog wrote state while off: %s", raw)
	}
	// On, stale: one emission, then de-duplication inside the hour.
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
		Mode: forecastModeOn, LastTickAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	delivered := make(chan struct{}, 2)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	if _, err := s.st.CreateWebhook(ts.URL, []string{EventForecastEvaluatorUnhealthy}, ""); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	startNotificationRuntimeForTest(t, s)
	s.forecastWatchdogOnce(now)
	state := s.st.forecastAlertState()
	if _, ok := state.Last["stalled"]; !ok {
		t.Fatalf("no stalled alert recorded: %+v", state)
	}
	select {
	case <-delivered:
	case <-time.After(3 * time.Second):
		t.Fatal("the stalled alert was recorded but no event was delivered")
	}
	first := state.Last["stalled"]
	s.forecastWatchdogOnce(now.Add(time.Minute))
	if got := s.st.forecastAlertState().Last["stalled"]; got != first {
		t.Errorf("a second alert %s fired inside the hour: %q -> %q", t.Name(), first, got)
	}
	select {
	case <-delivered:
		t.Error("a de-duplicated alert was delivered again")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestReadinessGateCoversPreLiveBenchCodes: a ledger whose only sets are pre-live still names their
// frozen benchmark, so the page asks for the check that lets those sets settle. Counting only
// in-population sets would leave the list empty at the switch, and every pre-live set would then be
// planned and never written.
func TestReadinessGateCoversPreLiveBenchCodes(t *testing.T) {
	s := newV1Server(t)
	setID, e := opsFixture(t, s) // accepted, non-terminal marks, frozen with the default sh000852
	// Move the ACTIVE benchmark away from the frozen one, so the union is actually exercised: with
	// both at sh000852 this test would pass even if pre-live sets were filtered out.
	if err := s.st.applyForecastBenchState("sh000300", "", ""); err != nil {
		t.Fatal(err)
	}

	codes, err := s.st.ForecastGateCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 2 || codes[0] != "sh000300" || codes[1] != "sh000852" {
		t.Fatalf("gate codes = %v, want [sh000300 sh000852]", codes)
	}
	items := s.forecastReadiness(time.Now())
	var gate *readinessItem
	for i := range items {
		if items[i].Key == "dry_gate" {
			gate = &items[i]
		}
	}
	if gate == nil || gate.OK {
		t.Fatalf("dry_gate item = %+v, want a failure naming sh000852", gate)
	}
	if rec := saveForecastSettings(t, s, `{"mode":"1"}`); rec.Code != http.StatusConflict {
		t.Fatalf("mode 1 before the check: %d %s", rec.Code, rec.Body.String())
	}
	if err := s.st.RecordForecastDryGate("sh000852", forecastGate{
		At: time.Now().UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
		t.Fatal(err)
	}
	for _, it := range s.forecastReadiness(time.Now()) {
		if it.Key == "dry_gate" && !it.OK {
			// sh000300 is the active benchmark and has no check yet.
			if !strings.Contains(it.Detail, "sh000300") {
				t.Errorf("dry_gate detail = %q, want it to name sh000300", it.Detail)
			}
		}
	}
	if err := s.st.RecordForecastDryGate("sh000300", forecastGate{
		At: time.Now().UTC().Format(time.RFC3339), By: "test", Note: "checked"}); err != nil {
		t.Fatal(err)
	}
	for _, it := range s.forecastReadiness(time.Now()) {
		if it.Key == "dry_gate" && !it.OK {
			t.Errorf("dry_gate still fails with both codes checked: %s", it.Detail)
		}
	}

	// The pre-live set is written through the same step once its own code is checked.
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	e.step(s, "manual")
	var status string
	if err := s.st.queryRow(`SELECT status FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != markResolved {
		t.Errorf("pre-live set d5 = %q, want resolved", status)
	}
}

// TestGateCodesAreNeverVacuous: with no accepted set at all, the active benchmark is still listed.
func TestGateCodesAreNeverVacuous(t *testing.T) {
	s := newV1Server(t)
	codes, err := s.st.ForecastGateCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 1 || codes[0] != s.st.ForecastBench() {
		t.Fatalf("gate codes = %v, want the active %q", codes, s.st.ForecastBench())
	}
}

// TestEvaluateNowRespectsModeAndSingleFlight: 409 when off, 409 while a pass holds the guard.
func TestEvaluateNowRespectsModeAndSingleFlight(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOff); err != nil {
		t.Fatal(err)
	}
	if status, code, _ := s.runForecastNow(nil, "admin"); status != http.StatusConflict || code != "forecast_eval_off" {
		t.Errorf("off: %d/%s, want 409/forecast_eval_off", status, code)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	if !s.forecastEval.tryStart() {
		t.Fatal("could not take the guard")
	}
	defer s.forecastEval.finish()
	if status, code, _ := s.runForecastNow(nil, "admin"); status != http.StatusConflict || code != "forecast_eval_running" {
		t.Errorf("running: %d/%s, want 409/forecast_eval_running", status, code)
	}
}

// TestPanicDoesNotKillTheLoop: a panicking pass is counted in the heartbeat and the next step still
// runs. The loop itself is a ticker, so the property is step's.
//
// Driven in mode 1, because a dry TICK does no work at all (ADR 0037, amended 2026-10-24): the
// recovery this pins belongs to the ticker that actually runs a pass, and a dry mode assertion here
// would only prove the skip.
func TestPanicDoesNotKillTheLoop(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e := &forecastEvaluator{now: time.Now, tickFn: func(*Server) error {
		calls++
		panic("boom")
	}}
	e.step(s, "tick")
	e.step(s, "tick")
	if calls != 2 {
		t.Fatalf("panicking step ran %d times, want 2: the loop stopped", calls)
	}
	hb, _ := s.st.ForecastHeartbeat()
	if hb.Pass.Panics != 1 {
		t.Errorf("panics = %d, want 1 (the second step's heartbeat)", hb.Pass.Panics)
	}
}

// TestPanicDoesNotKillTheWatchdog: the same property for the independent goroutine, driven through
// the wrapper the loop uses.
func TestPanicDoesNotKillTheWatchdog(t *testing.T) {
	s := newV1Server(t)
	panics := 0
	s.st.onSettingRead = func(string) {
		panics++
		panic("boom")
	}
	defer func() { s.st.onSettingRead = nil }()
	s.forecastWatchdogSafe(time.Now())
	s.forecastWatchdogSafe(time.Now())
	if panics != 2 {
		t.Fatalf("watchdog ran %d times, want 2: it stopped", panics)
	}
}

// TestWatchdogRunsWhileCleanupIsSlow: the two loops share no lock. A purge holding cleanupMu must not
// delay the watchdog.
func TestWatchdogRunsWhileCleanupIsSlow(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	s.cleanupMu.Lock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		s.cleanupMu.Unlock()
	}()
	start := time.Now()
	s.forecastWatchdogOnce(time.Now())
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("watchdog waited %v for cleanup", elapsed)
	}
}

// TestReadinessFailuresCarryAReasonCode: the page words a failing item in the reader's language from
// a closed reason code and the benchmark codes it names, never from the English detail. A passing
// item carries no reason.
func TestReadinessFailuresCarryAReasonCode(t *testing.T) {
	s := newV1Server(t)
	byKey := func() map[string]readinessItem {
		out := map[string]readinessItem{}
		for _, it := range s.forecastReadiness(time.Now()) {
			out[it.Key] = it
		}
		return out
	}
	items := byKey()
	want := map[string]string{
		"binary":     "",
		"population": "population_unset",
		"dry_pass":   "not_dry",
		"dry_gate":   "gate_missing",
		"alerts":     "alerts_no_channel",
	}
	for key, reason := range want {
		if got := items[key].Reason; got != reason {
			t.Errorf("%s reason = %q, want %q", key, got, reason)
		}
	}
	codes, err := s.st.ForecastGateCodes()
	if err != nil {
		t.Fatal(err)
	}
	if got := items["dry_gate"].Codes; len(got) == 0 || strings.Join(got, ",") != strings.Join(codes, ",") {
		t.Errorf("dry_gate codes = %v, want %v", got, codes)
	}

	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{
		Mode: forecastModeDry, LastTickAt: time.Now().UTC().Format(time.RFC3339),
		Pass: passStats{Kind: "tick", StartedAt: time.Now().UTC().Format(time.RFC3339),
			FinishedAt: time.Now().UTC().Format(time.RFC3339), Errors: 3},
	}); err != nil {
		t.Fatal(err)
	}
	if it := byKey()["dry_pass"]; it.Reason != "dry_pass_errors" || it.Count != 3 {
		t.Errorf("dry_pass = %+v, want reason dry_pass_errors with count 3", it)
	}

	if err := s.st.SetSetting(setForecastAlertEmailTo, "ops@example.com"); err != nil {
		t.Fatal(err)
	}
	if it := byKey()["alerts"]; it.Reason != "alerts_untested" {
		t.Errorf("alerts with a channel and no test = %+v, want reason alerts_untested", it)
	}
}

// TestReadinessReasonsAreTranslated ties the three places a reason code lives: every code the server
// can send (each goes through readinessItem.fail), the TypeScript union the page types it with, and a
// wording in every locale. A code missing from either would reach the reader as a raw key.
func TestReadinessReasonsAreTranslated(t *testing.T) {
	src, err := os.ReadFile("forecast_ops.go")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.fail\("([a-z_]+)"`).FindAllStringSubmatch(string(src), -1) {
		codes[m[1]] = true
	}
	// A scan that finds nothing would pass silently.
	if len(codes) < 10 {
		t.Fatalf("found only %d reason codes; the scan is broken", len(codes))
	}

	types, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "api", "types.ts"))
	if err != nil {
		t.Skipf("web sources not present: %v", err)
	}
	union := regexp.MustCompile(`(?s)export type ForecastReadinessReason =(.*?)\n\n`).FindSubmatch(types)
	if union == nil {
		t.Fatal("ForecastReadinessReason union not found in types.ts")
	}
	typed := map[string]bool{}
	for _, m := range regexp.MustCompile(`'([a-z_]+)'`).FindAllSubmatch(union[1], -1) {
		typed[string(m[1])] = true
	}
	for code := range codes {
		if !typed[code] {
			t.Errorf("reason %q is not in the ForecastReadinessReason union", code)
		}
	}
	for code := range typed {
		if !codes[code] {
			t.Errorf("the union lists %q, which the server never sends", code)
		}
	}

	for _, loc := range []string{"en-US", "zh-CN", "zh-TW"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "locales", loc+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var bundle map[string]string
		if err := json.Unmarshal(raw, &bundle); err != nil {
			t.Fatal(err)
		}
		for code := range codes {
			if bundle["forecast.eval.readiness.reason."+code] == "" {
				t.Errorf("%s has no wording for reason %q", loc, code)
			}
		}
	}
}
