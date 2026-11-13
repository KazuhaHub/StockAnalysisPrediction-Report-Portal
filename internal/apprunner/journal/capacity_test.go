//go:build linux || darwin

package journal

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func capacityStore(t *testing.T, b CapacityBudgets) *Store {
	t.Helper()
	s, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.SetCapacityBudgets(b); err != nil {
		t.Fatal(err)
	}
	return s
}

// admitPlan runs one admission in its own transaction, committing only an admission.
func admitPlan(t *testing.T, s *Store, plan capacityPlan) *capacityRefusal {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	refusal, err := s.admitCapacity(context.Background(), tx, plan, 10)
	if err != nil {
		t.Fatal(err)
	}
	if refusal == nil {
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return refusal
}

func backendPlan(n int, memory, cpu int64) capacityPlan {
	return capacityPlan{kind: reservationBackend, resourceID: fixtureID(n), installationID: fixtureID(500 + n), memoryMiB: memory, cpuMillis: cpu}
}

func heldCount(t *testing.T, s *Store) int {
	return scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE state='held'`)
}

func TestAdmissionRefusesEachDimensionByOneUnit(t *testing.T) {
	const mib = int64(1) << 20
	budgets := CapacityBudgets{MemoryMiB: 100, CPUMillis: 1000, DiskBytes: 2 * NamespaceReservationBytes, ImageStoreBytes: 10 * mib, RunningBackends: 3}
	cases := []struct {
		name      string
		dimension string
		fits      capacityPlan // admitted, uses the whole remaining amount of the dimension
		over      capacityPlan // one unit more than remains
		remaining int64
	}{
		{"memory", CapacityMemory, backendPlan(1, 100, 1), backendPlan(2, 101, 1), 100},
		{"cpu", CapacityCPU, backendPlan(1, 1, 1000), backendPlan(2, 1, 1001), 1000},
		{"disk", CapacityDisk,
			capacityPlan{kind: reservationNamespace, resourceID: fixtureID(1), installationID: fixtureID(501), diskBytes: 2 * NamespaceReservationBytes},
			capacityPlan{kind: reservationNamespace, resourceID: fixtureID(2), installationID: fixtureID(502), diskBytes: 2*NamespaceReservationBytes + 1}, 2 * NamespaceReservationBytes},
		{"image_store", CapacityImageStore,
			capacityPlan{kind: reservationImage, resourceID: imageResourceID(strings.Repeat("1", 64)), installationID: fixtureID(501), imageBytes: 10 * mib},
			capacityPlan{kind: reservationImage, resourceID: imageResourceID(strings.Repeat("2", 64)), installationID: fixtureID(502), imageBytes: 10*mib + 1}, 10 * mib},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := capacityStore(t, budgets)
			before := scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`)
			refusal := admitPlan(t, s, tc.over)
			if refusal == nil || refusal.dimension != tc.dimension || refusal.remaining != tc.remaining {
				t.Fatalf("over request: %+v", refusal)
			}
			if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != before {
				t.Fatal("a refused request left a reservation behind")
			}
			if refusal := admitPlan(t, s, tc.fits); refusal != nil {
				t.Fatalf("request that fits refused: %+v", refusal)
			}
			if heldCount(t, s) != 1 {
				t.Fatal("admitted request not held")
			}
			// Once the dimension is full, one more unit of it is refused with nothing remaining.
			if refusal := admitPlan(t, s, tc.over); refusal == nil || refusal.dimension != tc.dimension || refusal.remaining != 0 {
				t.Fatalf("full dimension: %+v", refusal)
			}
		})
	}
}

func TestAdmissionRefusesTheNinthBackend(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	for n := 1; n <= 8; n++ {
		if refusal := admitPlan(t, s, backendPlan(n, 16, 25)); refusal != nil {
			t.Fatalf("backend %d refused: %+v", n, refusal)
		}
	}
	refusal := admitPlan(t, s, backendPlan(9, 16, 25))
	if refusal == nil || refusal.dimension != CapacityRunningBackends || refusal.requested != 1 || refusal.remaining != 0 {
		t.Fatalf("ninth backend: %+v", refusal)
	}
	if heldCount(t, s) != 8 {
		t.Fatal("refused backend left a row")
	}
}

func TestMigrationCountsAgainstMemoryAndCPUButNotBackends(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 64, CPUMillis: 100, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 1})
	if refusal := admitPlan(t, s, backendPlan(1, 32, 50)); refusal != nil {
		t.Fatal(refusal)
	}
	// The backend limit is full, yet a migration job still fits the memory and CPU left.
	job := capacityPlan{kind: reservationMigration, resourceID: fixtureID(2), installationID: fixtureID(502), memoryMiB: 32, cpuMillis: 50}
	if refusal := admitPlan(t, s, job); refusal != nil {
		t.Fatalf("migration refused by the backend count: %+v", refusal)
	}
	job.resourceID = fixtureID(3)
	if refusal := admitPlan(t, s, job); refusal == nil || refusal.dimension != CapacityMemory {
		t.Fatalf("a second job beyond the memory budget: %+v", refusal)
	}
}

