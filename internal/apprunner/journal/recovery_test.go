//go:build linux || darwin

package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fixtureID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

func insertDeployment(db *sql.DB) error {
	public := []byte(strings.Repeat("x", 32))
	sum := sha256.Sum256(public)
	_, err := db.Exec(`INSERT INTO runner_deployment(singleton,deployment_id,controller_epoch,control_revision,verification_algorithm,verification_key,key_fingerprint,revision,created_ms,updated_ms)
        VALUES(1,?,?,1,'fixture-untrusted',?,?,1,1,1)`, fixtureID(1), fixtureID(2), public, hex.EncodeToString(sum[:]))
	return err
}

func insertCommand(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
        VALUES(?,?,?,'stage',1,'stage',?,1,?,1,'prepared','{}',NULL,1,1)`, fixtureID(3), fixtureID(4), fixtureID(5), strings.Repeat("a", 64), fixtureID(2))
	return err
}

func TestJournalChildProcess(t *testing.T) {
	mode := os.Getenv("PORTAL_RUNNER_JOURNAL_TEST_CHILD")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	store, err := Open(os.Getenv("PORTAL_RUNNER_JOURNAL_TEST_DIRECTORY"))
	if err != nil {
		if errors.Is(err, ErrInUse) {
			os.Exit(23)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(24)
	}
	if mode == "wal" {
		if err := insertDeployment(store.db); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(25)
		}
		if _, err := store.db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(25)
		}
		var busy, log, checkpointed int
		if err := store.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed); err != nil || busy != 0 {
			fmt.Fprintln(os.Stderr, "checkpoint", err)
			os.Exit(25)
		}
		if err := insertCommand(store.db); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(25)
		}
		// Deliberately bypass SQLite Close/checkpoint and Go defers. The OS
		// releases ownership, but committed WAL frames must survive reopening.
		os.Exit(0)
	}
	if err := store.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(25)
	}
	os.Exit(0)
}

func childJournal(directory, mode string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalChildProcess$")
	cmd.Env = append(os.Environ(), "PORTAL_RUNNER_JOURNAL_TEST_CHILD="+mode, "PORTAL_RUNNER_JOURNAL_TEST_DIRECTORY="+directory)
	return cmd
}

func TestJournalLockExcludesAnotherProcess(t *testing.T) {
	directory := privateDirectory(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	output, err := childJournal(directory, "lock").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("second process was not excluded: %v %s", err, output)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if output, err := childJournal(directory, "lock").CombinedOutput(); err != nil {
		t.Fatalf("ownership not released: %v %s", err, output)
	}
}

func TestJournalReopensCommittedWALAfterProcessExit(t *testing.T) {
	directory := privateDirectory(t)
	if output, err := childJournal(directory, "wal").CombinedOutput(); err != nil {
		t.Fatalf("WAL fixture: %v %s", err, output)
	}
	info, err := os.Stat(filepath.Join(directory, journalName+"-wal"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("no committed WAL fixture: %v", err)
	}
	// Test-only immutable read proves the command is absent from the base
	// database. Real startup must never ignore that committed WAL this way.
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, journalName), RawQuery: "mode=ro&immutable=1"}
	base, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, base, `SELECT COUNT(*) FROM runner_commands`) != 0 {
		base.Close()
		t.Fatal("fixture unexpectedly checkpointed command")
	}
	base.Close()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 1 {
		t.Fatal("startup discarded committed intent")
	}
}

func TestJournalRejectsMissingOrCorruptDeployment(t *testing.T) {
	for _, fixture := range []string{"orphan", "key_digest"} {
		t.Run(fixture, func(t *testing.T) {
			directory := privateDirectory(t)
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if fixture == "orphan" {
				if err := insertCommand(store.db); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := insertDeployment(store.db); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE runner_deployment SET key_fingerprint=?`, strings.Repeat("0", 64)); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if accepted, err := Open(directory); !errors.Is(err, ErrCorrupt) {
				if accepted != nil {
					accepted.Close()
				}
				t.Fatalf("invalid enrollment facts accepted: %v", err)
			}
		})
	}
}

func TestJournalInitializationIsAtomicOnPageExhaustion(t *testing.T) {
	// Measure the first table so the real pager limit permits it but rejects
	// later products. No implementation failure hook or large file is needed.
	measure, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer measure.Close()
	measure.SetMaxOpenConns(1)
	if _, err := measure.Exec(`PRAGMA page_size=512`); err != nil {
		t.Fatal(err)
	}
	if _, err := measure.Exec(schemaProducts()[0].sql); err != nil {
		t.Fatal(err)
	}
	pages := scalar(t, measure, `PRAGMA page_count`)
	directory := privateDirectory(t)
	path := filepath.Join(directory, journalName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA page_size=512`); err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL fixture profile: %s %v", mode, err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		t.Fatal(err)
	}
	if err := initialize(context.Background(), db); err == nil {
		t.Fatal("pager limit did not fail initialization")
	}
	if scalar(t, db, `SELECT COUNT(*) FROM sqlite_schema`) != 0 || scalar(t, db, `PRAGMA user_version`) != 0 {
		t.Fatal("failed initialization left partial schema/version")
	}
	if _, err := db.Exec(`PRAGMA max_page_count=1000000`); err != nil {
		t.Fatal(err)
	}
	if err := initialize(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if scalar(t, db, `PRAGMA user_version`) != 1 {
		t.Fatal("clean retry did not initialize")
	}
}
