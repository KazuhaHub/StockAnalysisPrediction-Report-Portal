package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// spyDriver wraps the portal SQLite driver and records what the store asks of it: which SQL texts
// were prepared, which ran through the driver's direct (unprepared) path, which prepared statements
// were closed, and how many times a pooled connection was handed back (every database/sql
// acquisition ends in exactly one release, so releases count connection acquisitions).
type spyDriver struct {
	inner driver.Driver

	mu       sync.Mutex
	prepares map[string]int
	direct   map[string]int
	open     int // prepared statements not yet closed
	closed   int // prepared statements closed
	releases atomic.Int64
	trace    []string // statements run, in order, while tracing is on
	tracing  bool
}

func (d *spyDriver) note(q string) {
	d.mu.Lock()
	if d.tracing {
		d.trace = append(d.trace, q)
	}
	d.mu.Unlock()
}

var spyDriverSeq atomic.Int64

// registerSpyDriver registers a fresh spy and returns it with its database/sql driver name.
func registerSpyDriver(t testing.TB) (*spyDriver, string) {
	t.Helper()
	probe, err := sql.Open(portalSQLiteDriver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	probe.Close()
	d := &spyDriver{inner: inner, prepares: map[string]int{}, direct: map[string]int{}}
	name := fmt.Sprintf("report-portal-sqlite-spy-%d", spyDriverSeq.Add(1))
	sql.Register(name, d)
	return d, name
}

func (d *spyDriver) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &spyConn{d: d, c: c}, nil
}

func (d *spyDriver) preparesOf(q string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.prepares[q]
}

func (d *spyDriver) directOf(q string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.direct[q]
}

func (d *spyDriver) openStmts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.open
}

func (d *spyDriver) closedStmts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}

type spyConn struct {
	d *spyDriver
	c driver.Conn
}

func (c *spyConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}

func (c *spyConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	var st driver.Stmt
	var err error
	if pc, ok := c.c.(driver.ConnPrepareContext); ok {
		st, err = pc.PrepareContext(ctx, q)
	} else {
		st, err = c.c.Prepare(q)
	}
	if err != nil {
		return nil, err
	}
	c.d.mu.Lock()
	c.d.prepares[q]++
	c.d.open++
	c.d.mu.Unlock()
	return &spyStmt{d: c.d, s: st, q: q}, nil
}

func (c *spyConn) Close() error { return c.c.Close() }
func (c *spyConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *spyConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	if bc, ok := c.c.(driver.ConnBeginTx); ok {
		return bc.BeginTx(ctx, o)
	}
	return nil, errors.New("spy: the wrapped connection has no BeginTx")
}

func (c *spyConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := c.c.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.d.mu.Lock()
	c.d.direct[q]++
	c.d.mu.Unlock()
	c.d.note(q)
	return ec.ExecContext(ctx, q, args)
}

func (c *spyConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := c.c.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	c.d.mu.Lock()
	c.d.direct[q]++
	c.d.mu.Unlock()
	c.d.note(q)
	return qc.QueryContext(ctx, q, args)
}

func (c *spyConn) Ping(ctx context.Context) error {
	if p, ok := c.c.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *spyConn) ResetSession(ctx context.Context) error {
	if r, ok := c.c.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

// IsValid is called by database/sql each time a connection returns to the pool.
func (c *spyConn) IsValid() bool {
	c.d.releases.Add(1)
	c.d.note("-- connection released")
	if v, ok := c.c.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *spyConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ck, ok := c.c.(driver.NamedValueChecker); ok {
		return ck.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// Serialize and Deserialize forward the image copy newTestStore relies on.
func (c *spyConn) Serialize() ([]byte, error) { return c.c.(sqliteImageConn).Serialize() }
func (c *spyConn) Deserialize(b []byte) error { return c.c.(sqliteImageConn).Deserialize(b) }

type spyStmt struct {
	d *spyDriver
	s driver.Stmt
	q string
}

func (s *spyStmt) Close() error {
	s.d.mu.Lock()
	s.d.open--
	s.d.closed++
	s.d.mu.Unlock()
	return s.s.Close()
}
func (s *spyStmt) NumInput() int { return s.s.NumInput() }
func (s *spyStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("spy: context path only")
}
func (s *spyStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("spy: context path only")
}
func (s *spyStmt) ExecContext(ctx context.Context, a []driver.NamedValue) (driver.Result, error) {
	s.d.note(s.q)
	return s.s.(driver.StmtExecContext).ExecContext(ctx, a)
}
func (s *spyStmt) QueryContext(ctx context.Context, a []driver.NamedValue) (driver.Rows, error) {
	s.d.note(s.q)
	return s.s.(driver.StmtQueryContext).QueryContext(ctx, a)
}

// newSpyStore returns an empty in-memory SQLite store (no portal schema) behind a spy driver.
func newSpyStore(t testing.TB) (*Store, *spyDriver) {
	t.Helper()
	d, name := registerSpyDriver(t)
	st, err := connectStoreWith("sqlite", ":memory:", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, d
}

// newSpyTestStore is newTestStore behind a spy driver: the full initialized portal schema.
func newSpyTestStore(t testing.TB) (*Store, *spyDriver) {
	t.Helper()
	img, err := testStoreImage()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	st, d := newSpyStore(t)
	if err := withSQLiteConn(st.db, func(c sqliteImageConn) error { return c.Deserialize(img) }); err != nil {
		t.Fatalf("restore the initialized test store: %v", err)
	}
	return st, d
}
