package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVendorSchedulerReservesCapacityAndDoesNotBankIdleTokens(t *testing.T) {
	s := newVendorScheduler()
	clock := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	finish, err := s.acquire(context.Background(), "tencent", vendorBackground)
	if err != nil {
		t.Fatal(err)
	}
	finish(http.StatusOK, "", nil)
	clock = clock.Add(2 * time.Second)
	if _, err := s.acquire(context.Background(), "tencent", vendorBackground); !isVendorDeferred(err) {
		t.Fatalf("background consumed the foreground reserve: %v", err)
	}
	finish, err = s.acquire(context.Background(), "tencent", vendorForeground)
	if err != nil {
		t.Fatal(err)
	}
	finish(http.StatusOK, "", nil)
	clock = clock.Add(time.Hour)
	finish, err = s.acquire(context.Background(), "tencent", vendorBackground)
	if err != nil {
		t.Fatal(err)
	}
	finish(http.StatusOK, "", nil)
	if _, err := s.acquire(context.Background(), "tencent", vendorBackground); !isVendorDeferred(err) {
		t.Fatalf("idle time created a burst: %v", err)
	}
}

func TestVendorTrafficClassifiesActualFailuresAndBoundsLatency(t *testing.T) {
	s := newVendorScheduler()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	for _, code := range []int{403, 429, 503, 404, 0, 200} {
		clock = clock.Add(time.Hour)
		done, err := s.acquire(context.Background(), "tencent", vendorForeground)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(100 * time.Millisecond)
		var failure error
		if code == 0 {
			failure = errors.New("network down")
		}
		done(code, "", failure)
		done(code, "", failure)
	}
	stats := s.snapshot()["tencent"]
	if stats.Requests != 6 || stats.Completed != 6 || stats.Failures != 5 || stats.Forbidden != 1 || stats.Throttled != 1 || stats.ServerErrors != 1 || stats.NetworkErrors != 1 || stats.P95Millis != 100 {
		t.Fatalf("actual-request accounting: %+v", stats)
	}
}

