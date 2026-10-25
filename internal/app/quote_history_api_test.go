package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestQuoteHistoryEndpointIsOptInAndIsolated(t *testing.T) {
	s := quoteServer(t)
	s.quotes.init()
	fixture := sohuFixture(t, "sh600081")
	source := quoteSohuSource()
	calls := 0
	source.fetch = func(context.Context, string, string, int) (*QuoteResp, error) {
		calls++
		return parseSohuHistory(fixture, "sh", "600081", "2026-10-22", "2026-10-23", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	}
	s.quotes.sources = []quoteSource{{name: "tencent", fetch: func(context.Context, string, string, int) (*QuoteResp, error) { return nil, errors.New("offline") }}, source}
	if r := quoteGET(t, s, "/api/quote-history/600081?range=1m"); r.Code != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("disabled source called: %d calls=%d", r.Code, calls)
	}
	s.st.SetSetting(setQuoteSourceOrder, "tencent,sohu")
	r := quoteGET(t, s, "/api/quote/600081?range=1m")
	var body map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if r.Code != http.StatusServiceUnavailable || body["historyAvailable"] != true || calls != 0 {
		t.Fatalf("normal route used history: %d %s calls=%d", r.Code, r.Body.String(), calls)
	}
	r = quoteGET(t, s, "/api/quote-history/600081?range=1m")
	if r.Code != http.StatusOK || calls != 1 {
		t.Fatalf("historical request: %d %s calls=%d", r.Code, r.Body.String(), calls)
	}
	out := quoteBody(t, r)
	if !out.HistoryOnly || out.HistoryThrough != "2026-10-23" || out.Snapshot.AsOf != "" {
		t.Fatalf("historical response mislabelled: %+v", out)
	}
	if _, _, ok := s.quotes.snapshotForTTL("sh", "600081"); ok {
		t.Fatal("history warmed live snapshot cache")
	}
	if r := quoteGET(t, s, "/api/quote/600081?range=1m"); r.Code != http.StatusServiceUnavailable {
		t.Fatal("history cache answered normal quote request")
	}
	for _, path := range []string{"/api/quote-history/600081?range=1d", "/api/quote-history/sh:000001?range=1m"} {
		if r := quoteGET(t, s, path); r.Code == http.StatusOK {
			t.Fatalf("unmeasured request answered: %s", path)
		}
	}
	if calls != 1 {
		t.Fatalf("unsupported interval made a history call: %d", calls)
	}
}

func TestStaleQuoteAPIKeepsVendorClockAndDisablesBrowserCache(t *testing.T) {
	s := quoteServer(t)
	clock := time.Date(2026, 10, 26, 2, 0, 0, 0, time.UTC)
	s.quotes.now = func() time.Time { return clock }
	s.quotes.init()
	s.quotes.sources = []quoteSource{{name: "tencent", fetch: func(context.Context, string, string, int) (*QuoteResp, error) { return nil, errors.New("offline") }}}
	s.quotes.put(quoteCacheKey("sh", "600081", 22, quoteIntervalDaily, quoteWindow{}), &QuoteResp{Symbol: "600081", Source: "tencent", Snapshot: QuoteSnapshot{Last: 1013, AsOf: "2026-10-23T16:14:56+08:00", Session: quoteSessionClose}}, -time.Minute)
	r := quoteGET(t, s, "/api/quote/600081?range=1m")
	out := quoteBody(t, r)
	if r.Code != 200 || !out.Stale || !out.Cached || out.Snapshot.AsOf != "2026-10-23T16:14:56+08:00" || r.Header().Get("Cache-Control") != "private, no-store" || out.RefreshAfterSecs != 120 {
		t.Fatalf("stale response: %d %+v %v", r.Code, out, r.Header())
	}
}
