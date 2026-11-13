package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const gib = int64(1) << 30

func ptr[T any](v T) *T { return &v }

func TestCapacityDefaultsApplyWhenAbsent(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	writePrivate(t, path, configFixture(dir))
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Capacity.Resolve()
	want := ResolvedCapacity{MemoryPercent: 25, CPUPercent: 50, DiskBytes: 8 * gib, RunningBackends: 8, ImageStoreBytes: 10 * gib}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
	// An empty object means the same as no object.
	if (&CapacityConfig{}).Resolve() != want {
		t.Fatal("empty capacity object does not resolve to the defaults")
	}
}

func TestCapacityFieldsOverrideDefaultsIndependently(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	cfg.Capacity = &CapacityConfig{MemoryPercent: ptr(40), RunningBackends: ptr(3)}
	writePrivate(t, path, cfg)
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Capacity.Resolve()
	if got.MemoryPercent != 40 || got.RunningBackends != 3 || got.CPUPercent != 50 || got.DiskBytes != 8*gib || got.ImageStoreBytes != 10*gib {
		t.Fatalf("partial override = %+v", got)
	}
}

func TestCapacityRanges(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	inRange := map[string]CapacityConfig{
		"memory_min":   {MemoryPercent: ptr(1)},
		"memory_max":   {MemoryPercent: ptr(90)},
		"cpu_min":      {CPUPercent: ptr(1)},
		"cpu_max":      {CPUPercent: ptr(100)},
		"disk_min":     {DiskBytes: ptr(gib)},
		"backends_min": {RunningBackends: ptr(1)},
		"backends_max": {RunningBackends: ptr(64)},
		"image_min":    {ImageStoreBytes: ptr(gib)},
		"image_max":    {ImageStoreBytes: ptr(int64(1) << 40)},
	}
	for name, capacity := range inRange {
		t.Run("accepts_"+name, func(t *testing.T) {
			cfg := configFixture(dir)
			c := capacity
			cfg.Capacity = &c
			writePrivate(t, path, cfg)
			if _, err := LoadConfig(path); err != nil {
				t.Fatalf("boundary value refused: %v", err)
			}
		})
	}
	outOfRange := map[string]CapacityConfig{
		"memory_zero":    {MemoryPercent: ptr(0)},
		"memory_over":    {MemoryPercent: ptr(91)},
		"memory_neg":     {MemoryPercent: ptr(-1)},
		"cpu_zero":       {CPUPercent: ptr(0)},
		"cpu_over":       {CPUPercent: ptr(101)},
		"disk_under":     {DiskBytes: ptr(gib - 1)},
		"disk_zero":      {DiskBytes: ptr(int64(0))},
		"backends_zero":  {RunningBackends: ptr(0)},
		"backends_over":  {RunningBackends: ptr(65)},
		"image_under":    {ImageStoreBytes: ptr(gib - 1)},
		"image_over":     {ImageStoreBytes: ptr(int64(1)<<40 + 1)},
		"image_negative": {ImageStoreBytes: ptr(int64(-5))},
	}
	for name, capacity := range outOfRange {
		t.Run("refuses_"+name, func(t *testing.T) {
			cfg := configFixture(dir)
			c := capacity
			cfg.Capacity = &c
			writePrivate(t, path, cfg)
			if _, err := LoadConfig(path); err == nil {
				t.Fatal("out-of-range value accepted")
			}
		})
	}
}

func TestCapacityRefusesUnknownField(t *testing.T) {
	dir := canonicalTempDir(t)
	path := filepath.Join(dir, "service.json")
	cfg := configFixture(dir)
	writePrivate(t, path, cfg)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := append(raw[:len(raw)-1:len(raw)-1], []byte(`,"capacity":{"memory_percent":20,"swap_percent":5}}`)...)
	if err := os.WriteFile(path, withUnknown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("unknown capacity field accepted")
	}
	duplicate := append(raw[:len(raw)-1:len(raw)-1], []byte(`,"capacity":{"memory_percent":20,"memory_percent":30}}`)...)
	if err := os.WriteFile(path, duplicate, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("duplicate capacity field accepted")
	}
}

