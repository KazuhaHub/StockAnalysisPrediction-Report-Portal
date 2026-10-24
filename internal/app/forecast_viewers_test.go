package app

import (
	"bytes"
	"encoding/hex"
	"log"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestForecastViewersConcurrentAndFailedRollover(t *testing.T) {
	st := newTestStore(t)
	var tracker forecastViewers
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := tracker.observe(st, []string{"one", "two", "three"}[i%3], now); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	bad := newTestStore(t)
	if err := bad.Close(); err != nil {
		t.Fatal(err)
	}
	next := now.AddDate(0, 0, 7)
	if err := tracker.flush(bad, next); err == nil {
		t.Fatal("failed store flush reported success")
	}
	if tracker.current != nil || len(tracker.pending) != 1 {
		t.Fatal("failed rollover retained private material or lost pending integer")
	}
	if err := tracker.flush(st, next); err != nil {
		t.Fatal(err)
	}
	if len(tracker.pending) != 0 || st.GetSetting(forecastViewerWeekKey(now), "") != "3" {
		t.Fatal("retry lost prior count")
	}
}

func TestPostgresForecastViewersFlush(t *testing.T) {
	st := pgStore(t)
	now := time.Date(2077, 1, 4, 12, 0, 0, 0, time.UTC)
	key := forecastViewerWeekKey(now)
	if err := st.DeleteSetting(key); err != nil {
		t.Fatal(err)
	}
	defer st.DeleteSetting(key)
	var tracker forecastViewers
	for _, user := range []string{"one", "two", "one"} {
		if err := tracker.observe(st, user, now); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := tracker.flush(st, now); err != nil {
			t.Fatal(err)
		}
	}
	if st.GetSetting(key, "") != "2" {
		t.Fatal("Postgres count mismatch")
	}
}

func TestWeeklyViewersStoreNoUsernames(t *testing.T) {
	st := newTestStore(t)
	var tracker forecastViewers
	var logged bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previousLog) })
	now := time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
	key := "forecast_viewers_2026-W53"
	for _, user := range []string{"viewer-alpha-private", "viewer-beta-private", "viewer-gamma-private", "viewer-alpha-private"} {
		if err := tracker.observe(st, user, now); err != nil {
			t.Fatal(err)
		}
	}
	salt := tracker.current.salt
	var private []string
	private = append(private, hex.EncodeToString(salt[:]))
	for hash := range tracker.current.seen {
		private = append(private, hex.EncodeToString(hash[:]))
	}
	if err := tracker.flush(st, now); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting(key, ""); got != "3" {
		t.Fatalf("count = %s", got)
	}
	rows, err := st.query("SELECT k,v FROM meta WHERE k LIKE 'forecast_viewers_%'")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatal(err)
		}
		count++
		if k != key {
			t.Fatalf("unexpected tracking key: %s", k)
		}
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			t.Fatalf("non-integer tracking value: %q", v)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || count != 1 {
		t.Fatalf("tracking persistence: rows=%d err=%v", count, err)
	}
	if err := tracker.flush(st, now); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting(key, ""); got != "3" {
		t.Fatalf("repeat flush inflated count: %s", got)
	}
	var dump bytes.Buffer
	if _, _, err := st.dumpTo(&dump); err != nil {
		t.Fatal(err)
	}
	for _, secret := range append(private, "viewer-alpha-private", "viewer-beta-private", "viewer-gamma-private") {
		if strings.Contains(dump.String(), secret) {
			t.Fatalf("private viewer material persisted: %s", secret)
		}
		if strings.Contains(logged.String(), secret) {
			t.Fatal("private viewer material logged")
		}
	}
	// Keep failure logging generic: even underlying driver errors must never echo a subject.
	source, err := os.ReadFile("forecast_groups_api.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `log.Print("forecast viewer observation failed")`) {
		t.Fatal("observation failure logging is not generic")
	}
	// The restart baseline is loaded once, not re-added on each flush. Same reader after restart
	// deliberately contributes again: this adoption measure is then an upper bound.
	var restart forecastViewers
	if err := restart.observe(st, "viewer-alpha-private", now); err != nil {
		t.Fatal(err)
	}
	if restart.current.salt == salt {
		t.Fatal("restart reused weekly salt")
	}
	for i := 0; i < 3; i++ {
		if err := restart.flush(st, now); err != nil {
			t.Fatal(err)
		}
	}
	if got := st.GetSetting(key, ""); got != "4" {
		t.Fatalf("restart baseline = %s", got)
	}
	next := now.AddDate(0, 0, 4)
	if err := restart.flush(st, next); err != nil {
		t.Fatal(err)
	}
	if restart.current != nil {
		t.Fatal("ended week's salt and hashes retained")
	}
	if err := restart.observe(st, "late-previous-week", now); err != nil {
		t.Fatal(err)
	}
	if restart.current != nil {
		t.Fatal("late request recreated expired salt")
	}
	if err := restart.observe(st, "viewer-alpha-private", next); err != nil {
		t.Fatal(err)
	}
	if err := restart.flush(st, next); err != nil {
		t.Fatal(err)
	}
	if got := st.GetSetting("forecast_viewers_2027-W01", ""); got != "1" {
		t.Fatalf("ISO year rollover: %s", got)
	}
	if got := st.GetSetting(key, ""); got != "4" {
		t.Fatal("rollover rewrote previous week")
	}
}

