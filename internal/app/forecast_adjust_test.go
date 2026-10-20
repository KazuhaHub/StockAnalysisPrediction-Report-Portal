package app

// P8 (plan §5.4): detect a bonus or rights issue by the adjusted ratio. The unadjusted body names only
// cash dividends, and a bonus stays inside the daily limit, so neither of P7's signals catches it.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestQfqRatioFlagsBonusIssueUnderTheLimit: a 10-for-1 bonus is a -9.09% move on the unadjusted
// close, inside the 10% limit, and the ratio between the adjusted and raw closes halves. A limit-down
// day of -9.5% on the same raw price is NOT a corporate action: the ratio is unchanged.
func TestQfqRatioFlagsBonusIssueUnderTheLimit(t *testing.T) {
	// Before the ex-date: raw 1100, adjusted 1100. On it: raw 1000, adjusted 2000 (qfq keeps the
	// pre-event level so the historical series does not jump).
	if !adjustRatioChanged(1100, 1100, 2000, 1000) {
		t.Error("a halved ratio was not flagged")
	}
	// A legal limit-down move with no adjustment: both ratios equal.
	if adjustRatioChanged(1100, 1100, 995, 995) {
		t.Error("a -9.5% limit-down day with an unchanged ratio was flagged")
	}
	// Fen rounding alone never flags. For every pair of raw prices within a few fen of each other,
	// an adjusted series equal to raw has no change.
	for raw1 := int64(100); raw1 < 140; raw1++ {
		for raw2 := int64(100); raw2 < 140; raw2++ {
			if adjustRatioChanged(raw1, raw1, raw2, raw2) {
				t.Fatalf("rounding alone flagged %d -> %d", raw1, raw2)
			}
		}
	}
	// A one-fen adjusted difference is inside the rounding bound for cheap prices.
	if adjustRatioChanged(105, 105, 104, 105) {
		t.Error("a one-fen adjusted difference on a cheap stock was flagged")
	}
}

func TestAdjustRatioHandlesDegeneratePrices(t *testing.T) {
	for _, tc := range [][4]int64{{0, 100, 100, 100}, {100, 0, 100, 100}, {100, 100, 0, 100}} {
		if adjustRatioChanged(tc[0], tc[1], tc[2], tc[3]) {
			t.Errorf("degenerate prices %v were flagged", tc)
		}
	}
}

// TestQfqRatioIsAOneSidedSignal: the ratio check only ever ADDS a corporate-action flag. It can never
// clear the dividend element or the limit backstop.
func TestQfqRatioIsAOneSidedSignal(t *testing.T) {
	dates := tradingDays("2026-10-19", 5)
	raw := barsFor(dates, 1000)
	raw[4].Close = 909 // -9.1%: inside the limit, no gap backstop
	in := settleInput{
		Set: forecastWorkSet{Symbol: "600160"}, Mark: forecastMark{Checkpoint: "d5"},
		Stock: raw, Index: barsFor(dates, 4000), Sessions: dates,
		AsOf: dates[4] + "T15:00:00+08:00",
		S0:   dates[0], BaseDate: dates[0], S0Fen: 1000, Session: dates[4],
		Now: shanghai(2026, time.November, 30, 15, 31),
		Adj: map[string]int64{dates[0]: 1000, dates[4]: 1818}, AdjChecked: true,
	}
	res := settleMark(in)
	if res.Status != markResolved || !res.CorpAction {
		t.Fatalf("result = %s/corp=%v, want resolved with corp_action", res.Status, res.CorpAction)
	}
	// The same body without the adjustment is a real loss, not excluded.
	in.Adj = map[string]int64{dates[0]: 1000, dates[4]: 909}
	if res := settleMark(in); res.CorpAction {
		t.Error("an unchanged ratio flagged a corporate action")
	}
}

// TestAdjustFetchOnlyForDueMarks: a tick whose marks are all ahead makes no qfq call.
func TestAdjustFetchOnlyForDueMarks(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	dates := tradingDays("2026-10-19", 6)
	index := barsFor(dates, 4000)
	stock := barsFor(dates, 1000)
	now := shanghai(2026, time.October, 21, 16, 0) // d5 is 10-26, not due
	e := evalStub(&now, index, stock, nil, nil)
	calls := 0
	e.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		calls++
		return nil, errors.New("no qfq call may happen")
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("qfq calls = %d with no due mark, want 0", calls)
	}
	// Once d5 is due there is exactly one call for the set.
	later := shanghai(2026, time.October, 27, 16, 0)
	e2 := evalStub(&later, index, stock, nil, nil)
	calls = 0
	e2.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		calls++
		out := map[string]int64{}
		for _, b := range stock[:5] {
			out[b.Date] = b.Close
		}
		return out, nil
	}
	if err := e2.tick(s); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("qfq calls = %d for one due set, want 1", calls)
	}
	_ = setID
}

// TestQfqFetchFailureIsASourceFailure: a failed adjustment fetch leaves the mark waiting with its
// attempts unchanged, never resolved on the cheaper check alone.
func TestQfqFetchFailureIsASourceFailure(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	dates := tradingDays("2026-10-19", 8)
	index := barsFor(dates, 4000)
	stock := barsFor(dates, 1000)
	now := shanghai(2026, time.October, 27, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	e.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		return nil, errors.New("qfq endpoint is down")
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status, refusal string
	var attempts int
	var ret *int64
	if err := s.st.queryRow(`SELECT status, refusal, attempts, ret_bp FROM prediction_marks
		WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&status, &refusal, &attempts, &ret); err != nil {
		t.Fatal(err)
	}
	if status != markRefused || refusal != "fetch_failed" || attempts != 0 || ret != nil {
		t.Errorf("mark = %s/%s/attempts=%d/ret=%v, want refused/fetch_failed/0/nil",
			status, refusal, attempts, ret)
	}
}

// TestAdjustRatioIsNeverStoredOrScored: no column receives a qfq value. The adjusted map is only ever
// compared inside settleMark; the stored prices are the unadjusted ones the body printed.
func TestAdjustRatioIsNeverStoredOrScored(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	dates := tradingDays("2026-10-19", 8)
	index := barsFor(dates, 4000)
	stock := barsFor(dates, 1000)
	stock[5].Close = 909 // a bonus-shaped raw close
	now := shanghai(2026, time.October, 27, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	// The adjusted series is exactly double before the event, so the ratio changed.
	e.adjust = func(context.Context, string, string, string, string) (map[string]int64, error) {
		out := map[string]int64{}
		for i, b := range stock {
			if i >= 5 {
				out[b.Date] = b.Close * 2
			} else {
				out[b.Date] = b.Close
			}
		}
		return out, nil
	}
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var corp int
	var closeFen, ret int64
	var benchRet int64
	if err := s.st.queryRow(`SELECT corp_action, close_fen, ret_bp, bench_ret_bp
		FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID).
		Scan(&corp, &closeFen, &ret, &benchRet); err != nil {
		t.Fatal(err)
	}
	if corp != 1 {
		t.Error("the adjusted-ratio signal did not flag the mark")
	}
	if closeFen != 909 {
		t.Errorf("close_fen = %d, want the raw 909: a qfq price must never be stored", closeFen)
	}
	// And the check is never written anywhere: the ledger has no column for it.
	body, err := os.ReadFile("forecast_eval_store.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Adjust") {
		t.Error("the store mentions the adjust ratio; it must only exist in the evaluator")
	}
}
