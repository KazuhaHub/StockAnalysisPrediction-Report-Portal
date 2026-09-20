package app

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The migration ledger.
//
// The base schema is the ACCEPTANCE contract (ADR 0034): a database must already satisfy
// baseSchemaStmts to be opened at all, and it is frozen for this compatibility window. That left
// every later addition with two options — move the baseline, or add nothing — which is why this
// exists: the baseline stays frozen and everything a release adds after it is an ordered,
// forward-only, idempotent step, applied once and recorded in `meta`.
//
// What a step must be, and why:
//
//   - FORWARD-ONLY and never edited after release. A database that already ran a step will not run it
//     again, so editing one changes the shape of new databases and leaves upgraded ones on the old
//     shape for ever. A step that turns out wrong is corrected by a later step.
//   - IDEMPOTENT, guarded by an existence probe rather than by matching a driver's "duplicate column"
//     error, so a process that died between the DDL and the ledger row can simply run it again.
//   - DECLARED. A step says which columns, tables and indices it guarantees, and the runner verifies
//     them on every start — for steps the ledger says are applied as well. "Recorded" is not
//     evidence: without this, a column dropped by hand would start up looking migrated.
//   - ONE TRANSACTION, one connection. On SQLite the pool is a single connection (store.go), so a
//     step that probed the schema through *Store while its own transaction held that connection would
//     deadlock rather than merely wait. Steps therefore get a migExec, which carries the transaction.
//
// The ledger lives in `meta` rather than in a table of its own because `meta` is already in the
// frozen baseline: a new ledger table would be an acceptance-contract change of its own, which is the
// thing this machinery exists to avoid.

// migrationLedgerPrefix namespaces the ledger rows among the other meta keys.
const migrationLedgerPrefix = "mig:"

func migrationLedgerKey(id string) string { return migrationLedgerPrefix + id }

// execer is the slice of database/sql both *sql.DB and *sql.Tx satisfy.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// migExec is one connection's worth of statements with the store's placeholder rewriting. Everything
// a step does — probing, DDL, verifying, recording — goes through one of these, so all of it happens
// inside the step's transaction and on its connection.
type migExec struct {
	s  *Store
	ex execer
}

func (s *Store) poolExec() migExec { return migExec{s: s, ex: s.db} }

func (e migExec) exec(q string, args ...any) (sql.Result, error) {
	return e.ex.Exec(e.s.bind(q), args...)
}

func (e migExec) queryRow(q string, args ...any) *sql.Row {
	return e.ex.QueryRow(e.s.bind(q), args...)
}

func (e migExec) query(q string, args ...any) (*sql.Rows, error) {
	return e.ex.Query(e.s.bind(q), args...)
}

func (e migExec) setting(k string) string {
	var v sql.NullString
	if err := e.queryRow(`SELECT v FROM meta WHERE k=?`, k).Scan(&v); err == nil && v.Valid {
		return v.String
	}
	return ""
}

func (e migExec) setSetting(k, v string) error {
	_, err := e.exec(`INSERT INTO meta(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

func (e migExec) tableExists(name string) bool {
	var n int
	if e.s.driver == "postgres" {
		e.queryRow(`SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema='public' AND table_name=?`, name).Scan(&n)
	} else {
		e.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	}
	return n > 0
}

func (e migExec) indexExists(name string) bool {
	var n int
	if e.s.driver == "postgres" {
		e.queryRow(`SELECT COUNT(*) FROM pg_indexes WHERE schemaname='public' AND indexname=?`, name).Scan(&n)
	} else {
		e.queryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&n)
	}
	return n > 0
}

func (e migExec) columnExists(table, col string) bool {
	if e.s.driver == "postgres" {
		var n int
		e.queryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name=? AND column_name=?`, table, col).Scan(&n)
		return n > 0
	}
	// Only ever called with hardcoded internal identifiers, so inlining the table name is safe.
	rows, err := e.query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == col {
			found = true
		}
	}
	return found
}

// ---------- what a step declares ----------

// colRef is a column a step guarantees, by table.
type colRef struct{ table, name string }

// restoreRule says how a dump written BEFORE this step may be loaded into a build that has it. The
// zero value refuses, which is the honest default: a step that has not said otherwise does not let an
// older dump in, and the operator is told to restore with the release that wrote it.
type restoreRule struct {
	// olderDumpOK means the columns, tables and data this step adds may simply be absent from a dump
	// written before it — true when they are nullable or defaulted and nothing has to be back-filled.
	// A step that back-fills data must leave it false: a dump without the column is also a dump
	// without the data, so loading it would leave rows that were never upgraded and produce a
	// database that never existed anywhere.
	olderDumpOK bool
	// needsDataUpgrade means loading a dump from a lower level requires this step's DATA work to run
	// over the restored rows. No step declares it yet; the restore refuses such a dump rather than
	// guessing (see checkDumpMigrations).
	needsDataUpgrade bool
}