func TestForecastViewerInvalidBaselinesDoNotOverwriteCounts(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	key := forecastViewerWeekKey(now)
	for _, raw := range []string{"-1", "bad", "9223372036854775808"} {
		if err := st.SetSetting(key, raw); err != nil {
			t.Fatal(err)
		}
		var tracker forecastViewers
		if err := tracker.observe(st, "private-user", now); err == nil {
			t.Fatalf("accepted invalid baseline %s", raw)
		}
		if err := tracker.flush(st, now); err != nil {
			t.Fatal(err)
		}
		if st.GetSetting(key, "") != raw {
			t.Fatal("invalid baseline was overwritten")
		}
	}
	if err := st.SetSetting(key, "9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	var tracker forecastViewers
	if err := tracker.observe(st, "private-user", now); err == nil {
		t.Fatal("count overflow admitted")
	}
	if len(tracker.current.seen) != 0 {
		t.Fatal("overflow retained an uncountable identity")
	}
}

func TestForecastViewerFlushSchedule(t *testing.T) {
	st := newTestStore(t)
	var tracker forecastViewers
	now := time.Date(2026, 10, 22, 12, 20, 0, 0, time.UTC)
	next := now.Truncate(time.Hour).Add(time.Hour)
	if tracker.flushDue(now, next) || tracker.flushDue(next.Add(-time.Nanosecond), next) {
		t.Fatal("flush before hourly deadline")
	}
	if !tracker.flushDue(next, next) || !tracker.flushDue(next.Add(time.Minute), next) {
		t.Fatal("hourly flush deadline missed")
	}
	sunday := time.Date(2026, 10, 25, 23, 59, 0, 0, time.UTC)
	if err := tracker.observe(st, "viewer", sunday); err != nil {
		t.Fatal(err)
	}
	monday := sunday.Add(time.Minute)
	if !tracker.flushDue(monday, monday.Add(time.Hour)) {
		t.Fatal("week rollover did not override later flush deadline")
	}
	if err := tracker.flush(st, monday); err != nil {
		t.Fatal(err)
	}
	if tracker.current != nil || tracker.flushDue(monday, monday.Add(time.Hour)) {
		t.Fatal("week material survived boundary or repeated boundary flush")
	}
}

func TestForecastViewerCountsOnlySuccessfulNonAdminGroups(t *testing.T) {
	s := newV1Server(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	s.forecastEval.now = func() time.Time { return now }
	seedForecastReader(t, s, "reader", scopeAll, false)
	seedForecastReader(t, s, "none", scopeNone, false)
	mustAddUser(t, s, "admin", true)
	for _, tc := range []struct{ user, query string }{{"reader", ""}, {"reader", ""}, {"none", ""}, {"admin", ""}, {"reader", "?mode=invalid"}} {
		s.apiReaderForecastGroups(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/forecast/groups"+tc.query, nil), tc.user)
	}
	if err := s.forecastViewers.flush(s.st, now); err != nil {
		t.Fatal(err)
	}
	if got := s.st.GetSetting("forecast_viewers_2026-W43", ""); got != "1" {
		t.Fatalf("non-reader views counted: %s", got)
	}
}
