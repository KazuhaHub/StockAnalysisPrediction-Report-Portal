package app

// Benchmark identification and per-code calendar routing (ADR 0029 §6, plan §4.3 F01/F02).
//
// F01: marketPrefix files every 0xxxxx code under Shenzhen, so sh000852 (CSI 1000, a Shanghai index)
// was fetched as sz000852 — a Shenzhen stock. F02: the tick fetched one calendar for sets[0]'s code
// and shared it with every set, so a set frozen with a different index was scored against the wrong
// one. Both are pinned here, against committed Tencent index fixtures and the (market, code) of each
// fetch, so a misrouted request cannot be served the right body by accident.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// indexFixtureBody returns the committed Tencent body for an allowlisted index code, parsed into the
// QuoteResp the vendor call would have produced. The test stub answers with these, so the parsing and
// the kind detection under test are the real ones.
func indexFixtureBody(t *testing.T, code string) *QuoteResp {
	t.Helper()
	body := readQuoteFixture(t, "tencent_fqkline_range_"+code+".json")
	resp, err := parseTencentQuote("sh", code[2:], body, 0)
	if err != nil {
		t.Fatalf("parse fixture %s: %v", code, err)
	}
	return resp
}

// TestBenchAllowlistMapsExplicitly is the F01 pin: the only way a benchmark becomes a vendor request
// is an exact row in the allowlist, and every row is an explicit (market, symbol).
func TestBenchAllowlistMapsExplicitly(t *testing.T) {
	for code, spec := range forecastBenchAllowlist {
		if spec.Market != "sh" {
			t.Errorf("%s: market = %q, want sh (indexes are published by Shanghai)", code, spec.Market)
		}
		if len(spec.Sym) != 6 {
			t.Errorf("%s: sym = %q, want six digits", code, spec.Sym)
		}
		if code != spec.Market+spec.Sym {
			t.Errorf("%s: code != market+sym (%q)", code, spec.Market+spec.Sym)
		}
	}

	// The misroutes marketPrefix would commit: a bare code, a Shenzhen spelling of a Shanghai index,
	// an ordinary stock, its prefixed form, and the empty string. Every one must be refused by
	// benchSpecFor AND by the settings save (400 forecast_bad_bench), storing nothing.
	s := newV1Server(t)
	for _, bad := range []string{"000852", "sz000852", "600519", "sh600519", ""} {
		if spec, ok := benchSpecFor(bad); ok {
			t.Errorf("benchSpecFor(%q) = %+v, want refused", bad, spec)
		}
		rec := httptest.NewRecorder()
		s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast",
			strBody(`{"bench":"`+bad+`"}`)), "admin")
		if rec.Code != http.StatusBadRequest || !hasCode(rec.Body.Bytes(), "forecast_bad_bench") {
			t.Errorf("settings bench=%q status = %d body = %s, want 400 forecast_bad_bench", bad, rec.Code, rec.Body.String())
		}
		if s.st.ForecastBench() != defaultForecastBench {
			t.Errorf("a refused benchmark %q was stored: %q", bad, s.st.ForecastBench())
		}
	}

	// And the four allowlisted codes resolve.
	for _, good := range []string{"sh000300", "sh000905", "sh000852", "sh000906"} {
		spec, ok := benchSpecFor(good)
		if !ok || spec.Market == "" || spec.Sym == "" {
			t.Errorf("benchSpecFor(%q) refused an allowlisted code", good)
		}
	}
}

// TestBenchFixturesExistAndParse: every allowlisted code has a committed Tencent fixture that parses
// and is recognised by the vendor as an index. The routing tests reuse these, so a missing or
// mis-captured fixture fails here rather than silently weakening them.
func TestBenchFixturesExistAndParse(t *testing.T) {
	for _, code := range forecastBenchCodes() {
		resp := indexFixtureBody(t, code)
		if resp.Kind != quoteKindIndex {
			t.Errorf("%s fixture kind = %q, want %q", code, resp.Kind, quoteKindIndex)
		}
		if len(resp.Bars) == 0 {
			t.Errorf("%s fixture has no bars", code)
		}
	}
}

