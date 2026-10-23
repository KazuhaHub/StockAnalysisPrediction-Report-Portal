package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
)

func TestShadowHTTPBudgetIncludesRedirects(t *testing.T) {
	var calls atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer stub.Close()
	ctx, budget := withQuoteShadowHTTPBudget(context.Background())
	_, err := vendorGet(ctx, stub.URL, "", 1024)
	var refusal *ledgerRefusal
	if !errors.As(err, &refusal) || refusal.Code != "shadow_budget" || calls.Load() != 4 || budget.attempts() != 4 {
		t.Fatalf("redirects escaped HTTP budget: calls=%d attempts=%d err=%v", calls.Load(), budget.attempts(), err)
	}
}

func TestShadowHTTPBudgetSharedAcrossSinaReads(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	ctx, budget := withQuoteShadowHTTPBudget(ctx)
	for i := 0; i < forecastShadowMaxComparisons; i++ {
		if _, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 4 || budget.attempts() != 4 {
		t.Fatalf("two complete reads did not count four HTTP requests: %d %d", *calls, budget.attempts())
	}
	if _, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); err == nil || *calls != 4 || budget.attempts() != 4 {
		t.Fatal("extra read escaped shared budget")
	}
}

func TestShadowHTTPBudgetIsolatedAndConcurrent(t *testing.T) {
	previous := vendorClient
	var calls atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer stub.Close()
	for range 2 {
		ctx, budget := withQuoteShadowHTTPBudget(context.Background())
		var group sync.WaitGroup
		for range 20 {
			group.Go(func() { _, _ = vendorGet(ctx, stub.URL, "", 1024) })
		}
		group.Wait()
		if budget.attempts() != 4 {
			t.Fatal("concurrent calls overran or lost budget")
		}
	}
	if calls.Load() != 8 || vendorClient != previous || vendorHTTPClient(context.Background()) != previous {
		t.Fatal("budget leaked into another pass or global client")
	}
}

func TestShadowHTTPBudgetCancelledCallIsNotAttempted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, budget := withQuoteShadowHTTPBudget(ctx)
	if _, err := vendorGet(ctx, "https://unused.invalid/", "", 1024); !errors.Is(err, context.Canceled) || budget.attempts() != 0 {
		t.Fatalf("cancelled request consumed network budget: %v %d", err, budget.attempts())
	}
}
