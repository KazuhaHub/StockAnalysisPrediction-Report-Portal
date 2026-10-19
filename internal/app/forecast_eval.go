package app

// Forecast verification: the evaluator (ADR 0029 §6, plan §6).
//
// The evaluator turns a set's pending marks into conclusions. It is the part of this feature that
// reaches the network, so almost everything here is designed around two questions: WHEN a bar may be
// believed, and WHAT it means when the stock did not trade.
//
// The loop ships disabled. That is not caution about the feature — it is that a background loop
// calling a third party of its own choosing on every install, whether the operator wanted it or not,
// is the one pattern in this codebase that always carries a switch (geo_update.go opens with the same
// guard, and every ADR 0017 target ships off). There is a reader, an admin writer and a UI for it.
//
// Two rules below are worth reading before the code, because both are places where the obvious
// implementation is wrong:
//
//  1. A bar may resolve a mark only once its session has COMPLETED. Tencent serves today's unfinished
//     bar during trading, and a resolved mark is permanent, so believing an intraday bar freezes a
//     live price into the record. The gate is on the clock and on the vendor's own timestamp, and the
//     fetch bypasses the cache — a cached 14:57 body served at 15:31 would otherwise pass a
//     clock-only check.
//
//  2. Sessions are counted on the BENCHMARK's bars, not the stock's. A halted stock has no bar at
//     all, so counting on its own series would slide its checkpoint past the resumption and the halt
//     would never be noticed.

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// setForecastAuto turns the evaluator on. Off by default, like every other background loop that
	// reaches a third party.
	setForecastAuto = "forecast_auto"
	// setForecastBench is the benchmark for NEW sets (D1). Sets already written keep theirs: a set
	// scored against one index must not change index when an admin picks another, or every
	// cross-month comparison would span two baselines.
	setForecastBench       = "forecast_bench"
	predEvalBatch          = 50 // sets per tick, so one tick is bounded whoever is waiting
	predReferenceLagDays   = 10 // how far before made_on the fetch window opens
	predRetryDeadlineTicks = 10 // a refusal becomes final this many sessions after its due session
	predSessionCloseMinute = 15 * 60
	predSessionStampMinute = 15*60 + 30 // a session's bar is usable only after this
)

// forecastEvaluator is the loop's own state. It holds nothing that matters across a restart: due work
// is derived from the rows every tick, precisely so a restart resumes rather than forgets.
type forecastEvaluator struct {
	mu      sync.Mutex
	running bool
	// fetch is the market-data seam. Nil means the real vendor call; a test replaces it so the
	// evaluator's arithmetic and its session rules are testable without a network.
	fetch func(ctx context.Context, market, code, from, to string) (*QuoteResp, error)
	// now is the clock seam, for the same reason.
	now func() time.Time
}

// loop is the ticker. Every minute it asks whether it should run at all, then whether anything is
// due; both answers come from the database, so a restart resumes without special handling.
func (e *forecastEvaluator) loop(s *Server) {
	for range time.Tick(time.Minute) {
		// Read the switch FIRST, so a disabled evaluator does no work whatsoever — no query, no
		// fetch, no write. A loop that checked a setting after touching the store would make "off"
		// mean "quiet" rather than "off".
		if s.st.GetSetting(setForecastAuto, "0") != "1" {
			continue
		}
		if !e.tryStart() {
			continue
		}
		func() {
			defer e.finish()
			if err := e.tick(s); err != nil {
				log.Printf("forecast evaluator: %v", err)
			}
		}()
	}
}

func (e *forecastEvaluator) tryStart() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return false
	}
	e.running = true
	return true
}

func (e *forecastEvaluator) finish() {
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
}

