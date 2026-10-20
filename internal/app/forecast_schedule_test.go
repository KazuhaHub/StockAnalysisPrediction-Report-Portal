package app

// The P1 scheduling contract (ADR 0029 §4, V26).
//
// These pin the properties that are invisible on a page: a mark that is not due is never a refusal, a
// source failure is never an attempt, finality needs both attempts and elapsed sessions, and a set
// that is comfortably in the future costs no vendor call at all.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// evalStub is the evaluator with fixed series and an injected clock. The benchmark code is the
// vendor's symbol ("000852"); every other code is the stock.
func evalStub(now *time.Time, index, stock []QuoteBar, indexErr, stockErr error) *forecastEvaluator {
	e := &forecastEvaluator{now: func() time.Time { return *now }}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if code == "000852" {
			if indexErr != nil {
				return nil, indexErr
			}
			return &QuoteResp{Bars: index, Kind: quoteKindIndex,
				Snapshot: QuoteSnapshot{AsOf: now.In(chinaZone).Format("2006-01-02") + "T15:00:01+08:00"}}, nil
		}
		if stockErr != nil {
			return nil, stockErr
		}
		return &QuoteResp{Bars: stock, Kind: quoteKindStock,
			Snapshot: QuoteSnapshot{AsOf: now.In(chinaZone).Format("2006-01-02") + "T15:00:01+08:00"}}, nil
	}
	return e
}

// ingestSymbol is a valid six-digit symbol, distinct per index. symbolFor in the group tests makes
// seven digits, which the classifier (correctly) refuses.
func ingestSymbol(i int) string { return fmt.Sprintf("61%04d", i) }

func TestS0FollowsReceiptTimeOfDay(t *testing.T) {
	dates := tradingDays("2026-10-19", 20) // Mon 19 Oct onward
	for _, tc := range []struct {
		name   string
		madeAt time.Time
		want   string
	}{
		{"10:00 before the close", shanghai(2026, time.October, 19, 10, 0), dates[0]},
		{"14:59:59 before the close", shanghai(2026, time.October, 19, 14, 59), dates[0]},
		{"15:00:00 at the close", shanghai(2026, time.October, 19, 15, 0), dates[1]},
		{"12:00 lunch break", shanghai(2026, time.October, 19, 12, 0), dates[0]},
		{"08:00 pre-open", shanghai(2026, time.October, 19, 8, 0), dates[0]},
		{"Sunday evening", shanghai(2026, time.October, 18, 21, 0), dates[0]},
	} {
		if got, ok := predS0(tc.madeAt, dates); !ok || got != tc.want {
			t.Errorf("%s: predS0 = %q (ok=%v), want %q", tc.name, got, ok, tc.want)
		}
	}

	// A holiday Monday is known only once Tuesday's bar exists: the calendar skips it.
	if got, ok := predS0(shanghai(2026, time.October, 19, 10, 0), dates[1:]); !ok || got != dates[1] {
		t.Errorf("holiday Monday: predS0 = %q (ok=%v), want %q", got, ok, dates[1])
	}
	// Not decided while no later session is on the calendar.
	if got, ok := predS0(shanghai(2026, time.October, 19, 10, 0), nil); ok {
		t.Errorf("predS0 decided with an empty calendar: %q", got)
	}
}

