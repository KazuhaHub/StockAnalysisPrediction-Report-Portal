package app

import (
	"fmt"
	"time"
)

// Damaged telemetry is not empty/healthy telemetry. Validate both loaded and newly accumulated
// state so corruption and integer wrap cannot be committed as plausible counters.
func validateForecastDiagnostics(shadow forecastShadowState, calls forecastCallState) error {
	at, err := time.Parse(time.RFC3339, shadow.UpdatedAt)
	if err != nil || shadow.UpdatedAt != at.UTC().Format(time.RFC3339) || shadow.UpdatedAt != calls.UpdatedAt || shadow.Version != 1 || calls.Version != 1 || shadow.Days == nil || calls.Days == nil || shadow.Recent == nil || len(shadow.Days) > 14 || len(calls.Days) > 14 || len(shadow.Recent) > 20 {
		return fmt.Errorf("invalid forecast diagnostic envelope")
	}
	cutoff, date := at.UTC().AddDate(0, 0, -13).Format(quoteWindowLayout), at.UTC().Format(quoteWindowLayout)
	validDate := func(value string) bool {
		parsed, err := time.Parse(quoteWindowLayout, value)
		return err == nil && parsed.Format(quoteWindowLayout) == value && value >= cutoff && value <= date
	}
	seen := map[string]bool{}
	for _, day := range shadow.Days {
		if !validDate(day.Date) || seen[day.Date] || day.Selected < 0 || day.Reads < 0 || day.HTTPAttempts < 0 || day.Matches < 0 || day.Mismatches < 0 || day.Unavailable < 0 || day.Reads > day.Selected || day.Matches > day.Selected || day.Mismatches > day.Selected-day.Matches || day.Selected-day.Matches-day.Mismatches != day.Unavailable {
			return fmt.Errorf("invalid shadow daily counters")
		}
		seen[day.Date] = true
	}
	shadowDates := seen
	if len(calls.Days) != len(shadow.Days) {
		return fmt.Errorf("inconsistent forecast diagnostic days")
	}
	seen = map[string]bool{}
	for _, day := range calls.Days {
		if !validDate(day.Date) || seen[day.Date] || !shadowDates[day.Date] || day.Sources == nil || len(day.Sources) > 32 {
			return fmt.Errorf("invalid call daily counters")
		}
		seen[day.Date] = true
		for source, count := range day.Sources {
			if source == "" || len(source) > 64 || count.Calls < 0 || count.Failures < 0 || count.Failures > count.Calls || count.ShadowHTTPAttempts < 0 || count.Refusals == nil || len(count.Refusals) > 128 {
				return fmt.Errorf("invalid source counters")
			}
			for code, n := range count.Refusals {
				if code == "" || len(code) > 64 || n < 0 {
					return fmt.Errorf("invalid refusal counters")
				}
			}
		}
	}
	previous := ""
	for _, result := range shadow.Recent {
		stamp, err := time.Parse(time.RFC3339, result.At)
		if err != nil || result.At != stamp.UTC().Format(time.RFC3339) || !validDate(stamp.UTC().Format(quoteWindowLayout)) || result.At > shadow.UpdatedAt || result.At < previous {
			return fmt.Errorf("invalid recent comparison timestamp")
		}
		previous = result.At
		if result.Status == "unavailable" {
			if result.Comparison != nil || result.DifferenceCount != 0 || result.Truncated {
				return fmt.Errorf("unavailable comparison contains agreement")
			}
			continue
		}
		comparison := result.Comparison
		if comparison == nil || comparison.ComparedDates < 0 || comparison.Differences == nil || len(comparison.Differences) > 32 || result.DifferenceCount < len(comparison.Differences) || result.Truncated != (result.DifferenceCount > len(comparison.Differences)) {
			return fmt.Errorf("invalid recent comparison evidence")
		}
		switch result.Status {
		case "match":
			if !comparison.Match || comparison.ComparedDates == 0 || result.DifferenceCount != 0 {
				return fmt.Errorf("invalid agreement evidence")
			}
		case "mismatch":
			if comparison.Match || result.DifferenceCount == 0 {
				return fmt.Errorf("invalid mismatch evidence")
			}
		default:
			return fmt.Errorf("invalid recent comparison status")
		}
	}
	return nil
}
