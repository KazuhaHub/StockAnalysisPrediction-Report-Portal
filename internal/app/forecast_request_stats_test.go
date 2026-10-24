package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// signalFlush writes the server's counters for the current test clock and returns today's row.
func signalFlush(t *testing.T, s *Server) map[string]int64 {
	t.Helper()
	now := s.forecastEval.clock()
	if err := s.forecastSignals.flush(s.st, now); err != nil {
		t.Fatal(err)
	}
	return signalDay(t, s.st, now.UTC().Format("2006-01-02"))
}

var slowGroupsLine = regexp.MustCompile(`(?m)^.*forecast groups: scope=.*$`)

// groupsLineFields parses "k=v" pairs of one slow-request line, keeping their order.
func groupsLineFields(t *testing.T, line string) (keys []string, values map[string]string) {
	t.Helper()
	values = map[string]string{}
	rest := line[strings.Index(line, "forecast groups: ")+len("forecast groups: "):]
	for _, pair := range strings.Fields(rest) {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			t.Fatalf("malformed field %q in %q", pair, line)
		}
		keys = append(keys, k)
		values[k] = v
	}
	return keys, values
}

func attributeAllSets(t *testing.T, s *Server, ou int64) {
	t.Helper()
	if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", ou); err != nil {
		t.Fatal(err)
	}
}

