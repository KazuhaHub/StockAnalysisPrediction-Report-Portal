package app

import (
	"fmt"
	"sort"
	"time"
)

type quoteShadowDifference struct {
	Date      string `json:"date"`
	Field     string `json:"field"`
	Anchor    bool   `json:"anchor"`
	Primary   *int64 `json:"primary,omitempty"`
	Candidate *int64 `json:"candidate,omitempty"`
}

type quoteShadowComparison struct {
	Match         bool                    `json:"match"`
	ComparedDates int                     `json:"compared_dates"`
	Differences   []quoteShadowDifference `json:"differences"`
}

// compareQuoteShadow is shared by offline replay and the future runtime shadow path. It compares
// evidence only: agreement grants no settlement capability and causes no ledger or health write.
func compareQuoteShadow(primary, candidate []QuoteBar, anchors [2]string) (quoteShadowComparison, error) {
	window, err := quoteParseWindow(anchors[0], anchors[1])
	if err != nil || !window.bounded() {
		return quoteShadowComparison{}, fmt.Errorf("shadow comparison requires two ordered anchor dates")
	}
	p, err := indexQuoteShadowBars(primary)
	if err != nil {
		return quoteShadowComparison{}, fmt.Errorf("primary: %w", err)
	}
	c, err := indexQuoteShadowBars(candidate)
	if err != nil {
		return quoteShadowComparison{}, fmt.Errorf("candidate: %w", err)
	}
	for _, date := range anchors {
		if _, have := p[date]; !have {
			return quoteShadowComparison{}, fmt.Errorf("primary lacks anchor %s", date)
		}
	}
	dates := make([]string, 0, len(p)+len(c))
	for date := range p {
		dates = append(dates, date)
	}
	for date := range c {
		if _, have := p[date]; !have {
			dates = append(dates, date)
		}
	}
	sort.Strings(dates)
	result := quoteShadowComparison{Differences: []quoteShadowDifference{}}
	for _, date := range dates {
		pb, phave := p[date]
		cb, chave := c[date]
		anchor := date == anchors[0] || date == anchors[1]
		if !phave || !chave {
			result.Differences = append(result.Differences, quoteShadowDifference{Date: date, Field: "date", Anchor: anchor})
			continue
		}
		result.ComparedDates++
		for _, field := range []struct {
			name string
			p, c int64
		}{{"open", pb.Open, cb.Open}, {"high", pb.High, cb.High}, {"low", pb.Low, cb.Low}, {"close", pb.Close, cb.Close}} {
			if field.p != field.c {
				pv, cv := field.p, field.c
				result.Differences = append(result.Differences, quoteShadowDifference{
					Date: date, Field: field.name, Anchor: anchor, Primary: &pv, Candidate: &cv,
				})
			}
		}
	}
	result.Match = len(result.Differences) == 0
	return result, nil
}

func indexQuoteShadowBars(bars []QuoteBar) (map[string]QuoteBar, error) {
	if len(bars) == 0 {
		return nil, fmt.Errorf("no bars to compare")
	}
	if err := quoteCheckBars(bars); err != nil {
		return nil, err
	}
	indexed := make(map[string]QuoteBar, len(bars))
	for _, bar := range bars {
		if _, err := time.Parse(quoteWindowLayout, bar.Date); err != nil {
			return nil, fmt.Errorf("invalid bar date %q", bar.Date)
		}
		if _, have := indexed[bar.Date]; have {
			return nil, fmt.Errorf("duplicate bar date %s", bar.Date)
		}
		indexed[bar.Date] = bar
	}
	return indexed, nil
}
