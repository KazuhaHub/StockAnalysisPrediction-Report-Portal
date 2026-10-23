package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"
)

func capturedHaltBody(t *testing.T, symbol string) *QuoteResp {
	t.Helper()
	r, err := parseTencentQuote(symbol[:2], symbol[2:],
		readQuoteFixture(t, "tencent_fqkline_halt_"+symbol+".json"), 0)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func insertCapturedForecast(t *testing.T, s *Server, symbol, price string, receipt time.Time) int64 {
	t.Helper()
	date := receipt.In(chinaZone).Format("2006-01-02")
	set, claims, problems := classifyPredictions(predInput{
		Raw: predWire(func(m map[string]any) {
			m["baseline"] = map[string]any{"price": price, "as_of": date, "source": "company_quote"}
		}), Rtype: predTestRtype, Symbol: symbol, MadeAt: receipt, BodySHA: "halt-body", BenchCode: "sh000852",
	})
	if len(problems) != 0 || set == nil || set.State != predStateAccepted {
		t.Fatalf("real classifier refused the halt fixture: %+v / %+v", set, problems)
	}
	reportID, _, err := s.st.UpsertReport(Rep{Symbol: symbol, Date: date, RType: predTestRtype,
		Title: "Suspended-stock evidence", MD: "# Suspended-stock evidence"})
	if err != nil {
		t.Fatal(err)
	}
	setID, _, err := s.st.InsertPredictionSet(reportID, set, claims)
	if err != nil {
		t.Fatal(err)
	}
	return setID
}

// Current, independently announced suspensions: neither body prints a candle during its halt.
// A short halt must resolve on current vendor evidence, not pretend three sessions have elapsed.
func TestCapturedShortHaltNeedsCurrentEvidence(t *testing.T) {
	stock, index := capturedHaltBody(t, "sh601326"), capturedHaltBody(t, "sh000852")
	now := shanghai(2026, time.October, 22, 17, 0)
	sessions := completedSessions(forecastSeries{Bars: index.Bars}, now)
	if stock.Bars[len(stock.Bars)-1].Date != "2026-10-20" ||
		stock.Bars[len(stock.Bars)-1].Close != 357 || !stampProvesCurrent(stock.Snapshot.AsOf, "2026-10-21") {
		t.Fatal("short-halt capture no longer provides the documented close and current evidence")
	}
	if got := sessionsPastDue(sessions, "2026-10-21", now); got != 1 {
		t.Fatalf("confirmation sessions = %d, want 1 (not enough to confirm)", got)
	}
	base := barOn(stock.Bars, "2026-10-14")
	if base == nil {
		t.Fatal("missing traded baseline")
	}
	in := settleInput{
		Set: forecastWorkSet{Symbol: "601326"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: stock.Bars, Index: index.Bars, Sessions: sessions, Source: stock.Source,
		AsOf: stock.Snapshot.AsOf, S0: base.Date, BaseDate: base.Date, S0Fen: base.Close,
		Session: "2026-10-21", Now: now, Valve: true,
	}
	result := settleMark(in)
	if result.Status != markResolved || !result.Halted || result.CloseFen == nil || *result.CloseFen != 357 {
		t.Fatalf("current evidence must beat the stale-feed valve: %+v", result)
	}
	// Deliberate input mutation, not a substituted capture: remove the only current evidence.
	in.AsOf, in.Valve = "", false
	if result := settleMark(in); result.Status != markRefused || result.Refusal != "stale_bar" {
		t.Fatalf("short halt without evidence was assumed confirmed: %+v", result)
	}
	in.Valve = true
	if result := settleMark(in); result.Status != markRefused || result.Refusal != "calendar_lag" {
		t.Fatalf("lag valve was ignored without current evidence: %+v", result)
	}
}

func TestCapturedHaltOnS0SettlesThroughEvaluator(t *testing.T) {
	stock, index := capturedHaltBody(t, "sz301314"), capturedHaltBody(t, "sh000852")
	now := shanghai(2026, time.October, 22, 17, 0)
	if last := stock.Bars[len(stock.Bars)-1]; last.Date != "2026-10-12" || last.Close != 4678 {
		t.Fatalf("halt capture's last traded bar = %+v", last)
	}
	s := newV1Server(t)
	// Use the real classifier and transactional ledger writer, with an explicit historical receipt.
	receipt := shanghai(2026, time.October, 12, 21, 0)
	setID := insertCapturedForecast(t, s, "301314", "46.78", receipt)
	stockFetches, adjustedFetches := 0, 0
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.fetch = func(_ context.Context, market, code, _, _ string) (*QuoteResp, error) {
		switch market + code {
		case "sh000852":
			return index, nil
		case "sz301314":
			stockFetches++
			return stock, nil
		default:
			return nil, fmt.Errorf("unexpected vendor target %s%s", market, code)
		}
	}
	e.adjust = func(_ context.Context, market, code, from, to string) (map[string]int64, error) {
		adjustedFetches++
		return replayForecastAdjustmentBody(t, market, code, from, to,
			readQuoteFixture(t, "tencent_fqkline_halt_qfq_sz301314.json"))
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var s0, base, check string
	var price int64
	if err := s.st.queryRow(`SELECT s0_date, base_date, s0_fen, base_check FROM prediction_sets WHERE id=?`, setID).
		Scan(&s0, &base, &price, &check); err != nil {
		t.Fatal(err)
	}
	if s0 != "2026-10-13" || base != "2026-10-12" || price != 4678 || check != baseMatch {
		t.Fatalf("halt baseline = %s/%s/%d/%s", s0, base, price, check)
	}
	var status, session, source string
	var halted, corp, ret, bench int
	if err := s.st.queryRow(`SELECT status, session_date, halted, corp_action, close_fen, ret_bp, bench_ret_bp, source
		FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID).
		Scan(&status, &session, &halted, &corp, &price, &ret, &bench, &source); err != nil {
		t.Fatal(err)
	}
	// Index closes: 7023.22 on S0 and 6766.90 on d5; using base_date's 7021.47 would give -363.
	if status != markResolved || session != "2026-10-20" || halted != 1 || corp != 0 || price != 4678 ||
		ret != 0 || bench != -365 || source != quoteSourceTencent || stockFetches == 0 || adjustedFetches != 1 {
		t.Fatalf("real evaluator mark = %s/%s halt=%d corp=%d close=%d return=%d bench=%d source=%s fetches=%d/%d",
			status, session, halted, corp, price, ret, bench, source, stockFetches, adjustedFetches)
	}
}

func TestCapturedDelistedSnapshotCannotBecomeABaseline(t *testing.T) {
	now := shanghai(2026, time.October, 22, 17, 0)
	load := func(suffix string) *QuoteResp {
		t.Helper()
		r, err := parseTencentQuote("sh", "600485", readQuoteFixture(t,
			"tencent_fqkline_delisted_sh600485_"+suffix+".json"), 0)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	recent, historical := load("recent"), load("2019")
	if len(recent.Bars) != 0 || len(historical.Bars) != 117 || recent.Snapshot.Last != 139 {
		t.Fatalf("delisted evidence shape changed: recent=%d historical=%d snapshot=%d",
			len(recent.Bars), len(historical.Bars), recent.Snapshot.Last)
	}
	index := capturedHaltBody(t, "sh000852")
	sessions := completedSessions(forecastSeries{Bars: index.Bars}, now)
	for _, stock := range [][]QuoteBar{recent.Bars, historical.Bars} {
		s0, _, _, err := resolveStartBar(stock,
			forecastWorkSet{Symbol: "600485", MadeOn: "2026-10-20"},
			shanghai(2026, time.October, 20, 21, 0), sessions)
		if !errors.Is(err, errNoBaseInWindow) || s0 != "2026-10-21" {
			t.Fatalf("a stale snapshot/2019 candle supplied a 2026 baseline: s0=%s err=%v", s0, err)
		}
	}
	// Exercise the real pipeline too: a future change in rangeBars must not manufacture a candle
	// from the current-looking snapshot before the pure baseline function receives its input.
	s := newV1Server(t)
	setID := insertCapturedForecast(t, s, "600485", "1.39", shanghai(2026, time.October, 12, 21, 0))
	stockFetches, adjustedFetches := 0, 0
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.fetch = func(_ context.Context, market, code, _, _ string) (*QuoteResp, error) {
		switch market + code {
		case "sh000852":
			return index, nil
		case "sh600485":
			stockFetches++
			return recent, nil
		default:
			return nil, fmt.Errorf("unexpected vendor target %s%s", market, code)
		}
	}
	e.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		adjustedFetches++
		return nil, errors.New("no valid stock leg to adjust")
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var baseline sql.NullInt64
	if err := s.st.queryRow(`SELECT s0_fen FROM prediction_sets WHERE id=?`, setID).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	var resolved, sourceFailure, attempts int
	if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND status='resolved'`, setID).
		Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks
		WHERE set_id=? AND status='refused' AND refusal='fetch_failed' AND attempts=0`, setID).
		Scan(&sourceFailure); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT COALESCE(SUM(attempts),0) FROM prediction_marks WHERE set_id=?`, setID).
		Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	// Empty history is a source fault: a fresh-looking snapshot cannot prove a traded close or
	// justify finalizing the set. The instrument's delisting is not inferred by this parser.
	if baseline.Valid || resolved != 0 || sourceFailure == 0 || attempts != 0 || stockFetches != 1 || adjustedFetches != 0 {
		t.Fatalf("delisted pipeline: baseline=%v resolved=%d source-failures=%d attempts=%d fetches=%d/%d",
			baseline, resolved, sourceFailure, attempts, stockFetches, adjustedFetches)
	}
}
