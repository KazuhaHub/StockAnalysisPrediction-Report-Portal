package app

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestSinaShadowResumesHistoryInsteadOfRefetchingSnapshot(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	client := *vendorHTTPClient(ctx)
	scheduler := newVendorScheduler()
	clock := time.Now()
	scheduler.now = func() time.Time { return clock }
	client.Transport = &vendorScheduledTransport{base: client.Transport, scheduler: scheduler}
	ctx = context.WithValue(withVendorPriority(ctx, vendorShadow), quoteProbeClientKey{}, &client)
	var progress sinaRangeProgress
	first, budget := withQuoteShadowHTTPBudget(withSinaRangeProgress(ctx, &progress, "scope", clock))
	if _, err := fetchSinaRange(first, "sh", "601899", "2026-06-15", "2026-06-18"); !isVendorDeferred(err) {
		t.Fatal(err)
	}
	if *calls != 1 || budget.attempts() != 1 {
		t.Fatalf("first real snapshot was not counted: %d %d", *calls, budget.attempts())
	}
	clock = clock.Add(10 * time.Second)
	second, budget := withQuoteShadowHTTPBudget(withSinaRangeProgress(ctx, &progress, "scope", clock))
	response, err := fetchSinaRange(second, "sh", "601899", "2026-06-15", "2026-06-18")
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || budget.attempts() != 1 || len(response.Bars) != 4 || response.Snapshot.AsOf != "2026-09-04T15:34:59+08:00" || response.Cached {
		t.Fatalf("continuation fetched a new snapshot or lost provenance: calls=%d attempts=%d %+v", *calls, budget.attempts(), response)
	}
	if len(progress.entries) != 0 {
		t.Fatal("completed read retained partial evidence")
	}
}

func TestSinaPartialSnapshotsAreScopedBoundedAndDoNotRenewExpiry(t *testing.T) {
	var progress sinaRangeProgress
	now := time.Now()
	ctx := withSinaRangeProgress(context.Background(), &progress, "scope-A", now)
	read := sinaContinuation(ctx, "sh|601899|from|to")
	snapshot := &QuoteResp{Snapshot: QuoteSnapshot{Last: 3335, AsOf: "2026-09-04T15:34:59+08:00"}}
	read.keep(snapshot)
	copy := read.get()
	copy.Snapshot.Last = 1
	if read.get().Snapshot.Last != 3335 {
		t.Fatal("shared snapshot was mutable")
	}
	later := sinaContinuation(withSinaRangeProgress(context.Background(), &progress, "scope-A", now.Add(5*time.Hour)), read.key)
	later.keep(snapshot)
	expired := sinaContinuation(withSinaRangeProgress(context.Background(), &progress, "scope-A", now.Add(6*time.Hour)), read.key)
	if expired.get() != nil {
		t.Fatal("admission deferral renewed partial evidence")
	}
	read.keep(snapshot)
	other := sinaContinuation(withSinaRangeProgress(context.Background(), &progress, "scope-B", now), read.key)
	if other.get() != nil {
		t.Fatal("source/session/mode scope leaked")
	}
	for i := 0; i < 100; i++ {
		key := time.Unix(int64(i), 0).String()
		sinaContinuation(withSinaRangeProgress(context.Background(), &progress, "bounded", now.Add(time.Duration(i)*time.Second)), key).keep(snapshot)
	}
	if len(progress.entries) > sinaPartialLimit {
		t.Fatal("unbounded partial snapshots")
	}
}

func TestSinaContinuationPreservesSnapshotAtTheShadowAttemptCap(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	var progress sinaRangeProgress
	now := time.Now()
	ctx = withSinaRangeProgress(ctx, &progress, "scope", now)
	first, budget := withQuoteShadowHTTPBudget(ctx)
	budget.used = forecastShadowMaxHTTPAttempts - 1
	if _, err := fetchSinaRange(first, "sh", "601899", "2026-06-15", "2026-06-18"); err == nil {
		t.Fatal("history escaped the hard attempt cap")
	}
	if *calls != 1 || budget.attempts() != 4 {
		t.Fatalf("incorrect capped accounting: %d %d", *calls, budget.attempts())
	}
	second, budget := withQuoteShadowHTTPBudget(ctx)
	if _, err := fetchSinaRange(second, "sh", "601899", "2026-06-15", "2026-06-18"); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || budget.attempts() != 1 {
		t.Fatalf("completed snapshot was fetched again: %d %d", *calls, budget.attempts())
	}
}

func TestSinaContinuationStillRequiresNewestHistoryCoherence(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	var progress sinaRangeProgress
	ctx = withSinaRangeProgress(ctx, &progress, "scope", time.Now())
	snapshot, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap)))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Snapshot.Last++
	read := sinaContinuation(ctx, forecastReadKey("sh", "601899", "2026-06-15", "2026-06-18"))
	read.keep(snapshot)
	if _, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); err == nil {
		t.Fatal("resumed snapshot bypassed full-tail coherence")
	} else {
		var refusal *ledgerRefusal
		if !errors.As(err, &refusal) || refusal.Code != "incomplete" {
			t.Fatalf("changed observation became a delivery fault: %v", err)
		}
	}
	if *calls != 1 || len(progress.entries) != 0 {
		t.Fatal("incoherent partial read was retained")
	}
	if _, err := fetchSinaRange(ctx, "sh", "601899", "2026-06-15", "2026-06-18"); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Fatalf("incoherent snapshot was not refreshed: %d", *calls)
	}
}

func TestSinaDisplayDoesNotConsumePartialComparisonSnapshots(t *testing.T) {
	ctx, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
	var progress sinaRangeProgress
	ctx = withSinaRangeProgress(ctx, &progress, "scope", time.Now())
	snapshot, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap)))
	if err != nil {
		t.Fatal(err)
	}
	sinaContinuation(ctx, forecastReadKey("sh", "601899", "2026-06-15", "2026-06-18")).keep(snapshot)
	if _, err := fetchSinaQuote(ctx, "sh", "601899", quoteMaxBars); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 || len(progress.entries) != 1 {
		t.Fatalf("display borrowed partial comparison evidence: calls=%d entries=%d", *calls, len(progress.entries))
	}
}
