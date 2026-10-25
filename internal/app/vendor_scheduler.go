package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type vendorPriority int

const (
	vendorForeground vendorPriority = iota
	vendorHistory
	vendorBackground
	vendorShadow
	vendorQueueLimit = 64
	vendorQueueWait  = 5 * time.Second
)

type vendorWorkKey struct{}
type vendorOriginKey struct{}

func withVendorPriority(ctx context.Context, priority vendorPriority) context.Context {
	return context.WithValue(ctx, vendorWorkKey{}, priority)
}

func vendorWorkPriority(ctx context.Context) vendorPriority {
	priority, _ := ctx.Value(vendorWorkKey{}).(vendorPriority)
	return priority
}

// Limits belong to the portal, not to a vendor's unpublished service contract. Every redirect
// and adjusted-price check spends the same source budget as an ordinary display request.
type vendorRatePolicy struct {
	RequestsPerMinute   int `json:"requestsPerMinute"`
	BackgroundPerMinute int `json:"backgroundPerMinute"`
}

var defaultVendorRatePolicy = vendorRatePolicy{RequestsPerMinute: 30, BackgroundPerMinute: 6}

type vendorDeferredError struct {
	Source  string
	Reason  string
	RetryAt time.Time
}

func (e *vendorDeferredError) Error() string {
	return fmt.Sprintf("vendor %s deferred: %s", e.Source, e.Reason)
}

func isVendorDeferred(err error) bool {
	var deferred *vendorDeferredError
	return errors.As(err, &deferred)
}

type vendorWaiter struct {
	priority vendorPriority
	ctx      context.Context
	at       time.Time
}

type vendorScheduleState struct {
	queue                                                        []*vendorWaiter
	next, nextBackground, coolingUntil                           time.Time
	inFlight, backgroundInFlight, cooldownStreak                 int
	probing                                                      bool
	requests, failures, deferred                                 uint64
	completed, forbidden, throttled, serverErrors, networkErrors uint64
	latency                                                      quoteLatencyHistogram
	requestTimes                                                 []time.Time
	totalWait, totalDuration                                     time.Duration
}

type vendorScheduleStatus struct {
	Completed          uint64 `json:"completed"`
	Forbidden          uint64 `json:"forbidden"`
	Throttled          uint64 `json:"throttled"`
	ServerErrors       uint64 `json:"serverErrors"`
	NetworkErrors      uint64 `json:"networkErrors"`
	P95Millis          int64  `json:"p95Millis"`
	Requests           uint64 `json:"requests"`
	Failures           uint64 `json:"failures"`
	Deferred           uint64 `json:"deferred"`
	RequestsLastMinute int    `json:"requestsLastMinute"`
	Queued             int    `json:"queued"`
	InFlight           int    `json:"inFlight"`
	OldestWaitMillis   int64  `json:"oldestWaitMillis"`
	WaitMillis         int64  `json:"waitMillis"`
	DurationMillis     int64  `json:"durationMillis"`
	CoolingUntil       string `json:"coolingUntil"`
	Probing            bool   `json:"probing"`
}

// A waiting background job is represented by its existing work record, not a goroutine holding
// a display slot. It receives a typed deferral and resumes after the next admission opportunity.
type vendorScheduler struct {
	mu        sync.Mutex
	sources   map[string]*vendorScheduleState
	changed   chan struct{}
	policy    vendorRatePolicy
	overrides map[string]vendorRatePolicy
	now       func() time.Time
}

func newVendorScheduler() *vendorScheduler {
	return &vendorScheduler{sources: map[string]*vendorScheduleState{}, changed: make(chan struct{}),
		policy: defaultVendorRatePolicy, now: time.Now}
}

var publicVendorScheduler = newVendorScheduler()

func (s *vendorScheduler) sourceLocked(source string) *vendorScheduleState {
	state := s.sources[source]
	if state == nil {
		state = &vendorScheduleState{}
		s.sources[source] = state
	}
	return state
}

