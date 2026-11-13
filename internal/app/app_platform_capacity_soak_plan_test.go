package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The helpers of the declared-capacity soak (doc 171, PR 4b) that need no Docker: how the eight backends are
// sized from the budgets the runner reports, which excess requests are probed and what each must be refused
// with, and how a window of latency samples is summarised. The native scenario that uses them is
// app_platform_capacity_soak_native_linux_test.go; these tests run in the ordinary suite.

func TestSoakSizingFitsEightBackendsAndLeavesRoomForAMinimumNinth(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		memory, cpu               int64
		wantMemory, wantCPU       int
		wantRemMemory, wantRemCPU int64
	}{
		// 25% of a 15.6 GiB MemTotal and 50% of four CPUs: the reference host.
		{"reference host", 3993, 2000, 497, 240, 3993 - 8*497, 2000 - 8*240},
		{"nominal 4 GiB", 4096, 2000, 510, 240, 4096 - 8*510, 2000 - 8*240},
		{"smallest host that fits", 8*16 + 16, 8*30 + 25, 16, 30, 16, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := soakBackendSizing(tc.memory, tc.cpu, soakBackends)
			if err != nil {
				t.Fatal(err)
			}
			if s.MemoryMiB != tc.wantMemory || s.CPUMillis != tc.wantCPU ||
				s.RemainingMemoryMiB != tc.wantRemMemory || s.RemainingCPUMillis != tc.wantRemCPU {
				t.Fatalf("sizing = %+v", s)
			}
			if int64(soakBackends*s.MemoryMiB)+s.RemainingMemoryMiB != tc.memory ||
				int64(soakBackends*s.CPUMillis)+s.RemainingCPUMillis != tc.cpu {
				t.Fatalf("eight backends and the remainder are not the budget: %+v", s)
			}
			if s.RemainingMemoryMiB < appplatform.MinBackendMemoryMiB || s.RemainingCPUMillis < appplatform.MinBackendCPUMillis {
				t.Fatalf("no room for a minimum ninth backend: %+v", s)
			}
		})
	}
}

func TestSoakSizingRefusesHostsItCannotSize(t *testing.T) {
	for _, tc := range []struct {
		name        string
		memory, cpu int64
	}{
		{"memory too small for eight minimum backends", 8*16 + 15, 2000},
		{"cpu too small for eight minimum backends", 4096, 8*25 + 24},
		// Rounded down to the 10-millis granularity a backend is below the descriptor minimum.
		{"cpu that rounds below the minimum", 4096, 8*29 + 25},
		{"no budget", 0, 0},
		// The backends would be clamped to the descriptor maximum, so eight would leave a ninth room.
		{"memory beyond the descriptor range", 8*4096 + 4096, 2000},
		{"cpu beyond the descriptor range", 4096, 8*4000 + 4000},
	} {
		if s, err := soakBackendSizing(tc.memory, tc.cpu, soakBackends); err == nil {
			t.Errorf("%s: sized %+v", tc.name, s)
		}
	}
}

func TestSoakExcessProbesNameTheDimensionEachMustBeRefusedWith(t *testing.T) {
	s, err := soakBackendSizing(3993, 2000, soakBackends)
	if err != nil {
		t.Fatal(err)
	}
	probes := soakBackendProbes(s)
	want := map[string]struct {
		memory, cpu int64
		dimension   string
	}{
		"ninth_backend":      {appplatform.MinBackendMemoryMiB, appplatform.MinBackendCPUMillis, "running_backends"},
		"memory_over_budget": {s.RemainingMemoryMiB + 1, appplatform.MinBackendCPUMillis, "memory"},
		"cpu_over_budget":    {appplatform.MinBackendMemoryMiB, s.RemainingCPUMillis + 1, "cpu"},
	}
	if len(probes) != len(want) {
		t.Fatalf("%d probes", len(probes))
	}
	for _, p := range probes {
		w, ok := want[p.Name]
		if !ok || int64(p.MemoryMiB) != w.memory || int64(p.CPUMillis) != w.cpu || p.Dimension != w.dimension {
			t.Errorf("probe %+v, want %+v", p, w)
		}
		// Every probe is a valid descriptor: only the capacity budget may refuse it.
		if p.MemoryMiB < appplatform.MinBackendMemoryMiB || p.MemoryMiB > appplatform.MaxBackendMemoryMiB ||
			p.CPUMillis < appplatform.MinBackendCPUMillis || p.CPUMillis > appplatform.MaxBackendCPUMillis {
			t.Errorf("probe %s is outside the descriptor ranges: %+v", p.Name, p)
		}
	}
}

