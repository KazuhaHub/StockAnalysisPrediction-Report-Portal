package app

// Operational signals for the reader surfaces (implementation plan section 12.1).
//
// Two kinds, both free of identities by construction:
//
//   - Process counters (requests, cache hits, flights joined, scored cells, busy, rate-limited,
//     refusals by reason, drawer opens) are kept in memory and flushed to `meta` as one flat JSON
//     object of integers per UTC day, under forecast_signals_<YYYY-MM-DD>. Counter names come from
//     a closed vocabulary below; a name or reason outside it is dropped or filed as "other", so no
//     account name, token or hash can reach the row.
//   - Log lines carry a reason code or an integer, never an account, a path or a query string.
//
// Persisting is a read-modify-write of the day's row with the events counted since the last
// successful flush, so a flush with nothing new changes nothing, a failed flush keeps its events for
// the next attempt, and a restart continues the day from the stored value. A restart or crash loses
// at most the events since the last flush (the loop flushes hourly).

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	sigRequests      = "requests"       // statistics (groups) requests that passed the access gate
	sigCacheHits     = "cache_hits"     // cells answered from the mature-cell memo
	sigFlightsJoined = "flights_joined" // cells that joined a computation another request started
	sigScored        = "scored"         // cells whose scoring a request started and saw complete
	sigBusy          = "busy"           // 503 forecast_busy answers
	sigRateLimited   = "rate_limited"   // 429 forecast_rate_limited answers
	sigDrawerOpens   = "drawer_opens"   // member lists served (the cell drawer)
	sigRefusedPrefix = "refused."       // refusals, by reason code

	forecastSignalsKeyPrefix     = "forecast_signals_"
	forecastSignalsRetentionDays = 14
	forecastRefusalLogEvery      = time.Minute
)

// forecastRefusalReasons is the closed list of access refusal codes. The resolver's own reasons come
// first ("ou" is renamed ou_none: the scope was explicitly set to none), then the refusals made by
// the routes themselves.
var forecastRefusalReasons = []string{
	"anonymous", "ou_none", "default_null", "dangling_group", "cyclic_chain", "unknown_value", "error",
	"custom_not_allowed", "stage_off", "bridge_token", "other",
}

// forecastSignalVocabulary is every counter name a row may hold.
var forecastSignalVocabulary = func() map[string]bool {
	names := map[string]bool{}
	for _, n := range []string{sigRequests, sigCacheHits, sigFlightsJoined, sigScored, sigBusy, sigRateLimited, sigDrawerOpens} {
		names[n] = true
	}
	for _, r := range forecastRefusalReasons {
		names[sigRefusedPrefix+r] = true
	}
	return names
}()

func validForecastRefusalReason(reason string) bool {
	for _, r := range forecastRefusalReasons {
		if r == reason {
			return true
		}
	}
	return false
}

func forecastSignalsKey(day string) string { return forecastSignalsKeyPrefix + day }

type forecastRefusalLog struct {
	at    time.Time
	since int64 // refusals of this reason since the last line, this one included
}

// A zero value is ready to use.
type forecastSignals struct {
	flushMu sync.Mutex // one read-modify-write of a day's row at a time
	mu      sync.Mutex
	counts  map[string]map[string]int64 // UTC day -> counter -> events not yet persisted
	logged  map[string]forecastRefusalLog
}

func (g *forecastSignals) addLocked(name string, now time.Time) {
	day := now.UTC().Format("2006-01-02")
	if g.counts == nil {
		g.counts = map[string]map[string]int64{}
	}
	if g.counts[day] == nil {
		g.counts[day] = map[string]int64{}
	}
	g.counts[day][name]++
}

