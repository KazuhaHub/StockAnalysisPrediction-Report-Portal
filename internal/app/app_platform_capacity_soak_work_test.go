package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// SEC-09 (doc 171): the soak also runs the platform task worker, the event delivery coordinator and a backend-originated task flood. This file holds
// what those parts record and how they are summarised, so that it is unit tested without a runner; the native soak drives them
// (app_platform_capacity_soak_work_native_linux_test.go) and scripts/acceptance/capacity_soak.py judges the evidence.

const (
	// soakWorkloadTenants run the worker's load: a steady pace of backend tasks and one event fanned out to every one of them.
	soakWorkloadTenants = 6
	// soakSaturatedTenants hold their installation task quota for the whole run, with tasks that are not due until after it, so the backend
	// flood meets the quota in every window.
	soakSaturatedTenants = 2
	// soakTaskEvery is the pace of backend tasks (one per workload tenant each tick) and soakEventEvery that of events (one event reaches every
	// workload tenant): 36 tasks and 30 deliveries a minute, which the one-task-at-a-time worker keeps up with.
	soakTaskEvery  = 10 * time.Second
	soakEventEvery = 12 * time.Second
	// soakWorkerInterval is the polling interval of the application task worker (the command's own default).
	soakWorkerInterval = time.Second
	// soakDrainLimit is how long after the last window the worker may take to finish what is still open.
	soakDrainLimit = 3 * time.Minute
	// soakFloodConnectionsPerBackend is the number of requests a flooding backend keeps in flight.
	soakFloodConnectionsPerBackend = 2
	// soakEventInflight is the subscription bound the workload subscriptions ask for (the maximum the contract allows).
	soakEventInflight = 16
	// soakReplayOccurrence is the saturating task a flooding backend replays: the replay of an existing occurrence is answered, not refused.
	soakReplayOccurrence = "sat:1"
)

// soakMillisStats summarises a series of durations in milliseconds.
type soakMillisStats struct {
	Count int   `json:"count"`
	P50   int64 `json:"p50_ms"`
	P95   int64 `json:"p95_ms"`
	Max   int64 `json:"max_ms"`
}

