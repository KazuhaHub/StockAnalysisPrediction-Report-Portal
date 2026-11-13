//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestUpdateStartClaimHoldsTheDeclaredResources(t *testing.T) {
	r := reservedStart(t)
	if err := r.f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	if _, err := r.f.store.ClaimUpdateStart(ctx, r.raw, r.sig); err != nil {
		t.Fatal(err)
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE state='held' AND kind='backend' AND memory_mib=256 AND cpu_millis=500 AND resource_id='`+r.command.Payload.ResourceID+`'`) != 1 {
		t.Fatal("update start not reserved at its declared amounts")
	}
}

func TestUpdateStartClaimRefusedRevertsAndLeavesNoRows(t *testing.T) {
	r := reservedStart(t)
	budgets := roomyBudgets()
	budgets.RunningBackends = 1
	if err := r.f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	// Another installation already runs the only backend the runner may.
	if _, err := r.f.store.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',16,25,0,0,'held',1,1)`, fixtureID(8001), fixtureID(8002), fixtureID(8003)); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	_, err := r.f.store.ClaimUpdateStart(ctx, r.raw, r.sig)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityRunningBackends || refusal.Receipt.Requested != "1" || refusal.Receipt.Remaining != "0" {
		t.Fatalf("err = %v", err)
	}
	if r.startState(t) != "reverted" {
		t.Fatalf("start state = %s", r.startState(t))
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_resources WHERE id='`+r.command.Payload.ResourceID+`'`) != 0 {
		t.Fatal("the never started target resource was left behind")
	}
	if scalar(t, r.f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+r.command.Payload.ResourceID+`'`) != 0 {
		t.Fatal("a reservation was left behind")
	}
	// The read-back, signed as a read, returns the same receipt; an execute signature does not.
	got, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, updateStartReadSignature(t, r.raw))
	if receipt, ok := got.(CapacityRefusal); err != nil || !ok || receipt != refusal.Receipt {
		t.Fatalf("read-back = %#v %v", got, err)
	}
	if got, err := r.f.store.ReadUpdateStartRunningOrAbsence(context.Background(), r.raw, r.sig); err == nil {
		if _, ok := got.(CapacityRefusal); ok {
			t.Fatal("an execute signature read the refusal")
		}
	}
	// A replayed dispatch-side lookup answers the same receipt.
	if again := r.f.store.lookupCapacityRefusal(context.Background(), CapacityIdentityOfUpdateStart(r.command)); again == nil || again.Receipt != refusal.Receipt {
		t.Fatal("replay lookup")
	}
}

func oldBackendFor(t *testing.T, r reservedStartFixture, observed string) string {
	t.Helper()
	db := r.f.store.db
	old := fixtureID(8100)
	if _, err := db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,?,'container','old-backend',?,1,?,1,1,?,?,?,1,NULL,1,1)`,
		old, r.command.InstallationID, r.command.Payload.NamespaceID, strings.Repeat("c", 64), r.command.ControllerEpoch, map[string]string{"absent": "absent", "stopped": "stopped"}[observed], observed, strings.Repeat("d", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',4096,4000,0,0,'held',1,1)`, fixtureID(8101), old, r.command.InstallationID); err != nil {
		t.Fatal(err)
	}
	return old
}

func TestUpdateStartNetsOnlyAProvenAbsentOldBackend(t *testing.T) {
	// The old backend of the same installation holds the whole budget.
	budgets := CapacityBudgets{MemoryMiB: 4096, CPUMillis: 4000, DiskBytes: 8 << 30, ImageStoreBytes: 10 << 30, RunningBackends: 8}
	ctx := withBackendResources(context.Background(), fixtureResources())

	stopped := reservedStart(t)
	if err := stopped.f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	oldStopped := oldBackendFor(t, stopped, "stopped")
	_, err := stopped.f.store.ClaimUpdateStart(ctx, stopped.raw, stopped.sig)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityMemory {
		t.Fatalf("a merely stopped old backend was netted: %v", err)
	}
	if scalar(t, stopped.f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+oldStopped+`' AND state='held'`) != 1 {
		t.Fatal("the stopped old backend lost its reservation")
	}

	absent := reservedStart(t)
	if err := absent.f.store.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	oldAbsent := oldBackendFor(t, absent, "absent")
	if _, err := absent.f.store.ClaimUpdateStart(ctx, absent.raw, absent.sig); err != nil {
		t.Fatalf("update refused although the old backend is proven absent: %v", err)
	}
	if scalar(t, absent.f.store.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+oldAbsent+`' AND state='released'`) != 1 {
		t.Fatal("old reservation was not netted")
	}
}

func TestUpdateStartClaimWithoutDeclaredResourcesFailsClosed(t *testing.T) {
	r := reservedStart(t)
	if err := r.f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.store.ClaimUpdateStart(context.Background(), r.raw, r.sig); !errors.Is(err, ErrCapacityPlanMissing) {
		t.Fatalf("err = %v", err)
	}
	if r.startState(t) != "prepared" {
		t.Fatal("a failed admission changed the start")
	}
}