func (e *forecastEvaluator) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// tick resolves everything that is due, in a bounded batch.
//
// It works per distinct FROZEN benchmark code, not per first set: sets frozen with different indexes
// are settled against their own index (F02), and a set whose code is empty or not in the allowlist is
// deferred rather than scored against whatever the setting happens to be — the current setting is
// never read here.
func (e *forecastEvaluator) tick(s *Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sets, err := s.st.ForecastSetsDue(e.clock(), predEvalBatch)
	if err != nil {
		return fmt.Errorf("select due work: %w", err)
	}
	if len(sets) == 0 {
		return nil
	}

	for _, group := range groupSetsByBench(sets) {
		if err := ctx.Err(); err != nil {
			return nil // out of time; the next tick continues from the rows
		}
		// Schedule first, and only fetch what the clock says could be due. A mark whose earliest
		// possible due is still in the future needs no calendar at all, so a Friday's batch of sets
		// costs nothing until Monday's close.
		need := make([]forecastWorkSet, 0, len(group.sets))
		for _, set := range group.sets {
			plan, handled := e.wakeOnlyPlan(set, e.clock())
			if handled {
				if len(plan.wakes) > 0 {
					if err := s.st.ApplyForecastPlan(plan, e.clock()); err != nil {
						log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
					}
				}
				continue
			}
			need = append(need, set)
		}
		if len(need) == 0 {
			continue
		}
		spec, ok := benchSpecFor(group.code)
		if !ok {
			// An empty or unknown frozen code: the whole group waits. This is a SOURCE-class
			// deferral — the run is not scored against the setting instead of its own index.
			for _, set := range need {
				plan := forecastPlan{setID: set.SetID, wakes: map[int64]time.Time{}}
				for _, m := range set.Marks {
					plan.deferMark(m, "no_bench", "")
				}
				if err := s.st.ApplyForecastPlan(plan, e.clock()); err != nil {
					log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
				}
			}
			continue
		}
		// One calendar fetch per distinct code, shared by every set frozen with it, so a tick of
		// fifty sets still costs one index body per benchmark in use. The fault, if any, names the
		// refusal code each case must record (fetch_failed / not_index / calendar_bad), so an operator
		// can tell a vendor outage from a wrong instrument from a broken calendar.
		indexSeries, fault := e.benchCalendar(ctx, s, spec, need)
		for _, set := range need {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if err := e.evaluateSet(ctx, s, set, indexSeries, fault); err != nil {
				log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
			}
		}
	}
	return nil
}

// wakeOnlyPlan schedules a set whose every non-terminal mark is safely in the future. It returns
// handled=false when at least one mark could be due, or a backoff has expired, so the caller fetches
// the calendar. With no non-terminal marks it is handled (nothing to do, nothing to fetch).
func (e *forecastEvaluator) wakeOnlyPlan(set forecastWorkSet, now time.Time) (forecastPlan, bool) {
	plan := forecastPlan{setID: set.SetID, wakes: map[int64]time.Time{}}
	for _, m := range set.Marks {
		if m.Status == markResolved || m.Status == markRefusedFinal {
			continue
		}
		if m.Status == markRefused && m.NextAttemptAt != "" {
			t, err := time.Parse(time.RFC3339, m.NextAttemptAt)
			if err != nil || !now.Before(t) {
				return forecastPlan{}, false // the backoff has expired: this set needs work
			}
			plan.wakes[m.ID] = t
			continue
		}
		wake := predWakeAt(set, m.Checkpoint)
		if wake.IsZero() || !now.Before(wake) {
			return forecastPlan{}, false
		}
		plan.wakes[m.ID] = wake
	}
	return plan, true
}

// benchFault is a benchmark series that cannot settle a mark, with the refusal code its cause maps to.
type benchFault struct {
	code string
	err  error
}

