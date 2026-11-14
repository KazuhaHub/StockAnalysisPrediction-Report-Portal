package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestParseCgroupLimit(t *testing.T) {
	for _, tc := range []struct {
		in    string
		limit int64
		ok    bool
	}{
		{"536870912\n", 536870912, true},
		{"max\n", 0, false},
		{"", 0, false},
		{"-1", 0, false},
		{"0", 0, false},
		{"12x", 0, false},
	} {
		limit, ok := parseCgroupLimit(tc.in)
		if limit != tc.limit || ok != tc.ok {
			t.Errorf("parseCgroupLimit(%q) = %d, %t", tc.in, limit, ok)
		}
	}
}

func TestParseCPUMax(t *testing.T) {
	for _, tc := range []struct {
		in      string
		workers int
	}{
		{"25000 100000\n", 1},
		{"250000 100000\n", 3},
		{"400000 100000", 4},
		{"max 100000", 1},
		{"", 1},
		{"junk", 1},
	} {
		if got := cpuWorkers(tc.in); got != tc.workers {
			t.Errorf("cpuWorkers(%q) = %d, want %d", tc.in, got, tc.workers)
		}
	}
}

func TestMemoryHoldLeavesRuntimeHeadroom(t *testing.T) {
	const mib = 1 << 20
	for _, tc := range []struct {
		limit int64
		known bool
		want  int64
	}{
		{512 * mib, true, 435 * mib}, // 85%, rounded down to whole MiB
		{497 * mib, true, 422 * mib},
		{16 * mib, true, 13 * mib},
		{0, false, 32 * mib},
	} {
		if got := memoryHold(tc.limit, tc.known); got != tc.want {
			t.Errorf("memoryHold(%d,%t) = %d, want %d", tc.limit, tc.known, got, tc.want)
		}
	}
}

func TestMemoryIsHeldAtEightyFivePercentAndTheGoRuntimeIsToldTheLimit(t *testing.T) {
	const mib = 1 << 20
	if memoryHeldPercent != 85 {
		t.Fatalf("the held share is %d percent", memoryHeldPercent)
	}
	// The held memory leaves room for the runtime, and the soft limit sits between the held memory and the hard one, so
	// the collector runs before the kernel kills the container: garbage never has to reach twice the live heap.
	for _, limit := range []int64{16 * mib, 497 * mib, 512 * mib} {
		held, soft := memoryHold(limit, true), softMemoryLimit(limit, true)
		if soft <= held || soft >= limit {
			t.Errorf("limit %d: held %d, soft limit %d", limit, held, soft)
		}
	}
	if got := softMemoryLimit(0, false); got != -1 {
		t.Errorf("an unknown limit sets no soft limit, got %d", got)
	}
}

func TestThreadsToSpawnStayBelowThePIDLimit(t *testing.T) {
	for _, tc := range []struct {
		max, current int64
		known        bool
		want         int
	}{
		{64, 10, true, 64 - 10 - threadMargin},
		{64, 0, true, 64 - threadMargin},
		{64, 64, true, 0},
		{64, 60, true, 0},
		{12, 4, true, 0},
		{1024, 5, true, 1024 - 5 - threadMargin},
		{0, 0, false, 0},
	} {
		if got := threadsToSpawn(tc.max, tc.current, tc.known); got != tc.want {
			t.Errorf("threadsToSpawn(%d,%d,%t) = %d, want %d", tc.max, tc.current, tc.known, got, tc.want)
		}
	}
}

func TestThreadMarginIsSmallSoThePIDsReachTheirLimit(t *testing.T) {
	// 64 PIDs is the soak's declared limit: a margin of eight leaves 56, 87.5 percent, above the 85 percent the verdict requires.
	if threadMargin < 1 || threadMargin > 8 {
		t.Fatalf("margin %d", threadMargin)
	}
	if reached := float64(64-threadMargin) / 64; reached < 0.85 {
		t.Fatalf("a 64 PID backend reaches %.3f of its limit", reached)
	}
}

func TestStatusReportsWhatTheLoadsDid(t *testing.T) {
	var served atomic.Int64
	served.Store(7)
	state := newSoakState(&served)
	state.heldBytes.Store(100)
	state.threads.Store(5)
	state.burners.Store(2)
	state.logBytes.Store(300)
	state.storageBytes.Store(400)
	state.storageFull.Store(1)
	state.dataPresent.Store(true)
	var got map[string]any
	if err := json.Unmarshal(state.statusJSON(), &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"held_bytes": 100, "threads": 5, "burners": 2, "log_bytes": 300, "storage_bytes": 400, "storage_full": 1, "requests": 7} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if got["data_present"] != true {
		t.Error("data_present lost")
	}
}
