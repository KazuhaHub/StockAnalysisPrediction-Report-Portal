package app

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	errForecastBusy    = errors.New("forecast compute capacity is unavailable")
	errForecastCompute = errors.New("forecast computation failed")
)

type forecastComputeConfig struct {
	Official int
	Explore  int
	Wait     time.Duration
}

type forecastFlight struct {
	done     chan struct{}
	cell     *forecastCell
	err      error
	official bool
	started  bool
	callers  int
	queries  map[*forecastUserQuery]bool
}

type forecastUserQuery struct {
	user, key string
	refs      int
}
type forecastQueryLease struct {
	owner    *forecastCompute
	query    *forecastUserQuery
	released bool
}

// A zero-value controller is ready to use. Pools bound scorers, not cache hits or maturity gates.
// Flights deliberately do not inherit a request context: a cancelled leader cannot kill followers.
type forecastCompute struct {
	mu            sync.Mutex
	flights       map[string]*forecastFlight
	active        [2]int // official, exploratory
	changed       chan struct{}
	cfg           forecastComputeConfig
	queries       map[string]*forecastUserQuery
	rates         map[string]*rateRec
	nextRatePrune time.Time
}

func (c *forecastCompute) wakeLocked() {
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
}

func (c *forecastCompute) do(ctx context.Context, key string, official bool, cfg forecastComputeConfig, fn func() (*forecastCell, error)) (*forecastCell, error) {
	return c.doForQuery(ctx, key, official, cfg, nil, fn)
}

func (c *forecastCompute) doForQuery(ctx context.Context, key string, official bool, cfg forecastComputeConfig, lease *forecastQueryLease, fn func() (*forecastCell, error)) (*forecastCell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Wait <= 0 {
		cfg.Wait = 10 * time.Second
	}
	if lease != nil && lease.owner != c {
		return nil, errForecastCompute
	}
	c.mu.Lock()
	if lease != nil && lease.released {
		c.mu.Unlock()
		return nil, errForecastCompute
	}
	if c.flights == nil {
		c.flights = make(map[string]*forecastFlight)
	}
	c.cfg = cfg
	f := c.flights[key]
	if f == nil {
		f = &forecastFlight{done: make(chan struct{}), official: official}
		c.flights[key] = f
		go c.execute(key, f, cfg.Wait, fn)
	} else if official && !f.started {
		// Identical official/custom requests share a flight. An official follower may promote a
		// queued exploratory leader; otherwise the exploratory queue could starve the official pool.
		f.official = true
	}
	if lease != nil {
		if f.queries == nil {
			f.queries = make(map[*forecastUserQuery]bool)
		}
		if !f.queries[lease.query] {
			f.queries[lease.query] = true
			lease.query.refs++
		}
	}
	f.callers++
	c.wakeLocked()
	c.mu.Unlock()
	defer func() { c.mu.Lock(); f.callers--; c.mu.Unlock() }()
	timer := time.NewTimer(cfg.Wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errForecastBusy
	case <-f.done:
		return f.cell, f.err
	}
}

func (c *forecastCompute) acquire(f *forecastFlight, wait time.Duration) (int, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		c.mu.Lock()
		pool, slots := 1, c.cfg.Explore
		if f.official {
			pool, slots = 0, c.cfg.Official
		}
		if slots <= 0 {
			c.mu.Unlock()
			return -1, errForecastBusy
		}
		if c.active[pool] < slots {
			c.active[pool]++
			f.started = true
			c.mu.Unlock()
			return pool, nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-timer.C:
			return -1, errForecastBusy
		case <-changed:
		}
	}
}

func (c *forecastCompute) execute(key string, f *forecastFlight, wait time.Duration, fn func() (*forecastCell, error)) {
	pool := -1
	var cell *forecastCell
	var err error
	defer func() {
		if recover() != nil {
			cell, err = nil, errForecastCompute
		}
		c.mu.Lock()
		if pool >= 0 {
			c.active[pool]--
		}
		f.cell, f.err = cell, err
		for query := range f.queries {
			c.releaseQueryLocked(query)
		}
		delete(c.flights, key)
		close(f.done)
		c.wakeLocked()
		c.mu.Unlock()
	}()
	pool, err = c.acquire(f, wait)
	if err == nil {
		cell, err = fn()
	}
}

// Both rate ceilings use per-user fixed windows. Zero means no frequency ceiling, not permission
// to run distinct exploratory queries simultaneously. Live counters are never dropped to make room.
func (c *forecastCompute) rateAllowedLocked(key string, limit int, now time.Time) bool {
	if limit == 0 {
		return true
	}
	if c.rates == nil {
		c.rates = make(map[string]*rateRec)
	}
	if len(c.rates) > 4096 && !now.Before(c.nextRatePrune) {
		for key, rec := range c.rates {
			if !now.Before(rec.resetAt) {
				delete(c.rates, key)
			}
		}
		c.nextRatePrune = now.Add(time.Minute)
	}
	rec := c.rates[key]
	if rec == nil || !now.Before(rec.resetAt) {
		rec = &rateRec{resetAt: now.Add(time.Minute)}
		c.rates[key] = rec
	}
	if rec.n >= limit {
		return false
	}
	rec.n++
	return true
}

func (c *forecastCompute) beginCustom(user, key string, limit int, now time.Time) (*forecastQueryLease, bool) {
	if user == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queries == nil {
		c.queries = make(map[string]*forecastUserQuery)
	}
	query := c.queries[user]
	if query != nil && query.key != key {
		return nil, false
	}
	if !c.rateAllowedLocked("custom:"+user, limit, now) {
		return nil, false
	}
	if query == nil {
		query = &forecastUserQuery{user: user, key: key}
		c.queries[user] = query
	}
	query.refs++
	return &forecastQueryLease{owner: c, query: query}, true
}

func (c *forecastCompute) allowMembers(user string, limit int, now time.Time) bool {
	if user == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rateAllowedLocked("members:"+user, limit, now)
}

func (c *forecastCompute) releaseQueryLocked(query *forecastUserQuery) {
	query.refs--
	if query.refs == 0 {
		delete(c.queries, query.user)
	}
}

func (l *forecastQueryLease) release() {
	if l == nil {
		return
	}
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	l.owner.releaseQueryLocked(l.query)
}
