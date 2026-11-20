package service

import (
	"errors"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/cpuset"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
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
	// ReservedCPUs is how many of the lowest-numbered online CPUs no app container may ever run on, so the Portal,
	// PostgreSQL and the runner keep them. Absent, it is 1 on a host with at least 4 online CPUs and 0 on a
	// smaller one; 0 turns the reservation off. The valid range, 0 to NCPU-1, depends on the host and is checked
	// when the budgets are derived.
	ReservedCPUs *int `json:"reserved_cpus,omitempty"`
	// AppCPUs pins the app set explicitly, in the kernel's canonical list notation ("1-3"), instead of deriving
	// it as every online CPU above the reserved ones. It is how an operator keeps every installed container's
	// plan unchanged across a resize of the host. It must name online CPUs, none of them reserved, and it needs a
	// reservation: with reserved_cpus 0 it is refused when the budgets are derived.
	AppCPUs string `json:"app_cpus,omitempty"`
}

const (
	gibibyte = int64(1) << 30

	defaultMemoryPercent   = 25
	defaultCPUPercent      = 50
	defaultDiskBytes       = 8 * gibibyte
	defaultRunningBackends = 8
	defaultImageStoreBytes = 10 * gibibyte
	maxImageStoreBytes     = int64(1) << 40

	// reservedCPUsMinHost is the smallest host that reserves a CPU by default: below it the reservation would
	// take a quarter or more of the machine from the apps.
	reservedCPUsMinHost = 4
	// maxConfiguredReservedCPUs bounds the value before the host is known; the host range is checked later.
	maxConfiguredReservedCPUs = 1023
)

// ResolvedCapacity is a CapacityConfig with every default applied.
type ResolvedCapacity struct {
	MemoryPercent   int
	CPUPercent      int
	DiskBytes       int64
	RunningBackends int
	ImageStoreBytes int64
	ReservedCPUs    int
	AppCPUs         string
}

// Resolve applies the defaults for a host whose CPU count is not known, which reserves no CPU. A nil receiver
// resolves to the defaults. The runner uses ResolveFor.
func (c *CapacityConfig) Resolve() ResolvedCapacity { return c.ResolveFor(0) }

// ResolveFor applies the defaults for a host with the given number of CPUs: one reserved CPU from 4 CPUs up.
func (c *CapacityConfig) ResolveFor(cpus int) ResolvedCapacity {
	r := ResolvedCapacity{MemoryPercent: defaultMemoryPercent, CPUPercent: defaultCPUPercent, DiskBytes: defaultDiskBytes, RunningBackends: defaultRunningBackends, ImageStoreBytes: defaultImageStoreBytes}
	if cpus >= reservedCPUsMinHost {
		r.ReservedCPUs = 1
	}
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
	if c.ReservedCPUs != nil {
		r.ReservedCPUs = *c.ReservedCPUs
	}
	r.AppCPUs = c.AppCPUs
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
		in(c.RunningBackends, 1, 64) && big(c.ImageStoreBytes, gibibyte, maxImageStoreBytes) &&
		in(c.ReservedCPUs, 0, maxConfiguredReservedCPUs) && (c.AppCPUs == "" || cpuset.Valid(c.AppCPUs))
}

// HostFacts are the readings the budgets derive from. A zero reading means
// the fact could not be read.
type HostFacts struct {
	MemoryBytes int64
	CPUs        int
	VolumeBytes int64
	// OnlineCPUs is the host's online CPU list (/sys/devices/system/cpu/online) and AffinityCPUs the runner's own
	// allowed set (sched_getaffinity). Both are read only to place a reservation. AffinityCPUs is nil when it
	// could not be read, and then the online list stands alone.
	OnlineCPUs, AffinityCPUs []int
	// CPUSetSupported is the daemon's claim that its containers can be pinned to CPUs: on cgroup v2, that the
	// cpuset controller is enabled for them.
	CPUSetSupported bool
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
	// NotReadyReservedCPUs is a reserved_cpus value outside 0 to NCPU-1.
	NotReadyReservedCPUs = "reserved_cpus_outside_host_range"
	// NotReadyOnlineCPUs is an online CPU list, or the runner's allowed set, that disagrees with the CPU count the
	// daemon reports, so the CPU set to reserve cannot be named with confidence.
	NotReadyOnlineCPUs = "online_cpus_disagree_with_reported"
	// NotReadyCPUSetUnsupported is a daemon that cannot pin containers to CPUs, which a reservation requires.
	NotReadyCPUSetUnsupported = "cpuset_controller_unavailable"
	// NotReadyAppCPUs is an explicit app_cpus that is not online, overlaps the reserved CPUs, or has no
	// reservation to belong to.
	NotReadyAppCPUs = "app_cpus_invalid_for_host"
	// NotReadyProfileTooLarge is a managed runtime profile that would exceed the Portal's size cap.
	NotReadyProfileTooLarge = "managed_profile_exceeds_size_cap"
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
	reserved, app, err := splitCPUs(c.ReservedCPUs, c.AppCPUs, h)
	if err != nil {
		return zero, err
	}
	memoryMiB := h.MemoryBytes / (1 << 20) * int64(c.MemoryPercent) / 100
	cpuMillis := int64(h.CPUs) * 1000 * int64(c.CPUPercent) / 100
	if reserved != nil {
		// The apps cannot use more CPU than the CPUs they may run on.
		cpuMillis = min(cpuMillis, int64(len(app))*1000)
	}
	if memoryMiB < appplatform.MinBackendMemoryMiB {
		return zero, &NotReadyError{NotReadyMemoryBudget}
	}
	if cpuMillis < appplatform.MinBackendCPUMillis {
		return zero, &NotReadyError{NotReadyCPUBudget}
	}
	budgets := journal.CapacityBudgets{MemoryMiB: memoryMiB, CPUMillis: cpuMillis, DiskBytes: c.DiskBytes, ImageStoreBytes: c.ImageStoreBytes, RunningBackends: int64(c.RunningBackends),
		ReservedCPUs: cpuset.Format(reserved), AppCPUs: cpuset.Format(app)}
	if h.CheckpointSeparate {
		// A namespace's retained checkpoints are counted against their own volume (doc 185 section 3.3).
		budgets.CheckpointBytes = h.CheckpointVolumeBytes
	}
	return budgets, nil
}

