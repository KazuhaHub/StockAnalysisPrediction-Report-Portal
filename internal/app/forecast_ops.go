package app

// Forecast verification: operating the evaluator (ADR 0029 §6, plan §6.1).
//
// The evaluator already knows how to settle a mark (forecast_eval.go). This file is about running it
// as a service: a mode that plans without writing, a heartbeat the page can read, an independent
// watchdog that notices when the loop has stopped, a status-only health route, a manual pass, and the
// readiness list that keeps the switch off until the whole chain has been exercised.
//
// Three shapes here are load-bearing:
//
//  1. `step` reads the mode FIRST and, when it is off, does exactly that one read. "Off" has to mean
//     off in the sense an operator means it — no query, no fetch, no write — because the alternative
//     is a loop that is merely quiet.
//  2. Dry mode runs the same planning code as a real pass and drops the plan at the write. That is
//     the only way the sample can be trusted: a separate "what would happen" path would drift.
//  3. The watchdog is a separate goroutine with its own recover. It must survive the evaluator
//     panicking, which is the case it exists for.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/version"
)

const (
	// setForecastEvalState is the heartbeat: written by every pass, read by the page, the readiness
	// list and the health route.
	setForecastEvalState = "forecast_eval_state"
	// setForecastDryGate records a hand-checked dry run per benchmark code. A code without one is
	// planned but never written, which is what keeps the first live sets from being scored against a
	// body nobody has compared with the exchange.
	setForecastDryGate = "forecast_dry_gate"
	// setForecastAlertState de-duplicates watchdog alerts: reason → last emission.
	setForecastAlertState = "forecast_alert_state"
	// setForecastAlertTestOKAt is written by P41 when a test alert reaches every configured channel.
	// P9 lists the readiness item; the writer arrives with the channels.
	setForecastAlertTestOKAt = "forecast_alert_test_ok_at"

	forecastModeOff = "0"
	forecastModeDry = "dry"
	forecastModeOn  = "1"

	// forecastStalledAfter is the heartbeat age at which the loop is no longer believed to be running.
	forecastStalledAfter = 10 * time.Minute
	// forecastLaggingAfter is how long a due mark may stay unsettled before it is an alert rather
	// than a slow vendor.
	forecastLaggingAfter = 6 * time.Hour

	forecastStallAlertEvery   = time.Hour
	forecastLaggingAlertEvery = 24 * time.Hour

	// forecastDrySampleMax bounds the sample the page renders; past twenty rows nobody reads it.
	forecastDrySampleMax = 20
)

// forecastMode normalises the stored switch. Anything an older or newer build wrote that is not one
// of the three modes reads as off, which is also how an older binary treats `dry`.
func forecastMode(v string) string {
	switch strings.TrimSpace(v) {
	case forecastModeOn:
		return forecastModeOn
	case forecastModeDry:
		return forecastModeDry
	default:
		return forecastModeOff
	}
}

// passStats is one pass, as the page and the watchdog read it.
type passStats struct {
	WaitingForBudget bool   `json:"waiting_for_budget,omitempty"`
	ResumeAt         string `json:"resume_at,omitempty"`
	ReusedBodies     int    `json:"reused_bodies,omitempty"`
	Kind             string `json:"kind"` // "tick" | "manual"
	StartedAt        string `json:"started_at"`
	FinishedAt       string `json:"finished_at"`
	Sets             int    `json:"sets"`
	// Fetches counts vendor bodies by source|market|code, so an operator can see WHO answered the
	// pass rather than only that it fetched.
	Fetches map[string]int `json:"fetches"`
	// Bodies is how many vendor bodies arrived — the count, not the census. readiness needs the one
	// question "did anything come back at all", and answering it from the census would mean parsing
	// keys, including the "unknown" bucket written when a request produced no answer.
	Bodies         int            `json:"bodies"`
	Resolved       int            `json:"resolved"`
	Halted         int            `json:"halted"`
	Refused        map[string]int `json:"refused"`
	RefusedByClass map[string]int `json:"refused_by_class"`
	Finalised      int            `json:"finalised"`
	Healed         int            `json:"healed"`
	Baselines      map[string]int `json:"baselines"`
	// AwaitingSession counts marks that are scheduled but not due. It is normal and large before the
	// first checkpoint; a number that never falls is not.
	AwaitingSession int `json:"awaiting_session"`
	// DueUnsettled counts marks whose due session has completed but which did not settle in this
	// pass, and how long ago the oldest one fell due.
	DueUnsettled              int    `json:"due_unsettled"`
	OldestDueUnsettledMinutes int    `json:"oldest_due_unsettled_minutes"`
	StaleValveTripped         bool   `json:"stale_valve_tripped"`
	Errors                    int    `json:"errors"`
	LastError                 string `json:"last_error,omitempty"`
	Panics                    int    `json:"panics"`
}