func TestDeriveBudgetsFromHostFacts(t *testing.T) {
	// The reference host of doc 167 section 4: 4 vCPU, 16 GiB, a 16 GiB volume.
	host := HostFacts{MemoryBytes: 16 * gib, CPUs: 4, VolumeBytes: 16 * gib}
	b, err := DeriveBudgets((*CapacityConfig)(nil).Resolve(), host)
	if err != nil {
		t.Fatal(err)
	}
	if b.MemoryMiB != 4096 || b.CPUMillis != 2000 || b.DiskBytes != 8*gib || b.ImageStoreBytes != 10*gib || b.RunningBackends != 8 {
		t.Fatalf("budgets = %+v", b)
	}
	other, err := DeriveBudgets(ResolvedCapacity{MemoryPercent: 90, CPUPercent: 100, DiskBytes: gib, RunningBackends: 64, ImageStoreBytes: gib}, HostFacts{MemoryBytes: 2 * gib, CPUs: 3, VolumeBytes: gib})
	if err != nil {
		t.Fatal(err)
	}
	if other.MemoryMiB != 1843 || other.CPUMillis != 3000 || other.DiskBytes != gib || other.RunningBackends != 64 {
		t.Fatalf("budgets = %+v", other)
	}
}

func TestDeriveBudgetsFailsClosed(t *testing.T) {
	defaults := (*CapacityConfig)(nil).Resolve()
	good := HostFacts{MemoryBytes: 16 * gib, CPUs: 4, VolumeBytes: 16 * gib}
	cases := map[string]struct {
		capacity ResolvedCapacity
		host     HostFacts
		reason   string
	}{
		"volume_smaller_than_disk_budget": {defaults, HostFacts{MemoryBytes: good.MemoryBytes, CPUs: good.CPUs, VolumeBytes: 8*gib - 1}, NotReadyVolumeTooSmall},
		"volume_unreadable":               {defaults, HostFacts{MemoryBytes: good.MemoryBytes, CPUs: good.CPUs}, NotReadyHostFact},
		"memory_unreadable":               {defaults, HostFacts{CPUs: good.CPUs, VolumeBytes: good.VolumeBytes}, NotReadyHostFact},
		"cpus_unreadable":                 {defaults, HostFacts{MemoryBytes: good.MemoryBytes, VolumeBytes: good.VolumeBytes}, NotReadyHostFact},
		"memory_below_one_backend":        {ResolvedCapacity{MemoryPercent: 1, CPUPercent: 50, DiskBytes: 8 * gib, RunningBackends: 8, ImageStoreBytes: 10 * gib}, HostFacts{MemoryBytes: 1 * gib, CPUs: 4, VolumeBytes: 16 * gib}, NotReadyMemoryBudget},
		"cpu_below_one_backend":           {ResolvedCapacity{MemoryPercent: 25, CPUPercent: 1, DiskBytes: 8 * gib, RunningBackends: 8, ImageStoreBytes: 10 * gib}, HostFacts{MemoryBytes: 16 * gib, CPUs: 2, VolumeBytes: 16 * gib}, NotReadyCPUBudget},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DeriveBudgets(tc.capacity, tc.host)
			var notReady *NotReadyError
			if !errors.As(err, &notReady) || notReady.Reason != tc.reason || !errors.Is(err, ErrNotReady) {
				t.Fatalf("err = %v, want not ready with reason %q", err, tc.reason)
			}
		})
	}
	// The exact boundary is admitted: the budget equals one minimum backend.
	if _, err := DeriveBudgets(defaults, HostFacts{MemoryBytes: 64 * (1 << 20), CPUs: 1, VolumeBytes: 8 * gib}); err != nil {
		// 25% of 64 MiB is 16 MiB (the minimum backend); 50% of one CPU is 500 millis.
		t.Fatalf("boundary budgets refused: %v", err)
	}
	if _, err := DeriveBudgets(defaults, HostFacts{MemoryBytes: 64*(1<<20) - 4*(1<<20), CPUs: 1, VolumeBytes: 8 * gib}); err == nil {
		t.Fatal("budget under the minimum backend accepted")
	}
}
