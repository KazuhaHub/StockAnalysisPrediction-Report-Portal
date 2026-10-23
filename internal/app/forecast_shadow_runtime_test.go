package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestShadowNeverWritesTheLedger(t *testing.T) {
	baseline := newV1Server(t)
	_, baseEval := opsFixture(t, baseline)
	if err := baseEval.tick(baseline); err != nil {
		t.Fatal(err)
	}
	want := forecastChecksum(t, baseline)
	for _, behavior := range []string{"match", "mismatch", "failure", "panic", "timeout", "wrong_source", "missing_anchor", "unavailable_history", "cached", "adjusted", "hidden_budget", "fourth_failure"} {
		t.Run(behavior, func(t *testing.T) {
			s := newV1Server(t)
			_, e := opsFixture(t, s)
			primaryFetch := e.fetch
			e.fetch = nil
			if err := s.st.SetSetting(setForecastFallback, "shadow"); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int64
			stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := requests.Add(1)
				if behavior == "fourth_failure" && n == 4 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer stub.Close()
			shadowCalls, primaryCalls, adjustCalls := 0, 0, 0
			adjust := e.adjust
			e.adjust = func(ctx context.Context, m, c, f, to string) (map[string]int64, error) {
				if shadowCalls != 0 {
					t.Fatal("shadow ran before adjustment evidence finished")
				}
				adjustCalls++
				return adjust(ctx, m, c, f, to)
			}
			s.quotes.sources = []quoteSource{
				ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
					if shadowCalls != 0 {
						t.Fatal("shadow ran before primary work finished")
					}
					primaryCalls++
					return primaryFetch(ctx, market, code, from, to)
				}),
				{name: quoteSourceSina, caps: []quoteCapability{{intervals: []quoteInterval{quoteIntervalSettledShadow}}}, fetchRange: func(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
					shadowCalls++
					if forecastChecksum(t, s) != want {
						t.Fatal("shadow started before complete primary ledger work")
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("shadow has no independent deadline")
					}
					switch behavior {
					case "panic":
						panic("candidate fault")
					case "timeout":
						<-ctx.Done()
						return nil, ctx.Err()
					case "failure":
						return nil, errors.New("candidate unavailable")
					}
					n := 2
					if behavior == "hidden_budget" {
						n = 5
					}
					if behavior == "fourth_failure" {
						n = 4
					}
					for range n {
						if _, err := vendorGet(ctx, stub.URL, "", 1024); err != nil {
							if behavior == "hidden_budget" {
								return &QuoteResp{BarsUnavailable: quoteBarsSourceFailed}, nil
							}
							return nil, err
						}
					}
					response, err := primaryFetch(ctx, market, code, from, to)
					if err != nil {
						return nil, err
					}
					result := *response
					result.Source = quoteSourceSina
					result.BarsSource = quoteSourceSina
					result.Market = market
					result.Symbol = code
					result.Currency = "CNY"
					result.TZ = "Asia/Shanghai"
					result.Bars = append([]QuoteBar{}, response.Bars...)
					if behavior == "mismatch" {
						result.Bars[0].High++
					}
					switch behavior {
					case "wrong_source":
						result.Source = quoteSourceTencent
						result.BarsSource = quoteSourceTencent
					case "missing_anchor":
						result.Bars = result.Bars[1:]
					case "unavailable_history":
						result.BarsUnavailable = quoteBarsSourceFailed
					case "cached":
						result.Cached = true
					case "adjusted":
						result.Adjusted = true
					}
					return &result, nil
				}},
			}
			if behavior == "timeout" {
				e.shadowTimeout = 5 * time.Millisecond
			}
			if err := e.tick(s); err != nil {
				t.Fatal(err)
			}
			if got := forecastChecksum(t, s); got != want {
				t.Fatalf("shadow %s changed ledger", behavior)
			}
			if primaryCalls == 0 || shadowCalls == 0 || shadowCalls > 2 || requests.Load() > 4 {
				t.Fatalf("missing/unbounded observations: primary=%d shadow=%d HTTP=%d", primaryCalls, shadowCalls, requests.Load())
			}
			state, calls, err := s.st.forecastDiagnostics()
			if err != nil {
				t.Fatal(err)
			}
			day := state.Days[len(state.Days)-1]
			if day.Reads != int64(shadowCalls) || day.HTTPAttempts != requests.Load() || len(state.Recent) == 0 || calls.Days[0].Sources[quoteSourceTencent].Calls != int64(primaryCalls+adjustCalls) {
				t.Fatalf("wrong persisted run: %+v %+v", state, calls)
			}
			for _, result := range state.Recent {
				wantStatus := "unavailable"
				if behavior == "match" {
					wantStatus = "match"
				}
				if behavior == "hidden_budget" {
					source := calls.Days[0].Sources[quoteSourceSina]
					if source.Failures != 0 || source.Refusals["shadow_budget"] != int64(len(state.Recent)) {
						t.Fatalf("quota misattributed or counted twice: %+v %+v", source, state.Recent)
					}
				}
				if behavior == "fourth_failure" {
					source := calls.Days[0].Sources[quoteSourceSina]
					if source.Failures != 1 || source.Refusals["shadow_budget"] != 1 {
						t.Fatalf("real fourth-request failure hidden by quota: %+v", source)
					}
				}
				if behavior == "mismatch" || behavior == "missing_anchor" {
					wantStatus = "mismatch"
				}
				if result.Status != wantStatus {
					t.Fatalf("candidate status = %+v, want %s", result, wantStatus)
				}
			}
			if len(s.quotes.snapshotHealth()) != 0 || len(s.quotes.snapshotLedgerHealth()) != 1 {
				t.Fatal("shadow polluted display/primary health")
			}
		})
	}
}

