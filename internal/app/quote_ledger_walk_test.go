package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func ledgerWalkSource(name string, fetch quoteRangeFetchFunc) quoteSource {
	return quoteSource{name: name, cashDividends: true, reportsKind: true, fetchRange: fetch,
		caps: []quoteCapability{{intervals: []quoteInterval{quoteIntervalSettled}}}}
}

func TestLedgerWalkRefusesBeforeCallingSources(t *testing.T) {
	calls := 0
	src := ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return &QuoteResp{}, nil
	})
	c := &quoteCache{sources: []quoteSource{src}}
	cfg := quoteConfig{Order: []string{"primary"}}
	for _, bounds := range [][2]string{{"", ""}, {"2026-10-01", ""}, {"bad", "2026-10-22"}, {"2026-10-22", "2026-10-01"}} {
		_, err := c.walkLedger(context.Background(), cfg, "sh", "601899", bounds[0], bounds[1], &ledgerTick{})
		var refusal *ledgerRefusal
		if !errors.As(err, &refusal) || refusal.Code != "bad_window" {
			t.Fatalf("invalid bounds %v: %v", bounds, err)
		}
	}
	c.sources[0].cashDividends = false
	_, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{})
	var refusal *ledgerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "no_source" || calls != 0 {
		t.Fatalf("no primary called a source: error=%v calls=%d", err, calls)
	}
}

func TestLedgerWalkBreakerAndHealthAreSeparate(t *testing.T) {
	calls := 0
	failure := errors.New("transport failure")
	c := &quoteCache{sources: []quoteSource{ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return nil, failure
	})}}
	cfg, tick := quoteConfig{Order: []string{"primary"}}, &ledgerTick{}
	for i := 0; i < 5; i++ {
		if _, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", tick); !errors.Is(err, failure) {
			t.Fatalf("failed or blocked source lost its actual failure: %v", err)
		}
	}
	if calls != 3 || len(c.snapshotHealth()) != 0 {
		t.Fatalf("breaker or health isolation: calls=%d display=%+v", calls, c.snapshotHealth())
	}
	health := c.snapshotLedgerHealth()
	if len(health) != 1 || health[0].Failures != 3 {
		t.Fatalf("ledger health=%+v", health)
	}
	if _, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{}); err == nil || calls != 4 {
		t.Fatal("new tick did not reopen the breaker")
	}
}

func TestLedgerWalkTypedRefusalsAndCancellationDoNotBlameVendor(t *testing.T) {
	for _, outcome := range []error{&ledgerRefusal{Code: "not_index"}, fmt.Errorf("wrapped: %w", &ledgerRefusal{Code: "not_index"}), context.Canceled} {
		calls := 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := &quoteCache{sources: []quoteSource{ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
			calls++
			if errors.Is(outcome, context.Canceled) {
				cancel()
			}
			return nil, outcome
		})}}
		tick := &ledgerTick{}
		for i := 0; i < 4; i++ {
			_, _ = c.walkLedger(ctx, quoteConfig{Order: []string{"primary"}}, "sh", "601899", "2026-10-01", "2026-10-22", tick)
		}
		if len(c.snapshotLedgerHealth()) != 0 || len(c.snapshotHealth()) != 0 {
			t.Fatal("typed refusal or caller cancellation changed vendor health")
		}
		if errors.Is(outcome, context.Canceled) && calls != 1 {
			t.Fatal("cancelled caller reached vendor again")
		}
		if !errors.Is(outcome, context.Canceled) && calls != 4 {
			t.Fatal("typed refusals incorrectly opened the circuit breaker")
		}
	}
}

func TestLedgerWalkCancellationWhileWaitingForSlot(t *testing.T) {
	c := &quoteCache{sources: []quoteSource{ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		t.Fatal("cancelled slot waiter called the vendor")
		return nil, nil
	})}}
	c.init()
	for i := 0; i < quoteUpstreamConcurrency; i++ {
		c.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < quoteUpstreamConcurrency; i++ {
			<-c.sem
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.walkLedger(ctx, quoteConfig{Order: []string{"primary"}}, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{})
	if !errors.Is(err, context.DeadlineExceeded) || len(c.snapshotLedgerHealth()) != 0 || len(c.sem) != quoteUpstreamConcurrency {
		t.Fatalf("slot cancellation changed health or occupancy: %v", err)
	}
}

func TestLedgerWalkCancellationBehindPassGateKeepsPassUsable(t *testing.T) {
	tick := &ledgerTick{}
	release, err := tick.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	calls := 0
	c := &quoteCache{sources: []quoteSource{ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return &QuoteResp{}, nil
	})}}
	cfg := quoteConfig{Order: []string{"primary"}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = c.walkLedger(ctx, cfg, "sh", "601899", "2026-10-01", "2026-10-22", tick)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 || len(c.snapshotLedgerHealth()) != 0 || len(c.sem) != 0 {
		t.Fatalf("queued cancellation reached upstream or health: calls=%d error=%v", calls, err)
	}
	release()
	held = false
	retry, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	resp, err := c.walkLedger(retry, cfg, "sh", "601899", "2026-10-01", "2026-10-22", tick)
	if err != nil || resp == nil || calls != 1 {
		t.Fatalf("cancelled waiter damaged pass progress: calls=%d error=%v", calls, err)
	}
}

