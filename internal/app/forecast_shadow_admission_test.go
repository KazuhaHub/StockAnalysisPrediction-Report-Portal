package app

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestForecastShadowShippedSinaGrantNeverAdmitsAWriter(t *testing.T) {
	c := &quoteCache{sources: defaultQuoteSources()}
	for _, market := range []string{"sh", "sz", "bj", "hk", "us"} {
		target := quoteAsk(t, market)
		peer, ok := c.shadowPeer(quoteConfigDefault(), target, quoteSourceTencent)
		want := market == "sh" || market == "sz"
		if ok != want || (ok && (peer.name != quoteSourceSina || peer.fetchRange == nil || peer.cashDividends || peer.reportsKind)) {
			t.Fatalf("wrong shipped comparison grant for %s: %+v %v", market, peer, ok)
		}
		for i := range c.sources {
			if c.sources[i].name != quoteSourceTencent && c.sources[i].servesExplicit(target, quoteIntervalSettled) {
				t.Fatalf("alternate writer admitted: %s/%s", market, c.sources[i].name)
			}
			c.sources[i].fetchRange = func(context.Context, string, string, string, string) (*QuoteResp, error) {
				t.Fatal("disabled primary fell back to a shadow source")
				return nil, nil
			}
		}
		_, err := c.walkLedger(context.Background(), quoteConfig{Order: []string{quoteSourceSina, quoteSourceYahoo}}, target.Market.id, target.Code, "2026-06-15", "2026-06-18", &ledgerTick{})
		var refusal *ledgerRefusal
		if !errors.As(err, &refusal) || refusal.Code != "no_source" {
			t.Fatalf("alternate source became a writer: %s %v", market, err)
		}
		if _, ok := c.shadowPeer(quoteConfig{Order: []string{quoteSourceTencent}}, target, quoteSourceTencent); ok {
			t.Fatal("disabled Sina remained a comparison peer")
		}
	}
}

// The actual shipped range adapter runs through the pass's private transport budget. Only its
// underlying transport is replaced with captured bytes; no process-wide client or ledger is changed.
func TestForecastShadowShippedSinaUsesBoundedRangeAndPersistsEvidence(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "match", true: "mismatch"}[mismatch], func(t *testing.T) {
			s := newV1Server(t)
			before := forecastChecksum(t, s)
			if err := s.st.SetSetting(setForecastFallback, "shadow"); err != nil {
				t.Fatal(err)
			}
			s.quotes.sources = defaultQuoteSources()
			fixtureCtx, attempts := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
			fixtureTransport := vendorHTTPClient(fixtureCtx).Transport
			for i := range s.quotes.sources {
				if s.quotes.sources[i].name != quoteSourceSina {
					continue
				}
				fetch := s.quotes.sources[i].fetchRange
				if fetch == nil {
					t.Fatal("shipped Sina has no bounded adapter")
				}
				s.quotes.sources[i].fetchRange = func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
					budget, ok := vendorHTTPClient(ctx).Transport.(*quoteShadowHTTPBudget)
					if !ok {
						t.Fatal("real adapter bypassed the pass HTTP budget")
					}
					budget.base = fixtureTransport
					return fetch(ctx, market, code, from, to)
				}
			}
			bars, err := parseSinaKLine(readQuoteFixture(t, fixSinaKline))
			if err != nil {
				t.Fatal(err)
			}
			primary := []QuoteBar{}
			for _, bar := range bars {
				if bar.Date >= "2026-06-15" && bar.Date <= "2026-06-18" {
					primary = append(primary, bar)
				}
			}
			if len(primary) != 4 {
				t.Fatalf("fixture lost its four accepted dates: %d", len(primary))
			}
			if mismatch {
				primary[0].High++
			}
			pass := newForecastShadowPass(s)
			pass.offer(s, forecastSeries{Source: quoteSourceTencent, Market: "sh", Code: "601899", Bars: primary})
			pass.finish(s, nil, time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC), true, time.Second)
			shadow, calls, err := s.st.forecastDiagnostics()
			if err != nil {
				t.Fatal(err)
			}
			if len(shadow.Recent) != 1 || len(shadow.Days) != 1 || len(calls.Days) != 1 {
				t.Fatalf("real adapter did not persist a result: %+v %+v", shadow, calls)
			}
			result := shadow.Recent[0]
			if result.Comparison == nil || result.Comparison.Match == mismatch || result.Candidate != quoteSourceSina || !reflect.DeepEqual(result.Anchors, [2]string{"2026-06-15", "2026-06-18"}) || result.Comparison.ComparedDates != 4 || result.DifferenceCount != map[bool]int{false: 0, true: 1}[mismatch] {
				t.Fatalf("real adapter evidence wrong: %+v", result)
			}
			day := shadow.Days[0]
			if *attempts != 2 || day.Reads != 1 || day.HTTPAttempts != 2 || calls.Days[0].Sources[quoteSourceSina].Calls != 1 || calls.Days[0].Sources[quoteSourceSina].ShadowHTTPAttempts != 2 {
				t.Fatalf("real adapter budget was not measured: %+v %+v HTTP=%d", shadow, calls, *attempts)
			}
			if forecastChecksum(t, s) != before || len(s.quotes.snapshotHealth()) != 0 || len(s.quotes.snapshotLedgerHealth()) != 0 {
				t.Fatal("real candidate changed ledger or source health")
			}
		})
	}
}

