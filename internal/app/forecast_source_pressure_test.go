package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The evaluator is the one background loop in this tree that spends a metered third-party budget, and
// on 2026-10-24 a production deployment learned what that costs: dry mode re-ran the same due sets
// every minute, the settlement endpoint answered 501, and a display vendor's rate limit then refused
// the reader's own price requests from the same egress. These tests are the three properties that
// would have caught it.

// TestDryTickerDoesNotTouchVendors: a dry mode tick is not a pass. Dry exists so an operator can see
// what a pass WOULD write, which is an operator action; a ticker running it every minute spends the
// vendor budget on a plan that the single write door then discards, because dry mode never writes the
// state that would make the same set not-due next minute.
func TestDryTickerDoesNotTouchVendors(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	before := forecastChecksum(t, s)
	fetches := 0
	innerFetch := e.fetch
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		fetches++
		return innerFetch(ctx, market, code, from, to)
	}
	adjusts := 0
	innerAdjust := e.adjust
	e.adjust = func(ctx context.Context, market, code, from, to string) (map[string]int64, error) {
		adjusts++
		return innerAdjust(ctx, market, code, from, to)
	}

	e.step(s, "tick")

	if fetches != 0 || adjusts != 0 {
		t.Fatalf("a dry tick reached the vendors: fetches=%d adjusts=%d", fetches, adjusts)
	}
	if _, ok := s.st.ForecastHeartbeat(); ok {
		t.Error("a dry tick wrote a heartbeat, so the page reports a pass that never ran")
	}
	if after := forecastChecksum(t, s); after != before {
		t.Errorf("a dry tick changed the ledger: %s -> %s", before, after)
	}
	// The mode read is what a skipped tick costs, the same as off.
	if got := s.st.GetSetting(setForecastAuto, "0"); got != forecastModeDry {
		t.Fatalf("mode = %q, want the tick to leave it alone", got)
	}
}

// The manual pass is the ONLY way a dry pass happens now, and it must still fetch and still write
// nothing. Without this positive control the test above passes on an evaluator that stopped running.
func TestDryManualPassStillFetchesAndWritesNothing(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	before := forecastChecksum(t, s)
	fetches := 0
	inner := e.fetch
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		fetches++
		return inner(ctx, market, code, from, to)
	}

	e.step(s, "manual")

	if fetches == 0 {
		t.Fatal("the manual dry pass fetched nothing, so there is no sample to check")
	}
	if after := forecastChecksum(t, s); after != before {
		t.Errorf("the manual dry pass wrote to the ledger: %s -> %s", before, after)
	}
	hb, ok := s.st.ForecastHeartbeat()
	if !ok || hb.Mode != forecastModeDry || len(hb.DrySample) == 0 {
		t.Fatalf("heartbeat = %+v, ok=%v", hb, ok)
	}
}

// TestLedgerBreakerSurvivesPassesAndRecoversAfterCooldown: the in-pass breaker is not enough. It is
// rebuilt every pass, so a ticker that runs every minute asks a source that has failed six times in a
// row to fail a seventh — which is the request amplification that got the egress blocked. The
// cooldown is process state, escalates while the source stays down, and clears on a success.
func TestLedgerBreakerSurvivesPassesAndRecoversAfterCooldown(t *testing.T) {
	now := time.Date(2026, time.October, 24, 10, 0, 0, 0, time.UTC)
	failure := errors.New("vendor web.ifzq.gtimg.cn returned 501 Not Implemented")
	calls := 0
	c := &quoteCache{now: func() time.Time { return now }, sources: []quoteSource{
		ledgerWalkSource("primary", func(context.Context, string, string, string, string) (*QuoteResp, error) {
			calls++
			return nil, failure
		}),
	}}
	cfg := quoteConfig{Order: []string{"primary"}}
	walk := func(tick *ledgerTick) error {
		_, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", tick)
		return err
	}

	// Pass 1: the in-pass ceiling holds, as before, and the pass records its failures.
	first := &ledgerTick{}
	for i := 0; i < 5; i++ {
		if err := walk(first); !errors.Is(err, failure) {
			t.Fatalf("a failed source lost its own error inside a pass: %v", err)
		}
	}
	if calls != 3 {
		t.Fatalf("in-pass ceiling = %d calls, want 3", calls)
	}
	c.closeLedgerPass(first)

	// Pass 2, a minute later: still cooling, so the vendor is not asked at all — and the caller still
	// sees WHY, not a bare "no source".
	now = now.Add(30 * time.Second)
	second := &ledgerTick{}
	if err := walk(second); !errors.Is(err, failure) {
		t.Fatalf("a blocked pass hid the reason the source is blocked: %v", err)
	}
	if calls != 3 {
		t.Fatalf("a cooling source was asked again: calls=%d", calls)
	}
	c.closeLedgerPass(second)

	// The cooldown expires: one attempt is allowed, and it escalates the next cooldown rather than
	// resetting to the shortest one.
	now = now.Add(2 * time.Minute)
	third := &ledgerTick{}
	if err := walk(third); !errors.Is(err, failure) {
		t.Fatalf("an expired cooldown did not retry: %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls after the cooldown = %d, want one more", calls)
	}
	c.closeLedgerPass(third)
	now = now.Add(90 * time.Second)
	if _, blocked := c.ledgerCooled("primary"); !blocked {
		t.Fatal("the second streak did not escalate the cooldown past 90 seconds")
	}

	// A success clears both the streak and the cooldown, so a recovered source is not punished for
	// the outage it just came out of.
	c.sources[0].fetchRange = func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return &QuoteResp{Source: "primary"}, nil
	}
	now = now.Add(10 * time.Minute)
	if _, err := c.walkLedger(context.Background(), cfg, "sh", "601899", "2026-10-01", "2026-10-22", &ledgerTick{}); err != nil {
		t.Fatalf("recovered source refused: %v", err)
	}
	if _, blocked := c.ledgerCooled("primary"); blocked {
		t.Fatal("a success left the source cooling")
	}
	c.mu.Lock()
	cooling := c.cooling["primary"]
	c.mu.Unlock()
	if cooling != nil {
		t.Fatalf("a success left cooling state behind: %+v, so the next outage would start escalated", cooling)
	}
}

