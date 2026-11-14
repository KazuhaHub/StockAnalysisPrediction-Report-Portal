package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func TestSoakRecorderWritesEveryUpdateAtomicallyAndKeepsPartialEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child-evidence.json")
	r := newSoakRecorder(path)
	r.SetBudgets(soakBudgets{MemoryMiB: 3993, CPUMillis: 2000, DiskBytes: 8 << 30, ImageStoreBytes: 10 << 30, RunningBackends: 8})
	r.AddProbe(soakProbeResult{Name: "ninth_backend", Expected: "capacity_exhausted_running_backends", Observed: "capacity_exhausted_running_backends", Level: soakProbeNative})
	var read soakEvidence
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &read) != nil {
		t.Fatalf("evidence after two updates: %v %q", err, raw)
	}
	if read.Format != soakEvidenceFormat || read.Budgets.MemoryMiB != 3993 || len(read.Probes) != 1 || !read.Probes[0].Passed {
		t.Fatalf("evidence = %+v", read)
	}
	// A refused probe is recorded as failed, with what was observed instead.
	r.AddProbe(soakProbeResult{Name: "cpu_over_budget", Expected: "capacity_exhausted_cpu", Observed: "capacity_exhausted_disk", Level: soakProbeNative})
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
		r := soakProbeResult{Expected: tc.expected, Observed: tc.observed, Level: soakProbeNative}
		r.judge()
		if r.Passed != tc.want {
			t.Errorf("%q vs %q: passed=%t", tc.expected, tc.observed, r.Passed)
		}
	}
	// A unit level probe observes nothing in this run, so it is never judged a pass, even if a value is set.
	unit := soakProbeResult{Expected: "x", Observed: "x", Level: soakProbeUnit}
	unit.judge()
	if unit.Passed {
		t.Error("a unit level probe passed")
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

// Probe levels. A native probe ran against the real runner or the real Portal handler in the soak and records what
// it observed. A unit probe is proven by unit tests that the same job runs; it records no observation, only the tests.
const (
	soakProbeNative = "native"
	soakProbeUnit   = "unit"
)

// soakProbeResult is one excess request and what it was refused with.
type soakProbeResult struct {
	Name     string
	Expected string
	Observed string
	Passed   bool
	Level    string
	// Tests are the unit tests that prove a unit level probe; the soak job runs them and the verdict reads their result.
	Tests  []string
	Detail string
	// Retired says how a refused probe installation was taken out of the count: "managed_uninstall" through the
	// ordinary uninstall, or "refused" when that was refused (the soak then fails; there is no other path).
	Retired string
}

// judge decides a native probe: the observed code is the expected one. A unit probe is never judged here.
func (p *soakProbeResult) judge() {
	p.Passed = p.Level == soakProbeNative && p.Expected != "" && p.Observed == p.Expected
}

// MarshalJSON writes a unit level probe with a null observation and a null verdict: nothing was observed in this run.
func (p soakProbeResult) MarshalJSON() ([]byte, error) {
	wire := struct {
		Name     string   `json:"name"`
		Expected string   `json:"expected"`
		Observed *string  `json:"observed"`
		Passed   *bool    `json:"passed"`
		Level    string   `json:"level"`
		Tests    []string `json:"tests,omitempty"`
		Detail   string   `json:"detail,omitempty"`
		Retired  string   `json:"retired,omitempty"`
	}{Name: p.Name, Expected: p.Expected, Level: p.Level, Tests: p.Tests, Detail: p.Detail, Retired: p.Retired}
	if p.Level != soakProbeUnit {
		wire.Observed, wire.Passed = &p.Observed, &p.Passed
	}
	return json.Marshal(wire)
}

// UnmarshalJSON is the reverse, so a test can read the evidence back.
func (p *soakProbeResult) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Name     string   `json:"name"`
		Expected string   `json:"expected"`
		Observed *string  `json:"observed"`
		Passed   *bool    `json:"passed"`
		Level    string   `json:"level"`
		Tests    []string `json:"tests"`
		Detail   string   `json:"detail"`
		Retired  string   `json:"retired"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	*p = soakProbeResult{Name: wire.Name, Expected: wire.Expected, Level: wire.Level, Tests: wire.Tests, Detail: wire.Detail, Retired: wire.Retired}
	if wire.Observed != nil {
		p.Observed = *wire.Observed
	}
	if wire.Passed != nil {
		p.Passed = *wire.Passed
	}
	return nil
}

// soakFailedProbes lists the native probes that were not refused with their typed error.
func soakFailedProbes(probes []soakProbeResult) []string {
	var failed []string
	for _, p := range probes {
		if p.Level == soakProbeNative && !p.Passed {
			failed = append(failed, fmt.Sprintf("%s: expected %q, observed %q", p.Name, p.Expected, p.Observed))
		}
	}
	return failed
}

// soakWindow is one minute of measurement under load.
type soakWindow struct {
	Minute int   `json:"minute"`
	At     int64 `json:"at_ms"`
	// StartedMS and EndedMS bound the minute the window's samples were spread over: the host samples of that minute
	// are the ones the verdict reads the backends' own cgroup counters from.
	StartedMS int64     `json:"started_ms"`
	EndedMS   int64     `json:"ended_ms"`
	Home      soakStats `json:"home"`
	Report    soakStats `json:"report"`
	Dispatch  soakStats `json:"dispatch"`
	// BatchStart is the time from the enqueue of a batch job to the real scheduler's request reaching the stub Dify.
	BatchStart soakStats `json:"batch_start"`
	// DispatchShallow is the same decision over a backlog of soakQueueBacklogShallow jobs. It is recorded beside the
	// judged one and never judged.
	DispatchShallow soakStats `json:"dispatch_shallow"`
	Flood           soakFlood `json:"flood"`
	// Backends is what each soak backend reports about its own loads, read through a signed request after
	// the window's samples: the proof that the floods really ran inside the containers.
	Backends []soakBackendStatus `json:"backends,omitempty"`
}

// soakLoaded holds the pooled summary of each latency series over the whole loaded period.
type soakLoaded struct {
	Home            soakStats `json:"home"`
	Report          soakStats `json:"report"`
	Dispatch        soakStats `json:"dispatch"`
	DispatchShallow soakStats `json:"dispatch_shallow"`
	BatchStart      soakStats `json:"batch_start"`
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

// soakFlood counts what the flood process did in the window: signed requests to the eight backends by outcome. The
// relay admits two requests at once Portal-wide; the rest of the attempts are refused 429 app_platform_busy, and both
// counts are recorded. Anything else is an error class of its own.
type soakFlood struct {
	BackendRequests      int64 `json:"backend_requests"`
	BackendRequestsOK    int64 `json:"backend_requests_ok"`
	BackendRequestsBusy  int64 `json:"backend_requests_busy"`
	BackendRequestErrors int64 `json:"backend_request_errors"`
	// Classes counts the window's requests by outcome: "200", "429:app_platform_busy", "502:<code>", "transport".
	Classes map[string]int64 `json:"classes"`
	// Alive is whether the flood process was running when the window ended, and HeartbeatAgeMS is how long ago its
	// counters file was last written: a process that stopped counting is not a flood.
	Alive          bool  `json:"flood_alive"`
	HeartbeatAgeMS int64 `json:"heartbeat_age_ms"`
}

// soakFloodCounters is what the flood process writes about itself once a second.
type soakFloodCounters struct {
	Requests    int64            `json:"requests"`
	Classes     map[string]int64 `json:"classes"`
	HeartbeatMS int64            `json:"heartbeat_ms"`
}

const (
	soakFloodOK   = "200"
	soakFloodBusy = "429:app_platform_busy"
)

// soakFloodClass names the outcome of one flood request: the status and the typed code of the answer, or "transport".
func soakFloodClass(status int, body []byte, err error) string {
	if err != nil {
		return "transport"
	}
	if status == http.StatusOK {
		return soakFloodOK
	}
	var wire struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &wire)
	return strconv.Itoa(status) + ":" + wire.Code
}

// soakFloodWindow is what the flood did between two readings of its counters.
func soakFloodWindow(previous, current soakFloodCounters, alive bool, now time.Time) soakFlood {
	classes := map[string]int64{}
	for class, count := range current.Classes {
		if delta := count - previous.Classes[class]; delta > 0 {
			classes[class] = delta
		}
	}
	window := soakFlood{BackendRequests: current.Requests - previous.Requests, BackendRequestsOK: classes[soakFloodOK], BackendRequestsBusy: classes[soakFloodBusy],
		Classes: classes, Alive: alive, HeartbeatAgeMS: now.UnixMilli() - current.HeartbeatMS}
	for class, count := range classes {
		if class != soakFloodOK && class != soakFloodBusy {
			window.BackendRequestErrors += count
		}
	}
	return window
}

// soakSessionLifetime is how long the soak's session and cookie live: the whole run, with its installs and baseline, and
// an hour more. The first smokes used the one hour of the actor fixture, which would have expired 55 minutes into the load.
func soakSessionLifetime(minutes, baselineSeconds int) time.Duration {
	return time.Duration(minutes)*time.Minute + time.Duration(baselineSeconds)*time.Second + time.Hour
}

// soakSampleTimes spreads n samples evenly across a window: the first at its start, one every window/n.
func soakSampleTimes(start time.Time, window time.Duration, n int) []time.Time {
	if n < 1 {
		return nil
	}
	times := make([]time.Time, n)
	for i := range times {
		times[i] = start.Add(time.Duration(i) * window / time.Duration(n))
	}
	return times
}

// soakBatchProbeEvery is how many samples pass between batch probes: every fifth sample, so one job every three
// seconds, twenty a minute. Each probe leaves a finished job in the table, and the queue's fair-share reads them all.
const soakBatchProbeEvery = 5

// soakSamplesPerWindow is the number of samples spread across one minute: one every 600 ms.
const soakSamplesPerWindow = 100

func soakBatchProbeDue(iteration int) bool { return iteration%soakBatchProbeEvery == 0 }

// soakImageProbeMemoryMiB is the memory the image store probe's two installs declare: one MiB more than the runner's whole
// memory budget, so the runner refuses their start for memory after it has loaded their images. A budget at or beyond the
// descriptor maximum cannot be exceeded by any valid descriptor, so the probe cannot run there.
func soakImageProbeMemoryMiB(budgetMiB int64) (int, error) {
	if budgetMiB < 1 || budgetMiB+1 > appplatform.MaxBackendMemoryMiB {
		return 0, fmt.Errorf("a runner memory budget of %d MiB cannot be exceeded by a valid descriptor", budgetMiB)
	}
	return int(budgetMiB) + 1, nil
}

// soakRunnerServiceTimeout is the -test.timeout of the runner service process the soak starts: the whole run and its installs and
// baseline, and half an hour more. The shared fixture's ten minutes would end the service in the middle of the load.
func soakRunnerServiceTimeout(minutes, baselineSeconds int) string {
	return fmt.Sprintf("%dm", minutes+baselineSeconds/60+30)
}

// soakVariant is a diagnostic switch for explaining a failing run, never part of an evidence run: it measures the same
// scenario with one part removed so the part's share of a latency can be told apart.
type soakVariant struct {
	Name        string
	MemoryDB    bool // the in-memory template store of the unit suite instead of a file-backed SQLite database
	BatchProbes bool // the batch enqueue-to-start probes; without them the scheduler still runs and holds its backlog
}

// soakParseVariant reads APP_CAPACITY_SOAK_VARIANT: empty, or a comma separated list of memory-db and no-batch-probes.
func soakParseVariant(env string) (soakVariant, error) {
	v := soakVariant{Name: env, BatchProbes: true}
	for _, part := range strings.Split(env, ",") {
		switch part {
		case "":
		case "memory-db":
			v.MemoryDB = true
		case "no-batch-probes":
			v.BatchProbes = false
		default:
			return soakVariant{}, fmt.Errorf("unknown soak variant %q", part)
		}
	}
	return v, nil
}

// soakImageStoreEvidence records how the image store probe ran, or why it stayed at unit level.
type soakImageStoreEvidence struct {
	Mode            string `json:"mode"`
	Reason          string `json:"reason,omitempty"`
	DockerFreeBytes int64  `json:"docker_free_bytes,omitempty"`
	Detail          string `json:"detail,omitempty"`
}

// soakPortalEvidence says what the measured Portal was: its database, the background loops it ran, and what it did not run.
type soakPortalEvidence struct {
	Database     string   `json:"database"`
	Running      []string `json:"running"`
	NotExercised []string `json:"not_exercised"`
}

type soakEvidence struct {
	Format        string            `json:"format"`
	Budgets       soakBudgets       `json:"budgets"`
	Sizing        soakSizing        `json:"sizing"`
	Installations map[string]int    `json:"installations"`
	Probes        []soakProbeResult `json:"probes"`
	Idle          soakWindow        `json:"idle"`
	// Loaded is the p95 of every loaded sample of the run pooled together: the figure the latency verdict judges.
	Loaded          soakLoaded     `json:"loaded"`
	IdleWindows     []soakWindow   `json:"idle_windows"`
	Windows         []soakWindow   `json:"windows"`
	Floods          map[string]any `json:"floods"`
	LoadStartedMS   int64          `json:"load_started_ms"`
	LoadFinishedMS  int64          `json:"load_finished_ms"`
	DurationMinutes int            `json:"duration_minutes"`
	Markers         []string       `json:"markers"`
	// SessionExpiresMS is when the soak's session and cookie expire; the verdict requires it to be after the load.
	SessionExpiresMS int64 `json:"session_expires_ms"`
	// Variant names the diagnostic variant of the run, empty for an evidence run.
	Variant    string                 `json:"variant"`
	ImageStore soakImageStoreEvidence `json:"image_store"`
	Portal     soakPortalEvidence     `json:"portal"`
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
func (r *soakRecorder) SetLoaded(pooled soakSamples) {
	r.update(func(e *soakEvidence) {
		e.Loaded = soakLoaded{Home: soakSummarize(pooled.home), Report: soakSummarize(pooled.report),
			Dispatch: soakSummarize(pooled.dispatch), DispatchShallow: soakSummarize(pooled.dispatchShallow), BatchStart: soakSummarize(pooled.batchStart)}
	})
}
func (r *soakRecorder) SetSession(expires time.Time) {
	r.update(func(e *soakEvidence) { e.SessionExpiresMS = expires.UnixMilli() })
}
func (r *soakRecorder) SetVariant(name string) { r.update(func(e *soakEvidence) { e.Variant = name }) }
func (r *soakRecorder) SetImageStore(v soakImageStoreEvidence) {
	r.update(func(e *soakEvidence) { e.ImageStore = v })
}
func (r *soakRecorder) SetPortal(v soakPortalEvidence) {
	r.update(func(e *soakEvidence) { e.Portal = v })
}
func (r *soakRecorder) SetIdle(w soakWindow) { r.update(func(e *soakEvidence) { e.Idle = w }) }
func (r *soakRecorder) SetLoad(startedMS, finishedMS int64, minutes int) {
	r.update(func(e *soakEvidence) {
		e.LoadStartedMS, e.LoadFinishedMS, e.DurationMinutes = startedMS, finishedMS, minutes
	})
}

func TestSoakUnitLevelProbeIsRecordedWithNoObservationAndTheTestsThatProveIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.json")
	r := newSoakRecorder(path)
	r.AddProbe(soakProbeResult{Name: "task_over_application_quota", Expected: "task_capacity", Level: soakProbeUnit, Tests: []string{"TestTaskQuotasRefuseAtTheirOwnBoundary"},
		Detail: "no application has more than one installation in this soak"})
	raw, _ := os.ReadFile(path)
	var generic struct {
		Probes []map[string]any `json:"probes"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil || len(generic.Probes) != 1 {
		t.Fatalf("evidence %q: %v", raw, err)
	}
	probe := generic.Probes[0]
	if probe["level"] != "unit" || probe["observed"] != nil || probe["passed"] != nil {
		t.Fatalf("a unit level probe claims an observation or a pass: %+v", probe)
	}
	tests, _ := probe["tests"].([]any)
	if len(tests) != 1 || tests[0] != "TestTaskQuotasRefuseAtTheirOwnBoundary" {
		t.Fatalf("tests = %v", probe["tests"])
	}
	// A native probe keeps both: a string observation and a boolean verdict.
	r.AddProbe(soakProbeResult{Name: "ninth_backend", Expected: "x", Observed: "x", Level: soakProbeNative})
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &generic); err != nil || generic.Probes[1]["observed"] != "x" || generic.Probes[1]["passed"] != true || generic.Probes[1]["level"] != "native" {
		t.Fatalf("native probe: %+v", generic.Probes)
	}
}

