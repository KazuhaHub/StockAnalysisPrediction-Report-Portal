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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	if err := os.Chown(childData, 41001, 41001); err != nil {
		t.Fatal(err)
	}

	// The development image profile: the soak backend is a local reference image from the job's own registry.
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
	child.Dir = dir
	child.Env = []string{
		"GOMAXPROCS=2", "PORTAL_CAPACITY_SOAK_CHILD=1", "PORTAL_CAPACITY_SOAK_DIR=" + childData,
		"PORTAL_CAPACITY_SOAK_IMAGE=" + image,
		"APP_CAPACITY_SOAK_MINUTES=" + strconv.Itoa(minutes), "APP_CAPACITY_SOAK_BASELINE_SECONDS=" + strconv.Itoa(baseline),
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
	s, session := platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, s, session)
	s.platformImageDir = t.TempDir()
	bindPlatformFixtureRuntime(t, s, session, enrollment)
	endpoint := platformRuntimeEndpoint{path: os.Getenv("PORTAL_DATA_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	s.platformBackendEndpoint.Store(&endpoint)
	c := &soakChild{
		t: t, ctx: context.Background(), s: s, session: session, endpoint: endpoint, image: os.Getenv("PORTAL_CAPACITY_SOAK_IMAGE"),
		rec: newSoakRecorder(filepath.Join(dir, soakChildFile)), nextID: 9000, symbol: "600000",
	}

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

	// 2. Eight managed backends at the sizing the budgets give, then the excess probes that need them.
	if c.writable, err = newPlatformWritableInstallScheduler(s, endpoint, c.id(), 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < soakBackends; i++ {
		c.managed = append(c.managed, c.installManaged(fmt.Sprintf("notes-soak-m%d", i), c.sizing.MemoryMiB, c.sizing.CPUMillis))
	}
	c.rec.SetCount("managed", len(c.managed))
	c.rec.Mark("managed_backends_running")
	c.probeBackends()
	c.probeLayers()
	c.probeStaging()

	// 3. Forty-two static installations bring the Portal to the cap; the next install is refused.
	for i := 0; i < soakStaticApps; i++ {
		c.statics = append(c.statics, c.installStatic(fmt.Sprintf("notes-soak-s%02d", i)))
	}
	c.rec.SetCount("static", len(c.statics))
	if got := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE phase<>'removed'`); got != platformInstallationCap {
		t.Fatalf("%d installations, want %d", got, platformInstallationCap)
	}
	c.probeInstallationCap()
	t.Log("SOAK_INSTALLATIONS_AT_THE_CAP")

	// 4. The task and event floods, then the load.
	c.floodQuotas()
	if failed := c.failedProbes(); len(failed) > 0 {
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

func (c *soakChild) failedProbes() []string {
	var failed []string
	for _, p := range c.rec.Snapshot().Probes {
		if !p.Passed {
			failed = append(failed, fmt.Sprintf("%s: expected %q, observed %q", p.Name, p.Expected, p.Observed))
		}
	}
	return failed
}

// --- the Portal's own reads, and the queue ---

// seedPortal adds one report and a backlog of queued batch jobs, so the pages and the queue have rows to read.
func (c *soakChild) seedPortal() {
	t := c.t
	body := strings.Repeat("Soak report paragraph with enough text to be a realistic page.\n\n", 200)
	id, _, err := c.s.st.UpsertReport(Rep{Symbol: c.symbol, Name: "Soak Co", Date: "2026-11-13", RType: predTestRtype, Title: "soak report", MD: body})
	if err != nil {
		t.Fatal(err)
	}
	c.reportID = id
	target := seedTarget(t, c.s.st)
	for i := 0; i < 200; i++ {
		if _, err := c.s.st.CreateBatchJob(target, 1, 0, c.session.username, []map[string]string{{"symbol": c.symbol}}, "50"); err != nil {
			t.Fatal(err)
		}
	}
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
func (c *soakChild) dispatch() time.Duration {
	began := time.Now()
	queue.Admit(c.s.queuedItems(), 0, queue.Plan{Budget: 4, Reserved: 1})
	return time.Since(began)
}

type soakSamples struct{ home, report, dispatch []time.Duration }

func (c *soakChild) sample(into *soakSamples, n int) {
	for i := 0; i < n; i++ {
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
		into.dispatch = append(into.dispatch, c.dispatch())
	}
}

const soakSamplesPerWindow = 100

func (s soakSamples) window(minute int) soakWindow {
	return soakWindow{Minute: minute, At: time.Now().UnixMilli(), Home: soakSummarize(s.home), Report: soakSummarize(s.report), Dispatch: soakSummarize(s.dispatch)}
}

func (c *soakChild) measureIdle(duration time.Duration) {
	var pooled soakSamples
	for deadline, i := time.Now().Add(duration), 0; time.Now().Before(deadline); i++ {
		var one soakSamples
		c.sample(&one, soakSamplesPerWindow)
		pooled.home, pooled.report, pooled.dispatch = append(pooled.home, one.home...), append(pooled.report, one.report...), append(pooled.dispatch, one.dispatch...)
		c.rec.AddIdleWindow(one.window(i))
		time.Sleep(time.Second)
	}
	c.rec.SetIdle(pooled.window(0))
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
	t := c.t
	release := c.stageManaged(app, memoryMiB, cpuMillis)
	review, err := c.s.createPlatformWritableInstallReview(c.ctx, c.session, release.sha256, release.trustRevision, c.audience(), c.endpoint)
	if err != nil {
		t.Fatal("review "+app, err)
	}
	committed, err := c.s.consumePlatformWritableInstallReview(c.ctx, c.session, review.ID, review.SHA256, c.id(), c.endpoint)
	if err != nil {
		t.Fatal("admit "+app, err)
	}
	return committed.InstallationID, c.drive(committed.OperationID, 6*time.Minute)
}

// drive ticks the writable install worker until the operation completes (empty code) or ends with an error code.
func (c *soakChild) drive(operation string, limit time.Duration) string {
	deadline := time.Now().Add(limit)
	ctx, cancel := context.WithDeadline(c.ctx, deadline)
	defer cancel()
	for time.Now().Before(deadline) {
		_ = c.writable.tick(ctx)
		if scalar[string](c.t, c.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) == "complete" {
			return ""
		}
		var code sql.NullString
		if err := c.s.st.queryRow(`SELECT error_code FROM app_platform_operations WHERE id=?`, operation).Scan(&code); err != nil {
			c.t.Fatal(err)
		}
		if code.Valid && code.String != "" {
			return code.String
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "timeout"
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

// retire takes a refused probe installation out of the count and, when it holds a data namespace, out of the
// runner's disk reservation, through the ordinary managed uninstall. It reports how it ended.
func (c *soakChild) retire(installation string) string {
	ctx := c.ctx
	removal, err := c.s.createPlatformRemovalReview(c.session, installation, appplatform.ActionUninstall)
	if err == nil {
		var commit platformRemovalCommit
		if commit, err = c.s.consumePlatformRemovalReview(c.session, removal.ID, removal.SHA256, c.id()); err == nil {
			var claim platformInstallSelection
			if claim, err = c.s.claimNextPlatformManagedUninstall(ctx, c.id()); err == nil && claim.lease.id == commit.OperationID {
				var run platformInstallRun
				if run, err = c.s.runPlatformManagedUninstall(ctx, claim.lease, c.endpoint); err == nil && run.lease.snapshot.Stage == appplatform.StageComplete {
					return "uninstall"
				}
			} else if err == nil {
				err = errors.New("claimed another operation")
			}
		}
	}
	mustExec(c.t, c.s.st, `UPDATE app_platform_installations SET phase='removed',desired_state='removed' WHERE id=?`, installation)
	return "database: " + err.Error()
}

// --- the excess probes ---

// probeBackends starts the three backends the eight leave no room for. Each is refused by the runner with the
// dimension it overflows, which the Portal keeps as the operation's error code.
func (c *soakChild) probeBackends() {
	for _, p := range soakBackendProbes(c.sizing) {
		app := "notes-soak-p-" + strings.ReplaceAll(p.Name, "_", "-")
		installation, code := c.installManagedWithCode(app, p.MemoryMiB, p.CPUMillis)
		cleanup := c.retire(installation)
		c.rec.AddProbe(soakProbeResult{Name: p.Name, Expected: "capacity_exhausted_" + p.Dimension, Observed: code, Native: true,
			Detail: fmt.Sprintf("requested %d MiB and %d millis against %d MiB and %d millis remaining; residue: %s", p.MemoryMiB, p.CPUMillis, c.sizing.RemainingMemoryMiB, c.sizing.RemainingCPUMillis, cleanup)})
	}
}

// probeLayers stages two crafted packages. The first has a layer that decompresses beyond its declaration and is
// refused by the offline import route (the same check the runner repeats on every blob it receives). The second
// declares eleven maximum-size layers, 11 GiB of the 10 GiB image store, from layers that store a thousandth of
// that: it stages, and the runner refuses its load at admission, before the Docker daemon is asked for anything.
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
	c.rec.AddProbe(soakProbeResult{Name: "layer_beyond_its_declaration", Expected: "app_platform_invalid_request", Observed: observed, Native: true,
		Detail: fmt.Sprintf("offline import answered %d and stored no release; the runner's own refusal of the same bytes (uncompressed_bound) is proven by the blob channel unit tests", response.Code)})

	layers := make([]platformImageLayerData, 0, soakImageStoreLayers)
	for i := 0; i < soakImageStoreLayers; i++ {
		layers = append(layers, soakZeroLayer(t, fmt.Sprintf("soak/layer-%02d", i), appplatform.MaxImageLayerUncompressedBytes))
	}
	big := newPlatformManagedImagePartsFromLayers(t, "3.2.0", layers)
	if code := platformOfflineRequest(c.mux, c.cookie, "application/zip", big.packageBytes(t, nil, "")); code.Code != http.StatusOK {
		t.Fatalf("the image-store package was not staged: %d %s", code.Code, code.Body.String())
	}
	app := "notes-soak-p-image-store"
	trust := scalar[int64](t, c.s.st, `SELECT trust_revision FROM app_platform_releases WHERE sha256=?`, platformDigest(big.descriptor))
	review, err := c.s.createPlatformWritableInstallReview(c.ctx, c.session, platformDigest(big.descriptor), trust, c.audience(), c.endpoint)
	if err != nil {
		t.Fatal("review "+app, err)
	}
	committed, err := c.s.consumePlatformWritableInstallReview(c.ctx, c.session, review.ID, review.SHA256, c.id(), c.endpoint)
	if err != nil {
		t.Fatal("admit "+app, err)
	}
	code := c.drive(committed.OperationID, 15*time.Minute)
	// It loaded nothing, so it holds no namespace and no backend: only its installation row leaves the count.
	mustExec(t, c.s.st, `UPDATE app_platform_installations SET phase='removed',desired_state='removed' WHERE id=?`, committed.InstallationID)
	c.rec.AddProbe(soakProbeResult{Name: "image_beyond_the_image_store", Expected: "capacity_exhausted_image_store", Observed: code, Native: true,
		Detail: fmt.Sprintf("%d layers declaring %d bytes each against an image store of %d bytes", soakImageStoreLayers, appplatform.MaxImageLayerUncompressedBytes, c.budgets.ImageStoreBytes)})
}

// probeStaging proves the 2 GiB staging cap against the real store of this Portal. One upload is bounded at
// 384 MiB by the offline route, so the cap refuses the sum: five uploads in flight hold 1920 MiB and the sixth is
// refused, and the refusal is the typed storage-full response. The same cap is proven at its exact value, without
// writing a byte, by the unit tests of this change.
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
	c.rec.AddProbe(soakProbeResult{Name: "staging_over_two_gib", Expected: "app_platform_storage_full", Observed: observed, Native: true,
		Detail: fmt.Sprintf("%d concurrent 384 MiB uploads held against a cap of %d bytes", len(releases), store.stagingCap)})
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
	c.rec.AddProbe(soakProbeResult{Name: "fifty_first_installation", Expected: "installation_capacity", Observed: observed, Native: true,
		Detail: fmt.Sprintf("review error refused=%t, HTTP %d, %d installations", reviewRefused, response.Code, scalar[int](c.t, c.s.st, `SELECT COUNT(*) FROM app_platform_installations WHERE phase<>'removed'`))})
}

// --- the platform floods ---

// grantTasks gives a managed installation the capabilities its tasks and events need, as the service-identity
// fixture does: the soak's descriptors declare none, so the install review granted nothing.
func (c *soakChild) grantTasks(installation string) {
	var app string
	var policy int64
	if err := c.s.st.queryRow(`SELECT app_id,policy_revision FROM app_platform_installations WHERE id=?`, installation).Scan(&app, &policy); err != nil {
		c.t.Fatal(err)
	}
	plan := scalar[string](c.t, c.s.st, `SELECT id FROM app_platform_review_plans WHERE app_id=? AND action='install' ORDER BY created_ms DESC LIMIT 1`, app)
	now := time.Now().UnixMilli()
	for _, capability := range []string{"background.run", "tasks.schedule", "events.subscribe", "reports.read"} {
		mustExec(c.t, c.s.st, `INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,?,1,'{}',?,?,?,?)`,
			installation, capability, policy, plan, now, now)
	}
}

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

// floodQuotas drives the event subscription to its in-flight bound, each of seven managed installations to its
// 256 task quota, and the whole Portal to its 10000 global quota. The global quota is reached with a backlog of
// waiting tasks attributed to the static installations (they cannot schedule tasks themselves: they have no
// backend), inserted with the columns the admission itself writes; the refusals are the real admission.
func (c *soakChild) floodQuotas() {
	t := c.t
	for _, installation := range c.managed {
		c.grantTasks(installation)
	}
	// Event quota: one subscription with one in-flight delivery saturates on the second source event.
	now, err := platformDatabaseNow(c.s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	subscription, created, err := c.s.createPlatformEventSubscription(c.ctx, c.managed[0], appplatform.EventSubscriptionSpec{
		EventType: appplatform.EventReportIngested, ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10), MaxInflight: 1})
	if err != nil || !created {
		t.Fatalf("event subscription: %s %t %v", subscription, created, err)
	}
	for i := 0; i < 2; i++ {
		event := NotificationEvent{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", 8300+i), SourceKey: fmt.Sprintf("soak:%d", i), Type: appplatform.EventReportIngested,
			Payload: `{"id":1}`, ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10), CreatedAt: now}
		if err := c.s.st.CaptureNotification(event, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	state := scalar[string](t, c.s.st, `SELECT state FROM app_platform_event_subscriptions WHERE id=?`, subscription)
	c.rec.AddProbe(soakProbeResult{Name: "event_subscription_over_quota", Expected: "review_required", Observed: state, Native: true,
		Detail: "two source events against a subscription that allows one delivery in flight; both source events are kept"})

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
	c.rec.AddProbe(soakProbeResult{Name: "task_over_installation_quota", Expected: "task_capacity", Observed: quotaObserved, Native: true,
		Detail: fmt.Sprintf("%d installations refused the 257th active task", filled)})

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
	c.rec.AddProbe(soakProbeResult{Name: "task_over_global_quota", Expected: "task_capacity", Observed: observed, Native: true,
		Detail: fmt.Sprintf("%d active tasks in all, %d on the installation that was refused, so only the global quota can have refused it", c.activeTasks(), perInstallation)})
	// The per-application quota (1000) needs four installations of one application, and the cap of fifty holds
	// fifty applications: it is proven at its boundary by TestTaskQuotasRefuseAtTheirOwnBoundary.
	c.rec.AddProbe(soakProbeResult{Name: "task_over_application_quota", Expected: "task_capacity", Observed: "task_capacity", Native: false,
		Detail: "unit level: appplatform.AdmitTask refuses at 1000 active tasks of one application independently of the other two quotas (TestTaskQuotasRefuseAtTheirOwnBoundary); no application has more than one installation in this soak"})
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
		response, err := c.s.requestPlatformWritableBackend(c.ctx, c.session, installation, http.MethodGet, "/v1/soak/status", nil, c.endpoint)
		if err != nil || response.Status != http.StatusOK {
			statuses = append(statuses, soakBackendStatus{Installation: installation})
			continue
		}
		statuses = append(statuses, soakParseBackendStatus(installation, response.Body))
	}
	return statuses
}

// --- the load ---

// load runs the floods for the requested minutes and, once a minute, measures the Portal's pages and the queue.
// Every backend is sent signed requests continuously (one in flight each), and task admissions that the quota
// refuses are repeated, so the platform's limits are under pressure the whole time. The backends flood their own
// memory, CPU, PIDs, log and storage from inside their containers.
func (c *soakChild) load(minutes int) {
	started := time.Now()
	c.rec.SetLoad(started.UnixMilli(), 0, minutes)
	c.rec.Mark("load_started")
	ctx, cancel := context.WithCancel(c.ctx)
	var wg sync.WaitGroup
	var requests, ok, failed, refusals atomic.Int64
	body := []byte(`{"soak":"` + strings.Repeat("m", 2048) + `"}`)
	for _, installation := range c.managed {
		wg.Add(1)
		go func(installation string) {
			defer wg.Done()
			for ctx.Err() == nil {
				requests.Add(1)
				if response, err := c.s.requestPlatformWritableBackend(ctx, c.session, installation, http.MethodPost, "/v1/echo", body, c.endpoint); err == nil && response.Status == http.StatusOK {
					ok.Add(1)
				} else {
					failed.Add(1)
					time.Sleep(50 * time.Millisecond)
				}
			}
		}(installation)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; ctx.Err() == nil; n++ {
			if err := c.admit(c.managed[0], 10000+n, "pressure"); errors.Is(err, errAppPlatformTaskQuota) {
				refusals.Add(1)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	var previous soakFlood
	for minute := 1; ; minute++ {
		next := started.Add(time.Duration(minute) * time.Minute)
		if final := started.Add(time.Duration(minutes) * time.Minute); next.After(final) {
			next = final
		}
		time.Sleep(time.Until(next))
		var samples soakSamples
		c.sample(&samples, soakSamplesPerWindow)
		window := samples.window(minute)
		current := soakFlood{BackendRequests: requests.Load(), BackendRequestsOK: ok.Load(), BackendRequestErrors: failed.Load(), TaskRefusals: refusals.Load()}
		window.Flood = soakFlood{
			BackendRequests: current.BackendRequests - previous.BackendRequests, BackendRequestsOK: current.BackendRequestsOK - previous.BackendRequestsOK,
			BackendRequestErrors: current.BackendRequestErrors - previous.BackendRequestErrors, TaskRefusals: current.TaskRefusals - previous.TaskRefusals,
		}
		previous = current
		window.Backends = c.backendStatuses()
		c.rec.AddWindow(window)
		if !time.Now().Before(started.Add(time.Duration(minutes) * time.Minute)) {
			break
		}
	}
	cancel()
	wg.Wait()
	c.rec.SetLoad(started.UnixMilli(), time.Now().UnixMilli(), minutes)
	c.rec.Mark("load_finished")
}
