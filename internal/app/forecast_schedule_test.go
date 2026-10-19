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

func TestCheckpointArithmeticToday(t *testing.T) {
	madeAt := shanghai(2026, time.October, 16, 21, 0) // Friday after the close
	sessions := tradingDays("2026-10-19", 12)

	sch := predScheduleFor("d5", madeAt, true, "", sessions[:6])
	if !sch.Known || sch.S0 != sessions[0] || sch.Record != sessions[4] || sch.Due != sessions[5] {
		t.Fatalf("d5 = %+v, want S0=%s record=%s due=%s", sch, sessions[0], sessions[4], sessions[5])
	}
	// Today's boundary: len == n is NOT due; len == n+1 is.
	if got := predScheduleFor("d5", madeAt, true, "", sessions[:5]); got.Known {
		t.Errorf("d5 with 5 sessions is known (%+v); today's rule requires N+1", got)
	}
	if got := predScheduleFor("d5", madeAt, true, "", sessions[:6]); !got.Known {
		t.Errorf("d5 with 6 sessions is not due")
	}
	// A frozen S0 wins over the first session in the window.
	if got := predScheduleFor("d5", madeAt, true, sessions[1], sessions[:7]); got.S0 != sessions[1] {
		t.Errorf("S0 = %q, want the frozen %q", got.S0, sessions[1])
	}
	// h12 is calendar-based and only exists for a standard horizon.
	if got := predScheduleFor("h12", madeAt, false, "", sessions); got.Known {
		t.Errorf("h12 on a non-standard horizon is known: %+v", got)
	}
	target := addMonths("2026-10-16", 12)
	if got := predScheduleFor("h12", madeAt, true, "", sessions); got.Known || got.Due != target {
		t.Errorf("h12 with only %d sessions = %+v, want not known with target %s", len(sessions), got, target)
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

	for i := 0; i < 4; i++ {
		e := evalStub(&now, index, nil, nil, nil) // the stock series is empty: no start bar
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
	e := evalStub(&now, index, nil, nil, nil) // no stock bar: a baseline no_bar
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
