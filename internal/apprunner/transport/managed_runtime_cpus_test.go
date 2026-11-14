package transport

import "testing"

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