func TestS0WaitsWhileTodayIsUnknown(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-19")
	// Pin the receipt to 10:00 CST, when today's session would be the start if its bar were known.
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at='2026-10-19T02:00:00Z', made_on='2026-10-19', cohort='2026-10' WHERE id=?`, setID); err != nil {
		t.Fatal(err)
	}
	// The calendar has only the session before the receipt: today's bar has not arrived yet.
	index := barsFor(tradingDays("2026-10-16", 1), 4000)
	stock := barsFor(tradingDays("2026-10-16", 1), 1000)
	now := shanghai(2026, time.October, 19, 10, 0)
	sets, err := s.st.ForecastSetsDue(now, 1)
	if err != nil || len(sets) != 1 {
		t.Fatalf("selection = %d (%v), want the seeded set", len(sets), err)
	}
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.evaluateSet(context.Background(), s, sets[0],
		forecastSeries{Bars: index, Kind: quoteKindIndex},
		nil, forecastSeries{Bars: stock, Kind: quoteKindStock, AsOf: "2026-10-16T15:00:01+08:00"},
		nil, false); err != nil {
		t.Fatal(err)
	}
	var check, s0 string
	s.st.queryRow("SELECT base_check, s0_date FROM prediction_sets WHERE id=?", setID).Scan(&check, &s0)
	if check != baseCheckPending || s0 != "" {
		t.Errorf("baseline = %q/%q, want pending and no S0: today's session is not known yet", check, s0)
	}
	var refused int
	s.st.queryRow("SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND status IN (?,?)",
		setID, markRefused, markRefusedFinal).Scan(&refused)
	if refused != 0 {
		t.Errorf("%d marks refused while the start session is unknown", refused)
	}
}

func TestDNIsTheNthSessionAfterS0(t *testing.T) {
	// Set 6: received Sunday 2026-10-18 in the evening; d5 is 2026-10-26.
	sessions := tradingDays("2026-10-19", 20)
	sch := predScheduleFor("d5", shanghai(2026, time.October, 18, 21, 0), true, "", sessions)
	if !sch.Known || sch.S0 != "2026-10-19" || sch.Record != "2026-10-26" || sch.Due != "2026-10-26" {
		t.Errorf("set 6 d5 = %+v, want S0 2026-10-19 and record/due 2026-10-26", sch)
	}

	// The demo report: received 2026-09-17 after 15:00, the fixture has no 2026-09-25 bar.
	demo := []string{"2026-09-17", "2026-09-18", "2026-09-21", "2026-09-22", "2026-09-23", "2026-09-24", "2026-09-28"}
	got := predScheduleFor("d5", shanghai(2026, time.September, 17, 16, 0), true, "", demo)
	if !got.Known || got.S0 != "2026-09-18" || got.Record != "2026-09-28" {
		t.Errorf("demo d5 = %+v, want S0 2026-09-18 and record 2026-09-28", got)
	}

	// A frozen S0 wins, and the Nth session after it is the record.
	frozen := predScheduleFor("d5", shanghai(2026, time.October, 18, 21, 0), true, sessions[1], sessions)
	if frozen.S0 != sessions[1] || frozen.Record != sessions[6] {
		t.Errorf("frozen S0: %+v, want S0=%s record=%s", frozen, sessions[1], sessions[6])
	}
	// Not due until the Nth session after S0 exists.
	if early := predScheduleFor("d5", shanghai(2026, time.October, 18, 21, 0), true, "", sessions[:5]); early.Known {
		t.Errorf("d5 was known with only five sessions: %+v", early)
	}
	// h12 stays calendar-based and only exists for a standard horizon.
	if h := predScheduleFor("h12", shanghai(2026, time.October, 18, 21, 0), false, "", sessions); h.Known {
		t.Errorf("h12 on a non-standard horizon is known: %+v", h)
	}
	long := tradingDays("2025-10-17", 400)
	if h := predScheduleFor("h12", shanghai(2025, time.October, 17, 10, 0), true, "", long); !h.Known || h.Record < "2026-10-17" {
		t.Errorf("h12 with a year of sessions = %+v, want known on or after 2026-10-17", h)
	}
}

func TestMarksUseTheStoredBaseline(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16") // after the close: S0 would be Monday
	sessions := tradingDays("2026-10-19", 7)
	index := barsFor(sessions, 4000)
	stock := barsFor(sessions, 1000)
	stock[1].Close = 1200 // a later session at a different price
	stock[5].Close = 1100

	// Store a baseline the receipt rule would NOT pick: S0 is the receipt Friday, not Monday. The
	// score must come from this row, so d5 is 1100/1000-1 = +10.00%, not 1100/1200-1.
	if _, err := s.st.exec(`UPDATE prediction_sets SET base_check=?, s0_date=?, base_date=?, s0_fen=1000
		WHERE id=?`, baseMatch, sessions[0], sessions[0], setID); err != nil {
		t.Fatal(err)
	}

	now := shanghai(2026, time.October, 27, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var ret int32
	if err := s.st.queryRow("SELECT ret_bp FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&ret); err != nil {
		t.Fatal(err)
	}
	if ret != 1000 {
		t.Errorf("d5 ret_bp = %d, want 1000 from the stored s0_fen", ret)
	}

	// The body's close on base_date no longer matches the stored anchor: the mark refuses instead of
	// silently moving its start.
	if _, err := s.st.exec(`DELETE FROM prediction_marks WHERE set_id=? AND checkpoint='d5'`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status) VALUES(?, 'd5', 'pending')`, setID); err != nil {
		t.Fatal(err)
	}
	moved := append([]QuoteBar(nil), stock...)
	moved[0].Close = 5000
	later := now.Add(2 * time.Hour)
	e = evalStub(&later, index, moved, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status, refusal string
	s.st.queryRow("SELECT status, refusal FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&status, &refusal)
	if status != markRefused || refusal != "anchor_mismatch" {
		t.Errorf("moved anchor = %s/%s, want refused/anchor_mismatch", status, refusal)
	}
}

func TestPlanIsAtomicPerSetBaselineFirst(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	var ids []int64
	rows, err := s.st.query("SELECT id FROM prediction_marks WHERE set_id=? ORDER BY id LIMIT 2", setID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 2 {
		t.Fatalf("marks = %d, want 2", len(ids))
	}

	s.st.onForecastApply = func(op int) error {
		if op == 3 { // baseline (1), mark 1 (2), mark 2 (3)
			return fmt.Errorf("injected failure")
		}
		return nil
	}
	defer func() { s.st.onForecastApply = nil }()

	first := int64(1111)
	err = s.st.ApplyForecastPlan(forecastPlan{
		setID: setID,
		baseline: &forecastBaseline{S0: "2026-10-19", BaseDate: "2026-10-19", S0Fen: 1000,
			Index: []QuoteBar{{Date: "2026-10-19", Close: 4000}}},
		settles: []forecastSettle{
			{markID: ids[0], res: markResult{Status: markResolved, Session: "2026-10-26", CloseFen: &first}},
			{markID: ids[1], res: markResult{Status: markResolved, Session: "2026-10-26", CloseFen: &first}},
		},
	}, time.Now())
	if err == nil {
		t.Fatal("the injected failure did not surface")
	}

	var check, s0 string
	s.st.queryRow("SELECT base_check, s0_date FROM prediction_sets WHERE id=?", setID).Scan(&check, &s0)
	if check != baseCheckPending || s0 != "" {
		t.Errorf("baseline = %q/%q after a failed plan, want pending and no S0", check, s0)
	}
	var status string
	s.st.queryRow("SELECT status FROM prediction_marks WHERE id=?", ids[0]).Scan(&status)
	if status != markPending {
		t.Errorf("mark 1 = %q, want pending: the plan must roll back as one unit", status)
	}
}

func TestWaitingMarksStayPending(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-18") // Sunday 21:00 CST
	index := barsFor(tradingDays("2026-10-19", 1), 4000)     // Monday only
	stock := barsFor(tradingDays("2026-10-19", 1), 1000)

	now := shanghai(2026, time.October, 19, 10, 0)
	for _, step := range []struct {
		at           time.Time
		wantStockGot int
	}{
		{shanghai(2026, time.October, 19, 10, 0), 0}, // before S0's close
		{shanghai(2026, time.October, 19, 15, 31), 1},
		{shanghai(2026, time.October, 20, 1, 0), 0}, // after the baseline, d5 is far off
	} {
		now = step.at
		stockGot := 0
		e := evalStub(&now, index, stock, nil, nil)
		inner := e.fetch
		e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
			if code != "000852" {
				stockGot++
			}
			return inner(ctx, market, code, from, to)
		}
		if err := e.tick(s); err != nil {
			t.Fatalf("tick at %s: %v", now, err)
		}
		if stockGot != step.wantStockGot {
			t.Errorf("at %s: %d stock fetches, want %d", now.Format(time.RFC3339), stockGot, step.wantStockGot)
		}
		var refused, attempts int
		s.st.queryRow(`SELECT COUNT(*), COALESCE(MAX(attempts),0) FROM prediction_marks
			WHERE set_id=? AND status IN (?,?)`, setID, markRefused, markRefusedFinal).Scan(&refused, &attempts)
		if refused != 0 || attempts != 0 {
			t.Errorf("at %s: refused=%d attempts=%d, want 0/0", now.Format(time.RFC3339), refused, attempts)
		}
	}
}

func TestNotDueSetsCostNoFetch(t *testing.T) {
	s := newV1Server(t)
	for i := 0; i < 55; i++ {
		seedAcceptedSetAt(t, s, ingestSymbol(i), "2026-10-16")
	}
	now := shanghai(2026, time.October, 16, 22, 0) // Friday evening
	fetched := 0
	e := evalStub(&now, nil, nil, nil, nil)
	inner := e.fetch
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		fetched++
		return inner(ctx, market, code, from, to)
	}
	// A tick is bounded at 50 sets, so scheduling 55 takes two ticks; neither may fetch anything.
	for i := 0; i < 2; i++ {
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
	}
	if fetched != 0 {
		t.Errorf("%d vendor calls for 55 sets that cannot be due before Monday", fetched)
	}
	// Every mark now has a Monday-after-close wake.
	var bad int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE status=? AND next_attempt_at=''`,
		markPending).Scan(&bad)
	if bad != 0 {
		t.Errorf("%d pending marks have no wake", bad)
	}
}

func TestSelectionDoesNotStarve(t *testing.T) {
	s := newV1Server(t)
	// 300 sets with pending marks that are not due. They must not occupy the batch.
	for i := 0; i < 300; i++ {
		id := seedAcceptedSetAt(t, s, ingestSymbol(i), "2026-10-16")
		if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at='2099-01-01T00:00:00Z' WHERE set_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	index := barsFor(tradingDays("2026-10-19", 22), 4000)
	stock := barsFor(tradingDays("2026-10-19", 22), 1000)
	now := shanghai(2026, time.November, 20, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status string
	s.st.queryRow("SELECT status FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&status)
	if status != markResolved {
		t.Errorf("d5 status = %q, want resolved: a due set behind 300 non-due ones was starved", status)
	}
}

func TestDueSelectionUsesTheDueIndex(t *testing.T) {
	s := newV1Server(t)
	if s.st.driver != "sqlite" {
		t.Skip("the query plan is SQLite-specific; the Postgres test covers the same shape")
	}
	rows, err := s.st.query("EXPLAIN QUERY PLAN "+forecastSetsDueQuery,
		markPending, markRefused, "2026-01-01T00:00:00Z", predStateAccepted, 50)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "idx_pred_marks_due") {
		t.Errorf("selection does not use idx_pred_marks_due:\n%s", plan)
	}
}

func TestCalendarNotYetShowingTheSessionIsNotARefusal(t *testing.T) {
	if got := predNotYetShown(shanghai(2026, time.October, 19, 15, 31)); !got.Equal(shanghai(2026, time.October, 19, 15, 41)) {
		t.Errorf("15:31 wake = %s, want +10 minutes", got.Format(time.RFC3339))
	}
	if got := predNotYetShown(shanghai(2026, time.October, 19, 18, 0)); got.Format("2006-01-02 15:04") != "2026-10-20 09:00" {
		t.Errorf("18:00 wake = %s, want the next weekday 09:00", got.Format("2006-01-02 15:04"))
	}
	if got := predNotYetShown(shanghai(2026, time.October, 19, 9, 0)); got.Format("2006-01-02 15:04") != "2026-10-19 15:30" {
		t.Errorf("09:00 wake = %s, want 15:30 the same day", got.Format("2006-01-02 15:04"))
	}

	// A due mark whose calendar stops before today waits, keeping attempts at zero.
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-19")
	index := barsFor(tradingDays("2026-10-19", 5), 4000) // Mon–Fri, missing the next Monday
	stock := barsFor(tradingDays("2026-10-19", 5), 1000)
	now := shanghai(2026, time.October, 26, 15, 31)
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status, next string
	var attempts int
	s.st.queryRow("SELECT status, attempts, next_attempt_at FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).
		Scan(&status, &attempts, &next)
	if status == markRefused || status == markRefusedFinal || attempts != 0 {
		t.Errorf("d5 = %s/%d, want pending/0: a calendar that has not shown the session is not a refusal", status, attempts)
	}
	if next == "" {
		t.Error("d5 has no wake")
	}
}

func TestOutageNeverFinalises(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	index := barsFor(tradingDays("2026-10-19", 22), 4000)
	stock := barsFor(tradingDays("2026-10-19", 22), 1000)
	now := shanghai(2026, time.November, 20, 16, 0)

	for i := 0; i < 20; i++ {
		e := evalStub(&now, index, stock, fmt.Errorf("vendor down"), nil)
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		s.st.queryRow("SELECT status, attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).
			Scan(&status, &attempts)
		if status == markRefusedFinal {
			t.Fatalf("a source outage finalised on tick %d", i+1)
		}
		if attempts != 0 {
			t.Fatalf("tick %d counted a source failure as an attempt (%d)", i+1, attempts)
		}
		now = now.Add(2 * time.Hour)
	}

	// One successful delivery settles it.
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status string
	s.st.queryRow("SELECT status FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&status)
	if status != markResolved {
		t.Errorf("d5 = %q after recovery, want resolved", status)
	}
}

func TestCatchUpAfterDryPeriodNeverFinalisesOnFirstFailure(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	sessions := tradingDays("2026-10-19", 40)
	index := barsFor(sessions, 4000)
	// The stock traded before S0 and once at session 10. d5's session is before its base date, so the
	// mark fails with no_bar while the baseline itself resolves.
	stock := []QuoteBar{bar("2026-10-15", 1000, 1000, 1010, 990), bar(sessions[10], 1000, 1000, 1010, 990)}
	now := shanghai(2026, time.December, 15, 16, 0)

	for i := 1; i <= 4; i++ {
		e := evalStub(&now, index, stock, nil, nil)
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
		var status string
		s.st.queryRow("SELECT status FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&status)
		want := markRefused
		if i == 4 {
			want = markRefusedFinal
		}
		if status != want {
			t.Errorf("failure %d: d5 = %q, want %q", i, status, want)
		}
		if i == 4 {
			var attempts int
			s.st.queryRow("SELECT attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&attempts)
			if attempts != 4 {
				t.Errorf("final attempts = %d, want 4", attempts)
			}
		}
		if i < 4 {
			if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at='' WHERE set_id=? AND checkpoint='d5'`, setID); err != nil {
				t.Fatal(err)
			}
		}
		now = now.Add(2 * time.Hour)
	}
}

