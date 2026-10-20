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
	// adjust is the front-adjusted fetch seam. It is the ONE qfq call (ADR 0029 §10) and exists only
	// to detect a bonus or rights issue the unadjusted body does not name.
	adjust func(ctx context.Context, market, code, from, to string) (map[string]int64, error)

	// tickFn is a test seam for step's panic recovery. Nil means e.tick.
	tickFn func(*Server) error

	// Per-pass state, valid only while this evaluator holds the single-flight guard (loop, watchdog
	// and the manual route all go through tryStart). Dry mode lives here: planWrites is the one
	// question every write asks.
	passMode     string
	stats        *passStats
	gate         map[string]bool
	bench        map[string]benchStat
	drySample    []drySample
	awaitingGate map[string]bool
}

// loop is the ticker. Every minute it asks whether it should run at all, then whether anything is
// due; both answers come from the database, so a restart resumes without special handling.
func (e *forecastEvaluator) loop(s *Server) {
	for range time.Tick(time.Minute) {
		// step reads the switch first: a disabled evaluator does no work whatsoever — no query, no
		// fetch, no write. A loop that checked a setting after touching the store would make "off"
		// mean "quiet" rather than "off".
		e.step(s, "tick")
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
	if e.stats != nil {
		e.stats.Sets = len(sets)
	}
	if len(sets) == 0 {
		return nil
	}
	sets = e.quarantineBadWindows(s, sets)
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
					if err := e.applyPlan(s, set, forecastSeries{}, plan, e.clock()); err != nil {
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
				if err := e.applyPlan(s, set, forecastSeries{}, plan, e.clock()); err != nil {
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
		if fault != nil {
			log.Printf("forecast evaluator: benchmark %s: %v", spec.Code, fault.err)
			for _, set := range need {
				if err := ctx.Err(); err != nil {
					return nil
				}
				if err := e.evaluateSet(ctx, s, set, indexSeries, fault, forecastSeries{}, nil, false); err != nil {
					log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
				}
			}
			continue
		}
		// Fetch every stock body before settling any of them: the stale valve asks whether the
		// VENDOR's whole feed lags, which is a property of this pass and cannot be answered one
		// series at a time.
		stocks := make([]forecastSeries, len(need))
		stockErrs := make([]error, len(need))
		for i, set := range need {
			if err := ctx.Err(); err != nil {
				return nil
			}
			stocks[i], stockErrs[i] = e.rangeBars(ctx, s, set.Market, set.Symbol,
				shiftDays(set.MadeOn, -predReferenceLagDays), e.clock())
			e.noteFetch(set.Market, set.Symbol, stocks[i].Source)
		}
		valve := staleValve(stocks, stockErrs, completedSessions(indexSeries, e.clock()))
		if e.stats != nil {
			e.stats.StaleValveTripped = valve
		}
		for i, set := range need {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if err := e.evaluateSet(ctx, s, set, indexSeries, nil, stocks[i], stockErrs[i], valve); err != nil {
				log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
			}
		}
	}
	return nil
}

// storedS0Reproduced reports whether the set's stored start is what predS0 derives from this
// calendar. An empty stored start is not a contradiction.
func storedS0Reproduced(stored string, madeAt time.Time, sessions []string) bool {
	if stored == "" {
		return true
	}
	want, ok := predS0(madeAt, sessions)
	return !ok || want == stored
}

// quarantineBadWindows refuses the sets whose receipt date cannot build a vendor window, before any
// URL exists. The window is quoteParseWindow's, so a date it would reject is never sent.
func (e *forecastEvaluator) quarantineBadWindows(s *Server, sets []forecastWorkSet) []forecastWorkSet {
	out := sets[:0]
	for _, set := range sets {
		if _, err := quoteParseWindow(shiftDays(set.MadeOn, -predReferenceLagDays),
			e.clock().In(chinaZone).Format("2006-01-02")); err == nil {
			out = append(out, set)
			continue
		}
		plan := forecastPlan{setID: set.SetID, wakes: map[int64]time.Time{}}
		for _, m := range set.Marks {
			plan.deferMark(m, "bad_window", "")
		}
		if err := e.applyPlan(s, set, forecastSeries{}, plan, e.clock()); err != nil {
			log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
		}
	}
	// nil them out so the caller's slice cap cannot retain a quarantined set's data
	for i := len(out); i < len(sets); i++ {
		sets[i] = forecastWorkSet{}
	}
	return out
}

// staleValve reports whether more than 20% of this pass's stock bodies end before the calendar's
// newest completed session. That is a vendor-wide lag, not a halt: nothing may be written and no
// attempts may move (V26).
func staleValve(stocks []forecastSeries, errs []error, sessions []string) bool {
	if len(sessions) == 0 || len(stocks) == 0 {
		return false
	}
	newest := sessions[len(sessions)-1]
	stale := 0
	valid := 0
	for i := range stocks {
		if errs[i] != nil || len(stocks[i].Bars) == 0 {
			continue
		}
		valid++
		if stocks[i].Bars[len(stocks[i].Bars)-1].Date < newest {
			stale++
		}
	}
	return valid > 0 && stale*5 > valid
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
	e.noteFetch(spec.Market, spec.Sym, series.Source)
	if err != nil {
		// Do NOT overwrite the last known bar with an empty series: the stale-calendar check must
		// keep seeing the age of the last body that arrived, not the moment a fetch failed.
		return series, &benchFault{code: "fetch_failed", err: err}
	}
	e.noteBench(spec.Code, series)
	if len(series.Bars) == 0 {
		// An index publishes a bar on every session. An empty body is a delivery fault for the whole
		// group, never a fact about one stock's series.
		return series, &benchFault{code: "no_bench",
			err: fmt.Errorf("benchmark %q returned no bars", spec.Code)}
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
func (e *forecastEvaluator) evaluateSet(ctx context.Context, s *Server, set forecastWorkSet, indexSeries forecastSeries, fault *benchFault, stockSeries forecastSeries, stockErr error, valve bool) error {
	now := e.clock()
	plan := forecastPlan{setID: set.SetID, wakes: map[int64]time.Time{}}

	if fault != nil {
		// The calendar is a shared dependency: without it no checkpoint date can be trusted. This is
		// a SOURCE failure — it leaves attempts unchanged and never counts toward finality. A mark
		// whose earliest possible due is still in the future is rescheduled rather than marked
		// refused: a vendor outage is not a fact about a checkpoint that cannot be due yet.
		for _, m := range set.Marks {
			if wake := predWakeAt(set, m.Checkpoint); !wake.IsZero() && now.Before(wake) {
				plan.wakes[m.ID] = wake
				continue
			}
			plan.deferMark(m, fault.code, "")
		}
		return e.applyPlan(s, set, stockSeries, plan, now)
	}

	if stockErr != nil {
		// The stock's own series failed to arrive. Also a source failure: the run was not scored
		// against a wrong price, it simply has not been delivered yet. Only marks that could be due
		// are refused; a far-off checkpoint keeps its scheduled wake.
		for _, m := range set.Marks {
			if wake := predWakeAt(set, m.Checkpoint); !wake.IsZero() && now.Before(wake) {
				plan.wakes[m.ID] = wake
				continue
			}
			plan.deferMark(m, "fetch_failed", "")
		}
		return e.applyPlan(s, set, stockSeries, plan, now)
	}
	if len(stockSeries.Bars) == 0 && stockSeries.UnverifiableDate == "" {
		// A range endpoint answers history: an empty stock body is a delivery fault, not "this stock
		// never traded". Charging it as no_base would let a source outage finalise the set.
		for _, m := range set.Marks {
			if wake := predWakeAt(set, m.Checkpoint); !wake.IsZero() && now.Before(wake) {
				plan.wakes[m.ID] = wake
				continue
			}
			plan.deferMark(m, "fetch_failed", "")
		}
		return e.applyPlan(s, set, stockSeries, plan, now)
	}
	indexBars, stockBars, exDates := indexSeries.Bars, stockSeries.Bars, stockSeries.ExDates
	seriesFrom := shiftDays(set.MadeOn, -predReferenceLagDays)
	// The BENCHMARK's completed sessions, so a halted stock cannot slide its own checkpoints. The
	// completion guard is what keeps today's unfinished bar out.
	sessions := completedSessions(indexSeries, now)

	madeAt, ok := setMadeAt(set)
	if !ok {
		return fmt.Errorf("set %d has no readable made_at (%q)", set.SetID, set.MadeAt)
	}

	// Self-consistency checks on the two bodies. The index-wide faults (zero volume, a stored
	// s0_date this calendar does not reproduce) are SOURCE class: they concern every mark in the set
	// and must never make one final.
	if hasZeroVolumeBar(indexBars) || !storedS0Reproduced(set.S0Date, madeAt, sessions) {
		for _, m := range set.Marks {
			// A mark whose earliest possible due is still in the future is rescheduled, not
			// refused: an index fault is not a fact about a checkpoint that cannot be due yet.
			if wake := predWakeAt(set, m.Checkpoint); !wake.IsZero() && now.Before(wake) {
				plan.wakes[m.ID] = wake
				continue
			}
			plan.deferMark(m, "calendar_bad", "")
		}
		return e.applyPlan(s, set, stockSeries, plan, now)
	}

	// A terminal baseline is used as STORED. Recomputing it on a later pass would let a revised
	// vendor body move the starting point of the set's whole return series. A NULL s0_fen on a
	// terminal row means the return cannot be computed; it is never a reason to recompute the price.
	settled := set.BaseCheck == baseMatch || set.BaseCheck == baseMismatch || set.BaseCheck == baseRefusedFinal
	var s0, baseDate string
	var s0Fen int64
	var err error
	if settled {
		s0, baseDate = set.S0Date, set.BaseDate
		if set.S0Fen != nil {
			s0Fen = *set.S0Fen
		}
	} else {
		// The baseline check is not a verdict on the set's honesty alone: it decides whether the
		// emitted quote is a price the market printed. A mismatch still has a start session, but the
		// set is excluded from the metrics (design §9.4).
		check, ready := predBaseCheck(madeAt, set.EmitFen, stockBars, sessions, stockSeries.UnverifiableDate)
		if !ready {
			// The reference bar is not complete yet. Not due: keep every mark waiting.
			for _, m := range set.Marks {
				wake := predWakeAt(set, m.Checkpoint)
				if wake.IsZero() || !now.Before(wake) {
					wake = predNotYetShown(now)
				}
				plan.wakes[m.ID] = wake
			}
			return e.applyPlan(s, set, stockSeries, plan, now)
		}
		s0, baseDate, s0Fen, err = resolveStartBar(stockBars, set, madeAt, sessions)
		if err == errNoBaseInWindow {
			// One bounded look-back: a stock halted for months still has a price before S0, and the
			// reference window is only ten days wide.
			wide, werr := e.rangeBars(ctx, s, set.Market, set.Symbol, shiftDays(set.MadeOn, -400), now)
			if werr != nil {
				// The look-back is a delivery fault, not this set's data: never charge the attempt.
				for _, m := range set.Marks {
					plan.deferMark(m, "fetch_failed", "")
				}
				return e.applyPlan(s, set, stockSeries, plan, now)
			}
			if wideIdx, ierr := e.rangeBars(ctx, s, indexSeries.Market, indexSeries.Code,
				shiftDays(set.MadeOn, -400), now); ierr == nil {
				indexBars = wideIdx.Bars
				indexSeries.Kind = wideIdx.Kind
			}
			{
				wideSet := set
				wideSet.S0Date = s0
				// The look-back is bounded but must reach the bars it fetched: widen the floor too.
				wideSet.MadeOn = shiftDays(set.MadeOn, -400)
				s0, baseDate, s0Fen, err = resolveStartBar(wide.Bars, wideSet, madeAt, sessions)
				if err == nil {
					// Settle from the same body the base was found in: the narrow window has no
					// traded bar at all, so using it would refuse the very halt we just resolved.
					stockBars, exDates = wide.Bars, wide.ExDates
					seriesFrom = shiftDays(set.MadeOn, -400)
					stockSeries.AsOf, stockSeries.Source = wide.AsOf, wide.Source
					stockSeries.UnverifiableDate = wide.UnverifiableDate
				}
			}
		}
		if err == nil {
			plan.baseline = &forecastBaseline{S0: s0, BaseDate: baseDate, S0Fen: s0Fen, Check: check, Index: indexBars}
		}
	}
	if err != nil {
		if s0 == "" {
			// The calendar has not shown a session after the receipt yet (holiday, vendor lag). That
			// is not due and not a refusal: keep every mark waiting.
			for _, m := range set.Marks {
				wake := predWakeAt(set, m.Checkpoint)
				if wake.IsZero() {
					plan.finals = append(plan.finals, forecastFinal{markID: m.ID, code: "no_schedule"})
					continue
				}
				if !now.Before(wake) {
					wake = predNotYetShown(now)
				}
				plan.wakes[m.ID] = wake
			}
			return e.applyPlan(s, set, stockSeries, plan, now)
		}
		// The start session exists but nothing traded in the reference window (nor in the bounded
		// look-back). This is the set's own data, so it is a MARK-SPECIFIC failure and does count as
		// an attempt; it only becomes terminal after the fixed clock has run out AND those attempts
		// exist. The session is recorded, so the one-shot heal never mistakes this for a pre-P1
		// refusal. A stock with a bar at/after S0 but none at a checkpoint is "no_bar" later.
		code := "no_base"
		if err != errNoBaseInWindow {
			code = "no_bar"
		}
		for _, m := range set.Marks {
			plan.deferMark(m, code, s0)
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
		return e.applyPlan(s, set, stockSeries, plan, now)
	}
	// From here on the baseline IS resolved (either stored or written in this pass), so a not-due
	// mark's wake is its real projected due, not S0's close plus ten minutes.
	set.S0Date = s0
	set.BaseCheck = baseMatch

	// Collect the due marks first: the adjustment check is one vendor call per set per tick, made
	// only when at least one mark's stock leg is actually resolvable now.
	type dueMark struct {
		mark forecastMark
		due  string
	}
	var due []dueMark
	for _, mark := range set.Marks {
		if mark.Status == markResolved || mark.Status == markRefusedFinal {
			continue
		}
		if mark.Status == markRefused && mark.NextAttemptAt != "" {
			if t, err := time.Parse(time.RFC3339, mark.NextAttemptAt); err == nil && now.Before(t) {
				continue
			}
		}
		sch := predScheduleFor(mark.Checkpoint, madeAt, set.HorizonStd, set.S0Date, sessions)
		if sch.Known && containsDate(sessions, sch.Due) {
			due = append(due, dueMark{mark: mark, due: sch.Due})
		}
	}
	// The adjustment check is made only for a mark whose stock leg can actually resolve: a set whose
	// whole batch is waiting on a missing bar must not spend a vendor call on it (plan §5.4).
	needAdj := false
	for _, d := range due {
		bar, ok := lastBarAtOrBefore(stockBars, d.due)
		if ok && bar.Date >= baseDate && barOn(indexBars, baseDate) != nil && barOn(indexBars, d.due) != nil {
			needAdj = true
			break
		}
	}
	adj := map[string]int64(nil)
	adjChecked := false
	if needAdj {
		last := due[0].due
		for _, d := range due {
			if d.due > last {
				last = d.due
			}
		}
		fetch := e.adjust
		if fetch == nil {
			fetch = fetchTencentAdjustRatio
		}
		var aerr error
		adj, aerr = fetch(ctx, set.Market, set.Symbol, baseDate, last)
		if aerr != nil {
			// The adjustment check is part of the answer: the mark waits, it is never resolved
			// without it (F10). A source failure, so no attempt is charged.
			for _, d := range due {
				e.noteDueUnsettled(d.due, now)
				plan.deferMark(d.mark, "fetch_failed", d.due)
			}
			return e.applyPlan(s, set, stockSeries, plan, now)
		}
		adjChecked = true
	}

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
			if wake.IsZero() {
				// An unknown checkpoint (or h12 on a non-standard horizon) can never be scheduled.
				// It cannot occur from this build's own marks, but a corrupt row must not spin.
				plan.finals = append(plan.finals, forecastFinal{markID: mark.ID, code: "no_schedule"})
				continue
			}
			if !now.Before(wake) {
				wake = predNotYetShown(now)
			}
			plan.wakes[mark.ID] = wake
			continue
		}
		res := settleMark(settleInput{
			Set: set, Mark: mark, Stock: stockBars, Index: indexBars, ExDates: exDates,
			AsOf: stockSeries.AsOf, UnverifiableDate: stockSeries.UnverifiableDate,
			Source: stockSeries.Source, Sessions: sessions,
			S0: s0, BaseDate: baseDate, S0Fen: s0Fen, Session: sch.Record, SeriesFrom: seriesFrom,
			Adj: adj, AdjChecked: adjChecked, Now: now, Valve: valve,
		})
		if res.Status == markResolved {
			plan.settles = append(plan.settles, forecastSettle{markID: mark.ID, res: res})
			continue
		}
		e.noteDueUnsettled(sch.Due, now)
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
	return e.applyPlan(s, set, stockSeries, plan, now)
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
	out := forecastSeries{ExDates: resp.ExDates, Kind: resp.Kind,
		AsOf: resp.Snapshot.AsOf, Source: resp.BarsSource, Market: market, Code: code}
	if out.Source == "" {
		out.Source = resp.Source
	}
	// The newest bar is dropped unless the vendor's own stamp says its session has closed AND the
	// wall clock agrees. A body with no stamp cannot vouch for a bar dated today; that bar is marked
	// unverifiable rather than silently believed.
	if n := len(resp.Bars); n > 0 {
		final, unverifiable := newestBarFinal(resp.Bars[n-1], resp.Snapshot.AsOf, now)
		if final {
			out.Bars = resp.Bars
		} else {
			out.Bars = resp.Bars[:n-1]
		}
		if unverifiable {
			out.UnverifiableDate = resp.Bars[n-1].Date
		}
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
	// AsOf is the vendor's own stamp, kept for the halt evidence and the completeness rule.
	AsOf string
	// UnverifiableDate is the newest bar's date when the body could not vouch for it, so a mark due
	// on that date refuses rather than sleeping on a bar that was dropped.
	UnverifiableDate string
	// Source names the vendor that answered, written onto every settled mark (F13).
	Source string
	// Market and Code are what produced this body, so a bounded look-back can widen the calendar too.
	Market, Code string
}

// ---------- sessions ----------

// completedSessions returns the benchmark session dates that have completed by now, ascending. A bar
// dated before today is complete; a bar dated today is complete only once the clock has passed the
// session stamp (the vendor's own partial bar was already dropped by rangeBars); a bar dated later
// never is. Sessions come from the benchmark, which has one on every exchange session including the
// days a stock is halted.
func completedSessions(cal forecastSeries, now time.Time) []string {
	today := now.In(chinaZone).Format("2006-01-02")
	stamp := predSessionStamp(now)
	var out []string
	for _, b := range cal.Bars {
		if b.Date > today {
			continue
		}
		if b.Date == today && !stamp {
			continue
		}
		out = append(out, b.Date)
	}
	sort.Strings(out)
	return out
}

// predSessionStamp reports whether the exchange's regular session has closed on this instant.
//
// It is a CLOCK check, and on its own it is not enough: the vendor's own stamp is checked too (see
// newestBarFinal). This one exists so a fetch made at 14:57 cannot be believed at 15:31 on the
// strength of the wall clock alone.
func predSessionStamp(now time.Time) bool {
	t := now.In(chinaZone)
	close := time.Date(t.Year(), t.Month(), t.Day(), predSessionStampMinute/60, predSessionStampMinute%60, 0, 0, chinaZone)
	return !t.Before(close)
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
	Source     string
}

// resolveStartBar picks S0 and the stock's base date.
//
// S0 is the first session whose close is after the receipt (predS0). base_date is the stock's close
// on S0 when it traded, otherwise its last traded close strictly before S0 inside the window: a
// stock halted on S0 has no price that day, and the report's own starting point is the last price
// the market printed for it. Returns errNoBaseInWindow when nothing inside the window traded.
func resolveStartBar(stock []QuoteBar, set forecastWorkSet, madeAt time.Time, sessions []string) (s0, baseDate string, s0Fen int64, err error) {
	s0 = set.S0Date
	if s0 == "" {
		var ok bool
		s0, ok = predS0(madeAt, sessions)
		if !ok {
			return "", "", 0, fmt.Errorf("no completed session decides the start yet")
		}
	}
	if bar := barOn(stock, s0); bar != nil {
		return s0, s0, bar.Close, nil
	}
	floor := ""
	if set.MadeOn != "" {
		floor = shiftDays(set.MadeOn, -predReferenceLagDays)
	}
	if bar, ok := lastBarInWindow(stock, floor, s0); ok {
		return s0, bar.Date, bar.Close, nil
	}
	// Return S0 even though the baseline cannot resolve: the caller needs it to decide whether the
	// fixed clock has run out before giving up on the set.
	return s0, "", 0, errNoBaseInWindow
}

// lastBarInWindow returns the last bar strictly before `before` and at or after `floor`. An empty
// floor means the whole series before the boundary.
func lastBarInWindow(bars []QuoteBar, floor, before string) (QuoteBar, bool) {
	var best QuoteBar
	found := false
	for _, b := range bars {
		if b.Date >= before || (floor != "" && b.Date < floor) {
			continue
		}
		if !found || b.Date > best.Date {
			best, found = b, true
		}
	}
	return best, found
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
func corporateActionBetween(symbol string, stock, index []QuoteBar, exDates []string, from, to, seriesFrom string) bool {
	for _, d := range exDates {
		if d > from && d <= to {
			return true
		}
	}
	// A series that begins at the fetch window's floor is a listing's first bars: they have no price
	// limit, so their moves are never a corporate action.
	listing := seriesFrom != "" && len(stock) > 0 && stock[0].Date >= seriesFrom
	for i, b := range stock {
		if b.Date <= from || b.Date > to {
			continue
		}
		if listing && i < 5 {
			continue
		}
		// A gap outside the board's fen-rounded limit prices cannot be trading, so it is a corporate
		// action — and the only signal that can catch a bonus issue the vendor told us nothing about.
		if prev := barBefore(stock, b.Date); prev != nil && prev.Close > 0 &&
			beyondLimitPrice(prev.Close, b.Close, predDailyLimitBp(symbol)) {
			return true
		}
	}
	return false
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
