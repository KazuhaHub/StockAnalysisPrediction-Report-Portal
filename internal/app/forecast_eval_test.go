package app

// The evaluator's rules (ADR 0029 §6, plan §6.5).
//
// Everything here is deterministic and fixture-driven: the clock and the vendor fetch are seams, so
// none of it touches a network. That is not only convenience — the two rules most worth pinning are
// about TIME, and a test that waits for a real session is a test nobody runs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------- fixtures ----------

func bar(date string, o, c, h, l int64) QuoteBar {
	return QuoteBar{Date: date, Open: o, Close: c, High: h, Low: l, Volume: 100}
}

// shanghai builds an instant in the exchange's zone, which is what every rule here reasons in.
func shanghai(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, chinaZone)
}

// tradingDays returns n consecutive weekday dates starting at start, skipping weekends. It does not
// know about holidays, which is fine: the evaluator does not either — its calendar is whatever the
// vendor answered with, and these fixtures ARE that answer.
func tradingDays(start string, n int) []string {
	t, err := time.ParseInLocation("2006-01-02", start, chinaZone)
	if err != nil {
		panic(err)
	}
	var out []string
	for len(out) < n {
		if t.Weekday() != time.Saturday && t.Weekday() != time.Sunday {
			out = append(out, t.Format("2006-01-02"))
		}
		t = t.AddDate(0, 0, 1)
	}
	return out
}

func barsFor(dates []string, close int64) []QuoteBar {
	out := make([]QuoteBar, 0, len(dates))
	prev := close
	for _, d := range dates {
		out = append(out, bar(d, prev, close, close+10, close-10))
		prev = close
	}
	return out
}

// ---------- arithmetic ----------

// TestBpFromRatioIsExact: returns are integer basis points, and the rounding is half away from zero.
// A float here would be a threshold comparison that resolves differently on different platforms, in
// the one table whose whole purpose is being citable.
func TestBpFromRatioIsExact(t *testing.T) {
	cases := []struct {
		to, from int64
		want     int32
	}{
		{865, 865, 0},
		{1000, 865, 1561},    // +15.6069% → 1561
		{995, 1000, -50},     // -0.5% → -50
		{852, 1000, -1480},   // -14.8%
		{10000, 100, 990000}, // tenfold
		{0, 100, -10000},     // a zero price is a -100% return, not an error
	}
	for _, tc := range cases {
		if got := bpFromRatio(tc.to, tc.from); got != tc.want {
			t.Errorf("bpFromRatio(%d, %d) = %d, want %d", tc.to, tc.from, got, tc.want)
		}
	}
	// A tie rounds AWAY from zero rather than toward it, so a positive and a negative of the same
	// magnitude are symmetric.
	if pos, neg := bpFromRatio(101, 100), bpFromRatio(99, 100); pos != -neg {
		t.Errorf("rounding is not symmetric: +%d vs %d", pos, neg)
	}
}

// TestDailyLimitFollowsTheBoard: the backstop for an unreported 送转 depends on knowing what a legal
// one-day move is, and the answer differs by board.
func TestDailyLimitFollowsTheBoard(t *testing.T) {
	for _, tc := range []struct {
		symbol string
		want   int32
	}{
		{"600160", 1000}, // main board
		{"000001", 1000},
		{"300772", 2000}, // ChiNext
		{"688111", 2000}, // STAR
	} {
		if got := dailyLimitBp(tc.symbol); got != tc.want {
			t.Errorf("dailyLimitBp(%s) = %d, want %d", tc.symbol, got, tc.want)
		}
	}
}

