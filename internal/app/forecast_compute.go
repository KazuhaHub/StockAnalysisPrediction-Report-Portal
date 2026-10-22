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
}

// A zero-value controller is ready to use. Pools bound scorers, not cache hits or maturity gates.
// Flights deliberately do not inherit a request context: a cancelled leader cannot kill followers.
type forecastCompute struct {
	mu      sync.Mutex
	flights map[string]*forecastFlight
	active  [2]int // official, exploratory
	changed chan struct{}
	cfg     forecastComputeConfig
}

func (c *forecastCompute) wakeLocked() {
	if c.changed != nil {
		close(c.changed)
	}
	c.changed = make(chan struct{})
}

func (c *forecastCompute) do(ctx context.Context, key string, official bool, cfg forecastComputeConfig, fn func() (*forecastCell, error)) (*forecastCell, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cfg.Wait <= 0 {
		cfg.Wait = 10 * time.Second
	}
	c.mu.Lock()
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
