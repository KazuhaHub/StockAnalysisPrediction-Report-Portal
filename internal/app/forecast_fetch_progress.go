package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Only one unfinished logical pass owns this evidence. It never enters the display cache or
// survives a restart. Work selection and guarded writes still read current database rows.
type forecastFetchProgress struct {
	key         string
	started     time.Time
	series      map[string]forecastSeries
	adjustments map[string]map[string]int64
	waiting     bool
}

func (e *forecastEvaluator) prepareFetchProgress(s *Server) {
	now := e.clock()
	key := fmt.Sprintf("%s|%s|%s|%t", e.passMode, strings.Join(s.quoteConfigLoad().Order, ","),
		now.In(chinaZone).Format("2006-01-02"), predSessionStamp(now))
	if p := e.progress; p != nil && p.key == key && now.Sub(p.started) >= 0 && now.Sub(p.started) < 6*time.Hour && len(p.series)+len(p.adjustments) < 256 {
		p.waiting = false
		return
	}
	e.progress = &forecastFetchProgress{key: key, started: now, series: map[string]forecastSeries{}, adjustments: map[string]map[string]int64{}}
}

func (e *forecastEvaluator) waitForVendor(err error) bool {
	var deferred *vendorDeferredError
	if !errors.As(err, &deferred) {
		return false
	}
	if e.progress != nil {
		e.progress.waiting = true
	}
	if e.stats != nil {
		e.stats.WaitingForBudget = true
		e.stats.ResumeAt = quoteAdminTime(deferred.RetryAt)
	}
	return true
}

func forecastReadKey(market, code, from, to string) string {
	return strings.Join([]string{market, code, from, to}, "|")
}

func (e *forecastEvaluator) adjustedBars(ctx context.Context, market, code, from, to string) (map[string]int64, error) {
	key := forecastReadKey(market, code, from, to)
	if e.progress != nil {
		if bars, ok := e.progress.adjustments[key]; ok {
			return bars, nil
		}
	}
	fetch := e.adjust
	if fetch == nil {
		fetch = fetchTencentAdjustRatio
	}
	bars, err := fetch(ctx, market, code, from, to)
	if err == nil && e.progress != nil {
		e.progress.adjustments[key] = bars
	}
	return bars, err
}
