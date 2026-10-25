package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

var errSinaHistoryObservation = errors.New("quote: sina newest history does not agree with its snapshot")

// fetchSinaRange is comparison-only. Sina has a count endpoint, not a range endpoint:
// request the full bounded tail, validate its newest observation, then trim by actual row dates.
// Capability grants are separate; this never grants a writer or changes display degradation.
func fetchSinaRange(ctx context.Context, market, code, from, to string) (*QuoteResp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	window, err := quoteParseWindow(from, to)
	if err != nil || !window.bounded() {
		return nil, &ledgerRefusal{Code: "bad_window"}
	}
	target, err := quoteSinaTarget(market, code)
	if err != nil {
		return nil, err
	}
	if !quoteMarketHasDailyHistory(market) {
		return nil, fmt.Errorf("quote: sina has no eligible daily history for this market")
	}
	ctx, cancel := context.WithTimeout(ctx, quoteFetchTimeout)
	defer cancel()
	continuation := sinaContinuation(ctx, forecastReadKey(market, code, from, to))
	response := continuation.get()
	resumed := response != nil
	if response == nil {
		response, err = fetchSinaSnapshot(ctx, target)
		if err != nil {
			return nil, err
		}
	}
	rawURL := fmt.Sprintf(quoteSinaKlineURL, target.Symbol, quoteMaxBars)
	body, err := vendorGet(ctx, rawURL, quoteSinaReferer, quoteBodyLimit)
	if err != nil {
		var refusal *ledgerRefusal
		if isVendorDeferred(err) || (errors.As(err, &refusal) && refusal.Code == "shadow_budget") {
			continuation.keep(response)
		} else {
			continuation.forget()
		}
		return nil, err
	}
	continuation.forget()
	response.Bars, err = parseSinaKLine(body)
	if err != nil {
		return nil, err
	}
	response.BarsSource = quoteSourceSina
	bounded, err := boundedSinaHistory(response, window)
	if resumed && errors.Is(err, errSinaHistoryObservation) {
		// A delayed second component can describe a different observation without a delivery fault.
		return nil, &ledgerRefusal{Code: "incomplete"}
	}
	return bounded, err
}

func boundedSinaHistory(response *QuoteResp, window quoteWindow) (*QuoteResp, error) {
	if response == nil {
		return nil, fmt.Errorf("quote: sina returned no response")
	}
	if response.BarsUnavailable != "" {
		return nil, fmt.Errorf("quote: sina history unavailable: %s", response.BarsUnavailable)
	}
	if response.Source != quoteSourceSina || response.BarsSource != quoteSourceSina || response.Adjusted || response.Cached {
		return nil, fmt.Errorf("quote: sina history source, adjustment or cache mismatch")
	}
	if len(response.Bars) > quoteMaxBars {
		return nil, fmt.Errorf("quote: sina history exceeds count cap")
	}
	if _, err := indexQuoteShadowBars(response.Bars); err != nil {
		return nil, fmt.Errorf("quote: sina history: %w", err)
	}
	bars := append([]QuoteBar{}, response.Bars...)
	sort.Slice(bars, func(i, j int) bool { return bars[i].Date < bars[j].Date })
	stamp, err := time.Parse(time.RFC3339, response.Snapshot.AsOf)
	if err != nil {
		return nil, fmt.Errorf("quote: sina snapshot timestamp: %w", err)
	}
	target, err := quoteSinaTarget(response.Market, response.Symbol)
	if err != nil {
		return nil, err
	}
	zone, err := quoteZone(target.Market.zone)
	if err != nil {
		return nil, err
	}
	newest := bars[len(bars)-1]
	if newest.Date != stamp.In(zone).Format(quoteWindowLayout) || newest.Close != response.Snapshot.Last {
		return nil, errSinaHistoryObservation
	}
	// A full tail starting after the requested beginning cannot establish that earlier window.
	// A shorter response can reflect a new listing or suspension; do not invent sessions before it.
	if len(bars) == quoteMaxBars && bars[0].Date > window.from {
		return nil, &ledgerRefusal{Code: "window_not_covered"}
	}
	bounded := make([]QuoteBar, 0, len(bars))
	for _, bar := range bars {
		if bar.Date >= window.from && bar.Date <= window.to {
			bounded = append(bounded, bar)
		}
	}
	if len(bounded) == 0 {
		return nil, fmt.Errorf("quote: sina has no bars in requested window")
	}
	result := *response
	result.Bars = bounded
	return &result, nil
}
