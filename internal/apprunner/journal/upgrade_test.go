//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newV1Journal creates an empty journal exactly as the frozen v1 binary wrote
// it and returns it open, so a test can add the rows it needs.
func newV1Journal(t *testing.T) (string, *sql.DB) {
	t.Helper()
	directory := privateDirectory(t)
	path := filepath.Join(directory, journalName)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("WAL fixture profile: %s %v", mode, err)
	}
	if err := initializeVersion(context.Background(), db, 1); err != nil {
		t.Fatal(err)
	}
	return directory, db
}

// v1Journal builds a populated v1 journal.
func v1Journal(t *testing.T) string {
	t.Helper()
	directory, db := newV1Journal(t)
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	for number, kind := range []string{"stage", "prepare-data", "restore"} {
		if _, err := db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
            VALUES(?,?,?,'stage',?,?,?,1,?,1,'prepared','{}',NULL,1,1)`, fixtureID(100+number), fixtureID(4), fixtureID(5), number+1, kind, strings.Repeat("a", 64), fixtureID(2)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
        VALUES(?,?,?,?,?,1,1,NULL,0,'preparing',1,NULL,1,1)`, fixtureID(7), fixtureID(8), fixtureID(5), fixtureID(4), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if scalar(t, db, `PRAGMA user_version`) != 1 {
		t.Fatal("fixture is not a v1 journal")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return directory
}

const importKindCommand = `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
    VALUES(?,?,?,'restore_data',1,?,?,1,?,1,'prepared','{}',NULL,1,1)`

func TestOpenUpgradesPopulatedV1Journal(t *testing.T) {
	directory := v1Journal(t)
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 2 {
		t.Fatal("journal not upgraded to v2")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 3 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_checkpoints`) != 1 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_deployment`) != 1 {
		t.Fatal("upgrade lost or invented rows")
	}
	for number, kind := range []string{"stage", "prepare-data", "restore"} {
		if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+fixtureID(100+number)+`' AND kind='`+kind+`' AND effect_number=`+string(rune('1'+number))+` AND state='prepared'`) != 1 {
			t.Fatalf("row %s changed by upgrade", kind)
		}
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`) != 4 || scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL`) != 4 {
		t.Fatal("upgrade left extra or missing schema objects")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE '%_v1'`) != 0 {
		t.Fatal("upgrade left the v1 table behind")
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("upgraded journal fails inspection: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatalf("upgraded journal does not reopen: %v", err)
	}
	reopened.Close()
}

func TestUpgradedJournalAcceptsImportCheckpointKind(t *testing.T) {
	directory := v1Journal(t)
	raw, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(importKindCommand, fixtureID(200), fixtureID(4), fixtureID(5), "import-checkpoint", strings.Repeat("c", 64), fixtureID(2)); err == nil {
		t.Fatal("v1 journal admitted a v2 kind")
	}
	raw.Close()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for number, kind := range []string{"import-checkpoint", "migrate-data", "discard-migration", "load-image"} {
		if _, err := store.db.Exec(importKindCommand, fixtureID(200+number), fixtureID(300+number), fixtureID(5), kind, strings.Repeat("c", 64), fixtureID(2)); err != nil {
			t.Fatalf("v2 kind %s refused: %v", kind, err)
		}
	}
	if _, err := store.db.Exec(importKindCommand, fixtureID(210), fixtureID(4), fixtureID(5), "exec", strings.Repeat("c", 64), fixtureID(2)); err == nil {
		t.Fatal("v2 journal admitted an arbitrary kind")
	}
	if _, err := inspect(context.Background(), store.db); err != nil {
		t.Fatalf("v2 rows fail inspection: %v", err)
	}
}

func TestFreshJournalIsV2(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if scalar(t, store.db, `PRAGMA user_version`) != 2 {
		t.Fatal("fresh journal is not v2")
	}
}

func TestV2JournalRefusedByV1Inspect(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := inspectAt(context.Background(), store.db, 1); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("v1 inspector accepted a v2 journal: %v", err)
	}
	if _, err := inspectAt(context.Background(), store.db, 2); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedV1SchemaNotUpgraded(t *testing.T) {
	for _, fixture := range []struct {
		name, mutation string
		want           error
	}{
		{"index_drift", `DROP INDEX idx_runner_commands_state; CREATE INDEX idx_runner_commands_state ON runner_commands(updated_ms)`, ErrIncompatible},
		{"extra_table", `CREATE TABLE unrelated(value TEXT)`, ErrIncompatible},
		{"bypassed_row", `PRAGMA ignore_check_constraints=ON; UPDATE runner_commands SET kind='exec' WHERE effect_number=1`, ErrCorrupt},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			directory := v1Journal(t)
			raw, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
			if err != nil {
				t.Fatal(err)
			}
			raw.SetMaxOpenConns(1)
			for _, statement := range strings.Split(fixture.mutation, "; ") {
				if _, err := raw.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, journalName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if store, err := Open(directory); !errors.Is(err, fixture.want) {
				if store != nil {
					store.Close()
				}
				t.Fatalf("tampered v1 journal opened: %v, want %v", err, fixture.want)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("refusal rewrote the v1 journal")
			}
		})
	}
}

// The upgraded schema is inspected inside the upgrade transaction, so an inspection that fails leaves
// the v1 file exactly as it was instead of a committed v2 journal nobody verified.
func TestUpgradeFailingInspectionLeavesAV1Journal(t *testing.T) {
	directory := v1Journal(t)
	original := inspectUpgraded
	inspectUpgraded = func(context.Context, querier, int) (int, error) { return 0, ErrIncompatible }
	defer func() { inspectUpgraded = original }()
	if store, err := Open(directory); err == nil {
		store.Close()
		t.Fatal("a journal whose upgrade failed inspection opened")
	}
	inspectUpgraded = original
	raw, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if scalar(t, raw, `PRAGMA user_version`) != 1 || scalar(t, raw, `SELECT COUNT(*) FROM runner_commands`) != 3 ||
		scalar(t, raw, `SELECT COUNT(*) FROM sqlite_schema WHERE name LIKE '%_v1'`) != 0 {
		t.Fatal("a failed upgrade did not roll back to the v1 journal")
	}
	if _, err := inspectAt(context.Background(), raw, 1); err != nil {
		t.Fatalf("the rolled-back journal is not a valid v1 journal: %v", err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatalf("the v1 journal did not upgrade afterwards: %v", err)
	}
	store.Close()
}

// The rowid of a command row is the order the runner admitted it in, and the supersession checks rely on it. The
// upgrade must not reorder rows: they are copied in rowid order, so two rows keep their relative order even when
// their effect identifiers sort the other way.
func TestUpgradePreservesCommandRowOrder(t *testing.T) {
	directory, db := newV1Journal(t)
	if err := insertDeployment(db); err != nil {
		t.Fatal(err)
	}
	// Inserted in descending identifier order: insertion order and primary key order are opposite.
	for number := range 6 {
		if _, err := db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
            VALUES(?,?,?,'stage',?,'stage',?,1,?,1,'prepared','{}',NULL,1,1)`, fixtureID(900-number), fixtureID(4), fixtureID(5), number+1, strings.Repeat("a", 64), fixtureID(2)); err != nil {
			t.Fatal(err)
		}
	}
	order := func(db *sql.DB) []string {
		rows, err := db.Query(`SELECT effect_id FROM runner_commands ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		return ids
	}
	before := order(db)
	db.Close()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after := order(store.db)
	if len(after) != len(before) {
		t.Fatalf("the upgrade changed the row count: %d to %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("the upgrade reordered the command rows: %v became %v", before, after)
		}
	}
	var later int64
	if err := store.db.QueryRow(`SELECT MAX(rowid) FROM runner_commands`).Scan(&later); err != nil || later != int64(len(before)) {
		t.Fatalf("the copied rows are not numbered from one in order: max rowid %d %v", later, err)
	}
}
