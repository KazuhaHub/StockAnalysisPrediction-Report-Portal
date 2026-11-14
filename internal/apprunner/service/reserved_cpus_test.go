package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cpuRange(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

func hostWith(cpus int) HostFacts {
	return HostFacts{MemoryBytes: 16 * gib, CPUs: cpus, VolumeBytes: 16 * gib, OnlineCPUs: cpuRange(cpus), AffinityCPUs: cpuRange(cpus), CPUSetSupported: true}
}

func TestReservedCPUsDefaultByHostSize(t *testing.T) {
	for cpus, want := range map[int]int{1: 0, 2: 0, 3: 0, 4: 1, 8: 1, 64: 1} {
		if got := (*CapacityConfig)(nil).ResolveFor(cpus).ReservedCPUs; got != want {
			t.Fatalf("default reserved CPUs on %d CPUs = %d, want %d", cpus, got, want)
		}
	}
	// An explicit value always wins, 0 included, and an empty object is the default.
	if got := (&CapacityConfig{ReservedCPUs: ptr(0)}).ResolveFor(8).ReservedCPUs; got != 0 {
		t.Fatalf("explicit 0 on 8 CPUs = %d", got)
	}
	if got := (&CapacityConfig{ReservedCPUs: ptr(3)}).ResolveFor(8).ReservedCPUs; got != 3 {
		t.Fatalf("explicit 3 on 8 CPUs = %d", got)
	}
	if got := (&CapacityConfig{}).ResolveFor(4).ReservedCPUs; got != 1 {
		t.Fatalf("empty capacity on 4 CPUs = %d", got)
	}
	// The host-independent form cannot know the host and reserves nothing.
	if got := (*CapacityConfig)(nil).Resolve().ReservedCPUs; got != 0 {
		t.Fatalf("Resolve reserved = %d", got)
	}
}

func TestReservedCPUsIsAStrictConfigField(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	cfg.Capacity = &CapacityConfig{ReservedCPUs: ptr(2)}
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Capacity.ReservedCPUs == nil || *loaded.Capacity.ReservedCPUs != 2 {
		t.Fatalf("reserved_cpus did not load: %v", err)
	}
	for name, value := range map[string]*int{"negative": ptr(-1), "absurd": ptr(1 << 20)} {
		cfg.Capacity = &CapacityConfig{ReservedCPUs: value}
		writePrivate(t, path, cfg)
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("%s reserved_cpus accepted", name)
		}
	}
	// The key is the documented one, and an unknown sibling is still refused.
	cfg.Capacity = &CapacityConfig{ReservedCPUs: ptr(1)}
	writePrivate(t, path, cfg)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reserved_cpus":1`) {
		t.Fatalf("wire key is not reserved_cpus: %s", raw)
	}
	withUnknown := append(raw[:len(raw)-1:len(raw)-1], []byte(`,"reserved_cpu":1}`)...)
	if err := os.WriteFile(path, withUnknown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown top-level field accepted")
	}
}

