//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// The declared-capacity soak (doc 171, PR 4b; OPS-08, closing SEC-09) against a real root runner.
//
// The parent runs as root beside the Docker daemon and owns the runner service, exactly as the managed image
// delivery acceptance does; the child is the Portal as an unprivileged user and drives everything the soak
// measures: fifty installations (forty-two static, eight managed backends of the soak fixture image sized from
// the budgets the runner reports), the excess probes, the platform floods, and the Portal's own latency. The host
// side (memory, CPU, PIDs, log and disk sizes, container stats) is sampled by scripts/acceptance/capacity_soak.py,
// which also judges the evidence. Nothing here runs unless APP_CAPACITY_SOAK_IMAGE is set.

// platformSyncBuffer is the runner service's captured output, safe to read while the process writes it.
type platformSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *platformSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *platformSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *platformSyncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

const (
	soakChildFile  = "child-evidence.json"
	soakStaticApps = 42
)

func soakEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		t.Fatalf("%s must be a positive integer", name)
	}
	return n
}

// --- the parent ---

func TestPlatformCapacitySoak(t *testing.T) {
	image := os.Getenv("APP_CAPACITY_SOAK_IMAGE")
	if image == "" {
		t.Skip("explicit capacity soak on a disposable worker with a real runner")
	}
	fixture, socket, out := os.Getenv("APP_DATA_PORTAL_FIXTURE"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET"), os.Getenv("APP_CAPACITY_SOAK_OUT")
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" ||
		!filepath.IsAbs(fixture) || !filepath.IsAbs(socket) || !filepath.IsAbs(out) {
		t.Fatal("requires disposable Linux root, the owned Docker socket and an output directory")
	}
	minutes := soakEnvInt(t, "APP_CAPACITY_SOAK_MINUTES", 5)
	baseline := soakEnvInt(t, "APP_CAPACITY_SOAK_BASELINE_SECONDS", 30)

	store, err := journal.Open(os.Getenv("APP_DATA_JOURNAL_CONTROL"))
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := store.ControllerDeployment(context.Background())
	if closeErr := store.Close(); err != nil || closeErr != nil {
		t.Fatal("read the enrolled deployment", err, closeErr)
	}
	dir, err := os.MkdirTemp(fixture, "capacity-soak-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dir, "portal.test")
	if err := os.WriteFile(binaryPath, binary, 0555); err != nil {
		t.Fatal(err)
	}
	childData := filepath.Join(dir, "child")
	if err := os.Mkdir(childData, 0700); err != nil {
		t.Fatal(err)
	}
	// The fixtures read the descriptor templates relative to their working directory. Copy them into a tree the
	// unprivileged child can traverse, as the data endpoint acceptance does, rather than open the checkout to it.
	work := filepath.Join(dir, "app")
	descriptors := filepath.Join(dir, "appplatform", "testdata", "descriptors")
	for _, folder := range []string{work, filepath.Join(dir, "appplatform"), filepath.Join(dir, "appplatform", "testdata"), descriptors} {
		if err := os.Mkdir(folder, 0755); err != nil {
			t.Fatal(err)
		}
	}
	templates, err := os.ReadDir(os.Getenv("APP_CAPACITY_SOAK_DESCRIPTORS"))
	if err != nil || len(templates) == 0 {
		t.Fatal("descriptor templates unavailable", err)
	}
	for _, entry := range templates {
		raw, err := os.ReadFile(filepath.Join(os.Getenv("APP_CAPACITY_SOAK_DESCRIPTORS"), entry.Name()))
		if err != nil || os.WriteFile(filepath.Join(descriptors, entry.Name()), raw, 0444) != nil {
			t.Fatal("copy descriptor template", entry.Name(), err)
		}
	}
	// The soak backend's executable is the layer of the images the image store probe loads; the child reads it from here.
	soakBinaryPath := ""
	if source := os.Getenv("APP_CAPACITY_SOAK_APP_BINARY"); source != "" {
		raw, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		soakBinaryPath = filepath.Join(dir, "soak-app")
		if err := os.WriteFile(soakBinaryPath, raw, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(childData, 41001, 41001); err != nil {
		t.Fatal(err)
	}

	// The development image profile: the soak backend is a local reference image from the job's own registry.
	// The runner keeps its declared default budgets, the 8 GiB disk budget included.
	t.Setenv("APP_RUNNER_SERVICE_TEST_TIMEOUT", soakRunnerServiceTimeout(minutes, baseline))
	runner := startPlatformServiceFixture(t, binaryPath, dir, deployment, func(c *service.Config) { c.AllowLocalReferenceImages = true })
	defer func() {
		runner.stop(t)
		removePlatformServiceFixtureRoot(t, runner.root)
	}()

	// The runner's own log is sampled by size every thirty seconds, as the operator would watch its journal.
	logCtx, stopLog := context.WithCancel(context.Background())
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		file, err := os.OpenFile(filepath.Join(out, "runner-log.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return
		}
		defer file.Close()
		for tick := time.NewTicker(30 * time.Second); ; {
			line, _ := json.Marshal(map[string]int64{"at_ms": time.Now().UnixMilli(), "bytes": int64(runner.output.Len())})
			_, _ = file.Write(append(line, '\n'))
			select {
			case <-logCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	defer func() { stopLog(); <-logDone }()

	enrollment, _ := json.Marshal(deployment.ControllerEnrollment)
	timeout := fmt.Sprintf("-test.timeout=%dm", minutes+75)
	child := exec.Command(binaryPath, "-test.run=^TestPlatformCapacitySoakChild$", "-test.v", timeout)
	child.Dir = work
	child.Env = []string{
		"GOMAXPROCS=2", "PORTAL_CAPACITY_SOAK_CHILD=1", "PORTAL_CAPACITY_SOAK_DIR=" + childData,
		"PORTAL_CAPACITY_SOAK_IMAGE=" + image,
		"APP_CAPACITY_SOAK_MINUTES=" + strconv.Itoa(minutes), "APP_CAPACITY_SOAK_BASELINE_SECONDS=" + strconv.Itoa(baseline),
		"APP_CAPACITY_SOAK_DIAGNOSE=" + os.Getenv("APP_CAPACITY_SOAK_DIAGNOSE"),
		"APP_CAPACITY_SOAK_VARIANT=" + os.Getenv("APP_CAPACITY_SOAK_VARIANT"),
		"APP_CAPACITY_SOAK_IMAGE_STORE=" + os.Getenv("APP_CAPACITY_SOAK_IMAGE_STORE"),
		"APP_CAPACITY_SOAK_IMAGE_STORE_REASON=" + os.Getenv("APP_CAPACITY_SOAK_IMAGE_STORE_REASON"),
		"APP_CAPACITY_SOAK_DOCKER_FREE_BYTES=" + os.Getenv("APP_CAPACITY_SOAK_DOCKER_FREE_BYTES"),
		"PORTAL_CAPACITY_SOAK_APP_BINARY=" + soakBinaryPath,
		"PORTAL_DATA_SOCKET=" + runner.socket,
		"PORTAL_DATA_ENROLLMENT=" + base64.RawStdEncoding.EncodeToString(enrollment),
		"TMPDIR=" + childData, "HOME=" + childData,
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 41001, Gid: 41001, Groups: []uint32{41001}}}
	var output platformSyncBuffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			<-waited
		}
		// A failed or finished run keeps the child's output and the runner's, next to the evidence.
		_ = os.WriteFile(filepath.Join(out, "portal-child.log"), []byte(output.String()), 0o644)
		_ = os.WriteFile(filepath.Join(out, "runner.log"), []byte(runner.output.String()), 0o644)
		if raw, err := os.ReadFile(filepath.Join(childData, soakChildFile)); err == nil {
			_ = os.WriteFile(filepath.Join(out, soakChildFile), raw, 0o644)
		}
		// The diagnosis files (profiles, goroutine dumps, JSON), when APP_CAPACITY_SOAK_DIAGNOSE was set.
		if files, err := os.ReadDir(filepath.Join(childData, "diag")); err == nil {
			_ = os.MkdirAll(filepath.Join(out, "diag"), 0o755)
			for _, file := range files {
				if raw, err := os.ReadFile(filepath.Join(childData, "diag", file.Name())); err == nil {
					_ = os.WriteFile(filepath.Join(out, "diag", file.Name()), raw, 0o644)
				}
			}
		}
	}()
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("Portal child failed: %v\n%s", err, tail(output.String(), 16384))
		}
	case err := <-runner.done:
		runner.done <- err
		t.Fatalf("the runner service stopped during the soak: %v\n%s", err, tail(runner.output.String(), 16384))
	}
	for _, marker := range []string{"SOAK_BASELINE_MEASURED", "SOAK_INSTALLATIONS_AT_THE_CAP", "SOAK_EXCESS_PROBES_REFUSED", "SOAK_LOAD_FINISHED"} {
		if !strings.Contains(output.String(), marker) {
			t.Fatal("child assertion missing: "+marker, tail(output.String(), 16384))
		}
		t.Log(marker)
	}
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// --- the child ---

type soakChild struct {
	t        *testing.T
	ctx      context.Context
	s        *Server
	session  platformActorSession
	endpoint platformRuntimeEndpoint
	mux      *http.ServeMux
	cookie   string
	image    string
	platform string
	rec      *soakRecorder
	writable *platformInstallScheduler
	sizing   soakSizing
	budgets  soakBudgets
	managed  []string
	statics  []string
	symbol   string
	reportID int64
	nextID   int
	batch    *soakBatch
	minutes  int
	baseline int
	variant  soakVariant
}

func (c *soakChild) id() string {
	c.nextID++
	return platformFixtureID(c.nextID)
}

func (c *soakChild) audience() []platformAudienceTarget {
	return []platformAudienceTarget{{kind: "user", username: c.session.username}}
}

func TestPlatformCapacitySoakChild(t *testing.T) {
	if os.Getenv("PORTAL_CAPACITY_SOAK_CHILD") != "1" {
		t.Skip("isolated Portal child of the capacity soak only")
	}
	if os.Geteuid() != 41001 {
		t.Fatal("Portal child did not drop root")
	}
	dir := os.Getenv("PORTAL_CAPACITY_SOAK_DIR")
	minutes := soakEnvInt(t, "APP_CAPACITY_SOAK_MINUTES", 5)
	baseline := soakEnvInt(t, "APP_CAPACITY_SOAK_BASELINE_SECONDS", 30)
	encoded, err := base64.RawStdEncoding.DecodeString(os.Getenv("PORTAL_DATA_ENROLLMENT"))
	if err != nil {
		t.Fatal(err)
	}
	var enrollment journal.ControllerEnrollment
	if json.Unmarshal(encoded, &enrollment) != nil {
		t.Fatal("missing public enrollment")
	}
	// The measured Portal is production-shaped: a file-backed SQLite database opened as the server opens it (the
	// production driver's connection setup, WAL included), not the in-memory template store of the unit suite.
	variant, err := soakParseVariant(os.Getenv("APP_CAPACITY_SOAK_VARIANT"))
	if err != nil {
		t.Fatal(err)
	}
	var s *Server
	var session platformActorSession
	if variant.MemoryDB { // a diagnostic variant only
		s, session = platformTrustTestServer(t, false)
	} else {
		store, err := OpenStore("sqlite", filepath.Join(dir, "portal.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		s, session = platformActorTestServerWithStore(t, store)
		if err := s.initializeAppPlatformControl(session); err != nil {
			t.Fatal(err)
		}
	}
	// The session and its cookie must outlive the run: the actor fixture's hour would expire 55 minutes into the load
	// and every home and stock request, and the flood, would answer 401.
	session.expires = time.Unix(time.Now().Add(soakSessionLifetime(minutes, baseline)).Unix(), 0)
	platformSeedActorSession(t, s.st, session)
	platformRegisterTestPublisher(t, s, session)
	s.platformImageDir = t.TempDir()
	s.names = LoadNames(t.TempDir(), s.st)
	bindPlatformFixtureRuntime(t, s, session, enrollment)
	endpoint := platformRuntimeEndpoint{path: os.Getenv("PORTAL_DATA_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	s.platformBackendEndpoint.Store(&endpoint)
	c := &soakChild{
		t: t, ctx: context.Background(), s: s, session: session, endpoint: endpoint, image: os.Getenv("PORTAL_CAPACITY_SOAK_IMAGE"),
		rec: newSoakRecorder(filepath.Join(dir, soakChildFile)), nextID: 9000, symbol: "600000", minutes: minutes, baseline: baseline, variant: variant,
	}
	c.rec.SetSession(session.expires)
	c.rec.SetVariant(variant.Name)
	c.rec.SetPortal(soakPortalEvidence{
		Database: "file-backed SQLite opened with OpenStore, as the server opens it",
		Running: []string{"the batch scheduler (scheduleTick, and scheduleLoop every 30 seconds) with the real Dify provider against a local stub",
			"the platform relay, installs and uninstalls through the ordinary workers and the real runner"},
		NotExercised: []string{"the platform task worker (the soak's tasks are admitted with a run time an hour out, so none is due)",
			"event delivery (the source events are captured and the subscription fenced; no delivery coordinator runs)",
			"Postgres", "a backend-originated message or task request (the soak backends have no outbound channel to the platform)"},
	})

	// The budgets the runner reports in its readiness facts, not the nominal host, size the backends.
	facts, err := s.observePlatformManagedRuntime(c.ctx, session, endpoint)
	if err != nil {
		t.Fatal("authenticated runtime profile", err)
	}
	profile := facts.observation.Profile()
	c.platform = profile.Platform
	if c.budgets, err = soakBudgetsOf(profile.Capacity); err != nil {
		t.Fatal(err)
	}
	if c.budgets.RunningBackends != soakBackends {
		t.Fatalf("the runner admits %d backends, the soak fills %d", c.budgets.RunningBackends, soakBackends)
	}
	if c.sizing, err = soakBackendSizing(c.budgets.MemoryMiB, c.budgets.CPUMillis, soakBackends); err != nil {
		t.Fatal(err)
	}
	c.rec.SetBudgets(c.budgets)
	c.rec.SetSizing(c.sizing)
	t.Logf("SOAK_BUDGETS %+v sizing %+v", c.budgets, c.sizing)

	c.mux = http.NewServeMux()
	s.wireRoutes(c.mux)
	c.cookie = s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())
	c.seedPortal()

	// 1. The idle baseline, before any backend exists.
	c.measureIdle(time.Duration(baseline) * time.Second)
	t.Log("SOAK_BASELINE_MEASURED")
	if soakDiagnosing() {
		c.diagnose("idle", 60*time.Second)
	}

	// 2. The image store probe loads two images of about 4 GiB into the daemon, so it runs before any backend holds memory.
	if c.writable, err = newPlatformWritableInstallScheduler(s, endpoint, c.id(), 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	c.probeImageStore()

	// 3. Eight managed backends at the sizing the budgets give, then the excess probes that need them.
	for i := 0; i < soakBackends; i++ {
		c.managed = append(c.managed, c.installManaged(fmt.Sprintf("notes-soak-m%d", i), c.sizing.MemoryMiB, c.sizing.CPUMillis))
	}
	c.rec.SetCount("managed", len(c.managed))
	c.rec.Mark("managed_backends_running")
	c.probeBackends()
	c.probeLayers()
	c.probeStaging()

	// 4. Forty-two static installations bring the Portal to the cap; the next install is refused.
	for i := 0; i < soakStaticApps; i++ {
		c.statics = append(c.statics, c.installStatic(fmt.Sprintf("notes-soak-s%02d", i)))
	}
	c.rec.SetCount("static", len(c.statics))
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE phase<>'removed'`); got != platformInstallationCap {
		t.Fatalf("%d installations, want %d", got, platformInstallationCap)
	}
	c.probeInstallationCap()
	t.Log("SOAK_INSTALLATIONS_AT_THE_CAP")

	// 5. The task and event floods, then the load.
	c.floodQuotas()
	if failed := soakFailedProbes(c.rec.Snapshot().Probes); len(failed) > 0 {
		t.Fatalf("excess requests not refused with their typed error: %v", failed)
	}
	t.Log("SOAK_EXCESS_PROBES_REFUSED")
	c.load(minutes)
	t.Log("SOAK_LOAD_FINISHED")
}

func soakBudgetsOf(c transport.ManagedRuntimeCapacity) (soakBudgets, error) {
	var b soakBudgets
	for _, field := range []struct {
		raw  string
		into *int64
	}{{c.MemoryMiB, &b.MemoryMiB}, {c.CPUMillis, &b.CPUMillis}, {c.DiskBytes, &b.DiskBytes}, {c.ImageStoreBytes, &b.ImageStoreBytes},
		{c.RunningBackends, &b.RunningBackends}, {c.EstimatedRows, &b.EstimatedRows}} {
		n, err := strconv.ParseInt(field.raw, 10, 64)
		if err != nil {
			return soakBudgets{}, fmt.Errorf("the runner reported no usable capacity budgets: %q", field.raw)
		}
		*field.into = n
	}
	return b, nil
}

// --- the Portal's own reads, and the queue ---

// seedPortal adds one report and the batch queue: the real scheduler against a stub Dify, with a standing backlog of
// queued jobs the dispatch decision is measured over and soakBatchHeld long runs that hold the budget's slots, so the pages
// and the queue have rows to read and the scheduler has real work to do.
func (c *soakChild) seedPortal() {
	t := c.t
	body := strings.Repeat("Soak report paragraph with enough text to be a realistic page.\n\n", 200)
	id, _, err := c.s.st.UpsertReport(Rep{Symbol: c.symbol, Name: "Soak Co", Date: "2026-11-13", RType: predTestRtype, Title: "soak report", MD: body})
	if err != nil {
		t.Fatal(err)
	}
	c.reportID = id
	c.batch = newSoakBatch(t, c.s, c.session.username)
	c.batch.seed(soakQueueBacklog)
	go c.s.scheduleLoop() // the 30 second tick of the running server
	if code, _ := c.get("/api/home"); code != http.StatusOK {
		t.Fatalf("home page: %d", code)
	}
	if code, _ := c.get("/api/stock/" + c.symbol); code != http.StatusOK {
		t.Fatalf("report page: %d", code)
	}
}

func (c *soakChild) get(path string) (int, time.Duration) {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.AddCookie(&http.Cookie{Name: cookieName, Value: c.cookie})
	recorder := httptest.NewRecorder()
	began := time.Now()
	c.mux.ServeHTTP(recorder, request)
	return recorder.Code, time.Since(began)
}

// dispatch is the queue's admission decision over the whole backlog: the multifactor priorities of every waiting
// job and the choice of the ones to start. It is the cost the batch queue pays to dispatch, measured where the
// existing queue measurement measures it (queuedItems), not the time a Dify run takes.
func (c *soakChild) dispatch(limit int) time.Duration {
	began := time.Now()
	if limit == 0 {
		queue.Admit(c.s.queuedItems(), 0, queue.Plan{Budget: 4, Reserved: 1})
	} else {
		queue.Admit(soakQueuedItems(c.s, limit), 0, queue.Plan{Budget: 4, Reserved: 1})
	}
	return time.Since(began)
}

// sampleOnce takes one of each latency sample: the two pages, the queue's dispatch decision over the deep and the shallow
// backlog and, on every soakBatchProbeEvery-th call, one batch job from enqueue to its start at the stub Dify.
func (c *soakChild) sampleOnce(into *soakSamples, iteration int) {
	if code, took := c.get("/api/home"); code == http.StatusOK {
		into.home = append(into.home, took)
	} else {
		c.t.Errorf("home page under measurement: %d", code)
	}
	if code, took := c.get("/api/stock/" + c.symbol); code == http.StatusOK {
		into.report = append(into.report, took)
	} else {
		c.t.Errorf("report page under measurement: %d", code)
	}
	into.dispatch = append(into.dispatch, c.dispatch(0))
	into.dispatchShallow = append(into.dispatchShallow, c.dispatch(soakQueueBacklogShallow))
	if c.variant.BatchProbes && soakBatchProbeDue(iteration) {
		if took, err := c.batch.probe(); err != nil {
			c.t.Errorf("batch probe under measurement: %v", err)
		} else {
			into.batchStart = append(into.batchStart, took)
		}
	}
}

// sampleWindow spreads n samples evenly across span from start (one every 600 ms for a minute of 100) and returns when
// the span is over. A sample that finds the clock past its slot runs at once; nothing is dropped.
func (c *soakChild) sampleWindow(start time.Time, span time.Duration, n int) soakSamples {
	var samples soakSamples
	for i, at := range soakSampleTimes(start, span, n) {
		time.Sleep(time.Until(at))
		c.sampleOnce(&samples, i)
	}
	time.Sleep(time.Until(start.Add(span)))
	return samples
}

func (s soakSamples) window(minute int, start time.Time, end time.Time) soakWindow {
	return soakWindow{Minute: minute, At: end.UnixMilli(), StartedMS: start.UnixMilli(), EndedMS: end.UnixMilli(), Home: soakSummarize(s.home), Report: soakSummarize(s.report),
		Dispatch: soakSummarize(s.dispatch), DispatchShallow: soakSummarize(s.dispatchShallow), BatchStart: soakSummarize(s.batchStart)}
}

func (s soakSamples) appended(more soakSamples) soakSamples {
	s.home, s.report, s.dispatch = append(s.home, more.home...), append(s.report, more.report...), append(s.dispatch, more.dispatch...)
	s.dispatchShallow, s.batchStart = append(s.dispatchShallow, more.dispatchShallow...), append(s.batchStart, more.batchStart...)
	return s
}

// measureIdle measures the baseline the same way the load is measured: samples spread evenly across one-minute windows, the
// batch probes included, the whole idle period pooled.
func (c *soakChild) measureIdle(duration time.Duration) {
	var pooled soakSamples
	perSample := time.Minute / soakSamplesPerWindow
	for began, index := time.Now(), 0; time.Since(began) < duration; index++ {
		start := time.Now()
		span := time.Minute
		if left := duration - time.Since(began); left < span {
			span = left
		}
		one := c.sampleWindow(start, span, int(span/perSample))
		pooled = pooled.appended(one)
		c.rec.AddIdleWindow(one.window(index, start, time.Now()))
	}
	c.rec.SetIdle(pooled.window(0, time.Now(), time.Now()))
}

// --- installations ---

// stageManaged signs and stages a managed release of the soak fixture image with the given declared resources.
func (c *soakChild) stageManaged(app string, memoryMiB, cpuMillis int) platformStagedRelease {
	raw, archive := platformManagedReleaseFixture(c.t, c.platform)
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		c.t.Fatal(err)
	}
	m.ID = app
	m.Backend.Image = c.image
	m.Backend.Resources = appplatform.Resources{MemoryMiB: memoryMiB, CPUMillis: cpuMillis, PIDs: 64}
	// Required capabilities of this kind are granted by the install review itself: the real grant path.
	m.Capabilities.Required = []string{"background.run", "events.subscribe", "reports.read", "tasks.schedule"}
	raw, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	release, err := c.s.stagePlatformRelease(c.session, raw, platformSignTestDescriptor(c.t, raw), archive)
	if err != nil {
		c.t.Fatal("stage "+app, err)
	}
	return release
}

// installManaged runs the ordinary writable install worker to completion for one managed backend.
func (c *soakChild) installManaged(app string, memoryMiB, cpuMillis int) string {
	installation, code := c.installManagedWithCode(app, memoryMiB, cpuMillis)
	if code != "" {
		c.t.Fatalf("install %s ended with %q", app, code)
	}
	return installation
}

// installManagedWithCode runs the same worker until the operation ends: the second result is its error code,
// empty when the install completed.
func (c *soakChild) installManagedWithCode(app string, memoryMiB, cpuMillis int) (string, string) {
	release := c.stageManaged(app, memoryMiB, cpuMillis)
	return c.installStagedRelease(app, release.sha256, release.trustRevision)
}

// installStagedRelease reviews, admits and drives the install of an already staged managed release.
func (c *soakChild) installStagedRelease(app, sha string, trust int64) (string, string) {
	t := c.t
	review, err := c.s.createPlatformWritableInstallReview(c.ctx, c.session, sha, trust, c.audience(), c.endpoint)
	if err != nil {
		t.Fatal("review "+app, err)
	}
	committed, err := c.s.consumePlatformWritableInstallReview(c.ctx, c.session, review.ID, review.SHA256, c.id(), c.endpoint)
	if err != nil {
		t.Fatal("admit "+app, err)
	}
	return committed.InstallationID, c.drive(committed.OperationID, 12*time.Minute)
}

// installOffline imports a release as an administrator does, through the offline import route, and installs it.
func (c *soakChild) installOffline(app string, parts platformManagedImageParts) (installation, code string) {
	response := platformOfflineRequest(c.mux, c.cookie, "application/zip", parts.packageBytes(c.t, nil, ""))
	if response.Code != http.StatusOK {
		c.t.Fatalf("offline import of %s answered %d: %.300s", app, response.Code, response.Body.String())
	}
	sha := platformDigest(parts.descriptor)
	trust := scalar[int64](c.t, c.s.st, `SELECT trust_revision FROM app_platform_releases WHERE sha256=?`, sha)
	return c.installStagedRelease(app, sha, trust)
}

// drive ticks the writable install worker until the operation completes (empty code) or ends with an error code.
func (c *soakChild) drive(operation string, limit time.Duration) string {
	deadline := time.Now().Add(limit)
	ctx, cancel := context.WithDeadline(c.ctx, deadline)
	defer cancel()
	var lastTick error
	for time.Now().Before(deadline) {
		if err := c.writable.tick(ctx); err != nil {
			lastTick = err
		}
		if scalar[string](c.t, c.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) == "complete" {
			return ""
		}
		var code sql.NullString
		if err := c.s.st.queryRow(`SELECT error_code FROM app_platform_operations WHERE id=?`, operation).Scan(&code); err != nil {
			c.t.Fatal(err)
		}
		if code.Valid && code.String != "" {
			c.t.Logf("operation %s ended with %s; last worker error: %v; status %+v\n%s", operation, code.String, lastTick, c.writable.currentStatus(), c.effectSummary(operation))
			return code.String
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "timeout"
}

// effectSummary lists the operation's effects with their state, so a failed install says where it stopped.
func (c *soakChild) effectSummary(operation string) string {
	rows, err := c.s.st.query(`SELECT stage,kind,state,receipt_json FROM app_platform_operation_effects WHERE operation_id=? ORDER BY stage,effect_number`, operation)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var out strings.Builder
	for rows.Next() {
		var stage, kind, state, receipt string
		if rows.Scan(&stage, &kind, &state, &receipt) == nil {
			fmt.Fprintf(&out, "  effect %s/%s %s %.300s\n", stage, kind, state, receipt)
		}
	}
	return out.String()
}

func (c *soakChild) stageStatic(app string) platformStagedRelease {
	raw, _, archive := platformSignedReleaseFixture(c.t, "1.0.0-beta.1")
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		c.t.Fatal(err)
	}
	m.ID = app
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Capabilities.Required = []string{}
	raw, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	release, err := c.s.stagePlatformRelease(c.session, raw, platformSignTestDescriptor(c.t, raw), archive)
	if err != nil {
		c.t.Fatal("stage "+app, err)
	}
	return release
}

func (c *soakChild) installStatic(app string) string {
	t := c.t
	release := c.stageStatic(app)
	review, err := c.s.createPlatformFrontendInstallReview(c.session, release.sha256, release.trustRevision, c.audience())
	if err != nil {
		t.Fatal("review "+app, err)
	}
	admitted, err := c.s.consumePlatformInstallReview(c.session, review.ID, review.SHA256, c.id())
	if err != nil {
		t.Fatal("admit "+app, err)
	}
	lease, err := c.s.st.claimPlatformOperation(admitted.OperationID, c.id())
	if err != nil {
		t.Fatal("claim "+app, err)
	}
	if _, err := c.s.runPlatformStaticInstall(c.ctx, lease, platformRuntimeEndpoint{}); err != nil {
		t.Fatal("install "+app, err)
	}
	return admitted.InstallationID
}

// retire takes a refused probe installation out of the count and its data namespace out of the runner's disk
// reservation through the ordinary managed uninstall, which removes a capacity-refused install that never started.
// There is no other path: a refusal of the uninstall is returned, and the soak fails on it.
func (c *soakChild) retire(installation string) error {
	ctx := c.ctx
	removal, err := c.s.createPlatformRemovalReview(c.session, installation, appplatform.ActionUninstall)
	if err != nil {
		return fmt.Errorf("uninstall review refused: %w", err)
	}
	commit, err := c.s.consumePlatformRemovalReview(c.session, removal.ID, removal.SHA256, c.id())
	if err != nil {
		return fmt.Errorf("uninstall admission refused: %w", err)
	}
	claim, err := c.s.claimNextPlatformManagedUninstall(ctx, c.id())
	if err != nil || claim.lease.id != commit.OperationID {
		return fmt.Errorf("uninstall claim refused: %v", err)
	}
	run, err := c.s.runPlatformManagedUninstall(ctx, claim.lease, c.endpoint)
	if err != nil || run.lease.snapshot.Stage != appplatform.StageComplete {
		return fmt.Errorf("uninstall did not complete: %v", err)
	}
	if phase := scalar[string](c.t, c.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, installation); phase != "removed" {
		return fmt.Errorf("the installation is %q after its uninstall", phase)
	}
	return nil
}

// --- the excess probes ---

// probeBackends starts the three backends the eight leave no room for. Each is refused by the runner with the
// dimension it overflows, which the Portal keeps as the operation's error code.
func (c *soakChild) probeBackends() {
	for _, p := range soakBackendProbes(c.sizing) {
		app := "notes-soak-p-" + strings.ReplaceAll(p.Name, "_", "-")
		installation, code := c.installManagedWithCode(app, p.MemoryMiB, p.CPUMillis)
		retired, retiredBy := c.retire(installation), "managed_uninstall"
		detail := fmt.Sprintf("requested %d MiB and %d millis against %d MiB and %d millis remaining", p.MemoryMiB, p.CPUMillis, c.sizing.RemainingMemoryMiB, c.sizing.RemainingCPUMillis)
		if retired != nil {
			// A refused uninstall leaves the namespace reserved: the soak records the refusal and fails.
			retiredBy, detail = "refused", detail+"; retirement: "+retired.Error()
		}
		c.t.Logf("SOAK_PROBE_RETIRED %s: %s (code %s)", p.Name, retiredBy, code)
		c.rec.AddProbe(soakProbeResult{Name: p.Name, Expected: "capacity_exhausted_" + p.Dimension, Observed: code, Level: soakProbeNative, Retired: retiredBy, Detail: detail})
		if retired != nil {
			c.t.Fatalf("the refused install %s could not be uninstalled: %v", p.Name, retired)
		}
	}
}

// probeLayers stages a crafted package whose first layer decompresses beyond its declaration. The offline import
// route refuses it (the same check the runner repeats on every blob it receives) and stores nothing.
func (c *soakChild) probeLayers() {
	t := c.t
	over := newPlatformManagedImageParts(t, "3.1.0", "soak-over")
	var d map[string]any
	if err := json.Unmarshal(over.descriptor, &d); err != nil {
		t.Fatal(err)
	}
	layer := d["backend"].(map[string]any)["images"].([]any)[0].(map[string]any)["layers"].([]any)[0].(map[string]any)
	layer["uncompressed_bytes"] = layer["uncompressed_bytes"].(float64) - 1
	descriptor, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	over.descriptor, over.signature = descriptor, platformSignTestDescriptor(t, descriptor)
	before := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_releases`)
	response := platformOfflineRequest(c.mux, c.cookie, "application/zip", over.packageBytes(t, nil, ""))
	observed := ""
	if response.Code == http.StatusBadRequest && strings.Contains(response.Body.String(), "app_platform_invalid_request") &&
		scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_releases`) == before {
		observed = "app_platform_invalid_request"
	}
	c.rec.AddProbe(soakProbeResult{Name: "layer_beyond_its_declaration", Expected: "app_platform_invalid_request", Observed: observed, Level: soakProbeNative,
		Detail: fmt.Sprintf("the Portal's offline import route answered %d and stored no release; the runner's own refusal of the same bytes (uncompressed_bound) is proven by the blob channel unit tests, not in this run", response.Code)})
}

// probeImageStore refuses a third image of about 4 GiB against the runner's 10 GiB image store, natively when the daemon's
// disk can hold two such images (the harness measures it and says so), else at unit level with the measured reason.
func (c *soakChild) probeImageStore() {
	unitTests := []string{"TestLoadImageReservesTheDeclaredBytesOnce", "TestLoadImageRefusedRunsNothingAndReplays", "TestAdmissionRefusesEachDimensionByOneUnit"}
	free, _ := strconv.ParseInt(os.Getenv("APP_CAPACITY_SOAK_DOCKER_FREE_BYTES"), 10, 64)
	evidence := soakImageStoreEvidence{Mode: os.Getenv("APP_CAPACITY_SOAK_IMAGE_STORE"), Reason: os.Getenv("APP_CAPACITY_SOAK_IMAGE_STORE_REASON"), DockerFreeBytes: free}
	binaryPath := os.Getenv("PORTAL_CAPACITY_SOAK_APP_BINARY")
	if evidence.Mode == "native" && binaryPath == "" {
		evidence.Mode, evidence.Reason = "unit", "the harness gave no soak backend binary to build the images from"
	}
	if evidence.Mode != "native" {
		c.rec.SetImageStore(evidence)
		c.rec.AddProbe(soakProbeResult{Name: "image_beyond_the_image_store", Expected: "capacity_exhausted_image_store", Level: soakProbeUnit, Tests: unitTests,
			Detail: "unit level: " + evidence.Reason})
		return
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		c.t.Fatal(err)
	}
	_, _, binaryTar, err := soakBinaryLayer(binary)
	if err != nil {
		c.t.Fatal(err)
	}
	fillers := soakFillerFileBytes(binaryTar)
	// The two images that load ask for one MiB more memory than the runner has, so each start is refused after the load: the
	// 4 GiB images are loaded and reserved but no container is ever created from them, and the installs retire like any
	// other refused probe.
	memory, err := soakImageProbeMemoryMiB(c.budgets.MemoryMiB)
	if err != nil {
		c.t.Fatal(err)
	}
	resources := appplatform.Resources{MemoryMiB: memory, CPUMillis: 100, PIDs: 64}
	var installed []string
	codes := map[string]string{}
	var third string
	// Two images load into the daemon (each declares about 4 GiB of zero-filled layers; the blobs that cross are a few MiB) and
	// their starts are refused for memory; the third is the one the 10 GiB store refuses at its load, before a byte of it is
	// written.
	for i, seed := range []string{"a", "b", "c"} {
		app := "notes-soak-i-" + seed
		began := time.Now()
		parts := soakImageRelease(c.t, app, seed, c.platform, binary, fillers, resources)
		installation, code := c.installOffline(app, parts)
		installed = append(installed, installation)
		codes[app] = code
		c.t.Logf("SOAK_IMAGE_STORE_PROBE_INSTALL %s: code %q after %v", app, code, time.Since(began).Round(time.Second))
		if i == 2 {
			third = code
		}
	}
	// The third image's installation is retired like every refused probe; the two that loaded are ordinary installs and are
	// uninstalled through the same managed removal.
	retiredBy, detail := "managed_uninstall", fmt.Sprintf("install codes %v; declared bytes of an image %d against the 10 GiB store", codes, soakImageDeclaredTarget)
	for _, installation := range installed {
		if err := c.retire(installation); err != nil {
			retiredBy, detail = "refused", detail+"; retirement: "+err.Error()
			break
		}
	}
	settled, reclaimErr := c.s.reclaimPlatformImages(c.ctx, c.endpoint)
	detail += fmt.Sprintf("; image reclaim after the uninstalls settled %d (error %v)", settled, reclaimErr)
	evidence.Detail = detail
	c.rec.SetImageStore(evidence)
	c.rec.AddProbe(soakProbeResult{Name: "image_beyond_the_image_store", Expected: "capacity_exhausted_image_store", Observed: third, Level: soakProbeNative, Retired: retiredBy, Detail: detail})
	if retiredBy != "managed_uninstall" {
		c.t.Fatalf("the image store probe installations could not be uninstalled: %s", detail)
	}
}

// probeStaging is a store-level probe of the 2 GiB staging cap, not a drive of the upload route. It reserves 384 MiB at a
// time (the most one offline upload may hold) against the real staging store of this Portal until the sixth is refused, and
// maps the refusal through the real management error handler, which answers the typed 507 storage-full. No byte is written.
// The route itself holds at most two uploads at once, 768 MiB, so no sequence of route requests reaches the cap without
// writing gigabytes of unpinned packages; the exact boundary of the production value is proven by the unit tests.
func (c *soakChild) probeStaging() {
	store, err := c.s.platformImages()
	if err != nil {
		c.t.Fatal(err)
	}
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	observed := ""
	for i := 0; i < 8 && observed == ""; i++ {
		release, err := store.reserveStaging(appplatform.MaxManagedOfflineEnvelopeBytes, "")
		if err == nil {
			releases = append(releases, release)
			continue
		}
		recorder := httptest.NewRecorder()
		platformManagementError(recorder, err)
		if recorder.Code == http.StatusInsufficientStorage && strings.Contains(recorder.Body.String(), "app_platform_storage_full") {
			observed = "app_platform_storage_full"
		}
	}
	c.rec.AddProbe(soakProbeResult{Name: "staging_over_two_gib", Expected: "app_platform_storage_full", Observed: observed, Level: soakProbeNative,
		Detail: fmt.Sprintf("store level, not through the upload route: %d reservations of 384 MiB against a cap of %d bytes, the refusal mapped by platformManagementError", len(releases), store.stagingCap)})
}

func (c *soakChild) probeInstallationCap() {
	release := c.stageStatic("notes-soak-s99")
	_, err := c.s.createPlatformFrontendInstallReview(c.session, release.sha256, release.trustRevision, c.audience())
	reviewRefused := errors.Is(err, errPlatformInstallationCapacity)
	body := fmt.Sprintf(`{"action":"install","release_sha256":%q,"trust_revision":%q,"audience":[{"type":"user","username":%q}]}`,
		release.sha256, strconv.FormatInt(release.trustRevision, 10), c.session.username)
	response := sessionTestRequest(c.mux, http.MethodPost, "/api/admin/app-platform/plans", c.cookie, body)
	observed := ""
	if reviewRefused && response.Code == http.StatusTooManyRequests && strings.Contains(response.Body.String(), `"installation_capacity"`) {
		observed = "installation_capacity"
	}
	c.rec.AddProbe(soakProbeResult{Name: "fifty_first_installation", Expected: "installation_capacity", Observed: observed, Level: soakProbeNative,
		Detail: fmt.Sprintf("review error refused=%t, HTTP %d, %d installations", reviewRefused, response.Code, scalar[int](c.t, c.s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE phase<>'removed'`))})
}

// --- the platform floods ---

func (c *soakChild) activeTasks() int {
	return scalar[int](c.t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE state NOT IN ('succeeded','failed','cancelled')`)
}

func (c *soakChild) admit(installation string, n int, prefix string) error {
	now, err := platformDatabaseNow(c.s.st.poolExec())
	if err != nil {
		c.t.Fatal(err)
	}
	_, _, err = c.s.admitPlatformTask(c.ctx, installation, platformTaskAdmission{
		OccurrenceID: fmt.Sprintf("%s:%d", prefix, n), Handler: appplatform.TaskHandlerBackend, Payload: []byte(fmt.Sprintf(`{"soak":%d}`, n)),
		RunAtMS: now + int64(time.Hour/time.Millisecond), ExpiresMS: now + int64(2*time.Hour/time.Millisecond),
	})
	return err
}

// floodQuotas drives the event subscription to its in-flight bound with a flood of source events, each of seven managed
// installations to its 256 task quota, and the whole Portal to its 10000 global quota. Task admissions are calls of
// admitPlatformTask in the Portal process: the function the backend task route runs after it has verified the backend's service
// identity. The soak backends have no outbound channel to the platform, so no admission comes from a backend. The global quota
// is reached with a backlog of waiting tasks attributed to the static installations (they cannot schedule tasks themselves:
// they have no backend), inserted with the columns the admission itself writes; the refusals are the real admission.
func (c *soakChild) floodQuotas() {
	t := c.t
	// Event quota: one subscription that allows one delivery in flight is flooded with source events. The first is delivered
	// (one task), the second finds the bound reached and fences the subscription for review, and the rest are captured and kept.
	now, err := platformDatabaseNow(c.s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	subscription, created, err := c.s.createPlatformEventSubscription(c.ctx, c.managed[0], appplatform.EventSubscriptionSpec{
		EventType: appplatform.EventReportIngested, ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10), MaxInflight: 1})
	if err != nil || !created {
		t.Fatalf("event subscription: %s %t %v", subscription, created, err)
	}
	const sourceEvents = 64
	for i := 0; i < sourceEvents; i++ {
		event := NotificationEvent{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", 8300+i), SourceKey: fmt.Sprintf("soak:%d", i), Type: appplatform.EventReportIngested,
			Payload: `{"id":1}`, ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10), CreatedAt: now}
		if err := c.s.st.CaptureNotification(event, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	state := scalar[string](t, c.s.st, `SELECT state FROM app_platform_event_subscriptions WHERE id=?`, subscription)
	kept := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM notification_events WHERE source_key LIKE 'soak:%'`)
	deliveries := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND handler=?`, c.managed[0], appplatform.TaskHandlerEvent)
	reasons := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.event_review_required' AND target_id=? AND detail LIKE '%inflight_limit%'`, subscription)
	eventObserved := ""
	if state == "review_required" && kept == sourceEvents && deliveries == 1 && reasons == 1 {
		eventObserved = "review_required:inflight_limit"
	}
	c.rec.AddProbe(soakProbeResult{Name: "event_subscription_over_quota", Expected: "review_required:inflight_limit", Observed: eventObserved, Level: soakProbeNative,
		Detail: fmt.Sprintf("%d source events against a subscription that allows one delivery in flight: state %q, %d source events kept, %d delivery task, %d audit entries naming inflight_limit", sourceEvents, state, kept, deliveries, reasons)})

	// Per-installation quota on seven installations; the eighth is filled halfway so the global quota is the one
	// that refuses last.
	filled := 0
	for index, installation := range c.managed {
		limit := appplatform.MaxInstallationActiveTasks
		if index == len(c.managed)-1 {
			limit = 100
		}
		have := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
		for n := have; n < limit; n++ {
			if err := c.admit(installation, n, "fill"); err != nil {
				t.Fatalf("admission %d of installation %d: %v", n, index, err)
			}
		}
		if index < len(c.managed)-1 {
			if err := c.admit(installation, limit, "over"); errors.Is(err, errAppPlatformTaskQuota) {
				filled++
			}
		}
	}
	quotaObserved := ""
	if filled == len(c.managed)-1 {
		quotaObserved = "task_capacity"
	}
	c.rec.AddProbe(soakProbeResult{Name: "task_over_installation_quota", Expected: "task_capacity", Observed: quotaObserved, Level: soakProbeNative,
		Detail: fmt.Sprintf("in-process admitPlatformTask (the function the backend task route calls after verifying the service identity): %d installations refused the 257th active task", filled)})

	// Global quota: a backlog up to one task short of the quota, then one more admitted and the next refused.
	c.seedBacklog(appplatform.MaxGlobalActiveTasks - 1)
	last := c.managed[len(c.managed)-1]
	admitted := c.admit(last, 1000, "global-last")
	refused := c.admit(last, 1001, "global-over")
	perInstallation := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, last)
	observed := ""
	if admitted == nil && errors.Is(refused, errAppPlatformTaskQuota) && c.activeTasks() == appplatform.MaxGlobalActiveTasks && perInstallation < appplatform.MaxInstallationActiveTasks {
		observed = "task_capacity"
	}
	c.rec.AddProbe(soakProbeResult{Name: "task_over_global_quota", Expected: "task_capacity", Observed: observed, Level: soakProbeNative,
		Detail: fmt.Sprintf("in-process admitPlatformTask: %d active tasks in all, %d on the installation that was refused, so only the global quota can have refused it", c.activeTasks(), perInstallation)})
	// The per-application quota (1000) needs four installations of one application, and the cap of fifty holds
	// fifty applications: it is proven at its boundary by TestTaskQuotasRefuseAtTheirOwnBoundary.
	c.rec.AddProbe(soakProbeResult{Name: "task_over_application_quota", Expected: "task_capacity", Level: soakProbeUnit, Tests: []string{"TestTaskQuotasRefuseAtTheirOwnBoundary"},
		Detail: "unit level: appplatform.AdmitTask refuses at 1000 active tasks of one application independently of the other two quotas; no application has more than one installation in this soak, so it is not observed here"})
	c.rec.SetFlood("active_tasks", c.activeTasks())
}