// TestBenchGetListsTheAllowlist: the admin GET advertises the allowed codes so the UI cannot offer one
// the validator would refuse.
func TestBenchGetListsTheAllowlist(t *testing.T) {
	s := newV1Server(t)
	rec := httptest.NewRecorder()
	s.apiForecastSettings(rec, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	codes, ok := got["bench_codes"].([]any)
	if !ok || len(codes) != len(forecastBenchAllowlist) {
		t.Fatalf("bench_codes = %v, want the %d allowlisted codes", got["bench_codes"], len(forecastBenchAllowlist))
	}
}

// TestEvaluatorFetchesEachFrozenBenchmark is the F02 pin: two sets frozen with two different codes in
// one tick cause two calendar fetches, each with the right (market, code), and each mark's benchmark
// return is measured from its OWN index fixture — asserted against a value computed from that fixture,
// not merely against "the two differ".
func TestEvaluatorFetchesEachFrozenBenchmark(t *testing.T) {
	s := newV1Server(t)
	sh000852 := indexFixtureBody(t, "sh000852")
	sh000906 := indexFixtureBody(t, "sh000906")

	const madeOn = "2026-03-02"
	// Two sets, one per code, both with d5 due. The stock series starts on S0 and is flat, so the
	// benchmark leg is exercised by the index alone and its expected value is computable by hand.
	for _, tc := range []struct{ sym, code string }{{"600160", "sh000852"}, {"600161", "sh000906"}} {
		setID := seedAcceptedSet(t, s, tc.sym, madeOn)
		if _, err := s.st.exec("UPDATE prediction_sets SET made_on=?, made_at='2026-03-02T13:00:00Z', bench_code=? WHERE id=?", madeOn, tc.code, setID); err != nil {
			t.Fatal(err)
		}
	}

	type fetchKey struct{ market, code string }
	var fetched []fetchKey
	stock := barsFor(tradingDays("2026-03-03", 10), 1000)
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		// An index is published by Shanghai: any other market means the routing is wrong, and the test
		// says so rather than serving a body that would mask it.
		if market != "sh" {
			return nil, fmt.Errorf("fetch for market %q: an index is only ever sh", market)
		}
		fetched = append(fetched, fetchKey{market, code})
		switch code {
		case "000852":
			return sh000852, nil
		case "000906":
			return sh000906, nil
		case "600160", "600161":
			return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
		}
		return nil, fmt.Errorf("unexpected fetch (%s,%s)", market, code)
	}
	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// Exactly two index fetches, both market sh.
	indexFetches := 0
	for _, f := range fetched {
		if f.code == "000852" || f.code == "000906" {
			indexFetches++
			if f.market != "sh" {
				t.Errorf("index fetch market = %q, want sh", f.market)
			}
		}
	}
	if indexFetches != 2 {
		t.Fatalf("index fetches = %d, want 2 (one per distinct frozen code); all fetches: %v", indexFetches, fetched)
	}

	// The expected benchmark return for each code, computed from that code's own fixture: the index
	// close on the d5 session over its close on S0. A mark routed to the wrong index cannot match.
	want := func(resp *QuoteResp) int32 {
		return expectedBenchRetBp(t, resp, madeOn)
	}
	benchFor := func(symbol string) int32 {
		var v int32
		if err := s.st.queryRow(`SELECT pm.bench_ret_bp FROM prediction_marks pm
			JOIN prediction_sets ps ON ps.id = pm.set_id
			WHERE ps.symbol=? AND pm.checkpoint='d5'`, symbol).Scan(&v); err != nil {
			t.Fatalf("%s d5 bench_ret_bp: %v", symbol, err)
		}
		return v
	}
	if got, w := benchFor("600160"), want(sh000852); got != w {
		t.Errorf("600160 (sh000852) bench_ret_bp = %d, want %d from its own index", got, w)
	}
	if got, w := benchFor("600161"), want(sh000906); got != w {
		t.Errorf("600161 (sh000906) bench_ret_bp = %d, want %d from its own index", got, w)
	}
}

// expectedBenchRetBp derives the d5 benchmark return for a fixture: the index close on the fifth
// session after made_on over its close on S0 (the first session after made_on), in basis points.
// It reads the fixture's own bars, so it is an independent check of the evaluator's routing.
func expectedBenchRetBp(t *testing.T, resp *QuoteResp, madeOn string) int32 {
	t.Helper()
	var sessions []string
	for _, b := range resp.Bars {
		if b.Date > madeOn {
			sessions = append(sessions, b.Date)
		}
	}
	if len(sessions) < 5 {
		t.Fatalf("fixture has %d sessions after %s, need at least 5 for d5", len(sessions), madeOn)
	}
	s0, d5 := sessions[0], sessions[4]
	s0Close := barOn(resp.Bars, s0)
	d5Close := barOn(resp.Bars, d5)
	if s0Close == nil || d5Close == nil {
		t.Fatalf("fixture is missing a bar on %s or %s", s0, d5)
	}
	return bpFromRatio(d5Close.Close, s0Close.Close)
}