// benchCalendar fetches the group's benchmark series and checks it is usable as a calendar. A fetch
// failure is fetch_failed, a body the vendor does not call an index is not_index, and a zero-volume
// bar is calendar_bad — three source-class faults with three distinct codes, never one `no_bar`.
func (e *forecastEvaluator) benchCalendar(ctx context.Context, s *Server, spec benchSpec, sets []forecastWorkSet) (forecastSeries, *benchFault) {
	series, err := e.indexCalendar(ctx, s, spec, sets)
	if err != nil {
		return series, &benchFault{code: "fetch_failed", err: err}
	}
	if series.Kind != quoteKindIndex {
		// A body the vendor does not call an index cannot settle any mark: the caller would be
		// comparing a stock's close for the benchmark leg.
		return series, &benchFault{code: "not_index",
			err: fmt.Errorf("benchmark %q is not an index (vendor kind %q)", spec.Code, series.Kind)}
	}
	if hasZeroVolumeBar(series.Bars) {
		// An index publishes a bar on every session the exchange is open, and never one with zero
		// volume. A zero-volume bar means the series is broken for the whole group — an index-wide
		// fault — so the group waits rather than scoring against it.
		return series, &benchFault{code: "calendar_bad",
			err: fmt.Errorf("benchmark %q calendar has a zero-volume bar", spec.Code)}
	}
	return series, nil
}

// benchGroup is the due sets frozen with one benchmark code, in first-appearance order.
type benchGroup struct {
	code string
	sets []forecastWorkSet
}

// groupSetsByBench groups due sets by their frozen bench_code, preserving the order in which each
// code first appears so the tick is deterministic.
func groupSetsByBench(sets []forecastWorkSet) []benchGroup {
	var out []benchGroup
	idx := map[string]int{}
	for _, set := range sets {
		i, ok := idx[set.BenchCode]
		if !ok {
			i = len(out)
			idx[set.BenchCode] = i
			out = append(out, benchGroup{code: set.BenchCode})
		}
		out[i].sets = append(out[i].sets, set)
	}
	return out
}

// indexCalendar fetches one allowed benchmark's daily bars, once for the whole tick's group. The
// window opens before the group's earliest receipt so S0 and its base date are both inside the body.
func (e *forecastEvaluator) indexCalendar(ctx context.Context, s *Server, spec benchSpec, sets []forecastWorkSet) (forecastSeries, error) {
	from := sets[0].MadeOn
	for _, set := range sets {
		if set.MadeOn < from {
			from = set.MadeOn
		}
	}
	return e.rangeBars(ctx, s, spec.Market, spec.Sym, shiftDays(from, -predReferenceLagDays), e.clock())
}

// hasZeroVolumeBar reports whether any bar in a series has non-positive volume. Used only on an
// index series, where it is the signature of a broken body.
func hasZeroVolumeBar(bars []QuoteBar) bool {
	for _, b := range bars {
		if b.Volume <= 0 {
			return true
		}
	}
	return false
}

