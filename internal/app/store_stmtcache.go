package app

import (
	"container/list"
	"context"
	"database/sql"
	"strings"
	"sync"
)

// stmtCacheCap bounds the prepared-statement cache. Some SQL is built with variable-length IN lists,
// so the number of distinct texts is unbounded; the least recently used statement is closed once the
// cache is full.
const stmtCacheCap = 256

// stmtPendingCap bounds the texts noted by transactions that could not prepare them (see want).
// stmtDrainPerCall bounds how many of them one pool-level call prepares.
const (
	stmtPendingCap   = 64
	stmtDrainPerCall = 4
)

// stmtCache is a bounded LRU of prepared statements keyed by the final SQL text (after placeholder
// rewriting). SQLite prepares a statement on every plain Exec/Query, and with the pool capped at one
// connection that work happens while every other caller queues for the connection. Preparing a text
// once and reusing it removes that work from the held-connection window.
//
// It exists for SQLite only. pgx's stdlib driver already caches statements per connection, and
// database/sql Prepare on PostgreSQL can break behind a transaction-pooling PgBouncer, so the
// PostgreSQL path never creates one (see newStmtCache) and runs exactly as before.
//
// Safety properties:
//   - A text is prepared at most once at a time: a concurrent caller for the same text waits for the
//     first one's prepare instead of preparing a duplicate.
//   - An entry is reference counted. Eviction (or Close) marks it and the statement is closed when the
//     last in-flight call releases it, so a statement is never closed under a running caller.
//   - A prepare that fails (including a cancelled context) is never cached; the caller falls back to
//     the plain path, which reports the same error, and the next caller prepares afresh.
//   - Only data-manipulation text is cached. DDL, PRAGMA and the like run uncached, so schema work never
//     leaves a cached statement behind. A schema change made by anything else is handled by SQLite,
//     which re-prepares a statement that hits SQLITE_SCHEMA.
type stmtCache struct {
	db  *sql.DB
	max int

	mu      sync.Mutex
	closed  bool
	order   *list.List // front = most recently used; elements hold *stmtEntry
	entries map[string]*list.Element
	prepare func(ctx context.Context, q string) (*sql.Stmt, error) // seam: tests count prepares
	pending map[string]struct{}                                    // texts a transaction wanted cached; see want
}

type stmtEntry struct {
	key     string
	stmt    *sql.Stmt
	ready   chan struct{} // closed when the prepare finished (stmt or err is set)
	err     error
	refs    int  // in-flight callers; guarded by stmtCache.mu
	evicted bool // removed from the cache; closed when refs reaches zero
}

// newStmtCache returns the cache for a driver, or nil where there must be none (PostgreSQL).
func newStmtCache(driver string, db *sql.DB) *stmtCache {
	if driver == "postgres" {
		return nil
	}
	c := &stmtCache{db: db, max: stmtCacheCap, order: list.New(), entries: map[string]*list.Element{}, pending: map[string]struct{}{}}
	c.prepare = func(ctx context.Context, q string) (*sql.Stmt, error) { return db.PrepareContext(ctx, q) }
	return c
}

// stmtCacheable reports whether q is plain data-manipulation SQL worth caching.
func stmtCacheable(q string) bool {
	q = strings.TrimLeft(q, " \t\r\n")
	end := 0
	for end < len(q) && q[end] != ' ' && q[end] != '\t' && q[end] != '\r' && q[end] != '\n' && q[end] != '(' {
		end++
	}
	switch strings.ToUpper(q[:end]) {
	case "SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "REPLACE":
		return true
	}
	return false
}

// acquire returns the cached statement for q, preparing it on first use, and a release func that must
// be called when the call using it has returned. It returns nil when the text is not cached (not
// cacheable, closed cache, or a failed prepare); the caller then runs the plain path.
func (c *stmtCache) acquire(ctx context.Context, q string) (*sql.Stmt, func()) {
	if c == nil || !stmtCacheable(q) {
		return nil, nil
	}
	c.drainPending(ctx)
	return c.acquireNoDrain(ctx, q)
}

