package app

// P7 settlement rules (plan §5.3): the baseline check, halts, completeness, the consistency checks,
// the daily-limit backstop and provenance. Each test states its direction.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestBaselineCheckFollowsDesignTable(t *testing.T) {
	sessions := []string{"2026-10-15", "2026-10-16", "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22"}
	closes := []int64{990, 1010, 1000, 1005, 1008, 1011}
	var stock []QuoteBar
	for i, d := range sessions {
		stock = append(stock, bar(d, closes[i], closes[i], closes[i]+10, closes[i]-10))
	}
	stock[2].Low, stock[2].High = 1000, 1050 // the receipt session's range

	at := func(h, mi int) time.Time { return shanghai(2026, time.October, 19, h, mi) }
	ptr := func(v int64) *int64 { return &v }
	cases := []struct {
		name    string
		madeAt  time.Time
		emitted *int64
		want    string
	}{
		{"intraday inside [low, high]", at(10, 0), ptr(1020), baseMatch},
		{"intraday on the low edge", at(10, 0), ptr(1000), baseMatch},
		{"intraday equal to the previous close", at(10, 0), ptr(1010), baseMatch},
		{"intraday two sessions back mismatches", at(10, 0), ptr(990), baseMismatch},
		{"intraday outside the range", at(10, 0), ptr(2000), baseMismatch},
		{"nil emitted", at(10, 0), nil, baseMismatch},
		{"pre-open compares with the previous session", at(8, 0), ptr(1010), baseMatch},
		{"pre-open with the receipt day's close", at(8, 0), ptr(1000), baseMismatch},
		{"after close uses the last close", at(16, 0), ptr(1000), baseMatch},
		{"after close uses the one before", at(16, 0), ptr(1010), baseMatch},
		{"after close, two sessions back", at(16, 0), ptr(990), baseMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ready := predBaseCheck(tc.madeAt, tc.emitted, stock, sessions, "")
			if !ready {
				t.Fatal("check not ready")
			}
			if got != tc.want {
				t.Errorf("check = %q, want %q", got, tc.want)
			}
		})
	}
	// The check waits until the receipt session's bar is complete.
	if _, ready := predBaseCheck(at(10, 0), ptr(1020), stock[:2], sessions[:2], ""); ready {
		t.Error("the check decided before the receipt session completed")
	}
}

// TestBackdatedAsOfCannotTurnMismatchIntoMatch: emitted_base_as_of is a diagnostic. The reference
// session comes from made_at, so a body stamped with a date when the price did match cannot make a
// current mismatch into a match.
func TestBackdatedAsOfCannotTurnMismatchIntoMatch(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	dates := tradingDays("2026-10-13", 8)
	index := barsFor(dates, 4000)
	stock := barsFor(dates, 1000)
	stock[1].Close = 980  // 10-14, three sessions before the receipt
	stock[2].Close = 1000 // 10-15
	stock[3].Close = 1010 // 10-16, the reference session
	// The emitted price matched 10-14, and the producer says so. The check must still use the
	// receipt session (10-16), where it matches neither close.
	if _, err := s.st.exec(`UPDATE prediction_sets SET emitted_base_fen=980,
		emitted_base_as_of='2026-10-14' WHERE id=?`, setID); err != nil {
		t.Fatal(err)
	}
	now := shanghai(2026, time.October, 27, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var check string
	s.st.queryRow("SELECT base_check FROM prediction_sets WHERE id=?", setID).Scan(&check)
	if check != baseMismatch {
		t.Errorf("base_check = %q, want mismatch: a backdated as_of must not rescue the price", check)
	}
}

func haltInput(sessions []string, lastTraded int, asOf string, now time.Time) settleInput {
	return settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: barsFor(sessions[:lastTraded], 1000), Index: barsFor(sessions, 4000),
		Sessions: sessions, AsOf: asOf,
		S0: sessions[0], BaseDate: sessions[0], S0Fen: 1000, Session: sessions[10], Now: now,
	}
}

