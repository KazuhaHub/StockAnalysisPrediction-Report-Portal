// Package journal owns the runner's local control journal. Opening it establishes
// storage integrity and exclusive ownership, not enrollment or execution authority.
package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"modernc.org/sqlite"
)

const journalName = "journal.sqlite3"
const lockName = ".owner.lock"
const journalDriver = "portal-app-runner-journal-v1"

var (
	ErrInUse        = errors.New("runner journal already has an owner")
	ErrUnsafePath   = errors.New("runner journal requires a private owned directory and regular private files")
	ErrUnsupported  = errors.New("runner journal filesystem profile is unsupported")
	ErrIncompatible = errors.New("runner journal schema is incompatible")
	ErrNewerVersion = errors.New("runner journal requires a newer binary")
	ErrCorrupt      = errors.New("runner journal integrity check failed")
)

type Store struct {
	db            *sql.DB
	release       func() error
	closeOnce     sync.Once
	closeErr      error
	closing       atomic.Bool
	executionOnce sync.Once
	executionGate chan struct{}
	loadOnce      sync.Once
	loadGate      chan struct{}
}

func init() {
	d := &sqlite.Driver{}
	d.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, _ string) error {
		pragmas := []string{"PRAGMA foreign_keys=ON", "PRAGMA synchronous=FULL", "PRAGMA trusted_schema=OFF", "PRAGMA ignore_check_constraints=OFF", "PRAGMA busy_timeout=1000"}
		if runtime.GOOS == "darwin" {
			pragmas = append(pragmas, "PRAGMA fullfsync=ON", "PRAGMA checkpoint_fullfsync=ON")
		}
		for _, q := range pragmas {
			if _, err := conn.ExecContext(context.Background(), q, nil); err != nil {
				return err
			}
		}
		return nil
	})
	sql.Register(journalDriver, d)
}

func journalDSN(directory, mode string) string {
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, journalName)}
	q := url.Values{"mode": {mode}}
	if mode == "ro" {
		q.Set("_pragma", "query_only(1)")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Open accepts a dedicated, existing owner-only directory. Runtime setup creates
// that directory once; applications never choose it or mount its journal files.
func Open(directory string) (*Store, error) {
	guard, err := lockDirectory(directory)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			guard.close()
		}
	}()
	if err := guard.prepareDatabase(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe, err := sql.Open(journalDriver, journalDSN(directory, "ro"))
	if err != nil {
		return nil, err
	}
	probe.SetMaxOpenConns(1)
	found, inspectErr := inspectAt(ctx, probe, schemaVersion)
	closeErr := probe.Close()
	if inspectErr != nil {
		return nil, inspectErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := guard.checkFiles(); err != nil {
		return nil, err
	}
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	if found == 0 {
		var mode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
			return nil, err
		}
		if mode != "wal" {
			return nil, ErrIncompatible
		}
		if err := initialize(ctx, db); err != nil {
			return nil, err
		}
		if _, err := inspect(ctx, db); err != nil {
			return nil, err
		}
	} else if found < schemaVersion {
		if err := upgrade(ctx, db, found); err != nil {
			return nil, err
		}
	}
	if err := checkProfile(ctx, db); err != nil {
		return nil, err
	}
	if err := guard.checkFiles(); err != nil {
		return nil, err
	}
	if err := guard.sync(); err != nil {
		return nil, err
	}
	ok, owned = true, true
	return &Store{db: db, release: guard.close}, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closing.Store(true)
		// A live synchronous provider must settle before the journal owner lock can
		// pass to another process. Uncertainty stays durable if settlement fails.
		s.acquireDataExecution(context.Background())
		defer s.releaseDataExecution()
		s.closeErr = errors.Join(s.db.Close(), s.release())
	})
	return s.closeErr
}

func initialize(ctx context.Context, db *sql.DB) error {
	return initializeVersion(ctx, db, schemaVersion)
}

