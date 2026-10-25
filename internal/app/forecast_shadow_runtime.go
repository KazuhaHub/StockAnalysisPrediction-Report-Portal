package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
)

const forecastShadowDeadline = 15 * time.Second

type forecastShadowSample struct {
	target  quoteTarget
	primary string
	bars    []QuoteBar
	anchors [2]string
}

type forecastShadowPass struct {
	enabled bool
	samples []forecastShadowSample
}

func newForecastShadowPass(s *Server) *forecastShadowPass {
	return &forecastShadowPass{enabled: forecastFallbackMode(s.st.GetSetting(setForecastFallback, "off")) == "shadow"}
}

// Only already-accepted primary bars are copied. Sampling is opportunistic and bounded, not a
// statistically representative survey or Tier 2 admission. Both ends of the compared body are
// explicit anchors; every intervening OHLC/date (including any S0/base date) is compared too.
func (pass *forecastShadowPass) offer(s *Server, series forecastSeries) {
	if !pass.enabled || len(pass.samples) >= forecastShadowMaxComparisons || len(series.Bars) == 0 || series.UnverifiableDate != "" {
		return
	}
	target, err := quoteTargetFor(series.Market, series.Code)
	if err != nil {
		return
	}
	primary, ok := ledgerPrimary(s.quotes.ledgerChain(s.quoteConfigLoad(), target))
	if !ok || primary.name != series.Source {
		return
	}
	for _, sample := range pass.samples {
		if sample.target.Symbol == target.Symbol {
			return
		}
	}
	if _, err := indexQuoteShadowBars(series.Bars); err != nil {
		return
	}
	bars := append([]QuoteBar{}, series.Bars...)
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	pass.samples = append(pass.samples, forecastShadowSample{target: target, primary: primary.name, bars: bars, anchors: [2]string{bars[0].Date, bars[len(bars)-1].Date}})
}

// The caller has finished primary processing and cancelled its context before entering here.
// Candidate data is never returned to a planner, and storage failure never retries ledger writes.
func (pass *forecastShadowPass) finish(s *Server, observations []forecastCallObservation, now time.Time, completed bool, deadline time.Duration) {
	if len(observations) == 0 && len(pass.samples) == 0 {
		return
	}
	run := forecastShadowRun{At: now.UTC().Format(time.RFC3339), Results: []forecastShadowResult{}}
	if deadline <= 0 {
		deadline = forecastShadowDeadline
	}
	ctx, cancel := context.WithTimeout(withVendorPriority(context.Background(), vendorShadow), deadline)
	defer cancel()
	ctx, budget := withQuoteShadowHTTPBudget(ctx)
	if completed && pass.enabled {
		for _, sample := range pass.samples {
			if ctx.Err() != nil || forecastFallbackMode(s.st.GetSetting(setForecastFallback, "off")) != "shadow" {
				break
			}
			result := forecastShadowResult{Target: quoteProbeTarget{Market: sample.target.Market.id, Code: sample.target.Code}, Primary: sample.primary, Anchors: sample.anchors, Status: "unavailable"}
			observed := false
			cfg := s.quoteConfigLoad()
			primary, ok := ledgerPrimary(s.quotes.ledgerChain(cfg, sample.target))
			if !ok || primary.name != sample.primary {
				result.Reason = "primary_changed"
			} else if source, ok := s.quotes.shadowPeer(cfg, sample.target, sample.primary); !ok {
				result.Reason = "no_shadow"
			} else {
				result.Candidate = source.name
				if budget.attempts() >= forecastShadowMaxHTTPAttempts {
					result.Reason = "shadow_budget"
				} else {
					before := budget.attempts()
					denials := budget.denials()
					scope := fmt.Sprintf("%s|%s|%s|%s|%t",
						forecastMode(s.st.GetSetting(setForecastAuto, forecastModeOff)),
						forecastFallbackMode(s.st.GetSetting(setForecastFallback, "off")),
						strings.Join(cfg.Order, ","), now.In(chinaZone).Format("2006-01-02"), predSessionStamp(now))
					readCtx := withSinaRangeProgress(ctx, &s.quotes.sinaProgress, scope, now)
					response, called, err := fetchForecastShadow(readCtx, s, source, sample)
					if called {
						run.Reads++
					}
					// Display-oriented adapters may degrade transport errors into unavailable history.
					// Attribute an actual budget denial without masking a real fourth-request failure.
					if budget.denials() > denials {
						err = &ledgerRefusal{Code: "shadow_budget"}
					}
					if err == nil {
						result.Comparison, err = compareForecastShadow(sample, source.name, response, now)
					}
					observation, reason := forecastCallResult(ctx, source.name, err)
					if !called {
						observation.Calls = 0
						observation.Failure = false
					}
					observation.ShadowHTTPAttempts = int64(budget.attempts() - before)
					observations = append(observations, observation)
					observed = true
					if err != nil {
						result.Reason = reason
					} else {
						result.Status = "mismatch"
						if result.Comparison.Match {
							result.Status = "match"
						}
					}
				}
			}
			if !observed {
				source := result.Candidate
				if source == "" {
					source = "configuration"
				}
				observations = append(observations, forecastCallObservation{Source: source, Refusal: result.Reason})
			}
			run.Results = append(run.Results, result)
		}
	}
	run.HTTPAttempts = budget.attempts()
	// A timed-out candidate must not cancel persistence of its own refusal/failure.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer saveCancel()
	if err := s.st.saveForecastDiagnostics(saveCtx, run, observations); err != nil {
		log.Printf("forecast diagnostics: save failed: %v", err)
	}
}