func TestSlowForecastGroupsRequestLogsTheDocumentedFields(t *testing.T) {
	s := newV1Server(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	ou := seedForecastReader(t, s, "private-reader-name", scopeOwn, false)
	sampleMembers(t, s, 32, nil)
	attributeAllSets(t, s, ou)
	s.forecastSlowGroups = time.Nanosecond // every request is "slow" for this test

	var body map[string]any
	out := captureLog(t, func() { body = readerGroups(t, s, "private-reader-name", "") })
	lines := slowGroupsLine.FindAllString(out, -1)
	if len(lines) != 1 {
		t.Fatalf("expected one slow-request line, got %d in %q", len(lines), out)
	}
	keys, v := groupsLineFields(t, lines[0])
	wantKeys := []string{"scope", "pop", "mode", "cells", "scored", "cached", "flight", "wait_ms", "dur_ms"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("fields = %v, want %v", keys, wantKeys)
	}
	if v["scope"] != "own" || v["pop"] != "ou" || v["mode"] != "month" {
		t.Errorf("scope/pop/mode = %s/%s/%s", v["scope"], v["pop"], v["mode"])
	}
	groups, _ := body["groups"].([]any)
	if cells, _ := strconv.Atoi(v["cells"]); cells != len(groups)*len(predCheckpoints) || cells == 0 {
		t.Errorf("cells = %s for %d groups", v["cells"], len(groups))
	}
	if v["scored"] != "1" || v["cached"] != "0" || v["flight"] != "0" {
		t.Errorf("a first request scores its one mature cell alone: scored=%s cached=%s flight=%s", v["scored"], v["cached"], v["flight"])
	}
	for _, k := range []string{"wait_ms", "dur_ms"} {
		if n, err := strconv.ParseInt(v[k], 10, 64); err != nil || n < 0 {
			t.Errorf("%s = %q is not a non-negative integer", k, v[k])
		}
	}
	if strings.Contains(out, "private-reader-name") {
		t.Fatalf("the log names the reader: %q", out)
	}

	// The same request again is answered from the memo: nothing scored, the mature cell cached.
	out = captureLog(t, func() { readerGroups(t, s, "private-reader-name", "") })
	_, v = groupsLineFields(t, slowGroupsLine.FindString(out))
	if v["scored"] != "0" || v["cached"] != "1" {
		t.Errorf("second request: scored=%s cached=%s, want 0 and 1", v["scored"], v["cached"])
	}
}

func TestSlowForecastGroupsLineNamesAllAndAdminPopulations(t *testing.T) {
	s := newV1Server(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	seedForecastReader(t, s, "all-reader", scopeAll, false)
	sampleMembers(t, s, 32, nil)
	s.forecastSlowGroups = time.Nanosecond
	out := captureLog(t, func() { readerGroups(t, s, "all-reader", "") })
	_, v := groupsLineFields(t, slowGroupsLine.FindString(out))
	if v["scope"] != "all" || v["pop"] != "all" {
		t.Errorf("all reader logged scope=%s pop=%s", v["scope"], v["pop"])
	}
	out = captureLog(t, func() {
		w := httptest.NewRecorder()
		s.apiForecastGroups(w, httptest.NewRequest("GET", "/api/admin/forecast/groups", nil), "admin")
		if w.Code != http.StatusOK {
			t.Fatal(w.Code)
		}
	})
	_, v = groupsLineFields(t, slowGroupsLine.FindString(out))
	if v["scope"] != "all" || v["pop"] != "all" {
		t.Errorf("the admin alias logged scope=%s pop=%s", v["scope"], v["pop"])
	}
}

func TestFastForecastGroupsRequestLogsNothing(t *testing.T) {
	s := newV1Server(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	seedForecastReader(t, s, "reader", scopeAll, false)
	sampleMembers(t, s, 32, nil)
	out := captureLog(t, func() { readerGroups(t, s, "reader", "") })
	if slowGroupsLine.MatchString(out) {
		t.Fatalf("a request under the default threshold was logged: %q", out)
	}
	if s.forecastSlowThreshold() != 3*time.Second {
		t.Fatalf("default threshold = %v, want 3s", s.forecastSlowThreshold())
	}
}

func TestForecastRequestStatsRecordFlightJoinsAndSlotWait(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 1, Wait: 2 * time.Second}
	var sig forecastSignals
	fixed := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	newStats := func() (*forecastRequestStats, context.Context) {
		st := &forecastRequestStats{sig: &sig, now: func() time.Time { return fixed }}
		return st, withForecastRequestStats(context.Background(), st)
	}
	statsA, ctxA := newStats()
	entered, release := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	hold := func() (*forecastCell, error) {
		if runs.Add(1) == 1 {
			close(entered)
			<-release
		}
		return &forecastCell{State: "mature"}, nil
	}
	answers := make(chan error, 3)
	call := func(ctx context.Context, key string) {
		go func() { _, err := c.doForQuery(ctx, key, true, cfg, nil, hold); answers <- err }()
	}
	call(ctxA, "a") // takes the only official slot and holds it
	<-entered
	statsB, ctxB := newStats()
	call(ctxB, "b") // a different flight: it queues behind the slot
	waitFlightCallers(t, c, "b", 1)
	statsC, ctxC := newStats()
	call(ctxC, "a") // the same flight as the first: it joins
	waitFlightCallers(t, c, "a", 2)
	time.Sleep(60 * time.Millisecond)
	close(release)
	for i := 0; i < 3; i++ {
		if err := <-answers; err != nil {
			t.Fatal(err)
		}
	}
	if a, joined, wait := statsA.scored.Load(), statsA.joined.Load(), statsA.waited(); a != 1 || joined != 0 || wait >= statsB.waited() {
		t.Errorf("leader: scored=%d joined=%d wait=%v (it took a free slot, so it waited less than the queued flight: %v)", a, joined, wait, statsB.waited())
	}
	if b, joined, wait := statsB.scored.Load(), statsB.joined.Load(), statsB.waited(); b != 1 || joined != 0 || wait < 50*time.Millisecond {
		t.Errorf("queued leader: scored=%d joined=%d wait=%v, want a wait of at least 50ms", b, joined, wait)
	}
	if scored, joined, wait := statsC.scored.Load(), statsC.joined.Load(), statsC.waited(); scored != 0 || joined != 1 || wait != 0 {
		t.Errorf("follower: scored=%d joined=%d wait=%v (it joined a flight already running)", scored, joined, wait)
	}
	st := newTestStore(t)
	if err := sig.flush(st, fixed); err != nil {
		t.Fatal(err)
	}
	got := signalDay(t, st, "2026-10-23")
	if got[sigScored] != 2 || got[sigFlightsJoined] != 1 {
		t.Errorf("process counters = %v, want scored 2 and flights_joined 1", got)
	}
}

func TestForecastRequestStatsAreOptional(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 1, Wait: time.Second}
	if _, err := c.do(context.Background(), "plain", true, cfg, func() (*forecastCell, error) { return &forecastCell{}, nil }); err != nil {
		t.Fatalf("a call without request statistics failed: %v", err)
	}
	var none *forecastRequestStats
	none.addCached()
	none.addJoined()
	none.addScored()
	none.addWait(time.Second)
	if got := none.waited(); got != 0 {
		t.Fatalf("nil statistics reported a wait: %v", got)
	}
}

func counterFixture(t *testing.T, scope string) (*Server, func(path string) *httptest.ResponseRecorder) {
	t.Helper()
	s := tokenServer(t)
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	seedForecastReader(t, s, "reader", scope, false)
	sampleMembers(t, s, 32, nil)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return s, func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, sessionForPath(t, s, "reader", "", path))
		return w
	}
}

