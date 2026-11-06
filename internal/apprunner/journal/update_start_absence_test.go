//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// The runner answers a restore-start read-receipt with a typed absence only when its journal holds no
// command row for the effect identity. A row in any state, a sibling in the same slot, a closed store and
// a bad signature all stay ordinary errors the Portal must treat as unknown.
func TestRestoreStartReadReportsAbsenceOnlyWhenNoRowExists(t *testing.T) {
	store, _, command := importedStartFixture(t)
	raw, signature := signImportedStart(t, command)
	var err error
	if command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	result, err := store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, signature)
	if err != nil {
		t.Fatal(err)
	}
	absence, ok := result.(UpdateStartAbsence)
	if !ok || absence.State != "absent" || absence.EffectID != command.EffectID || absence.OperationID != command.OperationID ||
		absence.Stage != "start" || absence.PayloadSHA256 == "" {
		t.Fatalf("absence reply does not bind the effect identity: %#v", result)
	}
	encoded, err := json.Marshal(absence)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseUpdateStartAbsence(encoded)
	if err != nil || !UpdateStartAbsenceMatchesCommand(parsed, command) {
		t.Fatalf("absence reply does not round-trip: %v", err)
	}
	if rows := scalarJournalInt(t, store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, command.EffectID); rows != 0 {
		t.Fatalf("answering absent wrote a row: %d", rows)
	}
	if _, err := store.ReadUpdateStartRunningReceipt(context.Background(), raw, signature); err == nil || errors.Is(err, ErrNoSuchEffect) {
		t.Fatalf("the running-receipt read must stay an ordinary error: %v", err)
	}
}

func TestRestoreStartReadNeverReportsAbsenceForAnyRow(t *testing.T) {
	for _, mode := range []string{"reserved", "claimed", "other effect same slot", "database closed", "bad signature"} {
		t.Run(mode, func(t *testing.T) {
			store, _, command := importedStartFixture(t)
			raw, signature := signImportedStart(t, command)
			switch mode {
			case "reserved", "claimed", "other effect same slot":
				if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
					t.Fatal(err)
				}
				if mode == "claimed" {
					if _, err := store.ClaimUpdateStart(context.Background(), raw, signature); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "other effect same slot" {
					mustJournalExec(t, store.db, `UPDATE runner_commands SET effect_id=? WHERE effect_id=?`, fixtureID(2990), command.EffectID)
				}
			case "database closed":
				store.db.Close()
			case "bad signature":
				signature = append([]byte(nil), signature...)
				signature[0] ^= 0xff
			}
			result, err := store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, signature)
			if _, absent := result.(UpdateStartAbsence); absent {
				t.Fatalf("an absence reply was minted for a non-absent effect: %v", err)
			}
			if err == nil {
				t.Fatalf("a read of an unsettled effect answered %#v", result)
			}
		})
	}
}

// Only restore-start has the typed absence. An update-start (or enable-start) keeps its old behaviour.
func TestUpdateStartReadHasNoAbsenceForOtherKinds(t *testing.T) {
	f, _, _, raw, signature := updateStartJournalFixture(t)
	result, err := f.store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, signature)
	if _, absent := result.(UpdateStartAbsence); absent || err == nil {
		t.Fatalf("an update-start read was answered with an absence: %#v %v", result, err)
	}
}

// An absence read must not overtake a claim in progress: it takes the data execution gate before it
// reports an absence and looks again.
func TestRestoreStartAbsenceReadWaitsForTheExecutionGate(t *testing.T) {
	store, _, command := importedStartFixture(t)
	raw, signature := signImportedStart(t, command)
	if err := store.acquireDataExecution(context.Background()); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			store.releaseDataExecution()
		}
	}
	defer release()
	type answer struct {
		result any
		err    error
	}
	done := make(chan answer, 1)
	go func() {
		result, err := store.ReadUpdateStartRunningOrAbsence(context.Background(), raw, signature)
		done <- answer{result, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("an absence was reported while the gate was held: %#v %v", got.result, got.err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := store.ReserveUpdateStart(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	release()
	got := <-done
	if _, absent := got.result.(UpdateStartAbsence); absent || got.err == nil {
		t.Fatalf("a reserved effect read as absent: %#v %v", got.result, got.err)
	}
}
