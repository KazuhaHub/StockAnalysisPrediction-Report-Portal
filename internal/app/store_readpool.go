package app

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"time"
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

// readPool is the SQLite read pool and its statement cache, swapped in and out as one value so a
// reader sees either a whole pool or none.
type readPool struct {
	db    *sql.DB
	stmts *stmtCache
}

// readPoolOrNil is the current read pool, or nil where there is none (PostgreSQL, in-memory, disabled,
// closed). It is the one place a reader looks, and the pointer is atomic: Close and the tests'
// closeReadPool swap it while requests are in flight.
func (s *Store) readPoolOrNil() *readPool { return s.rp.Load() }

// readerPool is the read pool, or the writer pool where there is none.
func (s *Store) readerPool() poolRef {
	if rp := s.rp.Load(); rp != nil {
		return poolRef{db: rp.db, stmts: rp.stmts}
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
	rp := s.rp.Load()
	if rp == nil {
		return s.poolExec()
	}
	return migExec{s: s, ex: rp.db, rp: rp}
}

// beginRead begins a read transaction on the read pool (the writer pool where there is none). The
// transaction is the reader's snapshot: every statement in it sees the same committed state.
func (s *Store) beginRead(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	tx, _, err := s.beginReadPool(ctx, opts)
	return tx, err
}

// beginReadPool is beginRead that also returns the pool the transaction began on (nil for the writer
// pool), so a caller that wraps it in a migExec uses that pool's statement cache.
func (s *Store) beginReadPool(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, *readPool, error) {
	if rp := s.rp.Load(); rp != nil {
		tx, err := rp.db.BeginTx(ctx, opts)
		return tx, rp, err
	}
	tx, err := s.db.BeginTx(ctx, opts)
	return tx, nil, err
}

// readScan runs a one-row read on the read pool and, if the pool fails for a reason other than "no
// such row", runs it again on the writer. The settings and account reads that decide whether a request
// is allowed, a queue is paused or a feature is on are written to treat an error as "use the default" or
// "not found"; a read-pool failure (a connection that cannot open, a pool closed under it) must not turn
// into that. do receives the Scan of the row and returns its error. The first failure is logged once.
func (s *Store) readScan(q string, args []any, do func(scan func(dest ...any) error) error) error {
	err := do(s.readQueryRow(q, args...).Scan)
	if err == nil || errors.Is(err, sql.ErrNoRows) || s.rp.Load() == nil {
		return err
	}
	s.readFallbackOnce.Do(func() {
		log.Printf("sqlite read pool: a read failed (%v); retrying on the writer connection (logged once)", err)
	})
	return do(s.queryRow(q, args...).Scan)
}

// readPoolSourceFile is the file a SQLite source opens, resolved the way the driver resolves it, so the
// read pool opens the same file as the writer. A plain name is cut at the first '?' (a '?' in the first
// position is part of the name), exactly as the driver does; a "file:" URI is the part between the scheme
// and the query, percent-decoded, without the empty authority of "file:///".
func sqliteSourceFile(source string) (file, query string) {
	if pos := strings.IndexByte(source, '?'); pos >= 1 {
		file, query = source[:pos], source[pos+1:]
	} else {
		file = source
	}
	if strings.HasPrefix(file, "file:") {
		raw := strings.TrimPrefix(file, "file:")
		if strings.HasPrefix(raw, "//") {
			raw = raw[2:]
			if i := strings.IndexByte(raw, '/'); i >= 0 {
				raw = raw[i:] // drop the authority ("" or "localhost")
			}
		}
		if dec, err := url.PathUnescape(raw); err == nil {
			raw = dec
		}
		return raw, query
	}
	return file, query
}

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
	if !strings.HasPrefix(source, "file:") {
		// The driver opens a plain name up to its first '?' and reads the rest as its own parameters;
		// the read pool says the same thing as a URI, so the characters a URI treats specially in the
		// name are escaped and the operator's parameters travel unchanged.
		name, query := sqliteSourceFile(source)
		uri = "file:" + strings.NewReplacer("%", "%25", "#", "%23", "?", "%3f", " ", "%20").Replace(name)
		if query != "" {
			uri += "?" + query
		}
	}
	uri = sqliteSource(uri) // the same busy timeout the writer carries, unless the source names one
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	}
	return uri + sep + "mode=ro&_pragma=query_only(1)", true
}