// TestHaltResolvesOnEvidenceOrConfirmation: a missing bar is a halt only when the vendor's own stamp
// covers the session, or three sessions have passed in silence. Until then it is retryable.
func TestHaltResolvesOnEvidenceOrConfirmation(t *testing.T) {
	all := tradingDays("2026-10-20", 20)
	earlyNow := shanghai(2026, time.November, 4, 15, 31) // only one completed session past the due
	early := all[:12]
	now := shanghai(2026, time.November, 30, 15, 31)

	// Within three sessions, no current stamp: never final, retryable.
	res := settleMark(haltInput(early, 8, all[7]+"T15:00:00+08:00", earlyNow))
	if res.Status != markRefused || res.Refusal != "stale_bar" {
		t.Errorf("early halt = %s/%s, want refused/stale_bar", res.Status, res.Refusal)
	}
	// A stamp covering the session resolves at once.
	res = settleMark(haltInput(early, 8, all[10]+"T15:00:00+08:00", earlyNow))
	if res.Status != markResolved || !res.Halted || res.CloseFen == nil || *res.CloseFen != 1000 {
		t.Errorf("evidenced halt = %s/%v/%v, want resolved/halted at 1000", res.Status, res.Halted, res.CloseFen)
	}
	// Three completed sessions later, confirmation is enough.
	res = settleMark(haltInput(early, 8, "", earlyNow))
	if res.Status != markRefused {
		t.Errorf("confirmation before three sessions = %s, want refused", res.Status)
	}
	res = settleMark(haltInput(all, 8, "", now))
	if res.Status != markResolved || !res.Halted {
		t.Errorf("confirmed halt = %s/%v, want resolved/halted", res.Status, res.Halted)
	}
	// A same-day 14:57 stamp is a body fetched before the close: it cannot confirm a halt.
	res = settleMark(haltInput(early, 8, all[10]+"T14:57:00+08:00", earlyNow))
	if res.Status != markRefused || res.Refusal != "stale_bar" {
		t.Errorf("partial same-day body = %s/%s, want refused/stale_bar", res.Status, res.Refusal)
	}
	// The valve says the whole feed is behind: nothing is written, nothing counts.
	in := haltInput(all, 8, "", now)
	in.Valve = true
	res = settleMark(in)
	if res.Status != markRefused || res.Refusal != "calendar_lag" {
		t.Errorf("valve = %s/%s, want refused/calendar_lag", res.Status, res.Refusal)
	}
}

// TestHaltedOnS0UsesLastPreS0Close: a stock halted on the start session is measured from its last
// traded close BEFORE S0, and the benchmark leg still runs from S0.
func TestHaltedOnS0UsesLastPreS0Close(t *testing.T) {
	dates := tradingDays("2026-10-19", 20)
	stock := barsFor(dates, 1000)
	// The stock trades before S0 at 900, then is halted from S0 onward.
	pre := bar(shiftDays(dates[0], -3), 900, 900, 910, 890)
	stock = []QuoteBar{pre}
	index := append([]QuoteBar{bar(pre.Date, 4000, 4000, 4010, 3990)}, barsFor(dates, 4000)...)
	if b := barOn(index, dates[5]); b != nil {
		b.Close = 4400 // the index moved after S0
	}

	s0, base, fen, err := resolveStartBar(stock, forecastWorkSet{MadeOn: dates[0]}, shanghai(2026, time.October, 19, 8, 0), dates)
	if err != nil || s0 != dates[0] || base != pre.Date || fen != 900 {
		t.Fatalf("halted baseline = %s/%s/%d (%v), want %s/%s/900", s0, base, fen, err, dates[0], pre.Date)
	}
	res := settleMark(settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: stock, Index: index, Sessions: dates, AsOf: dates[19] + "T15:00:00+08:00",
		S0: s0, BaseDate: base, S0Fen: fen, Session: dates[5],
		Now: shanghai(2026, time.November, 30, 15, 31),
	})
	if res.Status != markResolved || !res.Halted {
		t.Fatalf("d5 = %s/%v, want resolved/halted", res.Status, res.Halted)
	}
	if res.RetBp == nil || *res.RetBp != 0 {
		t.Errorf("ret = %v, want 0 (the close did not move while halted)", res.RetBp)
	}
	want := bpFromRatio(4400, 4000)
	if res.BenchRetBp == nil || *res.BenchRetBp != want {
		t.Errorf("bench_ret_bp = %v, want %d from S0", res.BenchRetBp, want)
	}
}

