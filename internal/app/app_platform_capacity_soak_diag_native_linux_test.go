//go:build linux && (amd64 || arm64)

package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Diagnosis of the Portal's own latency under the soak's load, behind APP_CAPACITY_SOAK_DIAGNOSE=1. For one idle and
// one loaded period it captures, from the measured process, a CPU profile, block and mutex profiles and a goroutine
// dump, times the phases of the home page and the report page (wall time against thread CPU time, so what a request
// waited for is separate from what it computed), reads how long callers waited for a database connection and how
// much of the time the single SQLite connection was in use, and reads the garbage collector's pauses and the
// scheduler's latencies. Nothing here changes how the Portal runs; the files are uploaded with the soak's evidence.

func soakDiagnosing() bool { return os.Getenv("APP_CAPACITY_SOAK_DIAGNOSE") == "1" }

func threadCPU() time.Duration {
	var usage unix.Rusage
	if unix.Getrusage(unix.RUSAGE_THREAD, &usage) != nil {
		return 0
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}

type soakDiagnosis struct {
	Label        string             `json:"label"`
	Seconds      int                `json:"seconds"`
	Requests     map[string]int     `json:"requests"`
	Home         soakPhaseSummary   `json:"home_total"`
	Report       soakPhaseSummary   `json:"report_total"`
	HomePhases   []soakPhaseSummary `json:"home_phases"`
	ReportPhases []soakPhaseSummary `json:"report_phases"`
	DB           soakDiagnosisDB    `json:"database"`
	Runtime      soakDiagnosisGC    `json:"runtime"`
}

type soakDiagnosisDB struct {
	MaxOpenConnections int     `json:"max_open_connections"`
	WaitCount          int64   `json:"pool_wait_count"`
	WaitMillis         float64 `json:"pool_wait_total_ms"`
	// SaturatedShare is the share of 2 ms samples in which every connection was in use.
	SaturatedShare float64 `json:"saturated_share"`
	Samples        int     `json:"samples"`
}

type soakDiagnosisGC struct {
	GCCycles          uint32  `json:"gc_cycles"`
	GCPauseTotalMs    float64 `json:"gc_pause_total_ms"`
	GCPauseP99Ms      float64 `json:"gc_pause_p99_ms"`
	SchedLatencyP50Ms float64 `json:"sched_latency_p50_ms"`
	SchedLatencyP99Ms float64 `json:"sched_latency_p99_ms"`
	Goroutines        int     `json:"goroutines_at_end"`
}

type soakPhaseRecorder struct {
	names []string
	wall  map[string][]time.Duration
	cpu   map[string][]time.Duration
}

func newSoakPhaseRecorder() *soakPhaseRecorder {
	return &soakPhaseRecorder{wall: map[string][]time.Duration{}, cpu: map[string][]time.Duration{}}
}

func (r *soakPhaseRecorder) time(name string, run func()) {
	if _, seen := r.wall[name]; !seen {
		r.names = append(r.names, name)
	}
	cpuBefore, began := threadCPU(), time.Now()
	run()
	r.wall[name] = append(r.wall[name], time.Since(began))
	r.cpu[name] = append(r.cpu[name], threadCPU()-cpuBefore)
}

func (r *soakPhaseRecorder) summaries() []soakPhaseSummary {
	out := make([]soakPhaseSummary, 0, len(r.names))
	for _, name := range r.names {
		out = append(out, soakSummarizePhase(name, r.wall[name], r.cpu[name]))
	}
	return out
}

func soakReadHistograms() (sched, pauses *metrics.Float64Histogram) {
	samples := []metrics.Sample{{Name: "/sched/latencies:seconds"}, {Name: "/sched/pauses/total/gc:seconds"}, {Name: "/gc/pauses:seconds"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() == metrics.KindFloat64Histogram {
		sched = samples[0].Value.Float64Histogram()
	}
	for _, s := range samples[1:] {
		if s.Value.Kind() == metrics.KindFloat64Histogram {
			return sched, s.Value.Float64Histogram()
		}
	}
	return sched, nil
}

// diagnose runs the measured requests (the home page and the report page, phase by phase) for the given time while
// profiling the process, and writes the profiles and a JSON summary under diag/ of the child's directory.
func (c *soakChild) diagnose(label string, duration time.Duration) {
	t := c.t
	dir := filepath.Join(os.Getenv("PORTAL_CAPACITY_SOAK_DIR"), "diag")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cpuFile, err := os.Create(filepath.Join(dir, label+"-cpu.pprof"))
	if err != nil {
		t.Fatal(err)
	}
	defer cpuFile.Close()

	statsBefore := c.s.st.db.Stats()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	schedBefore, pausesBefore := soakReadHistograms()
	runtime.SetBlockProfileRate(10_000) // every blocking event of 10 microseconds or more
	runtime.SetMutexProfileFraction(5)
	if err := pprof.StartCPUProfile(cpuFile); err != nil {
		t.Fatal(err)
	}

	var stopSampler atomic.Bool
	samplerDone := make(chan []int, 1)
	go func() {
		var inUse []int
		for !stopSampler.Load() {
			inUse = append(inUse, c.s.st.db.Stats().InUse)
			time.Sleep(2 * time.Millisecond)
		}
		samplerDone <- inUse
	}()

	homeParts, reportParts := newSoakPhaseRecorder(), newSoakPhaseRecorder()
	homeTotal, reportTotal := newSoakPhaseRecorder(), newSoakPhaseRecorder()
	requests := map[string]int{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.LockOSThread() // thread CPU time is only meaningful for work that stays on one thread
		defer runtime.UnlockOSThread()
		deadline := time.Now().Add(duration)
		dumped := false
		for time.Now().Before(deadline) {
			if !dumped && time.Until(deadline) < duration/2 {
				if file, err := os.Create(filepath.Join(dir, label+"-goroutines.txt")); err == nil {
					_ = pprof.Lookup("goroutine").WriteTo(file, 2)
					file.Close()
				}
				dumped = true
			}
			homeTotal.time("GET /api/home", func() { c.get("/api/home") })
			c.homePhases(homeParts)
			reportTotal.time("GET /api/stock/{symbol}", func() { c.get("/api/stock/" + c.symbol) })
			c.reportPhases(reportParts)
			requests["home"]++
			requests["report"]++
			time.Sleep(20 * time.Millisecond)
		}
	}()
	<-done
	stopSampler.Store(true)
	inUse := <-samplerDone
	pprof.StopCPUProfile()

	for _, name := range []string{"block", "mutex"} {
		if file, err := os.Create(filepath.Join(dir, label+"-"+name+".pprof")); err == nil {
			_ = pprof.Lookup(name).WriteTo(file, 0)
			file.Close()
		}
	}
	runtime.SetBlockProfileRate(0)
	runtime.SetMutexProfileFraction(0)

	statsAfter := c.s.st.db.Stats()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	schedAfter, pausesAfter := soakReadHistograms()
	diagnosis := soakDiagnosis{
		Label: label, Seconds: int(duration / time.Second), Requests: requests,
		Home: homeTotal.summaries()[0], Report: reportTotal.summaries()[0],
		HomePhases: homeParts.summaries(), ReportPhases: reportParts.summaries(),
		DB: soakDiagnosisDB{
			MaxOpenConnections: statsAfter.MaxOpenConnections, WaitCount: statsAfter.WaitCount - statsBefore.WaitCount,
			WaitMillis:     float64(statsAfter.WaitDuration-statsBefore.WaitDuration) / float64(time.Millisecond),
			SaturatedShare: soakUtilization(inUse, statsAfter.MaxOpenConnections), Samples: len(inUse),
		},
		Runtime: soakDiagnosisGC{
			GCCycles: memAfter.NumGC - memBefore.NumGC, GCPauseTotalMs: float64(memAfter.PauseTotalNs-memBefore.PauseTotalNs) / 1e6,
			GCPauseP99Ms:      soakHistogramPercentile(pausesBefore, pausesAfter, 0.99) * 1000,
			SchedLatencyP50Ms: soakHistogramPercentile(schedBefore, schedAfter, 0.50) * 1000,
			SchedLatencyP99Ms: soakHistogramPercentile(schedBefore, schedAfter, 0.99) * 1000,
			Goroutines:        runtime.NumGoroutine(),
		},
	}
	raw, err := json.MarshalIndent(diagnosis, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, label+"-diagnosis.json"), append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	c.t.Logf("SOAK_DIAGNOSIS %s: home p95 wall %dus cpu %dus off-cpu %dus; report p95 wall %dus; pool waits %d (%.1f ms), connection saturated %.0f%% of %d samples; gc pause total %.1f ms, sched latency p99 %.2f ms",
		label, diagnosis.Home.Wall.P95Micros, diagnosis.Home.CPU.P95Micros, diagnosis.Home.OffCPU.P95Micros, diagnosis.Report.Wall.P95Micros,
		diagnosis.DB.WaitCount, diagnosis.DB.WaitMillis, diagnosis.DB.SaturatedShare*100, diagnosis.DB.Samples,
		diagnosis.Runtime.GCPauseTotalMs, diagnosis.Runtime.SchedLatencyP99Ms)
}

// homePhases times what GET /api/home does, in the order the handler does it, calling the same store reads.
func (c *soakChild) homePhases(r *soakPhaseRecorder) {
	user := c.session.username
	request := httptest.NewRequest(http.MethodGet, "/api/home", nil)
	var f Filters
	var sc *ownerScope
	var reps []Rep
	r.time("filtersFrom", func() { f, _, _, _ = c.s.filtersFrom(request) })
	r.time("viewerScope (SQL)", func() { sc = c.s.viewerScope(user) })
	r.time("SearchNewLatest (SQL)", func() { reps, _, _ = c.s.st.SearchNewLatest(f, sc) })
	r.time("buildGroups+collapse (CPU)", func() { _ = collapseLatestBySymbol(buildGroups(dropInternal(reps), c.s.names.Get)) })
	r.time("NewTypes (SQL)", func() { _ = c.s.st.NewTypes(sc) })
	r.time("ReportKinds (SQL)", func() { _ = c.s.st.ReportKinds(sc) })
	r.time("presentVersions (SQL)", func() { _ = c.s.presentVersions(sc) })
	r.time("LinkGroups (SQL)", func() { _ = c.s.st.LinkGroups() })
	r.time("Links (SQL)", func() { _ = c.s.st.Links() })
	r.time("KindColors (SQL)", func() { _ = c.s.st.KindColors() })
}

// reportPhases times the two reads at the top of GET /api/stock/{symbol}; the rest of that handler is the
// difference to the whole request.
func (c *soakChild) reportPhases(r *soakPhaseRecorder) {
	var sc *ownerScope
	r.time("viewerScope (SQL)", func() { sc = c.s.viewerScope(c.session.username) })
	r.time("NewBySymbol (SQL)", func() { _, _ = c.s.st.NewBySymbol(c.symbol, sc) })
}