func TestLedgerWalkConcurrentPassHasOnlyThreeFailures(t *testing.T) {
	var calls atomic.Int32
	failure := errors.New("transport failure")
	c := &quoteCache{sources: []quoteSource{ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls.Add(1)
		return nil, failure
	})}}
	tick := &ledgerTick{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.walkLedger(context.Background(), quoteConfig{Order: []string{"primary"}}, "sh", "601899", "2026-10-01", "2026-10-22", tick)
			if !errors.Is(err, failure) {
				t.Errorf("concurrent walk lost failure: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want exactly three", calls.Load())
	}
}

func TestLedgerWalkPreservesFirstRefusalAndNeverCaches(t *testing.T) {
	first := &ledgerRefusal{Code: "not_index"}
	calls := 0
	c := &quoteCache{sources: []quoteSource{
		ledgerWalkSource("first", func(context.Context, string, string, string, string) (*QuoteResp, error) { return nil, first }),
		ledgerWalkSource("second", func(context.Context, string, string, string, string) (*QuoteResp, error) {
			return nil, errors.New("second failed")
		}),
	}}
	cfg := quoteConfig{Order: []string{"first", "second"}}
	_, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{})
	if !errors.Is(err, first) {
		t.Fatalf("lower priority failure replaced first outcome: %v", err)
	}
	c.sources[0].fetchRange = func(_ context.Context, market, code, from, to string) (*QuoteResp, error) {
		calls++
		if market != "sh" || code != "601899" || from != "2026-10-01" || to != "2026-10-22" {
			t.Fatal("validated window or identity changed")
		}
		return &QuoteResp{Source: "first"}, nil
	}
	for i := 0; i < 2; i++ {
		resp, err := c.walkLedger(context.Background(), cfg, "sh", "601899", " 2026-10-01 ", "2026-10-22", &ledgerTick{})
		if err != nil || resp == nil || resp.Source != "first" {
			t.Fatalf("success=%v, %v", resp, err)
		}
	}
	if calls != 2 || c.stats().Entries != 0 || len(c.flights) != 0 || len(c.snapshotHealth()) != 0 {
		t.Fatal("ledger request reused display cache/flight or contaminated display health")
	}
}

func TestLedgerWalkHoldsOneSlotAcrossTheChain(t *testing.T) {
	c := &quoteCache{}
	c.init()
	for i := 0; i < quoteUpstreamConcurrency-1; i++ {
		c.sem <- struct{}{}
	}
	defer func() {
		for i := 0; i < quoteUpstreamConcurrency-1; i++ {
			<-c.sem
		}
	}()
	called := 0
	c.sources = nil
	for _, name := range []string{"first", "second"} {
		c.sources = append(c.sources, ledgerWalkSource(name, func(context.Context, string, string, string, string) (*QuoteResp, error) {
			called++
			if len(c.sem) != quoteUpstreamConcurrency {
				t.Fatal("walk released its upstream slot between calls")
			}
			if called == 1 {
				return nil, &ledgerRefusal{Code: "not_index"}
			}
			return &QuoteResp{}, nil
		}))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.walkLedger(ctx, quoteConfig{Order: []string{"first", "second"}}, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{}); err != nil || called != 2 {
		t.Fatalf("walk acquired an extra slot or lost fallback: calls=%d error=%v", called, err)
	}
	if len(c.sem) != quoteUpstreamConcurrency-1 {
		t.Fatal("walk leaked its upstream slot")
	}
}

func TestLedgerWalkRejectsNilResponseAndDoesNotAdmitEvidenceFreeFallback(t *testing.T) {
	calls := 0
	c := &quoteCache{sources: []quoteSource{
		ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) { return nil, nil }),
		ledgerWalkSource("fallback", func(context.Context, string, string, string, string) (*QuoteResp, error) {
			calls++
			return &QuoteResp{}, nil
		}),
	}}
	c.sources[1].cashDividends = false
	resp, err := c.walkLedger(context.Background(), quoteConfig{Order: []string{"primary", "fallback"}}, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{})
	if resp != nil || err == nil || calls != 0 {
		t.Fatalf("nil response or evidence-free writer accepted: response=%v error=%v calls=%d", resp, err, calls)
	}
	if health := c.snapshotLedgerHealth(); len(health) != 1 || health[0].Source != "primary" || health[0].Failures != 1 {
		t.Fatalf("nil response did not charge only the called source: %+v", health)
	}
}