// TestHaltLongerThanTheWindowFetchesBack: when the ten-day window has no price, the evaluator widens
// it once (400 days) rather than declaring the baseline unknowable.
func TestHaltLongerThanTheWindowFetchesBack(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	dates := tradingDays("2026-10-19", 10)
	index := barsFor(dates, 4000)
	old := bar("2026-08-03", 900, 900, 910, 890) // outside the ten-day window, inside 400 days
	stock := append([]QuoteBar{old}, barsFor(dates[6:], 1000)...)

	now := shanghai(2026, time.October, 27, 16, 0)
	e := &forecastEvaluator{now: func() time.Time { return now }}
	var wides int
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		asOf := now.Format("2006-01-02") + "T15:00:01+08:00"
		if code == "000852" {
			return &QuoteResp{Bars: index, Kind: quoteKindIndex, Snapshot: QuoteSnapshot{AsOf: asOf}}, nil
		}
		if from <= "2026-09-01" {
			wides++
			return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: asOf}}, nil
		}
		return &QuoteResp{Bars: stock[1:], Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: asOf}}, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	if wides != 1 {
		t.Errorf("wide fetches = %d, want exactly one bounded look-back", wides)
	}
	var base string
	var fen int64
	if err := s.st.queryRow("SELECT base_date, s0_fen FROM prediction_sets WHERE id=?", setID).Scan(&base, &fen); err != nil {
		t.Fatal(err)
	}
	if base != old.Date || fen != 900 {
		t.Errorf("baseline = %s/%d, want %s/900", base, fen, old.Date)
	}
}

func TestLimitBackstopNeverFlagsATradingDay(t *testing.T) {
	notFlagged := []struct {
		name     string
		symbol   string
		prev, to int64
	}{
		{"+15% on ChiNext", "301272", 10000, 11500},
		{"+15% on STAR", "689009", 10000, 11500},
		{"+25% on Beijing", "830799", 10000, 12500},
		{"cheap stock's +10.48%", "600160", 105, 116},
		{"a limit-down day", "600160", 1000, 900},
		{"exactly at the rounded limit", "600160", 1000, 1100},
	}
	for _, tc := range notFlagged {
		if beyondLimitPrice(tc.prev, tc.to, predDailyLimitBp(tc.symbol)) {
			t.Errorf("%s was flagged", tc.name)
		}
	}
	// One fen beyond the rounded limit price IS impossible as trading.
	if !beyondLimitPrice(1000, 1102, predDailyLimitBp("600160")) {
		t.Error("one fen beyond the limit was not flagged")
	}
}

// TestLimitBackstopLeavesListingsFirstBarsAlone is the "first five bars" rule: a series that begins
// inside the fetch window is a listing's no-limit days and is never a corporate action.
func TestLimitBackstopLeavesListingsFirstBarsAlone(t *testing.T) {
	dates := tradingDays("2026-10-19", 20)
	floor := dates[0]
	// The series begins a fortnight after the fetch floor: a listing. Its first bar is four times
	// the next one, which no limit allows, and is exempt.
	stock := barsFor(dates[10:], 1000)
	stock[0].Close = 4000
	if corporateActionBetween("600160", stock, nil, nil, dates[10], dates[15], floor) {
		t.Error("a listing's first bar was flagged as a corporate action")
	}
	// The same move on a stock that was already trading at the floor IS a corporate action.
	normal := barsFor(dates, 1000)
	normal[10].Close = 4000
	if !corporateActionBetween("600160", normal, nil, nil, dates[0], dates[15], floor) {
		t.Error("an impossible move on an established series was not flagged")
	}
}

func TestMissingBenchBarIsUnverifiableNotResolved(t *testing.T) {
	dates := tradingDays("2026-10-19", 10)
	stock := barsFor(dates, 1000)
	index := append(barsFor(dates[:9], 4000), barsFor(dates[9:], 4000)[1:]...)
	if barOn(index, dates[9]) != nil {
		t.Fatal("setup: the due bar is still present")
	}
	res := settleMark(settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: stock, Index: index, Sessions: dates, AsOf: dates[9] + "T15:00:00+08:00",
		S0: dates[0], BaseDate: dates[0], S0Fen: 1000, Session: dates[9],
		Now: shanghai(2026, time.November, 30, 15, 31),
	})
	if res.Status != markRefused || res.Refusal != "unverifiable" {
		t.Errorf("missing bench bar = %s/%s, want refused/unverifiable", res.Status, res.Refusal)
	}
	if res.BenchRetBp != nil {
		t.Errorf("bench_ret_bp = %v, want nil (never a made-up benchmark)", res.BenchRetBp)
	}
}

