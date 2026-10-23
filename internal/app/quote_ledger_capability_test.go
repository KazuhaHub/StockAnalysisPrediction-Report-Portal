package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestSettledRequiresAnExplicitGrant(t *testing.T) {
	target := quoteAsk(t, "sh")
	for _, iv := range []quoteInterval{"settled", "settled_shadow"} {
		t.Run(string(iv), func(t *testing.T) {
			for _, src := range []quoteSource{
				{},
				{caps: []quoteCapability{{intervals: []quoteInterval{quoteIntervalDailyRange}}}},
				{caps: []quoteCapability{{markets: func(*quoteMarket) bool { return true }}}},
			} {
				if src.serves(target, iv) || src.covers(target.Market, iv) {
					t.Fatal("display capability or undeclared wildcard authorized ledger use")
				}
			}
			granted := quoteSource{caps: []quoteCapability{{
				markets: func(m *quoteMarket) bool { return m.id == "sh" }, intervals: []quoteInterval{iv},
			}}}
			if !granted.serves(target, iv) || granted.serves(quoteAsk(t, "sz"), iv) {
				t.Fatal("explicit ledger grant did not retain its market boundary")
			}
			if !granted.servesExplicit(target, iv) || granted.servesExplicit(quoteTarget{}, iv) || granted.covers(nil, iv) {
				t.Fatal("explicit grant accepted an unresolved market or lost a valid market")
			}
			other := quoteIntervalSettled
			if iv == other {
				other = quoteIntervalSettledShadow
			}
			if granted.serves(target, other) {
				t.Fatal("a writer grant and a shadow grant authorized each other")
			}
			granted.knows = func(quoteTarget) bool { return false }
			if granted.serves(target, iv) {
				t.Fatal("ledger grant bypassed the source's code boundary")
			}
		})
	}
	// Preserve the existing display-only stub contract in both directions.
	for _, iv := range quoteAllIntervals {
		if !(quoteSource{}).serves(target, iv) {
			t.Fatalf("ledger guard removed the display wildcard for %s", iv)
		}
	}
}

func TestLedgerFetchNeverCaches(t *testing.T) {
	for _, iv := range []quoteInterval{"settled", "settled_shadow"} {
		for _, warm := range []bool{false, true} {
			t.Run(string(iv)+"/warm="+strconv.FormatBool(warm), func(t *testing.T) {
				calls := 0
				c := &quoteCache{sources: []quoteSource{{name: "stub", fetch: func(context.Context, string, string, int) (*QuoteResp, error) {
					calls++
					return &QuoteResp{}, nil
				}}}}
				cfg := quoteConfigDefault()
				cfg.Order = []string{"stub"}
				if warm {
					c.put(quoteCacheKey("sh", "601899", 60, iv, quoteWindow{}), &QuoteResp{}, time.Minute)
				}
				before := c.stats()
				resp, cached, ttl, err := c.fetchUnder(context.Background(), cfg, "sh", "601899", 60, iv, quoteWindow{})
				if !errors.Is(err, errQuoteLedgerInterval) || resp != nil || cached || ttl != 0 || calls != 0 {
					t.Fatalf("display fetch accepted ledger request: response=%v cached=%v ttl=%v error=%v calls=%d", resp, cached, ttl, err, calls)
				}
				if after := c.stats(); after != before {
					t.Fatalf("ledger refusal changed display cache: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}

func TestDisplaySourceCallRefusesLedgerIntervals(t *testing.T) {
	for _, iv := range []quoteInterval{quoteIntervalSettled, quoteIntervalSettledShadow} {
		for _, shape := range []string{"count", "interval", "window"} {
			t.Run(string(iv)+"/"+shape, func(t *testing.T) {
				called := false
				fetch := func() (*QuoteResp, error) { called = true; return &QuoteResp{}, nil }
				src := quoteSource{caps: []quoteCapability{{intervals: []quoteInterval{iv}}}}
				switch shape {
				case "count":
					src.fetch = func(context.Context, string, string, int) (*QuoteResp, error) { return fetch() }
				case "interval":
					src.fetchAt = func(context.Context, string, string, int, quoteInterval) (*QuoteResp, error) { return fetch() }
				case "window":
					src.fetchRange = func(context.Context, string, string, string, string) (*QuoteResp, error) { return fetch() }
				}
				resp, err := src.call(context.Background(), "sh", "601899", 60, iv, quoteWindow{})
				if !errors.Is(err, errQuoteLedgerInterval) || resp != nil || called {
					t.Fatalf("display dispatch reached a ledger fetch: response=%v error=%v called=%v", resp, err, called)
				}
			})
		}
	}
}

func TestNoPublicRangeIsSettled(t *testing.T) {
	for name, spec := range quoteRanges {
		if spec.interval == "settled" || spec.interval == "settled_shadow" {
			t.Fatalf("public range %s exposes ledger capability", name)
		}
	}
	for _, iv := range quoteAllIntervals {
		if iv == "settled" || iv == "settled_shadow" {
			t.Fatalf("public interval inventory exposes ledger capability %s", iv)
		}
	}
}
