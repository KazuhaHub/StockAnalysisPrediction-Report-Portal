package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSoakRecorderWritesEveryUpdateAtomicallyAndKeepsPartialEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child-evidence.json")
	r := newSoakRecorder(path)
	r.SetBudgets(soakBudgets{MemoryMiB: 3993, CPUMillis: 2000, DiskBytes: 8 << 30, ImageStoreBytes: 10 << 30, RunningBackends: 8})
	r.AddProbe(soakProbeResult{Name: "ninth_backend", Expected: "capacity_exhausted_running_backends", Observed: "capacity_exhausted_running_backends", Native: true})
	var read soakEvidence
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &read) != nil {
		t.Fatalf("evidence after two updates: %v %q", err, raw)
	}
	if read.Format != soakEvidenceFormat || read.Budgets.MemoryMiB != 3993 || len(read.Probes) != 1 || !read.Probes[0].Passed {
		t.Fatalf("evidence = %+v", read)
	}
	// A refused probe is recorded as failed, with what was observed instead.
	r.AddProbe(soakProbeResult{Name: "cpu_over_budget", Expected: "capacity_exhausted_cpu", Observed: "capacity_exhausted_disk", Native: true})
	raw, _ = os.ReadFile(path)
	json.Unmarshal(raw, &read)
	if len(read.Probes) != 2 || read.Probes[1].Passed {
		t.Fatalf("a probe refused with the wrong code passed: %+v", read.Probes)
	}
	// No temporary file is left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries", len(entries))
	}
}

func TestSoakRecorderIsSafeForConcurrentWindows(t *testing.T) {
	r := newSoakRecorder(filepath.Join(t.TempDir(), "e.json"))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.AddWindow(soakWindow{Minute: i, At: time.Now().UnixMilli(), Home: soakStats{Count: 1}})
		}()
	}
	wg.Wait()
	if got := len(r.Snapshot().Windows); got != 20 {
		t.Fatalf("%d windows", got)
	}
}

func TestSoakProbeResultPassesOnlyOnTheExpectedCode(t *testing.T) {
	for _, tc := range []struct {
		expected, observed string
		want               bool
	}{
		{"x", "x", true}, {"x", "y", false}, {"x", "", false}, {"", "", false},
	} {
		r := soakProbeResult{Expected: tc.expected, Observed: tc.observed}
		r.judge()
		if r.Passed != tc.want {
			t.Errorf("%q vs %q: passed=%t", tc.expected, tc.observed, r.Passed)
		}
	}
}

// ---- the evidence the soak's Portal child writes ----

const soakEvidenceFormat = "portal.capacity-soak.child/1-dev.1"

// soakBudgets are the effective runner budgets, read from the runner's readiness facts.
type soakBudgets struct {
	MemoryMiB       int64 `json:"memory_mib"`
	CPUMillis       int64 `json:"cpu_millis"`
	DiskBytes       int64 `json:"disk_bytes"`
	ImageStoreBytes int64 `json:"image_store_bytes"`
	RunningBackends int64 `json:"running_backends"`
	EstimatedRows   int64 `json:"estimated_rows"`
}

// soakProbeResult is one excess request and what it was refused with. Native probes ran against the real
// runner or the real Portal handler in the soak; the others are proven at unit level and say why.
type soakProbeResult struct {
	Name     string `json:"name"`
	Expected string `json:"expected"`
	Observed string `json:"observed"`
	Passed   bool   `json:"passed"`
	Native   bool   `json:"native"`
	Detail   string `json:"detail,omitempty"`
}

func (p *soakProbeResult) judge() { p.Passed = p.Expected != "" && p.Observed == p.Expected }

// soakWindow is one minute of measurement under load.
type soakWindow struct {
	Minute   int       `json:"minute"`
	At       int64     `json:"at_ms"`
	Home     soakStats `json:"home"`
	Report   soakStats `json:"report"`
	Dispatch soakStats `json:"dispatch"`
	Flood    soakFlood `json:"flood"`
	// Backends is what each soak backend reports about its own loads, read through a signed request after
	// the window's samples: the proof that the floods really ran inside the containers.
	Backends []soakBackendStatus `json:"backends,omitempty"`
}

type soakBackendStatus struct {
	Installation string `json:"installation"`
	Answered     bool   `json:"answered"`
	HeldBytes    int64  `json:"held_bytes"`
	Threads      int64  `json:"threads"`
	Burners      int64  `json:"burners"`
	LogBytes     int64  `json:"log_bytes"`
	StorageBytes int64  `json:"storage_bytes"`
	StorageFull  int64  `json:"storage_full"`
	DataPresent  bool   `json:"data_present"`
	Requests     int64  `json:"requests"`
}

