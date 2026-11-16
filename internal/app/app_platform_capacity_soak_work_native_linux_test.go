//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// soakWork is the platform work the soak runs beside its other loads (doc 171, SEC-09): the application task worker, the event delivery
// coordinator and the backend-originated task flood's identities. Six installations (the workload tenants) get a steady pace of backend tasks and
// every event the soak captures; two (the saturated tenants) hold their installation task quota with tasks that are not due until the run is
// over, which is what the backend flood meets. The worker is the real scheduler of cmd/app-task-worker, polling at its default interval, against the
// real runner and the soak backends; the events are captured through the same store call the Portal's publishers use.
type soakWork struct {
	c         *soakChild
	workload  []string
	saturated []string
	// subscriptions maps an installation to its subscription id and the role the evidence names it by.
	subscriptions map[string]string
	roles         map[string]string
	depth         *soakDepthSampler
	started       time.Time

	workerCancel context.CancelFunc
	workCancel   context.CancelFunc
	workers      sync.WaitGroup
	workerAlive  atomic.Bool

	// The workload tenants submit through the Portal's own task route (baseURL is the loopback listener the flood uses) with their own service
	// identities, which are refreshed with the flood's every minute; submits collects what each submission answered and how long it took.
	baseURL    atomic.Value
	identities struct {
		sync.Mutex
		headers map[string]string
	}
	submits    soakSubmitCollector
	submitters sync.WaitGroup

	captured, captureErrors, admitErrors atomic.Int64
	reported                             struct{ captured, captureErrors int64 }
	latencies                            []int64
	identityPath                         string
}

func (c *soakChild) startWork(identityPath string) *soakWork {
	t := c.t
	w := &soakWork{c: c, workload: append([]string(nil), c.managed[soakSaturatedTenants:]...), saturated: append([]string(nil), c.managed[:soakSaturatedTenants]...),
		subscriptions: map[string]string{}, roles: map[string]string{}, depth: newSoakDepthSampler(), started: time.Now(), identityPath: identityPath}
	if len(w.workload) != soakWorkloadTenants {
		t.Fatalf("%d workload tenants, want %d", len(w.workload), soakWorkloadTenants)
	}
	c.s.platformTaskBackend = c.s.runPlatformBackendTask
	w.saturate()
	w.subscribe()
	w.writeIdentities()
	ctx, cancel := context.WithCancel(context.Background())
	w.workCancel = cancel
	workerCtx, workerCancel := context.WithCancel(context.Background())
	w.workerCancel = workerCancel
	scheduler, err := newPlatformTaskScheduler(c.s, platformFixtureID(8900), soakWorkerInterval)
	if err != nil {
		t.Fatal(err)
	}
	w.workers.Add(2)
	if c.variant.TaskWorker {
		w.workerAlive.Store(true)
		w.workers.Add(1)
		go func() {
			defer w.workers.Done()
			defer w.workerAlive.Store(false)
			if err := scheduler.run(workerCtx, false); err != nil && workerCtx.Err() == nil {
				t.Logf("SOAK_TASK_WORKER_STOPPED %v", err)
			}
		}()
	}
	go func() { defer w.workers.Done(); w.produce(ctx, c.variant.TaskWorker) }()
	go func() { defer w.workers.Done(); w.sample(ctx) }()
	t.Log("SOAK_PLATFORM_WORK_STARTED")
	return w
}

// saturate replaces the tasks the quota probes left behind: the workload tenants start empty, and each saturated tenant holds exactly its
// installation quota with tasks whose run time is after the run, so the worker never takes one and the flood meets the quota in every window.
func (w *soakWork) saturate() {
	c, t := w.c, w.c.t
	// The probes ran before any worker, so none of these tasks has an attempt.
	if attempts := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE attempts>0`); attempts != 0 {
		t.Fatalf("%d probe tasks have attempts", attempts)
	}
	mustExec(t, c.s.st, `DELETE FROM app_platform_tasks`)
	now, err := platformDatabaseNow(c.s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	runAt := now + int64(time.Duration(c.minutes+90)*time.Minute/time.Millisecond)
	for _, installation := range w.saturated {
		for n := 0; n < appplatform.MaxInstallationActiveTasks; n++ {
			if _, _, err := c.s.admitPlatformTask(c.ctx, installation, platformTaskAdmission{
				OccurrenceID: fmt.Sprintf("sat:%d", n), Handler: appplatform.TaskHandlerBackend, Payload: []byte(fmt.Sprintf(`{"soak":%d}`, n)),
				RunAtMS: runAt, ExpiresMS: runAt + int64(time.Hour/time.Millisecond)}); err != nil {
				t.Fatalf("saturating admission %d of %s: %v", n, installation, err)
			}
		}
		if got := w.activeOf(installation); got != appplatform.MaxInstallationActiveTasks {
			t.Fatalf("saturated tenant %s holds %d active tasks, want %d", installation, got, appplatform.MaxInstallationActiveTasks)
		}
	}
	if total := c.activeTasks(); total != soakSaturatedTenants*appplatform.MaxInstallationActiveTasks {
		t.Fatalf("%d active tasks after the saturation, want %d", total, soakSaturatedTenants*appplatform.MaxInstallationActiveTasks)
	}
}

func (w *soakWork) activeOf(installation string) int {
	return scalar[int](w.c.t, w.c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND state NOT IN ('succeeded','failed','cancelled')`, installation)
}