func TestSoakNativeProbesAreTheOnlyOnesAnExcessRequestIsJudgedOn(t *testing.T) {
	rec := newSoakRecorder(filepath.Join(t.TempDir(), "e.json"))
	rec.AddProbe(soakProbeResult{Name: "task_over_application_quota", Expected: "task_capacity", Level: soakProbeUnit, Tests: []string{"T"}})
	rec.AddProbe(soakProbeResult{Name: "ninth_backend", Expected: "x", Observed: "x", Level: soakProbeNative})
	if failed := soakFailedProbes(rec.Snapshot().Probes); len(failed) != 0 {
		t.Fatalf("a unit level probe was judged: %v", failed)
	}
	rec.AddProbe(soakProbeResult{Name: "cpu_over_budget", Expected: "x", Observed: "y", Level: soakProbeNative})
	if failed := soakFailedProbes(rec.Snapshot().Probes); len(failed) != 1 {
		t.Fatalf("failed = %v", failed)
	}
}

func TestSoakSessionOutlivesTheRun(t *testing.T) {
	// The session of the first smokes lasted an hour; a 120 minute run and its installs and baseline outlast that.
	for _, tc := range []struct{ minutes, baseline int }{{5, 30}, {120, 180}, {140, 180}} {
		lifetime := soakSessionLifetime(tc.minutes, tc.baseline)
		run := time.Duration(tc.minutes)*time.Minute + time.Duration(tc.baseline)*time.Second
		if lifetime < run+time.Hour {
			t.Errorf("%d minutes with a %d second baseline: lifetime %v is under the run plus an hour", tc.minutes, tc.baseline, lifetime)
		}
	}
	if soakSessionLifetime(120, 180) <= time.Hour {
		t.Fatal("the session of a 120 minute run is the one hour of the first smokes")
	}
}

func TestSoakFloodClassNamesStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		err    error
		want   string
	}{
		{200, `{"count":3}`, nil, "200"},
		{429, `{"error":"application busy","code":"app_platform_busy"}`, nil, "429:app_platform_busy"},
		{502, `{"error":"x","code":"app_platform_backend_unavailable"}`, nil, "502:app_platform_backend_unavailable"},
		{500, `not json`, nil, "500:"},
		{429, ``, nil, "429:"},
		{0, ``, errors.New("connection refused"), "transport"},
	} {
		if got := soakFloodClass(tc.status, []byte(tc.body), tc.err); got != tc.want {
			t.Errorf("status %d body %q err %v = %q, want %q", tc.status, tc.body, tc.err, got, tc.want)
		}
	}
}

func TestSoakFloodWindowIsTheDeltaOfTheCountersWithItsClassesAndLiveness(t *testing.T) {
	previous := soakFloodCounters{Requests: 100, Classes: map[string]int64{"200": 10, "429:app_platform_busy": 90}, HeartbeatMS: 1000}
	current := soakFloodCounters{Requests: 1100, Classes: map[string]int64{"200": 70, "429:app_platform_busy": 1020, "502:x": 10}, HeartbeatMS: 61_000}
	window := soakFloodWindow(previous, current, true, time.UnixMilli(62_500))
	if window.BackendRequests != 1000 || window.BackendRequestsOK != 60 || window.BackendRequestsBusy != 930 || window.BackendRequestErrors != 10 {
		t.Fatalf("window = %+v", window)
	}
	if got := window.Classes; got["200"] != 60 || got["429:app_platform_busy"] != 930 || got["502:x"] != 10 || len(got) != 3 {
		t.Fatalf("classes = %v", got)
	}
	if !window.Alive || window.HeartbeatAgeMS != 1500 {
		t.Fatalf("liveness = %t, heartbeat age %d", window.Alive, window.HeartbeatAgeMS)
	}
	// A class with no new requests in the window is not listed, and a flood process that exited is not alive.
	quiet := soakFloodWindow(current, current, false, time.UnixMilli(62_500))
	if len(quiet.Classes) != 0 || quiet.Alive || quiet.BackendRequests != 0 {
		t.Fatalf("quiet window = %+v", quiet)
	}
}