// drySample is one write a dry pass would have made, with the body it would have written it from.
// The market/code/source triple is the point: "planned" is not evidence that the right vendor
// answered for the right instrument.
type drySample struct {
	SetID  int64  `json:"set_id"`
	Mark   string `json:"mark,omitempty"`
	Kind   string `json:"kind"` // "settle" | "baseline" | "final"
	Market string `json:"market"`
	Code   string `json:"code"`
	Source string `json:"source,omitempty"`
	Due    string `json:"due,omitempty"`
}

// benchStat is one benchmark's newest bar, as the last pass saw it.
type benchStat struct {
	LastBar   string `json:"last_bar"`
	FetchedAt string `json:"fetched_at"`
	Source    string `json:"source"`
	Market    string `json:"market"`
	Code      string `json:"code"`
}

// forecastHeartbeat is the whole of what the evaluator has done lately. It lives in meta, so it
// survives a restart and a read-only page does not have to run a pass to show state.
type forecastHeartbeat struct {
	Mode       string               `json:"mode"`
	LastTickAt string               `json:"last_tick_at"`
	Pass       passStats            `json:"pass"`
	Bench      map[string]benchStat `json:"bench"`
	DrySample  []drySample          `json:"dry_sample,omitempty"`
	// AwaitingGate names benchmarks that were planned but not written because no dry-run check is
	// recorded for them. Only mode 1 can produce it.
	AwaitingGate []string `json:"awaiting_gate,omitempty"`
}

// forecastGate is one hand-checked dry run.
type forecastGate struct {
	At   string `json:"at"`
	By   string `json:"by"`
	Note string `json:"note"`
}