// subscribe gives every workload tenant a subscription to the soak's report with the largest in-flight bound, and the second saturated tenant one
// too: its first event meets the task quota, so the coordinator fences it with the reason task_capacity (the first saturated tenant keeps the
// subscription the quota probe fenced for its in-flight bound).
func (w *soakWork) subscribe() {
	c, t := w.c, w.c.t
	for _, installation := range append(append([]string(nil), w.workload...), w.saturated[1]) {
		id, created, err := c.s.createPlatformEventSubscription(c.ctx, installation, appplatform.EventSubscriptionSpec{
			EventType: appplatform.EventReportIngested, ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10), MaxInflight: soakEventInflight})
		if err != nil || !created {
			t.Fatalf("event subscription of %s: %s %t %v", installation, id, created, err)
		}
		w.subscriptions[installation] = id
		w.roles[installation] = "workload"
	}
	w.roles[w.saturated[1]] = "saturated"
	w.roles[w.saturated[0]] = "probe"
}

// writeIdentities issues each saturated backend's service identity for the task route and writes them where the flood process reads them. An
// identity lives five minutes at most; the file is rewritten every minute for the run.
func (w *soakWork) writeIdentities() {
	c, t := w.c, w.c.t
	headers := map[string]string{}
	for _, installation := range w.saturated {
		lease, err := platformNewUUID()
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := c.s.issuePlatformServiceIdentity(c.ctx, installation, lease, []string{"tasks.schedule"}, platformServiceIdentityMaxLifetime)
		if err != nil {
			t.Fatal("service identity of "+installation, err)
		}
		headers[installation] = soakIdentityHeader(raw)
		clear(raw)
	}
	workload := map[string]string{}
	for _, installation := range w.workload {
		lease, err := platformNewUUID()
		if err != nil {
			t.Fatal(err)
		}
		raw, _, err := c.s.issuePlatformServiceIdentity(c.ctx, installation, lease, []string{"tasks.schedule"}, platformServiceIdentityMaxLifetime)
		if err != nil {
			t.Fatal("service identity of workload tenant "+installation, err)
		}
		workload[installation] = soakIdentityHeader(raw)
		clear(raw)
	}
	w.identities.Lock()
	w.identities.headers = workload
	w.identities.Unlock()
	encoded, _ := json.Marshal(headers)
	if err := os.WriteFile(w.identityPath+".tmp", encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w.identityPath+".tmp", w.identityPath); err != nil {
		t.Fatal(err)
	}
}

// submit sends one workload tenant's backend task through the Portal's task route with that tenant's own identity, as a backend would, and records
// how it was answered and how long it took. Anything but a 200 is an admission error.
func (w *soakWork) submit(client *http.Client, index int, installation string, number int) {
	defer w.submitters.Done()
	base, _ := w.baseURL.Load().(string)
	w.identities.Lock()
	identity := w.identities.headers[installation]
	w.identities.Unlock()
	began := time.Now()
	status := 0
	var body []byte
	request, err := http.NewRequest(http.MethodPost, base+"/api/app-platform/installations/"+installation+"/tasks/background", bytes.NewReader(soakWorkloadTaskBody(index, number)))
	if err == nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(appplatform.TaskServiceIdentityHeader, identity)
		var response *http.Response
		if response, err = client.Do(request); err == nil {
			status = response.StatusCode
			body, _ = io.ReadAll(io.LimitReader(response.Body, 4096))
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}
	class := soakFloodClass(status, body, err)
	w.submits.record(class, time.Since(began).Milliseconds())
	if class != soakFloodOK {
		w.admitErrors.Add(1)
		w.c.t.Logf("SOAK_TASK_SUBMIT_ERROR %s: %s", installation, class)
	}
}