func TestDeriveBudgetsReservesTheLowestCPUs(t *testing.T) {
	for name, tc := range map[string]struct {
		cpus, percent, reserved int
		wantReserved, wantApp   string
		wantMillis              int64
	}{
		"2 CPUs, default":               {cpus: 2, percent: 50, reserved: -1, wantReserved: "", wantApp: "", wantMillis: 1000},
		"2 CPUs, one reserved":          {cpus: 2, percent: 100, reserved: 1, wantReserved: "0", wantApp: "1", wantMillis: 1000},
		"4 CPUs, default":               {cpus: 4, percent: 50, reserved: -1, wantReserved: "0", wantApp: "1-3", wantMillis: 2000},
		"4 CPUs, full percent":          {cpus: 4, percent: 100, reserved: -1, wantReserved: "0", wantApp: "1-3", wantMillis: 3000},
		"4 CPUs, 75 percent":            {cpus: 4, percent: 75, reserved: -1, wantReserved: "0", wantApp: "1-3", wantMillis: 3000},
		"4 CPUs, none reserved":         {cpus: 4, percent: 100, reserved: 0, wantReserved: "", wantApp: "", wantMillis: 4000},
		"8 CPUs, default":               {cpus: 8, percent: 50, reserved: -1, wantReserved: "0", wantApp: "1-7", wantMillis: 4000},
		"8 CPUs, full percent":          {cpus: 8, percent: 100, reserved: -1, wantReserved: "0", wantApp: "1-7", wantMillis: 7000},
		"8 CPUs, three reserved":        {cpus: 8, percent: 100, reserved: 3, wantReserved: "0-2", wantApp: "3-7", wantMillis: 5000},
		"8 CPUs, seven reserved":        {cpus: 8, percent: 100, reserved: 7, wantReserved: "0-6", wantApp: "7", wantMillis: 1000},
		"8 CPUs, percent below the set": {cpus: 8, percent: 25, reserved: 1, wantReserved: "0", wantApp: "1-7", wantMillis: 2000},
	} {
		t.Run(name, func(t *testing.T) {
			c := CapacityConfig{CPUPercent: ptr(tc.percent)}
			if tc.reserved >= 0 {
				c.ReservedCPUs = ptr(tc.reserved)
			}
			b, err := DeriveBudgets(c.ResolveFor(tc.cpus), hostWith(tc.cpus))
			if err != nil {
				t.Fatal(err)
			}
			if b.CPUMillis != tc.wantMillis || b.ReservedCPUs != tc.wantReserved || b.AppCPUs != tc.wantApp {
				t.Fatalf("millis=%d reserved=%q app=%q, want %d %q %q", b.CPUMillis, b.ReservedCPUs, b.AppCPUs, tc.wantMillis, tc.wantReserved, tc.wantApp)
			}
		})
	}
}

func TestDeriveBudgetsReservesTheLowestOnlineCPUsWhenTheyAreNotContiguous(t *testing.T) {
	host := hostWith(4)
	host.OnlineCPUs, host.AffinityCPUs = []int{0, 2, 3, 5}, []int{0, 2, 3, 5}
	b, err := DeriveBudgets((&CapacityConfig{CPUPercent: ptr(100)}).ResolveFor(4), host)
	if err != nil || b.ReservedCPUs != "0" || b.AppCPUs != "2-3,5" || b.CPUMillis != 3000 {
		t.Fatalf("budgets = %+v, %v", b, err)
	}
}

func TestDeriveBudgetsWithNothingReservedIsExactlyToday(t *testing.T) {
	defaults := (*CapacityConfig)(nil).Resolve()
	plain := HostFacts{MemoryBytes: 16 * gib, CPUs: 4, VolumeBytes: 16 * gib}
	want, err := DeriveBudgets(defaults, plain)
	if err != nil {
		t.Fatal(err)
	}
	// None of the CPU facts is read, so a disagreeing or missing one cannot refuse a host that reserves nothing.
	odd := plain
	odd.OnlineCPUs, odd.AffinityCPUs, odd.CPUSetSupported = []int{7}, []int{9}, false
	got, err := DeriveBudgets(defaults, odd)
	if err != nil || got != want || got.CPUMillis != 2000 || got.ReservedCPUs != "" || got.AppCPUs != "" {
		t.Fatalf("budgets = %+v, %v; want %+v", got, err, want)
	}
}