// TestBenchmarkBodyMustBeAnIndex: a body the vendor does not call an index cannot settle a mark.
// Whatever the stock leg says, the benchmark leg would be a company's close, so the mark waits.
func TestBenchmarkBodyMustBeAnIndex(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-03-02")
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='2026-03-02', made_at='2026-03-02T13:00:00Z', bench_code='sh000852' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}
	stock := barsFor(tradingDays("2026-03-03", 10), 1000)
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if market != "sh" {
			return nil, fmt.Errorf("fetch for market %q: an index is only ever sh", market)
		}
		// A STOCK body for the index's code.
		return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	// Nothing resolved, and the marks carry the not_index code — not the generic no_bar.
	var resolved int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE status=?", markResolved).Scan(&resolved)
	if resolved != 0 {
		t.Errorf("%d marks resolved against a stock body for the benchmark", resolved)
	}
	assertAllMarksRefusedTo(t, s.st, "not_index")
}

// TestZeroVolumeIndexBarDefersTheGroup: an index body with a zero-volume bar is a broken calendar, so
// the group waits rather than scoring against it, and the marks carry the calendar_bad code. (The
// attempts/finality half of this property is P1's; see TestSourceFailuresAreNotMarkSpecific.)
func TestZeroVolumeIndexBarDefersTheGroup(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-03-02")
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='2026-03-02', made_at='2026-03-02T13:00:00Z', bench_code='sh000852' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}
	indexBars := barsFor(tradingDays("2026-03-03", 10), 4000)
	indexBars[3].Volume = 0 // the zero-volume bar
	stock := barsFor(tradingDays("2026-03-03", 10), 1000)
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if market != "sh" {
			return nil, fmt.Errorf("fetch for market %q: an index is only ever sh", market)
		}
		if code == "000852" {
			return &QuoteResp{Bars: indexBars, Kind: quoteKindIndex, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
		}
		return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var resolved int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE status=?", markResolved).Scan(&resolved)
	if resolved != 0 {
		t.Errorf("%d marks resolved against a zero-volume index body", resolved)
	}
	assertAllMarksRefusedTo(t, s.st, "calendar_bad")
}

// TestBenchFetchFailureIsFetchFailed: a benchmark fetch that errors carries the fetch_failed code,
// distinct from not_index and calendar_bad.
func TestBenchFetchFailureIsFetchFailed(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-03-02")
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='2026-03-02', made_at='2026-03-02T13:00:00Z', bench_code='sh000852' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}
	stock := barsFor(tradingDays("2026-03-03", 10), 1000)
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if code == "000852" {
			return nil, fmt.Errorf("vendor unreachable")
		}
		return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var resolved int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE status=?", markResolved).Scan(&resolved)
	if resolved != 0 {
		t.Errorf("%d marks resolved after a benchmark fetch failure", resolved)
	}
	assertAllMarksRefusedTo(t, s.st, "fetch_failed")
}

// assertAllMarksRefusedTo asserts that the set has at least one refused mark and that every refused
// mark carries the expected refusal code (so a partial or mixed-code write cannot pass).
func assertAllMarksRefusedTo(t *testing.T, st *Store, code string) {
	t.Helper()
	total, matching := 0, 0
	rows, err := st.query(`SELECT refusal FROM prediction_marks WHERE status=?`, markRefused)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		total++
		if r == code {
			matching++
		}
	}
	if total == 0 {
		t.Fatalf("no refused marks; expected them refused to %q", code)
	}
	if matching != total {
		t.Errorf("%d of %d refused marks carry %q, want all", matching, total, code)
	}
}