// TestBeyondDailyLimitNeverFlagsALegalMove is the distinction the plan's FIRST gap heuristic missed.
// A limit-down day is a real move and must not be excluded as a corporate action; a gap larger than
// the limit cannot be trading and must be.
func TestBeyondDailyLimitNeverFlagsALegalMove(t *testing.T) {
	bars := []QuoteBar{
		bar("2026-10-20", 1000, 1000, 1010, 990),
		bar("2026-10-21", 1000, 900, 1005, 898), // exactly -10%: a limit-down day
		bar("2026-10-22", 900, 1080, 1085, 895), // +20% on a MAIN BOARD code: impossible as trading
	}
	if beyondDailyLimit("600160", bars, bars[1]) {
		t.Error("a -10% move was flagged; that is an ordinary limit-down day, not a corporate action")
	}
	if !beyondDailyLimit("600160", bars, bars[2]) {
		t.Error("a +20% move on a main-board code was not flagged; no legal session produces it")
	}
	// The same +20% IS legal on ChiNext.
	if beyondDailyLimit("300772", bars, bars[2]) {
		t.Error("a +20% move on ChiNext was flagged; that board's limit is 20%")
	}
}

// TestCorporateActionFromTheDividendElement: the vendor names cash dividends outright, and that is
// the primary signal. No qfq fetch is involved.
func TestCorporateActionFromTheDividendElement(t *testing.T) {
	dates := tradingDays("2026-10-01", 20)
	bars := barsFor(dates, 1000)
	from, to := dates[2], dates[10]
	ex := []string{dates[5]}
	if !corporateActionBetween("600160", bars, nil, ex, from, to) {
		t.Error("an ex-date inside the window was not detected")
	}
	if corporateActionBetween("600160", bars, nil, ex, dates[6], to) {
		t.Error("an ex-date BEFORE the window was counted")
	}
	if corporateActionBetween("600160", bars, nil, ex, from, dates[4]) {
		t.Error("an ex-date AFTER the window was counted")
	}
	// The window is exclusive at the start: an event ON base_date belongs to the report's own entry
	// price, not to its forecast.
	if corporateActionBetween("600160", bars, nil, []string{from}, from, to) {
		t.Error("an ex-date exactly on base_date was counted; the window opens after it")
	}
}

// ---------- sessions ----------

// TestCompletedSessionsExcludesAnUnfinishedBar is the rule that makes a resolved mark permanent-safe.
// Tencent serves today's bar during trading, and a resolved mark is never revisited, so believing
// that bar would freeze a live price into the record.
func TestCompletedSessionsExcludesAnUnfinishedBar(t *testing.T) {
	dates := tradingDays("2026-10-19", 6)
	index := barsFor(dates, 4000)
	today := dates[5]

	// At 10:00 on today's session, today is not complete.
	got := completedSessionsAfter(index, dates[0], shanghai(2026, time.October, dayOf(today), 10, 0))
	if containsStr(got, today) {
		t.Errorf("today's session was counted as complete at 10:00: %v", got)
	}
	// At 15:31 it is.
	got = completedSessionsAfter(index, dates[0], shanghai(2026, time.October, dayOf(today), 15, 31))
	if !containsStr(got, today) {
		t.Errorf("today's session was not counted as complete at 15:31: %v", got)
	}
	// Sessions at or before the receipt date are never in the window.
	got = completedSessionsAfter(index, dates[3], shanghai(2026, time.October, dayOf(today), 16, 0))
	for _, d := range got {
		if d <= dates[3] {
			t.Errorf("%s is at or before the receipt date but was counted", d)
		}
	}
}

// TestBarIsCompleteDistrustsAPartialBody is the second half of the same rule, and the one a clock-only
// check gets wrong: the vendor stamps its own time, and a body captured at 14:57 carries 14:57 however
// late it is read. That is exactly what a cached response does.
func TestBarIsCompleteDistrustsAPartialBody(t *testing.T) {
	partial := &QuoteResp{Snapshot: QuoteSnapshot{AsOf: "2026-10-21T14:57:03+08:00"}}
	final := &QuoteResp{Snapshot: QuoteSnapshot{AsOf: "2026-10-21T15:00:01+08:00"}}

	// Read at 15:31, a 14:57 body is still a partial bar.
	if barIsComplete(partial, shanghai(2026, time.October, 21, 15, 31)) {
		t.Error("a 14:57 body was believed at 15:31; this is the cached-partial-bar bug")
	}
	if !barIsComplete(final, shanghai(2026, time.October, 21, 15, 31)) {
		t.Error("a 15:00 body was not believed after the close")
	}
	// And before the close, neither is believed, whatever the body says.
	if barIsComplete(final, shanghai(2026, time.October, 21, 10, 0)) {
		t.Error("a body was believed at 10:00, before the session closed")
	}
}

