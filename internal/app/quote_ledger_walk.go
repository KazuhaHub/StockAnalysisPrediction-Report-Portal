package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type ledgerRefusal struct{ Code string }

func (r *ledgerRefusal) Error() string { return "ledger: " + r.Code }

// One instance belongs to one evaluator pass, and the three-failure ceiling below is per pass. That
// ceiling alone is not enough, and the 2026-10-24 incident is why: a pass is rebuilt every minute, so
// a source that has failed six times in a row is asked to fail a seventh, and an outage the vendor
// would have shrugged off becomes load that gets the whole egress blocked. The CROSS-pass state is
// the quoteCache's (`cooling`): one pass is not a lifetime, and a cooldown has to outlive it.
type ledgerTick struct {
	mu           sync.Mutex
	failures     map[string]int
	lastError    map[string]error
	recovered    map[string]bool
	gate         chan struct{}
	observations []forecastCallObservation
}

func (tick *ledgerTick) observe(observation forecastCallObservation) {
	if tick == nil {
		return
	}
	tick.mu.Lock()
	defer tick.mu.Unlock()
	tick.observations = append(tick.observations, observation)
}

func (tick *ledgerTick) snapshotObservations() []forecastCallObservation {
	if tick == nil {
		return nil
	}
	tick.mu.Lock()
	defer tick.mu.Unlock()
	return append([]forecastCallObservation{}, tick.observations...)
}

// Serialize walks within a pass without sharing their responses. A context-aware gate keeps a
// cancelled caller from waiting behind another walk and makes the three-failure ceiling exact.
func (tick *ledgerTick) acquire(ctx context.Context) (func(), error) {
	tick.mu.Lock()
	if tick.gate == nil {
		tick.gate = make(chan struct{}, 1)
	}
	gate := tick.gate
	tick.mu.Unlock()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (tick *ledgerTick) blocked(source string) (error, bool) {
	tick.mu.Lock()
	defer tick.mu.Unlock()
	return tick.lastError[source], tick.failures[source] >= 3
}

func (tick *ledgerTick) fail(source string, err error) {
	tick.mu.Lock()
	defer tick.mu.Unlock()
	if tick.failures == nil {
		tick.failures = map[string]int{}
		tick.lastError = map[string]error{}
	}
	tick.failures[source]++
	tick.lastError[source] = err
}

// recoverInPass records that source answered this pass, whatever it did before that. The cross-pass
// breaker uses it to tell "failed and stayed down" from "failed once and recovered", which is the
// difference between benching a source for longer and letting it back.
func (tick *ledgerTick) recoverInPass(source string) {
	tick.mu.Lock()
	defer tick.mu.Unlock()
	if tick.recovered == nil {
		tick.recovered = map[string]bool{}
	}
	tick.recovered[source] = true
}

// ledgerCooldowns is the escalating wait after a pass in which a source failed and never answered:
// one minute, then five, fifteen, thirty. It is deliberately NOT exponential from the first failure —
// one bad minute is an ordinary vendor blip, and a source that comes back within it should not be
// benched for an hour — and it has a ceiling rather than growing without bound, because a probe every
// half hour is what lets the evaluator notice the vendor returning on its own.
var ledgerCooldowns = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	30 * time.Minute,
}

// ledgerCooling is one source's cross-pass breaker.
type ledgerCooling struct {
	// streak counts consecutive PASSES that ended with this source failing and never answering, so
	// each one benches it for longer. Any success, in any pass, ends it.
	streak int
	until  time.Time
	last   error
}

// ledgerCooled reports whether source is cooling down and the failure to hand back while it is. The
// error is the source's own last one rather than a "breaker open" placeholder: the planner records
// what it is given, and "the vendor returned 501" is what an operator can act on while "blocked" is
// not.
func (c *quoteCache) ledgerCooled(source string) (error, bool) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	cooling := c.cooling[source]
	if cooling == nil || !c.now().Before(cooling.until) {
		return nil, false
	}
	return cooling.last, true
}

// closeLedgerPass settles each source's cross-pass state at the end of a pass.
//
// The rule is "a pass that ended with this source failing and never answering", not the in-pass
// ceiling: a group's benchmark is fetched ONCE per pass, so a down vendor trips the ceiling only when
// there happen to be three sets in the same group, while the exchange's whole settlement path is
// already failing. Requiring a success somewhere in the pass is what separates that from one odd
// symbol: a source that answered anything at all is working, and one mark's parse failure must not
// pause settlement for every other set.
func (c *quoteCache) closeLedgerPass(tick *ledgerTick) {
	if tick == nil {
		return
	}
	tick.mu.Lock()
	failed := map[string]error{}
	for source, err := range tick.lastError {
		if !tick.recovered[source] {
			failed[source] = err
		}
	}
	tick.mu.Unlock()
	if len(failed) == 0 {
		return
	}
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cooling == nil {
		c.cooling = map[string]*ledgerCooling{}
	}
	now := c.now()
	for source, err := range failed {
		cooling := c.cooling[source]
		if cooling == nil {
			cooling = &ledgerCooling{}
			c.cooling[source] = cooling
		}
		if cooling.streak < len(ledgerCooldowns) {
			cooling.streak++
		}
		cooling.until = now.Add(ledgerCooldowns[cooling.streak-1])
		cooling.last = err
	}
}