// evaluateSet resolves every due mark of one set, then fills the set's own baseline check.
//
// The shape is the P1 algorithm: schedule first (a mark that is not due is never a refusal), settle
// only when the due session has completed, and classify a failure before deciding whether it is an
// attempt at all. Every write lands in one transaction.
func (e *forecastEvaluator) evaluateSet(ctx context.Context, s *Server, set forecastWorkSet, indexSeries forecastSeries, fault *benchFault) error {
	now := e.clock()
	plan := forecastPlan{setID: set.SetID, wakes: map[int64]time.Time{}}

	if fault != nil {
		// The calendar is a shared dependency: without it no checkpoint date can be trusted. This is
		// a SOURCE failure — it leaves attempts unchanged and never counts toward finality.
		for _, m := range set.Marks {
			plan.deferMark(m, fault.code, "")
		}
		return s.st.ApplyForecastPlan(plan, now)
	}

	stockSeries, err := e.rangeBars(ctx, s, set.Market, set.Symbol, shiftDays(set.MadeOn, -predReferenceLagDays), now)
	if err != nil {
		// The stock's own series failed to arrive. Also a source failure: the run was not scored
		// against a wrong price, it simply has not been delivered yet.
		for _, m := range set.Marks {
			plan.deferMark(m, "fetch_failed", "")
		}
		return s.st.ApplyForecastPlan(plan, now)
	}
	indexBars, stockBars, exDates := indexSeries.Bars, stockSeries.Bars, stockSeries.ExDates
	// Sessions strictly after the receipt date, using the BENCHMARK's calendar so a halted stock
	// cannot slide its own checkpoints. The cutoff is what keeps today's unfinished bar out.
	sessions := completedSessionsAfter(indexBars, set.MadeOn, now)

	madeAt, err := time.Parse(time.RFC3339, set.MadeAt)
	if err != nil {
		madeAt, _ = time.ParseInLocation("2006-01-02", set.MadeOn, chinaZone)
	}

	s0, baseDate, s0Fen, err := resolveStartBar(stockBars, indexBars, set, now)
	if err != nil {
		// The baseline cannot be resolved yet. This is the set's own data, so it is a MARK-SPECIFIC
		// failure and does count as an attempt; it only becomes terminal after the fixed clock has
		// run out AND those attempts exist.
		for _, m := range set.Marks {
			plan.deferMark(m, "no_bar", "")
		}
		if s0 != "" && sessionsPastDue(sessions, s0, now) >= predRetryDeadlineTicks {
			min, first := 0, true
			for _, m := range set.Marks {
				a := m.Attempts + 1
				if first || a < min {
					min, first = a, false
				}
			}
			if !first && min >= predFinalMinAttempts {
				plan.terminalBaseline = true
			}
		}
		return s.st.ApplyForecastPlan(plan, now)
	}
	plan.baseline = &forecastBaseline{S0: s0, BaseDate: baseDate, S0Fen: s0Fen, Index: indexBars}

	for _, mark := range set.Marks {
		if mark.Status == markResolved || mark.Status == markRefusedFinal {
			continue // terminal, and the guard would refuse the write anyway
		}
		// A backoff that has not expired is not due, whatever the schedule says.
		if mark.Status == markRefused && mark.NextAttemptAt != "" {
			if t, err := time.Parse(time.RFC3339, mark.NextAttemptAt); err == nil && now.Before(t) {
				plan.wakes[mark.ID] = t
				continue
			}
		}
		sch := predScheduleFor(mark.Checkpoint, madeAt, set.HorizonStd, set.S0Date, sessions)
		if !sch.Known || !containsDate(sessions, sch.Due) {
			// Not due. Record a wake; never a refusal. A calendar that has not shown the session yet
			// is a delivery delay, not a bad prediction.
			wake := predWakeAt(set, mark.Checkpoint)
			if wake.IsZero() || !now.Before(wake) {
				wake = predNotYetShown(now)
			}
			plan.wakes[mark.ID] = wake
			continue
		}
		res := settleMark(set, mark, stockBars, indexBars, exDates, s0, baseDate, s0Fen, sch.Record)
		if res.Status == markResolved {
			plan.settles = append(plan.settles, forecastSettle{markID: mark.ID, res: res})
			continue
		}
		// Finality only as the result of a failed attempt (V26): the mark must have failed enough
		// times AND the fixed clock must have run out. A source failure never reaches this branch
		// because it never increments attempts.
		if refusalClassOf(res.Refusal) == refusalMarkSpecific &&
			mark.Attempts+1 >= predFinalMinAttempts &&
			sessionsPastDue(sessions, sch.Due, now) >= predRetryDeadlineTicks {
			plan.finals = append(plan.finals, forecastFinal{markID: mark.ID, code: res.Refusal})
			continue
		}
		plan.deferMark(mark, res.Refusal, sch.Due)
	}
	return s.st.ApplyForecastPlan(plan, now)
}

// containsDate reports whether a sorted session list contains a date.
func containsDate(dates []string, date string) bool {
	for _, d := range dates {
		if d == date {
			return true
		}
		if d > date {
			return false
		}
	}
	return false
}

