package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestForecastLimitsAreAudited(t *testing.T) {
	s := limitsServer(t)
	if code := saveSecurity(t, s, `{"limits":{"forecast_slots_official":3,"forecast_slots_explore":0,"forecast_wait_sec":5,"forecast_custom_per_min":11,"forecast_members_per_min":13}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	rows, _ := s.st.ListAudit(AuditFilter{Action: AuditPolicyChange, TargetID: "limits"})
	if len(rows) != 1 {
		t.Fatalf("policy audit rows: %d", len(rows))
	}
	var detail struct {
		Limits map[string]int `json:"limits"`
	}
	if err := json.Unmarshal([]byte(rows[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]int{setForecastSlotsOfficial: 3, setForecastSlotsExplore: 0, setForecastWaitSec: 5, setForecastCustomPerMin: 11, setForecastMembersPerMin: 13} {
		got, ok := detail.Limits[key]
		if !ok || got != value {
			t.Errorf("audit omitted %s=%d: %v", key, value, detail.Limits)
		}
	}
	if code := saveSecurity(t, s, `{"limits":{"forecast_slots_official":3,"forecast_slots_explore":0,"forecast_wait_sec":5,"forecast_custom_per_min":11,"forecast_members_per_min":13}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if rows, _ := s.st.ListAudit(AuditFilter{Action: AuditPolicyChange, TargetID: "limits"}); len(rows) != 1 {
		t.Fatalf("unchanged limits wrote another change audit: %d", len(rows))
	}
}

func TestForecastLimitsAuditSurvivesLaterValidationFailure(t *testing.T) {
	s := limitsServer(t)
	if code := saveSecurity(t, s, `{"limits":{"forecast_slots_official":3},"captcha":{"provider":"invalid-fixture"}}`); code != http.StatusBadRequest {
		t.Fatal(code)
	}
	if s.forecastComputeLimits().Official != 3 {
		t.Fatal("fixture no longer exercises a saved limit before later validation")
	}
	rows, _ := s.st.ListAudit(AuditFilter{Action: AuditPolicyChange, TargetID: "limits"})
	if len(rows) != 1 {
		t.Fatalf("saved capacity changed without an audit: %d rows", len(rows))
	}
}

func TestForecastPurgeBetweenLoadAndAdmission(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "reader", scopeAll, false)
	sampleMembers(t, s, 32, nil)
	fired := false
	s.st.onSettingRead = func(key string) {
		if key != setForecastWaitSec || fired {
			return
		}
		fired = true
		if _, err := s.st.exec("DELETE FROM prediction_sets"); err != nil {
			t.Fatal(err)
		}
		s.purgeForecastGroups()
	}
	w := httptest.NewRecorder()
	s.apiReaderForecastGroups(w, httptest.NewRequest("GET", "/api/forecast/groups", nil), "reader")
	if !fired {
		t.Fatal("purge interleaving was not exercised")
	}
	if w.Code != http.StatusServiceUnavailable || !hasCode(w.Body.Bytes(), "forecast_busy") {
		t.Fatalf("stale loaded population was served: %d", w.Code)
	}
	s.forecastGroupsMu.Lock()
	n := len(s.forecastGroupsCache)
	s.forecastGroupsMu.Unlock()
	if n != 0 {
		t.Fatalf("pre-purge population repopulated %d cache cells", n)
	}
	s.st.onSettingRead = nil
	readerGroups(t, s, "reader", "")
}

func TestForecastComputeCachedAndSmallBypassSlots(t *testing.T) {
	s := newV1Server(t)
	s.st.SetSetting(setForecastSlotsOfficial, "1")
	sets := sampleMembers(t, s, 32, nil)
	periods := []forecastPeriod{{Start: "2025-06-01", End: "2025-06-30", Label: "2025-06"}}
	all := forecastPopulation{Key: "all"}
	if _, err := s.buildForecastGroupsRequest(context.Background(), periods, sets, predTestRtype, "2026-10-18", true, time.Time{}, all, s.forecastGeneration(), nil); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	blocker := computeAsync(&s.forecastCompute, context.Background(), "blocker", true, s.forecastComputeLimits(), func() (*forecastCell, error) { close(entered); <-release; return &forecastCell{}, nil })
	<-entered
	defer func() { close(release); waitCompute(t, blocker) }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	groups, err := s.buildForecastGroupsRequest(ctx, periods, sets, predTestRtype, "2026-10-18", true, time.Time{}, all, s.forecastGeneration(), nil)
	if err != nil || groups[0].Cells["d5"].State != "mature" {
		t.Fatalf("cached request waited for a slot: %v", err)
	}
	groups, err = s.buildForecastGroupsRequest(ctx, periods, sets[:1], predTestRtype, "2026-10-18", true, time.Time{}, forecastPopulation{Key: "small", OUs: []int64{1}}, s.forecastGeneration(), nil)
	if err != nil || groups[0].Cells["d5"].State != "small" {
		t.Fatalf("maturity gate took a scoring slot: %v", err)
	}
}

func TestForecastBusyReturnsNoPartialRecord(t *testing.T) {
	s := newV1Server(t)
	s.st.SetSetting(setForecastSlotsExplore, "0")
	sampleMembers(t, s, 32, nil)
	w := httptest.NewRecorder()
	s.apiForecastGroups(w, httptest.NewRequest("GET", "/api/admin/forecast/groups?mode=custom&from=2025-06-01&to=2025-06-30&group=month", nil), "admin")
	if w.Code != http.StatusServiceUnavailable || !hasCode(w.Body.Bytes(), "forecast_busy") || w.Header().Get("Retry-After") == "" {
		t.Fatalf("busy response: %d %s", w.Code, w.Body.String())
	}
}

func TestForecastLimitsAreSettings(t *testing.T) {
	s := limitsServer(t)
	defaults := map[string]int{"forecast_slots_official": 2, "forecast_slots_explore": 2, "forecast_wait_sec": 10, "forecast_custom_per_min": 20, "forecast_members_per_min": 60}
	for key, value := range defaults {
		if s.limitsJSON()[key] != value {
			t.Fatalf("%s default: %v", key, s.limitsJSON()[key])
		}
	}
	if code := saveSecurity(t, s, `{"limits":{"forecast_slots_official":3,"forecast_slots_explore":0,"forecast_wait_sec":4,"forecast_custom_per_min":9,"forecast_members_per_min":10}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	cfg := s.forecastComputeLimits()
	if cfg.Official != 3 || cfg.Explore != 0 || cfg.Wait != 4*time.Second {
		t.Fatal(cfg)
	}
	for _, tc := range []struct {
		key string
		bad int
	}{{"forecast_slots_official", 0}, {"forecast_slots_official", 9}, {"forecast_slots_explore", -1}, {"forecast_slots_explore", 9}, {"forecast_wait_sec", 0}, {"forecast_wait_sec", 61}, {"forecast_custom_per_min", -1}, {"forecast_custom_per_min", 601}, {"forecast_members_per_min", -1}, {"forecast_members_per_min", 601}} {
		if code := saveSecurity(t, s, fmt.Sprintf(`{"limits":{%q:%d}}`, tc.key, tc.bad)); code != http.StatusBadRequest {
			t.Fatalf("%s=%d: %d", tc.key, tc.bad, code)
		}
	}
	if code := saveSecurity(t, s, `{"limits":{"forecast_custom_per_min":0,"forecast_members_per_min":0}}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if code := saveSecurity(t, s, `{}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if s.forecastComputeLimits() != cfg {
		t.Fatal("unrelated save reset forecast capacity")
	}
}

type computeAnswer struct {
	cell *forecastCell
	err  error
}

func computeAsync(c *forecastCompute, ctx context.Context, key string, official bool, cfg forecastComputeConfig, fn func() (*forecastCell, error)) <-chan computeAnswer {
	out := make(chan computeAnswer, 1)
	go func() { cell, err := c.do(ctx, key, official, cfg, fn); out <- computeAnswer{cell, err} }()
	return out
}

func waitCompute(t *testing.T, ch <-chan computeAnswer) computeAnswer {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(2 * time.Second):
		t.Fatal("compute did not finish")
		return computeAnswer{}
	}
}

func waitFlightCallers(t *testing.T, c *forecastCompute, key string, n int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		f := c.flights[key]
		ready := f != nil && f.callers >= n
		c.mu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("flight followers did not join")
}

func TestForecastComputeIsBounded(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 1, Wait: 80 * time.Millisecond}
	entered, release := make(chan struct{}), make(chan struct{})
	first := computeAsync(c, context.Background(), "explore-first", false, cfg, func() (*forecastCell, error) {
		close(entered)
		<-release
		return &forecastCell{State: "mature"}, nil
	})
	<-entered
	defer func() { close(release); waitCompute(t, first) }()
	var extra atomic.Int32
	second := computeAsync(c, context.Background(), "explore-second", false, cfg, func() (*forecastCell, error) {
		extra.Add(1)
		return &forecastCell{}, nil
	})
	if out := waitCompute(t, second); !errors.Is(out.err, errForecastBusy) {
		t.Fatalf("saturation: %v", out.err)
	}
	if extra.Load() != 0 {
		t.Fatal("saturated computation ran")
	}
	official := computeAsync(c, context.Background(), "official", true, cfg, func() (*forecastCell, error) { return &forecastCell{State: "mature"}, nil })
	if out := waitCompute(t, official); out.err != nil || out.cell.State != "mature" {
		t.Fatalf("exploration starved official: %+v", out)
	}
}

func TestForecastComputeSharesDetachedFlight(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 1, Wait: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	fn := func() (*forecastCell, error) {
		calls.Add(1)
		close(entered)
		<-release
		return &forecastCell{State: "mature"}, nil
	}
	leader := computeAsync(c, ctx, "same", true, cfg, fn)
	<-entered
	follower := computeAsync(c, context.Background(), "same", true, cfg, fn)
	waitFlightCallers(t, c, "same", 2)
	cancel()
	if out := waitCompute(t, leader); !errors.Is(out.err, context.Canceled) {
		t.Fatal(out.err)
	}
	close(release)
	if out := waitCompute(t, follower); out.err != nil || out.cell.State != "mature" {
		t.Fatalf("leader cancellation killed follower: %+v", out)
	}
	if calls.Load() != 1 {
		t.Fatalf("identical calls scored %d times", calls.Load())
	}
}

func TestForecastComputePromotesQueuedOfficialFollower(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 1, Wait: time.Second}
	entered, release := make(chan struct{}), make(chan struct{})
	first := computeAsync(c, context.Background(), "explore-running", false, cfg, func() (*forecastCell, error) { close(entered); <-release; return &forecastCell{}, nil })
	<-entered
	defer func() { close(release); waitCompute(t, first) }()
	var calls atomic.Int32
	fn := func() (*forecastCell, error) { calls.Add(1); return &forecastCell{State: "mature"}, nil }
	explore := computeAsync(c, context.Background(), "shared", false, cfg, fn)
	waitFlightCallers(t, c, "shared", 1)
	official := computeAsync(c, context.Background(), "shared", true, cfg, fn)
	if out := waitCompute(t, official); out.err != nil {
		t.Fatalf("official joined a starved exploration queue: %v", out.err)
	}
	if out := waitCompute(t, explore); out.err != nil {
		t.Fatal(out.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("promotion duplicated scoring: %d", calls.Load())
	}
}

func TestForecastComputeFailureReleasesCapacity(t *testing.T) {
	c := &forecastCompute{}
	cfg := forecastComputeConfig{Official: 1, Explore: 0, Wait: time.Second}
	if _, err := c.do(context.Background(), "disabled", false, cfg, func() (*forecastCell, error) { t.Error("disabled exploration ran"); return nil, nil }); !errors.Is(err, errForecastBusy) {
		t.Fatal(err)
	}
	if _, err := c.do(context.Background(), "panic", true, cfg, func() (*forecastCell, error) { panic("fixture") }); !errors.Is(err, errForecastCompute) {
		t.Fatal(err)
	}
	if _, err := c.do(context.Background(), "next", true, cfg, func() (*forecastCell, error) { return &forecastCell{}, nil }); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != [2]int{} || len(c.flights) != 0 {
		t.Fatalf("compute leaked capacity: %v %d", c.active, len(c.flights))
	}
}
