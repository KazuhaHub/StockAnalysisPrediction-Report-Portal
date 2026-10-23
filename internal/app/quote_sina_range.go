package app

import (
	"context"
	"fmt"
	"sort"
	"time"
)

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
	response, err := fetchSinaQuote(ctx, market, code, quoteMaxBars)
	if err != nil {
		return nil, err
	}
	return boundedSinaHistory(response, window)
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
		return nil, fmt.Errorf("quote: sina newest history does not agree with its snapshot")
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