func TestSoakSamplesAreSpreadEvenlyAcrossTheirWindow(t *testing.T) {
	start := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	times := soakSampleTimes(start, time.Minute, 100)
	if len(times) != 100 || !times[0].Equal(start) {
		t.Fatalf("%d sample times, first %v", len(times), times[0])
	}
	for i := 1; i < len(times); i++ {
		if gap := times[i].Sub(times[i-1]); gap != 600*time.Millisecond {
			t.Fatalf("gap %d is %v, want 600ms", i, gap)
		}
	}
	if last := times[len(times)-1]; last.After(start.Add(time.Minute)) {
		t.Fatalf("the last sample at %v falls outside the window", last)
	}
	if got := soakSampleTimes(start, time.Minute, 0); len(got) != 0 {
		t.Fatal("no samples requested")
	}
}

func TestSoakProbeEveryNthIterationSpacesTheBatchProbes(t *testing.T) {
	// One batch job every few seconds: a probe per minute is a job in the table, and the queue's fair-share reads them all.
	perMinute := 0
	for i := 0; i < soakSamplesPerWindow; i++ {
		if soakBatchProbeDue(i) {
			perMinute++
		}
	}
	if perMinute < 10 || perMinute > 30 {
		t.Fatalf("%d batch probes in a window of %d samples", perMinute, soakSamplesPerWindow)
	}
}