// TestEmptyBenchCodeNeverReadsTheSetting: a set frozen with an empty code is deferred, and the
// evaluator never falls back to reading forecast_bench to rescue it. The counting store proves the
// absence of the read rather than asserting a value.
func TestEmptyBenchCodeNeverReadsTheSetting(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastBench, "sh000852"); err != nil {
		t.Fatal(err)
	}
	setID := seedAcceptedSet(t, s, "600160", "2026-03-02")
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='2026-03-02', made_at='2026-03-02T13:00:00Z', bench_code='' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}

	var reads []string
	s.st.onSettingRead = func(k string) { reads = append(reads, k) }
	defer func() { s.st.onSettingRead = nil }()

	stock := barsFor(tradingDays("2026-03-03", 10), 1000)
	e := &forecastEvaluator{now: func() time.Time { return shanghai(2026, time.March, 20, 16, 0) }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-03-20T15:00:01+08:00"}}, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}
	for _, k := range reads {
		if k == setForecastBench {
			t.Fatal("the evaluator read forecast_bench for a set whose frozen code is empty: it must defer, not fall back to the setting")
		}
	}
	var refused int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE status=? AND refusal='no_bench'", markRefused).Scan(&refused)
	if refused == 0 {
		t.Error("an empty frozen code produced no deferral")
	}
}

// ---------- P4: effective-dated, audited benchmarks ----------

func saveForecastSettings(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast", strBody(body)), "admin")
	return rec
}

func TestBenchChangeIsEffectiveDatedAndReconciled(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.applyForecastBenchState("sh000300", "", ""); err != nil {
		t.Fatal(err)
	}
	// October was actually frozen with CSI 1000, so the active value is describing the wrong index.
	seedGroupSet(t, s, gsSeed{Symbol: "600160", MadeOn: "2026-10-12", Bench: "sh000852"})

	now := time.Date(2026, time.October, 20, 4, 0, 0, 0, time.UTC) // 12:00 CST
	if err := s.reconcileForecastBench(now); err != nil {
		t.Fatal(err)
	}
	if got := s.st.ForecastBench(); got != "sh000852" {
		t.Errorf("active = %q, want sh000852 (what October was frozen with)", got)
	}
	if next, from := s.st.forecastScheduledBench(); next != "sh000300" || from != "2026-11" {
		t.Errorf("scheduled = %q from %q, want sh000300 from 2026-11", next, from)
	}
	rows := auditRows(t, s, AuditForecastBench)
	if len(rows) != 2 {
		t.Fatalf("bench audit rows = %d, want 2 (active + schedule)", len(rows))
	}
	for _, r := range rows {
		if r.Actor != "system" {
			t.Errorf("reconcile audit actor = %q, want system", r.Actor)
		}
	}
	// This is the reader ingest uses: the October cohort keeps its code, November gets the new one.
	if got := s.st.ForecastBenchFor("2026-10"); got != "sh000852" {
		t.Errorf("For(2026-10) = %q, want sh000852", got)
	}
	if got := s.st.ForecastBenchFor("2026-11"); got != "sh000300" {
		t.Errorf("For(2026-11) = %q, want sh000300", got)
	}

	// A second reconcile sees no disagreement and writes nothing.
	if err := s.reconcileForecastBench(now); err != nil {
		t.Fatal(err)
	}
	if rows := auditRows(t, s, AuditForecastBench); len(rows) != 2 {
		t.Errorf("a no-op reconcile filed %d rows, want 2", len(rows))
	}
}

