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
	empty, inspectErr := inspect(ctx, probe)
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
	if empty {
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
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, product := range schemaProducts() {
		if _, err := tx.ExecContext(ctx, product.sql); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=1`); err != nil {
		return err
	}
	return tx.Commit()
}

func inspect(ctx context.Context, db *sql.DB) (bool, error) {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return false, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if version > schemaVersion {
		return false, ErrNewerVersion
	}
	if version < 0 {
		return false, ErrIncompatible
	}
	var all int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema`).Scan(&all); err != nil {
		return false, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if version == 0 && all != 0 {
		return false, ErrIncompatible
	}
	if version == schemaVersion {
		expected := map[string]schemaProduct{}
		for _, product := range schemaProducts() {
			expected[product.name] = product
		}
		rows, err := db.QueryContext(ctx, `SELECT type,name,sql FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`)
		if err != nil {
			return false, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		for rows.Next() {
			var kind, name string
			var definition sql.NullString
			if err := rows.Scan(&kind, &name, &definition); err != nil {
				rows.Close()
				return false, err
			}
			want, ok := expected[name]
			if !ok || kind != want.kind || !definition.Valid || strings.TrimSpace(definition.String) != want.sql {
				rows.Close()
				return false, ErrIncompatible
			}
			delete(expected, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		if len(expected) != 0 {
			return false, ErrIncompatible
		}
		var mode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			return false, err
		}
		if mode != "wal" {
			return false, ErrIncompatible
		}
	}
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check(1)`).Scan(&integrity); err != nil {
		return false, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if integrity != "ok" {
		return false, ErrCorrupt
	}
	rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return false, err
	}
	bad := rows.Next()
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return false, rowErr
	}
	if bad {
		return false, ErrCorrupt
	}
	if version == schemaVersion {
		// Validate row contracts directly as well as running the engine's
		// integrity pragma. Do not assume its read-only execution checks every
		// table CHECK expression. Predicates come only from our closed schema.
		for _, product := range schemaProducts() {
			if product.kind == "table" {
				var invalid bool
				if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+product.name+` WHERE (`+product.rowCheck+`) IS NOT TRUE)`).Scan(&invalid); err != nil {
					if ctx.Err() != nil {
						return false, ctx.Err()
					}
					return false, fmt.Errorf("%w: %w", ErrCorrupt, err)
				}
				if invalid {
					return false, ErrCorrupt
				}
			}
		}
		if err := checkDeployment(ctx, db); err != nil {
			return false, err
		}
	}
	return version == 0, nil
}

func checkDeployment(ctx context.Context, db *sql.DB) error {
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
