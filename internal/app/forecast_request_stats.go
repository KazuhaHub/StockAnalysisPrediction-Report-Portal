package app

import (
	"context"
	"log"
	"sync/atomic"
	"time"
)

// forecastSlowGroupsDefault is how long a statistics request may take before it is logged.
const forecastSlowGroupsDefault = 3 * time.Second

// forecastSlowThreshold is the duration above which a statistics request is logged.
func (s *Server) forecastSlowThreshold() time.Duration {
	if s.forecastSlowGroups > 0 {
		return s.forecastSlowGroups
	}
	return forecastSlowGroupsDefault
}

// forecastRequestStats is what one statistics request did, for its slow-request line, and the hook
// through which the same events reach the process counters. It travels in the request context so the
// builder and the compute controller need no new parameters; without it every method is a no-op.
type forecastRequestStats struct {
	sig                           *forecastSignals
	now                           func() time.Time
	cells, scored, cached, joined atomic.Int64
	waitNs                        atomic.Int64
}

type forecastRequestStatsKey struct{}

func withForecastRequestStats(ctx context.Context, st *forecastRequestStats) context.Context {
	return context.WithValue(ctx, forecastRequestStatsKey{}, st)
}

func forecastRequestStatsFrom(ctx context.Context) *forecastRequestStats {
	st, _ := ctx.Value(forecastRequestStatsKey{}).(*forecastRequestStats)
	return st
}

func (st *forecastRequestStats) process(name string) {
	if st.sig != nil && st.now != nil {
		st.sig.add(name, st.now())
	}
}

func (st *forecastRequestStats) setCells(n int) {
	if st != nil {
		st.cells.Store(int64(n))
	}
}

func (st *forecastRequestStats) addCached() {
	if st != nil {
		st.cached.Add(1)
		st.process(sigCacheHits)
	}
}

func (st *forecastRequestStats) addJoined() {
	if st != nil {
		st.joined.Add(1)
		st.process(sigFlightsJoined)
	}
}

func (st *forecastRequestStats) addScored() {
	if st != nil {
		st.scored.Add(1)
		st.process(sigScored)
	}
}

// addWait keeps the longest wait seen: how long any one computation of the request waited for a
// scoring slot, or for its answer when it gave up.
func (st *forecastRequestStats) addWait(d time.Duration) {
	if st == nil || d <= 0 {
		return
	}
	for {
		cur := st.waitNs.Load()
		if int64(d) <= cur || st.waitNs.CompareAndSwap(cur, int64(d)) {
			return
		}
	}
}

func (st *forecastRequestStats) waited() time.Duration {
	if st == nil {
		return 0
	}
	return time.Duration(st.waitNs.Load())
}

// forecastGroupsLogMode is the mode word for the slow-request line: only the two the route accepts,
// so a query value cannot put arbitrary text into a log.
func forecastGroupsLogMode(raw string) string {
	switch raw {
	case "", "month":
		return "month"
	case "custom":
		return "custom"
	}
	return "other"
}

// logSlowForecastGroups writes the documented line when a statistics request took longer than the
// threshold. It names the scope kind and the population kind, never an account or the OU ids.
func (s *Server) logSlowForecastGroups(d forecastDecision, pop forecastPopulation, mode string, st *forecastRequestStats, took time.Duration) {
	if took <= s.forecastSlowThreshold() {
		return
	}
	kind := "ou"
	if pop.Key == "all" {
		kind = "all"
	}
	log.Printf("forecast groups: scope=%s pop=%s mode=%s cells=%d scored=%d cached=%d flight=%d wait_ms=%d dur_ms=%d",
		d.Scope, kind, forecastGroupsLogMode(mode), st.cells.Load(), st.scored.Load(), st.cached.Load(), st.joined.Load(),
		st.waited().Milliseconds(), took.Milliseconds())
}