func TestAdmissionReplayAndReplacementDoNotDoubleCount(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 100, CPUMillis: 1000, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	p := backendPlan(1, 100, 10)
	if refusal := admitPlan(t, s, p); refusal != nil {
		t.Fatal(refusal)
	}
	// The same resource asks again, as a replay does: it is already counted.
	if refusal := admitPlan(t, s, p); refusal != nil {
		t.Fatalf("replay double counted: %+v", refusal)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 1 {
		t.Fatal("replay created a second row")
	}
	// A backfilled row is replaced by the declared amount of the same resource.
	if _, err := s.db.Exec(`UPDATE runner_reservations SET memory_mib=4096,cpu_millis=4000`); err != nil {
		t.Fatal(err)
	}
	smaller := backendPlan(1, 30, 10)
	if refusal := admitPlan(t, s, smaller); refusal != nil {
		t.Fatalf("declared amount refused although the resource is replacing its own row: %+v", refusal)
	}
	if scalar(t, s.db, `SELECT memory_mib FROM runner_reservations`) != 30 {
		t.Fatal("backfilled amount not replaced by the declared one")
	}
}

func TestNettingRequiresProvenAbsenceInTheSameTransaction(t *testing.T) {
	budgets := CapacityBudgets{MemoryMiB: 100, CPUMillis: 1000, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8}
	s := capacityStore(t, budgets)
	old := backendPlan(1, 100, 100)
	if refusal := admitPlan(t, s, old); refusal != nil {
		t.Fatal(refusal)
	}
	// The old backend's resource, same installation, is stopped but not removed: both rows count.
	insertResourceRow(t, s.db, 1, "container", "present", "stopped")
	replacement := capacityPlan{kind: reservationBackend, resourceID: fixtureID(2), installationID: old.installationID, memoryMiB: 100, cpuMillis: 100}
	if refusal := admitPlan(t, s, replacement); refusal == nil || refusal.dimension != CapacityMemory {
		t.Fatalf("netted a backend that is only stopped: %+v", refusal)
	}
	if heldCount(t, s) != 1 {
		t.Fatal("a refused replacement changed the old reservation")
	}
	// Once the resource is proven absent, the old reservation is netted in the replacement's transaction.
	if _, err := s.db.Exec(`UPDATE runner_resources SET desired_state='absent',observed_state='absent',revision=4 WHERE id=?`, fixtureID(1)); err != nil {
		t.Fatal(err)
	}
	if refusal := admitPlan(t, s, replacement); refusal != nil {
		t.Fatalf("replacement refused although the old resource is proven absent: %+v", refusal)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+fixtureID(1)+`' AND state='released'`) != 1 || heldCount(t, s) != 1 {
		t.Fatal("old reservation not released by netting")
	}
	// A different installation's reservation is never netted, whatever its resource says.
	s2 := capacityStore(t, budgets)
	stranger := backendPlan(3, 50, 10)
	if refusal := admitPlan(t, s2, stranger); refusal != nil {
		t.Fatal(refusal)
	}
	insertResourceRow(t, s2.db, 3, "container", "absent", "absent")
	mine := backendPlan(4, 100, 10)
	if refusal := admitPlan(t, s2, mine); refusal == nil || refusal.dimension != CapacityMemory {
		t.Fatalf("netted another installation's reservation: %+v", refusal)
	}
	if scalar(t, s2.db, `SELECT COUNT(*) FROM runner_reservations WHERE state='released'`) != 0 {
		t.Fatal("a refusal released a reservation of another installation")
	}
}

func insertResourceRow(t *testing.T, db *sql.DB, n int, kind, desired, observed string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO runner_resources(id,installation_id,data_namespace_id,kind,native_id,release_sha256,generation,controller_epoch,control_fence,writer_fence,desired_state,observed_state,ownership_sha256,revision,observed_ms,created_ms,updated_ms)
        VALUES(?,?,?,?,?,?,1,?,1,1,?,?,?,1,NULL,1,1)`,
		fixtureID(n), fixtureID(500+n), fixtureID(900+n), kind, "native/"+fixtureID(n), strings.Repeat("a", 64), fixtureID(2), desired, observed, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseOnlyMarksTheNamedHeldRow(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 100, CPUMillis: 1000, DiskBytes: 1 << 40, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	admitPlan(t, s, backendPlan(1, 50, 10))
	admitPlan(t, s, backendPlan(2, 50, 10))
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseReservation(context.Background(), tx, fixtureID(1), 20); err != nil {
		t.Fatal(err)
	}
	// Releasing an unknown or already released resource changes nothing and is not an error.
	if err := releaseReservation(context.Background(), tx, fixtureID(99), 20); err != nil {
		t.Fatal(err)
	}
	if err := releaseReservation(context.Background(), tx, fixtureID(1), 21); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if heldCount(t, s) != 1 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE resource_id='`+fixtureID(2)+`' AND state='held'`) != 1 {
		t.Fatal("release changed the wrong rows")
	}
	if refusal := admitPlan(t, s, backendPlan(3, 50, 10)); refusal != nil {
		t.Fatalf("released memory not reusable: %+v", refusal)
	}
}

func TestBudgetsMustBePositive(t *testing.T) {
	s, err := Open(privateDirectory(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, b := range []CapacityBudgets{{}, {MemoryMiB: 1}, {MemoryMiB: 1, CPUMillis: 1, DiskBytes: 1, ImageStoreBytes: 1}, {MemoryMiB: -1, CPUMillis: 1, DiskBytes: 1, ImageStoreBytes: 1, RunningBackends: 1}} {
		if err := s.SetCapacityBudgets(b); err == nil {
			t.Fatalf("budgets %+v accepted", b)
		}
	}
	if _, ok := s.CapacityBudgets(); ok {
		t.Fatal("refused budgets were recorded")
	}
}

func TestNamespaceReservationIsTheQuotaPlusTwoCheckpoints(t *testing.T) {
	if NamespaceReservationBytes != 768<<20 {
		t.Fatalf("namespace reservation = %d", NamespaceReservationBytes)
	}
	if NamespaceQuotaBytes != 268435456 {
		t.Fatal("namespace quota constant drifted from the quota the Portal commands carry")
	}
}