func TestCSI800ScheduleFromNovember(t *testing.T) {
	s := newV1Server(t)
	from := nextCohort(shanghaiCohort(time.Now()))
	rec := saveForecastSettings(t, s, `{"bench_schedule":"sh000906"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["effective_cohort"] != from {
		t.Errorf("effective_cohort = %v, want %q", resp["effective_cohort"], from)
	}
	if got := s.st.ForecastBench(); got != defaultForecastBench {
		t.Errorf("active = %q: a scheduled change must not re-score the cohort in flight", got)
	}

	// The CST month boundary: 23:59:59 on 31 October is still October; 00:00:00 on 1 November is
	// November. The producer's sandbox runs in UTC, so this is exactly where a mistake lands.
	if got := shanghaiCohort(time.Date(2026, time.October, 31, 15, 59, 59, 0, time.UTC)); got != "2026-10" {
		t.Errorf("cohort at 2026-10-31T15:59:59Z = %q, want 2026-10", got)
	}
	if got := shanghaiCohort(time.Date(2026, time.October, 31, 16, 0, 0, 0, time.UTC)); got != "2026-11" {
		t.Errorf("cohort at 2026-10-31T16:00:00Z = %q, want 2026-11", got)
	}

	// Pin the reader on the fixed cohorts rather than on today's clock.
	if err := s.st.applyForecastBenchState(defaultForecastBench, "sh000906", "2026-11"); err != nil {
		t.Fatal(err)
	}
	if got := s.st.ForecastBenchFor("2026-10"); got != defaultForecastBench {
		t.Errorf("For(2026-10) = %q, want %q", got, defaultForecastBench)
	}
	if got := s.st.ForecastBenchFor("2026-11"); got != "sh000906" {
		t.Errorf("For(2026-11) = %q, want sh000906", got)
	}
}

func TestBenchCancel(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.applyForecastBenchState(defaultForecastBench, "sh000906", "2026-11"); err != nil {
		t.Fatal(err)
	}
	rec := saveForecastSettings(t, s, `{"bench_cancel":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body.String())
	}
	if next, from := s.st.forecastScheduledBench(); next != "" || from != "" {
		t.Errorf("after cancel: %q from %q, want both empty", next, from)
	}
	rows := auditRows(t, s, AuditForecastBench)
	if len(rows) != 1 {
		t.Fatalf("cancel audit rows = %d, want 1", len(rows))
	}
	if !strings.Contains(rows[0].Detail, "sh000906") {
		t.Errorf("cancel detail = %q, want the cancelled code", rows[0].Detail)
	}
	// A second cancel has nothing to clear, so it writes nothing.
	if rec := saveForecastSettings(t, s, `{"bench_cancel":true}`); rec.Code != http.StatusOK {
		t.Fatalf("second cancel: %d %s", rec.Code, rec.Body.String())
	}
	if rows := auditRows(t, s, AuditForecastBench); len(rows) != 1 {
		t.Errorf("second cancel filed %d rows, want 1", len(rows))
	}
}

