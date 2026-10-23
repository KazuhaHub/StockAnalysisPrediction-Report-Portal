package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// signalDay decodes the counters stored for one UTC day, failing when the row is not a flat object
// of integers.
func signalDay(t *testing.T, st *Store, day string) map[string]int64 {
	t.Helper()
	raw := st.GetSetting(forecastSignalsKey(day), "")
	if raw == "" {
		return nil
	}
	var out map[string]int64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("signal row %s is not a flat object of integers: %q: %v", day, raw, err)
	}
	return out
}

func TestForecastSignalsFlushIsIdempotent(t *testing.T) {
	st := newTestStore(t)
	var g forecastSignals
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		g.add(sigRequests, now)
	}
	g.add(sigCacheHits, now)
	for i := 0; i < 3; i++ {
		if err := g.flush(st, now); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]int64{sigRequests: 5, sigCacheHits: 1}
	if got := signalDay(t, st, "2026-10-23"); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeat flushes inflated the counters: %v, want %v", got, want)
	}
	g.add(sigRequests, now)
	for i := 0; i < 2; i++ {
		if err := g.flush(st, now); err != nil {
			t.Fatal(err)
		}
	}
	want[sigRequests] = 6
	if got := signalDay(t, st, "2026-10-23"); !reflect.DeepEqual(got, want) {
		t.Fatalf("a flush after new events: %v, want %v", got, want)
	}
}

func TestForecastSignalsFailedFlushKeepsTheDelta(t *testing.T) {
	st := newTestStore(t)
	bad := newTestStore(t)
	if err := bad.Close(); err != nil {
		t.Fatal(err)
	}
	var g forecastSignals
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	g.add(sigRequests, now)
	g.add(sigRequests, now)
	if err := g.flush(bad, now); err == nil {
		t.Fatal("a flush into a closed store reported success")
	}
	g.add(sigRequests, now)
	if err := g.flush(st, now); err != nil {
		t.Fatal(err)
	}
	if err := g.flush(st, now); err != nil {
		t.Fatal(err)
	}
	if got := signalDay(t, st, "2026-10-23")[sigRequests]; got != 3 {
		t.Fatalf("the retry lost or repeated the failed delta: %d, want 3", got)
	}
}

func TestForecastSignalsContinueAfterARestart(t *testing.T) {
	st := newTestStore(t)
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	var before forecastSignals
	for i := 0; i < 4; i++ {
		before.add(sigDrawerOpens, now)
	}
	if err := before.flush(st, now); err != nil {
		t.Fatal(err)
	}
	var after forecastSignals // a new process holds no memory of the first
	after.add(sigDrawerOpens, now)
	after.add(sigDrawerOpens, now)
	if err := after.flush(st, now); err != nil {
		t.Fatal(err)
	}
	if got := signalDay(t, st, "2026-10-23")[sigDrawerOpens]; got != 6 {
		t.Fatalf("a restart reset the day: %d, want 6", got)
	}
}