// readinessItem is one line of the §1.4 gate. Key is stable and the SPA translates it; Detail is the
// server's own words for a machine client and for the log.
type readinessItem struct {
	Key    string `json:"key"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	// Reason is a closed code for why a failing item fails, so the page can word it in the reader's
	// language; Detail stays as the English fallback for logs and older clients. Codes names the
	// benchmarks a gate item is missing, and Count carries a number the reason refers to.
	Reason string   `json:"reason,omitempty"`
	Codes  []string `json:"codes,omitempty"`
	Count  int      `json:"count,omitempty"`
}

// fail marks the item failing with a reason code and its English detail. Every reason goes through
// here, so TestReadinessReasonsAreTranslated can find each code and check the page can word it.
func (it *readinessItem) fail(reason, detail string) {
	it.OK, it.Reason, it.Detail = false, reason, detail
}

// step is one loop iteration. When the mode is off it makes exactly ONE settings read and nothing
// else; otherwise it runs one pass and writes the heartbeat, whatever happens inside.
//
// A dry mode TICK runs no pass at all (ADR 0037, amended 2026-10-24). Dry exists so an operator can
// look at what a pass would write; it is an operator action, not a background job, and a ticker that
// re-fetched the same due sets every minute was doing vendor work whose result was then discarded by
// the single write door. On a deployment holding a few hundred checkpoints that was thousands of
// vendor requests a day, which is what got the settlement endpoints rate-limited and then the
// display ones blocked by the same egress. The manual route (runForecastNow) still runs a pass, and
// that is the only way a dry pass happens now; nothing else about the mode changed.
func (e *forecastEvaluator) step(s *Server, kind string) {
	mode := forecastMode(s.st.GetSetting(setForecastAuto, "0"))
	if mode == forecastModeOff {
		return
	}
	if mode == forecastModeDry && kind == "tick" {
		return
	}
	if !e.tryStart() {
		return
	}
	defer e.finish()

	now := e.clock()
	st := &passStats{
		Kind:           kind,
		StartedAt:      now.UTC().Format(time.RFC3339),
		Fetches:        map[string]int{},
		Refused:        map[string]int{},
		RefusedByClass: map[string]int{},
		Baselines:      map[string]int{},
	}
	e.beginPass(s, mode, st)
	defer func() {
		if r := recover(); r != nil {
			st.Panics++
			st.LastError = fmt.Sprintf("panic: %v", r)
			log.Printf("forecast evaluator: panic: %v", r)
		}
		st.FinishedAt = e.clock().UTC().Format(time.RFC3339)
		if err := s.st.saveForecastHeartbeat(e.heartbeat(mode, st)); err != nil {
			log.Printf("forecast evaluator: heartbeat: %v", err)
		}
		e.endPass()
	}()

	tick := e.tick
	if e.tickFn != nil {
		tick = e.tickFn
	}
	if err := tick(s); err != nil {
		st.Errors++
		st.LastError = err.Error()
		log.Printf("forecast evaluator: %v", err)
	}
}

// beginPass installs the pass's mode and gate, and merges the previous heartbeat's benchmark lines
// so a pass that fetched nothing does not erase what is known about a code.
func (e *forecastEvaluator) beginPass(s *Server, mode string, st *passStats) {
	e.passMode = mode
	e.stats = st
	e.drySample = nil
	e.awaitingGate = map[string]bool{}
	e.bench = map[string]benchStat{}
	e.gate = map[string]bool{}
	if prev, ok := s.st.ForecastHeartbeat(); ok {
		for k, v := range prev.Bench {
			e.bench[k] = v
		}
	}
	if mode == forecastModeOn {
		for code := range s.st.ForecastDryGate() {
			e.gate[code] = true
		}
	}
}

func (e *forecastEvaluator) endPass() {
	e.stats = nil
	e.gate = nil
	e.bench = nil
	e.drySample = nil
	e.awaitingGate = nil
	e.passMode = ""
}

// heartbeat builds the stored shape from the pass's own state.
func (e *forecastEvaluator) heartbeat(mode string, st *passStats) forecastHeartbeat {
	awaiting := make([]string, 0, len(e.awaitingGate))
	for code := range e.awaitingGate {
		awaiting = append(awaiting, code)
	}
	sort.Strings(awaiting)
	hb := forecastHeartbeat{
		Mode:         mode,
		LastTickAt:   e.clock().UTC().Format(time.RFC3339),
		Pass:         *st,
		Bench:        e.bench,
		DrySample:    e.drySample,
		AwaitingGate: awaiting,
	}
	return hb
}

// planWrites answers whether this set's plan may land on the ledger. Dry mode never writes; mode 1
// writes only for a benchmark whose dry run has been checked by hand.
func (e *forecastEvaluator) planWrites(benchCode string) bool {
	switch e.passMode {
	case "":
		// No pass installed: this is a direct tick from a test or an internal caller, which has
		// always meant "write". step() and runForecastNow() always install a mode first.
		return true
	case forecastModeOn:
		return e.gate[benchCode]
	default:
		return false
	}
}

// applyPlan is the single write door of a pass. Every ledger write goes through it, so dry mode
// cannot forget one: there is nowhere else to write from.
func (e *forecastEvaluator) applyPlan(s *Server, set forecastWorkSet, stock forecastSeries, plan forecastPlan, now time.Time) error {
	e.countPlan(plan, now)
	if !e.planWrites(set.BenchCode) {
		if e.passMode == forecastModeOn {
			e.awaitingGate[set.BenchCode] = true
		}
		e.samplePlan(set, stock, plan)
		return nil
	}
	if err := s.st.ApplyForecastPlan(plan, now); err != nil {
		return err
	}
	if len(plan.settles) > 0 && len(plan.defers) == 0 {
		var remaining int
		if err := s.st.queryRow(`SELECT COUNT(*) FROM prediction_marks WHERE set_id=? AND status=?`, set.SetID, markRefused).Scan(&remaining); err == nil && remaining == 0 {
			s.st.clearForecastDeliveryFailure(set.SetID)
		}
	}
	return nil
}

// countPlan folds a plan's outcome into the pass's counters, whether or not it is written.
func (e *forecastEvaluator) countPlan(plan forecastPlan, now time.Time) {
	st := e.stats
	if st == nil {
		return
	}
	for _, settle := range plan.settles {
		st.Resolved++
		if settle.res.Halted {
			st.Halted++
		}
	}
	for _, f := range plan.finals {
		st.Finalised++
		st.Refused[f.code]++
		st.RefusedByClass[refusalClassOf(f.code)]++
	}
	for _, d := range plan.defers {
		st.Refused[d.code]++
		st.RefusedByClass[refusalClassOf(d.code)]++
	}
	if plan.baseline != nil {
		st.Baselines[plan.baseline.Check]++
	}
	st.AwaitingSession += len(plan.wakes)
}

// samplePlan records up to forecastDrySampleMax would-be writes: the resolved marks and the baseline
// are the values the ledger would have gained.
func (e *forecastEvaluator) samplePlan(set forecastWorkSet, stock forecastSeries, plan forecastPlan) {
	add := func(s drySample) {
		if len(e.drySample) >= forecastDrySampleMax {
			return
		}
		e.drySample = append(e.drySample, s)
	}
	base := drySample{SetID: set.SetID, Market: set.Market, Code: set.Symbol, Source: stock.Source}
	if plan.baseline != nil {
		b := base
		b.Kind = "baseline"
		add(b)
	}
	for _, settle := range plan.settles {
		s := base
		s.Kind = "settle"
		s.Mark = checkpointOf(set, settle.markID)
		s.Due = settle.res.Session
		add(s)
	}
	for _, f := range plan.finals {
		s := base
		s.Kind = "final"
		s.Mark = checkpointOf(set, f.markID)
		add(s)
	}
}

func checkpointOf(set forecastWorkSet, markID int64) string {
	for _, m := range set.Marks {
		if m.ID == markID {
			return m.Checkpoint
		}
	}
	return strconv.FormatInt(markID, 10)
}

// noteFetch records one vendor request in the pass's fetch census. An EMPTY source means no body
// arrived, which is exactly what readiness's dry_pass asks about, so that case counts as an attempt
// and not as a body.
func (e *forecastEvaluator) noteFetch(market, code, source string) {
	if e.stats == nil {
		return
	}
	if source == "" {
		e.stats.Fetches["unknown|"+market+"|"+code]++
		return
	}
	e.stats.Fetches[source+"|"+market+"|"+code]++
	e.stats.Bodies++
}

// noteBench records the newest bar of a benchmark body, for the stale-calendar check.
func (e *forecastEvaluator) noteBench(code string, series forecastSeries) {
	if e.bench == nil {
		return
	}
	last := ""
	if n := len(series.Bars); n > 0 {
		last = series.Bars[n-1].Date
	}
	fetched := series.FetchedAt
	if fetched.IsZero() {
		fetched = e.clock()
	}
	e.bench[code] = benchStat{
		LastBar:   last,
		FetchedAt: fetched.UTC().Format(time.RFC3339),
		Source:    series.Source,
		Market:    series.Market,
		Code:      code,
	}
}

// noteDueUnsettled records a mark whose due session has completed but which this pass did not
// settle, and how long ago that session closed.
func (e *forecastEvaluator) noteDueUnsettled(due string, now time.Time) {
	if e.stats == nil || due == "" {
		return
	}
	e.stats.DueUnsettled++
	t, err := time.ParseInLocation("2006-01-02", due, chinaZone)
	if err != nil {
		return
	}
	closed := t.Add(predSessionCloseMinute * time.Minute)
	mins := int(now.Sub(closed).Minutes())
	if mins < 0 {
		mins = 0
	}
	if mins > e.stats.OldestDueUnsettledMinutes {
		e.stats.OldestDueUnsettledMinutes = mins
	}
}

// ---------- heartbeat store ----------

func (s *Store) ForecastHeartbeat() (forecastHeartbeat, bool) {
	var hb forecastHeartbeat
	raw := s.GetSetting(setForecastEvalState, "")
	if raw == "" {
		return hb, false
	}
	if err := json.Unmarshal([]byte(raw), &hb); err != nil {
		return hb, false
	}
	return hb, true
}

func (s *Store) saveForecastHeartbeat(hb forecastHeartbeat) error {
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	return s.SetSetting(setForecastEvalState, string(body))
}

// ForecastDryGate returns the hand-checked dry runs, keyed by benchmark code.
func (s *Store) ForecastDryGate() map[string]forecastGate {
	out := map[string]forecastGate{}
	raw := s.GetSetting(setForecastDryGate, "")
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]forecastGate{}
	}
	return out
}

// RecordForecastDryGate stores one code's check. The caller has already validated the code and note.
func (s *Store) RecordForecastDryGate(code string, gate forecastGate) error {
	s.forecastGateMu.Lock()
	defer s.forecastGateMu.Unlock()
	all := s.ForecastDryGate()
	all[code] = gate
	body, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return s.SetSetting(setForecastDryGate, string(body))
}

// ForecastGateCodes is every benchmark the evaluator can select: the active code, plus every code
// frozen by an accepted set that still has a non-terminal mark (pre-live sets included, because the
// evaluator settles them like any other). With no accepted set at all the active code is still
// listed, so the item can never be vacuously satisfied.
func (s *Store) ForecastGateCodes() ([]string, error) {
	seen := map[string]bool{}
	if code := s.ForecastBench(); code != "" {
		seen[code] = true
	}
	rows, err := s.query(`SELECT DISTINCT ps.bench_code FROM prediction_sets ps
		JOIN prediction_marks m ON m.set_id = ps.id
		WHERE ps.state = ? AND m.status IN (?,?) AND ps.bench_code <> ''`,
		predStateAccepted, markPending, markRefused)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		// A code the evaluator can never select (an old or hand-edited row) is not a gate it can
		// record: the settings handler refuses such a code, so listing it would block readiness for
		// ever with an unfixable item.
		if _, ok := benchSpecFor(code); ok {
			seen[code] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out, nil
}

// ---------- readiness ----------

// forecastReadiness evaluates the operational gates, including configured ledger capabilities.
func (s *Server) forecastReadiness(now time.Time) []readinessItem {
	items := make([]readinessItem, 0, 6)

	// 1. This binary contains R1a and R1b, including the P1 refusal classification. True by
	// construction in any build with this file; listed so the page says which build it is.
	items = append(items, readinessItem{Key: "binary", OK: true, Detail: version.String()})

	// 2. The ledger start is set.
	_, populationSet := s.st.ForecastPopulationFrom()
	population := readinessItem{Key: "population", OK: populationSet}
	if !populationSet {
		population.fail("population_unset", "forecast_population_from is not set")
	}
	items = append(items, population)

	// 3. The mode is dry, and the last dry pass produced evidence: it finished without errors or
	// panics AND planned at least one write.
	//
	// "No error" alone was not enough, and the 2026-10-24 incident is why. A vendor outage is a SOURCE
	// fault: it charges no attempt and finalises nothing, which is correct, but it also raises no
	// error — so a pass in which every vendor request was refused finished clean, planned nothing, and
	// turned this item green. An operator read six ticks and switched the evaluator on over a vendor
	// that was answering 501. The sample is the evidence the whole dry mode exists to produce; a pass
	// that produced none has not been checked, whatever its error count.
	mode := forecastMode(s.st.GetSetting(setForecastAuto, "0"))
	hb, haveHB := s.st.ForecastHeartbeat()
	dryOK := mode == forecastModeDry && haveHB && hb.Mode == forecastModeDry &&
		hb.Pass.FinishedAt != "" && hb.Pass.Errors == 0 && hb.Pass.Panics == 0 &&
		len(hb.DrySample) > 0
	dry := readinessItem{Key: "dry_pass", OK: dryOK}
	switch {
	case mode != forecastModeDry:
		dry.fail("not_dry", "the evaluator is not in dry mode")
	case !haveHB || hb.Mode != forecastModeDry:
		// Switching 1 -> dry and straight back must not pass: the heartbeat is from the mode-1 run.
		dry.fail("no_dry_pass", "no pass has run since the mode was set to dry")
	case hb.Pass.FinishedAt == "":
		dry.fail("dry_pass_running", "no dry pass has finished yet")
	case hb.Pass.Errors > 0 || hb.Pass.Panics > 0:
		dry.Count = hb.Pass.Errors + hb.Pass.Panics
		dry.fail("dry_pass_errors", "the last dry pass reported errors or panics: "+strconv.Itoa(dry.Count))
	case len(hb.DrySample) == 0 && hb.Pass.Bodies == 0:
		// Due work and not one body came back. This is the incident: the vendor, or the egress to it,
		// is not answering. Switching on would plan the same empty pass every minute.
		dry.fail("dry_pass_no_source",
			"the last dry pass fetched no vendor body, so nothing could be planned; check the quote sources")
	case len(hb.DrySample) == 0:
		// The vendor answered and nothing was plannable: no due mark, or the marks it has cannot be
		// settled yet. A fact about the ledger, not about the vendor — and the next due session, or a
		// later manual pass, is what changes it.
		dry.fail("dry_pass_no_plan",
			"the last dry pass planned no write; nothing is due yet, or nothing it selected can settle")
	}
	items = append(items, dry)

	// 4. A dry-run check is recorded for every benchmark the evaluator can select.
	gates := s.st.ForecastDryGate()
	var missing []string
	gateCodes, gateErr := s.st.ForecastGateCodes()
	if gateErr != nil {
		gate := readinessItem{Key: "dry_gate"}
		gate.fail("gate_unreadable", "could not read the benchmarks in use")
		items = append(items, gate)
	} else {
		for _, code := range gateCodes {
			if _, ok := gates[code]; !ok {
				missing = append(missing, code)
			}
		}
		gate := readinessItem{Key: "dry_gate", OK: len(missing) == 0}
		if len(missing) > 0 {
			gate.Codes = missing
			gate.fail("gate_missing", "no dry-run check for "+strings.Join(missing, ", "))
		}
		items = append(items, gate)
	}

	// 5. At least one alert channel is configured and enabled, and a test reached every such channel
	// within thirty days. P41 writes the key; any channel change deletes it. The owner dropped the
	// original "both channels" requirement on 2026-10-24: one verified channel is enough.
	alertsOK := false
	if raw := s.st.GetSetting(setForecastAlertTestOKAt, ""); raw != "" {
		if at, err := time.Parse(time.RFC3339, raw); err == nil {
			age := now.Sub(at)
			// A stamp in the future (a clock change, a hand-edited row) proves nothing.
			alertsOK = age >= -5*time.Minute && age <= 30*24*time.Hour
		}
	}
	alerts := readinessItem{Key: "alerts", OK: true}
	switch snapshot, err := s.st.notificationSettings(); {
	case err != nil:
		alerts.fail("alerts_unreadable", "could not read the alert configuration")
	case len(snapshot.forecastConfigured) == 0:
		alerts.fail("alerts_no_channel", "no alert channel is configured")
	case !snapshot.forecastChannelsReady:
		alerts.fail("alerts_channels_paused", "every configured alert channel is disabled or paused in the notification settings")
	case !alertsOK:
		alerts.fail("alerts_untested", "no test alert has reached the enabled channels in the last 30 days")
	}
	items = append(items, alerts)
	chain := s.quotes.configuredLedgerSources(s.quoteConfigLoad(), quoteIntervalSettled)
	_, primaryOK := ledgerPrimary(chain)
	ledgerOK := len(chain) > 0 && primaryOK
	ledger := readinessItem{Key: "ledger_chain", OK: ledgerOK}
	if !ledgerOK {
		ledger.fail("ledger_no_primary", "no enabled settlement source has both required evidence capabilities")
	}
	items = append(items, ledger)

	return items
}

// readinessFailure returns the failing items, or nil when every one holds.
func readinessFailure(items []readinessItem) []readinessItem {
	var out []readinessItem
	for _, it := range items {
		if !it.OK {
			out = append(out, it)
		}
	}
	return out
}

// ---------- health ----------

// handleHealthzForecast is the evaluator's own probe: status only, one key, no detail. It is public
// because an external monitor is not a portal account; the body cannot leak anything but the one
// bit, and the heartbeat it reads holds counts an anonymous caller must not see.
//
// While the mode is off the route answers 200: a switched-off evaluator is healthy by definition,
// and a probe that failed on every install that never enabled it would be ignored on the one that
// did. DRY answers 200 for the same reason: its ticker deliberately does no work (step), so a
// heartbeat that only a manual pass refreshes is not evidence of a dead loop, and a probe that went
// red a few minutes after every dry pass would be reporting the design.
func (s *Server) handleHealthzForecast(w http.ResponseWriter, r *http.Request) {
	ok := true
	if mode := forecastMode(s.st.GetSetting(setForecastAuto, "0")); mode == forecastModeOn {
		hb, have := s.st.ForecastHeartbeat()
		if !have {
			ok = false
		} else if at, err := time.Parse(time.RFC3339, hb.LastTickAt); err != nil ||
			time.Since(at) > forecastStalledAfter {
			ok = false
		}
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, status, map[string]any{"ok": ok})
}

// ---------- watchdog ----------

// forecastWatchdog is the independent liveness check. It is its own goroutine with its own recover:
// the case it exists for is the evaluator having died, so sharing that goroutine would share the
// failure.
func (s *Server) forecastWatchdog() {
	for range time.Tick(time.Minute) {
		s.forecastWatchdogSafe(time.Now())
	}
}

// forecastWatchdogSafe is one tick with the recover that keeps a panic inside the watchdog from
// taking the watchdog down. It is separate so the recovery is what the loop and the test both run.
func (s *Server) forecastWatchdogSafe(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("forecast watchdog: panic: %v", r)
		}
	}()
	s.forecastWatchdogOnce(now)
}

// forecastWatchdogOnce evaluates the alerts for this minute. Every branch that is not an alert is a
// single settings read; the last emission times live in meta so a restart does not re-page.
func (s *Server) forecastWatchdogOnce(now time.Time) {
	mode := forecastMode(s.st.GetSetting(setForecastAuto, "0"))
	if mode == forecastModeOff {
		return
	}
	// The daily benchmark promotion (P4) rides on the watchdog: it is already awake once a minute and
	// the promotion is idempotent.
	s.promoteForecastBench(now)
	chain := s.quotes.configuredLedgerSources(s.quoteConfigLoad(), quoteIntervalSettled)
	if _, primaryOK := ledgerPrimary(chain); len(chain) == 0 || !primaryOK {
		s.emitForecastAlert("no_source",
			"no qualified settlement primary is enabled; inspect the quote.order audit", now, forecastLaggingAlertEvery)
	}

	hb, have := s.st.ForecastHeartbeat()
	if !have {
		// A dry evaluator has no background loop to hear from, so only mode 1 can be missing its
		// heartbeat. Reporting it in dry would page on every deployment that had never run a manual
		// pass, which is the ordinary state of the mode.
		if mode == forecastModeOn {
			s.emitForecastAlert("stalled", "no heartbeat has been written yet", now, forecastStallAlertEvery)
		}
		return
	}
	if mode == forecastModeOn {
		if at, err := time.Parse(time.RFC3339, hb.LastTickAt); err != nil || now.Sub(at) > forecastStalledAfter {
			s.emitForecastAlert("stalled",
				"the heartbeat is older than 10 minutes", now, forecastStallAlertEvery)
		}
	}
	// Lagging is about the ledger, including retries sleeping until next_attempt_at.
	// The latest pass can be empty while old work remains; a stale pass can also describe
	// work that has recovered. Neither is authority for the current backlog.
	backlog, backlogErr := s.forecastBacklog(now)
	if backlogErr != nil {
		log.Printf("forecast watchdog backlog: %v", backlogErr)
	}
	if backlogErr == nil && backlog.OldestMinutes > int(forecastLaggingAfter/time.Minute) {
		s.emitForecastAlert("lagging",
			fmt.Sprintf("%d completed checkpoints remain unsettled; %d older than 6 hours; oldest %d minutes (%s %s due %s)",
				backlog.Due, backlog.Overdue, backlog.OldestMinutes, backlog.Items[0].Symbol, backlog.Items[0].Checkpoint, backlog.Items[0].Due), now, forecastLaggingAlertEvery)
	}
	if hb.Pass.StaleValveTripped {
		s.emitForecastAlert("lagging",
			"the stale-data valve tripped: the vendor's feed is behind the calendar", now, forecastLaggingAlertEvery)
	}
	if staleCalendar(hb, now) {
		s.emitForecastAlert("lagging",
			"a benchmark calendar is behind the exchange after the close", now, forecastLaggingAlertEvery)
	}
}

// staleCalendar reports whether, after a weekday's close, any benchmark the pass fetched still ends
// before today. That is the vendor lagging the exchange, not a stock being halted.
func staleCalendar(hb forecastHeartbeat, now time.Time) bool {
	t := now.In(chinaZone)
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	if t.Hour() < 18 {
		return false
	}
	today := t.Format("2006-01-02")
	started, err := time.Parse(time.RFC3339, hb.Pass.StartedAt)
	if err != nil {
		return false
	}
	for _, b := range hb.Bench {
		// Only a code this pass actually fetched can say the vendor is behind now. A carried-over
		// entry from an idle pass would otherwise alert every evening about a calendar nobody asked
		// for.
		fetched, ferr := time.Parse(time.RFC3339, b.FetchedAt)
		if ferr != nil || fetched.Before(started) {
			continue
		}
		if b.LastBar != "" && b.LastBar < today {
			return true
		}
	}
	return false
}

// forecastChannelResult is one channel's last delivery, for the page.
type forecastChannelResult struct {
	At    string `json:"at"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// forecastAlertRecord is `forecast_alert_state`. `Last` de-duplicates the watchdog's events by
// reason; `Channels` is what the page shows as the last delivery.
type forecastAlertRecord struct {
	Last     map[string]string                `json:"last,omitempty"`
	Channels map[string]forecastChannelResult `json:"channels,omitempty"`
}

func (s *Store) forecastAlertState() forecastAlertRecord {
	var out forecastAlertRecord
	raw := s.GetSetting(setForecastAlertState, "")
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return forecastAlertRecord{}
	}
	return out
}