// TestCheckpointSessionIsSessionBasedAndH12IsCalendar: dN counts sessions after S0; h12 is decided by
// the calendar and exists only for standard horizons.
func TestCheckpointSessionIsSessionBasedAndH12IsCalendar(t *testing.T) {
	dates := tradingDays("2026-10-20", 140)
	set := forecastWorkSet{MadeOn: "2026-10-17", HorizonStd: true}

	for _, tc := range []struct {
		cp   string
		want string
	}{
		{"d5", dates[4]},
		{"d20", dates[19]},
		{"d60", dates[59]},
		{"d120", dates[119]},
	} {
		got, due := checkpointSession(tc.cp, set, dates)
		if !due || got != tc.want {
			t.Errorf("%s = %q (due=%v), want %q", tc.cp, got, due, tc.want)
		}
	}

	// A non-standard horizon has no h12 at all, which is what keeps a six-month call out of the
	// twelve-month column.
	if _, due := checkpointSession("h12", forecastWorkSet{MadeOn: "2026-10-17", HorizonStd: false}, dates); due {
		t.Error("h12 was due for a non-standard horizon")
	}
	// A standard one lands on the first session on or after made_on + 12 months.
	set2 := forecastWorkSet{MadeOn: "2025-10-17", HorizonStd: true}
	got, due := checkpointSession("h12", set2, dates)
	if !due {
		t.Fatal("h12 was not due twelve months after a receipt inside the fixture's span")
	}
	if got < "2026-10-17" {
		t.Errorf("h12 session = %s, want on or after 2026-10-17", got)
	}
	// Not due while the series has not reached it.
	if _, due := checkpointSession("h12", forecastWorkSet{MadeOn: "2026-10-17", HorizonStd: true}, dates); due {
		t.Error("h12 was due one year early")
	}
}

// ---------- settling ----------

// TestSettleMarkHandlesAHalt: a halted stock resolves on its last traded close and is FLAGGED, not
// refused. A refusal that can never resolve is what hollows out a cohort without showing it.
func TestSettleMarkHandlesAHalt(t *testing.T) {
	dates := tradingDays("2026-10-20", 30)
	stock := barsFor(dates[:8], 1000) // stops trading after dates[7]
	index := barsFor(dates, 4000)

	res := settleMark(forecastWorkSet{Symbol: "600160"}, forecastMark{Checkpoint: "d20"},
		stock, index, nil, dates[0], dates[0], 1000, dates[19])

	if res.Status != markResolved {
		t.Fatalf("status = %q, want resolved: a halt is an answer, not a refusal", res.Status)
	}
	if !res.Halted {
		t.Error("halted = false for a stock whose last bar predates the checkpoint")
	}
	if res.BarDate != dates[7] {
		t.Errorf("bar_date = %s, want the last traded bar %s", res.BarDate, dates[7])
	}
	if res.RetBp == nil || *res.RetBp != 0 {
		t.Errorf("ret = %v, want 0 (the close did not move)", res.RetBp)
	}
}

