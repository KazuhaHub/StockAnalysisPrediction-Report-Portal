package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

const cacheTestSelect = `SELECT v FROM cache_t WHERE id=?`

func cacheTestStore(t *testing.T) (*Store, *spyDriver) {
	t.Helper()
	st, d := newSpyStore(t)
	if _, err := st.db.Exec(`CREATE TABLE cache_t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 50; i++ {
		if _, err := st.db.Exec(`INSERT INTO cache_t(id,v) VALUES(?,?)`, i, "v"); err != nil {
			t.Fatal(err)
		}
	}
	return st, d
}

func TestStmtCachePreparesEachTextOnce(t *testing.T) {
	st, d := cacheTestStore(t)
	for i := 0; i < 20; i++ {
		var v string
		if err := st.queryRow(cacheTestSelect, 1+i%5).Scan(&v); err != nil || v != "v" {
			t.Fatalf("queryRow: %q %v", v, err)
		}
		rows, err := st.query(cacheTestSelect, 2)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if _, err := st.exec(`UPDATE cache_t SET v=? WHERE id=?`, "v", 3); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{cacheTestSelect, `UPDATE cache_t SET v=? WHERE id=?`} {
		if n := d.preparesOf(q); n != 1 {
			t.Fatalf("%q prepared %d times, want exactly 1", q, n)
		}
		if n := d.directOf(q); n != 0 {
			t.Fatalf("%q ran %d times through the unprepared path", q, n)
		}
	}
	// The context variants share the same cache.
	var v string
	if err := st.queryRowContext(context.Background(), cacheTestSelect, 4).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if n := d.preparesOf(cacheTestSelect); n != 1 {
		t.Fatalf("a context call prepared the text again: %d", n)
	}
}

func TestStmtCacheSkipsNonDataStatements(t *testing.T) {
	st, d := cacheTestStore(t)
	ddl := `CREATE TABLE cache_u(id INTEGER)`
	if _, err := st.exec(ddl); err != nil {
		t.Fatal(err)
	}
	if st.stmts.size() != 0 || d.preparesOf(ddl) != 0 {
		t.Fatalf("DDL was cached or prepared: size=%d prepares=%d", st.stmts.size(), d.preparesOf(ddl))
	}
}

func TestStmtCacheIsBounded(t *testing.T) {
	st, d := cacheTestStore(t)
	st.stmts.max = 4
	for n := 1; n <= 30; n++ {
		q := `SELECT COUNT(*) FROM cache_t WHERE id IN (` + strings.TrimSuffix(strings.Repeat("?,", n), ",") + `)`
		args := make([]any, n)
		for i := range args {
			args[i] = i + 1
		}
		var got int
		if err := st.queryRow(q, args...).Scan(&got); err != nil || got != n {
			t.Fatalf("IN list of %d: got %d, err %v", n, got, err)
		}
		if size := st.stmts.size(); size > 4 {
			t.Fatalf("cache holds %d statements after %d distinct texts, cap 4", size, n)
		}
	}
	if st.stmts.size() != 4 {
		t.Fatalf("cache size = %d, want 4", st.stmts.size())
	}
	if open := d.openStmts(); open != 4 {
		t.Fatalf("%d prepared statements still open, want exactly the 4 cached (evicted ones must be closed)", open)
	}
	if closed := d.closedStmts(); closed != 26 {
		t.Fatalf("%d statements closed, want 26", closed)
	}
}

func TestStmtCacheEvictionNeverClosesAStatementInUse(t *testing.T) {
	st, d := cacheTestStore(t)
	st.stmts.max = 1
	ctx := context.Background()
	held, release := st.stmts.acquire(ctx, cacheTestSelect)
	if held == nil {
		t.Fatal("no statement")
	}
	// A second text evicts the first while a caller still holds it.
	other, releaseOther := st.stmts.acquire(ctx, `SELECT COUNT(*) FROM cache_t`)
	if other == nil {
		t.Fatal("no statement")
	}
	releaseOther()
	if st.stmts.size() != 1 {
		t.Fatalf("size = %d", st.stmts.size())
	}
	if d.closedStmts() != 0 {
		t.Fatal("the evicted statement was closed while a caller still held it")
	}
	var v string
	if err := held.QueryRowContext(ctx, 1).Scan(&v); err != nil || v != "v" {
		t.Fatalf("held statement after eviction: %q %v", v, err)
	}
	release()
	if d.closedStmts() != 1 {
		t.Fatalf("the evicted statement was not closed once released: %d closed", d.closedStmts())
	}
}

func TestStmtCacheSurvivesSchemaChange(t *testing.T) {
	st, d := cacheTestStore(t)
	q := `SELECT * FROM cache_t WHERE id=?`
	cols := func() []string {
		rows, err := st.query(q, 1)
		if err != nil {
			t.Fatalf("query after schema change: %v", err)
		}
		defer rows.Close()
		c, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		vals := make([]any, len(c))
		ptrs := make([]any, len(c))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if !rows.Next() {
			t.Fatalf("no row: %v", rows.Err())
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %d columns: %v", len(c), err)
		}
		return c
	}
	if c := cols(); len(c) != 2 {
		t.Fatalf("columns before: %v", c)
	}
	if _, err := st.exec(`ALTER TABLE cache_t ADD COLUMN extra TEXT DEFAULT 'x'`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`CREATE INDEX cache_t_v ON cache_t(v)`); err != nil {
		t.Fatal(err)
	}
	c := cols()
	if len(c) != 3 || c[2] != "extra" {
		t.Fatalf("columns after ALTER TABLE: %v (a stale cached statement kept the old shape)", c)
	}
	if n := d.preparesOf(q); n != 1 {
		t.Fatalf("the statement was prepared %d times; the test must exercise SQLite's own re-prepare of a cached one", n)
	}
	// And a write through a cached statement across a drop/recreate of the table.
	ins := `INSERT INTO cache_t(id,v) VALUES(?,?)`
	if _, err := st.exec(ins, 100, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`DROP TABLE cache_t`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(`CREATE TABLE cache_t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.exec(ins, 101, "b"); err != nil {
		t.Fatalf("insert through a statement cached before the table was recreated: %v", err)
	}
	var n int
	if err := st.queryRow(`SELECT COUNT(*) FROM cache_t`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}
}

func TestStmtCacheContextCancellationLeavesNothingBroken(t *testing.T) {
	st, _ := cacheTestStore(t)
	heavy := `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<?) SELECT COUNT(*) FROM c`
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					time.Sleep(time.Duration(i%3) * 100 * time.Microsecond)
					cancel()
				}()
				var n int
				_ = st.queryRowContext(ctx, heavy, 200000).Scan(&n)
				cancel()
			}
		}()
	}
	wg.Wait()
	var v string
	if err := st.queryRow(cacheTestSelect, 1).Scan(&v); err != nil || v != "v" {
		t.Fatalf("normal query after cancellations: %q %v", v, err)
	}
	var n int
	if err := st.queryRow(heavy, 1000).Scan(&n); err != nil || n != 1000 {
		t.Fatalf("the statement cancelled mid-query is broken for the next caller: %d %v", n, err)
	}
}

