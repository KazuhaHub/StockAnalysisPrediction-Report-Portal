package app

import (
	"context"
	"database/sql"
	"log"
	"strings"
)

// sqliteReadPoolSize is the number of read-only connections beside the one writer connection. Small and
// bounded on purpose: the file is read by one process, a handler holds a reader for a few milliseconds,
// and every connection costs a page cache and a file handle. It is a constant, not a setting, because
// config.yaml carries infrastructure only and nothing an operator should tune.
const sqliteReadPoolSize = 4

// The SQLite read pool.
//
// SQLite ran on one connection, so every query of every request queued behind every other, including a
// reader behind a writer. In the soak the backend relay's two admitted requests alone kept that
// connection about a third occupied, and the reader pages missed their latency bounds (docs/app-platform/
// 171-capacity-envelope-plan.md, resolutions 73 and 76).
//
// The database is put in WAL mode and a second pool of read-only connections is opened on the same file.
// In WAL a reader never blocks the writer and the writer never blocks a reader: each read transaction
// sees the database as of the last commit that completed before it began. Two consequences carry the
// design:
//
//   - Read-your-writes across requests holds. A write that has returned (autocommit, or Commit) is
//     visible to every read that begins afterwards, on any connection, so a client that saves and then
//     reloads sees what it saved. What a reader cannot see is a write that is still uncommitted, which
//     is what a snapshot is for.
//   - Anything that must see its own transaction's writes, or that runs inside a write transaction,
//     stays on the writer. Routing is therefore explicit: a call site that is a standalone read-only
//     query says so by using a read* accessor below, and every other path (all writes, every
//     transaction, every read-modify-write, the schema work) keeps using the writer. There is no
//     inspection of the SQL to guess; a read that is not named here simply stays where it was.
//
// PostgreSQL has its own pool of ten and none of this applies: the read accessors there are the one
// pool, readDB is db, and no second pool, WAL or statement cache exists.

// poolRef is one pool and the statement cache prepared against it. A statement belongs to the pool it
// was prepared on, so the writer and the read pool each carry their own cache.
type poolRef struct {
	db    *sql.DB
	stmts *stmtCache
}

func (s *Store) writerPool() poolRef { return poolRef{db: s.db, stmts: s.stmts} }

// readerPool is the read pool, or the writer pool where there is none.
func (s *Store) readerPool() poolRef {
	if s.rdb != nil {
		return poolRef{db: s.rdb, stmts: s.rstmts}
	}
	return s.writerPool()
}

// poolOf is the pool (and statement cache) a *sql.DB belongs to.
func (s *Store) poolOf(db *sql.DB) poolRef {
	if s.rdb != nil && db == s.rdb {
		return s.readerPool()
	}
	return s.writerPool()
}

// readDB is the pool a standalone read-only query runs on.
func (s *Store) readDB() *sql.DB { return s.readerPool().db }

func (p poolRef) execBound(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if st, release := p.stmts.acquire(ctx, q); st != nil {
		defer release()
		return st.ExecContext(ctx, args...)
	}
	return p.db.ExecContext(ctx, q, args...)
}

func (p poolRef) queryBound(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if st, release := p.stmts.acquire(ctx, q); st != nil {
		defer release()
		return st.QueryContext(ctx, args...)
	}
	return p.db.QueryContext(ctx, q, args...)
}

func (p poolRef) queryRowBound(ctx context.Context, q string, args ...any) *sql.Row {
	if st, release := p.stmts.acquire(ctx, q); st != nil {
		defer release()
		return st.QueryRowContext(ctx, args...)
	}
	return p.db.QueryRowContext(ctx, q, args...)
}

func (p poolRef) txExecContext(ctx context.Context, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	if st, release := p.stmts.acquireExisting(q); st != nil {
		defer release()
		ts := tx.StmtContext(ctx, st)
		defer ts.Close()
		return ts.ExecContext(ctx, args...)
	}
	p.stmts.want(q)
	return tx.ExecContext(ctx, q, args...)
}

func (p poolRef) txQueryContext(ctx context.Context, tx *sql.Tx, q string, args ...any) (*sql.Rows, error) {
	if st, release := p.stmts.acquireExisting(q); st != nil {
		defer release()
		ts := tx.StmtContext(ctx, st)
		defer ts.Close()
		return ts.QueryContext(ctx, args...)
	}
	p.stmts.want(q)
	return tx.QueryContext(ctx, q, args...)
}

func (p poolRef) txQueryRowContext(ctx context.Context, tx *sql.Tx, q string, args ...any) *sql.Row {
	if st, release := p.stmts.acquireExisting(q); st != nil {
		defer release()
		ts := tx.StmtContext(ctx, st)
		defer ts.Close()
		return ts.QueryRowContext(ctx, args...)
	}
	p.stmts.want(q)
	return tx.QueryRowContext(ctx, q, args...)
}

