package broker

import (
	"sync"
	"time"
)

// TokenBucket is an exact integer token bucket. The runner keeps one per
// installation for background egress (120 per minute, burst 20); it is
// in-memory scheduling state, never authority.
//
// Tokens are counted in 1/60000ths: a call costs 60000 units and each elapsed
// millisecond refills `rate` units, so ratePerMinute is exact with no float
// drift. A clock that steps backwards refills nothing.
type TokenBucket struct {
	mu     sync.Mutex
	rate   int64
	cap    int64
	level  int64
	last   time.Time
	now    func() time.Time
	enable bool
}

const tokenUnits = 60000

// NewTokenBucket returns a bucket that starts full. A non-positive rate or
// burst yields a bucket that never allows a call.
func NewTokenBucket(ratePerMinute, burst int, now func() time.Time) *TokenBucket {
	if now == nil {
		now = time.Now
	}
	b := &TokenBucket{now: now}
	if ratePerMinute > 0 && burst > 0 {
		b.rate = int64(ratePerMinute)
		b.cap = int64(burst) * tokenUnits
		b.level = b.cap
		b.last = now()
		b.enable = true
	}
	return b
}

// Allow takes one token, reporting whether one was available.
func (b *TokenBucket) Allow() bool {
	if b == nil || !b.enable {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if now.Before(b.last) {
		// A clock that stepped backwards refills nothing; measure from here on.
		b.last = now
	}
	if elapsed := now.Sub(b.last).Milliseconds(); elapsed > 0 {
		if fill := b.cap/b.rate + 1; elapsed >= fill {
			b.level = b.cap
			b.last = now
		} else {
			b.level = min(b.cap, b.level+elapsed*b.rate)
			// Keep the sub-millisecond remainder so refills are not lost.
			b.last = b.last.Add(time.Duration(elapsed) * time.Millisecond)
		}
	}
	if b.level < tokenUnits {
		return false
	}
	b.level -= tokenUnits
	return true
}
