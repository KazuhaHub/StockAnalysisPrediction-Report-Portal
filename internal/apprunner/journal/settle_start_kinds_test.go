//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// keyedInspector answers per resource, so a settlement can find its target and miss the enable source (or the
// reverse). A resource it does not list is absent.
type keyedInspector struct {
	fakeInspector
	byResource map[string]driver.TargetInspection
	seen       map[string]int
}

func (k *keyedInspector) InspectTarget(_ context.Context, resourceID string) (driver.TargetInspection, error) {
	if k.seen == nil {
		k.seen = map[string]int{}
	}
	k.seen[resourceID]++
	return k.byResource[resourceID], nil
}

// restoreStartFixture reserves a restore-start (its own operation, the release unchanged) for the update fixture.
func restoreStartFixture(t *testing.T) reservedStartFixture {
	t.Helper()
	f, _, command, _, _ := updateStartJournalFixture(t)
	command.Kind = "restore-start"
	command.OperationID = fixtureID(1534)
	command.Payload.TargetReleaseSHA256 = command.Payload.SourceReleaseSHA256
	var err error
	if command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, sig, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReserveUpdateStart(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return reservedStartFixture{f: f, command: command, raw: raw, sig: sig}
}

// enableStartAsReserved is the enable start fixture in the shape the settlement helpers use.
func enableStartAsReserved(t *testing.T) (reservedStartFixture, string) {
	t.Helper()
	f, command, raw, sig, source := enableStartFixture(t)
	holdSource(t, f, source, command.InstallationID, 256, 500)
	return reservedStartFixture{f: f, command: command, raw: raw, sig: sig}, source
}

func (r reservedStartFixture) targetReservation(t *testing.T) string {
	t.Helper()
	var state string
	if err := r.f.store.db.QueryRow(`SELECT state FROM runner_reservations WHERE resource_id=?`, r.command.Payload.ResourceID).Scan(&state); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return state
}

// claimOrphan claims the start under capacity budgets, so its target holds a reservation, and leaves it.
func (r reservedStartFixture) claimOrphan(t *testing.T, bound bool) {
	t.Helper()
	if err := r.f.store.SetCapacityBudgets(CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8}); err != nil {
		t.Fatal(err)
	}
	r.orphanedWith(t, withBackendResources(context.Background(), fixtureResources()), bound)
}