// splitCPUs places the reservation: the lowest-numbered online CPUs are reserved and the rest are the app set.
// With nothing reserved it reads no CPU fact and returns no sets, so the apps run wherever the daemon puts them,
// exactly as before the reservation existed.
func splitCPUs(reserve int, explicitApp string, h HostFacts) (reserved, app []int, err error) {
	if reserve < 0 || reserve >= h.CPUs {
		return nil, nil, &NotReadyError{NotReadyReservedCPUs}
	}
	if reserve == 0 {
		if explicitApp != "" {
			return nil, nil, &NotReadyError{NotReadyAppCPUs}
		}
		return nil, nil, nil
	}
	if !h.CPUSetSupported {
		return nil, nil, &NotReadyError{NotReadyCPUSetUnsupported}
	}
	if len(h.OnlineCPUs) == 0 {
		return nil, nil, &NotReadyError{NotReadyHostFact}
	}
	if !ascending(h.OnlineCPUs) || len(h.OnlineCPUs) != h.CPUs || (h.AffinityCPUs != nil && !cpuset.Equal(h.AffinityCPUs, h.OnlineCPUs)) {
		return nil, nil, &NotReadyError{NotReadyOnlineCPUs}
	}
	reserved, app = h.OnlineCPUs[:reserve:reserve], h.OnlineCPUs[reserve:]
	if explicitApp != "" {
		pinned, perr := cpuset.Parse(explicitApp)
		if perr != nil || !subset(pinned, app) {
			return nil, nil, &NotReadyError{NotReadyAppCPUs}
		}
		app = pinned
	}
	return reserved, app, nil
}

// subset reports whether every CPU of small is in big. Both are ascending.
func subset(small, big []int) bool {
	i := 0
	for _, cpu := range small {
		for i < len(big) && big[i] < cpu {
			i++
		}
		if i == len(big) || big[i] != cpu {
			return false
		}
	}
	return true
}

func ascending(cpus []int) bool {
	for i, cpu := range cpus {
		if cpu < 0 || (i > 0 && cpu <= cpus[i-1]) {
			return false
		}
	}
	return true
}

// FormatBudgets renders the effective budgets and the CPU sets for the ready line and readiness facts.
func FormatBudgets(b journal.CapacityBudgets) string {
	reserved, app := b.ReservedCPUs, b.AppCPUs
	if app == "" {
		reserved, app = "none", "all"
	}
	return fmt.Sprintf("memory_mib=%d cpu_millis=%d disk_bytes=%d image_store_bytes=%d running_backends=%d reserved_cpus=%s app_cpus=%s", b.MemoryMiB, b.CPUMillis, b.DiskBytes, b.ImageStoreBytes, b.RunningBackends, reserved, app)
}

// planCapacity turns the declared capacity and what the runner read of the host into the budgets it admits
// against and the options that pin the driver to the app CPUs. The facts carry what only the caller can read
// (the volumes and the CPU lists); the daemon's report supplies the rest. The runner calls nothing else to derive
// capacity, so a default resolved without the host, or an app set that never reaches the driver, fails its test.
func planCapacity(cfg Config, report driver.Report, facts HostFacts) (journal.CapacityBudgets, []driver.RuntimeOption, error) {
	facts.MemoryBytes, facts.CPUs, facts.CPUSetSupported = report.ReportedMemoryBytes, report.ReportedCPUs, report.CPUSetSupported
	if facts.OnlineCPUs == nil {
		// Sysfs unreadable: the runner's own allowed set stands in for it.
		facts.OnlineCPUs = facts.AffinityCPUs
	}
	budgets, err := DeriveBudgets(cfg.Capacity.ResolveFor(report.ReportedCPUs), facts)
	if err != nil {
		return journal.CapacityBudgets{}, nil, err
	}
	if !transport.ManagedProfileFits(budgets) {
		return journal.CapacityBudgets{}, nil, &NotReadyError{NotReadyProfileTooLarge}
	}
	var options []driver.RuntimeOption
	if budgets.AppCPUs != "" {
		options = append(options, driver.WithAppCPUSet(budgets.AppCPUs))
	}
	return budgets, options, nil
}
