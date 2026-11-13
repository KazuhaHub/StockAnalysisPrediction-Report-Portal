//go:build linux || darwin

package journal

import (
	"strings"
	"testing"
)

// leakedStart seeds one installation's reserved candidate start: a pending resource that never reached the
// native layer, its reservation, and the start command in the given state.
func leakedStart(t *testing.T, s *Store, n int, startState string) (installation, resource string) {
	t.Helper()
	installation, resource = fixtureID(9000+n), fixtureID(9100+n)
	if _, err := s.db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,?,'container',?,?,1,?,2,1,'present','unknown',?,1,NULL,1,1)`,
		resource, installation, fixtureID(9200+n), "writable-candidate-pending/v1/"+resource, strings.Repeat("a", 64), fixtureID(2), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	receipt := "{}"
	if startState == "unknown" {
		receipt = `{"claim":1}`
	}
	if _, err := s.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
        VALUES(?,?,?,'start',1,'start-candidate',?,1,?,2,?,?,NULL,1,1)`, fixtureID(9300+n), fixtureID(9400+n), installation, strings.Repeat("c", 64), fixtureID(2), startState, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO runner_reservations(id,resource_id,installation_id,kind,memory_mib,cpu_millis,disk_bytes,image_bytes,state,created_ms,updated_ms) VALUES(?,?,?,'backend',16,25,0,0,'held',1,1)`, fixtureID(9500+n), resource, installation); err != nil {
		t.Fatal(err)
	}
	return installation, resource
}

func supersede(t *testing.T, s *Store, n int, installation string) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
        VALUES(?,?,?,'stop',1,'stop',?,2,?,3,'prepared','{}',NULL,2,2)`, fixtureID(9600+n), fixtureID(9700+n), installation, strings.Repeat("d", 64), fixtureID(2)); err != nil {
		t.Fatal(err)
	}
}

// Eight installs whose starts were reserved and then abandoned or superseded must not keep the runner at its
// eight-backend limit: once each is settled by definite evidence, a ninth backend is admitted.
func TestAbandonedStartsDoNotExhaustRunningBackends(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state string
		prep  func(t *testing.T, s *Store, n int, installation string)
	}{
		{"superseded_before_claim", "prepared", supersede},
		{"reverted", "reverted", func(*testing.T, *Store, int, string) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8})
			for n := 1; n <= 8; n++ {
				installation, _ := leakedStart(t, s, n, tc.state)
				tc.prep(t, s, n, installation)
			}
			if refusal := admitPlan(t, s, backendPlan(1, 16, 25)); refusal != nil {
				t.Fatalf("a ninth backend was refused although the eight starts are settled: %+v", refusal)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE state='released'`) != 8 {
				t.Fatal("the settled reservations were not released")
			}
		})
	}
}

// A start that is only reserved, or that was claimed and is still unknown, may have a container: it keeps its
// reservation even when a newer command exists.
func TestUnsettledStartsKeepTheirReservation(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 2})
	installation, _ := leakedStart(t, s, 1, "unknown")
	supersede(t, s, 1, installation)
	leakedStart(t, s, 2, "prepared") // reserved, nothing newer, a renewed command may still claim it
	if refusal := admitPlan(t, s, backendPlan(1, 16, 25)); refusal == nil || refusal.dimension != CapacityRunningBackends {
		t.Fatalf("an unsettled start lost its reservation: %+v", refusal)
	}
	if heldCount(t, s) != 2 {
		t.Fatal("a reservation was released without evidence")
	}
}
