package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

type ledgerRefusal struct{ Code string }

func (r *ledgerRefusal) Error() string { return "ledger: " + r.Code }

// One instance belongs to one evaluator pass. Lifetime, not wall-clock time, resets the breaker.
type ledgerTick struct {
	mu       sync.Mutex
	failures map[string]int
}

func (tick *ledgerTick) blocked(source string) bool {
	tick.mu.Lock()
	defer tick.mu.Unlock()
	return tick.failures[source] >= 3
}

func (tick *ledgerTick) fail(source string) {
	tick.mu.Lock()
	defer tick.mu.Unlock()
	if tick.failures == nil {
		tick.failures = map[string]int{}
	}
	tick.failures[source]++
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
	c.init()
	chain := c.ledgerChain(cfg, target)
	if _, ok := ledgerPrimary(chain); !ok {
		return nil, &ledgerRefusal{Code: "no_source"}
	}
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var firstErr error
	for _, src := range chain {
		if !src.cashDividends || !src.reportsKind || tick.blocked(src.name) {
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
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil {
			c.noteLedgerResult(src.name, nil)
			return resp, nil
		}
		if firstErr == nil {
			firstErr = err
		}
		var refusal *ledgerRefusal
		if !errors.As(err, &refusal) {
			tick.fail(src.name)
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