// rangeBars fetches unadjusted daily bars, directly and without the shared cache.
//
// Bypassing the cache is not an optimisation, it is a correctness requirement in two directions: the
// cache can serve a body fetched during the trading session (whose last bar is unfinished), and every
// per-set window is a new cache key, so a batch of fifty sets would evict the entries the quote strip
// and the charts are using. The congestion control is kept — the upstream ceiling is shared with the
// readers, so this loop cannot crowd them out.
func (e *forecastEvaluator) rangeBars(ctx context.Context, s *Server, market, code, from string, now time.Time) (forecastSeries, error) {
	release, err := s.quotes.acquire(ctx)
	if err != nil {
		return forecastSeries{}, err
	}
	defer release()
	fetch := e.fetch
	if fetch == nil {
		fetch = fetchTencentRange
	}
	resp, err := fetch(ctx, market, code, from, now.In(chinaZone).Format("2006-01-02"))
	if err != nil {
		return forecastSeries{}, err
	}
	if resp == nil {
		return forecastSeries{}, fmt.Errorf("no response")
	}
	out := forecastSeries{ExDates: resp.ExDates, Kind: resp.Kind}
	// The last bar is dropped unless the vendor's own stamp says its session has closed AND the wall
	// clock agrees. Both checks, because either alone has a hole: the clock alone believes a body the
	// cache fetched at 14:57, and the vendor's stamp alone says nothing about a body from yesterday.
	last := ""
	if n := len(resp.Bars); n > 0 {
		last = resp.Bars[n-1].Date
	}
	if last != "" && !barIsComplete(resp, now) {
		out.Bars = resp.Bars[:len(resp.Bars)-1]
	} else {
		out.Bars = resp.Bars
	}
	return out, nil
}

// forecastSeries is one fetched series with the extra facts the evaluator needs from the same call.
type forecastSeries struct {
	Bars    []QuoteBar
	ExDates []string
	// Kind is the vendor's own instrument kind ("index"/"stock"), carried through so the benchmark
	// leg can refuse a body that is not an index rather than score against it (F01).
	Kind string
}

// ---------- sessions ----------

// completedSessionsAfter returns the dates of sessions strictly after madeOn that have completed by
// now: the date itself is included once its close has passed, and today's bar is excluded until then.
//
// The bars come from the benchmark, which has one on every session the exchange is open — including
// the days a stock is halted, which is the whole reason they are the calendar.
func completedSessionsAfter(indexBars []QuoteBar, madeOn string, now time.Time) []string {
	today := now.In(chinaZone).Format("2006-01-02")
	stamp := predSessionStamp(now)
	var out []string
	for _, b := range indexBars {
		if b.Date <= madeOn {
			continue
		}
		// A bar dated today is usable only after the close. A bar dated earlier is complete by
		// definition — the vendor would not have published it otherwise.
		if b.Date == today && !stamp {
			continue
		}
		if b.Date > today {
			continue
		}
		out = append(out, b.Date)
	}
	sort.Strings(out)
	return out
}

// predSessionStamp reports whether the exchange's regular session has closed on this instant.
//
// It is a CLOCK check, and on its own it is not enough: the vendor's own timestamp is checked too
// (see barIsComplete). This one exists so that a fetch made at 14:57 cannot be believed at 15:31 on
// the strength of the wall clock alone.
func predSessionStamp(now time.Time) bool {
	t := now.In(chinaZone)
	close := time.Date(t.Year(), t.Month(), t.Day(), predSessionStampMinute/60, predSessionStampMinute%60, 0, 0, chinaZone)
	return !t.Before(close)
}

