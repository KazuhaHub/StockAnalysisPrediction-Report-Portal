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
func (e *forecastEvaluator) tick(s *Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sets, err := s.st.ForecastSetsNeedingWork(e.clock(), predEvalBatch)
	if err != nil {
		return fmt.Errorf("select due work: %w", err)
	}
	if len(sets) == 0 {
		return nil
	}
	// One index fetch per tick, shared by every set in it: the session calendar is the same series
	// for all of them, and fetching it per set would multiply the smallest possible burst by the
	// batch size.
	indexSeries, indexErr := e.indexCalendar(ctx, s, sets[0].BenchCode, sets)

	for _, set := range sets {
		if err := ctx.Err(); err != nil {
			return nil // out of time; the next tick continues from the rows
		}
		if err := e.evaluateSet(ctx, s, set, indexSeries, indexErr); err != nil {
			log.Printf("forecast evaluator: set %d: %v", set.SetID, err)
		}
	}
	return nil
}

// indexCalendar fetches the benchmark's daily bars once for the whole tick.
func (e *forecastEvaluator) indexCalendar(ctx context.Context, s *Server, bench string, sets []forecastWorkSet) (forecastSeries, error) {
	if bench == "" {
		bench = s.st.ForecastBench()
	}
	// An index is quoted on the exchange that publishes it: sh000852 is Shanghai.
	market, code := benchMarketCode(bench)
	if market == "" {
		return forecastSeries{}, fmt.Errorf("benchmark %q is not a six-digit code with a market", bench)
	}
	from := sets[0].MadeOn
	for _, set := range sets {
		if set.MadeOn < from {
			from = set.MadeOn
		}
	}
	return e.rangeBars(ctx, s, market, code, shiftDays(from, -predReferenceLagDays), e.clock())
}

// evaluateSet resolves every due mark of one set, then fills the set's own baseline check.
func (e *forecastEvaluator) evaluateSet(ctx context.Context, s *Server, set forecastWorkSet, indexSeries forecastSeries, indexErr error) error {
	if indexErr != nil {
		// The calendar is a shared dependency: without it no checkpoint date can be trusted, so the
		// whole set waits rather than guessing. Retryable, and the retry is recorded on the marks.
		return s.st.DeferForecastSet(set, "no_bar", indexErr)
	}
	now := e.clock()
	stockSeries, err := e.rangeBars(ctx, s, set.Market, set.Symbol, shiftDays(set.MadeOn, -predReferenceLagDays), now)
	if err != nil {
		return s.st.DeferForecastSet(set, "no_bar", err)
	}
	indexBars, stockBars, exDates := indexSeries.Bars, stockSeries.Bars, stockSeries.ExDates
	// Sessions strictly after the receipt date, using the BENCHMARK's calendar so a halted stock
	// cannot slide its own checkpoints. The cutoff is what keeps today's unfinished bar out.
	sessions := completedSessionsAfter(indexBars, set.MadeOn, now)

	s0, baseDate, s0Fen, err := resolveStartBar(stockBars, indexBars, set, now)
	if err != nil {
		return s.st.DeferForecastSet(set, "no_bar", err)
	}

	for _, mark := range set.Marks {
		if mark.Status == markResolved || mark.Status == markRefusedFinal {
			continue // terminal, and the guard would refuse the write anyway
		}
		session, due := checkpointSession(mark.Checkpoint, set, sessions)
		if !due {
			continue
		}
		// Finality: a refusal that has stood for long enough stops being retried, which is what
		// makes every input of a mature cell terminal and therefore makes the cell immutable.
		if mark.Status == markRefused && sessionsPastDue(sessions, session, now) >= predRetryDeadlineTicks {
			if err := s.st.MarkRefusedFinal(mark.ID); err != nil {
				return err
			}
			continue
		}
		res := settleMark(set, mark, stockBars, indexBars, exDates, s0, baseDate, s0Fen, session)
		if err := s.st.SettleForecastMark(mark.ID, res); err != nil {
			return err
		}
	}
	return s.st.FinishForecastBaseline(set, s0, baseDate, s0Fen, indexBars)
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
	out := forecastSeries{ExDates: resp.ExDates}
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
		return "", "", 0, fmt.Errorf("no traded bar on or after %s", s0)
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

// benchMarketCode splits a benchmark code into the market and the code the quote stack expects.
func benchMarketCode(bench string) (string, string) {
	b := strings.TrimSpace(bench)
	// An explicit market prefix is accepted and stripped. The setting stores the prefixed form
	// ("sh000852") because that is what a human types and what the default is, but the quote stack
	// wants them apart — and a validator that rejected its own default's spelling would be a
	// validation bug that only shows up when someone saves the value it shipped with.
	switch {
	case strings.HasPrefix(b, "sh") || strings.HasPrefix(b, "sz"):
		b = b[2:]
	}
	if len(b) != 6 {
		return "", ""
	}
	for i := 0; i < len(b); i++ {
		if b[i] < '0' || b[i] > '9' {
			return "", ""
		}
	}
	switch marketPrefix(b) {
	case "sh":
		return "sh", b
	case "sz":
		return "sz", b
	default:
		// Indices are published by the exchange whose marketPrefix would put them elsewhere: 000852
		// (CSI 1000) is a Shanghai index carrying Shenzhen's prefix. Index codes go to Shanghai.
		if strings.HasPrefix(b, "000") || strings.HasPrefix(b, "399") {
			return "sh", b
		}
		return "", ""
	}
}
