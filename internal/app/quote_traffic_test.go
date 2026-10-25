package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestQuoteTrafficSeparatesLiveHistoryAndStaleOutcomes(t *testing.T) {
	clock := time.Now()
	fail := false
	c := &quoteCache{now: func() time.Time { return clock }}
	c.sources = []quoteSource{{name: "tencent", fetch: func(context.Context, string, string, int) (*QuoteResp, error) {
		if fail {
			return nil, errors.New("offline")
		}
		return &QuoteResp{Snapshot: QuoteSnapshot{Last: 1013}}, nil
	}}, {name: "sohu", historyOnly: true, fetch: func(context.Context, string, string, int) (*QuoteResp, error) {
		return &QuoteResp{HistoryOnly: true}, nil
	}}}
	cfg := quoteConfigDefault()
	read := func() {
		t.Helper()
		_, _, _, _ = c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
	}
	read()
	read()
	fail = true
	clock = clock.Add(quoteTTLClosed + time.Minute)
	read()
	clock = clock.Add(quoteStaleGrace)
	read()
	cfg.Order = []string{"sohu"}
	cfg.HistoryOnly = true
	read()
	_, _, _, _ = c.fetchUnder(context.Background(), cfg, "sh", "600081", 66, quoteIntervalSettled, quoteWindow{})
	stats := c.trafficSnapshot()
	if stats.Live.Requests != 4 || stats.Live.Completed != 4 || stats.Live.CacheHits != 1 || stats.Live.Loads != 3 || stats.Live.Stale != 1 || stats.Live.Failures != 1 || stats.History.Requests != 1 || stats.History.Loads != 1 {
		t.Fatalf("display metrics mixed outcomes: %+v", stats)
	}
	c.clear()
	if after := c.trafficSnapshot(); after.Live.Requests != 4 || after.History.Requests != 1 {
		t.Fatal("purging display data erased process evidence")
	}
}

func TestQuoteTrafficCountsFlightJoinsWithoutCallingThemCacheHits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &quoteCache{}
	c.init()
	key := quoteCacheKey("sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
	c.flights[key] = &quoteFlight{done: make(chan struct{})}
	cancel()
	_, _, _, err := c.fetchUnder(ctx, quoteConfigDefault(), "sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stats := c.trafficSnapshot().Live
	if stats.Requests != 1 || stats.Completed != 1 || stats.Merged != 1 || stats.CacheHits != 0 || stats.Loads != 0 || stats.Failures != 1 {
		t.Fatalf("join accounting: %+v", stats)
	}
}

func TestQuoteBatchTrafficUsesUniqueSymbolsAsCacheDenominator(t *testing.T) {
	c := &quoteCache{sources: []quoteSource{{name: "tencent"}}}
	a, _ := quoteResolve("600081")
	b, _ := quoteResolve("600519")
	c.put(quoteCacheKey("sh", "600081", 0, quoteIntervalSnapshot, quoteWindow{}), &QuoteResp{Symbol: "600081"}, time.Minute)
	result := c.fetchBatchUnder(context.Background(), quoteConfigDefault(), []quoteTarget{a, a, b})
	if len(result) != 1 {
		t.Fatal(result)
	}
	stats := c.trafficSnapshot().Batch
	if stats.Requests != 1 || stats.Symbols != 2 || stats.CachedSymbols != 1 || stats.MissingSymbols != 1 || stats.Loads != 1 {
		t.Fatalf("batch denominator counted duplicates or requests: %+v", stats)
	}
}

func TestQuoteLatencyHistogramIsBoundedAndReportsAnUpperBound(t *testing.T) {
	var h quoteLatencyHistogram
	if h.p95Millis() != 0 {
		t.Fatal("empty percentile invented data")
	}
	for range 19 {
		h.observe(50 * time.Millisecond)
	}
	h.observe(2 * time.Second)
	if h.p95Millis() != 50 {
		t.Fatal(h.p95Millis())
	}
	h.observe(90 * time.Second)
	if h.p95Millis() != 2500 {
		t.Fatal(h.p95Millis())
	}
}

func TestQuoteAdminExposesDisplayTrafficWithoutSerializingHistogramState(t *testing.T) {
	s := quoteAdminServer(t)
	s.quotes.put(quoteCacheKey("sh", "600081", 66, quoteIntervalDaily, quoteWindow{}), &QuoteResp{Symbol: "600081"}, time.Minute)
	_, _, _, err := s.quotes.fetchUnder(context.Background(), quoteConfigDefault(), "sh", "600081", 66, quoteIntervalDaily, quoteWindow{})
	if err != nil {
		t.Fatal(err)
	}
	rec := quoteAdminDo(t, s, http.MethodGet, "/api/admin/quote", "root", "")
	var body struct {
		DisplayTraffic struct {
			Live map[string]any `json:"live"`
		} `json:"displayTraffic"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.DisplayTraffic.Live["cacheHits"] != float64(1) || body.DisplayTraffic.Live["requests"] != float64(1) || len(body.DisplayTraffic.Live) != 8 {
		t.Fatalf("unexpected traffic wire shape: %d %s", rec.Code, rec.Body.String())
	}
}