// barIsComplete reports whether a vendor body may be believed for the date it claims.
//
// The vendor reports its own stamp, and on a live session it is the time of the last tick — the
// fixture captured at 14:57:03 carries exactly that. A body stamped before the close is a partial
// bar however late it is read, which is what makes a cached response safe to distrust.
func barIsComplete(resp *QuoteResp, now time.Time) bool {
	if resp == nil {
		return false
	}
	if !predSessionStamp(now) {
		return false
	}
	asOf := strings.TrimSpace(resp.Snapshot.AsOf)
	if asOf == "" {
		// A vendor that omits its stamp cannot vouch for the bar, so the clock is all there is. The
		// closed-market case is the one this still serves: outside the session the last bar is final.
		return !duringRegularSession(now)
	}
	t, err := time.Parse(time.RFC3339, asOf)
	if err != nil {
		return !duringRegularSession(now)
	}
	return !t.In(chinaZone).Before(time.Date(t.In(chinaZone).Year(), t.In(chinaZone).Month(), t.In(chinaZone).Day(),
		predSessionCloseMinute/60, predSessionCloseMinute%60, 0, 0, chinaZone))
}

// duringRegularSession reports whether the instant falls inside the exchange's continuous trading,
// lunch break included in the middle of it. Used only to decide whether a bar can be trusted when the
// vendor gives no timestamp of its own.
func duringRegularSession(now time.Time) bool {
	t := now.In(chinaZone)
	switch t.Weekday() {
	case time.Saturday, time.Sunday:
		return false
	}
	return quoteWeekday(t) && inRegularSession(t)
}

// inRegularSession reports whether the instant is inside the A-share continuous session, lunch break
// in the middle of it.
func inRegularSession(t time.Time) bool {
	minute := t.Hour()*60 + t.Minute()
	for _, span := range quoteRegularSpans("sh") {
		if minute >= span.startMinute && minute < span.endMinute {
			return true
		}
	}
	return false
}

// checkpointSession maps a checkpoint to the session it settles on, and whether that session has
// completed yet.
//
// dN counts N sessions after S0's session. h12 is calendar-based and lands on the first session on or
// after made_on + 12 months — and only standard-horizon sets have it at all, which is what keeps a
// caller-supplied six-month horizon out of the twelve-month column.
func checkpointSession(cp string, set forecastWorkSet, sessions []string) (string, bool) {
	if cp == "h12" {
		if !set.HorizonStd {
			return "", false
		}
		target := addMonths(set.MadeOn, 12)
		for _, s := range sessions {
			if s >= target {
				return s, true
			}
		}
		return target, false
	}
	n, ok := predCheckpointSessions[cp]
	if !ok {
		return "", false
	}
	if len(sessions) <= n {
		return "", false
	}
	return sessions[n-1], true
}

// sessionsPastDue counts how many completed sessions have passed since a mark's due session. Used for
// the finality deadline: a refusal that has stood for long enough is recorded as final rather than
// retried for ever.
func sessionsPastDue(sessions []string, due string, now time.Time) int {
	n := 0
	for _, s := range sessions {
		if s > due {
			n++
		}
	}
	return n
}

// ---------- settling ----------

// markResult is one mark's resolved state.
type markResult struct {
	Status     string
	Refusal    string
	Session    string
	BarDate    string
	Halted     bool
	CorpAction bool
	CloseFen   *int64
	LowFen     *int64
	HighFen    *int64
	RetBp      *int32
	BenchRetBp *int32
	Attempts   int
}

