package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuoteStaleFallbackPreservesStampAndDoesNotRenewGrace(t *testing.T) {
	clock := time.Now()
	fail := false
	c := &quoteCache{now: func() time.Time { return clock }, sources: []quoteSource{{name: "tencent", fetch: func(context.Context, string, string, int) (*QuoteResp, error) {
		if fail {
			return nil, errors.New("source unavailable")
		}
		return &QuoteResp{Symbol: "600081", Source: "tencent", BarsSource: "tencent", Snapshot: QuoteSnapshot{Last: 1013, AsOf: "2026-10-23T16:14:56+08:00"}}, nil
	}}}}
	cfg := quoteConfigDefault()
	first, _, _, err := c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
	if err != nil {
		t.Fatal(err)
	}
	fail = true
	clock = clock.Add(quoteTTLClosed + time.Minute)
	for i := 0; i < 2; i++ {
		resp, cached, ttl, err := c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
		if err != nil || !cached || ttl != 0 || !resp.Stale || resp.Snapshot.AsOf != first.Snapshot.AsOf {
			t.Fatalf("stale response lost its provenance: %+v cached=%v ttl=%v err=%v", resp, cached, ttl, err)
		}
		if first.Stale {
			t.Fatal("shared successful response was mutated")
		}
	}
	clock = clock.Add(quoteStaleGrace)
	if _, _, _, err := c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalDaily, quoteWindow{}); err == nil {
		t.Fatal("failed refresh renewed stale grace indefinitely")
	}
}

func TestStaleDisplayEvidenceIsNeverAnEvaluatorInput(t *testing.T) {
	c := &quoteCache{}
	c.put(quoteCacheKey("sh", "600081", 66, quoteIntervalDaily, quoteWindow{}), &QuoteResp{Stale: true, Snapshot: QuoteSnapshot{Last: 1013}}, time.Minute)
	if _, _, _, err := c.fetchUnder(context.Background(), quoteConfigDefault(), "sh", "600081", 66, quoteIntervalSettled, quoteWindow{}); !errors.Is(err, errQuoteLedgerInterval) {
		t.Fatalf("ledger consumed display evidence: %v", err)
	}
}

func TestQuoteStaleGraceIsCheckedAfterTheFailedRefresh(t *testing.T) {
	clock := time.Now()
	c := &quoteCache{now: func() time.Time { return clock }}
	c.sources = []quoteSource{{name: "tencent", fetch: func(context.Context, string, string, int) (*QuoteResp, error) {
		clock = clock.Add(quoteStaleGrace)
		return nil, errors.New("source unavailable")
	}}}
	c.put(quoteCacheKey("sh", "600081", 66, quoteIntervalDaily, quoteWindow{}), &QuoteResp{Snapshot: QuoteSnapshot{Last: 1013}}, -time.Minute)
	if _, _, _, err := c.fetchUnder(context.Background(), quoteConfigDefault(), "sh", "600081", 66, quoteIntervalDaily, quoteWindow{}); err == nil {
		t.Fatal("response escaped its stale grace during upstream work")
	}
}