// Read pool states, as reported by PoolState.
const (
	readPoolOpen          = "open"
	readPoolDisabled      = "disabled"
	readPoolNotApplicable = "not_applicable"
	readPoolClosed        = "closed"
)

// setReadPoolStatus records why the pool is in the state it is in, for PoolState.
func (s *Store) setReadPoolStatus(state, reason string) {
	s.rpMu.Lock()
	s.rpState, s.rpReason = state, reason
	s.rpMu.Unlock()
}

// disableReadPool leaves every read on the writer, says why once in the log, and records the reason.
func (s *Store) disableReadPool(reason string) {
	log.Printf("sqlite read pool disabled: %s; reads stay on the single connection", reason)
	s.setReadPoolStatus(readPoolDisabled, reason)
}

// openReadPool puts the database in WAL mode and opens the read pool on it. It is a no-op, leaving every
// read on the writer, for PostgreSQL, for a source that cannot be shared, for a filesystem that cannot
// run WAL safely, and whenever the file does not end up in WAL. Each of those is recorded with its
// reason (PoolState) and, where an operator could act on it, logged.
//
// WAL needs shared memory that every process on the file maps. The Unix VFS always has some, so
// "PRAGMA journal_mode=WAL" answers "wal" on a network mount or a FUSE or virtualized volume too, and
// the trouble shows up later, inside requests (locking that does not work, an index that is not
// shared). The answer to the pragma therefore proves nothing; the filesystem is asked first, by type,
// and an unknown or undetectable one gets no pool and no WAL (the fail-safe).
func (s *Store) openReadPool(source string) {
	if s.driver != "sqlite" {
		s.setReadPoolStatus(readPoolNotApplicable, "")
		return
	}
	if s.rp.Load() != nil {
		return
	}
	roSource, ok := sqliteReadSource(source)
	if !ok {
		s.setReadPoolStatus(readPoolNotApplicable, "in-memory database or a source that sets its own mode")
		return
	}
	file, _ := sqliteSourceFile(source)
	fsName, unsafeFS, err := sqliteFilesystemCheck(file)
	switch {
	case err != nil:
		s.disableReadPool("the filesystem of " + file + " could not be identified (" + err.Error() + ")")
		return
	case unsafeFS:
		s.disableReadPool("the database is on a " + fsName + " filesystem, which cannot run WAL safely")
		return
	}
	s.walPath = file + "-wal"
	s.fsName = fsName
	// A source that names a journal mode is the operator's choice: honour it, and use the pool only if
	// it is WAL. The mode is persistent in the file, so it is set once, here, not on every connection.
	pragma := "PRAGMA journal_mode=WAL"
	if strings.Contains(source, "journal_mode") {
		pragma = "PRAGMA journal_mode"
	}
	var mode string
	if err := s.db.QueryRow(pragma).Scan(&mode); err != nil || !strings.EqualFold(mode, "wal") {
		s.disableReadPool("journal mode is " + mode + " (" + errString(err) + ")")
		return
	}
	rdb, err := sql.Open(portalSQLiteDriver, roSource)
	if err != nil {
		s.disableReadPool(err.Error())
		return
	}
	rdb.SetMaxOpenConns(sqliteReadPoolSize)
	rdb.SetMaxIdleConns(sqliteReadPoolSize) // keep them open: reopening a connection re-runs the pragmas
	if err := rdb.Ping(); err != nil {
		rdb.Close()
		s.disableReadPool(err.Error())
		return
	}
	s.rp.Store(&readPool{db: rdb, stmts: newStmtCache(s.driver, rdb)})
	s.setReadPoolStatus(readPoolOpen, "")
	s.startWALMaintenance()
}

func errString(err error) string {
	if err == nil {
		return "no error"
	}
	return err.Error()
}

// closeReadPool closes the read pool and sends reads back to the writer. It is safe while reads are in
// flight: the pool is swapped out first, so a new read takes the writer, and a read that already holds the
// old pool finishes or fails with "sql: database is closed" and never touches freed state. Close calls it,
// and a test uses it to measure the single-connection shape.
func (s *Store) closeReadPool() {
	s.stopWALMaintenance()
	rp := s.rp.Swap(nil)
	if rp == nil {
		return
	}
	s.setReadPoolStatus(readPoolClosed, "")
	rp.stmts.close()
	rp.db.Close()
}