// settleMark computes a checkpoint's outcome from the two series.
//
// The stock's bar is the last one at or before the checkpoint's session, and it is taken at or after
// base_date: a bar printed before the report existed is not this report's outcome, and the fill
// window below uses the same boundary for the same reason.
func settleMark(set forecastWorkSet, mark forecastMark, stock, index []QuoteBar, exDates []string, s0, baseDate string, s0Fen int64, session string) markResult {
	res := markResult{Status: markResolved, Session: session, Attempts: mark.Attempts}

	bar, ok := lastBarAtOrBefore(stock, session)
	if !ok || bar.Date < baseDate {
		res.Status, res.Refusal = markRefused, "no_bar"
		return res
	}
	res.BarDate = bar.Date
	res.Halted = bar.Date < session // suspended or delisted: the last traded close is the answer

	closeFen := bar.Close
	res.CloseFen = &closeFen

	// The fill window is (base_date, session]. An empty one means nothing traded since the base, so
	// there is no low or high to report rather than a zero.
	if lo, hi, ok := lowHighBetween(stock, baseDate, session); ok {
		res.LowFen, res.HighFen = &lo, &hi
	}
	if s0Fen > 0 {
		ret := bpFromRatio(closeFen, s0Fen)
		res.RetBp = &ret
	}
	if b := barOn(index, baseDate); b != nil {
		if e := barOn(index, session); e != nil && b.Close > 0 {
			bench := bpFromRatio(e.Close, b.Close)
			res.BenchRetBp = &bench
		}
	}
	res.CorpAction = corporateActionBetween(set.Symbol, stock, index, exDates, baseDate, session)

	// A resumed or delisted stock keeps the halt flag; it never stays refused, because a refusal that
	// can never resolve is exactly what hollows out a cohort without showing it.
	return res
}

// resolveStartBar picks S0 and the stock's base date.
//
// They are two dates on purpose. S0 is the session whose close the report could not have known, so it
// is the first session strictly after the report was RECEIVED. base_date is the first session on or
// after S0 on which the stock actually traded, which is where the returns start: a stock halted on S0
// has no price that day, and using the benchmark's close for it would compare two different days.
func resolveStartBar(stock, index []QuoteBar, set forecastWorkSet, now time.Time) (s0, baseDate string, s0Fen int64, err error) {
	for _, b := range index {
		if b.Date <= set.MadeOn {
			continue
		}
		if b.Date > now.In(chinaZone).Format("2006-01-02") {
			break
		}
		s0 = b.Date
		break
	}
	if s0 == "" {
		return "", "", 0, fmt.Errorf("no session after %s yet", set.MadeOn)
	}
	bar, ok := firstBarAtOrAfter(stock, s0)
	if !ok {
		// Return S0 even though the baseline cannot resolve: the caller needs it to decide whether
		// the fixed clock has run out before giving up on the set.
		return s0, "", 0, fmt.Errorf("no traded bar on or after %s", s0)
	}
	return s0, bar.Date, bar.Close, nil
}

// ---------- corporate actions (D8) ----------

// corporateActionBetween reports whether an ex-event fell in this window.
//
// Two signals, because one is not enough:
//
//   - The dividend element the unadjusted feed carries on an ex-date names the event outright
//     ({"fh_sh":"3.8","djr":…,"cqr":…}). But it is a CASH-dividend element: across every fixture in
//     testdata/quote, no seventh element mentions 送, 转增 or 配股 — and a bonus issue is the case
//     that matters most, because it halves the unadjusted close.
//   - So a gap larger than the board's daily limit is the backstop, and the only signal that can
//     catch a 送转 nobody told us about. A one-day move beyond the limit cannot be trading, so it is
//     a corporate action. This is why the plan's original gap heuristic was rejected and this one is
//     not: that one fired on ordinary limit-down days, which ARE a real move.
//
// A flagged mark is excluded from the metrics and counted (ADR 0029 §9). No qfq fetch is needed.
func corporateActionBetween(symbol string, stock, index []QuoteBar, exDates []string, from, to string) bool {
	for _, d := range exDates {
		if d > from && d <= to {
			return true
		}
	}
	for _, b := range stock {
		if b.Date <= from || b.Date > to {
			continue
		}
		// A gap larger than the board's limit cannot be trading, so it is a corporate action — and it
		// is the only signal that can catch a 送转 the vendor told us nothing about.
		if beyondDailyLimit(symbol, stock, b) {
			return true
		}
	}
	return false
}