func TestForecastServedRequestsCountCacheHitsScoringAndDrawerOpens(t *testing.T) {
	s, get := counterFixture(t, scopeAll)
	// A served request scores its one mature cell; the repeat is answered from the memo.
	for i := 0; i < 2; i++ {
		if w := get("/api/forecast/groups"); w.Code != http.StatusOK {
			t.Fatalf("groups: %d %s", w.Code, w.Body.String())
		}
	}
	if w := get("/api/forecast/groups/members?label=2025-06&checkpoint=d5"); w.Code != http.StatusOK {
		t.Fatalf("members: %d %s", w.Code, w.Body.String())
	}
	// A malformed drawer request is not an open.
	if w := get("/api/forecast/groups/members?label=2025-06&checkpoint=bogus"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad members: %d", w.Code)
	}
	got := signalFlush(t, s)
	want := map[string]int64{sigRequests: 2, sigCacheHits: 1, sigScored: 1, sigDrawerOpens: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counters = %v, want %v", got, want)
	}
}

func TestForecastBusyIsCounted(t *testing.T) {
	s, get := counterFixture(t, scopeAll)
	s.st.SetSetting(setForecastSlotsExplore, "0") // no exploratory capacity: a custom range is busy
	w := get("/api/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month")
	if w.Code != http.StatusServiceUnavailable || !hasCode(w.Body.Bytes(), "forecast_busy") {
		t.Fatalf("busy: %d %s", w.Code, w.Body.String())
	}
	got := signalFlush(t, s)
	want := map[string]int64{sigRequests: 1, sigBusy: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counters = %v, want %v", got, want)
	}
}

func TestForecastRateLimitsAreCountedForGroupsAndMembers(t *testing.T) {
	s, get := counterFixture(t, scopeAll)
	s.st.SetSetting(setForecastCustomPerMin, "1")
	s.st.SetSetting(setForecastMembersPerMin, "1")
	const custom = "/api/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month"
	const members = "/api/forecast/groups/members?label=2025-06&checkpoint=d5"
	if w := get(custom); w.Code != http.StatusOK {
		t.Fatalf("first custom request: %d %s", w.Code, w.Body.String())
	}
	if w := get(custom); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second custom request: %d", w.Code)
	}
	if w := get(members); w.Code != http.StatusOK {
		t.Fatalf("first members request: %d", w.Code)
	}
	if w := get(members); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second members request: %d", w.Code)
	}
	got := signalFlush(t, s)
	if got[sigRateLimited] != 2 || got[sigRequests] != 1 || got[sigDrawerOpens] != 1 {
		t.Fatalf("counters = %v, want rate_limited 2, requests 1, drawer_opens 1 (a refused request is not a served one)", got)
	}
}