func TestEnabledAliasesMode(t *testing.T) {
	s := newV1Server(t)
	// The switch is already off: posting off is a no-op, not an audit row.
	if rec := saveForecastSettings(t, s, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("enabled:false: %d %s", rec.Code, rec.Body.String())
	}
	if rows := auditRows(t, s, AuditForecastAuto); len(rows) != 0 {
		t.Fatalf("an unchanged switch filed %d rows, want 0", len(rows))
	}
	if rec := saveForecastSettings(t, s, `{"mode":1}`); rec.Code != http.StatusOK {
		t.Fatalf("mode:1: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.st.GetSetting(setForecastAuto, "0"); got != "1" {
		t.Errorf("mode 1 stored %q, want 1", got)
	}
	if rows := auditRows(t, s, AuditForecastAuto); len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	// The alias and the string spelling both reach the same value and change nothing.
	for _, body := range []string{`{"enabled":true}`, `{"mode":"1"}`} {
		if rec := saveForecastSettings(t, s, body); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rows := auditRows(t, s, AuditForecastAuto); len(rows) != 1 {
		t.Errorf("unchanged saves filed %d rows, want 1", len(rows))
	}
	if rec := saveForecastSettings(t, s, `{"mode":"dry"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("mode dry status = %d, want 400 (P4 implements 0/1 only)", rec.Code)
	}
}

func TestOffBenchmarkSetsAreExcludedAndCounted(t *testing.T) {
	t.Run("a later set with another index is out and counted", func(t *testing.T) {
		s := newV1Server(t)
		seedGroupSet(t, s, gsSeed{Symbol: "600160", MadeOn: "2026-10-12", Bench: "sh000852"})
		seedGroupSet(t, s, gsSeed{Symbol: "600161", MadeOn: "2026-10-13", Bench: "sh000905"})
		sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31")
		if err != nil {
			t.Fatal(err)
		}
		members, _, cov := forecastMembership(sets, time.Time{}, "sh000852")
		if cov.OffBenchmark != 1 {
			t.Errorf("off_benchmark = %d, want 1", cov.OffBenchmark)
		}
		if len(members) != 1 || members[0].Symbol != "600160" {
			t.Errorf("members = %v, want only 600160", symbolsOf(members))
		}
		if sum := cov.Absent + cov.Rejected + cov.Backtest + cov.Test + cov.PreLive +
			cov.Member + cov.Duplicate + cov.Degraded + cov.Mismatch + cov.PendingBaseline +
			cov.OffBenchmark; sum != cov.Reports {
			t.Errorf("coverage sums to %d, want %d", sum, cov.Reports)
		}
	})

	t.Run("an off-benchmark earliest set takes the symbol out", func(t *testing.T) {
		s := newV1Server(t)
		seedGroupSet(t, s, gsSeed{Symbol: "600160", MadeOn: "2026-10-01", Bench: "sh000905"})
		seedGroupSet(t, s, gsSeed{Symbol: "600160", MadeOn: "2026-10-02", Bench: "sh000852"})
		sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31")
		if err != nil {
			t.Fatal(err)
		}
		members, _, cov := forecastMembership(sets, time.Time{}, "sh000852")
		if cov.OffBenchmark != 2 || len(members) != 0 {
			t.Errorf("off_benchmark=%d members=%v, want 2 and none (the symbol is out)", cov.OffBenchmark, symbolsOf(members))
		}
	})
}

func symbolsOf(sets []*groupSet) []string {
	out := make([]string, 0, len(sets))
	for _, gs := range sets {
		out = append(out, gs.Symbol)
	}
	return out
}

func TestCustomRangeSlicesByBenchmark(t *testing.T) {
	s := newV1Server(t)
	seedGroupSet(t, s, gsSeed{Symbol: "600160", MadeOn: "2026-10-12", Bench: "sh000852"})
	seedGroupSet(t, s, gsSeed{Symbol: "600161", MadeOn: "2026-11-12", Bench: "sh000906"})
	sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-11-30")
	if err != nil {
		t.Fatal(err)
	}
	periods := []forecastPeriod{{Start: "2026-10-01", End: "2026-11-30", Label: "2026-10..11"}}
	groups := s.buildForecastGroups(periods, sets, predTestRtype, "2026-12-01", false, time.Time{})
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2 (one per benchmark)", len(groups))
	}
	seen := map[string]bool{}
	for _, g := range groups {
		seen[g.Bench] = true
		if g.Coverage.Member != 1 {
			t.Errorf("group %q member = %d, want 1", g.Label, g.Coverage.Member)
		}
		if !strings.Contains(g.Label, g.Bench) {
			t.Errorf("label %q does not name the benchmark %q", g.Label, g.Bench)
		}
	}
	if !seen["sh000852"] || !seen["sh000906"] {
		t.Errorf("benchmarks = %v, want sh000852 and sh000906", seen)
	}
}

func TestBenchPromotionAtBoot(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.applyForecastBenchState("sh000852", "sh000906", "2026-10"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 20, 4, 0, 0, 0, time.UTC)
	s.promoteForecastBench(now)
	if got := s.st.ForecastBench(); got != "sh000906" {
		t.Errorf("active = %q, want sh000906 after promotion", got)
	}
	if next, from := s.st.forecastScheduledBench(); next != "" || from != "" {
		t.Errorf("after promotion: %q from %q, want both empty", next, from)
	}
	rows := auditRows(t, s, AuditForecastBench)
	if len(rows) != 1 || rows[0].Actor != "system" || !strings.Contains(rows[0].Detail, "promote") {
		t.Fatalf("promotion audit = %+v, want one system row with reason promote", rows)
	}
	// A second boot has nothing due.
	s.promoteForecastBench(now)
	if rows := auditRows(t, s, AuditForecastBench); len(rows) != 1 {
		t.Errorf("a second boot filed %d rows, want 1", len(rows))
	}
}

func TestIngestFreezesTheCohortsBenchmark(t *testing.T) {
	s := newV1Server(t)
	benches := func() string {
		var b string
		if err := s.st.queryRow("SELECT bench_code FROM prediction_sets ORDER BY id DESC LIMIT 1").Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	code, m := ingestPost(t, s, ingestBody(string(predWire(nil))))
	if code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, m)
	}
	if got := benches(); got != defaultForecastBench {
		t.Errorf("frozen bench = %q, want %q", got, defaultForecastBench)
	}

	// A change that is due for the CURRENT cohort (not yet promoted) is already what ingest reads.
	if err := s.st.applyForecastBenchState(defaultForecastBench, "sh000906", shanghaiCohort(time.Now())); err != nil {
		t.Fatal(err)
	}
	body := replaceOnce(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"600161"`)
	code, m = ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("second ingest: %d %v", code, m)
	}
	if got := benches(); got != "sh000906" {
		t.Errorf("frozen bench = %q, want sh000906 from the current cohort's schedule", got)
	}
}