// beyondDailyLimit reports whether the move into this bar is larger than the board's limit allows.
func beyondDailyLimit(symbol string, bars []QuoteBar, bar QuoteBar) bool {
	prev := barBefore(bars, bar.Date)
	if prev == nil || prev.Close <= 0 {
		return false
	}
	limit := dailyLimitBp(symbol)
	changeBp := bpFromRatio(bar.Close, prev.Close)
	if changeBp < 0 {
		changeBp = -changeBp
	}
	return changeBp > limit
}

// dailyLimitBp is the price limit for the board a code belongs to, in basis points.
//
// 10% on the main boards, 20% on ChiNext (300xxx) and STAR (688xxx). The first day of a listing has
// no limit at all, but a set can only reference a symbol that already traded, so that case cannot
// arise here — and if it somehow did, a flagged mark is excluded rather than wrong.
func dailyLimitBp(symbol string) int32 {
	switch {
	case strings.HasPrefix(symbol, "300"), strings.HasPrefix(symbol, "688"):
		return 2000
	default:
		return 1000
	}
}

// ---------- series helpers ----------

func lastBarAtOrBefore(bars []QuoteBar, date string) (QuoteBar, bool) {
	var best QuoteBar
	found := false
	for _, b := range bars {
		if b.Date <= date && (!found || b.Date > best.Date) {
			best, found = b, true
		}
	}
	return best, found
}

func firstBarAtOrAfter(bars []QuoteBar, date string) (QuoteBar, bool) {
	var best QuoteBar
	found := false
	for _, b := range bars {
		if b.Date >= date && (!found || b.Date < best.Date) {
			best, found = b, true
		}
	}
	return best, found
}

func barOn(bars []QuoteBar, date string) *QuoteBar {
	for i := range bars {
		if bars[i].Date == date {
			return &bars[i]
		}
	}
	return nil
}

func barBefore(bars []QuoteBar, date string) *QuoteBar {
	var best *QuoteBar
	for i := range bars {
		if bars[i].Date < date && (best == nil || bars[i].Date > best.Date) {
			best = &bars[i]
		}
	}
	return best
}

// lowHighBetween returns the traded low and high over (from, to], and whether there was any bar at
// all. An empty window reports nothing rather than a price of zero.
func lowHighBetween(bars []QuoteBar, from, to string) (int64, int64, bool) {
	var lo, hi int64
	found := false
	for _, b := range bars {
		if b.Date <= from || b.Date > to {
			continue
		}
		if !found || b.Low < lo {
			lo = b.Low
		}
		if !found || b.High > hi {
			hi = b.High
		}
		found = true
	}
	return lo, hi, found
}

// bpFromRatio returns (to/from - 1) in basis points, EXACTLY, with integer arithmetic.
//
// A float here would be a threshold comparison that resolves differently on different platforms, in
// the one table whose whole purpose is being citable. rounded-scale integer division is exact for the
// range these numbers live in, and the rounding is half-away-from-zero so a tie is not silently
// biased toward zero.
func bpFromRatio(to, from int64) int32 {
	if from == 0 {
		return 0
	}
	// Prices are fen and non-negative, so `from` is positive and the sign of the result is the sign
	// of the numerator. Half is taken as an explicit positive value: `from/2` would carry `from`'s
	// sign and, on the negative branch, push the quotient the wrong way by one basis point.
	num := (to - from) * 10000
	half := from / 2
	if num >= 0 {
		return int32((num + half) / from)
	}
	return int32((num - half) / from)
}

func shiftDays(date string, days int) string {
	t, err := time.ParseInLocation("2006-01-02", date, chinaZone)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, days).Format("2006-01-02")
}

func addMonths(date string, months int) string {
	t, err := time.ParseInLocation("2006-01-02", date, chinaZone)
	if err != nil {
		return date
	}
	return t.AddDate(0, months, 0).Format("2006-01-02")
}
