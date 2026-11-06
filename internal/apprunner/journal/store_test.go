//go:build linux || darwin

package journal

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func privateDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func rawJournal(t *testing.T, directory string, statements ...string) {
	t.Helper()
	path := filepath.Join(directory, journalName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var value int
	if err := db.QueryRow(q).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNewJournalSchemaProfileAndReopen(t *testing.T) {
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	var engine string
	if err := store.db.QueryRow(`SELECT sqlite_version()`).Scan(&engine); err != nil {
		t.Fatal(err)
	}
	t.Log("SQLite engine", engine)
	if scalar(t, store.db, `PRAGMA user_version`) != 1 {
		t.Fatal("wrong journal version")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`) != 4 {
		t.Fatal("wrong approved table count")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL`) != 4 {
		t.Fatal("wrong approved explicit index count")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_deployment`) != 0 {
		t.Fatal("opening silently enrolled a controller")
	}
	for range 2 {
		if scalar(t, store.db, `PRAGMA foreign_keys`) != 1 || scalar(t, store.db, `PRAGMA synchronous`) != 2 || scalar(t, store.db, `PRAGMA trusted_schema`) != 0 {
			t.Fatal("connection profile not enforced")
		}
		var mode string
		if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal mode: %s %v", mode, err)
		}
		store.db.SetMaxIdleConns(0)
	}
	store.db.SetMaxIdleConns(1)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("nonprivate file: %s %v", entry.Name(), info.Mode())
		}
	}
	if _, err := Open(directory); !errors.Is(err, ErrInUse) {
		t.Fatalf("second owner admitted: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if scalar(t, reopened.db, `PRAGMA user_version`) != 1 {
		t.Fatal("reopen lost schema")
	}
}

func TestUnknownOrNewerJournalIsNotModified(t *testing.T) {
	for _, fixture := range []struct {
		name string
		sql  []string
		want error
	}{
		{"unknown", []string{`CREATE TABLE unrelated(value TEXT)`, `INSERT INTO unrelated VALUES('fixture')`}, ErrIncompatible},
		{"future", []string{`PRAGMA user_version=2`}, ErrNewerVersion},
		{"partial", []string{`CREATE TABLE runner_deployment(singleton INTEGER)`}, ErrIncompatible},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			directory := privateDirectory(t)
			rawJournal(t, directory, fixture.sql...)
			path := filepath.Join(directory, journalName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory); !errors.Is(err, fixture.want) {
				t.Fatalf("open: %v, want %v", err, fixture.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refusal rewrote the database")
			}
		})
	}
}

func TestCorruptJournalIsNotRepaired(t *testing.T) {
	directory := privateDirectory(t)
	path := filepath.Join(directory, journalName)
	before := []byte("this is not a SQLite database")
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("corrupt journal opened: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("corruption refusal rewrote input")
	}
}

func TestChangedJournalIndexDefinitionIsRefused(t *testing.T) {
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rawJournal(t, directory, `DROP INDEX idx_runner_commands_state`, `CREATE INDEX idx_runner_commands_state ON runner_commands(updated_ms)`)
	if _, err := Open(directory); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("same-name drift accepted: %v", err)
	}
}

func TestJournalRejectsInsecurePaths(t *testing.T) {
	for _, kind := range []string{"directory_permissions", "database_permissions", "database_symlink", "lock_symlink", "hardlink", "orphan_wal", "unknown_file"} {
		t.Run(kind, func(t *testing.T) {
			directory := privateDirectory(t)
			outside := filepath.Join(privateDirectory(t), "outside")
			if err := os.WriteFile(outside, []byte("fixture outside file"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory_permissions":
				if err := os.Chmod(directory, 0750); err != nil {
					t.Fatal(err)
				}
			case "database_permissions":
				if err := os.WriteFile(filepath.Join(directory, journalName), nil, 0644); err != nil {
					t.Fatal(err)
				}
			case "database_symlink":
				if err := os.Symlink(outside, filepath.Join(directory, journalName)); err != nil {
					t.Fatal(err)
				}
			case "lock_symlink":
				if err := os.Symlink(outside, filepath.Join(directory, lockName)); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, filepath.Join(directory, journalName)); err != nil {
					t.Fatal(err)
				}
			case "orphan_wal":
				if err := os.WriteFile(filepath.Join(directory, journalName+"-wal"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown_file":
				if err := os.WriteFile(filepath.Join(directory, "portal.db"), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Open(directory); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("unsafe path accepted: %v", err)
			}
			after, err := os.ReadFile(outside)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != "fixture outside file" {
				t.Fatal("followed an unsafe filesystem alias")
			}
		})
	}
}

func TestJournalDirectoryIsNotAConnectionString(t *testing.T) {
	directory := filepath.Join(privateDirectory(t), "private #?_pragma=foreign_keys(0)")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA foreign_keys`) != 1 {
		t.Fatal("path text injected a connection option")
	}
	if _, err := os.Stat(filepath.Join(directory, journalName)); err != nil {
		t.Fatal(err)
	}
}