func TestSoakRunnerServiceOutlastsTheRun(t *testing.T) {
	// The runner service is a test process with its own timeout, ten minutes by default; the first smokes with a three
	// minute baseline found it killed mid-load, and a 120 minute run would have lost it after ten.
	for _, tc := range []struct{ minutes, baseline int }{{5, 180}, {120, 180}, {140, 180}} {
		timeout, err := time.ParseDuration(soakRunnerServiceTimeout(tc.minutes, tc.baseline))
		if err != nil {
			t.Fatal(err)
		}
		run := time.Duration(tc.minutes)*time.Minute + time.Duration(tc.baseline)*time.Second
		if timeout < run+30*time.Minute {
			t.Errorf("%d minutes: the runner service times out after %v", tc.minutes, timeout)
		}
	}
}

func TestSoakVariantIsADiagnosticSwitchAndNeverEvidence(t *testing.T) {
	for _, tc := range []struct {
		env                  string
		memoryDB, batchProbe bool
		ok                   bool
	}{
		{"", false, true, true},
		{"memory-db", true, true, true},
		{"no-batch-probes", false, false, true},
		{"memory-db,no-batch-probes", true, false, true},
		{"unknown", false, false, false},
	} {
		v, err := soakParseVariant(tc.env)
		if (err == nil) != tc.ok || (tc.ok && (v.MemoryDB != tc.memoryDB || v.BatchProbes != tc.batchProbe)) {
			t.Errorf("variant %q = %+v, %v", tc.env, v, err)
		}
	}
}