// produce submits one backend task for every workload tenant every soakTaskEvery and captures one event every soakEventEvery, and refreshes the flood's
// identities every minute.
func (w *soakWork) produce(ctx context.Context, work bool) {
	c := w.c
	tasks, events, identities := time.NewTicker(soakTaskEvery), time.NewTicker(soakEventEvery), time.NewTicker(time.Minute)
	if !work { // a diagnostic variant without the task worker: nothing is admitted or captured, the identities are still refreshed
		tasks.Stop()
		events.Stop()
	}
	defer tasks.Stop()
	defer events.Stop()
	defer identities.Stop()
	taskNumber, eventNumber := 0, 0
	client := &http.Client{Timeout: 25 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case <-identities.C:
			w.writeIdentities()
		case <-tasks.C:
			taskNumber++
			for index, installation := range w.workload {
				w.submitters.Add(1)
				go w.submit(client, index, installation, taskNumber)
			}
		case <-events.C:
			eventNumber++
			event := NotificationEvent{ID: fmt.Sprintf("10000000-0000-4000-8000-%012d", 900000+eventNumber), SourceKey: fmt.Sprintf("soakload:%d", eventNumber),
				Type: appplatform.EventReportIngested, Payload: fmt.Sprintf(`{"id":%d}`, eventNumber), ResourceType: "report", ResourceID: strconv.FormatInt(c.reportID, 10),
				CreatedAt: time.Now().UnixMilli()}
			if err := c.s.st.CaptureNotification(event, nil, nil); err != nil {
				w.captureErrors.Add(1)
				c.t.Logf("SOAK_EVENT_CAPTURE_ERROR %d: %v", eventNumber, err)
			} else {
				w.captured.Add(1)
			}
		}
	}
}

// sample reads the queue depths once a second.
func (w *soakWork) sample(ctx context.Context) {
	for tick := time.NewTicker(time.Second); ; {
		select {
		case <-ctx.Done():
			tick.Stop()
			return
		case <-tick.C:
		}
		active, events := w.counts(`SELECT installation_id,COUNT(*) FROM app_platform_tasks WHERE state NOT IN ('succeeded','failed','cancelled') GROUP BY installation_id`),
			w.counts(`SELECT installation_id,COUNT(*) FROM app_platform_tasks WHERE state NOT IN ('succeeded','failed','cancelled') AND handler='`+appplatform.TaskHandlerEvent+`' GROUP BY installation_id`)
		w.depth.observe(active, events, w.saturated)
	}
}

func (w *soakWork) counts(query string) map[string]int {
	result := map[string]int{}
	rows, err := w.c.s.st.query(query)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if rows.Scan(&key, &n) == nil {
			result[key] = n
		}
	}
	return result
}

func (w *soakWork) args(extra ...any) []any {
	args := make([]any, 0, len(w.workload)+len(extra))
	for _, installation := range w.workload {
		args = append(args, installation)
	}
	return append(args, extra...)
}

// window summarises one minute from the Portal's own tables.
func (w *soakWork) window(start, end time.Time) (soakTaskWindow, soakEventWindow, soakQuotaWindow, soakWorkloadSubmit) {
	c, t := w.c, w.c.t
	startMS, endMS, now := start.UnixMilli(), end.UnixMilli(), time.Now().UnixMilli()
	rows, err := c.s.st.query(`SELECT handler,state,created_ms,COALESCE(completed_ms,0),next_attempt_ms FROM app_platform_tasks
		WHERE installation_id IN (`+placeholders(len(w.workload))+`) AND (created_ms>=? OR completed_ms>=? OR state IN ('waiting','leased','running'))`, w.args(startMS, startMS)...)
	if err != nil {
		t.Errorf("task window query: %v", err)
		return soakTaskWindow{}, soakEventWindow{}, soakQuotaWindow{}, soakWorkloadSubmit{}
	}
	var taskRows []soakTaskRow
	delivered := 0
	for rows.Next() {
		var row soakTaskRow
		if rows.Scan(&row.Handler, &row.State, &row.CreatedMS, &row.CompletedMS, &row.NextAttemptMS) == nil {
			taskRows = append(taskRows, row)
			if row.Handler == appplatform.TaskHandlerEvent && row.State == "succeeded" && row.CompletedMS >= startMS && row.CompletedMS < endMS {
				delivered++
			}
		}
	}
	rows.Close()
	tasks, latencies := soakTaskWindowOf(taskRows, w.stateCounts(w.started.UnixMilli()), startMS, endMS, now, w.workerAlive.Load())
	w.latencies = append(w.latencies, latencies...)
	maxActive, maxEvent, saturated := w.depth.take()
	captured, errors := w.captured.Load(), w.captureErrors.Load()
	active, fenced := w.subscriptionStates()
	events := soakEventWindow{Captured: int(captured - w.reported.captured), CaptureErrors: int(errors - w.reported.captureErrors), Delivered: delivered,
		MaxInflightPerSubscription: maxEvent, SubscriptionsActive: active, SubscriptionsFenced: fenced}
	w.reported.captured, w.reported.captureErrors = captured, errors
	return tasks, events, soakQuotaWindow{MaxInstallationActive: maxActive, SaturatedActive: saturated}, w.submits.take()
}