func TestForecastSignalsAreKeyedByTheEventDay(t *testing.T) {
	st := newTestStore(t)
	var g forecastSignals
	g.add(sigRequests, time.Date(2026, 10, 23, 23, 59, 59, 0, time.UTC))
	g.add(sigRequests, time.Date(2026, 10, 24, 0, 0, 1, 0, time.UTC))
	g.add(sigRequests, time.Date(2026, 10, 24, 0, 0, 2, 0, time.UTC))
	// One late flush still files each count under the day it happened on.
	if err := g.flush(st, time.Date(2026, 10, 24, 5, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := signalDay(t, st, "2026-10-23")[sigRequests]; got != 1 {
		t.Errorf("first day = %d, want 1", got)
	}
	if got := signalDay(t, st, "2026-10-24")[sigRequests]; got != 2 {
		t.Errorf("second day = %d, want 2", got)
	}
}

func TestForecastSignalsPruneOldDaysOnly(t *testing.T) {
	st := newTestStore(t)
	for key, value := range map[string]string{
		forecastSignalsKey("2026-09-01"):  `{"requests":1}`,
		forecastSignalsKey("2026-10-08"):  `{"requests":2}`, // 15 days before the flush: dropped
		forecastSignalsKey("2026-10-09"):  `{"requests":3}`, // 14 days before the flush: kept
		forecastSignalsKey("2026-10-22"):  `{"requests":4}`,
		"forecast_signals_note":           "not a day",
		"forecast_viewers_2026-W01":       "7",
		"forecast_state_calls_2020-01-01": "{}",
	} {
		if err := st.SetSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	var g forecastSignals
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	g.add(sigRequests, now)
	if err := g.flush(st, now); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{forecastSignalsKey("2026-09-01"), forecastSignalsKey("2026-10-08")} {
		if got := st.GetSetting(key, ""); got != "" {
			t.Errorf("%s survived the retention window: %q", key, got)
		}
	}
	for _, key := range []string{forecastSignalsKey("2026-10-09"), forecastSignalsKey("2026-10-22"), forecastSignalsKey("2026-10-23"),
		"forecast_signals_note", "forecast_viewers_2026-W01", "forecast_state_calls_2020-01-01"} {
		if got := st.GetSetting(key, ""); got == "" {
			t.Errorf("%s was pruned but is inside the window or not a signal day", key)
		}
	}
}

func TestForecastSignalsAcceptOnlyTheClosedVocabulary(t *testing.T) {
	st := newTestStore(t)
	var g forecastSignals
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	g.add("some-username", now)
	g.add(sigRefusedPrefix+"some-username", now)
	g.add(sigRequests, now)
	g.refuse("some-username", now) // an unknown reason is still a refusal, filed as other
	g.refuse("ou_none", now)
	if err := g.flush(st, now); err != nil {
		t.Fatal(err)
	}
	got := signalDay(t, st, "2026-10-23")
	want := map[string]int64{sigRequests: 1, sigRefusedPrefix + "other": 1, sigRefusedPrefix + "ou_none": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
	for name := range got {
		if !forecastSignalVocabulary[name] {
			t.Errorf("%q is stored but not in the closed vocabulary", name)
		}
	}
}

func TestForecastSignalsKeepADamagedRow(t *testing.T) {
	st := newTestStore(t)
	key := forecastSignalsKey("2026-10-23")
	var g forecastSignals
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	for _, damaged := range []string{"not json", `{"requests":1.5}`, `[1,2]`, `{"requests":"3"}`} {
		if err := st.SetSetting(key, damaged); err != nil {
			t.Fatal(err)
		}
		g.add(sigRequests, now)
		if err := g.flush(st, now); err == nil {
			t.Fatalf("a damaged row %q was merged as if it were readable", damaged)
		}
		if got := st.GetSetting(key, ""); got != damaged {
			t.Fatalf("the damaged row %q was overwritten with %q", damaged, got)
		}
		// Nothing was lost: once the row is readable again the delta is written once.
		if err := st.SetSetting(key, `{"requests":10}`); err != nil {
			t.Fatal(err)
		}
		if err := g.flush(st, now); err != nil {
			t.Fatal(err)
		}
		if got := signalDay(t, st, "2026-10-23")[sigRequests]; got != 11 {
			t.Fatalf("after repairing %q: %d, want 11", damaged, got)
		}
	}
}

func TestForecastSignalsVocabularyHasNoIdentityShapedName(t *testing.T) {
	for name := range forecastSignalVocabulary {
		if name == "" || strings.ToLower(name) != name || strings.ContainsAny(name, " \t:@/") {
			t.Errorf("%q is not a lower-case counter name", name)
		}
	}
	for _, name := range []string{sigRequests, sigCacheHits, sigFlightsJoined, sigScored, sigBusy, sigRateLimited, sigDrawerOpens} {
		if !forecastSignalVocabulary[name] {
			t.Errorf("%q is missing from the vocabulary", name)
		}
	}
	for _, reason := range forecastRefusalReasons {
		if !forecastSignalVocabulary[sigRefusedPrefix+reason] {
			t.Errorf("refusal reason %q is not countable", reason)
		}
	}
}

func TestPostgresForecastSignalsFlush(t *testing.T) {
	st := pgStore(t)
	now := time.Date(2077, 1, 4, 12, 0, 0, 0, time.UTC)
	key := forecastSignalsKey("2077-01-04")
	if err := st.DeleteSetting(key); err != nil {
		t.Fatal(err)
	}
	defer st.DeleteSetting(key)
	var g forecastSignals
	g.add(sigRequests, now)
	g.add(sigRequests, now)
	g.refuse("ou_none", now)
	for i := 0; i < 3; i++ {
		if err := g.flush(st, now); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string]int64{sigRequests: 2, sigRefusedPrefix + "ou_none": 1}
	if got := signalDay(t, st, "2077-01-04"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Postgres counters = %v, want %v", got, want)
	}
}