func TestBackoffEscalatesOnTheEvaluatorClock(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	var markID int64
	s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID)

	now := shanghai(2026, time.October, 20, 16, 0)
	for attempts, want := range map[int]time.Duration{0: time.Hour, 1: 6 * time.Hour, 2: 24 * time.Hour, 3: 24 * time.Hour} {
		if _, err := s.st.exec("UPDATE prediction_marks SET attempts=?, status=? WHERE id=?", attempts, markPending, markID); err != nil {
			t.Fatal(err)
		}
		tx, err := s.st.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.st.DeferForecastMarks(tx, []forecastMark{{ID: markID, Status: markPending, Attempts: attempts}},
			"no_bar", "2026-10-20", now); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var next, resolved string
		var gotAttempts int
		s.st.queryRow("SELECT next_attempt_at, resolved_at, attempts FROM prediction_marks WHERE id=?", markID).
			Scan(&next, &resolved, &gotAttempts)
		if wantNext := now.Add(want).UTC().Format(time.RFC3339); next != wantNext {
			t.Errorf("attempts %d: next = %s, want %s", attempts, next, wantNext)
		}
		if gotAttempts != attempts+1 {
			t.Errorf("attempts %d: stored %d, want %d", attempts, gotAttempts, attempts+1)
		}
		if resolved != "" {
			t.Errorf("attempts %d: resolved_at set while refused", attempts)
		}
	}
}