// soakParseBackendStatus reads the fixture's status document; anything else is an unanswered backend.
func soakParseBackendStatus(installation string, raw []byte) soakBackendStatus {
	var wire struct {
		Soak         string `json:"soak"`
		HeldBytes    int64  `json:"held_bytes"`
		Threads      int64  `json:"threads"`
		Burners      int64  `json:"burners"`
		LogBytes     int64  `json:"log_bytes"`
		StorageBytes int64  `json:"storage_bytes"`
		StorageFull  int64  `json:"storage_full"`
		DataPresent  bool   `json:"data_present"`
		Requests     int64  `json:"requests"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Soak != "status" {
		return soakBackendStatus{Installation: installation}
	}
	return soakBackendStatus{Installation: installation, Answered: true, HeldBytes: wire.HeldBytes, Threads: wire.Threads, Burners: wire.Burners,
		LogBytes: wire.LogBytes, StorageBytes: wire.StorageBytes, StorageFull: wire.StorageFull, DataPresent: wire.DataPresent, Requests: wire.Requests}
}

// soakFlood counts what the platform floods did in the window: signed requests to the eight backends by
// outcome, and task admissions refused at their quota.
type soakFlood struct {
	BackendRequests      int64 `json:"backend_requests"`
	BackendRequestsOK    int64 `json:"backend_requests_ok"`
	BackendRequestErrors int64 `json:"backend_request_errors"`
	TaskRefusals         int64 `json:"task_refusals"`
}

type soakEvidence struct {
	Format          string            `json:"format"`
	Budgets         soakBudgets       `json:"budgets"`
	Sizing          soakSizing        `json:"sizing"`
	Installations   map[string]int    `json:"installations"`
	Probes          []soakProbeResult `json:"probes"`
	Idle            soakWindow        `json:"idle"`
	IdleWindows     []soakWindow      `json:"idle_windows"`
	Windows         []soakWindow      `json:"windows"`
	Floods          map[string]any    `json:"floods"`
	LoadStartedMS   int64             `json:"load_started_ms"`
	LoadFinishedMS  int64             `json:"load_finished_ms"`
	DurationMinutes int               `json:"duration_minutes"`
	Markers         []string          `json:"markers"`
}

// soakRecorder keeps the evidence and rewrites its file after every update, through a temporary file and a
// rename, so a run that dies half way still leaves the evidence it had gathered.
type soakRecorder struct {
	mu       sync.Mutex
	path     string
	evidence soakEvidence
}

func newSoakRecorder(path string) *soakRecorder {
	r := &soakRecorder{path: path}
	r.evidence = soakEvidence{Format: soakEvidenceFormat, Installations: map[string]int{}, Floods: map[string]any{}}
	return r
}

func (r *soakRecorder) update(change func(e *soakEvidence)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(&r.evidence)
	raw, err := json.MarshalIndent(&r.evidence, "", "  ")
	if err != nil {
		return
	}
	temporary := r.path + ".tmp"
	if os.WriteFile(temporary, append(raw, '\n'), 0o644) == nil {
		_ = os.Rename(temporary, r.path)
	}
}

func (r *soakRecorder) Snapshot() soakEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evidence
}

func (r *soakRecorder) SetBudgets(b soakBudgets) { r.update(func(e *soakEvidence) { e.Budgets = b }) }
func (r *soakRecorder) SetSizing(s soakSizing)   { r.update(func(e *soakEvidence) { e.Sizing = s }) }
func (r *soakRecorder) SetCount(name string, n int) {
	r.update(func(e *soakEvidence) { e.Installations[name] = n })
}
func (r *soakRecorder) SetFlood(name string, value any) {
	r.update(func(e *soakEvidence) { e.Floods[name] = value })
}
func (r *soakRecorder) Mark(name string) {
	r.update(func(e *soakEvidence) { e.Markers = append(e.Markers, name) })
}
func (r *soakRecorder) AddProbe(p soakProbeResult) {
	p.judge()
	r.update(func(e *soakEvidence) { e.Probes = append(e.Probes, p) })
}
func (r *soakRecorder) AddWindow(w soakWindow) {
	r.update(func(e *soakEvidence) { e.Windows = append(e.Windows, w) })
}
func (r *soakRecorder) AddIdleWindow(w soakWindow) {
	r.update(func(e *soakEvidence) { e.IdleWindows = append(e.IdleWindows, w) })
}
func (r *soakRecorder) SetIdle(w soakWindow) { r.update(func(e *soakEvidence) { e.Idle = w }) }
func (r *soakRecorder) SetLoad(startedMS, finishedMS int64, minutes int) {
	r.update(func(e *soakEvidence) {
		e.LoadStartedMS, e.LoadFinishedMS, e.DurationMinutes = startedMS, finishedMS, minutes
	})
}

func TestSoakBackendStatusParsesWhatTheFixtureReports(t *testing.T) {
	raw := []byte(`{"soak":"status","held_bytes":364904448,"threads":41,"burners":1,"log_bytes":900000,"storage_bytes":268435456,"storage_full":3,"data_present":true,"requests":12,"goroutines":50}`)
	status := soakParseBackendStatus("installation-1", raw)
	want := soakBackendStatus{Installation: "installation-1", Answered: true, HeldBytes: 364904448, Threads: 41, Burners: 1, LogBytes: 900000,
		StorageBytes: 268435456, StorageFull: 3, DataPresent: true, Requests: 12}
	if status != want {
		t.Fatalf("status = %+v", status)
	}
	for _, bad := range [][]byte{nil, []byte(`not json`), []byte(`{"soak":"other"}`)} {
		if got := soakParseBackendStatus("installation-2", bad); got.Answered || got.Installation != "installation-2" {
			t.Errorf("an unusable answer %q was taken as a status: %+v", bad, got)
		}
	}
}