func TestDeriveBudgetsRefusesReservationsOutsideTheHost(t *testing.T) {
	for name, reserved := range map[string]int{"all CPUs": 4, "more than the host": 9, "negative": -1} {
		_, err := DeriveBudgets(ResolvedCapacity{MemoryPercent: 25, CPUPercent: 50, DiskBytes: 8 * gib, RunningBackends: 8, ImageStoreBytes: 10 * gib, ReservedCPUs: reserved}, hostWith(4))
		var notReady *NotReadyError
		if !errors.As(err, &notReady) || notReady.Reason != NotReadyReservedCPUs || !errors.Is(err, ErrNotReady) {
			t.Fatalf("%s: err = %v, want %q", name, err, NotReadyReservedCPUs)
		}
	}
	// NCPU-1 is the largest valid value.
	if _, err := DeriveBudgets(ResolvedCapacity{MemoryPercent: 25, CPUPercent: 50, DiskBytes: 8 * gib, RunningBackends: 8, ImageStoreBytes: 10 * gib, ReservedCPUs: 3}, hostWith(4)); err != nil {
		t.Fatalf("NCPU-1 refused: %v", err)
	}
}

func TestDeriveBudgetsRefusesReadinessOnCPUFactsThatDisagree(t *testing.T) {
	resolved := (*CapacityConfig)(nil).ResolveFor(4)
	if resolved.ReservedCPUs != 1 {
		t.Fatal("fixture must reserve a CPU")
	}
	for name, tc := range map[string]struct {
		mutate func(*HostFacts)
		reason string
	}{
		"online list shorter than NCPU":  {func(h *HostFacts) { h.OnlineCPUs = []int{0, 1, 2} }, NotReadyOnlineCPUs},
		"online list longer than NCPU":   {func(h *HostFacts) { h.OnlineCPUs = cpuRange(5) }, NotReadyOnlineCPUs},
		"affinity differs from online":   {func(h *HostFacts) { h.AffinityCPUs = []int{0, 1, 2} }, NotReadyOnlineCPUs},
		"affinity same size, other CPUs": {func(h *HostFacts) { h.AffinityCPUs = []int{0, 1, 2, 4} }, NotReadyOnlineCPUs},
		"online list unreadable":         {func(h *HostFacts) { h.OnlineCPUs, h.AffinityCPUs = nil, nil }, NotReadyHostFact},
		"online list not ascending":      {func(h *HostFacts) { h.OnlineCPUs = []int{0, 2, 1, 3} }, NotReadyOnlineCPUs},
		"online list repeats a CPU":      {func(h *HostFacts) { h.OnlineCPUs = []int{0, 1, 1, 3} }, NotReadyOnlineCPUs},
		"daemon without cpuset":          {func(h *HostFacts) { h.CPUSetSupported = false }, NotReadyCPUSetUnsupported},
	} {
		t.Run(name, func(t *testing.T) {
			host := hostWith(4)
			tc.mutate(&host)
			_, err := DeriveBudgets(resolved, host)
			var notReady *NotReadyError
			if !errors.As(err, &notReady) || notReady.Reason != tc.reason || !errors.Is(err, ErrNotReady) {
				t.Fatalf("err = %v, want not ready with reason %q", err, tc.reason)
			}
		})
	}
	// Without an affinity reading the online list alone is trusted; the sysfs list is the primary source.
	host := hostWith(4)
	host.AffinityCPUs = nil
	if _, err := DeriveBudgets(resolved, host); err != nil {
		t.Fatalf("online list alone refused: %v", err)
	}
}

func TestFormatBudgetsNamesTheCPUSets(t *testing.T) {
	b, err := DeriveBudgets((*CapacityConfig)(nil).ResolveFor(4), hostWith(4))
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatBudgets(b); !strings.Contains(got, "cpu_millis=2000") || !strings.HasSuffix(got, "reserved_cpus=0 app_cpus=1-3") {
		t.Fatalf("ready line = %q", got)
	}
	none, _ := DeriveBudgets((*CapacityConfig)(nil).Resolve(), HostFacts{MemoryBytes: 16 * gib, CPUs: 4, VolumeBytes: 16 * gib})
	if got := FormatBudgets(none); !strings.HasSuffix(got, "reserved_cpus=none app_cpus=all") {
		t.Fatalf("ready line without a reservation = %q", got)
	}
}