func TestBaselineBecomesTerminal(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	sessions := tradingDays("2026-10-19", 12)
	index := barsFor(sessions, 4000)
	now := shanghai(2026, time.November, 10, 16, 0)

	// A bar only outside both windows (500 days back): no usable start, but a delivered body.
	oldOnly := []QuoteBar{bar(shiftDays("2026-10-16", -500), 1000, 1000, 1010, 990)}
	for i := 0; i < 4; i++ {
		e := evalStub(&now, index, oldOnly, nil, nil)
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at='' WHERE set_id=?`, setID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var check string
	s.st.queryRow("SELECT base_check FROM prediction_sets WHERE id=?", setID).Scan(&check)
	if check != baseRefusedFinal {
		t.Fatalf("base_check = %q, want %q", check, baseRefusedFinal)
	}
	var wrong int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks
		WHERE set_id=? AND NOT (status=? AND refusal=?)`, setID, markRefusedFinal, "no_base").Scan(&wrong)
	if wrong != 0 {
		t.Errorf("%d marks were not terminal no_base", wrong)
	}
}

func TestPrematureRefusalRepairRunsOnceOnTheSignature(t *testing.T) {
	s := newV1Server(t)
	var setIDs []int64
	for i := 0; i < 4; i++ {
		setIDs = append(setIDs, seedAcceptedSetAt(t, s, ingestSymbol(i), "2026-10-16"))
	}
	for _, id := range setIDs {
		if _, err := s.st.exec(`UPDATE prediction_marks SET status=?, refusal='no_bar',
			session_date='', resolved_at='', attempts=1 WHERE set_id=?`, markRefused, id); err != nil {
			t.Fatal(err)
		}
	}
	// A mark written by the new code carries a session and must not match the signature.
	fresh := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=?, refusal='no_bar',
		session_date='2026-10-20', attempts=1 WHERE set_id=?`, markRefused, fresh); err != nil {
		t.Fatal(err)
	}

	s.runForecastRepair()

	var healed int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE status=? AND refusal='' AND attempts=0`,
		markPending).Scan(&healed)
	if healed != 20 {
		t.Errorf("healed = %d, want 20", healed)
	}
	var untouched int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND status=? AND session_date='2026-10-20'`,
		fresh, markRefused).Scan(&untouched)
	if untouched != 5 {
		t.Errorf("marks with a real session touched = %d, want 5", untouched)
	}
	rows := auditRows(t, s, AuditForecastRepair)
	if len(rows) != 1 || !strings.Contains(rows[0].Detail, `"count":20`) {
		t.Fatalf("repair audit = %+v, want one row with count 20", rows)
	}
	// A second boot does nothing and writes nothing.
	s.runForecastRepair()
	if rows := auditRows(t, s, AuditForecastRepair); len(rows) != 1 {
		t.Errorf("a second repair wrote %d rows, want 1", len(rows))
	}
}

func TestHealAndApplyNeverCallThePoolInsideATransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	var markID int64
	s.st.queryRow("SELECT id FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).Scan(&markID)
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=?, refusal='no_bar', session_date='', resolved_at='' WHERE id=?`,
		markRefused, markID); err != nil {
		t.Fatal(err)
	}

	// On SQLite a pooled call inside the transaction would deadlock on the single connection. A
	// two-second deadline turns that into a failure rather than a hung test.
	for name, fn := range map[string]func(){
		"heal": func() { s.runForecastRepair() },
		"apply": func() {
			_ = s.st.ApplyForecastPlan(forecastPlan{setID: setID, settles: []forecastSettle{
				{markID: markID, res: markResult{Status: markResolved, Session: "2026-10-26"}},
			}}, time.Now())
		},
	} {
		done := make(chan struct{})
		go func() { fn(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("%s deadlocked: a pool call inside the transaction", name)
		}
	}
}