// An orphaned restore start with no container settles as not run and releases its target reservation.
func TestSettleOrphanedRestoreStartNeverRan(t *testing.T) {
	r := restoreStartFixture(t)
	r.claimOrphan(t, false)
	if r.targetReservation(t) != "held" {
		t.Fatal("the orphaned restore start holds no reservation")
	}
	inspector := &fakeInspector{}
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "never_ran" || receipt.StartEffectID != r.command.EffectID {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.startState(t) != "reverted" || r.targetReservation(t) != "released" {
		t.Fatalf("start %s reservation %s", r.startState(t), r.targetReservation(t))
	}
	if desired, observed, revision := r.resource(t); desired != "absent" || observed != "absent" || revision != 4 {
		t.Fatalf("resource: %s %s %d", desired, observed, revision)
	}
	if again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil || again != receipt {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

// A container found for an orphaned restore start settles it applied, and keeps the target's reservation.
func TestSettleOrphanedRestoreStartWithAContainer(t *testing.T) {
	r := restoreStartFixture(t)
	r.claimOrphan(t, true)
	native := strings.Repeat("ab", 32)
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{inspection: driver.TargetInspection{Found: true, NativeID: native, State: "running"}})
	if err != nil || receipt.Outcome != "applied" || receipt.NativeID != native {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.startState(t) != "applied" || r.targetReservation(t) != "held" {
		t.Fatalf("start %s reservation %s", r.startState(t), r.targetReservation(t))
	}
}

// An orphaned enable start that never created its target releases both the target and the stopped source it
// replaced, once, when both are proven absent.
func TestSettleOrphanedEnableStartNeverRanReleasesTheSource(t *testing.T) {
	r, source := enableStartAsReserved(t)
	r.claimOrphan(t, false)
	inspector := &keyedInspector{}
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "never_ran" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if inspector.seen[source] != 2 || inspector.seen[r.command.Payload.ResourceID] != 2 {
		t.Fatalf("source and target must each be looked at twice: %v", inspector.seen)
	}
	if r.startState(t) != "reverted" || r.targetReservation(t) != "released" {
		t.Fatalf("start %s target reservation %s", r.startState(t), r.targetReservation(t))
	}
	if held, released, observed := sourceRows(t, r.f, source); held != 0 || released != 1 || observed != "absent" {
		t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
	}
	// A replay returns the same receipt and releases nothing more.
	if again, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector); err != nil || again != receipt {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if held, released, _ := sourceRows(t, r.f, source); held != 0 || released != 1 {
		t.Fatalf("replay changed the source: held=%d released=%d", held, released)
	}
}

// The source keeps its reservation unless it is proven absent: a source still there is never released.
func TestSettleOrphanedEnableStartKeepsASourceStillThere(t *testing.T) {
	r, source := enableStartAsReserved(t)
	r.claimOrphan(t, false)
	inspector := &keyedInspector{byResource: map[string]driver.TargetInspection{source: {Found: true, NativeID: strings.Repeat("cd", 32), State: "exited"}}}
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "never_ran" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.targetReservation(t) != "released" {
		t.Fatal("the target's reservation was kept")
	}
	if held, released, observed := sourceRows(t, r.f, source); held != 1 || released != 0 || observed != "stopped" {
		t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
	}
}

// An orphaned enable start whose target exists settles applied, and releases the source once it is proven gone.
func TestSettleOrphanedEnableStartWithAContainer(t *testing.T) {
	r, source := enableStartAsReserved(t)
	r.claimOrphan(t, true)
	native := strings.Repeat("ab", 32)
	inspector := &keyedInspector{byResource: map[string]driver.TargetInspection{
		r.command.Payload.ResourceID: {Found: true, NativeID: native, State: "running"},
	}}
	raw, sig := r.settleCommand(t, 1)
	receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, inspector)
	if err != nil || receipt.Outcome != "applied" || receipt.NativeID != native {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	if r.startState(t) != "applied" || r.targetReservation(t) != "held" {
		t.Fatalf("start %s target reservation %s", r.startState(t), r.targetReservation(t))
	}
	if held, released, observed := sourceRows(t, r.f, source); held != 0 || released != 1 || observed != "absent" {
		t.Fatalf("source held=%d released=%d observed=%s", held, released, observed)
	}
}

// A settlement over a start of another kind is refused before the journal is read.
func TestSettleUpdateStartStillRefusesOtherKinds(t *testing.T) {
	r := reservedStart(t)
	r.orphaned(t, false)
	start := r.command
	start.Kind = "start-candidate"
	rawStart, _, err := control.SignUpdateStart(start, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err == nil {
		t.Fatalf("a start of another kind was signed: %s", rawStart)
	}
	if _, err := control.SettleUpdateStartLogicalDigest(control.SettleUpdateStartCommand{StartRaw: []byte(`{}`)}); !errors.Is(err, control.ErrInvalidCommand) {
		t.Fatal("an empty start was accepted")
	}
}

// The reserved-only removal also covers a prepared enable or restore start that never ran.
func TestReservedRemovalCoversEnableAndRestoreStarts(t *testing.T) {
	for name, build := range map[string]func(*testing.T) reservedStartFixture{
		"restore": restoreStartFixture,
		"enable":  func(t *testing.T) reservedStartFixture { r, _ := enableStartAsReserved(t); return r },
	} {
		t.Run(name, func(t *testing.T) {
			r := build(t)
			prover := &fakeTargetProver{}
			raw, sig := r.removal(t, "stop", 1)
			stopped, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover)
			if err != nil || stopped.State != "stopped" {
				t.Fatalf("stop: %+v %v", stopped, err)
			}
			if r.startState(t) != "reverted" {
				t.Fatalf("the start is %s", r.startState(t))
			}
			raw, sig = r.removal(t, "remove_code", 2)
			if removed, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil || removed.State != "absent" {
				t.Fatalf("remove: %+v %v", removed, err)
			}
			if desired, observed, _ := r.resource(t); desired != "absent" || observed != "absent" {
				t.Fatalf("resource: %s %s", desired, observed)
			}
		})
	}
}