func TestForecastShadowRechecksControlsBetweenSamples(t *testing.T) {
	for _, change := range []string{"fallback_off", "remove_peer", "remove_primary", "incomplete_pass"} {
		t.Run(change, func(t *testing.T) {
			s := newV1Server(t)
			if err := s.st.SetSetting(setForecastFallback, "shadow"); err != nil {
				t.Fatal(err)
			}
			s.quotes.sources = defaultQuoteSources()
			calls := 0
			for i := range s.quotes.sources {
				if s.quotes.sources[i].name != quoteSourceSina {
					continue
				}
				s.quotes.sources[i].fetchRange = func(context.Context, string, string, string, string) (*QuoteResp, error) {
					calls++
					key, value := setForecastFallback, "off"
					if change == "remove_peer" {
						key, value = setQuoteSourceOrder, quoteSourceTencent
					}
					if change == "remove_primary" {
						key, value = setQuoteSourceOrder, quoteSourceSina
					}
					if err := s.st.SetSetting(key, value); err != nil {
						t.Fatal(err)
					}
					return nil, errors.New("first candidate failed after configuration changed")
				}
			}
			pass := newForecastShadowPass(s)
			bars := []QuoteBar{{Date: "2026-06-15", Open: 1000, High: 1000, Low: 1000, Close: 1000}}
			for _, code := range []string{"000852", "000852", "601899", "601898"} {
				pass.offer(s, forecastSeries{Source: quoteSourceTencent, Market: "sh", Code: code, Bars: bars})
			}
			if len(pass.samples) != 2 || pass.samples[1].target.Code != "601899" {
				t.Fatalf("sampling lost uniqueness or two-body limit: %+v", pass.samples)
			}
			// Samples own their bars; later primary-buffer reuse cannot alter retained evidence.
			bars[0].High++
			if pass.samples[0].bars[0].High != 1000 {
				t.Fatal("sampling retained the primary's mutable buffer")
			}
			pass.finish(s, nil, time.Date(2026, 9, 8, 8, 0, 0, 0, time.UTC), change != "incomplete_pass", time.Second)
			wantCalls, wantResults := 1, 2
			if change == "fallback_off" {
				wantResults = 1
			}
			if change == "incomplete_pass" {
				wantCalls, wantResults = 0, 0
			}
			shadow, counts, err := s.st.forecastDiagnostics()
			if err != nil || calls != wantCalls || len(shadow.Recent) != wantResults || shadow.Days[0].Reads != int64(wantCalls) || counts.Days[0].Sources[quoteSourceSina].Calls != int64(wantCalls) {
				t.Fatalf("revoked candidate still called or counted: %s calls=%d shadow=%+v counts=%+v err=%v", change, calls, shadow, counts, err)
			}
			if change == "remove_peer" && shadow.Recent[1].Reason != "no_shadow" {
				t.Fatalf("removed peer was not refused: %+v", shadow.Recent)
			}
			if change == "remove_primary" && shadow.Recent[1].Reason != "primary_changed" {
				t.Fatalf("changed primary was not refused: %+v", shadow.Recent)
			}
		})
	}
}