func soakMillisSummarize(values []int64) soakMillisStats {
	if len(values) == 0 {
		return soakMillisStats{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	at := func(percentile int) int64 {
		index := (len(sorted)*percentile + 99) / 100
		if index < 1 {
			index = 1
		}
		return sorted[min(index, len(sorted))-1]
	}
	return soakMillisStats{Count: len(sorted), P50: at(50), P95: at(95), Max: at(100)}
}

// soakTaskRow is the part of a platform task the window summary reads.
type soakTaskRow struct {
	Handler       string
	State         string
	CreatedMS     int64
	CompletedMS   int64
	NextAttemptMS int64
}

// soakTaskWindow is what the worker did with the workload tenants' tasks in one window. The failure counts are the tasks in that state at the
// end of the window, since the load began: a task that failed is never retried by the worker and never goes away.
type soakTaskWindow struct {
	Created         int             `json:"created"`
	CreatedBackend  int             `json:"created_backend"`
	CreatedEvent    int             `json:"created_event"`
	Succeeded       int             `json:"succeeded"`
	Failed          int             `json:"failed"`
	Uncertain       int             `json:"uncertain"`
	ReviewRequired  int             `json:"review_required"`
	Cancelled       int             `json:"cancelled"`
	Open            int             `json:"open"`
	OldestOpenAgeMS int64           `json:"oldest_open_age_ms"`
	Latency         soakMillisStats `json:"latency_ms"`
	WorkerAlive     bool            `json:"worker_alive"`
}

// soakEventWindow is what the event delivery coordinator did in one window.
type soakEventWindow struct {
	Captured                   int `json:"captured"`
	CaptureErrors              int `json:"capture_errors"`
	Delivered                  int `json:"delivered"`
	MaxInflightPerSubscription int `json:"max_inflight_per_subscription"`
	SubscriptionsActive        int `json:"subscriptions_active"`
	SubscriptionsFenced        int `json:"subscriptions_fenced"`
}

// soakQuotaWindow is the task quota as sampled during one window: the most active tasks any installation held and the saturated tenants' own.
type soakQuotaWindow struct {
	MaxInstallationActive int            `json:"max_installation_active"`
	SaturatedActive       map[string]int `json:"saturated_active"`
}

// soakBackendFlood counts what the backend-originated task flood did in one window, by class.
type soakBackendFlood struct {
	Requests       int64            `json:"requests"`
	Answered       int64            `json:"answered"`
	Refused        int64            `json:"refused"`
	Classes        map[string]int64 `json:"classes"`
	Alive          bool             `json:"alive"`
	HeartbeatAgeMS int64            `json:"heartbeat_age_ms"`
	// ForgedRequests and ForgedClasses are the anonymous half of the flood: credentials naming a real installation with a wrong MAC. They are
	// counted apart so that Requests stays the authenticated backends' count.
	ForgedRequests int64            `json:"forged_requests"`
	ForgedClasses  map[string]int64 `json:"forged_classes"`
}

// soakWorkloadSubmit is what the workload tenants' own submissions through the task route did in one window: how each was answered and how long
// it took. It is the cross-tenant measurement: these tenants share the Portal, the database and the route with the flood.
type soakWorkloadSubmit struct {
	Requests int64            `json:"requests"`
	Answered int64            `json:"answered"`
	Classes  map[string]int64 `json:"classes"`
	Latency  soakMillisStats  `json:"latency_ms"`
}

// soakSubmitCollector gathers the workload tenants' submissions between two takes.
type soakSubmitCollector struct {
	mu        sync.Mutex
	classes   map[string]int64
	latencies []int64
}

func (c *soakSubmitCollector) record(class string, ms int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.classes == nil {
		c.classes = map[string]int64{}
	}
	c.classes[class]++
	c.latencies = append(c.latencies, ms)
}

// take returns what was recorded since the last take and starts over.
func (c *soakSubmitCollector) take() soakWorkloadSubmit {
	c.mu.Lock()
	defer c.mu.Unlock()
	window := soakWorkloadSubmit{Classes: map[string]int64{}, Latency: soakMillisSummarize(c.latencies)}
	for class, n := range c.classes {
		window.Classes[class] = n
		window.Requests += n
	}
	window.Answered = window.Classes[soakFloodOK]
	c.classes, c.latencies = nil, nil
	return window
}

// soakWorkloadTaskBody is the backend task one workload tenant submits through the route; every occurrence is new.
func soakWorkloadTaskBody(index, number int) []byte {
	return []byte(fmt.Sprintf(`{"occurrenceId":"load:%d:%d","handler":%q,"payload":{"soak":%d}}`, index, number, appplatform.TaskHandlerBackend, number))
}

const (
	soakTaskFloodAnswered = "200"
	soakTaskFloodRefused  = "429:task_capacity"
)

// soakBackendFloodWindow is what the flood's task requests did between two readings of its counters.
func soakBackendFloodWindow(previous, current soakFloodCounters, alive bool, now time.Time) soakBackendFlood {
	classes := map[string]int64{}
	for class, count := range current.TaskClasses {
		if delta := count - previous.TaskClasses[class]; delta > 0 {
			classes[class] = delta
		}
	}
	forged := map[string]int64{}
	for class, count := range current.ForgedClasses {
		if delta := count - previous.ForgedClasses[class]; delta > 0 {
			forged[class] = delta
		}
	}
	return soakBackendFlood{Requests: current.TaskRequests - previous.TaskRequests, Answered: classes[soakTaskFloodAnswered], Refused: classes[soakTaskFloodRefused],
		Classes: classes, Alive: alive, HeartbeatAgeMS: now.UnixMilli() - current.HeartbeatMS,
		ForgedRequests: current.ForgedRequests - previous.ForgedRequests, ForgedClasses: forged}
}

// soakForgedIdentityHeader is a credential that names the same installation as header and carries another lease id under the MAC of the first: a
// well-formed, canonical identity whose MAC is wrong. n makes each one different. An unreadable header gives "".
func soakForgedIdentityHeader(header string, n int) string {
	raw, err := decodePlatformServiceIdentityHeader(header)
	if err != nil {
		return ""
	}
	var grant appplatform.ServiceIdentityGrant
	if json.Unmarshal(raw, &grant) != nil {
		return ""
	}
	grant.LeaseID = fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
	forged, err := json.Marshal(grant)
	if err != nil {
		return ""
	}
	return soakIdentityHeader(forged)
}

// soakTaskWindowOf summarises the workload tenants' tasks over [start, end). rows are the tasks created in or since the window or still open;
// states counts every workload task by state since the load began.
func soakTaskWindowOf(rows []soakTaskRow, states map[string]int, start, end, now int64, alive bool) (soakTaskWindow, []int64) {
	window := soakTaskWindow{WorkerAlive: alive, Failed: states["failed"], Uncertain: states["uncertain"], ReviewRequired: states["review_required"], Cancelled: states["cancelled"]}
	var latencies []int64
	for _, row := range rows {
		if row.CreatedMS >= start && row.CreatedMS < end {
			window.Created++
			if row.Handler == appplatform.TaskHandlerEvent {
				window.CreatedEvent++
			} else {
				window.CreatedBackend++
			}
		}
		if row.State == "succeeded" && row.CompletedMS >= start && row.CompletedMS < end {
			window.Succeeded++
			latencies = append(latencies, max(row.CompletedMS-row.CreatedMS, 0))
		}
		if row.State == "waiting" || row.State == "leased" || row.State == "running" {
			if row.NextAttemptMS > now {
				continue // waiting out its backoff, not stuck
			}
			window.Open++
			if age := now - max(row.CreatedMS, row.NextAttemptMS); age > window.OldestOpenAgeMS {
				window.OldestOpenAgeMS = age
			}
		}
	}
	window.Latency = soakMillisSummarize(latencies)
	return window, latencies
}

// soakSubscriptionState is one event subscription of the soak at the end of the run: its role, its state and, when the coordinator fenced it for
// review, the reason the audit log records.
type soakSubscriptionState struct {
	Installation string `json:"installation"`
	Role         string `json:"role"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
}

// soakPlatformWork is the part of the evidence the whole run adds up to.
type soakPlatformWork struct {
	WorkloadTenants   int                     `json:"workload_tenants"`
	SaturatedTenants  int                     `json:"saturated_tenants"`
	WorkerInterval    int64                   `json:"worker_interval_ms"`
	LoadedLatency     soakMillisStats         `json:"loaded_latency_ms"`
	Tasks             soakTaskTotals          `json:"tasks_total"`
	Events            soakEventTotals         `json:"events_total"`
	Drain             soakDrain               `json:"drain"`
	Subscriptions     []soakSubscriptionState `json:"subscriptions"`
	SaturatedTaskRuns int                     `json:"saturated_tasks_run"`
}

type soakTaskTotals struct {
	Created     int            `json:"created"`
	AdmitErrors int            `json:"admit_errors"`
	ByState     map[string]int `json:"by_state"`
}

type soakEventTotals struct {
	Captured           int `json:"captured"`
	ExpectedDeliveries int `json:"expected_deliveries"`
	Delivered          int `json:"delivered"`
	CaptureErrors      int `json:"capture_errors"`
}

type soakDrain struct {
	Seconds   int  `json:"seconds"`
	OpenAfter int  `json:"open_after"`
	TimedOut  bool `json:"timed_out"`
}

// soakDepthSampler keeps the largest queue depths seen since it was last read: one reading a second from the Portal's own table.
type soakDepthSampler struct {
	mu        sync.Mutex
	maxActive int
	maxEvent  int
	saturated map[string]int
}

func newSoakDepthSampler() *soakDepthSampler { return &soakDepthSampler{saturated: map[string]int{}} }

// observe takes the active tasks of every installation and the active event deliveries of every installation, and the tenants that are saturated.
func (d *soakDepthSampler) observe(active, events map[string]int, saturated []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, n := range active {
		d.maxActive = max(d.maxActive, n)
	}
	for _, n := range events {
		d.maxEvent = max(d.maxEvent, n)
	}
	for _, installation := range saturated {
		d.saturated[installation] = active[installation]
	}
}

// take returns the maxima since the last take and starts over; the saturated tenants' counts are the latest seen.
func (d *soakDepthSampler) take() (maxActive, maxEvent int, saturated map[string]int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	maxActive, maxEvent = d.maxActive, d.maxEvent
	saturated = map[string]int{}
	for k, v := range d.saturated {
		saturated[k] = v
	}
	d.maxActive, d.maxEvent = 0, 0
	return maxActive, maxEvent, saturated
}

// soakIdentityHeader is the value of X-Portal-Service-Identity: the raw service identity, base64url without padding, as the route decodes it.
func soakIdentityHeader(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }

// soakBackendFloodBody is one task a flooding backend submits through the session-free task route. Every fourth request replays the occurrence
// the tenant already holds (answered, created=false); the rest are new occurrences that meet the installation quota.
func soakBackendFloodBody(prefix string, sequence int64) []byte {
	if sequence%4 == 0 {
		return []byte(fmt.Sprintf(`{"occurrenceId":%q,"handler":%q,"payload":{"soak":1}}`, soakReplayOccurrence, appplatform.TaskHandlerBackend))
	}
	return []byte(fmt.Sprintf(`{"occurrenceId":"flood-%s-%d","handler":%q,"payload":{"flood":%d}}`, prefix, sequence, appplatform.TaskHandlerBackend, sequence))
}

// soakFenceReason reads the reason out of the audit detail the coordinator writes when it fences a subscription.
func soakFenceReason(detail string) string {
	var wire struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(detail), &wire)
	return wire.Reason
}

func TestSoakMillisSummaryIsTheNearestRankPercentile(t *testing.T) {
	values := make([]int64, 100)
	for i := range values {
		values[i] = int64(i + 1)
	}
	got := soakMillisSummarize(values)
	if got.Count != 100 || got.P50 != 50 || got.P95 != 95 || got.Max != 100 {
		t.Fatalf("summary = %+v", got)
	}
	if soakMillisSummarize(nil) != (soakMillisStats{}) {
		t.Fatal("an empty series has a summary")
	}
	if one := soakMillisSummarize([]int64{7}); one.P50 != 7 || one.P95 != 7 || one.Max != 7 {
		t.Fatalf("one sample = %+v", one)
	}
}

func TestSoakTaskWindowCountsCreatedCompletedLatencyAndWhatIsOpen(t *testing.T) {
	rows := []soakTaskRow{
		{Handler: appplatform.TaskHandlerBackend, State: "succeeded", CreatedMS: 1_000, CompletedMS: 1_400, NextAttemptMS: 1_000},
		{Handler: appplatform.TaskHandlerEvent, State: "succeeded", CreatedMS: 1_100, CompletedMS: 2_100, NextAttemptMS: 1_100},
		{Handler: appplatform.TaskHandlerEvent, State: "succeeded", CreatedMS: 500, CompletedMS: 900, NextAttemptMS: 500},     // completed before the window
		{Handler: appplatform.TaskHandlerBackend, State: "waiting", CreatedMS: 1_800, NextAttemptMS: 1_800},                   // due and open
		{Handler: appplatform.TaskHandlerBackend, State: "running", CreatedMS: 1_200, NextAttemptMS: 1_200},                   // the oldest open one
		{Handler: appplatform.TaskHandlerBackend, State: "waiting", CreatedMS: 1_300, NextAttemptMS: 9_000},                   // backing off: not open
		{Handler: appplatform.TaskHandlerBackend, State: "succeeded", CreatedMS: 2_500, CompletedMS: 2_600, NextAttemptMS: 0}, // after the window
	}
	window, latencies := soakTaskWindowOf(rows, map[string]int{"failed": 1, "uncertain": 2, "review_required": 3, "cancelled": 4}, 1_000, 2_200, 2_200, true)
	if window.Created != 5 || window.CreatedBackend != 4 || window.CreatedEvent != 1 || window.Succeeded != 2 || window.Open != 2 || window.OldestOpenAgeMS != 1_000 {
		t.Fatalf("window = %+v", window)
	}
	if window.Failed != 1 || window.Uncertain != 2 || window.ReviewRequired != 3 || window.Cancelled != 4 || !window.WorkerAlive {
		t.Fatalf("states = %+v", window)
	}
	if len(latencies) != 2 || window.Latency.Count != 2 || window.Latency.Max != 1_000 || window.Latency.P50 != 400 {
		t.Fatalf("latencies = %v %+v", latencies, window.Latency)
	}
}

func TestSoakBackendFloodWindowIsTheDeltaOfTheTaskCounters(t *testing.T) {
	previous := soakFloodCounters{TaskRequests: 100, TaskClasses: map[string]int64{"200": 20, "429:task_capacity": 80}, HeartbeatMS: 1_000}
	current := soakFloodCounters{TaskRequests: 2_100, TaskClasses: map[string]int64{"200": 520, "429:task_capacity": 1_560, "429:rate_limited": 20}, HeartbeatMS: 61_000}
	window := soakBackendFloodWindow(previous, current, true, time.UnixMilli(62_000))
	if window.Requests != 2_000 || window.Answered != 500 || window.Refused != 1_480 || !window.Alive || window.HeartbeatAgeMS != 1_000 {
		t.Fatalf("window = %+v", window)
	}
	if len(window.Classes) != 3 || window.Classes["429:rate_limited"] != 20 {
		t.Fatalf("classes = %v", window.Classes)
	}
}

// The anonymous half of the flood sends credentials that name a real installation with a wrong MAC; its answers are counted apart.
func TestSoakBackendFloodWindowCountsTheForgedCredentialsApart(t *testing.T) {
	previous := soakFloodCounters{TaskRequests: 100, TaskClasses: map[string]int64{"200": 20}, ForgedRequests: 10, ForgedClasses: map[string]int64{"404:app_platform_task_unavailable": 10}}
	current := soakFloodCounters{TaskRequests: 2_100, TaskClasses: map[string]int64{"200": 520}, ForgedRequests: 510,
		ForgedClasses: map[string]int64{"404:app_platform_task_unavailable": 480, "429:app_platform_busy": 30}, HeartbeatMS: 61_000}
	window := soakBackendFloodWindow(previous, current, true, time.UnixMilli(62_000))
	if window.ForgedRequests != 500 || window.ForgedClasses["404:app_platform_task_unavailable"] != 470 || window.ForgedClasses["429:app_platform_busy"] != 30 {
		t.Fatalf("window = %+v", window)
	}
	if window.Requests != 2_000 {
		t.Fatalf("the forged requests changed the backend request count: %d", window.Requests)
	}
}

func TestSoakForgedIdentityHeaderNamesTheSameInstallationWithAnotherLeaseAndIsNeverTheOriginal(t *testing.T) {
	grant := appplatform.ServiceIdentityGrant{Format: appplatform.ServiceIdentityFormat, LeaseID: "99999999-9999-4999-8999-999999999999", AppID: "a-app",
		InstallationID: "11111111-1111-4111-8111-111111111111", ControllerEpoch: "22222222-2222-4222-8222-222222222222", Generation: "1", PolicyRevision: "1",
		ReleaseSHA256: strings.Repeat("a", 64), Resources: []string{"tasks.schedule"}, IssuedMS: "1", ExpiresMS: "2", Mac: strings.Repeat("b", 64)}
	raw, _ := json.Marshal(grant)
	original := soakIdentityHeader(raw)
	seen := map[string]bool{original: true}
	for n := 1; n <= 3; n++ {
		forged := soakForgedIdentityHeader(original, n)
		if seen[forged] {
			t.Fatalf("forgery %d repeats", n)
		}
		seen[forged] = true
		decoded, err := decodePlatformServiceIdentityHeader(forged)
		if err != nil {
			t.Fatal(err)
		}
		if named, ok := platformServiceCredentialInstallation(decoded); !ok || named != grant.InstallationID {
			t.Fatalf("forgery %d names %q", n, named)
		}
	}
	if soakForgedIdentityHeader("not base64!", 1) != "" {
		t.Fatal("an unreadable header produced a forgery")
	}
}

func TestSoakBackendFloodRequestsAreTheTaskRoutesClosedBodyAndOneInFourReplaysTheSaturatingTask(t *testing.T) {
	now := time.Now().UnixMilli()
	replays := 0
	for sequence := int64(1); sequence <= 8; sequence++ {
		admission, err := readPlatformTaskAdmission(soakBackendFloodBody("a", sequence), now)
		if err != nil {
			t.Fatalf("request %d does not parse: %v", sequence, err)
		}
		if admission.Handler != appplatform.TaskHandlerBackend {
			t.Fatalf("handler %q", admission.Handler)
		}
		if admission.OccurrenceID == soakReplayOccurrence {
			replays++
			if _, digest, err := appplatform.ValidateTaskPayload([]byte(`{"soak":1}`)); err != nil {
				t.Fatal(err)
			} else if _, replayDigest, err := appplatform.ValidateTaskPayload(admission.Payload); err != nil || replayDigest != digest {
				t.Fatalf("the replay must carry the saturating task's payload: %v", err)
			}
		}
	}
	if replays != 2 {
		t.Fatalf("%d replays in eight requests", replays)
	}
	first, second := soakBackendFloodBody("a", 1), soakBackendFloodBody("b", 1)
	if string(first) == string(second) {
		t.Fatal("two flooding backends submit the same occurrence")
	}
}

// The workload tenants submit their tasks through the task route with their own identity; the window keeps what each submission answered and how
// long it took.
func TestSoakWorkloadSubmitWindowCountsClassesAndSummarisesLatencyAndStartsOverOnTake(t *testing.T) {
	var collector soakSubmitCollector
	for _, ms := range []int64{10, 20, 30, 40} {
		collector.record("200", ms)
	}
	collector.record("429:app_platform_busy", 90)
	window := collector.take()
	if window.Requests != 5 || window.Answered != 4 || window.Classes["200"] != 4 || window.Classes["429:app_platform_busy"] != 1 {
		t.Fatalf("window = %+v", window)
	}
	if window.Latency.Count != 5 || window.Latency.Max != 90 || window.Latency.P95 != 90 || window.Latency.P50 != 30 {
		t.Fatalf("latency = %+v", window.Latency)
	}
	if again := collector.take(); again.Requests != 0 || again.Latency.Count != 0 || len(again.Classes) != 0 {
		t.Fatalf("a second take starts over: %+v", again)
	}
}

func TestSoakWorkloadTaskBodyIsTheRoutesClosedBodyAndNeverRepeatsAnOccurrence(t *testing.T) {
	now := time.Now().UnixMilli()
	seen := map[string]bool{}
	for index := 0; index < 3; index++ {
		for number := 1; number <= 3; number++ {
			admission, err := readPlatformTaskAdmission(soakWorkloadTaskBody(index, number), now)
			if err != nil || admission.Handler != appplatform.TaskHandlerBackend {
				t.Fatalf("body %d/%d: %+v %v", index, number, admission, err)
			}
			if seen[admission.OccurrenceID] {
				t.Fatalf("occurrence %q repeats", admission.OccurrenceID)
			}
			seen[admission.OccurrenceID] = true
		}
	}
}

func TestSoakIdentityHeaderIsTheBase64URLOfTheRawIdentityTheRouteDecodes(t *testing.T) {
	raw := []byte(`{"format":"x","resources":["tasks.schedule"]}`)
	header := soakIdentityHeader(raw)
	if strings.ContainsAny(header, "+/=") {
		t.Fatalf("header %q is not unpadded base64url", header)
	}
	decoded, err := decodePlatformServiceIdentityHeader(header)
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decoded %q, %v", decoded, err)
	}
}

func TestSoakDepthSamplerKeepsTheLargestDepthSinceTheLastTakeAndTheLatestSaturatedCount(t *testing.T) {
	sampler := newSoakDepthSampler()
	sampler.observe(map[string]int{"a": 3, "s": 256}, map[string]int{"a": 2}, []string{"s"})
	sampler.observe(map[string]int{"a": 9, "s": 255}, map[string]int{"a": 5}, []string{"s"})
	sampler.observe(map[string]int{"a": 1, "s": 256}, map[string]int{"a": 1}, []string{"s"})
	maxActive, maxEvent, saturated := sampler.take()
	if maxActive != 256 || maxEvent != 5 || saturated["s"] != 256 {
		t.Fatalf("take = %d %d %v", maxActive, maxEvent, saturated)
	}
	maxActive, maxEvent, _ = sampler.take()
	if maxActive != 0 || maxEvent != 0 {
		t.Fatalf("a second take starts over: %d %d", maxActive, maxEvent)
	}
}

func TestSoakFenceReasonIsReadFromTheAuditDetail(t *testing.T) {
	if got := soakFenceReason(`{"reason":"task_capacity"}`); got != "task_capacity" {
		t.Fatalf("reason %q", got)
	}
	if got := soakFenceReason(`not json`); got != "" {
		t.Fatalf("reason %q", got)
	}
}

// The verdict reads these field names; the Python harness and this file must agree on them.
func TestSoakPlatformWorkEvidenceUsesTheFieldNamesTheVerdictReads(t *testing.T) {
	window := soakWindow{Tasks: &soakTaskWindow{}, Events: &soakEventWindow{}, Quota: &soakQuotaWindow{SaturatedActive: map[string]int{}}, BackendFlood: &soakBackendFlood{Classes: map[string]int64{}, ForgedClasses: map[string]int64{}},
		WorkloadSubmit: &soakWorkloadSubmit{Classes: map[string]int64{}}}
	raw, err := json.Marshal(window)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for section, fields := range map[string][]string{
		"tasks":           {"created", "created_backend", "created_event", "succeeded", "failed", "uncertain", "review_required", "cancelled", "open", "oldest_open_age_ms", "latency_ms", "worker_alive"},
		"events":          {"captured", "capture_errors", "delivered", "max_inflight_per_subscription", "subscriptions_active", "subscriptions_fenced"},
		"quota":           {"max_installation_active", "saturated_active"},
		"backend_flood":   {"requests", "answered", "refused", "classes", "alive", "heartbeat_age_ms"},
		"workload_submit": {"requests", "answered", "classes", "latency_ms"},
	} {
		for _, field := range fields {
			fields, _ := generic[section].(map[string]any)
			if _, ok := fields[field]; !ok {
				t.Errorf("window.%s.%s is missing", section, field)
			}
		}
	}
	raw, _ = json.Marshal(soakPlatformWork{Subscriptions: []soakSubscriptionState{}})
	var work map[string]any
	_ = json.Unmarshal(raw, &work)
	for _, field := range []string{"workload_tenants", "saturated_tenants", "worker_interval_ms", "loaded_latency_ms", "tasks_total", "events_total", "drain", "subscriptions", "saturated_tasks_run"} {
		if _, ok := work[field]; !ok {
			t.Errorf("platform_work.%s is missing", field)
		}
	}
	for field, nested := range map[string][]string{"tasks_total": {"created", "admit_errors", "by_state"}, "events_total": {"captured", "expected_deliveries", "delivered", "capture_errors"},
		"drain": {"seconds", "open_after", "timed_out"}} {
		fields, _ := work[field].(map[string]any)
		for _, name := range nested {
			if _, ok := fields[name]; !ok {
				t.Errorf("platform_work.%s.%s is missing", field, name)
			}
		}
	}
}
