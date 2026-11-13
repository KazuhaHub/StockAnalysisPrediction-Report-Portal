//go:build linux || darwin

package journal

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
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

func holdFor(t *testing.T, s *Store, resource, installation string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',256,500,0,0,'held',1,1)`,
		fixtureID(8900), resource, installation); err != nil {
		t.Fatal(err)
	}
}

func reservationState(t *testing.T, s *Store, resource string) string {
	t.Helper()
	var state string
	if err := s.db.QueryRow(`SELECT state FROM runner_reservations WHERE resource_id=?`, resource).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// An orphaned start the runner proves never ran has no container: its reservation is released with the proof.
func TestOrphanSettlementReleasesTheReservationOfAStartThatNeverRan(t *testing.T) {
	r := reservedStart(t)
	r.orphaned(t, false)
	holdFor(t, r.f.store, r.command.Payload.ResourceID, r.command.InstallationID)
	raw, sig := r.settleCommand(t, 1)
	if receipt, err := r.f.store.SettleUpdateStart(context.Background(), raw, sig, &fakeInspector{}); err != nil || receipt.Outcome != "never_ran" {
		t.Fatalf("%+v %v", receipt, err)
	}
	if reservationState(t, r.f.store, r.command.Payload.ResourceID) != "released" {
		t.Fatal("the orphan settlement did not release the reservation")
	}
}

// The advance of an update-start removal releases at remove_code, where the container is gone, and not at stop.
func TestUpdateStartRemovalReleasesOnlyAtRemoveCode(t *testing.T) {
	s := capacityStore(t, roomyBudgets())
	resource, native := fixtureID(8910), strings.Repeat("a", 64)
	if _, err := s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,?,'container',?,?,1,?,1,1,'present','running',?,2,1,1,1)`,
		resource, fixtureID(8911), fixtureID(8912), native, strings.Repeat("a", 64), fixtureID(2), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	holdFor(t, s, resource, fixtureID(8911))
	advance := func(stage string) {
		tx, err := s.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		n, err := advanceUpdateStartRemovalResource(context.Background(), tx, resource, native, stage, 5, 6)
		if err != nil || n != 1 {
			t.Fatalf("%s: %d %v", stage, n, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	advance("stop")
	if reservationState(t, s, resource) != "held" {
		t.Fatal("a stop released the reservation")
	}
	advance("remove_code")
	if reservationState(t, s, resource) != "released" {
		t.Fatal("the removal did not release the reservation")
	}
}

// callsReleaseReservation reports whether the named function of a source file calls releaseReservation. The two
// settlements below take opaque native observations that only a Linux daemon can produce, so their release hook
// is pinned by the source: deleting the call fails the test.
func callsReleaseReservation(t *testing.T, file, function string) bool {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found, calls := false, false
	for _, declaration := range parsed.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function || fn.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "releaseReservation" {
					calls = true
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("%s has no function %s", file, function)
	}
	return calls
}

func TestSettlementsThatProveAbsenceReleaseTheReservation(t *testing.T) {
	for _, tc := range []struct{ file, function string }{
		{"managed_removal.go", "settleManagedRemoval"},
		{"delete_data.go", "settleDeleteData"},
		{"migrate_data.go", "settleMigrateRows"},
		{"update_start_settle.go", "settleOrphan"},
	} {
		if !callsReleaseReservation(t, tc.file, tc.function) {
			t.Errorf("%s in %s no longer releases the reservation of the resource it proves absent", tc.function, tc.file)
		}
	}
}
