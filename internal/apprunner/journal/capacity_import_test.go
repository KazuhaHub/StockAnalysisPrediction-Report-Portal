//go:build linux || darwin

package journal

import (
	"context"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"testing"
)

func TestImportCheckpointHoldsTheNamespaceDisk(t *testing.T) {
	store, command, raw, sig := importStoreFixture(t)
	if err := store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.beginImportCheckpoint(context.Background(), raw, sig, importDataPin); err != nil {
		t.Fatal(err)
	}
	var kind string
	var disk int64
	if err := store.db.QueryRow(`SELECT kind,disk_bytes FROM runner_reservations WHERE resource_id=? AND state='held'`, command.Payload.NamespaceID).Scan(&kind, &disk); err != nil {
		t.Fatalf("an imported namespace was not reserved: %v", err)
	}
	if kind != "namespace" || disk != NamespaceReservationBytes {
		t.Fatalf("reservation = %s %d", kind, disk)
	}
}

func TestImportCheckpointRefusedLeavesNoRowAndReplays(t *testing.T) {
	store, command, raw, sig := importStoreFixture(t)
	budgets := roomyBudgets()
	budgets.DiskBytes = NamespaceReservationBytes - 1
	if err := store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.beginImportCheckpoint(context.Background(), raw, sig, importDataPin)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityDisk {
		t.Fatalf("err = %v", err)
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+command.Payload.NamespaceID+`'`) != 0 || scalar(t, store.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("a refused import left a row")
	}
	if scalar(t, store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='import-checkpoint' AND state='reverted'`) != 1 {
		t.Fatal("refused import not reverted")
	}
	// A replay of the same effect answers with the same refusal.
	state, err := store.importState(context.Background(), raw, sig)
	var again *CapacityRefusalError
	if !errors.As(err, &again) || again.Receipt != refusal.Receipt || state != "" {
		t.Fatalf("replay = %q %v", state, err)
	}
	// And the read-back does too.
	_, _, err = store.readImportCheckpoint(context.Background(), raw, sig, control.VerifyImportCheckpointSettlement)
	if !errors.As(err, &again) || again.Receipt != refusal.Receipt {
		t.Fatalf("read-back = %v", err)
	}
}