func TestForecastShadowWaitingForSlotDoesNotBillSource(t *testing.T) {
	s := newV1Server(t)
	s.quotes.init()
	for range cap(s.quotes.sem) {
		s.quotes.sem <- struct{}{}
	}
	defer func() {
		for range cap(s.quotes.sem) {
			<-s.quotes.sem
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	source := quoteSource{name: quoteSourceSina, fetchRange: func(context.Context, string, string, string, string) (*QuoteResp, error) {
		t.Fatal("source invoked without an upstream slot")
		return nil, nil
	}}
	response, called, err := fetchForecastShadow(ctx, s, source, forecastShadowSample{})
	if called || response != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting billed a source read: called=%v response=%v err=%v", called, response, err)
	}
}

func TestForecastShadowRequiresEnabledPeerAndAcceptedPrimary(t *testing.T) {
	for _, kind := range []string{"off", "disabled_peer", "no_primary", "bad_index"} {
		t.Run(kind, func(t *testing.T) {
			s := newV1Server(t)
			_, e := opsFixture(t, s)
			fetch := e.fetch
			e.fetch = nil
			if err := s.st.SetSetting(setForecastFallback, "shadow"); err != nil {
				t.Fatal(err)
			}
			if kind == "off" {
				if err := s.st.SetSetting(setForecastFallback, "off"); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			primary := ledgerWalkSource(quoteSourceTencent, func(ctx context.Context, m, c, f, to string) (*QuoteResp, error) {
				response, err := fetch(ctx, m, c, f, to)
				if response != nil && kind == "bad_index" && c == "000852" {
					response.Kind = quoteKindStock
				}
				return response, err
			})
			peer := quoteSource{name: quoteSourceSina, caps: []quoteCapability{{intervals: []quoteInterval{quoteIntervalSettledShadow}}}, fetchRange: func(context.Context, string, string, string, string) (*QuoteResp, error) {
				calls++
				return nil, errors.New("must not run")
			}}
			s.quotes.sources = []quoteSource{primary, peer}
			if kind == "disabled_peer" {
				if err := s.st.SetSetting(setQuoteSourceOrder, "tencent"); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "no_primary" {
				if err := s.st.SetSetting(setQuoteSourceOrder, "sina"); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.tick(s); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("unaccepted primary or disabled mode/peer fetched %d candidates", calls)
			}
		})
	}
}
