package main

import (
	"strconv"
	"strings"
)

// threadMargin is the number of tasks the fork load leaves free below the container's PID limit, for the
// runtime's own threads: garbage collection, the signal thread, the HTTP server, and one more for every goroutine
// that is blocked in a system call at the same time (the log and storage writers). A thread the runtime cannot
// start is fatal, so the margin is not zero, but it is small: the first smoke left 24 of 64 free and reached 41.
const threadMargin = 8

// parseCgroupLimit reads a cgroup v2 limit file. "max" and anything that is not a positive decimal mean the
// limit is not known.
func parseCgroupLimit(raw string) (int64, bool) {
	value := strings.TrimSpace(raw)
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != value {
		return 0, false
	}
	return n, true
}

// cpuWorkers is the number of busy loops that saturate a cgroup v2 cpu.max quota: the quota divided by the
// period, rounded up, and at least one. An unlimited or unreadable quota burns one core.
func cpuWorkers(raw string) int {
	fields := strings.Fields(raw)
	if len(fields) != 2 {
		return 1
	}
	quota, errQuota := strconv.ParseInt(fields[0], 10, 64)
	period, errPeriod := strconv.ParseInt(fields[1], 10, 64)
	if errQuota != nil || errPeriod != nil || quota <= 0 || period <= 0 {
		return 1
	}
	workers := int((quota + period - 1) / period)
	if workers < 1 {
		return 1
	}
	return workers
}

// memoryHeldPercent is the share of its memory limit the backend keeps resident. The first version held 60%; a
// backend at its declared limit holds most of it, and the headroom is what the Go runtime, the network buffers and the
// page cache of the storage flood (synced every few MiB) need.
const memoryHeldPercent = 85

// memoryHold is how much memory the backend keeps resident: memoryHeldPercent of its limit, in whole MiB. Without a
// known limit it holds a small fixed amount.
func memoryHold(limit int64, known bool) int64 {
	const mib = 1 << 20
	if !known {
		return 32 * mib
	}
	return limit * memoryHeldPercent / 100 / mib * mib
}

// softMemoryLimit is the Go runtime's own memory limit: 95% of the container's, so the collector runs before the kernel
// has to kill the process. Without it the collector waits for the heap to reach twice the held memory, which is far past
// the cgroup limit at 85%. -1 means no limit is set.
func softMemoryLimit(limit int64, known bool) int64 {
	if !known {
		return -1
	}
	return limit * 95 / 100
}

// threadsToSpawn is how many extra threads the fork load may start without reaching the PID limit.
func threadsToSpawn(limit, current int64, known bool) int {
	if !known {
		return 0
	}
	room := limit - current - threadMargin
	if room < 0 {
		return 0
	}
	return int(room)
}

// maxLoadedLimit is the largest memory limit of a container that runs the loads. The image store probe's images start this
// same executable with a memory declaration over the runner's whole budget (so that their start is refused after their load):
// they must serve their data writer and hold nothing.
const maxLoadedLimit = 1 << 30

// loadsEnabled reports whether the container runs the soak loads. An unknown limit is a soak backend of the development
// profile.
func loadsEnabled(limit int64, known bool) bool { return !known || limit <= maxLoadedLimit }
