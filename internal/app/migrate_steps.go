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
	id string
	// explicit forbids implicit upgrade of an existing database at server startup.
	explicit bool
	columns  []colRef
	tables   []string
	indices  []string
	restore  restoreRule
	up       func(migExec) error
	verify   func(migExec) error
}

// migrations is the ordered list this build knows. Append only; never edit a released step.
func (s *Store) migrations() []migration {
	return []migration{migration0001(), migration0002(), migration0003(), migration0004(), migration0005(),
		migration0006(), migration0007()}
}

// The owner-approved purge ownership evidence is empty on upgrade. Historic global markers do
// not prove OU membership, so this step never backfills them. Tier 2 provenance has not shipped;
// its previously proposed 0007 id must move to the next free id if that work is later approved.
func migration0007() migration {
	return migration{
		id: "0007", explicit: true,
		tables:  []string{"prediction_purge_owners"},
		columns: []colRef{{"prediction_purge_owners", "rtype"}, {"prediction_purge_owners", "cohort"}, {"prediction_purge_owners", "attributed_ou"}},
		restore: restoreRule{olderDumpOK: true},
		up:      createPredictionPurgeOwners,
		verify:  verifyPredictionPurgeOwners,
	}
}

// The forecast-verification ledger: prediction_sets, prediction_claims, prediction_marks (ADR 0029).
//
// Three tables rather than columns on an existing one, and the reason is the one thing this feature
// cannot work around. `tracking_items` is DELETEd and re-INSERTed on every ingest (SetTracking), so
// any machine-computed outcome stored there is erased by the next run of the same report; `reports` is
// upserted on identity, so it cannot hold more than one set per report. A set is a thing with claims
// and marks hanging off it, not a property of either.
//
// The tables are new and empty, so an older dump restores cleanly: it simply has none of this, which
// is what "not recorded yet" means. Every column is therefore nullable or defaulted.
//
// Prices are integer fen and probabilities integer basis points, never REAL. The store is dual-driver,
// where REAL is an 8-byte double on SQLite and a 4-byte float4 on Postgres, so a boundary comparison
// could resolve differently per driver in the one table whose whole purpose is being citable.
func migration0004() migration {
	return migration{
		id: string(predictionMigrationID),
		// The cleanup_runs column is declared so verifyMigrationProducts checks it on every start:
		// "recorded" is not evidence, and a column dropped by hand would otherwise start up looking
		// migrated.
		columns: []colRef{{table: "cleanup_runs", name: "predictions_deleted"}},
		tables: []string{
			"prediction_sets", "prediction_claims", "prediction_marks", "prediction_purges",
		},
		indices: []string{
			"idx_pred_sets_idem", "idx_pred_sets_group", "idx_pred_sets_symbol",
			"idx_pred_claims_key", "idx_pred_marks_cp", "idx_pred_marks_due", "idx_pred_purges_month",
		},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			pk := e.s.pkAuto()
			// cleanup_runs is a BASELINE table, so the ledger's cleanup target cannot be given its own
			// column by declaring it in baseSchemaStmts() — that shape is the acceptance contract, and
			// adding to it would refuse every existing database. It is added here instead, guarded by
			// the existence probe the framework requires so a retry after a crash is a no-op.
			if !e.columnExists("cleanup_runs", "predictions_deleted") {
				if _, err := e.exec(`ALTER TABLE cleanup_runs ADD COLUMN predictions_deleted INTEGER DEFAULT 0`); err != nil {
					return err
				}
			}
			for _, q := range []string{
				// report_id is NULLed, never deleted, when its report is purged: the ledger outlives
				// reports because the reports retention floor is 365 days while h12 resolves twelve
				// months after a set is made, so a floor-scoped ledger would lose every set on the day
				// its long-horizon mark became answerable.
				//
				// scenario_basis is the one contract field kept as a COLUMN rather than echoed into the
				// payload, and the test is whether the statistics group on it. It says how the scenario
				// prices came to be — today always "estimated", meaning the numbers were projected from
				// a model rather than reported — and it is the same class of field as probability_method
				// and decision_quality, which are columns for the same reason. Everything else the
				// producer says about provenance is audit detail with no query against it, so it stays
				// in `payload`, which is stored verbatim.
				`CREATE TABLE IF NOT EXISTS prediction_sets(
					id ` + pk + `,
					report_id BIGINT,
					symbol TEXT NOT NULL DEFAULT '',
					rtype TEXT NOT NULL DEFAULT '',
					version TEXT NOT NULL DEFAULT '',
					title TEXT NOT NULL DEFAULT '',
					report_date TEXT NOT NULL DEFAULT '',
					made_at TEXT NOT NULL,
					made_on TEXT NOT NULL,
					cohort TEXT NOT NULL,
					zone TEXT NOT NULL DEFAULT 'Asia/Shanghai',
					state TEXT NOT NULL,
					errors TEXT NOT NULL DEFAULT '',
					payload TEXT NOT NULL DEFAULT '',
					payload_truncated INTEGER NOT NULL DEFAULT 0,
					payload_sha256 TEXT NOT NULL DEFAULT '',
					body_sha256 TEXT NOT NULL DEFAULT '',
					emitter_app TEXT NOT NULL DEFAULT '',
					emitter_version TEXT NOT NULL DEFAULT '',
					calculator TEXT NOT NULL DEFAULT '',
					schema_name TEXT NOT NULL DEFAULT '',
					policy_version TEXT NOT NULL DEFAULT '',
					probability_method TEXT NOT NULL DEFAULT '',
					probability_transform TEXT NOT NULL DEFAULT '',
					scenario_basis TEXT NOT NULL DEFAULT '',
					decision_status TEXT NOT NULL DEFAULT '',
					decision_quality TEXT NOT NULL DEFAULT '',
					information_cutoff TEXT NOT NULL DEFAULT '',
					horizon_end TEXT NOT NULL DEFAULT '',
					horizon_std INTEGER NOT NULL DEFAULT 0,
					emitted_base_fen BIGINT,
					emitted_base_as_of TEXT NOT NULL DEFAULT '',
					emitted_base_source TEXT NOT NULL DEFAULT '',
					s0_date TEXT NOT NULL DEFAULT '',
					base_date TEXT NOT NULL DEFAULT '',
					s0_fen BIGINT,
					base_check TEXT NOT NULL DEFAULT 'pending',
					bench_code TEXT NOT NULL DEFAULT '')`,
				// The idempotency key for a retried ingest. NULLs are distinct in a unique index on
				// both drivers, so any number of PURGED sets may share a payload hash — which they
				// will, because two runs of one symbol produce identical payloads.
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_sets_idem ON prediction_sets(report_id, payload_sha256)`,
				// Group queries filter on rtype equality plus a made_on range, so the index leads with
				// rtype. cohort is kept for display and export, not as the read key.
				`CREATE INDEX IF NOT EXISTS idx_pred_sets_group ON prediction_sets(rtype, made_on, state)`,
				`CREATE INDEX IF NOT EXISTS idx_pred_sets_symbol ON prediction_sets(symbol, made_on)`,

				`CREATE TABLE IF NOT EXISTS prediction_claims(
					id ` + pk + `,
					set_id BIGINT NOT NULL,
					claim_key TEXT NOT NULL,
					kind TEXT NOT NULL,
					stance TEXT NOT NULL DEFAULT '',
					label TEXT NOT NULL DEFAULT '',
					price_fen BIGINT,
					prob_bp INTEGER,
					weight_bp INTEGER)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_claims_key ON prediction_claims(set_id, claim_key)`,

				// One row per accepted set x checkpoint, created pending with the set. resolved and
				// refused_final are both terminal, which is what makes a mature cell's inputs fixed
				// and therefore makes a mature cell immutable by construction rather than by caching.
				`CREATE TABLE IF NOT EXISTS prediction_marks(
					id ` + pk + `,
					set_id BIGINT NOT NULL,
					checkpoint TEXT NOT NULL,
					status TEXT NOT NULL DEFAULT 'pending',
					refusal TEXT NOT NULL DEFAULT '',
					attempts INTEGER NOT NULL DEFAULT 0,
					next_attempt_at TEXT NOT NULL DEFAULT '',
					session_date TEXT NOT NULL DEFAULT '',
					bar_date TEXT NOT NULL DEFAULT '',
					halted INTEGER NOT NULL DEFAULT 0,
					corp_action INTEGER NOT NULL DEFAULT 0,
					close_fen BIGINT,
					low_fen BIGINT,
					high_fen BIGINT,
					ret_bp INTEGER,
					bench_ret_bp INTEGER,
					bench_code TEXT NOT NULL DEFAULT '',
					resolved_at TEXT NOT NULL DEFAULT '',
					source TEXT NOT NULL DEFAULT '')`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_marks_cp ON prediction_marks(set_id, checkpoint)`,
				`CREATE INDEX IF NOT EXISTS idx_pred_marks_due ON prediction_marks(status, next_attempt_at)`,

				// One row per (rtype, cohort) month a retention pass removed. This is what lets the
				// monthly page say "purged on <date>" instead of recomputing the month as one where
				// nothing was predicted — a materially different and much better-looking claim.
				`CREATE TABLE IF NOT EXISTS prediction_purges(
					id ` + pk + `,
					rtype TEXT NOT NULL,
					cohort TEXT NOT NULL,
					purged_at TEXT NOT NULL DEFAULT '',
					sets_removed INTEGER NOT NULL DEFAULT 0)`,
				`CREATE UNIQUE INDEX IF NOT EXISTS idx_pred_purges_month ON prediction_purges(rtype, cohort)`,
			} {
				if _, err := e.exec(q); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// Independent sessions and personal login history. Older backups need no data upgrade:
// history is copied lazily from their audit rows the first time an account accesses it.
func migration0003() migration {
	cols := []colRef{{"users", "login_history_keep"}, {"users", "login_history_initialized"}}
	products := append([]colRef{}, cols...)
	for _, name := range []string{"id", "username", "revision", "created_at", "last_seen", "expires_at", "ip", "client", "method", "revoked"} {
		products = append(products, colRef{"user_sessions", name})
	}
	for _, name := range []string{"id", "username", "at", "ip", "detail"} {
		products = append(products, colRef{"login_history", name})
	}
	return migration{
		id: "0003", columns: products, tables: []string{"user_sessions", "login_history"},
		indices: []string{"idx_user_sessions_owner", "idx_login_history_owner"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for i, c := range cols {
				if !e.columnExists(c.table, c.name) {
					def := "100"
					if i == 1 {
						def = "0"
					}
					if _, err := e.exec("ALTER TABLE users ADD COLUMN " + c.name + " INTEGER NOT NULL DEFAULT " + def); err != nil {
						return err
					}
				}
			}
			for _, q := range []string{
				`CREATE TABLE IF NOT EXISTS user_sessions(id TEXT PRIMARY KEY, username TEXT NOT NULL, revision BIGINT NOT NULL, created_at BIGINT NOT NULL, last_seen BIGINT NOT NULL, expires_at BIGINT NOT NULL, ip TEXT NOT NULL, client TEXT NOT NULL, method TEXT NOT NULL DEFAULT '', revoked INTEGER NOT NULL DEFAULT 0)`,
				`CREATE INDEX IF NOT EXISTS idx_user_sessions_owner ON user_sessions(username,revision,expires_at)`,
				`CREATE TABLE IF NOT EXISTS login_history(id BIGINT PRIMARY KEY, username TEXT NOT NULL, at TEXT NOT NULL, ip TEXT NOT NULL, detail TEXT NOT NULL)`,
				`CREATE INDEX IF NOT EXISTS idx_login_history_owner ON login_history(username,id)`,
			} {
				if _, err := e.exec(q); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

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

// migration0002 records the highest formal release an account has actually loaded. It is account
// state rather than browser state: signing in from a new device must not replay notes the account
// has already seen, while the first loaded page after a deploy must still surface that release.
//
// The column is nullable and no existing row needs a backfill. NULL means the account predates this
// feature and has not yet loaded a formal release through it, so an older dump is safe to restore.
func migration0002() migration {
	col := colRef{table: "users", name: "last_used_version"}
	return migration{
		id:      "0002",
		columns: []colRef{col},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			if e.columnExists(col.table, col.name) {
				return nil
			}
			_, err := e.exec(`ALTER TABLE users ADD COLUMN last_used_version TEXT`)
			if err != nil {
				return fmt.Errorf("add %s.%s: %w", col.table, col.name, err)
			}
			return nil
		},
	}
}

// migration0005 adds `display` to announcements: how a row is shown to a reader.
//
// The column is a superset of the existing `popup` boolean — strip / popup / both — and it is
// ADDITIVE and nullable with no default, so a database written before it simply carries NULL, and
// NULL reads as "derive from popup" (announcement_store.go). That derivation, not a back-fill, is
// what makes a dump written before this step loadable: the restore never has to invent a value, and
// the writer keeps `popup` in sync for one release line so a rolled-back older binary still behaves.
func migration0005() migration {
	cols := []colRef{{table: "announcements", name: "display"}}
	return migration{
		id:      "0005",
		columns: cols,
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			for _, c := range cols {
				if e.columnExists(c.table, c.name) {
					continue // already there: a retry after a crash, or a hand-added column
				}
				if _, err := e.exec(`ALTER TABLE ` + c.table + ` ADD COLUMN ` + c.name + ` TEXT`); err != nil {
					return fmt.Errorf("add %s.%s: %w", c.table, c.name, err)
				}
			}
			return nil
		},
	}
}

// migration0006 adds the forecast verification access model (ADR 0039).
//
// user_groups.forecast_scope is who may see the forecast record from this OU: 'all', 'own' or 'none',
// with NULL meaning "inherit from the parent OU" (the nearest set value wins), the same tri-state the
// enrolment columns of 0001 use. The column is nullable with no default, so applying the step changes
// nobody's access: every OU inherits, and the Default OU is normalised to 'none' at boot rather than
// here, because a step that writes data cannot let an older dump in (restoreRule.olderDumpOK).
//
// prediction_sets.attributed_ou is the OU the run that produced a set was attributed to, read from the
// signed owner token at insert and never written again. NULL means unattributed (no token, an invalid
// token, or a set written before this step) — such a set belongs to no OU's 'own' population and is
// counted only under 'all'. It is never serialised to any client.
func migration0006() migration {
	cols := []colRef{
		{table: "user_groups", name: "forecast_scope"},
		{table: "prediction_sets", name: "attributed_ou"},
	}
	return migration{
		id:      "0006",
		columns: cols,
		indices: []string{"idx_pred_sets_ou"},
		restore: restoreRule{olderDumpOK: true},
		up: func(e migExec) error {
			if !e.columnExists("user_groups", "forecast_scope") {
				if _, err := e.exec(`ALTER TABLE user_groups ADD COLUMN forecast_scope TEXT`); err != nil {
					return fmt.Errorf("add user_groups.forecast_scope: %w", err)
				}
			}
			if !e.columnExists("prediction_sets", "attributed_ou") {
				if _, err := e.exec(`ALTER TABLE prediction_sets ADD COLUMN attributed_ou BIGINT`); err != nil {
					return fmt.Errorf("add prediction_sets.attributed_ou: %w", err)
				}
			}
			// The 'own' population filters on attributed_ou within one rtype and a made_on range, the
			// same shape idx_pred_sets_group serves for 'all'.
			_, err := e.exec(`CREATE INDEX IF NOT EXISTS idx_pred_sets_ou
				ON prediction_sets(attributed_ou, rtype, made_on)`)
			return err
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
func (s *Store) runMigrationsWithExplicit(allowExplicit bool) error {
	steps := s.migrations()
	known := map[string]bool{}
	for _, m := range steps {
		known[m.id] = true
	}
	if err := s.refuseUnknownLedgerRows(known); err != nil {
		return err
	}
	// Preflight before applying even an earlier automatic step: a refused startup writes no DDL.
	for _, m := range steps {
		if m.explicit && !allowExplicit {
			var recorded string
			err := s.queryRow(`SELECT v FROM meta WHERE k=?`, migrationLedgerKey(m.id)).Scan(&recorded)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if recorded == "" {
				return fmt.Errorf("migration %s requires an explicit offline upgrade: stop the portal, take a pre-upgrade backup with the previous release, then run report-portal schema-upgrade --apply; server startup does not alter this schema", m.id)
			}
			if err := s.verifyMigrationProducts(s.poolExec(), []migration{m}); err != nil {
				return err
			}
		}
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
		if m.verify != nil {
			if err := m.verify(e); err != nil {
				return fmt.Errorf("migration %s: %w", m.id, err)
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