// acquireNoDrain is acquire without the pending-notes step.
func (c *stmtCache) acquireNoDrain(ctx context.Context, q string) (*sql.Stmt, func()) {
	if c == nil || !stmtCacheable(q) {
		return nil, nil
	}
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return nil, nil
		}
		if el, ok := c.entries[q]; ok {
			e := el.Value.(*stmtEntry)
			c.order.MoveToFront(el)
			e.refs++
			c.mu.Unlock()
			select {
			case <-e.ready:
			case <-ctx.Done():
				c.release(e)
				return nil, nil
			}
			if e.err != nil {
				// The preparing caller failed (for instance its context was cancelled) and has removed
				// the entry. Try again rather than inherit its error.
				c.release(e)
				if ctx.Err() != nil {
					return nil, nil
				}
				continue
			}
			return e.stmt, func() { c.release(e) }
		}
		e := &stmtEntry{key: q, ready: make(chan struct{}), refs: 1}
		c.entries[q] = c.order.PushFront(e)
		c.evictLocked()
		c.mu.Unlock()

		st, err := c.prepare(ctx, q)

		c.mu.Lock()
		if err != nil {
			e.err = err
			if el, ok := c.entries[q]; ok && el.Value.(*stmtEntry) == e {
				c.order.Remove(el)
				delete(c.entries, q)
			}
			e.evicted = true
		} else {
			e.stmt = st
		}
		close(e.ready)
		c.mu.Unlock()
		if err != nil {
			c.release(e)
			return nil, nil
		}
		return st, func() { c.release(e) }
	}
}

// want notes that a transaction ran q without a cached statement. A transaction on the single
// SQLite connection cannot prepare on the pool, so a text that only ever runs inside transactions
// would never be cached; the next pool-level call prepares the noted texts instead (drainPending).
// The set is bounded, and a full set drops new notes rather than growing.
func (c *stmtCache) want(q string) {
	if c == nil || !stmtCacheable(q) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if _, cached := c.entries[q]; cached {
		return
	}
	if len(c.pending) < stmtPendingCap {
		c.pending[q] = struct{}{}
	}
}

// drainPending prepares a few noted texts. It runs only from pool-level calls, which already hold no
// connection, and takes the connection for each prepare like any other pool caller.
func (c *stmtCache) drainPending(ctx context.Context) {
	c.mu.Lock()
	if len(c.pending) == 0 || c.closed {
		c.mu.Unlock()
		return
	}
	var todo []string
	for q := range c.pending {
		delete(c.pending, q)
		todo = append(todo, q)
		if len(todo) == stmtDrainPerCall {
			break
		}
	}
	c.mu.Unlock()
	for _, q := range todo {
		if st, release := c.acquireNoDrain(ctx, q); st != nil {
			release()
		}
	}
}

// acquireExisting is acquire without the prepare: it returns a statement only if the text is already
// cached and ready. A transaction on the single SQLite connection must use it, because preparing on
// the pool would wait for a connection the transaction itself holds.
func (c *stmtCache) acquireExisting(q string) (*sql.Stmt, func()) {
	if c == nil || !stmtCacheable(q) {
		return nil, nil
	}
	c.mu.Lock()
	el, ok := c.entries[q]
	if !ok || c.closed {
		c.mu.Unlock()
		return nil, nil
	}
	e := el.Value.(*stmtEntry)
	select {
	case <-e.ready:
	default:
		c.mu.Unlock()
		return nil, nil
	}
	if e.err != nil {
		c.mu.Unlock()
		return nil, nil
	}
	c.order.MoveToFront(el)
	e.refs++
	c.mu.Unlock()
	return e.stmt, func() { c.release(e) }
}

// evictLocked drops least recently used entries until the cache is within its cap. An entry that is
// still being prepared or used is only unlinked; whoever holds the last reference closes it.
func (c *stmtCache) evictLocked() {
	for c.order.Len() > c.max {
		el := c.order.Back()
		e := el.Value.(*stmtEntry)
		c.order.Remove(el)
		delete(c.entries, e.key)
		e.evicted = true
		if e.refs == 0 {
			c.closeEntry(e)
		}
	}
}

// release gives back one reference and closes the statement if it was evicted meanwhile.
func (c *stmtCache) release(e *stmtEntry) {
	c.mu.Lock()
	e.refs--
	last := e.refs == 0 && e.evicted
	c.mu.Unlock()
	if last {
		c.closeEntry(e)
	}
}

// closeEntry closes the entry's statement, once it has finished preparing. It never holds c.mu across
// Close, which takes the connection's lock.
func (c *stmtCache) closeEntry(e *stmtEntry) {
	<-e.ready
	if e.stmt != nil {
		_ = e.stmt.Close()
	}
}

// close closes every cached statement and refuses further caching. It is safe on a nil cache.
func (c *stmtCache) close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.closed = true
	var idle []*stmtEntry
	for el := c.order.Front(); el != nil; el = el.Next() {
		e := el.Value.(*stmtEntry)
		e.evicted = true
		if e.refs == 0 {
			idle = append(idle, e)
		}
	}
	c.order.Init()
	c.entries = map[string]*list.Element{}
	c.mu.Unlock()
	for _, e := range idle {
		c.closeEntry(e)
	}
}

// size is the number of cached statements.
func (c *stmtCache) size() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
