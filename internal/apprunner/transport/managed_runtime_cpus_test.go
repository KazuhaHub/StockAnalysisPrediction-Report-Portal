package transport

import (
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/cpuset"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func capacityFixture() ManagedRuntimeCapacity {
	return ManagedRuntimeCapacity{MemoryMiB: "4096", CPUMillis: "2000", DiskBytes: "8589934592", ImageStoreBytes: "10737418240", RunningBackends: "8", EstimatedRows: "0"}
}

func TestManagedCapacityReportsTheCPUSets(t *testing.T) {
	c := capacityFixture()
	if !validManagedCapacity(c) {
		t.Fatal("capacity without a reservation refused")
	}
	c.ReservedCPUs, c.AppCPUs = "0", "1-3"
	if !validManagedCapacity(c) {
		t.Fatal("capacity with a reservation refused")
	}
	for name, mutate := range map[string]func(*ManagedRuntimeCapacity){
		"reserved without app": func(c *ManagedRuntimeCapacity) { c.AppCPUs = "" },
		"app without reserved": func(c *ManagedRuntimeCapacity) { c.ReservedCPUs = "" },
		"malformed app":        func(c *ManagedRuntimeCapacity) { c.AppCPUs = "1-" },
		"malformed reserved":   func(c *ManagedRuntimeCapacity) { c.ReservedCPUs = "zero" },
		"overlapping sets":     func(c *ManagedRuntimeCapacity) { c.AppCPUs = "0-3" },
		"not canonical":        func(c *ManagedRuntimeCapacity) { c.AppCPUs = "1,2,3" },
	} {
		bad := c
		mutate(&bad)
		if validManagedCapacity(bad) {
			t.Fatalf("%s accepted: %+v", name, bad)
		}
	}
}

func budgetsWithApp(reserved, app string) journal.CapacityBudgets {
	return journal.CapacityBudgets{MemoryMiB: 4096, CPUMillis: 2000, DiskBytes: 8 << 30, ImageStoreBytes: 10 << 30, RunningBackends: 8, ReservedCPUs: reserved, AppCPUs: app}
}

func TestManagedProfileFitsTheDecoderCapOnlyWhileTheCPUSetsAreBounded(t *testing.T) {
	if !ManagedProfileFits(budgetsWithApp("", "")) || !ManagedProfileFits(budgetsWithApp("0", "1-3")) || !ManagedProfileFits(budgetsWithApp("0", "1-1023")) {
		t.Fatal("an ordinary profile does not fit")
	}
	// A sparse online list: every CPU its own range.
	var sparse []int
	for cpu := 1; len(sparse) < 700; cpu += 3 {
		sparse = append(sparse, cpu)
	}
	if ManagedProfileFits(budgetsWithApp("0", cpuset.Format(sparse))) {
		t.Fatalf("a %d byte CPU list fits a %d byte cap", len(cpuset.Format(sparse)), maxManagedRuntimeBytes)
	}
	// What is admitted by the check is accepted by the decoder, at the worst-case field widths.
	worst := budgetsWithApp("0", "1-3")
	raw, err := json.Marshal(worstCaseManagedWire(worst))
	if err != nil || len(raw) > maxManagedRuntimeBytes {
		t.Fatalf("worst case wire is %d bytes: %v", len(raw), err)
	}
}
