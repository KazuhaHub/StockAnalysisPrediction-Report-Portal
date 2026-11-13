//go:build linux || darwin

package journal

import (
	"context"
	"testing"
)

// A reservation is released in the transaction that records the resource's proven absence, and not before: a stop
// leaves the container on the host, so it keeps counting.
func TestReservationIsReleasedOnlyWhenAbsenceIsProven(t *testing.T) {
	r := reservedStart(t)
	db := r.f.store.db
	if err := r.f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',256,500,0,0,'held',1,1)`,
		fixtureID(8301), r.command.Payload.ResourceID, r.command.InstallationID); err != nil {
		t.Fatal(err)
	}
	prover := &fakeTargetProver{}
	raw, sig := r.removal(t, "stop", 1)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
		t.Fatal(err)
	}
	if scalar(t, db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+r.command.Payload.ResourceID+`' AND state='held'`) != 1 {
		t.Fatal("a stop released the reservation")
	}
	raw, sig = r.removal(t, "remove_code", 2)
	if _, err := r.f.store.RemoveReservedUpdateStart(context.Background(), raw, sig, prover); err != nil {
		t.Fatal(err)
	}
	if scalar(t, db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+r.command.Payload.ResourceID+`' AND state='released'`) != 1 {
		t.Fatal("the proven removal did not release the reservation")
	}
}