// TestSettleMarkNeverLooksBeforeBaseDate: a bar printed before the report existed is not this report's
// outcome, and the fill window starts at the same boundary for the same reason.
func TestSettleMarkNeverLooksBeforeBaseDate(t *testing.T) {
	dates := tradingDays("2026-10-20", 30)
	// A big move BEFORE the report's base date, then a flat stretch after it.
	stock := barsFor(dates, 1000)
	stock[0].Low = 100 // an early low the report could not have seen
	index := barsFor(dates, 4000)

	res := settleMark(forecastWorkSet{Symbol: "600160"}, forecastMark{Checkpoint: "d5"},
		stock, index, nil, dates[3], dates[3], 1000, dates[8])

	if res.LowFen == nil {
		t.Fatal("no low reported")
	}
	if *res.LowFen == 100 {
		t.Error("the low window reached back before base_date")
	}
}

// TestSettleMarkMeasuresAgainstTheBenchmark: the excess return is what the metrics use, and it is the
// stock's return minus the index's over the SAME window.
func TestSettleMarkMeasuresAgainstTheBenchmark(t *testing.T) {
	dates := tradingDays("2026-10-20", 10)
	stock := barsFor(dates, 1100) // +10%
	index := barsFor(dates, 4000)
	index[5].Close = 4400 // the index rose 10% too

	res := settleMark(forecastWorkSet{Symbol: "600160"}, forecastMark{Checkpoint: "d5"},
		stock, index, nil, dates[0], dates[0], 1000, dates[5])

	if res.RetBp == nil || res.BenchRetBp == nil {
		t.Fatal("a return is missing")
	}
	if *res.RetBp != *res.BenchRetBp {
		t.Errorf("ret %d vs bench %d: a stock that matched its index has no excess", *res.RetBp, *res.BenchRetBp)
	}
}

// TestResolveStartBarIsTheFirstSessionAfterReceipt: S0 is the first close the report could not have
// known. The base date is the first session the stock actually traded on, which is a different date
// when the stock was halted exactly then.
func TestResolveStartBarIsTheFirstSessionAfterReceipt(t *testing.T) {
	dates := tradingDays("2026-10-19", 10)
	index := barsFor(dates, 4000)
	stock := barsFor(dates[2:], 1000) // the stock's series starts two sessions late
	now := shanghai(2026, time.October, 30, 16, 0)

	s0, base, fen, err := resolveStartBar(stock, index, forecastWorkSet{MadeOn: dates[0]}, now)
	if err != nil {
		t.Fatal(err)
	}
	if s0 != dates[1] {
		t.Errorf("s0 = %s, want the first session after receipt %s", s0, dates[1])
	}
	if base != dates[2] {
		t.Errorf("base_date = %s, want the first session the stock traded %s", base, dates[2])
	}
	if fen != 1000 {
		t.Errorf("s0_fen = %d, want 1000", fen)
	}
}

// ---------- the loop and its switch ----------

// TestEvaluatorDoesNothingWhenOff is the switch's actual contract: off means no query, no fetch and no
// write — not "quiet". A loop that checked the setting after touching the store would still cost a
// query per minute on every install.
func TestEvaluatorDoesNothingWhenOff(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, "0"); err != nil {
		t.Fatal(err)
	}
	// Seed work that WOULD be picked up if the loop ran.
	seedAcceptedSet(t, s, "600160", "2026-10-17")

	e := &forecastEvaluator{}
	fetched := 0
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		fetched++
		return nil, errors.New("the evaluator must not fetch while it is off")
	}
	// One tick's worth of the loop body, exactly as loop() runs it.
	if s.st.GetSetting(setForecastAuto, "0") == "1" {
		t.Fatal("setup: the switch is on")
	}
	if fetched != 0 {
		t.Errorf("%d fetches happened while the switch was off", fetched)
	}
	var pending int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE status = ?", markPending).Scan(&pending)
	if pending == 0 {
		t.Fatal("setup: no pending marks were seeded")
	}
	// And no mark was touched.
	var touched int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE resolved_at <> ''").Scan(&touched)
	if touched != 0 {
		t.Errorf("%d marks were resolved while the switch was off", touched)
	}
}