// TestEvaluatorStopsFetchingASourceThatKeepsFailing is the same property one level up, through a real
// pass: consecutive ticks against a vendor that answers nothing must not each cost the same request
// budget. This is the shape the 2026-10-24 deployment was in — dry mode in front of a failing vendor,
// a tick a minute, nothing written to make a set not-due.
func TestEvaluatorStopsFetchingASourceThatKeepsFailing(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	now := shanghai(2026, time.November, 20, 16, 0)
	e.now = func() time.Time { return now }
	s.quotes.now = func() time.Time { return now }
	e.fetch = nil
	calls := 0
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return nil, errors.New("vendor web.ifzq.gtimg.cn returned 501 Not Implemented")
	})}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}

	e.step(s, "manual")
	first := calls
	if first == 0 {
		t.Fatal("the first pass never reached the vendor, so there is no pressure to bound")
	}
	// A second pass inside the cooldown asks nobody, and says why.
	e.step(s, "manual")
	if calls != first {
		t.Fatalf("a cooling source was asked %d more times one minute into its cooldown", calls-first)
	}
	var lastErr string
	s.quotes.mu.Lock()
	if cooling := s.quotes.cooling[quoteSourceTencent]; cooling != nil && cooling.last != nil {
		lastErr = cooling.last.Error()
	}
	s.quotes.mu.Unlock()
	if !strings.Contains(lastErr, "501") {
		t.Fatalf("the cooldown forgot the vendor's own reason: %q", lastErr)
	}

	// Past the first cooldown, one pass is allowed to try again — the breaker is a pause, not an
	// off switch.
	now = now.Add(2 * time.Minute)
	e.step(s, "manual")
	if calls == first {
		t.Fatal("the cooldown never expired, so a recovered vendor would never be retried")
	}
}

// A dry mode tick no longer runs a pass at all, which also means it no longer spends the vendor
// budget that the pass would have spent. Pinned through step, because that is where the decision is.
func TestDryTickerSpendsNothingOnAFailingVendor(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	e.fetch = nil
	calls := 0
	s.quotes.sources = []quoteSource{ledgerWalkSource(quoteSourceTencent, func(context.Context, string, string, string, string) (*QuoteResp, error) {
		calls++
		return nil, errors.New("vendor web.ifzq.gtimg.cn returned 501 Not Implemented")
	})}
	if err := s.st.SetSetting(setForecastAuto, forecastModeDry); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		e.step(s, "tick")
	}
	if calls != 0 {
		t.Fatalf("five dry ticks made %d vendor requests", calls)
	}
}

func TestVendorGetNon2xxLeavesEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Server", "tencent-cos")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("<html>\n <head><title>429 Too Many Requests</title></head>\n<body>rate limited</body></html>"))
	}))
	defer srv.Close()

	_, err := vendorGet(context.Background(), srv.URL+"/appstock/app/fqkline/get?param=sh000852", "https://gu.qq.com/", 4096)
	var vendorErr *vendorStatusError
	if !errors.As(err, &vendorErr) {
		t.Fatalf("non-2xx did not produce a typed error: %v", err)
	}
	if vendorErr.StatusCode != http.StatusTooManyRequests || vendorErr.RequestPath != "/appstock/app/fqkline/get" {
		t.Fatalf("evidence = %+v", vendorErr)
	}
	if vendorErr.Server != "tencent-cos" || vendorErr.RetryAfter != "120" || vendorErr.Proto == "" {
		t.Fatalf("headers lost: %+v", vendorErr)
	}
	msg := err.Error()
	for _, want := range []string{"429", "tencent-cos", "Retry-After: 120", "GET", "/appstock/app/fqkline/get", "rate limited"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not carry %q", msg, want)
		}
	}
	// A body summary is EVIDENCE, not a document: newlines and markup noise go, and the whole thing
	// stays short enough to read in one log line.
	if strings.Contains(msg, "\n") || strings.Contains(vendorErr.Body, "\n") {
		t.Fatalf("summary kept line breaks: %q", msg)
	}
	if len([]rune(vendorErr.Body)) > vendorBodySummaryRunes {
		t.Fatalf("summary is %d runes, want at most %d", len([]rune(vendorErr.Body)), vendorBodySummaryRunes)
	}
}

// The evidence is bounded, and a vendor that answers with a huge error page costs the same fixed
// number of bytes as any other: the summary is read from the same bound the caller set.
func TestVendorGetNon2xxSummaryIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", 64*1024))
	}))
	defer srv.Close()

	_, err := vendorGet(context.Background(), srv.URL, "", 128)
	var vendorErr *vendorStatusError
	if !errors.As(err, &vendorErr) {
		t.Fatalf("non-2xx did not produce a typed error: %v", err)
	}
	if len(vendorErr.Body) == 0 {
		t.Fatal("no evidence from a 502")
	}
	if len(vendorErr.Body) > 256 {
		t.Fatalf("summary = %d bytes, want it bounded", len(vendorErr.Body))
	}
}