func TestNewestBarRule(t *testing.T) {
	b := bar("2026-10-22", 1000, 1000, 1010, 990)
	// Thursday 01:00: Wednesday's bar is final.
	if final, unv := newestBarFinal(bar("2026-10-21", 1000, 1000, 1010, 990),
		"2026-10-21T15:00:00+08:00", shanghai(2026, time.October, 22, 1, 0)); !final || unv {
		t.Error("yesterday's bar was not final")
	}
	// A 14:57 stamp on the bar's own date is a partial bar.
	if final, _ := newestBarFinal(b, "2026-10-22T14:57:03+08:00", shanghai(2026, time.October, 22, 15, 31)); final {
		t.Error("a 14:57 stamp was believed after the close")
	}
	// A body stamped on the bar's date and after the close, read after 15:30, is final.
	if final, _ := newestBarFinal(b, "2026-10-22T15:00:01+08:00", shanghai(2026, time.October, 22, 15, 31)); !final {
		t.Error("a closed stamp was not believed after 15:30")
	}
	// No stamp on today's bar: unverifiable, not final.
	if final, unv := newestBarFinal(b, "", shanghai(2026, time.October, 22, 15, 31)); final || !unv {
		t.Error("an unstamped bar dated today was believed")
	}
	// A suspended stock stamped 09:00 keeps its LAST TRADED bar (dated before the stamp).
	last := bar("2026-10-21", 1000, 1000, 1010, 990)
	if final, unv := newestBarFinal(last, "2026-10-22T09:00:00+08:00", shanghai(2026, time.October, 22, 15, 31)); !final || unv {
		t.Error("a suspended stock's last traded bar was not final")
	}
}

func TestCalendarAndAnchorChecks(t *testing.T) {
	dates := tradingDays("2026-10-19", 10)
	stock := barsFor(dates, 1000)
	index := barsFor(dates, 4000)
	in := settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: stock, Index: index, Sessions: dates, AsOf: dates[9] + "T15:00:00+08:00",
		S0: dates[0], BaseDate: dates[0], S0Fen: 1000, Session: dates[9],
		Now: shanghai(2026, time.November, 30, 15, 31),
	}
	if res := settleMark(in); res.Status != markResolved {
		t.Fatalf("setup: %s/%s", res.Status, res.Refusal)
	}
	// A stock bar off the index calendar.
	off := append([]QuoteBar(nil), stock...)
	off = append(off, bar("2026-10-25", 1000, 1000, 1010, 990)) // a Sunday the index does not print
	if res := settleMark(withStock(in, off)); res.Refusal != "calendar_mismatch" {
		t.Errorf("off-calendar stock bar = %q, want calendar_mismatch", res.Refusal)
	}
	// The stored start price differs from the body's base close.
	moved := append([]QuoteBar(nil), stock...)
	moved[0].Close = 900
	if res := settleMark(withStock(in, moved)); res.Refusal != "anchor_mismatch" {
		t.Errorf("moved anchor = %q, want anchor_mismatch", res.Refusal)
	}
}

func withStock(in settleInput, stock []QuoteBar) settleInput {
	in.Stock = stock
	return in
}

