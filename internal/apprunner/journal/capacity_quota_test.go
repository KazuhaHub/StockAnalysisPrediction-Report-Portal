package journal

import "testing"

const gib = int64(1) << 30

func quotaPlan(n int, quota int64) capacityPlan {
	return capacityPlan{kind: reservationNamespace, resourceID: fixtureID(n), installationID: fixtureID(500 + n), diskBytes: NamespaceReservationFor(quota), quotaBytes: quota}
}

func TestNamespaceReservationScalesWithTheQuota(t *testing.T) {
	if NamespaceReservationFor(256<<20) != 768<<20 || NamespaceReservationFor(3*gib) != 9*gib {
		t.Fatalf("reservation = %d, %d", NamespaceReservationFor(256<<20), NamespaceReservationFor(3*gib))
	}
	if NamespaceReservationFor(256<<20) != NamespaceReservationBytes {
		t.Fatal("the default quota must reserve what every namespace reserved before")
	}
}

// Shared filesystem: the data and both retained checkpoints count against one disk budget.
func TestQuotaAdmissionOnASharedVolumeCountsThreeQuotas(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 10 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	if refusal := admitPlan(t, s, quotaPlan(1, 3*gib)); refusal != nil {
		t.Fatalf("3 GiB refused on a 10 GiB budget: %+v", refusal)
	}
	refusal := admitPlan(t, s, quotaPlan(2, 3*gib))
	if refusal == nil || refusal.dimension != CapacityDataQuota {
		t.Fatalf("second 3 GiB namespace: %+v", refusal)
	}
	if shortfall := refusal.requested - refusal.remaining; shortfall != 8*gib {
		t.Fatalf("shortfall = %d, want 8 GiB (9 GiB asked, 1 GiB left)", shortfall)
	}
	if heldCount(t, s) != 1 {
		t.Fatal("a refused namespace left a reservation")
	}
}

// Separate checkpoint volume: the data volume holds one quota per namespace, the checkpoint volume two.
func TestQuotaAdmissionOnSeparateVolumesCountsEachVolume(t *testing.T) {
	budgets := CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 7 * gib, CheckpointBytes: 7 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8}
	s := capacityStore(t, budgets)
	if refusal := admitPlan(t, s, quotaPlan(1, 3*gib)); refusal != nil {
		t.Fatalf("market-monitor's 3 GiB refused on 7 GiB volumes: %+v", refusal)
	}
	// 3 GiB more fits the data volume (6 of 7) but its two checkpoints do not fit the checkpoint volume.
	refusal := admitPlan(t, s, quotaPlan(2, 3*gib))
	if refusal == nil || refusal.dimension != CapacityDataQuotaCheckpoint {
		t.Fatalf("checkpoint volume: %+v", refusal)
	}
	if refusal.requested != 6*gib || refusal.remaining != gib {
		t.Fatalf("checkpoint refusal %+v", refusal)
	}
	// A small app still fits both volumes.
	if refusal := admitPlan(t, s, quotaPlan(3, 256<<20)); refusal != nil {
		t.Fatalf("small namespace refused: %+v", refusal)
	}
	// The data volume refuses first when it is the smaller one.
	small := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 2 * gib, CheckpointBytes: 100 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	if refusal := admitPlan(t, small, quotaPlan(1, 3*gib)); refusal == nil || refusal.dimension != CapacityDataQuota || refusal.requested != 3*gib || refusal.remaining != 2*gib {
		t.Fatalf("data volume: %+v", refusal)
	}
}

// A raise replaces the namespace's own reservation instead of counting twice.
func TestQuotaRaiseReplacesTheNamespaceReservation(t *testing.T) {
	s := capacityStore(t, CapacityBudgets{MemoryMiB: 1 << 20, CPUMillis: 1 << 20, DiskBytes: 10 * gib, ImageStoreBytes: 1 << 40, RunningBackends: 8})
	if refusal := admitPlan(t, s, quotaPlan(1, 256<<20)); refusal != nil {
		t.Fatal(refusal)
	}
	if refusal := admitPlan(t, s, quotaPlan(1, 3*gib)); refusal != nil {
		t.Fatalf("raise to 3 GiB refused: %+v", refusal)
	}
	if got := scalar(t, s.db, `SELECT disk_bytes FROM runner_reservations WHERE resource_id='`+fixtureID(1)+`'`); int64(got) != 9*gib {
		t.Fatalf("held disk = %d", got)
	}
	if refusal := admitPlan(t, s, quotaPlan(1, 4*gib)); refusal == nil || refusal.dimension != CapacityDataQuota {
		t.Fatalf("raise beyond the budget: %+v", refusal)
	}
}