func (s *vendorScheduler) notifyLocked() { close(s.changed); s.changed = make(chan struct{}) }

func (s *vendorScheduler) acquire(ctx context.Context, source string, priority vendorPriority) (func(int, string, error), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	waiter := &vendorWaiter{priority: priority, ctx: ctx, at: s.now()}
	state := s.sourceLocked(source)
	if priority < vendorBackground {
		if len(state.queue) >= vendorQueueLimit {
			state.deferred++
			s.mu.Unlock()
			return nil, &vendorDeferredError{Source: source, Reason: "queue_full"}
		}
		state.queue = append(state.queue, waiter)
		s.notifyLocked()
	}
	remove := func() {
		for i, queued := range state.queue {
			if queued == waiter {
				state.queue = append(state.queue[:i], state.queue[i+1:]...)
				s.notifyLocked()
				break
			}
		}
	}
	defer func() { remove(); s.mu.Unlock() }()
	deadline := time.NewTimer(vendorQueueWait)
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// A queue/budget wake-up can precede expiry but lose the mutex until after it. Selecting
		// the deadline only while asleep would then admit an already expired reader.
		select {
		case <-deadline.C:
			state.deferred++
			return nil, &vendorDeferredError{Source: source, Reason: "queue_timeout"}
		default:
		}
		now := s.now()
		if now.Before(state.coolingUntil) || state.probing {
			state.deferred++
			return nil, &vendorDeferredError{Source: source, Reason: "cooldown", RetryAt: state.coolingUntil}
		}
		first := (*vendorWaiter)(nil)
		for _, queued := range state.queue {
			if queued.ctx != nil && queued.ctx.Err() != nil {
				continue
			}
			if first == nil || queued.priority < first.priority {
				first = queued
			}
		}
		ready := state.next
		background := priority >= vendorBackground
		if background && state.nextBackground.After(ready) {
			ready = state.nextBackground
		}
		turn := first == waiter || (first == nil && background)
		capacity := state.inFlight < quoteUpstreamConcurrency && (!background || state.backgroundInFlight == 0)
		if turn && capacity && !now.Before(ready) {
			remove()
			policy := s.policyLocked(source)
			state.next = now.Add(time.Minute / time.Duration(policy.RequestsPerMinute))
			if background {
				state.nextBackground = now.Add(time.Minute / time.Duration(policy.BackgroundPerMinute))
				state.backgroundInFlight++
			}
			state.inFlight++
			state.probing = state.cooldownStreak > 0
			probe := state.probing
			state.requests++
			state.requestTimes = append(state.requestTimes, now)
			s.trimTimesLocked(state, now)
			state.totalWait += now.Sub(waiter.at)
			var once sync.Once
			return func(status int, retryAfter string, err error) {
				once.Do(func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					state.inFlight--
					if background {
						state.backgroundInFlight--
					}
					finished := s.now()
					state.totalDuration += finished.Sub(now)
					state.completed++
					state.latency.observe(finished.Sub(now))
					switch {
					case err != nil:
						state.networkErrors++
					case status == http.StatusForbidden:
						state.forbidden++
					case status == http.StatusTooManyRequests:
						state.throttled++
					case status >= 500:
						state.serverErrors++
					}
					if err != nil || status >= 400 {
						state.failures++
					}
					if status == http.StatusForbidden || status == http.StatusTooManyRequests || status >= 500 {
						state.cooldownStreak++
						i := state.cooldownStreak - 1
						if i >= len(ledgerCooldowns) {
							i = len(ledgerCooldowns) - 1
						}
						until := finished.Add(ledgerCooldowns[i])
						if requested := vendorRetryAfter(retryAfter, finished); requested.After(until) {
							until = requested
						}
						if until.After(state.coolingUntil) {
							state.coolingUntil = until
						}
					} else if probe && err == nil && status >= 200 && status < 400 {
						state.cooldownStreak, state.coolingUntil = 0, time.Time{}
					}
					if probe {
						state.probing = false
					}
					s.notifyLocked()
				})
			}, nil
		}
		if background {
			state.deferred++
			return nil, &vendorDeferredError{Source: source, Reason: "rate_limit", RetryAt: ready}
		}
		changed := s.changed
		delay := ready.Sub(now)
		if delay <= 0 {
			delay = vendorQueueWait
		}
		timer := time.NewTimer(delay)
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		case <-timer.C:
		case <-deadline.C:
			timer.Stop()
			s.mu.Lock()
			state.deferred++
			return nil, &vendorDeferredError{Source: source, Reason: "queue_timeout"}
		}
		timer.Stop()
		s.mu.Lock()
	}
}

