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
