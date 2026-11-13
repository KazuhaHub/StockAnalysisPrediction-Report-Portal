//go:build linux || darwin

package journal

import (
	"context"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// claimedEnableStart is an enable start whose claim netted the stopped source: the source row is still held, and
// its executor is then lost, so the start stays unknown.
func claimedEnableStart(t *testing.T) (checkpointFixture, control.UpdateStartCommand, string) {
	t.Helper()
	f, command, raw, sig, source := enableStartFixture(t)
	if err := f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8}); err != nil {
		t.Fatal(err)
	}
	holdSource(t, f, source, command.InstallationID, 256, 500)
	ctx := withBackendResources(context.Background(), fixtureResources())
	if _, err := f.store.ClaimUpdateStart(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id='`+command.EffectID+`' AND state='unknown'`) != 1 {
		t.Fatal("the enable start is not unknown")
	}
	return f, command, source
}

func settleSource(t *testing.T, f checkpointFixture, command control.UpdateStartCommand, proven bool) error {
	t.Helper()
	tx, err := f.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := settleEnableStartSource(context.Background(), tx, command, proven, time.Now().UTC().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func sourceRows(t *testing.T, f checkpointFixture, source string) (held, released int, observed string) {
	t.Helper()
	held = scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+source+`' AND state='held'`)
	released = scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+source+`' AND state='released'`)
	if err := f.store.db.QueryRow(`SELECT observed_state FROM runner_resources WHERE id=?`, source).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	return
}

// An orphan settlement that proved the stopped source absent releases the source's row once, and records the
// resource absent; the target's row is left to the settlement's own rule.
func TestOrphanSettlementReleasesTheProvenAbsentEnableSource(t *testing.T) {
	f, command, source := claimedEnableStart(t)
	if err := settleSource(t, f, command, true); err != nil {
		t.Fatal(err)
	}
	if held, released, observed := sourceRows(t, f, source); held != 0 || released != 1 || observed != "absent" {
		t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
	}
	if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+command.Payload.ResourceID+`' AND state='held'`) != 1 {
		t.Fatal("the settlement of the source touched the target's reservation")
	}
	// A replay releases nothing more.
	if err := settleSource(t, f, command, true); err != nil {
		t.Fatal(err)
	}
	if held, released, _ := sourceRows(t, f, source); held != 0 || released != 1 {
		t.Fatalf("replay: held=%d released=%d", held, released)
	}
}

// A source that was not proven absent keeps its row and its resource state.
func TestOrphanSettlementKeepsAnEnableSourceThatIsNotProvenAbsent(t *testing.T) {
	t.Run("not_observed_absent", func(t *testing.T) {
		f, command, source := claimedEnableStart(t)
		if err := settleSource(t, f, command, false); err != nil {
			t.Fatal(err)
		}
		if held, released, observed := sourceRows(t, f, source); held != 1 || released != 0 || observed != "stopped" {
			t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
		}
	})
	t.Run("running_source", func(t *testing.T) {
		f, command, source := claimedEnableStart(t)
		mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='running' WHERE id=?`, source)
		// Either a refusal (the source no longer matches the checkpoint) or a no-op: nothing is released.
		_ = settleSource(t, f, command, true)
		if held, released, observed := sourceRows(t, f, source); held != 1 || released != 0 || observed != "running" {
			t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
		}
	})
	t.Run("another_installations_source", func(t *testing.T) {
		f, command, source := claimedEnableStart(t)
		mustJournalExec(t, f.store.db, `UPDATE runner_resources SET installation_id=? WHERE id=?`, fixtureID(9900), source)
		if err := settleSource(t, f, command, true); err != nil {
			t.Fatal(err)
		}
		if held, released, _ := sourceRows(t, f, source); held != 1 || released != 0 {
			t.Fatalf("source held=%d released=%d", held, released)
		}
	})
}

// Only an enable start transferred a source: an update start's settlement releases nothing here.
func TestOrphanSettlementOfAnUpdateStartReleasesNoSource(t *testing.T) {
	f, command, source := claimedEnableStart(t)
	command.Kind = "update-start"
	if err := settleSource(t, f, command, true); err != nil {
		t.Fatal(err)
	}
	if held, _, observed := sourceRows(t, f, source); held != 1 || observed != "stopped" {
		t.Fatalf("source held=%d observed=%s", held, observed)
	}
}