func (c *quoteCache) shadowPeer(cfg quoteConfig, target quoteTarget, primary string) (quoteSource, bool) {
	for _, name := range cfg.Order {
		for _, source := range c.sources {
			if source.name == name && name != primary && source.fetchRange != nil && source.servesExplicit(target, quoteIntervalSettledShadow) {
				return source, true
			}
		}
	}
	return quoteSource{}, false
}

func fetchForecastShadow(ctx context.Context, s *Server, source quoteSource, sample forecastShadowSample) (response *QuoteResp, called bool, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = fmt.Errorf("shadow source panicked")
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	release, err := s.quotes.acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	called = true
	response, err = source.fetchRange(ctx, sample.target.Market.id, sample.target.Code, sample.anchors[0], sample.anchors[1])
	return response, called, err
}

func compareForecastShadow(sample forecastShadowSample, expectedSource string, response *QuoteResp, now time.Time) (*quoteShadowComparison, error) {
	if response == nil || response.BarsUnavailable != "" || response.Source != expectedSource || response.Source != response.BarsSource || response.Market != sample.target.Market.id || response.Symbol != sample.target.Code || response.Currency != sample.target.Market.currency || response.TZ != sample.target.Market.zone || response.Adjusted || response.Cached {
		return nil, fmt.Errorf("invalid shadow response identity or history")
	}
	if _, err := indexQuoteShadowBars(response.Bars); err != nil {
		return nil, err
	}
	bars := []QuoteBar{}
	for _, bar := range response.Bars {
		if bar.Date >= sample.anchors[0] && bar.Date <= sample.anchors[1] {
			bars = append(bars, bar)
		}
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	if len(bars) == 0 {
		return nil, fmt.Errorf("shadow history has no bars in accepted window")
	}
	if final, _ := newestBarFinal(bars[len(bars)-1], response.Snapshot.AsOf, now); !final {
		return nil, &ledgerRefusal{Code: "incomplete"}
	}
	comparison, err := compareQuoteShadow(sample.bars, bars, sample.anchors)
	if err != nil {
		return nil, err
	}
	return &comparison, nil
}

func forecastCallResult(ctx context.Context, source string, err error) (forecastCallObservation, string) {
	observation := forecastCallObservation{Source: source, Calls: 1}
	if err == nil {
		return observation, ""
	}
	var deferred *vendorDeferredError
	if errors.As(err, &deferred) {
		observation.Calls = 0
		observation.Refusal = deferred.Reason
		return observation, deferred.Reason
	}
	if ctx.Err() != nil {
		observation.Refusal = "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			observation.Refusal = "deadline"
		}
		return observation, observation.Refusal
	}
	var refusal *ledgerRefusal
	if errors.As(err, &refusal) {
		code := refusal.Code
		if _, known := predRefusalClass[code]; !known {
			switch code {
			case "window_not_covered", "shadow_budget", "incomplete", "breaker", "cooldown", "missing_fetcher":
			default:
				code = "other_refusal"
			}
		}
		observation.Refusal = code
		return observation, code
	}
	observation.Failure = true
	return observation, "fetch_failed"
}