func TestVendorQueueDeadlineIsRecheckedAfterMutexReacquisition(t *testing.T) {
	s := newVendorScheduler()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	done, err := s.acquire(context.Background(), "tencent", vendorForeground)
	if err != nil {
		t.Fatal(err)
	}
	done(http.StatusOK, "", nil)
	result := make(chan error, 1)
	go func() {
		finish, err := s.acquire(context.Background(), "tencent", vendorForeground)
		if finish != nil {
			finish(http.StatusOK, "", nil)
		}
		result <- err
	}()
	waitVendorQueued(t, s, "tencent", 1)
	// A waiter can wake for a budget/queue change and then lose the mutex until its queue deadline.
	// Expiry must still prevent dispatch even though the timer was not the selected wake-up case.
	s.mu.Lock()
	clock = clock.Add(2 * time.Second)
	s.notifyLocked()
	time.Sleep(vendorQueueWait + 25*time.Millisecond)
	s.mu.Unlock()
	select {
	case err := <-result:
		var deferred *vendorDeferredError
		if !errors.As(err, &deferred) || deferred.Reason != "queue_timeout" {
			t.Fatalf("expired waiter dispatched: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired waiter did not return")
	}
	stats := s.snapshot()["tencent"]
	if stats.Requests != 1 || stats.Deferred != 1 || stats.Queued != 0 {
		t.Fatalf("expired admission spent vendor budget: %+v", stats)
	}
}

func TestVendorSchedulerForegroundOvertakesHistoryAndCancellationRemovesWaiter(t *testing.T) {
	s := newVendorScheduler()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	finish, err := s.acquire(context.Background(), "tencent", vendorForeground)
	if err != nil {
		t.Fatal(err)
	}
	finish(http.StatusOK, "", nil)
	historyCtx, cancelHistory := context.WithCancel(context.Background())
	historyResult := make(chan error, 1)
	go func() { _, err := s.acquire(historyCtx, "tencent", vendorHistory); historyResult <- err }()
	waitVendorQueued(t, s, "tencent", 1)
	foreground := make(chan error, 1)
	go func() {
		done, err := s.acquire(context.Background(), "tencent", vendorForeground)
		if err == nil {
			done(http.StatusOK, "", nil)
		}
		foreground <- err
	}()
	waitVendorQueued(t, s, "tencent", 2)
	s.mu.Lock()
	clock = clock.Add(2 * time.Second)
	s.notifyLocked()
	s.mu.Unlock()
	select {
	case err := <-foreground:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground did not overtake queued history")
	}
	cancelHistory()
	if err := <-historyResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitVendorQueued(t, s, "tencent", 0)
}

func waitVendorQueued(t *testing.T, s *vendorScheduler, source string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := len(s.sourceLocked(source).queue)
		s.mu.Unlock()
		if n == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("source %s did not reach queue length %d", source, want)
}

func TestVendorSchedulerCooldownIsSharedAndHasOnlyOneRecoveryProbe(t *testing.T) {
	s := newVendorScheduler()
	clock := time.Now()
	s.now = func() time.Time { return clock }
	done, err := s.acquire(context.Background(), "tencent", vendorBackground)
	if err != nil {
		t.Fatal(err)
	}
	done(http.StatusTooManyRequests, "120", nil)
	clock = clock.Add(time.Minute)
	if _, err := s.acquire(context.Background(), "tencent", vendorForeground); !isVendorDeferred(err) {
		t.Fatalf("foreground bypassed shared Retry-After: %v", err)
	}
	clock = clock.Add(time.Minute)
	probe, err := s.acquire(context.Background(), "tencent", vendorForeground)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(10 * time.Second)
	if _, err := s.acquire(context.Background(), "tencent", vendorBackground); !isVendorDeferred(err) {
		t.Fatalf("more than one recovery probe admitted: %v", err)
	}
	probe(http.StatusOK, "", nil)
	done, err = s.acquire(context.Background(), "tencent", vendorBackground)
	if err != nil {
		t.Fatal(err)
	}
	done(http.StatusOK, "", nil)
	if status := s.snapshot()["tencent"]; status.Requests != 3 || status.Failures != 1 || status.CoolingUntil != "" {
		t.Fatalf("incorrect actual-request diagnostics: %+v", status)
	}
}

type vendorSchedulerTransportFunc func(*http.Request) (*http.Response, error)

func (f vendorSchedulerTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVendorScheduledTransportCountsRedirectsAndGroupsSiblingHosts(t *testing.T) {
	s := newVendorScheduler()
	s.policy = vendorRatePolicy{RequestsPerMinute: 60000, BackgroundPerMinute: 60000}
	var mu sync.Mutex
	var hosts []string
	base := vendorSchedulerTransportFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hosts = append(hosts, r.URL.Hostname())
		mu.Unlock()
		status, header := http.StatusOK, http.Header{}
		if r.URL.Hostname() == "qt.gtimg.cn" {
			status = http.StatusFound
			header.Set("Location", "https://web.ifzq.gtimg.cn/result")
		} else if r.URL.Hostname() == "web.ifzq.gtimg.cn" {
			status = http.StatusFound
			header.Set("Location", "https://vendor-cdn.example/result")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
	c := &http.Client{Transport: &vendorScheduledTransport{base: base, scheduler: s}}
	request, err := http.NewRequestWithContext(context.WithValue(context.Background(), vendorOriginKey{}, "tencent"), http.MethodGet, "https://qt.gtimg.cn/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if n := s.snapshot()["tencent"].Requests; n != 3 {
		t.Fatalf("redirect was not charged: %d", n)
	}
	if len(hosts) != 3 {
		t.Fatal(hosts)
	}
}

func TestVendorSchedulerBoundsQueueAndNeverDispatchesCancelledWork(t *testing.T) {
	s := newVendorScheduler()
	s.mu.Lock()
	state := s.sourceLocked("sina")
	for i := 0; i < vendorQueueLimit; i++ {
		state.queue = append(state.queue, &vendorWaiter{priority: vendorForeground})
	}
	s.mu.Unlock()
	if _, err := s.acquire(context.Background(), "sina", vendorForeground); !isVendorDeferred(err) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.acquire(ctx, "tencent", vendorBackground); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.snapshot()["tencent"].Requests != 0 {
		t.Fatal("cancelled work reached the network")
	}
}

func TestQuoteConcurrencyGateKeepsLiveSlotWhenHistoryAndEvaluationAreBusy(t *testing.T) {
	c := &quoteCache{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, priority := range []vendorPriority{vendorHistory, vendorHistory, vendorBackground} {
		release, err := c.acquire(withVendorPriority(ctx, priority))
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	extra := make(chan func(), 1)
	go func() {
		release, err := c.acquire(withVendorPriority(ctx, vendorHistory))
		if err == nil {
			extra <- release
		}
		close(extra)
	}()
	select {
	case release := <-extra:
		if release != nil {
			release()
		}
		t.Fatal("history consumed the last live slot")
	case <-time.After(10 * time.Millisecond):
	}
	liveCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	release, err := c.acquire(liveCtx)
	if err != nil {
		t.Fatalf("live query blocked behind history: %v", err)
	}
	release()
	cancel()
	if release := <-extra; release != nil {
		release()
		t.Fatal("cancelled history was admitted")
	}
}
