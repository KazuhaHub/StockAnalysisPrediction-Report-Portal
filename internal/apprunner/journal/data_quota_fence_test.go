//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strconv"
	"testing"
)

// A volume row keeps the writer fence its namespace was prepared with: a disable or an update raises the
// container's and the namespace's fences, never the volume's. A raise is accepted at that fence and refused above it,
// which is the contract the Portal builds its set_data_quota command against.
func TestSetDataQuotaAcceptsTheVolumeFenceAndRefusesAHigherOne(t *testing.T) {
	ctx := context.Background()
	f, _ := appliedCheckpointFixture(t)
	fixtureVolumePresent(t, f)
	oldBytes, oldInodes := mustDataInt(f.data.QuotaBytes), mustDataInt(f.data.QuotaInodes)
	var volume int64
	if err := f.store.db.QueryRow(`SELECT writer_fence FROM runner_resources WHERE id=? AND kind='volume'`, f.command.Payload.NamespaceID).Scan(&volume); err != nil {
		t.Fatal(err)
	}
	if strconv.FormatInt(volume, 10) != f.data.WriterFence {
		t.Fatalf("the volume holds fence %d, not its prepare-data receipt's %s", volume, f.data.WriterFence)
	}
	calls := 0
	raised := f
	raised.command.Payload.WriterFence = strconv.FormatInt(volume+1, 10)
	command, raw, sig := quotaRaise(t, raised, 1, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); !errors.Is(err, ErrFenceReconciliation) || calls != 0 {
		t.Fatalf("a raise above the volume's fence was accepted: %v calls=%d", err, calls)
	}
	command, raw, sig = quotaRaise(t, f, 2, oldBytes, oldInodes, oldBytes*2, oldInodes*2)
	if _, err := f.store.DispatchSetDataQuota(ctx, raw, sig, raiseNative(command, &calls)); err != nil || calls != 1 {
		t.Fatalf("a raise at the volume's fence was refused: %v calls=%d", err, calls)
	}
}
