//go:build linux

package service

import (
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/cpuset"
	"golang.org/x/sys/unix"
)

// volumeBytes is the size of the filesystem holding path, or zero when it cannot be read. Zero is never a size:
// DeriveBudgets reports it as an unreadable host fact and the runner is not ready. The size is blocks times the
// fragment size (Frsize), the unit the block counts are in; Bsize is only the preferred transfer size.
func volumeBytes(path string) int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil || st.Frsize <= 0 {
		return 0
	}
	size := int64(st.Blocks) * int64(st.Frsize)
	if size < 0 {
		return 0
	}
	return size
}

// sameFilesystem reports whether two paths are on one filesystem. Unreadable paths are reported as separate, so
// the stricter check applies.
func sameFilesystem(a, b string) bool {
	var x, y unix.Stat_t
	if unix.Stat(a, &x) != nil || unix.Stat(b, &y) != nil {
		return false
	}
	return x.Dev == y.Dev
}

// onlineCPUsPath is the kernel's list of CPUs that are online.
const onlineCPUsPath = "/sys/devices/system/cpu/online"

// onlineCPUs is the host's online CPU list. It is read from sysfs, not derived from the CPU count, because the
// CPUs a reservation names must be real ones: with CPUs offline the numbers are not 0 to NCPU-1.
func onlineCPUs() ([]int, error) { return cpuset.ReadFile(onlineCPUsPath) }

// allowedCPUs is the set of CPUs the runner itself may be scheduled on (sched_getaffinity).
func allowedCPUs() ([]int, error) {
	var set unix.CPUSet
	if err := unix.SchedGetaffinity(0, &set); err != nil {
		return nil, err
	}
	var cpus []int
	for cpu := 0; cpu < len(set)*64; cpu++ {
		if set.IsSet(cpu) {
			cpus = append(cpus, cpu)
		}
	}
	if len(cpus) == 0 {
		return nil, errors.New("empty CPU affinity")
	}
	return cpus, nil
}
