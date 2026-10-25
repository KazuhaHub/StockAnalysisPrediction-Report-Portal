package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestForecastShadowSinaContinuationUsesRealAdmissionAcrossPasses(t *testing.T) {
	for _, change := range []string{"resume", "source_order", "mode", "day", "session", "observation_changed"} {
		t.Run(change, func(t *testing.T) {
			s := newV1Server(t)
			s.st.SetSetting(setForecastAuto, forecastModeDry)
			s.st.SetSetting(setForecastFallback, "shadow")
			s.quotes.init()
			fixture, calls := sinaRangeTestContext(t, readQuoteFixture(t, fixSinaKline), http.StatusOK)
			clock := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
			if change == "session" {
				clock = time.Date(2026, 9, 4, 7, 29, 55, 0, time.UTC)
			}
			scheduler := newVendorScheduler()
			scheduler.now = func() time.Time { return clock }
			base := vendorHTTPClient(fixture).Transport
			if change == "observation_changed" {
				originalTransport := base
				base = forecastVendorTransport(func(request *http.Request) (*http.Response, error) {
					response, err := originalTransport.RoundTrip(request)
					if err == nil && request.URL.Hostname() == "hq.sinajs.cn" {
						body, readErr := io.ReadAll(response.Body)
						response.Body.Close()
						if readErr != nil {
							return nil, readErr
						}
						response.Body = io.NopCloser(strings.NewReader(strings.Replace(string(body), ",33.350,", ",33.360,", 1)))
					}
					return response, err
				})
			}
			transport := &vendorScheduledTransport{base: base, scheduler: scheduler}
			for i := range s.quotes.sources {
				if s.quotes.sources[i].name == quoteSourceSina {
					s.quotes.sources[i].fetchRange = func(ctx context.Context, m, c, f, to string) (*QuoteResp, error) {
						// Replace only the private budget's underlying network, retaining its actual-attempt accounting.
						budget := vendorHTTPClient(ctx).Transport.(*quoteShadowHTTPBudget)
						budget.base = transport
						return fetchSinaRange(ctx, m, c, f, to)
					}
				}
			}
			original, err := parseSinaSnapshot("sh", "601899", string(readQuoteFixture(t, fixSinaSnap)))
			if err != nil {
				t.Fatal(err)
			}
			original.Bars, err = parseSinaKLine(readQuoteFixture(t, fixSinaKline))
			if err != nil {
				t.Fatal(err)
			}
			original.BarsSource = quoteSourceSina
			bounded, err := boundedSinaHistory(original, quoteWindow{from: "2026-06-15", to: "2026-06-18"})
			if err != nil {
				t.Fatal(err)
			}
			target, _ := quoteTargetFor("sh", "601899")
			pass := &forecastShadowPass{enabled: true, samples: []forecastShadowSample{{target: target, primary: quoteSourceTencent, bars: bounded.Bars, anchors: [2]string{"2026-06-15", "2026-06-18"}}}}
			before := forecastChecksum(t, s)
			pass.finish(s, nil, clock, true, time.Second)
			state, observed, err := s.st.forecastDiagnostics()
			if err != nil {
				t.Fatal(err)
			}
			if *calls != 1 || state.Recent[0].Reason != "rate_limit" || observed.Days[0].Sources[quoteSourceSina].Failures != 0 {
				t.Fatalf("partial read misclassified: calls=%d %+v %+v", *calls, state, observed)
			}
			clock = clock.Add(10 * time.Second)
			switch change {
			case "source_order":
				s.st.SetSetting(setQuoteSourceOrder, "sina,tencent")
			case "mode":
				s.st.SetSetting(setForecastAuto, forecastModeOn)
			case "day":
				clock = clock.Add(24 * time.Hour)
			}
			pass.finish(s, nil, clock, true, time.Second)
			state, observed, err = s.st.forecastDiagnostics()
			if err != nil {
				t.Fatal(err)
			}
			latest := state.Recent[len(state.Recent)-1]
			if change == "resume" {
				if latest.Status != "match" {
					t.Fatalf("shadow did not resume: %+v", state)
				}
			} else if change == "observation_changed" {
				if latest.Reason != "incomplete" {
					t.Fatalf("changed observation became a delivery failure: %+v", state)
				}
			} else if latest.Reason != "rate_limit" {
				t.Fatalf("partial snapshot crossed scope: %+v", state)
			}
			var attempts, failures int64
			for _, day := range state.Days {
				attempts += day.HTTPAttempts
			}
			for _, day := range observed.Days {
				failures += day.Sources[quoteSourceSina].Failures
			}
			if *calls != 2 || attempts != 2 || failures != 0 || forecastChecksum(t, s) != before || s.quotes.stats().Entries != 0 {
				t.Fatalf("continuation leaked accounting, cache or ledger: calls=%d attempts=%d failures=%d", *calls, attempts, failures)
			}
		})
	}
}