// updateForecastAlertState is the read-modify-write for the one meta key two paths write (the
// watchdog's de-duplication and every channel delivery). Serialized so one cannot drop the other.
func (s *Store) updateForecastAlertState(fn func(*forecastAlertRecord)) {
	s.forecastAlertMu.Lock()
	defer s.forecastAlertMu.Unlock()
	rec := s.forecastAlertState()
	if rec.Last == nil {
		rec.Last = map[string]string{}
	}
	if rec.Channels == nil {
		rec.Channels = map[string]forecastChannelResult{}
	}
	fn(&rec)
	body, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := s.SetSetting(setForecastAlertState, string(body)); err != nil {
		log.Printf("forecast alert state: %v", err)
	}
}

// emitForecastAlert fires one event unless the reason has fired inside its window, and records the
// emission time either way. The webhook and the two channels are fired here and nowhere else, so a
// reason produces one delivery per channel.
func (s *Server) emitForecastAlert(reason, detail string, now time.Time, every time.Duration) {
	fire := false
	s.st.updateForecastAlertState(func(rec *forecastAlertRecord) {
		if raw, ok := rec.Last[reason]; ok {
			if at, err := time.Parse(time.RFC3339, raw); err == nil && now.Sub(at) < every {
				return
			}
		}
		rec.Last[reason] = now.UTC().Format(time.RFC3339)
		fire = true
	})
	if !fire {
		return
	}
	hb, _ := s.st.ForecastHeartbeat()
	payload := map[string]any{
		"reason":       reason,
		"detail":       detail,
		"mode":         hb.Mode,
		"last_tick_at": hb.LastTickAt,
		"at":           now.UTC().Format(time.RFC3339),
	}
	s.fireEvent(EventForecastEvaluatorUnhealthy, payload)
}

