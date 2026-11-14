package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// recordDriver is a database-free driver that records the SQL text each call finally sends and
// answers every statement with an empty result. It exists to assert what reaches a driver on the
// PostgreSQL dialect path, which no SQLite-backed test can see.
type recordDriver struct {
	mu   sync.Mutex
	sent []string
}

var recordDriverSeq atomic.Int64

func (d *recordDriver) Open(string) (driver.Conn, error) { return &recordConn{d: d}, nil }

func (d *recordDriver) take() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.sent
	d.sent = nil
	return out
}

type recordConn struct{ d *recordDriver }

func (c *recordConn) note(q string) {
	c.d.mu.Lock()
	c.d.sent = append(c.d.sent, q)
	c.d.mu.Unlock()
}
func (c *recordConn) Prepare(q string) (driver.Stmt, error) { return &recordStmt{c: c, q: q}, nil }
func (c *recordConn) Close() error                          { return nil }
func (c *recordConn) Begin() (driver.Tx, error)             { return recordTx{}, nil } //nolint:staticcheck // interface requirement
func (c *recordConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return recordTx{}, nil
}
func (c *recordConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	c.note(q)
	return driver.RowsAffected(0), nil
}
func (c *recordConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	c.note(q)
	return recordRows{}, nil
}

type recordTx struct{}

func (recordTx) Commit() error   { return nil }
func (recordTx) Rollback() error { return nil }

type recordStmt struct {
	c *recordConn
	q string
}

func (s *recordStmt) Close() error  { return nil }
func (s *recordStmt) NumInput() int { return -1 }
func (s *recordStmt) Exec([]driver.Value) (driver.Result, error) { //nolint:staticcheck // interface requirement
	s.c.note(s.q)
	return driver.RowsAffected(0), nil
}
func (s *recordStmt) Query([]driver.Value) (driver.Rows, error) { //nolint:staticcheck // interface requirement
	s.c.note(s.q)
	return recordRows{}, nil
}

type recordRows struct{}

func (recordRows) Columns() []string         { return []string{"x"} }
func (recordRows) Close() error              { return nil }
func (recordRows) Next([]driver.Value) error { return io.EOF }

func recordStore(t *testing.T, dialect string) (*Store, *recordDriver) {
	t.Helper()
	d := &recordDriver{}
	name := fmt.Sprintf("report-portal-record-%d", recordDriverSeq.Add(1))
	sql.Register(name, d)
	st, err := connectStoreWith(dialect, "", name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	d.take()
	return st, d
}

// Every executor entry point must hand the driver SQL whose placeholders have been rewritten for the
// dialect exactly once: "$n" on PostgreSQL, untouched "?" on SQLite. A path that skips the rewrite
// sends "?" to PostgreSQL ("syntax error at end of input"); one that does it twice cannot be told
// apart from once on text, so each entry point is also checked for the exact result.
func TestExecutorEntryPointsRewritePlaceholdersOnce(t *testing.T) {
	for _, dialect := range []string{"postgres", "sqlite"} {
		checkEntryPointsRewrite(t, dialect)
	}
}

func checkEntryPointsRewrite(t *testing.T, dialect string) {
	t.Helper()
	const q = `SELECT 1 FROM t WHERE a=? AND b=?`
	const pg = `SELECT 1 FROM t WHERE a=$1 AND b=$2`
	ctx := context.Background()
	{
		want := q
		if dialect == "postgres" {
			want = pg
		}
		st, d := recordStore(t, dialect)
		inTx := func(f func(e migExec)) func() {
			return func() {
				tx, err := st.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				d.take() // the transaction's own BEGIN is not under test
				f(migExec{s: st, ex: tx})
			}
		}
		calls := map[string]func(){
			"exec":                  func() { st.exec(q, 1, 2) },
			"query":                 func() { r, _ := st.query(q, 1, 2); closeRows(r) },
			"queryRow":              func() { st.queryRow(q, 1, 2).Scan(new(int)) },
			"execContext":           func() { st.execContext(ctx, q, 1, 2) },
			"queryContext":          func() { r, _ := st.queryContext(ctx, q, 1, 2); closeRows(r) },
			"queryRowContext":       func() { st.queryRowContext(ctx, q, 1, 2).Scan(new(int)) },
			"migExec.exec/pool":     func() { migExec{s: st, ex: st.db}.exec(q, 1, 2) },
			"migExec.query/pool":    func() { r, _ := migExec{s: st, ex: st.db}.query(q, 1, 2); closeRows(r) },
			"migExec.queryRow/pool": func() { migExec{s: st, ex: st.db}.queryRow(q, 1, 2).Scan(new(int)) },
			"migExec.exec/ctx":      func() { migExec{s: st, ex: platformContextPool{DB: st.db, ctx: ctx}}.exec(q, 1, 2) },
			"migExec.query/ctx": func() {
				r, _ := migExec{s: st, ex: platformContextPool{DB: st.db, ctx: ctx}}.query(q, 1, 2)
				closeRows(r)
			},
			"migExec.queryRow/ctx": func() { migExec{s: st, ex: platformContextPool{DB: st.db, ctx: ctx}}.queryRow(q, 1, 2).Scan(new(int)) },
			"migExec.exec/tx":      inTx(func(e migExec) { e.exec(q, 1, 2) }),
			"migExec.query/tx":     inTx(func(e migExec) { r, _ := e.query(q, 1, 2); closeRows(r) }),
			"migExec.queryRow/tx":  inTx(func(e migExec) { e.queryRow(q, 1, 2).Scan(new(int)) }),
		}
		for name, call := range calls {
			d.take()
			call()
			got := d.take()
			if len(got) != 1 || strings.TrimSpace(got[0]) != want {
				t.Errorf("%s on %s sent %q, want exactly [%q]", name, dialect, got, want)
			}
		}
	}
}

func closeRows(r *sql.Rows) {
	if r != nil {
		r.Close()
	}
}