// ---------- WAL size ----------

// sqliteJournalSizeLimit is how large the write-ahead log may stay after a checkpoint has emptied it:
// SQLite truncates the file to this size when the log restarts, instead of leaving it at its high-water
// mark for the life of the process. Overlapping readers can keep a checkpoint from finishing during a
// burst, so the log can grow past this and is brought back by the maintenance below.
var sqliteJournalSizeLimit int64 = 64 << 20

// walCheckpointInterval is how often the maintenance checkpoints the log on the writer connection.
var walCheckpointInterval = 30 * time.Second

// checkpointWAL runs one maintenance pass on the writer connection. A passive checkpoint copies what
// no reader still needs back into the database file and never waits on a reader, so it cannot stall a
// request; it is the retry for the automatic checkpoint, which a steady overlap of short readers can
// keep from finishing. If the log is then fully copied and larger than sqliteJournalSizeLimit, a
// truncating checkpoint empties it and returns its disk space. It reports the log size in frames before
// the pass and in bytes after it.
func (s *Store) checkpointWAL() (frames int64, bytes int64, err error) {
	var busy, logFrames, done int64
	if err = s.db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &logFrames, &done); err != nil {
		return 0, s.WALBytes(), err
	}
	if logFrames >= 0 && done == logFrames && s.WALBytes() > sqliteJournalSizeLimit {
		err = s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &done)
	}
	return logFrames, s.WALBytes(), err
}

// WALBytes is the size of the write-ahead log file, 0 where there is none.
func (s *Store) WALBytes() int64 {
	if s.walPath == "" {
		return 0
	}
	info, err := os.Stat(s.walPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

func (s *Store) startWALMaintenance() {
	s.rpMu.Lock()
	defer s.rpMu.Unlock()
	if s.walStop != nil {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.walStop, s.walDone = stop, done
	interval := walCheckpointInterval
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if _, _, err := s.checkpointWAL(); err != nil {
					s.walErrOnce.Do(func() { log.Printf("sqlite wal checkpoint failed: %v (logged once)", err) })
				}
			}
		}
	}()
}

func (s *Store) stopWALMaintenance() {
	s.rpMu.Lock()
	stop, done := s.walStop, s.walDone
	s.walStop, s.walDone = nil, nil
	s.rpMu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
}

// ---------- state ----------

// PoolConnections is one pool's database/sql statistics.
type PoolConnections struct {
	Max       int     `json:"max"`
	Open      int     `json:"open"`
	InUse     int     `json:"in_use"`
	Idle      int     `json:"idle"`
	WaitCount int64   `json:"wait_count"`
	WaitMs    float64 `json:"wait_ms"`
}

func poolConnections(db *sql.DB) PoolConnections {
	st := db.Stats()
	return PoolConnections{Max: st.MaxOpenConnections, Open: st.OpenConnections, InUse: st.InUse, Idle: st.Idle,
		WaitCount: st.WaitCount, WaitMs: float64(st.WaitDuration) / float64(time.Millisecond)}
}

// StorePoolState is what an operator or the soak reads to see how the database is being served: the
// read pool's state with the reason when it is off, both pools' connection statistics and the size of
// the write-ahead log.
type StorePoolState struct {
	Driver     string           `json:"driver"`
	ReadPool   string           `json:"read_pool"`
	Reason     string           `json:"reason,omitempty"`
	Filesystem string           `json:"filesystem,omitempty"`
	Writer     PoolConnections  `json:"writer"`
	Reader     *PoolConnections `json:"reader,omitempty"`
	WALBytes   int64            `json:"wal_bytes"`
}

// PoolState reports the pools. It reads counters only and takes no connection.
func (s *Store) PoolState() StorePoolState {
	s.rpMu.Lock()
	state, reason := s.rpState, s.rpReason
	s.rpMu.Unlock()
	if state == "" {
		state = readPoolNotApplicable
	}
	out := StorePoolState{Driver: s.driver, ReadPool: state, Reason: reason, Filesystem: s.fsName,
		Writer: poolConnections(s.db), WALBytes: s.WALBytes()}
	if rp := s.rp.Load(); rp != nil {
		r := poolConnections(rp.db)
		out.Reader = &r
	}
	return out
}