func TestSoakTaskFloodPlanReachesTheGlobalQuotaBeforeEveryInstallationIsFull(t *testing.T) {
	plan := soakTaskFloodPlan(50)
	if plan.PerInstallation != appplatform.MaxInstallationActiveTasks || plan.Global != appplatform.MaxGlobalActiveTasks {
		t.Fatalf("plan = %+v", plan)
	}
	// 50 installations at 256 hold 12800 tasks, more than the 10000 global quota: the flood fills installations
	// in order until the global quota refuses, so both the installation and the global limit are reached.
	if plan.FullInstallations != appplatform.MaxGlobalActiveTasks/appplatform.MaxInstallationActiveTasks ||
		plan.GlobalRemainder != appplatform.MaxGlobalActiveTasks%appplatform.MaxInstallationActiveTasks {
		t.Fatalf("plan = %+v", plan)
	}
	if soakTaskFloodPlan(10).GlobalReachable {
		t.Fatal("ten installations cannot reach the global quota")
	}
	if !plan.GlobalReachable {
		t.Fatal("fifty installations must reach the global quota")
	}
}

func TestSoakSummarizeUsesTheNearestRankP95(t *testing.T) {
	var samples []time.Duration
	for i := 1; i <= 100; i++ {
		samples = append(samples, time.Duration(i)*time.Millisecond)
	}
	stats := soakSummarize(samples)
	if stats.Count != 100 || stats.P95Micros != 95_000 || stats.MaxMicros != 100_000 || stats.P50Micros != 50_000 {
		t.Fatalf("stats = %+v", stats)
	}
	if soakSummarize(nil).Count != 0 {
		t.Fatal("empty window")
	}
	// The input is not reordered for the caller.
	unordered := []time.Duration{3 * time.Millisecond, time.Millisecond, 2 * time.Millisecond}
	soakSummarize(unordered)
	if unordered[0] != 3*time.Millisecond {
		t.Fatal("summarize sorted the caller's slice")
	}
}

// soakBackends is the declared running-backend limit the soak fills.
const soakBackends = 8

// soakSizing is what each of the eight backends declares and what is left of the runner's budgets afterwards.
type soakSizing struct {
	MemoryMiB, CPUMillis                   int
	RemainingMemoryMiB, RemainingCPUMillis int64
}

// soakCPUGranularity is the step of a CPU limit the kernel enforces: a quota is whole milliseconds of each 100 ms
// period, so 246 millis are enforced as 250 (the smoke measured eight such backends using 2000 millis, not 1968).
// A backend's CPU is rounded down to it, so the enforced sum stays inside the budget.
const soakCPUGranularity = 10

// soakBackendSizing divides the budgets the runner reports in its readiness facts, not the nominal host, among
// the backends: floor((budget - one minimum backend) / backends), the CPU further rounded down to soakCPUGranularity. One minimum backend is left unreserved on
// purpose. The runner names the first overflowing dimension in the order memory, CPU, disk, image store,
// running backends, so a ninth backend that does not fit memory is refused as "memory"; only a ninth that fits
// memory and CPU proves the count limit itself. The result must lie inside the descriptor ranges unclamped: a
// host so large that the backends would be clamped to the maximum cannot show the budget being the limit.
func soakBackendSizing(memoryMiB, cpuMillis int64, backends int) (soakSizing, error) {
	if backends < 1 || memoryMiB < 1 || cpuMillis < 1 {
		return soakSizing{}, errSoakSizing
	}
	memory := (memoryMiB - appplatform.MinBackendMemoryMiB) / int64(backends)
	cpu := (cpuMillis - appplatform.MinBackendCPUMillis) / int64(backends) / soakCPUGranularity * soakCPUGranularity
	if memory < appplatform.MinBackendMemoryMiB || memory > appplatform.MaxBackendMemoryMiB ||
		cpu < appplatform.MinBackendCPUMillis || cpu > appplatform.MaxBackendCPUMillis {
		return soakSizing{}, errSoakSizing
	}
	return soakSizing{
		MemoryMiB: int(memory), CPUMillis: int(cpu),
		RemainingMemoryMiB: memoryMiB - int64(backends)*memory,
		RemainingCPUMillis: cpuMillis - int64(backends)*cpu,
	}, nil
}

var errSoakSizing = errors.New("the runner budgets cannot be divided among the soak backends within the descriptor ranges")

// soakProbe is one excess backend request and the dimension the runner must refuse it with.
type soakProbe struct {
	Name                 string
	MemoryMiB, CPUMillis int
	Dimension            string
}

// soakBackendProbes are the three backend requests that must be refused once the eight backends are running:
// a minimum ninth backend (the count limit), one memory unit over what remains, one CPU unit over what remains.
func soakBackendProbes(s soakSizing) []soakProbe {
	return []soakProbe{
		{"ninth_backend", appplatform.MinBackendMemoryMiB, appplatform.MinBackendCPUMillis, "running_backends"},
		{"memory_over_budget", int(s.RemainingMemoryMiB) + 1, appplatform.MinBackendCPUMillis, "memory"},
		{"cpu_over_budget", appplatform.MinBackendMemoryMiB, int(s.RemainingCPUMillis) + 1, "cpu"},
	}
}

