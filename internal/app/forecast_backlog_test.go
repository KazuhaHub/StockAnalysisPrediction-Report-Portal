package app

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestForecastBacklogIncludesSleepingRetries(t *testing.T) {
	s := newV1Server(t)
	id := seedAcceptedSetAt(t, s, "688469", "2026-10-19")
	now := shanghai(2026, time.October, 29, 17, 0)
	next := now.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=?, refusal='fetch_failed', session_date='2026-10-27', next_attempt_at=? WHERE set_id=? AND checkpoint='d5'`, markRefused, next, id); err != nil {
		t.Fatal(err)
	}
	e := &forecastEvaluator{now: func() time.Time { return now }}
	e.recordDeliveryFailure(s, forecastWorkSet{SetID: id, Symbol: "688469"}, "adjustment", "tencent", "2026-10-20", "2026-10-27", errors.New("no adjusted rows"))
	b, err := s.forecastBacklog(now)
	if err != nil || b.Due != 1 || b.Overdue != 1 || b.Retrying != 1 || b.OldestMinutes != 2970 {
		t.Fatalf("backlog=%+v error=%v", b, err)
	}
	if len(b.Items) != 1 || b.Items[0].NextAttempt != next || b.Items[0].Failure == nil || b.Items[0].Failure.Stage != "adjustment" {
		t.Fatalf("items=%+v", b.Items)
	}
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=? WHERE set_id=? AND checkpoint='d5'`, markResolved, id); err != nil {
		t.Fatal(err)
	}
	b, err = s.forecastBacklog(now)
	if err != nil || b.Due != 0 {
		t.Fatalf("resolved backlog=%+v / %v", b, err)
	}
}

func TestForecastBacklogDoesNotInventSessions(t *testing.T) {
	s := newV1Server(t)
	id := seedAcceptedSetAt(t, s, "600519", "2026-10-19")
	now := shanghai(2026, time.October, 29, 17, 0)
	b, err := s.forecastBacklog(now)
	if err != nil || b.Due != 0 {
		t.Fatalf("weekday projection counted: %+v / %v", b, err)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at='2026-10-19T02:00:00Z',s0_date='2026-10-19' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	s.forecastEval.publishReadCalendar("sh000852", tradingDays("2026-10-19", 9))
	b, err = s.forecastBacklog(now)
	if err != nil || b.Due != 1 || b.Items[0].Due != "2026-10-26" {
		t.Fatalf("observed calendar not used: %+v / %v", b, err)
	}
}

func TestWatchdogUsesLedgerInsteadOfLastEmptyPass(t *testing.T) {
	s := newV1Server(t)
	id := seedAcceptedSetAt(t, s, "688469", "2026-10-19")
	now := shanghai(2026, time.October, 29, 17, 0)
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=?,session_date='2026-10-27',next_attempt_at=? WHERE set_id=? AND checkpoint='d5'`, markRefused, now.Add(time.Hour).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{Mode: forecastModeOn, LastTickAt: now.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	s.forecastWatchdogOnce(now)
	if s.st.forecastAlertState().Last["lagging"] == "" {
		t.Fatal("sleeping overdue work hidden by empty pass")
	}
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=? WHERE set_id=?`, markResolved, id); err != nil {
		t.Fatal(err)
	}
	// A stale per-pass backlog must not keep paging after the ledger is settled.
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{Mode: forecastModeOn, LastTickAt: now.Add(25 * time.Hour).UTC().Format(time.RFC3339), Pass: passStats{OldestDueUnsettledMinutes: 9999}}); err != nil {
		t.Fatal(err)
	}
	first := s.st.forecastAlertState().Last["lagging"]
	s.forecastWatchdogOnce(now.Add(25 * time.Hour))
	if s.st.forecastAlertState().Last["lagging"] != first {
		t.Fatal("last pass paged after ledger recovered")
	}
}

func TestForecastDeliveryDiagnosticsBoundAndRedact(t *testing.T) {
	s := newV1Server(t)
	e := &forecastEvaluator{}
	for i := int64(1); i <= 60; i++ {
		e.recordDeliveryFailure(s, forecastWorkSet{SetID: i, Symbol: "600519"}, "adjustment", "tencent", "2026-10-19", "2026-10-26", errors.New("Get https://user:secret@example.com/path?token=private: timeout"))
	}
	rows := s.st.forecastDeliveryFailures()
	if len(rows) != 50 {
		t.Fatalf("unbounded diagnostics: %d", len(rows))
	}
	for _, r := range rows {
		if strings.Contains(r.Detail, "secret") || strings.Contains(r.Detail, "private") || strings.Contains(r.Detail, "user:") {
			t.Fatalf("credentials leaked: %s", r.Detail)
		}
	}
	s.st.clearForecastDeliveryFailure(60)
	if len(s.st.forecastDeliveryFailures()) != 49 {
		t.Fatal("successful set kept stale failure")
	}
}

// A source with no as-of stamp cannot vouch for a bar dated today, so the evaluator settles a
// session's marks only once the date has rolled over (newestBarFinal). The lagging clock must start
// there: measured from the close, every trading evening paged at 21:00 over work nothing could do.
func TestLaggingClockStartsWhenTheBarCanBeVouchedFor(t *testing.T) {
	s := newV1Server(t)
	id := seedAcceptedSetAt(t, s, "688469", "2026-10-23")
	if _, err := s.st.exec(`UPDATE prediction_marks SET session_date='2026-10-30' WHERE set_id=? AND checkpoint='d5'`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetSetting(setForecastAuto, forecastModeOn); err != nil {
		t.Fatal(err)
	}
	evening := shanghai(2026, time.October, 30, 22, 11)
	b, err := s.forecastBacklog(evening)
	if err != nil || b.Due != 1 || b.OldestMinutes != 401 || b.Overdue != 0 || b.OldestLateMinutes != 0 {
		t.Fatalf("evening backlog=%+v error=%v", b, err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{Mode: forecastModeOn, LastTickAt: evening.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	s.forecastWatchdogOnce(evening)
	if at := s.st.forecastAlertState().Last["lagging"]; at != "" {
		t.Fatalf("lagging paged at %s for a session that cannot settle before midnight", at)
	}

	// Six hours after the rollover the work is genuinely late, and before the next open.
	morning := shanghai(2026, time.October, 31, 6, 1)
	b, err = s.forecastBacklog(morning)
	if err != nil || b.Overdue != 1 || b.OldestLateMinutes != 361 {
		t.Fatalf("morning backlog=%+v error=%v", b, err)
	}
	if err := s.st.saveForecastHeartbeat(forecastHeartbeat{Mode: forecastModeOn, LastTickAt: morning.UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	s.forecastWatchdogOnce(morning)
	if s.st.forecastAlertState().Last["lagging"] == "" {
		t.Fatal("work still unsettled six hours after the rollover did not page")
	}
}