// migration is one schema step.
type migration struct {
	id      string
	columns []colRef
	tables  []string
	indices []string
	restore restoreRule
	up      func(migExec) error
}

// migrations is the ordered list this build knows. Append only; never edit a released step.
func (s *Store) migrations() []migration { return []migration{migration0001()} }

// migration0001 adds the per-OU security policy columns (the login-protection settings).
//
// All three are nullable with no default, and NULL means "inherit from the OU above" — the same
// convention the run-governance columns on this table already use. No data is touched, so a dump
// written before it may be loaded: the columns simply come back empty, which is what "inherit" is.
func migration0001() migration {
	cols := []colRef{
		{table: "user_groups", name: "totp_enroll"},
		{table: "user_groups", name: "passkey_enroll"},
		{table: "user_groups", name: "require_2fa"},
	}
	return migration{
		id:      "0001",
		columns: cols,
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for _, c := range cols {
				if e.columnExists(c.table, c.name) {
					continue // already there: a retry after a crash, or a hand-added column
				}
				if _, err := e.exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.name + ` INTEGER`); err != nil {
					return fmt.Errorf("add %s.%s: %w", c.table, c.name, err)
				}
			}
			return nil
		},
	}
}

// ---------- running them ----------

// runMigrations applies what this build knows and this database has not recorded, then verifies every
// step's declared products.
//
// The "written by a newer release" check belongs HERE rather than in the runner below: it is a
// judgement about the database as a whole, made once at startup against the full list this build
// knows. The runner is also driven directly by tests with steps of their own, and a subset list is
// not a statement that the other steps do not exist.
func (s *Store) runMigrations() error {
	steps := s.migrations()
	known := map[string]bool{}
	for _, m := range steps {
		known[m.id] = true
	}
	if err := s.refuseUnknownLedgerRows(known); err != nil {
		return err
	}
	return s.runMigrationSteps(steps)
}

// runMigrationSteps is the runner itself, taking the list so the framework's contract can be tested
// without a synthetic step in the real list.
func (s *Store) runMigrationSteps(steps []migration) error {
	for _, m := range steps {
		if err := s.applyMigration(m); err != nil {
			return err
		}
	}
	return s.verifyMigrationProducts(s.poolExec(), steps)
}

// refuseUnknownLedgerRows refuses a database whose ledger names a step this build does not have. The
// same rule the generation marker applies: what wrote it knew things this build does not, so it must
// not be operated on.
func (s *Store) refuseUnknownLedgerRows(known map[string]bool) error {
	return refuseUnknownLedgerRowsExec(s.poolExec(), known)
}

func refuseUnknownLedgerRowsExec(e migExec, known map[string]bool) error {
	rows, err := e.query(`SELECT k FROM meta WHERE k LIKE ?`, migrationLedgerPrefix+"%")
	if err != nil {
		return err
	}
	defer rows.Close()
	var unknown []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return err
		}
		if id := strings.TrimPrefix(k, migrationLedgerPrefix); !known[id] {
			unknown = append(unknown, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(unknown) > 0 {
		return fmt.Errorf("this database records migration %s, which this build does not have: it was "+
			"written by a newer release. Start it with a release that knows that step, or restore a "+
			"backup taken before it", strings.Join(unknown, ", "))
	}
	return nil
}

// migrationIdsTx reads the ledger for a dump's header: the steps this build has that the database
// records as applied, in the order this build knows them. The order comes from the list, not from the
// rows, so a header is a sequence rather than a set.
func (s *Store) migrationIdsTx(tx *sql.Tx) []string {
	applied := map[string]bool{}
	rows, err := tx.Query(s.bind(`SELECT k FROM meta WHERE k LIKE ?`), migrationLedgerPrefix+"%")
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil {
			applied[strings.TrimPrefix(k, migrationLedgerPrefix)] = true
		}
	}
	var out []string
	for _, m := range s.migrations() {
		if applied[m.id] {
			out = append(out, m.id)
		}
	}
	return out
}

// backupTableNames is every table a dump has to carry: the baseline's tables, plus the ones a
// migration declares. Without the second half a migration that adds a table would quietly leave that
// table's data out of every backup — the kind of loss nobody notices until a restore.
func backupTableNames(base []string, steps []migration) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t == "" || seen[t] {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	for _, t := range base {
		add(t)
	}
	for _, m := range steps {
		for _, t := range m.tables {
			add(t)
		}
	}
	sort.Strings(out)
	return out
}

// ---------- what a dump may carry ----------