// TestIndexWideCalendarFaultsNeverCountTowardFinality: a zero-volume index bar and a stored s0_date
// the calendar does not reproduce are source faults. Twelve failed sessions leave every mark refused
// with attempts unchanged and none final.
func TestIndexWideCalendarFaultsNeverCountTowardFinality(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(index *forecastSeries, s *Server, setID int64)
	}{
		{"zero-volume index bar", func(index *forecastSeries, _ *Server, _ int64) {
			index.Bars[3].Volume = 0
		}},
		{"non-reproduced s0", func(_ *forecastSeries, s *Server, setID int64) {
			if _, err := s.st.exec(`UPDATE prediction_sets SET s0_date='2026-10-30' WHERE id=?`, setID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newV1Server(t)
			setID := seedAcceptedSetAt(t, s, "600160", "2026-10-19") // S0 2026-10-19
			dates := tradingDays("2026-10-19", 14)
			index := forecastSeries{Bars: barsFor(dates, 4000), Kind: quoteKindIndex}
			stock := barsFor(dates, 1000)
			tc.setup(&index, s, setID)
			now := shanghai(2026, time.November, 10, 16, 0)
			e := evalStub(&now, index.Bars, stock, nil, nil)
			for i := 0; i < 4; i++ {
				if err := e.tick(s); err != nil {
					t.Fatal(err)
				}
			}
			var status, refusal string
			var attempts int
			if err := s.st.queryRow(`SELECT status, refusal, attempts FROM prediction_marks
				WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&status, &refusal, &attempts); err != nil {
				t.Fatal(err)
			}
			if status != markRefused || attempts != 0 {
				t.Errorf("mark = %s/%d, want refused/0", status, attempts)
			}
			if refusal != "calendar_bad" {
				t.Errorf("refusal = %q, want calendar_bad", refusal)
			}
		})
	}
}

func TestSourceFailuresLeaveAttemptsAlone(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-19")
	var markID int64
	s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID)
	mark := forecastMark{ID: markID, Status: markPending}
	for _, code := range []string{"fetch_failed", "no_bench", "not_index", "no_source", "calendar_lag", "calendar_bad"} {
		if refusalClassOf(code) != refusalSource {
			t.Errorf("%s is not source-class", code)
		}
		if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, defers: []forecastDeferGroup{
			{marks: []forecastMark{mark}, code: code},
		}}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	s.st.queryRow("SELECT attempts FROM prediction_marks WHERE id=?", markID).Scan(&attempts)
	if attempts != 0 {
		t.Errorf("attempts = %d after source failures, want 0", attempts)
	}
}

func TestSettleRecordsTheAnsweringSource(t *testing.T) {
	dates := tradingDays("2026-10-19", 10)
	res := settleMark(settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: barsFor(dates, 1000), Index: barsFor(dates, 4000), Sessions: dates,
		AsOf: dates[9] + "T15:00:00+08:00", Source: "sina",
		S0: dates[0], BaseDate: dates[0], S0Fen: 1000, Session: dates[9],
		Now: shanghai(2026, time.November, 30, 15, 31),
	})
	if res.Status != markResolved || res.Source != "sina" {
		t.Fatalf("result = %s/%q, want resolved/sina", res.Status, res.Source)
	}
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-19")
	var markID int64
	s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID)
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, settles: []forecastSettle{
		{markID: markID, res: res},
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var stored string
	s.st.queryRow("SELECT source FROM prediction_marks WHERE id=?", markID).Scan(&stored)
	if stored != "sina" {
		t.Errorf("stored source = %q, want sina", stored)
	}
}

// TestMalformedMadeOnNeverReachesAVendorURL: a receipt date that cannot build a window is refused
// before any fetch, so a bad row cannot put a control character into a vendor URL.
func TestMalformedMadeOnNeverReachesAVendorURL(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-10-17")
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='not-a-date', made_at='2026-10-17T13:00:00Z' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}
	now := shanghai(2026, time.October, 20, 16, 0)
	e := &forecastEvaluator{now: func() time.Time { return now }}
	fetched := 0
	e.fetch = func(context.Context, string, string, string, string) (*QuoteResp, error) {
		fetched++
		return nil, errors.New("no vendor call may happen")
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	if fetched != 0 {
		t.Errorf("%d vendor calls for a malformed made_on", fetched)
	}
	var code string
	s.st.queryRow("SELECT refusal FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&code)
	if code != "bad_window" {
		t.Errorf("refusal = %q, want bad_window", code)
	}
}

// TestValveIsSettledFromTheWholePass: the stale valve is a property of every stock body in the pass,
// not of one set, so a single halted stock cannot trip it.
func TestValveIsSettledFromTheWholePass(t *testing.T) {
	dates := tradingDays("2026-10-19", 10)
	newest := dates[9]
	var stocks []forecastSeries
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		last := newest
		if i == 0 {
			last = dates[6]
		}
		bars := barsFor(dates, 1000)
		if last != newest {
			bars = bars[:7]
		}
		stocks = append(stocks, forecastSeries{Bars: bars})
	}
	if staleValve(stocks, errs, dates) {
		t.Error("one halted stock out of six tripped the valve")
	}
	// Two of six is still not "more than 20%"? It is 33%, so it trips.
	stocks[1].Bars = stocks[1].Bars[:7]
	if !staleValve(stocks, errs, dates) {
		t.Error("two of six stale bodies did not trip the valve")
	}
	// A fetch error is not a stale body.
	errs[0], errs[1] = fmt.Errorf("down"), fmt.Errorf("down")
	if staleValve(stocks, errs, dates) {
		t.Error("failed fetches were counted as stale bodies")
	}
}