func initializeVersion(ctx context.Context, db *sql.DB, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, product := range schemaProducts(version) {
		if _, err := tx.ExecContext(ctx, product.sql); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

func inspect(ctx context.Context, db *sql.DB) (bool, error) {
	version, err := inspectAt(ctx, db, schemaVersion)
	return version == 0, err
}

// querier is the read side of a database handle or of a transaction on it.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inspectUpgraded is the inspection an upgrade runs on its own transaction before committing.
var inspectUpgraded = inspectAt

// inspectAt verifies the journal against the exact closed schema of the version
// it declares and returns that version; zero means a blank database. Versions
// above supported are refused, which is how an older binary treats a newer file.
func inspectAt(ctx context.Context, db querier, supported int) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if version > supported {
		return 0, ErrNewerVersion
	}
	if version < 0 {
		return 0, ErrIncompatible
	}
	var all int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema`).Scan(&all); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if version == 0 && all != 0 {
		return 0, ErrIncompatible
	}
	if version != 0 {
		expected := map[string]schemaProduct{}
		for _, product := range schemaProducts(version) {
			expected[product.name] = product
		}
		rows, err := db.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		for rows.Next() {
			var kind, name string
			var definition sql.NullString
			if err := rows.Scan(&kind, &name, &definition); err != nil {
				rows.Close()
				return 0, err
			}
			want, ok := expected[name]
			if !ok || kind != want.kind || !definition.Valid || strings.TrimSpace(definition.String) != want.sql {
				rows.Close()
				return 0, ErrIncompatible
			}
			delete(expected, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return 0, err
		}
		if len(expected) != 0 {
			return 0, ErrIncompatible
		}
		var mode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			return 0, err
		}
		if mode != "wal" {
			return 0, ErrIncompatible
		}
	}
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check(1)`).Scan(&integrity); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if integrity != "ok" {
		return 0, ErrCorrupt
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return 0, err
	}
	bad := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return 0, rowErr
	}
	if bad {
		return 0, ErrCorrupt
	}
	if version != 0 {
		// Validate row contracts directly as well as running the engine's
		// integrity pragma. Do not assume its read-only execution checks every
		// table CHECK expression. Predicates come only from our closed schema.
		for _, product := range schemaProducts(version) {
			if product.kind == "table" {
				var invalid bool
				if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+product.name+` WHERE (`+product.rowCheck+`) IS NOT TRUE)`).Scan(&invalid); err != nil {
					if ctx.Err() != nil {
						return 0, ctx.Err()
					}
					return 0, fmt.Errorf("%w: %w", ErrCorrupt, err)
				}
				if invalid {
					return 0, ErrCorrupt
				}
			}
		}
		if err := checkDeployment(ctx, db); err != nil {
			return 0, err
		}
	}
	return version, nil
}

func checkDeployment(ctx context.Context, db querier) error {
	var key []byte
	var fingerprint string
	err := db.QueryRowContext(ctx, `SELECT verification_key,key_fingerprint FROM runner_deployment WHERE singleton=1`).Scan(&key, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		var orphaned bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runner_commands) OR EXISTS(SELECT 1 FROM runner_resources) OR EXISTS(SELECT 1 FROM runner_checkpoints)`).Scan(&orphaned); err != nil {
			return err
		}
		if orphaned {
			return ErrCorrupt
		}
		return nil
	}
	if err != nil {
		return err
	}
	sum := sha256.Sum256(key)
	if fingerprint != hex.EncodeToString(sum[:]) {
		return ErrCorrupt
	}
	return nil
}

func checkProfile(ctx context.Context, db *sql.DB) error {
	profiles := []struct {
		pragma string
		value  int
	}{
		{"foreign_keys", 1}, {"synchronous", 2}, {"trusted_schema", 0}, {"ignore_check_constraints", 0}, {"query_only", 0},
	}
	if runtime.GOOS == "darwin" {
		profiles = append(profiles, struct {
			pragma string
			value  int
		}{"fullfsync", 1}, struct {
			pragma string
			value  int
		}{"checkpoint_fullfsync", 1})
	}
	for _, profile := range profiles {
		var actual int
		if err := db.QueryRowContext(ctx, "PRAGMA "+profile.pragma).Scan(&actual); err != nil {
			return err
		}
		if actual != profile.value {
			return fmt.Errorf("%w: %s", ErrIncompatible, profile.pragma)
		}
	}
	var mode string
	if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return ErrIncompatible
	}
	return nil
}

// upgrade moves a verified older journal to the current schema in one
// transaction. The v1 table is renamed aside, the current table is created from
// its exact product SQL, rows are copied by explicit column name in rowid order
// and counted, and only then is the old table dropped; nothing is created by
// renaming. The order matters: the rowid of a command row is the order the
// runner admitted it in, and later checks rely on it (see rowOrder).
func upgrade(ctx context.Context, db *sql.DB, from int) error {
	if from != 1 {
		return ErrIncompatible
	}
	products := map[string]schemaProduct{}
	for _, product := range schemaProducts(schemaVersion) {
		products[product.name] = product
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var before int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands`).Scan(&before); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE runner_commands RENAME TO runner_commands_v1`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, products["runner_commands"].sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(`+runnerCommandColumns+`) SELECT `+runnerCommandColumns+` FROM runner_commands_v1 ORDER BY rowid`); err != nil {
		return err
	}
	var after int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands`).Scan(&after); err != nil {
		return err
	}
	if after != before {
		return fmt.Errorf("%w: upgrade copied %d of %d command rows", ErrCorrupt, after, before)
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE runner_commands_v1`); err != nil {
		return err
	}
	for _, product := range schemaProducts(schemaVersion) {
		if product.kind == "index" && strings.Contains(product.sql, " ON runner_commands(") {
			if _, err := tx.ExecContext(ctx, product.sql); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version=%d`, schemaVersion)); err != nil {
		return err
	}
	// Inspect the result before it can be committed: the transaction sees its own schema changes, and a
	// failure here rolls back to the untouched v1 file.
	if _, err := inspectUpgraded(ctx, tx, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

const runnerCommandColumns = "effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms"
