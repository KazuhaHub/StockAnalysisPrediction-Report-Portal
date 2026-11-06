//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// The runner answers "no such effect" only when the journal proves no command row exists for the
// effect identity. Every other outcome stays an ordinary error the Portal must treat as unknown.
func TestImportReadReportsNoSuchEffectOnlyWhenRowIsAbsent(t *testing.T) {
	store, command, raw, signature := importStoreFixture(t)
	if _, err := store.ReadImportCheckpointReceipt(context.Background(), raw, signature); !errors.Is(err, ErrNoSuchEffect) {
		t.Fatalf("an unclaimed effect must be absent: %v", err)
	}
	result, err := store.ReadImportCheckpointOrAbsence(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	absence, ok := result.(ImportCheckpointAbsence)
	if !ok || !importAbsenceMatchesCommand(absence, command) || absence.State != "absent" {
		t.Fatalf("absence reply does not bind the effect identity: %#v", result)
	}
	encoded, err := json.Marshal(absence)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseImportCheckpointAbsence(encoded); err != nil {
		t.Fatalf("absence reply does not round-trip: %v", err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("answering absent wrote a row: rows=%d err=%v", rows, err)
	}
}

func TestImportReadNeverReportsNoSuchEffectForAnyRow(t *testing.T) {
	for _, mode := range []string{"unknown", "prepared", "applied", "other effect same slot", "database closed", "bad signature"} {
		t.Run(mode, func(t *testing.T) {
			store, command, raw, signature, intent := claimedImportFixture(t)
			switch mode {
			case "prepared":
				mustJournalExec(t, store.db, `UPDATE runner_commands SET state='prepared' WHERE effect_id=?`, command.EffectID)
			case "applied":
				if _, err := store.settleImportCheckpoint(context.Background(), intent, importSnapshotFor(intent)); err != nil {
					t.Fatal(err)
				}
			case "other effect same slot":
				mustJournalExec(t, store.db, `UPDATE runner_commands SET effect_id=? WHERE effect_id=?`, fixtureID(1990), command.EffectID)
			case "database closed":
				store.db.Close()
			case "bad signature":
				signature = append([]byte(nil), signature...)
				signature[0] ^= 0xff
			}
			_, err := store.ReadImportCheckpointReceipt(context.Background(), raw, signature)
			if mode == "applied" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrNoSuchEffect) {
				t.Fatalf("a non-absent effect must not read as absent: %v", err)
			}
			if result, err := store.ReadImportCheckpointOrAbsence(context.Background(), raw, signature); err == nil {
				if _, absent := result.(ImportCheckpointAbsence); absent {
					t.Fatal("an absence reply was minted for a non-absent effect")
				}
			}
		})
	}
}