// add counts one event under a name from the closed vocabulary; any other name is ignored.
func (g *forecastSignals) add(name string, now time.Time) {
	if !forecastSignalVocabulary[name] {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.addLocked(name, now)
}

// refuse counts one refusal and says whether a log line is due: at most one a minute per reason,
// carrying how many refusals of that reason it stands for. The reason returned is the vocabulary
// code actually counted.
func (g *forecastSignals) refuse(reason string, now time.Time) (code string, n int64, logIt bool) {
	if !validForecastRefusalReason(reason) {
		reason = "other"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.addLocked(sigRefusedPrefix+reason, now)
	if g.logged == nil {
		g.logged = map[string]forecastRefusalLog{}
	}
	entry := g.logged[reason]
	entry.since++
	if !entry.at.IsZero() && now.Sub(entry.at) < forecastRefusalLogEvery {
		g.logged[reason] = entry
		return reason, 0, false
	}
	g.logged[reason] = forecastRefusalLog{at: now}
	return reason, entry.since, true
}

// drain takes everything counted so far, leaving the counters empty, and drops days that are already
// outside the retention window (they would be pruned the moment they were written).
func (g *forecastSignals) drain(now time.Time) map[string]map[string]int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.counts
	g.counts = nil
	cutoff := forecastSignalsCutoff(now)
	for day := range out {
		if day < cutoff {
			delete(out, day)
		}
	}
	return out
}

// restore puts back a delta that could not be written, so the next flush carries it.
func (g *forecastSignals) restore(day string, delta map[string]int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.counts == nil {
		g.counts = map[string]map[string]int64{}
	}
	if g.counts[day] == nil {
		g.counts[day] = map[string]int64{}
	}
	for name, n := range delta {
		g.counts[day][name] += n
	}
}

func forecastSignalsCutoff(now time.Time) string {
	return now.UTC().AddDate(0, 0, -forecastSignalsRetentionDays).Format("2006-01-02")
}

// flush persists what was counted since the last successful flush and prunes rows past retention.
// The first error is returned; a day that could not be written stays in memory for the next call.
func (g *forecastSignals) flush(st *Store, now time.Time) error {
	g.flushMu.Lock()
	defer g.flushMu.Unlock()
	pending := g.drain(now)
	days := make([]string, 0, len(pending))
	for day := range pending {
		days = append(days, day)
	}
	sort.Strings(days)
	var first error
	for _, day := range days {
		if err := st.addForecastSignalDay(day, pending[day]); err != nil {
			g.restore(day, pending[day])
			if first == nil {
				first = fmt.Errorf("forecast signals %s: %w", day, err)
			}
		}
	}
	if err := st.pruneForecastSignals(now); err != nil && first == nil {
		first = fmt.Errorf("forecast signals prune: %w", err)
	}
	return first
}

// decodeForecastSignalDay accepts only a flat JSON object of non-negative integers.
func decodeForecastSignalDay(raw string) (map[string]int64, error) {
	var out map[string]int64
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("not an object")
	}
	for name, n := range out {
		if n < 0 {
			return nil, fmt.Errorf("negative count for %s", name)
		}
	}
	return out, nil
}

// addForecastSignalDay adds a delta to one day's stored row. A row that cannot be read is left
// exactly as it is and reported, never overwritten: the caller keeps the delta.
func (st *Store) addForecastSignalDay(day string, delta map[string]int64) error {
	key := forecastSignalsKey(day)
	var raw string
	stored := map[string]int64{}
	switch err := st.queryRow("SELECT v FROM meta WHERE k=?", key).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		var derr error
		if stored, derr = decodeForecastSignalDay(raw); derr != nil {
			return fmt.Errorf("stored row is damaged: %w", derr)
		}
	}
	for name, n := range delta {
		stored[name] += n
	}
	out, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	return st.SetSetting(key, string(out))
}

// pruneForecastSignals deletes day rows older than the retention window. A key under the prefix
// whose suffix is not a date is not ours to delete.
func (st *Store) pruneForecastSignals(now time.Time) error {
	rows, err := st.query("SELECT k FROM meta WHERE k LIKE ?", forecastSignalsKeyPrefix+"%")
	if err != nil {
		return err
	}
	var stale []string
	cutoff := forecastSignalsCutoff(now)
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		day := strings.TrimPrefix(k, forecastSignalsKeyPrefix)
		if day == k {
			continue
		}
		if _, perr := time.Parse("2006-01-02", day); perr == nil && day < cutoff {
			stale = append(stale, k)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, k := range stale {
		if err := st.DeleteSetting(k); err != nil {
			return err
		}
	}
	return nil
}

// forecastSignalsLoop flushes the counters every hour. The rows are per UTC day, so the hourly
// cadence only bounds what a crash can lose; it does not split a day.
func (s *Server) forecastSignalsLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	next := time.Now().UTC().Truncate(time.Hour).Add(time.Hour)
	for now := range ticker.C {
		if now.Before(next) {
			continue
		}
		if err := s.forecastSignals.flush(s.st, s.forecastEval.clock()); err != nil {
			log.Printf("forecast signals: flush failed: %v", err)
		}
		next = now.UTC().Truncate(time.Hour).Add(time.Hour)
	}
}

// forecastSignal counts one event on the process counters.
func (s *Server) forecastSignal(name string) {
	s.forecastSignals.add(name, s.forecastEval.clock())
}