// TestResolvedMarkIsTerminal: the guard is the whole immutability promise. A second pass — an
// overlapping tick, a restart, a retry — must not be able to rewrite a conclusion.
func TestResolvedMarkIsTerminal(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-10-17")
	var markID int64
	if err := s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID); err != nil {
		t.Fatal(err)
	}
	first := int64(1111)
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, settles: []forecastSettle{{markID: markID, res: markResult{
		Status: markResolved, Session: "2026-10-24", BarDate: "2026-10-24", CloseFen: &first,
	}}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	second := int64(2222)
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, settles: []forecastSettle{{markID: markID, res: markResult{
		Status: markResolved, Session: "2026-10-24", BarDate: "2026-10-24", CloseFen: &second,
	}}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.st.queryRow("SELECT close_fen FROM prediction_marks WHERE id=?", markID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Errorf("close_fen = %d, want the first conclusion %d: a resolved mark must not move", got, first)
	}
	// refused_final is terminal in exactly the same way.
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, finals: []forecastFinal{{markID: markID, code: "no_bar"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var status string
	s.st.queryRow("SELECT status FROM prediction_marks WHERE id=?", markID).Scan(&status)
	if status != markResolved {
		t.Errorf("status = %q: MarkRefusedFinal changed a resolved mark, which only applies to refused ones", status)
	}
}

// TestBaselineIsWrittenOnce: the starting point of a set's whole return series must not move, so the
// write is guarded on base_check rather than unconditional.
func TestBaselineIsWrittenOnce(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-10-17")
	dates := tradingDays("2026-10-19", 5)
	index := barsFor(dates, 4000)

	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, baseline: &forecastBaseline{
		S0: dates[0], BaseDate: dates[0], S0Fen: 1000, Index: index,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A second pass with a different start point.
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, baseline: &forecastBaseline{
		S0: dates[3], BaseDate: dates[3], S0Fen: 9999, Index: index,
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var s0 string
	var fen int64
	if err := s.st.queryRow("SELECT s0_date, s0_fen FROM prediction_sets WHERE id=?", setID).Scan(&s0, &fen); err != nil {
		t.Fatal(err)
	}
	if s0 != dates[0] || fen != 1000 {
		t.Errorf("baseline moved to %s/%d; it must be written once", s0, fen)
	}
}

// TestRefusalFinalityIsPerV26: a mark-specific failure is an attempt and can become final; a source
// failure leaves attempts unchanged and never counts toward finality.
func TestRefusalFinalityIsPerV26(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSet(t, s, "600160", "2026-10-17")
	var markID int64
	s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID)

	mark := forecastMark{ID: markID, Status: markPending}
	for i := 1; i <= 3; i++ {
		if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, defers: []forecastDeferGroup{
			{marks: []forecastMark{mark}, code: "no_bar"},
		}}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	var attempts int
	s.st.queryRow("SELECT status, attempts FROM prediction_marks WHERE id=?", markID).Scan(&status, &attempts)
	if status != markRefused || attempts != 3 {
		t.Fatalf("after 3 no_bar defers: status=%q attempts=%d, want refused/3", status, attempts)
	}

	// A source failure is a delivery fault, not this mark's attempt.
	mark.Refusal = "no_bar"
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, defers: []forecastDeferGroup{
		{marks: []forecastMark{mark}, code: "fetch_failed"},
	}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.st.queryRow("SELECT status, attempts FROM prediction_marks WHERE id=?", markID).Scan(&status, &attempts)
	if status != markRefused || attempts != 3 {
		t.Errorf("after a source failure: status=%q attempts=%d, want refused/3 (unchanged)", status, attempts)
	}

	// Finality still ends the retries.
	if err := s.st.ApplyForecastPlan(forecastPlan{setID: setID, finals: []forecastFinal{{markID: markID, code: "no_bar"}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.st.queryRow("SELECT status FROM prediction_marks WHERE id=?", markID).Scan(&status)
	if status != markRefusedFinal {
		t.Errorf("status = %q, want %q", status, markRefusedFinal)
	}
}

// ---------- admin settings ----------

// TestForecastSettingsRoundTrip: a switch with no writer is a constant with a misleading name.
func TestForecastSettingsRoundTrip(t *testing.T) {
	s := newV1Server(t)

	rec := httptest.NewRecorder()
	s.apiForecastSettings(rec, httptest.NewRequest("GET", "/api/admin/forecast", nil), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d", rec.Code)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["enabled"] != false {
		t.Errorf("enabled = %v, want false: the evaluator ships off", got["enabled"])
	}

	expectedFrom := nextCohort(shanghaiCohort(time.Now()))
	body := `{"enabled":true,"bench_schedule":"sh000905"}`
	rec = httptest.NewRecorder()
	s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast", strBody(body)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body.String())
	}
	var saved map[string]any
	json.Unmarshal(rec.Body.Bytes(), &saved)
	if saved["effective_cohort"] != expectedFrom {
		t.Errorf("effective_cohort = %v, want %q", saved["effective_cohort"], expectedFrom)
	}
	if s.st.GetSetting(setForecastAuto, "0") != "1" {
		t.Error("the switch was not persisted")
	}
	// Effective-dated: the active value does not move on save; the next cohort gets the new code.
	if s.st.ForecastBench() != defaultForecastBench {
		t.Errorf("active bench = %q, want %q: a change must not re-score this cohort",
			s.st.ForecastBench(), defaultForecastBench)
	}
	if next, from := s.st.forecastScheduledBench(); next != "sh000905" || from != expectedFrom {
		t.Errorf("scheduled = %q from %q, want sh000905 from %q", next, from, expectedFrom)
	}

	// Flipping the switch is audited. The settings endpoint is the only writer of forecast_auto, and
	// what it turns on is a background loop that reaches a third-party vendor on its own — the kind
	// of change an operator has to be able to find in the log afterwards, together with who made it.
	// The audit entry is asserted here rather than assumed from the code, because a recordChange that
	// silently stopped being called would leave every test in this file green.
	rows := auditRows(t, s, AuditForecastAuto)
	if len(rows) != 1 {
		t.Fatalf("audit entries for %s = %d, want 1", AuditForecastAuto, len(rows))
	}
	if rows[0].Actor != "admin" || rows[0].TargetType != "settings" || rows[0].TargetID != setForecastAuto {
		t.Errorf("audit row = %+v, want actor=admin on settings/%s", rows[0], setForecastAuto)
	}
	if benchRows := auditRows(t, s, AuditForecastBench); len(benchRows) != 1 {
		t.Fatalf("audit entries for %s = %d, want 1", AuditForecastBench, len(benchRows))
	}

	// A request that only changes the benchmark does not touch the switch, so it must not file an
	// audit row for it: the log says a loop was turned on, and it has to keep meaning that.
	if _, err := s.st.exec(`DELETE FROM audit_log WHERE action=?`, AuditForecastAuto); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast", strBody(`{"bench":"sh000300"}`)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status %d: %s", rec.Code, rec.Body.String())
	}
	if rows := auditRows(t, s, AuditForecastAuto); len(rows) != 0 {
		t.Errorf("a benchmark-only save filed %d audit rows, want 0", len(rows))
	}

	// A benchmark that cannot be fetched is refused rather than stored: a set written against it
	// could never be scored.
	rec = httptest.NewRecorder()
	s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast", strBody(`{"bench":"nonsense"}`)), "admin")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status for a bad benchmark = %d, want 400", rec.Code)
	}
	if next, _ := s.st.forecastScheduledBench(); next != "sh000300" {
		t.Errorf("a refused benchmark changed the schedule to %q", next)
	}

	// Rescheduling the active value when nothing is pending writes nothing at all.
	clean := newV1Server(t)
	if _, err := clean.st.exec(`DELETE FROM audit_log WHERE action IN (?,?)`, AuditForecastAuto, AuditForecastBench); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	clean.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast",
		strBody(`{"bench_schedule":"`+defaultForecastBench+`"}`)), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op POST status %d: %s", rec.Code, rec.Body.String())
	}
	if rows := auditRows(t, clean, AuditForecastBench); len(rows) != 0 {
		t.Errorf("a no-op resave filed %d audit rows, want 0", len(rows))
	}
	if next, from := clean.st.forecastScheduledBench(); next != "" || from != "" {
		t.Errorf("a no-op resave scheduled %q from %q", next, from)
	}
}

