package app

import (
	"math"
	"runtime/metrics"
	"testing"
	"time"
)

// Helpers of the soak's diagnosis of the Portal's own latency under load (behind APP_CAPACITY_SOAK_DIAGNOSE): how a
// phase of a request is summarised, how the share of time the single SQLite connection is in use is read from
// samples, and how a runtime histogram delta becomes a percentile. They need no Docker.

func TestSoakPhaseSummarySplitsWallIntoCPUAndOffCPU(t *testing.T) {
	wall := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond}
	cpu := []time.Duration{9 * time.Millisecond, 5 * time.Millisecond, 4 * time.Millisecond}
	s := soakSummarizePhase("SearchNewLatest", wall, cpu)
	if s.Name != "SearchNewLatest" || s.Wall.Count != 3 || s.Wall.MaxMicros != 30_000 || s.CPU.MaxMicros != 9_000 {
		t.Fatalf("summary = %+v", s)
	}
	// Off-CPU time is what the request spent waiting: 1, 15 and 26 ms here, never negative.
	if s.OffCPU.MaxMicros != 26_000 || s.OffCPU.P50Micros != 15_000 {
		t.Fatalf("off-cpu = %+v", s.OffCPU)
	}
	over := soakSummarizePhase("p", []time.Duration{time.Millisecond}, []time.Duration{3 * time.Millisecond})
	if over.OffCPU.MaxMicros != 0 {
		t.Fatalf("a CPU reading above the wall time produced negative waiting: %+v", over.OffCPU)
	}
	if got := soakSummarizePhase("empty", nil, nil); got.Wall.Count != 0 {
		t.Fatal("empty phase")
	}
}

func TestSoakConnectionUtilizationIsTheShareOfSamplesWithTheConnectionInUse(t *testing.T) {
	if got := soakUtilization([]int{0, 1, 1, 0}, 1); got != 0.5 {
		t.Fatalf("utilization = %v", got)
	}
	if got := soakUtilization(nil, 1); got != 0 {
		t.Fatalf("no samples = %v", got)
	}
	// With a pool of two, "saturated" means both connections in use.
	if got := soakUtilization([]int{2, 2, 1, 0}, 2); got != 0.5 {
		t.Fatalf("saturation of a pool of two = %v", got)
	}
}

func TestSoakMeanOccupancyIsTheAverageShareOfTheConnectionsInUse(t *testing.T) {
	// A pool of ten with two connections in use for most of the time is 20% occupied, and never saturated.
	if got := soakMeanOccupancy([]int{2, 2, 2, 2}, 10); got != 0.2 {
		t.Fatalf("occupancy = %v", got)
	}
	if got := soakMeanOccupancy([]int{0, 1, 1, 0}, 1); got != 0.5 {
		t.Fatalf("single connection = %v", got)
	}
	if soakMeanOccupancy(nil, 10) != 0 || soakMeanOccupancy([]int{1}, 0) != 0 {
		t.Fatal("no samples or no pool")
	}
	if soakUtilization([]int{2, 2, 2, 2}, 10) != 0 {
		t.Fatal("a pool of ten with two in use is not saturated")
	}
}

func TestSoakHistogramPercentileReadsOnlyWhatHappenedBetweenTwoReadings(t *testing.T) {
	buckets := []float64{0, 0.001, 0.01, 0.1, math.Inf(1)}
	before := &metrics.Float64Histogram{Buckets: buckets, Counts: []uint64{100, 0, 0, 0}}
	after := &metrics.Float64Histogram{Buckets: buckets, Counts: []uint64{100, 90, 9, 1}}
	// 100 new events: 90 in (1ms,10ms], 9 in (10ms,100ms], 1 above.
	if got := soakHistogramPercentile(before, after, 0.5); got != 0.01 {
		t.Fatalf("p50 = %v", got)
	}
	if got := soakHistogramPercentile(before, after, 0.95); got != 0.1 {
		t.Fatalf("p95 = %v", got)
	}
	if got := soakHistogramPercentile(before, after, 1); got != 0.1 {
		t.Fatalf("an open top bucket reports its lower bound, got %v", got)
	}
	if got := soakHistogramPercentile(before, before, 0.5); got != 0 {
		t.Fatalf("no new events = %v", got)
	}
	if got := soakHistogramPercentile(before, nil, 0.5); got != 0 {
		t.Fatalf("a missing reading = %v", got)
	}
}

// soakPhaseSummary is how long one phase of a request took, and how much of that was the thread running (CPU) and
// how much was waiting (off CPU: the connection pool, a lock, the scheduler).
type soakPhaseSummary struct {
	Name   string    `json:"name"`
	Wall   soakStats `json:"wall"`
	CPU    soakStats `json:"cpu"`
	OffCPU soakStats `json:"off_cpu"`
}

func soakSummarizePhase(name string, wall, cpu []time.Duration) soakPhaseSummary {
	off := make([]time.Duration, 0, len(wall))
	for i := range wall {
		wait := wall[i]
		if i < len(cpu) {
			wait -= cpu[i]
		}
		if wait < 0 {
			wait = 0
		}
		off = append(off, wait)
	}
	return soakPhaseSummary{Name: name, Wall: soakSummarize(wall), CPU: soakSummarize(cpu), OffCPU: soakSummarize(off)}
}

// soakUtilization is the share of samples in which every connection of the pool was in use. With one connection
// (SQLite) it is the share of time the database was busy for one caller and closed to every other.
func soakUtilization(inUse []int, maxOpen int) float64 {
	if len(inUse) == 0 || maxOpen < 1 {
		return 0
	}
	busy := 0
	for _, n := range inUse {
		if n >= maxOpen {
			busy++
		}
	}
	return float64(busy) / float64(len(inUse))
}

// soakMeanOccupancy is the average share of the pool's connections in use over the samples.
func soakMeanOccupancy(inUse []int, maxOpen int) float64 {
	if len(inUse) == 0 || maxOpen < 1 {
		return 0
	}
	total := 0
	for _, n := range inUse {
		total += n
	}
	return float64(total) / float64(len(inUse)) / float64(maxOpen)
}

// soakHistogramPercentile is the q-quantile (upper bucket bound, seconds) of the events a runtime histogram gained
// between two readings. A quantile that lands in an unbounded top bucket reports that bucket's lower bound.
func soakHistogramPercentile(before, after *metrics.Float64Histogram, q float64) float64 {
	if after == nil || len(after.Counts) == 0 || len(after.Buckets) != len(after.Counts)+1 {
		return 0
	}
	delta := make([]uint64, len(after.Counts))
	var total uint64
	for i, c := range after.Counts {
		delta[i] = c
		if before != nil && i < len(before.Counts) && before.Counts[i] <= c {
			delta[i] = c - before.Counts[i]
		}
		total += delta[i]
	}
	if total == 0 {
		return 0
	}
	need := uint64(math.Ceil(q * float64(total)))
	var seen uint64
	for i, c := range delta {
		seen += c
		if seen >= need {
			if upper := after.Buckets[i+1]; !math.IsInf(upper, 1) {
				return upper
			}
			return after.Buckets[i]
		}
	}
	return after.Buckets[len(after.Buckets)-2]
}