// seedBacklog inserts waiting tasks for the static installations until the Portal holds target active tasks.
func (c *soakChild) seedBacklog(target int) {
	t := c.t
	missing := target - c.activeTasks()
	if missing <= 0 {
		return
	}
	now := time.Now().UnixMilli()
	tx, err := c.s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	query := c.s.st.bind(`INSERT INTO app_platform_tasks(
		id,installation_id,app_id,publisher_id,occurrence_id,handler,payload,payload_sha256,
		state,generation,policy_revision,release_sha256,controller_epoch,attempts,max_attempts,
		fence,revision,worker_id,lease_id,lease_until_ms,next_attempt_ms,expires_ms,
		error_code,result_json,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,'waiting',?,?,?,?,0,?,1,1,NULL,NULL,NULL,?,?,NULL,NULL,NULL,?,?)`)
	payload := []byte(`{"backlog":1}`)
	for n := 0; n < missing; n++ {
		installation := c.statics[n%len(c.statics)]
		var app, publisher, epoch string
		var generation, policy int64
		if err := tx.QueryRow(c.s.st.bind(`SELECT app_id,publisher_id,generation,policy_revision,controller_epoch FROM app_platform_installations WHERE id=?`), installation).
			Scan(&app, &publisher, &generation, &policy, &epoch); err != nil {
			t.Fatal(err)
		}
		var release string
		if err := tx.QueryRow(c.s.st.bind(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`), installation).Scan(&release); err != nil {
			t.Fatal(err)
		}
		id, err := platformNewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(query, id, installation, app, publisher, fmt.Sprintf("backlog:%d", n), appplatform.TaskHandlerBackend, payload, platformDigest(payload),
			generation, policy, release, epoch, platformTaskMaxAttempts, now+int64(time.Hour/time.Millisecond), now+int64(2*time.Hour/time.Millisecond), now, now); err != nil {
			t.Fatal("seed backlog", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// backendStatuses asks every soak backend what its loads have done, through the same signed request path the
// flood uses.
func (c *soakChild) backendStatuses() []soakBackendStatus {
	statuses := make([]soakBackendStatus, 0, len(c.managed))
	for _, installation := range c.managed {
		status := soakBackendStatus{Installation: installation}
		// A backend throttled to its CPU share can miss one request's deadline; it gets a few tries.
		for attempt := 0; attempt < 8 && !status.Answered; attempt++ {
			if response, err := c.s.requestPlatformWritableBackend(c.ctx, c.session, installation, http.MethodGet, "/v1/soak/status", nil, c.endpoint); err == nil && response.Status == http.StatusOK {
				status = soakParseBackendStatus(installation, response.Body)
			} else {
				time.Sleep(300 * time.Millisecond)
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// --- the load ---

// soakFloodTarget is one backend the flood process drives through the Portal's HTTP surface.
type soakFloodTarget struct {
	URL  string `json:"url"`
	Body string `json:"body"`
}

// TestPlatformCapacitySoakFlood is the flood generator, a process of its own: it sends signed backend requests
// through the Portal's real HTTP route, one attempt in flight per backend, and counts the outcomes by class in a file it
// rewrites once a second. The relay admits at most two requests at once Portal-wide (platformDocumentBudget), so the
// attempts the Portal refuses answer 429 app_platform_busy; that is the production admission behaviour and it is counted
// as such. The Portal process whose latency is measured therefore carries the platform's work for those requests and none
// of the generator's. It does nothing unless the soak child started it.
func TestPlatformCapacitySoakFlood(t *testing.T) {
	if os.Getenv("PORTAL_CAPACITY_SOAK_FLOOD") != "1" {
		t.Skip("flood process of the capacity soak only")
	}
	var targets []soakFloodTarget
	if err := json.Unmarshal([]byte(os.Getenv("PORTAL_CAPACITY_SOAK_FLOOD_TARGETS")), &targets); err != nil || len(targets) == 0 {
		t.Fatal("no flood targets", err)
	}
	cookie, out := os.Getenv("PORTAL_CAPACITY_SOAK_FLOOD_COOKIE"), os.Getenv("PORTAL_CAPACITY_SOAK_FLOOD_OUT")
	var mu sync.Mutex
	counters := soakFloodCounters{Classes: map[string]int64{}}
	record := func(class string) {
		mu.Lock()
		defer mu.Unlock()
		counters.Requests++
		counters.Classes[class]++
	}
	client := &http.Client{Timeout: 25 * time.Second}
	for _, target := range targets {
		go func(target soakFloodTarget) {
			for {
				status, class := 0, ""
				var body []byte
				request, err := http.NewRequest(http.MethodPost, target.URL, strings.NewReader(target.Body))
				if err == nil {
					request.Header.Set("Content-Type", "application/json")
					request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
					var response *http.Response
					if response, err = client.Do(request); err == nil {
						status = response.StatusCode
						body, _ = io.ReadAll(io.LimitReader(response.Body, 4096))
						_, _ = io.Copy(io.Discard, response.Body)
						response.Body.Close()
					}
				}
				class = soakFloodClass(status, body, err)
				record(class)
				if class == soakFloodOK {
					continue
				}
				time.Sleep(50 * time.Millisecond)
			}
		}(target)
	}
	deadline := time.Now().Add(time.Duration(soakEnvInt(t, "PORTAL_CAPACITY_SOAK_FLOOD_MINUTES", 1)) * time.Minute)
	for time.Now().Before(deadline) {
		mu.Lock()
		snapshot := soakFloodCounters{Requests: counters.Requests, Classes: map[string]int64{}, HeartbeatMS: time.Now().UnixMilli()}
		for class, count := range counters.Classes {
			snapshot.Classes[class] = count
		}
		mu.Unlock()
		raw, _ := json.Marshal(snapshot)
		_ = os.WriteFile(out+".tmp", raw, 0o644)
		_ = os.Rename(out+".tmp", out)
		time.Sleep(time.Second)
	}
}

// startFlood serves the Portal's routes on a loopback listener and starts the flood process against them. alive reports whether
// the flood process is still running.
func (c *soakChild) startFlood(minutes int) (stop func(), read func() soakFloodCounters, alive func() bool) {
	t := c.t
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: c.mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	var targets []soakFloodTarget
	body := `{"soak":"` + strings.Repeat("m", 2048) + `"}`
	for _, installation := range c.managed {
		var release string
		var policy int64
		if err := c.s.st.queryRow(`SELECT a.release_sha256,i.policy_revision FROM app_platform_activations a JOIN app_platform_installations i ON i.id=a.installation_id
			WHERE a.installation_id=? AND a.state='active'`, installation).Scan(&release, &policy); err != nil {
			t.Fatal(err)
		}
		request, _ := json.Marshal(map[string]any{
			"binding": platformBackendRequestBinding{Generation: "1", PolicyRevision: strconv.FormatInt(policy, 10), ReleaseSHA256: release},
			"method":  "POST", "path": "/v1/echo", "body": body})
		targets = append(targets, soakFloodTarget{URL: "http://" + listener.Addr().String() + "/api/app-platform/installations/" + installation + "/backend", Body: string(request)})
	}
	rawTargets, _ := json.Marshal(targets)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(os.Getenv("TMPDIR"), "flood-counters.json")
	flood := exec.Command(executable, "-test.run=^TestPlatformCapacitySoakFlood$", "-test.v", fmt.Sprintf("-test.timeout=%dm", minutes+10))
	flood.Env = []string{"GOMAXPROCS=1", "PORTAL_CAPACITY_SOAK_FLOOD=1", "PORTAL_CAPACITY_SOAK_FLOOD_TARGETS=" + string(rawTargets),
		"PORTAL_CAPACITY_SOAK_FLOOD_COOKIE=" + c.cookie, "PORTAL_CAPACITY_SOAK_FLOOD_OUT=" + out,
		"PORTAL_CAPACITY_SOAK_FLOOD_MINUTES=" + strconv.Itoa(minutes+5), "TMPDIR=" + os.Getenv("TMPDIR"), "HOME=" + os.Getenv("HOME")}
	var floodOutput platformSyncBuffer
	flood.Stdout, flood.Stderr = &floodOutput, &floodOutput
	if err := flood.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = flood.Wait()
		close(exited)
	}()
	stop = func() {
		_ = flood.Process.Kill()
		<-exited
		_ = server.Close()
	}
	alive = func() bool {
		select {
		case <-exited:
			c.t.Logf("SOAK_FLOOD_EXITED output tail: %s", tail(floodOutput.String(), 2000))
			return false
		default:
			return true
		}
	}
	read = func() soakFloodCounters {
		var counters soakFloodCounters
		if raw, err := os.ReadFile(out); err == nil {
			_ = json.Unmarshal(raw, &counters)
		}
		return counters
	}
	return stop, read, alive
}

// load runs the floods for the requested minutes and measures the Portal's pages, the queue's dispatch decision and the real
// scheduler's start of a batch job throughout: soakSamplesPerWindow samples spread evenly across each one-minute window. The
// flood is a separate process driving the Portal's HTTP route, so the measured process sees platform load only. The backends
// flood their own memory, CPU, PIDs, log and storage from inside their containers.
func (c *soakChild) load(minutes int) {
	stop, read, alive := c.startFlood(minutes)
	defer stop()
	if soakDiagnosing() {
		c.diagnose("loaded", 60*time.Second)
	}
	started := time.Now()
	c.rec.SetLoad(started.UnixMilli(), 0, minutes)
	c.rec.Mark("load_started")
	var previous soakFloodCounters
	var pooled soakSamples
	for minute := 1; minute <= minutes; minute++ {
		windowStart := started.Add(time.Duration(minute-1) * time.Minute)
		samples := c.sampleWindow(windowStart, time.Minute, soakSamplesPerWindow)
		window := samples.window(minute, windowStart, windowStart.Add(time.Minute))
		pooled = pooled.appended(samples)
		c.rec.SetLoaded(pooled)
		current := read()
		window.Flood = soakFloodWindow(previous, current, alive(), time.Now())
		previous = current
		window.Backends = c.backendStatuses()
		c.rec.AddWindow(window)
	}
	c.rec.SetLoad(started.UnixMilli(), time.Now().UnixMilli(), minutes)
	c.rec.Mark("load_finished")
}