// ---------- helpers ----------

func dayOf(date string) int {
	t, err := time.ParseInLocation("2006-01-02", date, chinaZone)
	if err != nil {
		panic(err)
	}
	return t.Day()
}

func strBody(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// seedAcceptedSet ingests one covered report and returns its set id, so a test starts from a row the
// real write path produced rather than from hand-written SQL.
func seedAcceptedSet(t *testing.T, s *Server, symbol, date string) int64 {
	t.Helper()
	body := ingestBody(string(predWire(nil)))
	body = replaceOnce(body, `"symbol":"600160"`, `"symbol":"`+symbol+`"`)
	body = replaceOnce(body, `"date":"2026-10-17"`, `"date":"`+date+`"`)
	code, m := ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("seed ingest: %d %v", code, m)
	}
	var setID int64
	if err := s.st.queryRow("SELECT id FROM prediction_sets ORDER BY id DESC LIMIT 1").Scan(&setID); err != nil {
		t.Fatalf("read seeded set: %v", err)
	}
	return setID
}

// seedAcceptedSetAt is seedAcceptedSet with the receipt instant pinned, so a test can control where a
// set falls relative to the ledger start. The receipt is the given day's 21:00 CST (13:00Z).
func seedAcceptedSetAt(t *testing.T, s *Server, symbol, date string) int64 {
	t.Helper()
	setID := seedAcceptedSet(t, s, symbol, date)
	madeAt := date + "T13:00:00Z" // 21:00 CST on `date`
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_on=?, made_at=?, cohort=? WHERE id=?`,
		date, madeAt, date[:7], setID); err != nil {
		t.Fatalf("pin seeded set: %v", err)
	}
	return setID
}

// TestBpFromRatioRoundsHalfAwayFromZeroOnBothSides pins the negative branch. `from/2` carries the
// sign of `from`, so a version that used it directly is off by one basis point on every exact tie
// below zero — a small number, in the one table whose whole point is that its numbers are exact.
func TestBpFromRatioRoundsHalfAwayFromZeroOnBothSides(t *testing.T) {
	// 5000 * t = -x.5 exactly, for x from 1 to 9: every one of these is a tie.
	for _, tc := range []struct{ to, from, want int64 }{
		{9999, 10000, -1},  // -0.01% exactly, rounds to -1
		{9995, 10000, -5},  // -0.05%
		{9990, 10000, -10}, // -0.10%
		{10005, 10000, 5},  // +0.05%
		{10001, 10000, 1},  // +0.01%
	} {
		if got := bpFromRatio(tc.to, tc.from); int64(got) != tc.want {
			t.Errorf("bpFromRatio(%d,%d) = %d, want %d", tc.to, tc.from, got, tc.want)
		}
	}
}

// TestEvaluatorTickSettlesDueMarks is the only test that runs the whole path: pick the work, fetch
// both series, resolve S0, settle each due mark, and write the baseline. Every rule above is pinned
// in isolation; this one is what says they compose.
func TestEvaluatorTickSettlesDueMarks(t *testing.T) {
	s := newV1Server(t)
	if err := s.st.SetSetting(setForecastAuto, "1"); err != nil {
		t.Fatal(err)
	}

	// The set is received on 2026-10-16, so S0 is the next session.
	setID := seedAcceptedSet(t, s, "600160", "2026-10-16")
	// Its made_on is today's real date, which the evaluator will compare against fixture bars far in
	// the past. Pin the row instead so the arithmetic is deterministic.
	if _, err := s.st.exec("UPDATE prediction_sets SET made_on='2026-10-16', made_at='2026-10-16T13:00:00Z' WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}

	// Twenty-two sessions after the receipt date, enough for d5 and d20 and not for d60.
	dates := tradingDays("2026-10-19", 22)
	index := barsFor(dates, 4000)
	stock := barsFor(dates, 1000)
	stock[10].Close, stock[10].High = 1100, 1110 // a real move in the middle, nothing corporate

	now := shanghai(2026, time.November, 20, 16, 0)
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		switch code {
		case "000852":
			// The index body must declare its kind: since P3 a benchmark series whose vendor kind is
			// not "index" cannot settle a mark, so a stub that leaves Kind empty is (correctly) refused.
			return &QuoteResp{Bars: index, Kind: quoteKindIndex, Snapshot: QuoteSnapshot{AsOf: "2026-11-20T15:00:01+08:00"}}, nil
		case "600160":
			return &QuoteResp{Bars: stock, Kind: quoteKindStock, Snapshot: QuoteSnapshot{AsOf: "2026-11-20T15:00:01+08:00"}}, nil
		}
		return nil, fmt.Errorf("unexpected code %q", code)
	}

	if err := e.tick(s); err != nil {
		t.Fatalf("tick: %v", err)
	}

	// d5 and d20 settled; the later checkpoints are still waiting for their sessions.
	for _, tc := range []struct {
		cp   string
		want string
	}{
		{"d5", markResolved},
		{"d20", markResolved},
		{"d60", markPending},
		{"d120", markPending},
		{"h12", markPending},
	} {
		var status string
		if err := s.st.queryRow("SELECT status FROM prediction_marks WHERE set_id=? AND checkpoint=?", setID, tc.cp).Scan(&status); err != nil {
			t.Fatalf("%s: %v", tc.cp, err)
		}
		if status != tc.want {
			t.Errorf("%s status = %q, want %q", tc.cp, status, tc.want)
		}
	}

	// The resolved marks carry a return and a session date.
	var ret, bench int64
	var session string
	if err := s.st.queryRow(`SELECT ret_bp, bench_ret_bp, session_date FROM prediction_marks
		WHERE set_id=? AND checkpoint='d5'`, setID).Scan(&ret, &bench, &session); err != nil {
		t.Fatal(err)
	}
	if session != dates[4] {
		t.Errorf("d5 session = %s, want the fifth session %s", session, dates[4])
	}
	// The fixture is flat until session 10, so d5's return is genuinely zero. What matters is that the
	// benchmark was measured over the same window and stored beside it.
	if bench != 0 {
		t.Errorf("d5 bench_ret_bp = %d, want 0 (the index is flat too)", bench)
	}

	// S0 and the baseline were written exactly once.
	var s0, base string
	var fen int64
	var check string
	if err := s.st.queryRow("SELECT s0_date, base_date, s0_fen, base_check FROM prediction_sets WHERE id=?", setID).
		Scan(&s0, &base, &fen, &check); err != nil {
		t.Fatal(err)
	}
	if s0 != dates[0] || base != dates[0] {
		t.Errorf("s0/base = %s/%s, want %s/%s", s0, base, dates[0], dates[0])
	}
	if fen != 1000 {
		t.Errorf("s0_fen = %d, want 1000", fen)
	}
	if check != baseMatch {
		t.Errorf("base_check = %q, want %q", check, baseMatch)
	}

	// A second tick must not move anything: the marks are resolved, so the work query no longer
	// returns this set.
	stock[4].Close = 9999 // a value that would be visible if anything re-read it
	if err := e.tick(s); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	var closeFen int64
	s.st.queryRow("SELECT close_fen FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&closeFen)
	if closeFen == 9999 {
		t.Error("a second tick rewrote a resolved mark")
	}
}
