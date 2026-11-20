//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestV5WidensTheKindListWithTheTwoDataCommands(t *testing.T) {
	if schemaVersion != 5 {
		t.Fatalf("schema version %d", schemaVersion)
	}
	for _, kind := range []string{"'set-data-quota'", "'import-data'"} {
		if !strings.Contains(commandsSQL(5), kind) {
			t.Fatalf("v5 runner_commands does not declare %s", kind)
		}
		for _, version := range []int{1, 2, 3, 4} {
			if strings.Contains(commandsSQL(version), kind) {
				t.Fatalf("v%d declares %s: older products are frozen", version, kind)
			}
		}
	}
}

// insertDataCommand writes one row of the given kind, as the runner does when it admits a command.
func insertDataCommand(t *testing.T, db *sql.DB, n int, kind, stage string) error {
	t.Helper()
	_, err := db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,1,?,?,1,?,1,'prepared','{}',NULL,1,1)`,
		fixtureID(n), fixtureID(100+n), fixtureID(200+n), stage, kind, strings.Repeat("d", 64), fixtureID(2))
	return err
}

// Every older journal reaches v5 in one open, keeps its command rows in admission order, and then accepts the new
// kinds; an older journal's own table still refuses them.
func TestEveryOlderJournalUpgradesToV5AndAcceptsTheNewKinds(t *testing.T) {
	for _, from := range []int{1, 2, 3, 4} {
		t.Run(string(rune('0'+from)), func(t *testing.T) {
			directory := versionJournal(t, from)
			db, err := sql.Open(journalDriver, journalDSN(directory, "rw"))
			if err != nil {
				t.Fatal(err)
			}
			if err := insertDeployment(db); err != nil {
				t.Fatal(err)
			}
			if err := insertDataCommand(t, db, 1, "snapshot", "snapshot"); err != nil {
				t.Fatal(err)
			}
			if err := insertDataCommand(t, db, 2, "prepare-data", "prepare_data"); err != nil {
				t.Fatal(err)
			}
			if err := insertDataCommand(t, db, 3, "set-data-quota", "prepare_data"); err == nil {
				t.Fatalf("v%d accepted a kind it does not declare", from)
			}
			db.Close()
			store, err := Open(directory)
			if err != nil {
				t.Fatalf("v%d journal was not upgraded: %v", from, err)
			}
			defer store.Close()
			if scalar(t, store.db, `PRAGMA user_version`) != 5 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands`) != 2 {
				t.Fatal("upgrade lost rows or did not set the version")
			}
			var first string
			if err := store.db.QueryRow(`SELECT effect_id FROM runner_commands ORDER BY rowid LIMIT 1`).Scan(&first); err != nil || first != fixtureID(1) {
				t.Fatalf("rowid order changed: %s %v", first, err)
			}
			if scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runner_proxy_budget'`) != 1 ||
				scalar(t, store.db, `SELECT COUNT(*) FROM sqlite_schema WHERE name='runner_reservations'`) != 1 {
				t.Fatal("upgrade did not leave every v5 table")
			}
			if _, err := inspect(context.Background(), store.db); err != nil {
				t.Fatalf("upgraded journal fails inspection: %v", err)
			}
			for n, kind := range []string{"set-data-quota", "import-data"} {
				stage := map[string]string{"set-data-quota": "prepare_data", "import-data": "restore_data"}[kind]
				if err := insertDataCommand(t, store.db, 10+n, kind, stage); err != nil {
					t.Fatalf("v5 refused %s: %v", kind, err)
				}
			}
		})
	}
}

func TestV5JournalIsRefusedByAnOlderInspect(t *testing.T) {
	store, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := inspectAt(context.Background(), store.db, 4); err != ErrNewerVersion {
		t.Fatalf("a v5 journal was not refused by a v4 binary: %v", err)
	}
}
