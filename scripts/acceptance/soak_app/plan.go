package main

import (
	"strconv"
	"strings"
)

// threadMargin is the number of tasks the fork load leaves free below the container's PID limit, for the
// runtime's own threads (garbage collection, the signal thread, the HTTP server).
const threadMargin = 12

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

// memoryHold is how much memory the backend keeps resident: 70% of its limit, in whole MiB, so the Go runtime
// and the network buffers still fit under the limit and the kernel never has to kill the process. Without a
// known limit it holds a small fixed amount.
func memoryHold(limit int64, known bool) int64 {
	const mib = 1 << 20
	if !known {
		return 32 * mib
	}
	return limit * 7 / 10 / mib * mib
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