func TestDueCountMatchesSelection(t *testing.T) {
	s := newV1Server(t)
	for i := 0; i < 4; i++ {
		seedAcceptedSetAt(t, s, ingestSymbol(i), "2026-10-16")
	}
	now := time.Now()
	sets, err := s.st.ForecastSetsDue(now, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.st.CountForecastSetsDue(); got != int64(len(sets)) || got != 4 {
		t.Errorf("count = %d, selection = %d, want 4/4", got, len(sets))
	}
}

func TestSourceFailuresAreNotMarkSpecific(t *testing.T) {
	for _, code := range []string{"fetch_failed", "not_index", "calendar_bad", "no_bench", "unknown_code"} {
		if got := refusalClassOf(code); got != refusalSource {
			t.Errorf("%s is %q, want source", code, got)
		}
	}
	for _, code := range []string{"no_bar", "no_base"} {
		if got := refusalClassOf(code); got != refusalMarkSpecific {
			t.Errorf("%s is %q, want mark-specific", code, got)
		}
	}
}

func TestFinalityNeedsSessionsAsWellAsAttempts(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	sessions := tradingDays("2026-10-19", 40)
	index := barsFor(sessions, 4000)
	stock := []QuoteBar{bar("2026-10-15", 1000, 1000, 1010, 990), bar(sessions[10], 1000, 1000, 1010, 990)}

	early, _ := time.ParseInLocation("2006-01-02", sessions[11], chinaZone)
	early = time.Date(early.Year(), early.Month(), early.Day(), 16, 0, 0, 0, chinaZone)
	for i := 0; i < 4; i++ {
		e := evalStub(&early, index, stock, nil, nil)
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at='' WHERE set_id=? AND checkpoint='d5'`, setID); err != nil {
				t.Fatal(err)
			}
		}
	}
	var status string
	var attempts int
	s.st.queryRow("SELECT status, attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).
		Scan(&status, &attempts)
	if status == markRefusedFinal {
		t.Fatalf("finalised after 4 attempts but only 6 sessions past due")
	}
	if status != markRefused || attempts != 4 {
		t.Fatalf("early: %s/%d, want refused/4", status, attempts)
	}

	late, _ := time.ParseInLocation("2006-01-02", sessions[29], chinaZone)
	late = time.Date(late.Year(), late.Month(), late.Day(), 16, 0, 0, 0, chinaZone)
	if _, err := s.st.exec(`UPDATE prediction_marks SET next_attempt_at='' WHERE set_id=? AND checkpoint='d5'`, setID); err != nil {
		t.Fatal(err)
	}
	e := evalStub(&late, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	s.st.queryRow("SELECT status, attempts FROM prediction_marks WHERE set_id=? AND checkpoint='d5'", setID).
		Scan(&status, &attempts)
	if status != markRefusedFinal || attempts != 5 {
		t.Errorf("late: %s/%d, want refused_final/5 (the final attempt is recorded)", status, attempts)
	}
}

func TestHealDoesNotMatchANewBaselineRefusal(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	index := barsFor(tradingDays("2026-10-19", 12), 4000)
	now := shanghai(2026, time.November, 10, 16, 0)
	oldOnly := []QuoteBar{bar(shiftDays("2026-10-16", -500), 1000, 1000, 1010, 990)}
	e := evalStub(&now, index, oldOnly, nil, nil) // no usable start bar: a baseline no_base
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var sessions int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND session_date<>''`, setID).Scan(&sessions)
	if sessions == 0 {
		t.Fatal("a baseline no_bar did not record a session; the heal would mistake it for a pre-P1 refusal")
	}

	s.runForecastRepair()
	var healed int
	s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND status=?`, setID, markPending).Scan(&healed)
	if healed != 0 {
		t.Errorf("%d new-code baseline refusals were healed; they are real attempts", healed)
	}
}

func TestUnknownCheckpointTerminalises(t *testing.T) {
	s := newV1Server(t)
	setID := seedAcceptedSetAt(t, s, "600160", "2026-10-16")
	if _, err := s.st.exec(`INSERT INTO prediction_marks(set_id, checkpoint, status, bench_code)
		VALUES(?, 'weird', ?, 'sh000852')`, setID, markPending); err != nil {
		t.Fatal(err)
	}
	index := barsFor(tradingDays("2026-10-19", 12), 4000)
	stock := barsFor(tradingDays("2026-10-19", 12), 1000)
	now := shanghai(2026, time.November, 10, 16, 0)
	e := evalStub(&now, index, stock, nil, nil)
	if err := e.tick(s); err != nil {
		t.Fatal(err)
	}
	var status, refusal string
	s.st.queryRow("SELECT status, refusal FROM prediction_marks WHERE set_id=? AND checkpoint='weird'", setID).
		Scan(&status, &refusal)
	if status != markRefusedFinal || refusal != "no_schedule" {
		t.Errorf("weird checkpoint = %s/%s, want refused_final/no_schedule", status, refusal)
	}
}