// ---------- manual pass ----------

// runForecastNow is the evaluate-now entry point. It shares the loop's single-flight guard, so a
// manual pass and a tick cannot overlap.
func (s *Server) runForecastNow(r *http.Request, user string) (int, string, any) {
	mode := forecastMode(s.st.GetSetting(setForecastAuto, "0"))
	if mode == forecastModeOff {
		return http.StatusConflict, "forecast_eval_off", nil
	}
	if !s.forecastEval.tryStart() {
		return http.StatusConflict, "forecast_eval_running", nil
	}
	// step would call tryStart itself; run the pass here under the guard we already hold.
	now := s.forecastEval.clock()
	st := &passStats{
		Kind: "manual", StartedAt: now.UTC().Format(time.RFC3339),
		Fetches: map[string]int{}, Refused: map[string]int{},
		RefusedByClass: map[string]int{}, Baselines: map[string]int{},
	}
	s.forecastEval.beginPass(s, mode, st)
	defer func() {
		if rec := recover(); rec != nil {
			st.Panics++
			st.LastError = fmt.Sprintf("panic: %v", rec)
			log.Printf("forecast evaluator: manual pass panic: %v", rec)
		}
		st.FinishedAt = s.forecastEval.clock().UTC().Format(time.RFC3339)
		if err := s.st.saveForecastHeartbeat(s.forecastEval.heartbeat(mode, st)); err != nil {
			log.Printf("forecast evaluator: heartbeat: %v", err)
		}
		s.forecastEval.endPass()
		s.forecastEval.finish()
	}()
	if err := s.forecastEval.tick(s); err != nil {
		st.Errors++
		st.LastError = err.Error()
	}
	s.recordChange(r, user, AuditForecastEvalRun, "forecast", "evaluate", map[string]any{"mode": mode})
	return http.StatusOK, "", map[string]any{"ok": true, "mode": mode}
}