// readQuery, readQueryRow and their context forms are query and queryRow on the read pool. They take the
// same "?" SQL and must only be given a standalone SELECT: no write, no statement that has to see
// another statement's uncommitted effect, nothing inside a transaction.
func (s *Store) readQuery(q string, args ...any) (*sql.Rows, error) {
	return s.readQueryContext(context.Background(), q, args...)
}

func (s *Store) readQueryRow(q string, args ...any) *sql.Row {
	return s.readQueryRowContext(context.Background(), q, args...)
}

func (s *Store) readQueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.readerPool().queryBound(ctx, s.bind(q), args...)
}

func (s *Store) readQueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return s.readerPool().queryRowBound(ctx, s.bind(q), args...)
}

// readExec is the read pool as an executor, for the readers written over a migExec so that one body
// serves a snapshot transaction and a standalone call. It is poolExec for reads: where there is no read
// pool it is poolExec itself.
func (s *Store) readExec() migExec {
	if s.rdb == nil {
		return s.poolExec()
	}
	return migExec{s: s, ex: s.rdb, ro: true}
}

// beginRead begins a read transaction on the read pool (the writer pool where there is none). The
// transaction is the reader's snapshot: every statement in it sees the same committed state. A caller
// that wraps it in a migExec must use readTxExec so its statements reuse the right statement cache.
func (s *Store) beginRead(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return s.readDB().BeginTx(ctx, opts)
}

// readTxExec wraps a transaction begun by beginRead.
func (s *Store) readTxExec(tx *sql.Tx) migExec { return migExec{s: s, ex: tx, ro: s.rdb != nil} }

// sqliteReadSource derives the read pool's source from the writer's: the same file opened read-only
// (mode=ro, and query_only so a statement that is not a read fails even if the open flag were ignored)
// with the same busy timeout. ok is false where a second connection cannot see the same database: an
// in-memory source, whose every connection is its own database, or a source that already chooses its
// own mode.
func sqliteReadSource(source string) (string, bool) {
	if source == "" || strings.Contains(source, ":memory:") || strings.Contains(source, "mode=") {
		return "", false
	}
	uri := source
	if !strings.HasPrefix(uri, "file:") {
		// A plain path becomes a URI, so the characters a URI treats specially are escaped.
		uri = "file:" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f", " ", "%20").Replace(source)
	}
	uri = sqliteSource(uri) // the same busy timeout the writer carries, unless the source names one
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	return uri + sep + "mode=ro&_pragma=query_only(1)", true
}

// openReadPool puts the database in WAL mode and opens the read pool on it. It is a no-op, leaving every
// read on the writer, for PostgreSQL, for a source that cannot be shared, and whenever the file does not
// end up in WAL: a filesystem without shared memory (a network mount) answers the pragma with the mode
// it kept, and read-only connections in a rollback-journal database would block the writer, which is
// the opposite of the point.
func (s *Store) openReadPool(source string) {
	if s.driver != "sqlite" || s.rdb != nil {
		return
	}
	roSource, ok := sqliteReadSource(source)
	if !ok {
		return
	}
	// A source that names a journal mode is the operator's choice: honour it, and use the pool only if
	// it is WAL. The mode is persistent in the file, so it is set once, here, not on every connection.
	pragma := "PRAGMA journal_mode=WAL"
	if strings.Contains(source, "journal_mode") {
		pragma = "PRAGMA journal_mode"
	}
	var mode string
	if err := s.db.QueryRow(pragma).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		log.Printf("sqlite read pool disabled: journal mode is %q (%v); reads stay on the single connection", mode, err)
		return
	}
	rdb, err := sql.Open(portalSQLiteDriver, roSource)
	if err != nil {
		log.Printf("sqlite read pool disabled: %v", err)
		return
	}
	rdb.SetMaxOpenConns(sqliteReadPoolSize)
	rdb.SetMaxIdleConns(sqliteReadPoolSize) // keep them open: reopening a connection re-runs the pragmas
	if err := rdb.Ping(); err != nil {
		rdb.Close()
		log.Printf("sqlite read pool disabled: %v", err)
		return
	}
	s.rdb = rdb
	s.rstmts = newStmtCache(s.driver, rdb)
}

// closeReadPool closes the read pool and sends reads back to the writer. It must not run while a read
// is in flight; Close calls it, and a test uses it to measure the single-connection shape.
func (s *Store) closeReadPool() {
	if s.rdb == nil {
		return
	}
	s.rstmts.close()
	s.rdb.Close()
	s.rdb, s.rstmts = nil, nil
}
