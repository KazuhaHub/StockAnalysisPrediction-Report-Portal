package journal

// CapacityBudgets are the absolute ceilings the runner admits reservations
// against (doc 171). The service derives them from host facts at startup.
type CapacityBudgets struct {
	MemoryMiB       int64
	CPUMillis       int64
	DiskBytes       int64
	ImageStoreBytes int64
	RunningBackends int64
}