// soakTaskPlan is how the task flood reaches the quotas.
type soakTaskPlan struct {
	PerInstallation, Global int
	// FullInstallations installations are filled to their own quota before the global quota refuses, and the
	// next one takes GlobalRemainder tasks. GlobalReachable is false when the installations cannot hold the
	// global quota at all.
	FullInstallations, GlobalRemainder int
	GlobalReachable                    bool
}

func soakTaskFloodPlan(installations int) soakTaskPlan {
	plan := soakTaskPlan{PerInstallation: appplatform.MaxInstallationActiveTasks, Global: appplatform.MaxGlobalActiveTasks}
	plan.FullInstallations = plan.Global / plan.PerInstallation
	plan.GlobalRemainder = plan.Global % plan.PerInstallation
	plan.GlobalReachable = installations*plan.PerInstallation > plan.Global
	return plan
}

// soakStats summarises one window of latency samples, in microseconds.
type soakStats struct {
	Count     int   `json:"count"`
	P50Micros int64 `json:"p50_us"`
	P95Micros int64 `json:"p95_us"`
	MaxMicros int64 `json:"max_us"`
}

func soakSummarize(samples []time.Duration) soakStats {
	if len(samples) == 0 {
		return soakStats{}
	}
	sorted := append([]time.Duration(nil), samples...)
	stats := soakStats{Count: len(sorted)}
	stats.P50Micros = latencyPercentile(sorted, 50).Microseconds()
	stats.P95Micros = latencyPercentile(sorted, 95).Microseconds()
	stats.MaxMicros = latencyPercentile(sorted, 100).Microseconds()
	return stats
}

// The offline upload route bounds one package at MaxManagedOfflineEnvelopeBytes (384 MiB), so no single upload
// can exceed the 2 GiB staging cap; the cap guards the sum of the packages held and in flight. These tests
// prove the refusal at the production cap without writing a byte, and the soak repeats the sum probe against
// the real store in the Portal child.
func TestStagingCapIsTwoGiBAndRefusesTheOneThatDoesNotFit(t *testing.T) {
	s, _ := platformTrustTestServer(t, false)
	s.platformImageDir = t.TempDir()
	store, err := s.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	if platformStagingCapBytes != 2<<30 || store.stagingCap != 2<<30 {
		t.Fatalf("staging cap = %d", store.stagingCap)
	}
	release, err := store.reserveStaging(store.stagingCap, "")
	if err != nil {
		t.Fatalf("a package of exactly the cap was refused: %v", err)
	}
	if _, err := store.reserveStaging(1, ""); !errors.Is(err, errPlatformImageStoreFull) {
		t.Fatalf("a byte beyond the cap was admitted: %v", err)
	}
	release()
	if _, err := store.reserveStaging(store.stagingCap+1, ""); !errors.Is(err, errPlatformImageStoreFull) {
		t.Fatalf("a package one byte over the cap was admitted: %v", err)
	}
	w := httptest.NewRecorder()
	platformManagementError(w, fmt.Errorf("stage: %w", errPlatformImageStoreFull))
	if w.Code != http.StatusInsufficientStorage || !strings.Contains(w.Body.String(), `"app_platform_storage_full"`) {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestStagingCapCountsPackagesInFlightTogether(t *testing.T) {
	s, _ := platformTrustTestServer(t, false)
	s.platformImageDir = t.TempDir()
	store, err := s.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	var releases []func()
	admitted := 0
	for {
		release, err := store.reserveStaging(appplatform.MaxManagedOfflineEnvelopeBytes, "")
		if err != nil {
			if !errors.Is(err, errPlatformImageStoreFull) {
				t.Fatal(err)
			}
			break
		}
		releases = append(releases, release)
		admitted++
	}
	// 2 GiB holds five whole 384 MiB uploads (1920 MiB) and not a sixth (2304 MiB).
	if admitted != 5 {
		t.Fatalf("%d concurrent uploads admitted", admitted)
	}
	for _, release := range releases {
		release()
	}
	if _, err := store.reserveStaging(appplatform.MaxManagedOfflineEnvelopeBytes, ""); err != nil {
		t.Fatalf("released reservations were not returned: %v", err)
	}
}

// Each task quota refuses at exactly its boundary and independently of the other two.
func TestTaskQuotasRefuseAtTheirOwnBoundary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quota appplatform.TaskQuota
		want  bool
	}{
		{"all below", appplatform.TaskQuota{GlobalActive: 9999, ApplicationActive: 999, InstallationActive: 255}, true},
		{"installation at 256", appplatform.TaskQuota{GlobalActive: 100, ApplicationActive: 100, InstallationActive: 256}, false},
		{"application at 1000", appplatform.TaskQuota{GlobalActive: 100, ApplicationActive: 1000, InstallationActive: 10}, false},
		{"global at 10000", appplatform.TaskQuota{GlobalActive: 10000, ApplicationActive: 10, InstallationActive: 10}, false},
	} {
		if got := appplatform.AdmitTask(tc.quota) == nil; got != tc.want {
			t.Errorf("%s: admitted=%t", tc.name, got)
		}
	}
}
