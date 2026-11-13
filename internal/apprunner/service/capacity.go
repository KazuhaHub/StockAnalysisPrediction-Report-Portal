package service

import (
	"errors"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// ErrNotReady is the runner's readiness refusal. Every capacity failure wraps it.
var ErrNotReady = errors.New("app-runner runtime is not ready")

// CapacityConfig is the optional, operator-declared share of the host the runner
// may reserve (doc 171). A field left out takes its default; an explicit value
// outside its range is a configuration error, never silently replaced.
type CapacityConfig struct {
	MemoryPercent *int   `json:"memory_percent,omitempty"`
	CPUPercent    *int   `json:"cpu_percent,omitempty"`
	DiskBytes     *int64 `json:"disk_bytes,omitempty"`
	// RunningBackends bounds the live backends: every backend reservation counts from its claim until its removal
	// is proven, so a stopped (disabled) backend counts exactly like a running one.
	RunningBackends *int   `json:"running_backends,omitempty"`
	ImageStoreBytes *int64 `json:"image_store_bytes,omitempty"`
}

const (
	gibibyte = int64(1) << 30

	defaultMemoryPercent   = 25
	defaultCPUPercent      = 50
	defaultDiskBytes       = 8 * gibibyte
	defaultRunningBackends = 8
	defaultImageStoreBytes = 10 * gibibyte
	maxImageStoreBytes     = int64(1) << 40
)

// ResolvedCapacity is a CapacityConfig with every default applied.
type ResolvedCapacity struct {
	MemoryPercent   int
	CPUPercent      int
	DiskBytes       int64
	RunningBackends int
	ImageStoreBytes int64
}

// Resolve applies the defaults. A nil receiver resolves to the defaults.
func (c *CapacityConfig) Resolve() ResolvedCapacity {
	r := ResolvedCapacity{MemoryPercent: defaultMemoryPercent, CPUPercent: defaultCPUPercent, DiskBytes: defaultDiskBytes, RunningBackends: defaultRunningBackends, ImageStoreBytes: defaultImageStoreBytes}
	if c == nil {
		return r
	}
	if c.MemoryPercent != nil {
		r.MemoryPercent = *c.MemoryPercent
	}
	if c.CPUPercent != nil {
		r.CPUPercent = *c.CPUPercent
	}
	if c.DiskBytes != nil {
		r.DiskBytes = *c.DiskBytes
	}
	if c.RunningBackends != nil {
		r.RunningBackends = *c.RunningBackends
	}
	if c.ImageStoreBytes != nil {
		r.ImageStoreBytes = *c.ImageStoreBytes
	}
	return r
}

func (c *CapacityConfig) valid() bool {
	if c == nil {
		return true
	}
	in := func(v *int, lo, hi int) bool { return v == nil || (*v >= lo && *v <= hi) }
	big := func(v *int64, lo, hi int64) bool { return v == nil || (*v >= lo && *v <= hi) }
	// The upper bound of disk_bytes is the volume size, which is only known at startup.
	return in(c.MemoryPercent, 1, 90) && in(c.CPUPercent, 1, 100) && big(c.DiskBytes, gibibyte, int64(1)<<62) &&
		in(c.RunningBackends, 1, 64) && big(c.ImageStoreBytes, gibibyte, maxImageStoreBytes)
}

// HostFacts are the three readings the budgets derive from. A zero reading means
// the fact could not be read.
type HostFacts struct {
	MemoryBytes int64
	CPUs        int
	VolumeBytes int64
	// CheckpointSeparate is set when the checkpoint root is on another filesystem than the data root. Then its
	// volume must hold the checkpoint share of the disk budget; otherwise the data volume covers both.
	CheckpointSeparate    bool
	CheckpointVolumeBytes int64
}

// CheckpointShare is the part of the disk budget the retained recovery checkpoints may use: two checkpoints of
// at most 256 MiB for every 768 MiB namespace, which is two thirds.
func CheckpointShare(diskBytes int64) int64 { return diskBytes / 3 * 2 }

// Typed reasons a runner reports for not being ready on capacity grounds.
const (
	NotReadyHostFact         = "host_fact_unreadable"
	NotReadyVolumeTooSmall   = "volume_smaller_than_disk_budget"
	NotReadyMemoryBudget     = "memory_budget_below_minimum_backend"
	NotReadyCPUBudget        = "cpu_budget_below_minimum_backend"
	NotReadyCheckpointVolume = "checkpoint_volume_smaller_than_share"
)

// NotReadyError carries the typed reason. It matches ErrNotReady.
type NotReadyError struct{ Reason string }

func (e *NotReadyError) Error() string        { return "app-runner runtime is not ready: " + e.Reason }
func (e *NotReadyError) Is(target error) bool { return target == ErrNotReady }

// DeriveBudgets turns the declared shares into absolute budgets. It fails closed:
// an unreadable fact, a volume smaller than the disk budget, or a memory or CPU
// budget that cannot hold one backend at the descriptor minimum is not ready.
func DeriveBudgets(c ResolvedCapacity, h HostFacts) (journal.CapacityBudgets, error) {
	var zero journal.CapacityBudgets
	if h.MemoryBytes <= 0 || h.CPUs <= 0 || h.VolumeBytes <= 0 {
		return zero, &NotReadyError{NotReadyHostFact}
	}
	if h.VolumeBytes < c.DiskBytes {
		return zero, &NotReadyError{NotReadyVolumeTooSmall}
	}
	if h.CheckpointSeparate {
		if h.CheckpointVolumeBytes <= 0 {
			return zero, &NotReadyError{NotReadyHostFact}
		}
		if h.CheckpointVolumeBytes < CheckpointShare(c.DiskBytes) {
			return zero, &NotReadyError{NotReadyCheckpointVolume}
		}
	}
	memoryMiB := h.MemoryBytes / (1 << 20) * int64(c.MemoryPercent) / 100
	cpuMillis := int64(h.CPUs) * 1000 * int64(c.CPUPercent) / 100
	if memoryMiB < appplatform.MinBackendMemoryMiB {
		return zero, &NotReadyError{NotReadyMemoryBudget}
	}
	if cpuMillis < appplatform.MinBackendCPUMillis {
		return zero, &NotReadyError{NotReadyCPUBudget}
	}
	return journal.CapacityBudgets{MemoryMiB: memoryMiB, CPUMillis: cpuMillis, DiskBytes: c.DiskBytes, ImageStoreBytes: c.ImageStoreBytes, RunningBackends: int64(c.RunningBackends)}, nil
}

// String renders the effective budgets for the ready line and readiness facts.
func FormatBudgets(b journal.CapacityBudgets) string {
	return fmt.Sprintf("memory_mib=%d cpu_millis=%d disk_bytes=%d image_store_bytes=%d running_backends=%d", b.MemoryMiB, b.CPUMillis, b.DiskBytes, b.ImageStoreBytes, b.RunningBackends)
}
