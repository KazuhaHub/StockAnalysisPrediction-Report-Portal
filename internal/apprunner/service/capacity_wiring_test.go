package service

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func reportWith(cpus int, cpuset bool) driver.Report {
	return driver.Report{ReportedCPUs: cpus, ReportedMemoryBytes: 16 * gib, CPUSetSupported: cpuset}
}

func factsFor(cpus int) HostFacts {
	return HostFacts{VolumeBytes: 16 * gib, OnlineCPUs: cpuRange(cpus), AffinityCPUs: cpuRange(cpus)}
}

// The runner derives its budgets for the host it reads, so a 4 CPU host with an empty capacity block reserves CPU 0
// and hands exactly the rest to the driver: a Resolve() in place of ResolveFor, or a dropped option, fails here.
func TestPlanCapacityReservesByDefaultAndPinsTheDriver(t *testing.T) {
	budgets, options, err := planCapacity(Config{}, reportWith(4, true), factsFor(4))
	if err != nil {
		t.Fatal(err)
	}
	if budgets.ReservedCPUs != "0" || budgets.AppCPUs != "1-3" || budgets.CPUMillis != 2000 {
		t.Fatalf("budgets = %+v", budgets)
	}
	if got := driver.RuntimeAppCPUSet(options...); got != "1-3" {
		t.Fatalf("driver app CPU set = %q, want 1-3", got)
	}
}

func TestPlanCapacityOnASmallHostOrWithNothingReservedPinsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  Config
		cpus int
	}{
		"2 CPUs":          {Config{}, 2},
		"explicitly zero": {Config{Capacity: &CapacityConfig{ReservedCPUs: ptr(0)}}, 8},
		"3 CPUs, default": {Config{}, 3},
	} {
		budgets, options, err := planCapacity(tc.cfg, reportWith(tc.cpus, false), factsFor(tc.cpus))
		if err != nil || budgets.AppCPUs != "" || driver.RuntimeAppCPUSet(options...) != "" {
			t.Fatalf("%s: budgets %+v, pinned %q, %v", name, budgets, driver.RuntimeAppCPUSet(options...), err)
		}
	}
}

func TestPlanCapacityFallsBackToTheAllowedSetWhenSysfsIsUnreadable(t *testing.T) {
	facts := factsFor(4)
	facts.OnlineCPUs = nil
	budgets, _, err := planCapacity(Config{}, reportWith(4, true), facts)
	if err != nil || budgets.AppCPUs != "1-3" {
		t.Fatalf("budgets %+v, %v", budgets, err)
	}
	facts.AffinityCPUs = nil
	if _, _, err := planCapacity(Config{}, reportWith(4, true), facts); !errors.Is(err, ErrNotReady) {
		t.Fatalf("with neither list readable: %v", err)
	}
}

func TestPlanCapacityRefusesAProfileTheDecoderCannotAccept(t *testing.T) {
	var sparse []int
	for cpu := 0; len(sparse) < 1100; cpu += 59 {
		sparse = append(sparse, cpu)
	}
	facts := HostFacts{VolumeBytes: 16 * gib, OnlineCPUs: sparse, AffinityCPUs: sparse}
	_, _, err := planCapacity(Config{}, reportWith(len(sparse), true), facts)
	var notReady *NotReadyError
	if !errors.As(err, &notReady) || notReady.Reason != NotReadyProfileTooLarge {
		t.Fatalf("err = %v, want %q", err, NotReadyProfileTooLarge)
	}
}

func TestExplicitAppCPUsSurviveAHostResize(t *testing.T) {
	cfg := Config{Capacity: &CapacityConfig{AppCPUs: "1-3"}}
	small, _, err := planCapacity(cfg, reportWith(4, true), factsFor(4))
	if err != nil {
		t.Fatal(err)
	}
	// The host is resized from 4 to 8 CPUs: the pinned app set, so every installed container's plan, is unchanged.
	big, options, err := planCapacity(cfg, reportWith(8, true), factsFor(8))
	if err != nil || big.AppCPUs != small.AppCPUs || big.AppCPUs != "1-3" || big.ReservedCPUs != "0" || driver.RuntimeAppCPUSet(options...) != "1-3" {
		t.Fatalf("after resize: %+v, %v", big, err)
	}
	if big.CPUMillis != 3000 && big.CPUMillis != 4000 {
		t.Fatalf("budget %d is not bounded by the pinned set", big.CPUMillis)
	}
	if big.CPUMillis > 3000 {
		t.Fatalf("budget %d exceeds the 3 pinned CPUs", big.CPUMillis)
	}
}

func TestExplicitAppCPUsAreCheckedAgainstTheHost(t *testing.T) {
	for name, tc := range map[string]struct {
		app      string
		reserved *int
		cpus     int
	}{
		"overlaps the reserved CPU": {"0-3", nil, 4},
		"not online":                {"1-9", nil, 4},
		"no reservation":            {"1-3", ptr(0), 4},
	} {
		cfg := Config{Capacity: &CapacityConfig{AppCPUs: tc.app, ReservedCPUs: tc.reserved}}
		_, _, err := planCapacity(cfg, reportWith(tc.cpus, true), factsFor(tc.cpus))
		var notReady *NotReadyError
		if !errors.As(err, &notReady) || notReady.Reason != NotReadyAppCPUs {
			t.Fatalf("%s: err = %v, want %q", name, err, NotReadyAppCPUs)
		}
	}
}

func TestAppCPUsIsAStrictConfigField(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	cfg.Capacity = &CapacityConfig{AppCPUs: "1-3"}
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil || loaded.Capacity.AppCPUs != "1-3" {
		t.Fatalf("app_cpus did not load: %v", err)
	}
	for _, bad := range []string{"1,2,3", "x", "3-1", "1-3\n"} {
		cfg.Capacity = &CapacityConfig{AppCPUs: bad}
		writePrivate(t, path, cfg)
		if _, err := LoadConfig(path); err == nil {
			t.Fatalf("app_cpus %q accepted", bad)
		}
	}
}