// stateCounts is the workload tenants' tasks by state since the load began.
func (w *soakWork) stateCounts(sinceMS int64) map[string]int {
	counts := map[string]int{}
	rows, err := w.c.s.st.query(`SELECT state,COUNT(*) FROM app_platform_tasks WHERE installation_id IN (`+placeholders(len(w.workload))+`) AND created_ms>=? GROUP BY state`, w.args(sinceMS)...)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if rows.Scan(&state, &n) == nil {
			counts[state] = n
		}
	}
	return counts
}

func (w *soakWork) subscriptionStates() (active, fenced int) {
	for _, state := range w.subscriptionRows() {
		if state.State == "active" {
			active++
		} else {
			fenced++
		}
	}
	return active, fenced
}

// subscriptionRows is every subscription of the soak with its state and the reason the audit log gives for a fence.
func (w *soakWork) subscriptionRows() []soakSubscriptionState {
	c, t := w.c, w.c.t
	var result []soakSubscriptionState
	for _, installation := range append(append([]string(nil), w.workload...), w.saturated...) {
		rows, err := c.s.st.query(`SELECT id,state FROM app_platform_event_subscriptions WHERE installation_id=? ORDER BY created_ms`, installation)
		if err != nil {
			t.Errorf("subscription query: %v", err)
			continue
		}
		type pair struct{ id, state string }
		var found []pair
		for rows.Next() {
			var p pair
			if rows.Scan(&p.id, &p.state) == nil {
				found = append(found, p)
			}
		}
		rows.Close()
		for _, p := range found {
			entry := soakSubscriptionState{Installation: installation, Role: w.roles[installation], State: p.state}
			if p.state != "active" {
				var detail string
				if err := c.s.st.queryRow(`SELECT detail FROM audit_log WHERE action='app_platform.event_review_required' AND target_id=? ORDER BY id DESC LIMIT 1`, p.id).Scan(&detail); err == nil {
					entry.Reason = soakFenceReason(detail)
				}
			}
			result = append(result, entry)
		}
	}
	return result
}

// finish stops the producers, lets the worker finish what is still open (up to soakDrainLimit), stops it, and returns what the run added up to.
func (w *soakWork) finish() soakPlatformWork {
	c, t := w.c, w.c.t
	w.workCancel()
	w.submitters.Wait() // a submission in flight is part of the run
	began := time.Now()
	open := func() int {
		return scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id IN (`+placeholders(len(w.workload))+`) AND state IN ('waiting','leased','running')`, w.args()...)
	}
	left := open()
	for left > 0 && time.Since(began) < soakDrainLimit {
		time.Sleep(time.Second)
		left = open()
	}
	drain := soakDrain{Seconds: int(time.Since(began).Seconds()), OpenAfter: left, TimedOut: left > 0}
	w.workerCancel()
	w.workers.Wait()
	states := w.stateCounts(w.started.UnixMilli())
	created := 0
	for _, n := range states {
		created += n
	}
	delivered := scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id IN (`+placeholders(len(w.workload))+`) AND handler=? AND state='succeeded' AND created_ms>=?`,
		w.args(appplatform.TaskHandlerEvent, w.started.UnixMilli())...)
	ranSaturated := 0
	for _, installation := range w.saturated {
		ranSaturated += scalar[int](t, c.s.st, `SELECT COUNT(*) FROM app_platform_tasks WHERE installation_id=? AND attempts>0 AND occurrence_id LIKE 'sat:%'`, installation)
	}
	return soakPlatformWork{WorkloadTenants: len(w.workload), SaturatedTenants: len(w.saturated), WorkerInterval: soakWorkerInterval.Milliseconds(),
		LoadedLatency: soakMillisSummarize(w.latencies),
		Tasks:         soakTaskTotals{Created: created, AdmitErrors: int(w.admitErrors.Load()), ByState: states},
		Events: soakEventTotals{Captured: int(w.captured.Load()), ExpectedDeliveries: int(w.captured.Load()) * len(w.workload), Delivered: delivered,
			CaptureErrors: int(w.captureErrors.Load())},
		Drain: drain, Subscriptions: w.subscriptionRows(), SaturatedTaskRuns: ranSaturated}
}

func soakIdentityFile() string { return filepath.Join(os.Getenv("TMPDIR"), "flood-identities.json") }