func TestStmtCacheInTransactions(t *testing.T) {
	st, d := cacheTestStore(t)
	ctx := context.Background()
	// Warm the cache outside any transaction.
	var v string
	if err := st.queryRow(cacheTestSelect, 1).Scan(&v); err != nil {
		t.Fatal(err)
	}
	upd := `UPDATE cache_t SET v=? WHERE id=?`
	if _, err := st.exec(upd, "v", 1); err != nil {
		t.Fatal(err)
	}

	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.txExecContext(ctx, tx, upd, "tx1", 1); err != nil {
		t.Fatal(err)
	}
	if err := st.txQueryRowContext(ctx, tx, cacheTestSelect, 1).Scan(&v); err != nil || v != "tx1" {
		t.Fatalf("read own write in tx: %q %v", v, err)
	}
	rows, err := st.txQueryContext(ctx, tx, cacheTestSelect, 1)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := st.queryRow(cacheTestSelect, 1).Scan(&v); err != nil || v != "tx1" {
		t.Fatalf("after commit: %q %v", v, err)
	}

	tx, err = st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.txExecContext(ctx, tx, upd, "rolled", 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := st.queryRow(cacheTestSelect, 1).Scan(&v); err != nil || v != "tx1" {
		t.Fatalf("after rollback: %q %v", v, err)
	}
	for _, q := range []string{cacheTestSelect, upd} {
		if n := d.preparesOf(q); n != 1 {
			t.Fatalf("%q prepared %d times across pool and transactions, want 1", q, n)
		}
		if n := d.directOf(q); n != 0 {
			t.Fatalf("%q took the unprepared path %d times", q, n)
		}
	}
	// A text nobody has cached yet runs plainly in a transaction (it holds the only connection, so
	// preparing on the pool would deadlock) and stays uncached; the pool caches it later.
	fresh := `SELECT COUNT(*) FROM cache_t WHERE id>?`
	tx, err = st.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.txQueryRowContext(ctx, tx, fresh, 0).Scan(&n); err != nil || n != 50 {
		t.Fatalf("%d %v", n, err)
	}
	tx.Commit()
	if got := d.preparesOf(fresh); got != 0 {
		t.Fatalf("a transaction prepared %d statements on the pool", got)
	}
	if err := st.queryRow(fresh, 0).Scan(&n); err != nil || n != 50 {
		t.Fatalf("%d %v", n, err)
	}
	if got := d.preparesOf(fresh); got != 1 {
		t.Fatalf("pool call prepared %d times", got)
	}
}

func TestStmtCacheConcurrentCallersPrepareOnce(t *testing.T) {
	st, d := cacheTestStore(t)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				var v string
				if err := st.queryRow(cacheTestSelect, 1+(g+i)%50).Scan(&v); err != nil {
					t.Error(err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if n := d.preparesOf(cacheTestSelect); n != 1 {
		t.Fatalf("prepared %d times under concurrency, want 1", n)
	}
}

func TestStoreCloseClosesEveryCachedStatement(t *testing.T) {
	st, d := newSpyStore(t)
	if _, err := st.db.Exec(`CREATE TABLE cache_t(id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		var n int
		if err := st.queryRow(`SELECT COUNT(*) FROM cache_t WHERE id>?+`+strings.Repeat("0+", i)+`0`, 0).Scan(&n); err != nil {
			t.Fatal(err)
		}
	}
	if st.stmts.size() != 5 || d.openStmts() != 5 {
		t.Fatalf("cached %d, open %d, want 5 and 5", st.stmts.size(), d.openStmts())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if d.openStmts() != 0 || st.stmts.size() != 0 {
		t.Fatalf("after Close: %d statements still open, %d cached", d.openStmts(), st.stmts.size())
	}
	// A closed store does not panic or resurrect the cache.
	var n int
	if err := st.queryRow(`SELECT 1`).Scan(&n); err == nil {
		t.Fatal("query on a closed store succeeded")
	}
}

func TestStmtCacheIsBypassedOnPostgres(t *testing.T) {
	if c := newStmtCache("postgres", nil); c != nil {
		t.Fatal("PostgreSQL got a statement cache")
	}
	if c := newStmtCache("sqlite", &sql.DB{}); c == nil {
		t.Fatal("SQLite got no statement cache")
	}
	// A nil cache is the PostgreSQL path: acquire declines and close is a no-op.
	var c *stmtCache
	if st, release := c.acquire(context.Background(), cacheTestSelect); st != nil || release != nil {
		t.Fatal("a nil cache handed out a statement")
	}
	c.close()
	if c.size() != 0 {
		t.Fatal("nil cache has a size")
	}
	// A PostgreSQL store carries no cache, and a placeholder query through every entry point reaches
	// the driver rewritten once and unprepared.
	pg, rec := recordStore(t, "postgres")
	if pg.stmts != nil {
		t.Fatal("PostgreSQL store has a statement cache")
	}
	checkEntryPointsRewrite(t, "postgres")
	_ = rec
}

// A text that only ever runs inside transactions (the backend request relay) cannot be prepared by
// the transaction that first meets it, because that transaction holds the only connection. It is
// noted, prepared by the next pool-level call, and then every later transaction reuses it.
func TestStmtCacheWarmsTextsFirstSeenInTransactions(t *testing.T) {
	st, d := cacheTestStore(t)
	ctx := context.Background()
	q := `SELECT COUNT(*) FROM cache_t WHERE id<?`
	inTx := func() {
		tx, err := st.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var n int
		if err := st.txQueryRowContext(ctx, tx, q, 10).Scan(&n); err != nil || n != 9 {
			t.Fatalf("%d %v", n, err)
		}
	}
	inTx()
	if d.preparesOf(q) != 0 {
		t.Fatal("the transaction prepared on the pool")
	}
	var one int
	if err := st.queryRow(`SELECT 1`).Scan(&one); err != nil { // any pool-level call drains the notes
		t.Fatal(err)
	}
	if d.preparesOf(q) != 1 {
		t.Fatalf("the noted text was prepared %d times by the next pool call, want 1", d.preparesOf(q))
	}
	directBefore := d.directOf(q)
	inTx()
	inTx()
	if d.preparesOf(q) != 1 || d.directOf(q) != directBefore {
		t.Fatalf("later transactions did not reuse the cached statement: prepares=%d direct=%d->%d", d.preparesOf(q), directBefore, d.directOf(q))
	}
}

func TestStmtCachePendingNotesAreBounded(t *testing.T) {
	st, _ := cacheTestStore(t)
	for i := 0; i < 10*stmtPendingCap; i++ {
		st.stmts.want(fmt.Sprintf("SELECT %d", i))
	}
	st.stmts.mu.Lock()
	n := len(st.stmts.pending)
	st.stmts.mu.Unlock()
	if n > stmtPendingCap {
		t.Fatalf("%d pending notes, cap %d", n, stmtPendingCap)
	}
}

// Draining the notes must cost the unrelated caller at most one prepare, and a caller that is already
// cancelled must neither lose the notes nor be what they are prepared under.
func TestStmtCacheDrainIsOnePerCallAndSurvivesCancelledCaller(t *testing.T) {
	st, d := cacheTestStore(t)
	texts := []string{
		`SELECT COUNT(*) FROM cache_t WHERE id<?`,
		`SELECT COUNT(*) FROM cache_t WHERE id>?`,
		`SELECT COUNT(*) FROM cache_t WHERE id=?`,
	}
	for _, q := range texts {
		st.stmts.want(q)
	}
	prepared := func() int {
		n := 0
		for _, q := range texts {
			n += d.preparesOf(q)
		}
		return n
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	st.stmts.drainPending(cancelled)
	if n := prepared(); n != 1 {
		t.Fatalf("one drain by a cancelled caller prepared %d texts, want exactly 1", n)
	}
	st.stmts.drainPending(context.Background())
	st.stmts.drainPending(context.Background())
	if n := prepared(); n != 3 {
		t.Fatalf("three drains prepared %d texts, want 3", n)
	}
}