// checkDumpMigrations decides whether this build can load a dump that declares `declared` — the steps
// its data satisfies, recorded in the dump's header by the release that wrote it.
//
// The list has to be a contiguous prefix of the steps this build knows: it names a point in the
// sequence, and a list with a hole, a repeat, an unknown id or a different order names no such point.
// A dump that merely claims a step is not evidence that it carries one, which is why the columns are
// checked separately (optionalColumns says which ones may be absent, and everything else must be
// there).
func checkDumpMigrations(declared []string, steps []migration) error {
	if len(declared) > len(steps) {
		return fmt.Errorf("the backup declares %d migrations but this build has %d: it was written by "+
			"a newer release. Restore it with that release, or upgrade this one", len(declared), len(steps))
	}
	for i, id := range declared {
		if steps[i].id != id {
			if i > 0 && steps[i-1].id == id {
				return fmt.Errorf("the backup declares migration %s twice", id)
			}
			return fmt.Errorf("the backup declares migration %s, where this build's next step is %s: "+
				"the backup was not written by a release in this line", id, steps[i].id)
		}
	}
	// Everything the dump does not carry has to be loadable without it.
	for _, m := range steps[len(declared):] {
		if m.restore.needsDataUpgrade {
			return fmt.Errorf("the backup predates migration %s, whose data this build cannot upgrade "+
				"on restore: restore it with the release that wrote it, then back it up again", m.id)
		}
		if !m.restore.olderDumpOK {
			return fmt.Errorf("the backup predates migration %s and steps this build cannot apply to "+
				"restored data: restore it with the release that wrote it, then back it up again", m.id)
		}
	}
	return nil
}

// optionalColumns is what a dump may omit: the columns of steps it does not carry, where the step
// itself says an older dump is fine. A column of a step the dump DOES declare may never be missing —
// the header and the content have to agree, or a damaged backup would be loaded as an old one.
func optionalColumns(declared []string, steps []migration) map[string]map[string]bool {
	carried := map[string]bool{}
	for _, id := range declared {
		carried[id] = true
	}
	out := map[string]map[string]bool{}
	for _, m := range steps {
		if carried[m.id] || !m.restore.olderDumpOK {
			continue
		}
		for _, c := range m.columns {
			if out[c.table] == nil {
				out[c.table] = map[string]bool{}
			}
			out[c.table][c.name] = true
		}
	}
	return out
}

// applyMigration runs one step in one transaction: probe, DDL, verify, record. Any failure rolls the
// whole step back, so the ledger and the schema cannot disagree — a step is recorded if and only if
// it ran and its products are there.
func (s *Store) applyMigration(m migration) error {
	const attempts = 25
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		lastErr = s.applyMigrationOnce(m)
		if lastErr == nil {
			return nil
		}
		if !isContentionErr(lastErr) {
			return fmt.Errorf("migration %s: %w", m.id, lastErr)
		}
		// Another process is migrating the same database. Back off and look again rather than fail:
		// two instances starting together is a restart that overlapped, not a broken database.
		time.Sleep(time.Duration(20+attempt*10) * time.Millisecond)
	}
	return fmt.Errorf("migration %s: another process is migrating this database (%w)", m.id, lastErr)
}

func (s *Store) applyMigrationOnce(m migration) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	e := migExec{s: s, ex: tx}

	// The ledger is re-read INSIDE this transaction, so whoever gets the write lock second sees the
	// first one's row and does nothing. The exclusion that makes that the right test comes from one
	// place, deliberately: on Postgres `initSerialized` (store.go) holds an advisory lock for the whole
	// of init, and on SQLite the database has a single writer. A lock here as well would be a second
	// thing to get wrong, and would not cover a caller that ran migrations outside init anyway.
	if e.setting(migrationLedgerKey(m.id)) != "" {
		return nil // applied while we waited
	}
	if err := m.up(e); err != nil {
		return err
	}
	if err := s.verifyMigrationProducts(e, []migration{m}); err != nil {
		return err
	}
	// The record follows the work, never precedes it.
	if err := e.setSetting(migrationLedgerKey(m.id), time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}

// verifyMigrationProducts checks what every step declared. It runs for the whole list on every start
// — recorded or not — because the ledger says what was applied, not what is still there.
func (s *Store) verifyMigrationProducts(e migExec, steps []migration) error {
	for _, m := range steps {
		for _, c := range m.columns {
			if !e.columnExists(c.table, c.name) {
				return fmt.Errorf("migration %s declares %s.%s, which is missing: this database is "+
					"recorded as migrated but does not have that shape", m.id, c.table, c.name)
			}
		}
		for _, tbl := range m.tables {
			if !e.tableExists(tbl) {
				return fmt.Errorf("migration %s declares table %s, which is missing", m.id, tbl)
			}
		}
		for _, idx := range m.indices {
			if !e.indexExists(idx) {
				return fmt.Errorf("migration %s declares index %s, which is missing", m.id, idx)
			}
		}
	}
	return nil
}

// isContentionErr reports whether an error is another writer holding the database, rather than
// something wrong with the step. SQLite reports SQLITE_BUSY as "database is locked"; Postgres
// reports a serialization or deadlock failure with a 40xxx SQLSTATE.
func isContentionErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"database is locked", "database table is locked", "sqlite_busy",
		"deadlock detected", "could not serialize", "concurrent update",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return errors.Is(err, errContention)
}

// errContention marks a retryable failure raised by our own code (the tests use it to inject one).
var errContention = errors.New("database is locked")
