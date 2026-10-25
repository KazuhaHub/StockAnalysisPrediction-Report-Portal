package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestForecastAdmissionDeferralResumesWithoutRepeatingSuccessfulReads(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	s.st.SetSetting(setForecastAuto, forecastModeDry)
	before := forecastChecksum(t, s)
	fetch, adjust := e.fetch, e.adjust
	credits := 0
	successes := map[string]int{}
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if vendorWorkPriority(ctx) != vendorBackground {
			t.Fatal("evaluator bypassed background admission")
		}
		if credits == 0 {
			return nil, &vendorDeferredError{Source: "tencent", Reason: "rate_limit", RetryAt: e.clock().Add(time.Minute)}
		}
		credits--
		successes[code]++
		return fetch(ctx, market, code, from, to)
	}
	e.adjust = func(ctx context.Context, market, code, from, to string) (map[string]int64, error) {
		if credits == 0 {
			return nil, &vendorDeferredError{Source: "tencent", Reason: "rate_limit"}
		}
		credits--
		successes["adjust"]++
		return adjust(ctx, market, code, from, to)
	}
	for round := 0; round < 3; round++ {
		credits = 1
		e.step(s, "manual")
		hb, _ := s.st.ForecastHeartbeat()
		if round < 2 {
			if !hb.Pass.WaitingForBudget || len(hb.DrySample) != 0 || hb.Pass.Errors != 0 {
				t.Fatalf("slice %d did not wait cleanly: %+v", round, hb)
			}
		} else if hb.Pass.WaitingForBudget || len(hb.DrySample) == 0 {
			t.Fatalf("resumed work did not finish: %+v", hb)
		}
	}
	for _, key := range []string{"000852", "600160", "adjust"} {
		if successes[key] != 1 {
			t.Fatalf("successful %s reads repeated: %v", key, successes)
		}
	}
	if after := forecastChecksum(t, s); after != before {
		t.Fatal("dry waiting changed ledger state")
	}
}

func TestForecastProgressInvalidatesAtSessionBoundaryAndPolicyChange(t *testing.T) {
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	now := shanghai(2026, time.November, 20, 15, 29)
	e.now = func() time.Time { return now }
	e.prepareFetchProgress(s)
	e.progress.series["old"] = forecastSeries{Source: "tencent"}
	now = now.Add(2 * time.Minute)
	e.prepareFetchProgress(s)
	if len(e.progress.series) != 0 {
		t.Fatal("pre-close evidence survived the close boundary")
	}
	e.progress.series["old"] = forecastSeries{Source: "tencent"}
	s.st.SetSetting(setQuoteSourceOrder, "sina")
	e.prepareFetchProgress(s)
	if len(e.progress.series) != 0 {
		t.Fatal("evidence survived a primary-source policy change")
	}
}

func TestForecastResumedSettlementMatchesUnthrottledPass(t *testing.T) {
	reference := newV1Server(t)
	_, expected := opsFixture(t, reference)
	if err := expected.tick(reference); err != nil {
		t.Fatal(err)
	}
	s := newV1Server(t)
	_, e := opsFixture(t, s)
	fetch, adjust := e.fetch, e.adjust
	credits := 0
	e.fetch = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
		if credits == 0 {
			return nil, &vendorDeferredError{Source: "tencent", Reason: "rate_limit"}
		}
		credits--
		return fetch(ctx, market, code, from, to)
	}
	e.adjust = func(ctx context.Context, market, code, from, to string) (map[string]int64, error) {
		if credits == 0 {
			return nil, &vendorDeferredError{Source: "tencent", Reason: "rate_limit"}
		}
		credits--
		return adjust(ctx, market, code, from, to)
	}
	for i := 0; i < 3; i++ {
		credits = 1
		if err := e.tick(s); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := forecastChecksum(t, s), forecastChecksum(t, reference); got != want {
		t.Fatalf("resumed settlement diverged:\ngot %s\nwant %s", got, want)
	}
	if e.progress != nil {
		t.Fatal("completed pass retained temporary evidence")
	}
}

func TestLedgerAdmissionDeferralDoesNotTripFailureBreaker(t *testing.T) {
	c := &quoteCache{sources: []quoteSource{ledgerWalkSource("tencent", func(context.Context, string, string, string, string) (*QuoteResp, error) {
		return nil, fmt.Errorf("transport: %w", &vendorDeferredError{Source: "tencent", Reason: "rate_limit"})
	})}}
	pass := &ledgerTick{}
	for i := 0; i < 4; i++ {
		_, err := c.walkLedger(context.Background(), quoteConfig{Order: []string{"tencent"}}, "sh", "600519", "2026-10-01", "2026-10-23", pass)
		if !isVendorDeferred(err) {
			t.Fatal(err)
		}
	}
	c.closeLedgerPass(pass)
	if _, blocked := pass.blocked("tencent"); blocked {
		t.Fatal("rate admission counted as vendor failure")
	}
	if _, cooling := c.ledgerCooled("tencent"); cooling {
		t.Fatal("rate admission opened failure breaker")
	}
	for _, observation := range pass.snapshotObservations() {
		if observation.Failure || observation.Calls != 0 {
			t.Fatalf("deferral fabricated upstream work: %+v", observation)
		}
	}
}