// ledgerSucceeded ends a source's cooldown and streak. Called on every success, so a source that
// came back is not benched for the outage it just recovered from.
func (c *quoteCache) ledgerSucceeded(source string) {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.cooling, source)
}

// walkLedger never uses display cache or single-flight. Tier 1 admits only sources that provide
// both forms of evidence; a merely declared fallback cannot become an unapproved Tier 2 writer.
func (c *quoteCache) walkLedger(ctx context.Context, cfg quoteConfig, market, code, from, to string, tick *ledgerTick) (*QuoteResp, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	window, err := quoteParseWindow(from, to)
	if err != nil || !window.bounded() {
		return nil, &ledgerRefusal{Code: "bad_window"}
	}
	target, err := quoteTargetFor(market, code)
	if err != nil {
		return nil, err
	}
	if tick == nil {
		return nil, fmt.Errorf("ledger: missing pass state")
	}
	endWalk, err := tick.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer endWalk()
	c.init()
	chain := c.ledgerChain(cfg, target)
	if _, ok := ledgerPrimary(chain); !ok {
		tick.observe(forecastCallObservation{Source: "configuration", Refusal: "no_source"})
		return nil, &ledgerRefusal{Code: "no_source"}
	}
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var firstErr error
	for _, src := range chain {
		if !src.cashDividends || !src.reportsKind {
			continue
		}
		// Two breakers, checked together and for the same reason. The in-pass one bounds one pass;
		// the cross-pass cooldown is what an outage actually needs, because a pass that is rebuilt
		// every minute would otherwise re-ask a source that has already failed six times. Neither
		// records a CALL: the vendor was not asked, and a refusal that counted traffic would report
		// load that never happened.
		if failure, blocked := tick.blocked(src.name); blocked {
			tick.observe(forecastCallObservation{Source: src.name, Refusal: "breaker"})
			if firstErr == nil {
				firstErr = failure
			}
			continue
		}
		if failure, cooling := c.ledgerCooled(src.name); cooling {
			tick.observe(forecastCallObservation{Source: src.name, Refusal: "cooldown"})
			if firstErr == nil {
				firstErr = failure
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var resp *QuoteResp
		if src.fetchRange == nil {
			err = fmt.Errorf("ledger: source %q has no range fetcher", src.name)
		} else {
			resp, err = src.fetchRange(ctx, target.Market.id, target.Code, window.from, window.to)
			if err == nil && resp == nil {
				err = fmt.Errorf("ledger: source %q returned no response", src.name)
			}
		}
		callErr := err
		if ctx.Err() != nil {
			callErr = ctx.Err()
		}
		observation, _ := forecastCallResult(ctx, src.name, callErr)
		if src.fetchRange == nil {
			observation.Calls = 0
			observation.Failure = false
			observation.Refusal = "missing_fetcher"
		}
		tick.observe(observation)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			c.noteLedgerResult(src.name, nil)
			c.ledgerSucceeded(src.name)
			tick.recoverInPass(src.name)
			return resp, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		var refusal *ledgerRefusal
		if !errors.As(err, &refusal) {
			tick.fail(src.name, err)
			c.noteLedgerResult(src.name, err)
		}
	}
	if firstErr == nil {
		firstErr = &ledgerRefusal{Code: "no_source"}
	}
	return nil, firstErr
}

func (c *quoteCache) noteLedgerResult(source string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ledgerHealth == nil {
		c.ledgerHealth = map[string]*QuoteSourceHealth{}
	}
	h := c.ledgerHealth[source]
	if h == nil {
		h = &QuoteSourceHealth{Source: source}
		c.ledgerHealth[source] = h
	}
	if err != nil {
		h.Failures++
		h.LastError, h.LastErrorAt = err.Error(), c.now()
	} else {
		h.LastSuccess, h.Failures = c.now(), 0
	}
}

func (c *quoteCache) snapshotLedgerHealth() []QuoteSourceHealth {
	c.init()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []QuoteSourceHealth
	for _, h := range c.ledgerHealth {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}