func vendorRetryAfter(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds > 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if until, err := http.ParseTime(value); err == nil && until.After(now) {
		return until
	}
	return time.Time{}
}

func (s *vendorScheduler) trimTimesLocked(state *vendorScheduleState, now time.Time) {
	i := 0
	for i < len(state.requestTimes) && !state.requestTimes[i].After(now.Add(-time.Minute)) {
		i++
	}
	state.requestTimes = append([]time.Time(nil), state.requestTimes[i:]...)
}

func (s *vendorScheduler) snapshot() map[string]vendorScheduleStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]vendorScheduleStatus{}
	now := s.now()
	for source, state := range s.sources {
		s.trimTimesLocked(state, now)
		status := vendorScheduleStatus{Requests: state.requests, Failures: state.failures, Deferred: state.deferred,
			Completed: state.completed, Forbidden: state.forbidden, Throttled: state.throttled, ServerErrors: state.serverErrors, NetworkErrors: state.networkErrors, P95Millis: state.latency.p95Millis(),
			RequestsLastMinute: len(state.requestTimes), Queued: len(state.queue), InFlight: state.inFlight,
			WaitMillis: state.totalWait.Milliseconds(), DurationMillis: state.totalDuration.Milliseconds(), Probing: state.probing}
		for _, waiter := range state.queue {
			if age := now.Sub(waiter.at).Milliseconds(); age > status.OldestWaitMillis {
				status.OldestWaitMillis = age
			}
		}
		if state.coolingUntil.After(now) {
			status.CoolingUntil = quoteAdminTime(state.coolingUntil)
		}
		out[source] = status
	}
	return out
}

func vendorSourceForHost(host string) string {
	switch strings.ToLower(host) {
	case "qt.gtimg.cn", "web.ifzq.gtimg.cn":
		return "tencent"
	case "hq.sinajs.cn", "money.finance.sina.com.cn", "vip.stock.finance.sina.com.cn":
		return "sina"
	case "push2.eastmoney.com", "push2his.eastmoney.com", "push2delay.eastmoney.com", "82.push2.eastmoney.com":
		return "eastmoney"
	case "query1.finance.yahoo.com", "query2.finance.yahoo.com":
		return "yahoo"
	case "q.stock.sohu.com":
		return "sohu"
	}
	return ""
}

type vendorScheduledTransport struct {
	base      http.RoundTripper
	scheduler *vendorScheduler
}

func (t *vendorScheduledTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	source := vendorSourceForHost(request.URL.Hostname())
	if source == "" {
		source, _ = request.Context().Value(vendorOriginKey{}).(string)
	}
	if source == "" {
		return t.base.RoundTrip(request)
	}
	finish, err := t.scheduler.acquire(request.Context(), source, vendorWorkPriority(request.Context()))
	if err != nil {
		return nil, err
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		finish(0, "", err)
		return response, err
	}
	if response.Body == nil {
		finish(response.StatusCode, response.Header.Get("Retry-After"), nil)
	} else {
		response.Body = &vendorScheduledBody{ReadCloser: response.Body, finish: func() { finish(response.StatusCode, response.Header.Get("Retry-After"), nil) }}
	}
	return response, nil
}

type vendorScheduledBody struct {
	io.ReadCloser
	finish func()
}

func (b *vendorScheduledBody) Close() error { err := b.ReadCloser.Close(); b.finish(); return err }
