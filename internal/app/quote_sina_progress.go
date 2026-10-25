package app

import (
	"context"
	"sync"
	"time"
)

const sinaPartialLimit = 64
const sinaPartialLifetime = 6 * time.Hour

// This continuation contains only a parsed, independently fetched snapshot from an unfinished
// comparison read. It is separate from display cache and never supplies a ledger writer.
type sinaRangeProgress struct {
	mu      sync.Mutex
	scope   string
	entries map[string]sinaPartialSnapshot
}

type sinaPartialSnapshot struct {
	snapshot QuoteResp
	started  time.Time
}

type sinaProgressContextKey struct{}
type sinaReadContinuation struct {
	store      *sinaRangeProgress
	scope, key string
	now        time.Time
}

func withSinaRangeProgress(ctx context.Context, store *sinaRangeProgress, scope string, now time.Time) context.Context {
	return context.WithValue(ctx, sinaProgressContextKey{}, sinaReadContinuation{store: store, scope: scope, now: now})
}

func sinaContinuation(ctx context.Context, key string) *sinaReadContinuation {
	read, ok := ctx.Value(sinaProgressContextKey{}).(sinaReadContinuation)
	if !ok || read.store == nil {
		return nil
	}
	read.key = key
	return &read
}

func (p *sinaRangeProgress) prepareLocked(scope string, now time.Time) {
	if p.scope != scope || p.entries == nil {
		p.scope = scope
		p.entries = map[string]sinaPartialSnapshot{}
	}
	for key, entry := range p.entries {
		if now.Before(entry.started) || now.Sub(entry.started) >= sinaPartialLifetime {
			delete(p.entries, key)
		}
	}
}

func (read *sinaReadContinuation) get() *QuoteResp {
	if read == nil {
		return nil
	}
	p := read.store
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareLocked(read.scope, read.now)
	entry, ok := p.entries[read.key]
	if !ok {
		return nil
	}
	copy := entry.snapshot
	return &copy
}

func (read *sinaReadContinuation) keep(snapshot *QuoteResp) {
	if read == nil {
		return
	}
	p := read.store
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareLocked(read.scope, read.now)
	// Repeated deferrals never renew the original lifetime.
	if _, ok := p.entries[read.key]; ok {
		return
	}
	if len(p.entries) >= sinaPartialLimit {
		var oldest string
		var started time.Time
		for key, entry := range p.entries {
			if oldest == "" || entry.started.Before(started) {
				oldest, started = key, entry.started
			}
		}
		delete(p.entries, oldest)
	}
	copy := *snapshot
	copy.Bars, copy.ExDates = nil, nil
	p.entries[read.key] = sinaPartialSnapshot{snapshot: copy, started: read.now}
}

func (read *sinaReadContinuation) forget() {
	if read == nil {
		return
	}
	p := read.store
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.scope == read.scope {
		delete(p.entries, read.key)
	}
}
