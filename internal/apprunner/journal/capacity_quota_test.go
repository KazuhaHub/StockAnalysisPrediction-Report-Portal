package journal

import (
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

const gib = int64(1) << 30

func quotaPlan(t *testing.T, s *Store, n int, quota, inodes int64) capacityPlan {
	t.Helper()
	plan, ok := s.namespacePlan(fixtureID(n), fixtureID(500+n), quota, inodes)
	if !ok {
		t.Fatal("no namespace plan")
	}
	return plan
}

// A namespace reserves its quota and two retained checkpoints, each as large as the archive cap: the cap is
// what a checkpoint may take, so a fully reserved volume can always hold both.
func TestNamespaceReservationCoversTwoArchiveCaps(t *testing.T) {
	data, checkpoints, ok := NamespaceReservation(3*gib, 100000)
	cap := dataArchiveCap(3*gib, 100000)
	if !ok || data != 3*gib || checkpoints != 2*cap || cap <= 3*gib {
		t.Fatalf("reservation = %d + %d (cap %d) %v", data, checkpoints, cap, ok)
	}
	// The default namespace keeps the 512 MiB floor of the cap.
	if _, checkpoints, _ := NamespaceReservation(256<<20, 65536); checkpoints != 2*(512<<20) {
		t.Fatalf("default checkpoints reserve %d", checkpoints)
	}
	for _, bad := range [][2]int64{{0, 65536}, {-1, 65536}, {256 << 20, 0}, {1 << 62, 65536}, {256 << 20, 1 << 62}, {16<<30 + 1, 65536}, {256 << 20, 1000001}} {
		if _, _, ok := NamespaceReservation(bad[0], bad[1]); ok {
			t.Fatalf("reservation for %v accepted", bad)
		}
	}
}

func dataArchiveCap(quota, inodes int64) int64 { return data.ArchiveCapFor(quota, inodes) }

// Shared filesystem: the data and both retained checkpoints count against one disk budget.
func TestQuotaAdmissionOnASharedVolumeCountsTheCheckpointCaps(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 11 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	first := quotaPlan(t, s, 1, 3*gib, 100000)
	total := first.diskBytes + first.checkpointBytes
	if refusal := admitPlan(t, s, first); refusal != nil {
		t.Fatalf("3 GiB refused on an 11 GiB budget: %+v", refusal)
	}
	refusal := admitPlan(t, s, quotaPlan(t, s, 2, 3*gib, 100000))
	if refusal == nil || refusal.dimension != CapacityDataQuota {
		t.Fatalf("second 3 GiB namespace: %+v", refusal)
	}
	if refusal.requested != total || refusal.requested-refusal.remaining != total-(11*gib-total) {
		t.Fatalf("refusal %+v, total %d", refusal, total)
	}
	if heldCount(t, s) != 2 {
		t.Fatalf("held rows = %d, want the data row and the checkpoint row of one namespace", heldCount(t, s))
	}
}

// Separate checkpoint volume: the data volume holds one quota per namespace, the checkpoint volume two caps.
func TestQuotaAdmissionOnSeparateVolumesCountsEachVolume(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 7 * gib, CheckpointBytes: 7 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	// One 2 GiB namespace: its two caps are a little over 4 GiB.
	if refusal := admitPlan(t, s, quotaPlan(t, s, 1, 2*gib, 100000)); refusal != nil {
		t.Fatalf("2 GiB refused on 7 GiB volumes: %+v", refusal)
	}
	second := quotaPlan(t, s, 2, 2*gib, 100000)
	refusal := admitPlan(t, s, second)
	if refusal == nil || refusal.dimension != CapacityDataQuotaCheckpoint || refusal.requested != second.checkpointBytes {
		t.Fatalf("checkpoint volume: %+v", refusal)
	}
	if refusal := admitPlan(t, s, quotaPlan(t, s, 3, 256<<20, 65536)); refusal != nil {
		t.Fatalf("small namespace refused: %+v", refusal)
	}
	small := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 2 * gib, CheckpointBytes: 100 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	if refusal := admitPlan(t, small, quotaPlan(t, small, 1, 3*gib, 100000)); refusal == nil || refusal.dimension != CapacityDataQuota || refusal.requested != 3*gib || refusal.remaining != 2*gib {
		t.Fatalf("data volume: %+v", refusal)
	}
}

// A raise replaces the namespace's own reservation instead of counting twice, and releasing one
// releases both rows.
func TestQuotaRaiseReplacesTheNamespaceReservation(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 11 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	if refusal := admitPlan(t, s, quotaPlan(t, s, 1, 256<<20, 65536)); refusal != nil {
		t.Fatal(refusal)
	}
	raised := quotaPlan(t, s, 1, 3*gib, 100000)
	if refusal := admitPlan(t, s, raised); refusal != nil {
		t.Fatalf("raise to 3 GiB refused: %+v", refusal)
	}
	if got := scalar(t, s.db, `SELECT COALESCE(SUM(disk_bytes),0) FROM runner_reservations WHERE state='held'`); int64(got) != raised.diskBytes+raised.checkpointBytes {
		t.Fatalf("held disk = %d", got)
	}
	if refusal := admitPlan(t, s, quotaPlan(t, s, 1, 4*gib, 100000)); refusal == nil || refusal.dimension != CapacityDataQuota {
		t.Fatalf("raise beyond the budget: %+v", refusal)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseReservation(t.Context(), tx, fixtureID(1), 20); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if heldCount(t, s) != 0 {
		t.Fatal("releasing the namespace left its checkpoint reservation held")
	}
}
