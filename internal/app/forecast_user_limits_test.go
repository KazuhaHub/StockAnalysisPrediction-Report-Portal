package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForecastPerUserLimits(t *testing.T) {
	c := &forecastCompute{}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	a, ok := c.beginCustom("alice", "query-a", 2, now)
	if !ok {
		t.Fatal("first query refused")
	}
	if _, ok := c.beginCustom("alice", "query-b", 2, now); ok {
		t.Fatal("one user ran distinct queries concurrently")
	}
	b, ok := c.beginCustom("alice", "query-a", 2, now)
	if !ok {
		t.Fatal("identical follower refused")
	}
	if _, ok := c.beginCustom("alice", "query-a", 2, now); ok {
		t.Fatal("request ceiling bypassed by identical follower")
	}
	other, ok := c.beginCustom("bob", "query-b", 2, now)
	if !ok {
		t.Fatal("one user spent another user's budget")
	}
	other.release()
	a.release()
	b.release()
	if _, ok := c.beginCustom("alice", "query-b", 2, now); ok {
		t.Fatal("completion reset the rate budget")
	}
	next, ok := c.beginCustom("alice", "query-b", 2, now.Add(time.Minute))
	if !ok {
		t.Fatal("next window stayed blocked")
	}
	next.release()
	for i := 0; i < 3; i++ {
		if !c.allowMembers("alice", 3, now) {
			t.Fatal("members refused below limit")
		}
	}
	if c.allowMembers("alice", 3, now) {
		t.Fatal("members ceiling bypassed")
	}
	if !c.allowMembers("bob", 3, now) || !c.allowMembers("alice", 3, now.Add(time.Minute)) {
		t.Fatal("members window/user isolation broken")
	}
	for i := 0; i < 100; i++ {
		if !c.allowMembers("alice", 0, now) {
			t.Fatal("zero rate is not unlimited")
		}
	}
	unlimited, ok := c.beginCustom("alice", "unlimited", 0, now.Add(2*time.Minute))
	if !ok {
		t.Fatal("zero custom rate refused")
	}
	if _, ok := c.beginCustom("alice", "different", 0, now.Add(2*time.Minute)); ok {
		t.Fatal("zero rate disabled distinct-query guard")
	}
	unlimited.release()
}

func TestForecastDistinctQuerySurvivesCallerCancellation(t *testing.T) {
	c := &forecastCompute{}
	lease, ok := c.beginCustom("alice", "query-a", 0, time.Now())
	if !ok {
		t.Fatal("lease refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	answer := make(chan error, 1)
	go func() {
		_, err := c.doForQuery(ctx, "cell", false, forecastComputeConfig{Official: 1, Explore: 1, Wait: time.Second}, lease, func() (*forecastCell, error) { close(entered); <-release; return &forecastCell{}, nil })
		answer <- err
	}()
	<-entered
	cancel()
	<-answer
	lease.release()
	if _, ok := c.beginCustom("alice", "query-b", 0, time.Now()); ok {
		t.Fatal("cancelled caller bypassed detached computation's query guard")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if next, ok := c.beginCustom("alice", "query-b", 0, time.Now()); ok {
			next.release()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("completed detached computation kept user's query locked")
}

func TestForecastReaderRateErrors(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "reader", scopeAll, false)
	s.st.SetSetting(setForecastCustomPerMin, "1")
	s.st.SetSetting(setForecastMembersPerMin, "1")
	// Invalid queries still cost a request: repeated bad input must not bypass the ceiling.
	for _, handler := range []handler{s.apiReaderForecastGroups, s.apiReaderForecastGroupMembers} {
		for i := 0; i < 2; i++ {
			w := httptest.NewRecorder()
			handler(w, httptest.NewRequest("GET", "/api/forecast/groups?mode=custom", nil), "reader")
			if i == 1 && (w.Code != http.StatusTooManyRequests || !hasCode(w.Body.Bytes(), "forecast_rate_limited") || w.Header().Get("Retry-After") == "") {
				t.Fatalf("rate error: %d %s", w.Code, w.Body.String())
			}
		}
	}
}