func TestSoakImageStoreProbeAsksForOneMiBMoreThanTheBudgetSoNoContainerIsEverCreatedFromAFourGiBImage(t *testing.T) {
	// The images load before the start is admitted, so a start refused for memory leaves the 4 GiB image loaded, reserved
	// and unused. Starting a container from it timed out in the daemon on one of three smokes.
	for _, budget := range []int64{3925, 3995, 4000} {
		memory, err := soakImageProbeMemoryMiB(budget)
		if err != nil || int64(memory) != budget+1 {
			t.Errorf("budget %d: %d, %v", budget, memory, err)
		}
		if memory > appplatform.MaxBackendMemoryMiB || memory < appplatform.MinBackendMemoryMiB {
			t.Errorf("budget %d: %d MiB is outside the descriptor range", budget, memory)
		}
	}
	for _, budget := range []int64{appplatform.MaxBackendMemoryMiB, appplatform.MaxBackendMemoryMiB + 1000, 0} {
		if memory, err := soakImageProbeMemoryMiB(budget); err == nil {
			t.Errorf("budget %d: asked for %d MiB, a request no budget can refuse", budget, memory)
		}
	}
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

func TestSoakRecorderKeepsThePooledLoadedP95OfEveryWindowTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "e.json")
	r := newSoakRecorder(path)
	var pooled soakSamples
	for window := 0; window < 3; window++ {
		var one soakSamples
		for i := 1; i <= 100; i++ {
			// Window 2 is slow in every sample; the pooled p95 must still weigh all 300 samples, not one window.
			scale := time.Millisecond
			if window == 1 {
				scale = 10 * time.Millisecond
			}
			one.home = append(one.home, time.Duration(i)*scale)
		}
		pooled.home = append(pooled.home, one.home...)
		r.SetLoaded(pooled)
	}
	var read soakEvidence
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &read); err != nil {
		t.Fatal(err)
	}
	if read.Loaded.Home.Count != 300 {
		t.Fatalf("pooled count = %d", read.Loaded.Home.Count)
	}
	sorted := soakSummarize(pooled.home)
	if read.Loaded.Home.P95Micros != sorted.P95Micros || read.Loaded.Home.P95Micros <= 95_000 {
		t.Fatalf("pooled p95 = %d", read.Loaded.Home.P95Micros)
	}
	if !strings.Contains(string(raw), `"loaded"`) || !strings.Contains(string(raw), `"dispatch_shallow"`) {
		t.Fatal("the pooled loaded p95 is not under the keys the verdict reads")
	}
}
