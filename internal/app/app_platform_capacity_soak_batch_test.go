package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

// The real batch executor under the capacity soak (doc 171, PR 4b; SEC-09 "Portal business executor capacity remains
// available"). The soak runs the Portal's own scheduler (scheduleTick, which scheduleLoop also drives) against a local
// stub of Dify, enqueues batch jobs while the platform is flooded, and measures how long a job takes from its
// enqueue to the moment the executor reaches Dify for it. The stub answers at once, so the figure is the Portal's
// scheduling and dispatch, not the time of a Dify run.
//
// The queue keeps the standing backlog the dispatch decision is measured over. A scheduler admits what it can, so the
// backlog is kept in place the way a busy production queue keeps one: every slot of the budget is held by a long run.
// Each probe job is enqueued at a higher priority and given exactly one more slot for its own lifetime (the budget is
// raised by one, and lowered again before the probe's run ends), so the admission that starts it is the scheduler's
// own and the backlog is never drained.

const (
	// soakBatchHeld is the number of slots held by the long runs of the backlog's jobs.
	soakBatchHeld = 3
	// soakProbeInput is the input that tells the stub which run is a probe.
	soakProbeInput = "soak_probe"
	// soakProbePriority ranks a probe above the backlog (priority 50).
	soakProbePriority = "90"
)

// soakDify is the stub Dify the soak's batch targets point at. A backlog run is a long run: it holds its slot until the
// stub is released. A probe run is answered immediately, once the test has lowered the budget again.
type soakDify struct {
	server  *httptest.Server
	mu      sync.Mutex
	arrived map[string]chan time.Time
	gates   map[string]chan struct{}
	release chan struct{}
	once    sync.Once
}

func newSoakDify() *soakDify {
	d := &soakDify{arrived: map[string]chan time.Time{}, gates: map[string]chan struct{}{}, release: make(chan struct{})}
	d.server = httptest.NewServer(http.HandlerFunc(d.handle))
	return d
}

func (d *soakDify) close() {
	d.once.Do(func() { close(d.release) })
	d.server.Close()
}

func (d *soakDify) url() string { return d.server.URL }

// expect registers a probe before it is enqueued: the channel yields the instant the stub received its run, and the
// gate lets the stub answer it.
func (d *soakDify) expect(key string) (arrived <-chan time.Time, gate chan struct{}) {
	d.mu.Lock()
	defer d.mu.Unlock()
	at, gateChannel := make(chan time.Time, 1), make(chan struct{})
	d.arrived[key], d.gates[key] = at, gateChannel
	return at, gateChannel
}

func (d *soakDify) handle(w http.ResponseWriter, r *http.Request) {
	received := time.Now()
	var body struct {
		Inputs map[string]any `json:"inputs"`
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(raw, &body)
	key, _ := body.Inputs[soakProbeInput].(string)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush() // the stream is open: the run has reached Dify
	}
	if key == "" {
		select { // a long run of the backlog
		case <-d.release:
		case <-r.Context().Done():
			return
		}
	} else {
		d.mu.Lock()
		at, gate := d.arrived[key], d.gates[key]
		d.mu.Unlock()
		if at != nil {
			at <- received
		}
		if gate != nil {
			select {
			case <-gate:
			case <-time.After(30 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
	}
	fmt.Fprintf(w, "data: {\"event\":\"workflow_started\",\"task_id\":\"t\",\"workflow_run_id\":\"run-%s\",\"data\":{}}\n\n", key)
	fmt.Fprintf(w, "data: {\"event\":\"workflow_finished\",\"task_id\":\"t\",\"workflow_run_id\":\"run-%s\",\"data\":{\"status\":\"succeeded\",\"outputs\":{}}}\n\n", key)
}

// soakBatch drives the real scheduler of a Server against a soakDify.
type soakBatch struct {
	t           testing.TB
	s           *Server
	dify        *soakDify
	user        string
	probeTarget int64
	holdTarget  int64
	held        int
	next        int
	// wait bounds how long a probe waits for the scheduler to start it, and for it to finish.
	wait time.Duration
}

func newSoakBatch(t testing.TB, s *Server, user string) *soakBatch {
	t.Helper()
	if err := s.st.UpsertPlugin(difyPluginSlug, "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatal(err)
	}
	b := &soakBatch{t: t, s: s, dify: newSoakDify(), user: user, held: soakBatchHeld, wait: 30 * time.Second}
	target := fmt.Sprintf(`{"base_url":%q,"api_key":"soak"}`, b.dify.url())
	var err error
	if b.probeTarget, err = s.st.CreateTarget(difyPluginSlug, "soak probe", target); err != nil {
		t.Fatal(err)
	}
	if b.holdTarget, err = s.st.CreateTarget(difyPluginSlug, "soak backlog", target); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *soakBatch) close() { b.dify.close() }

func (b *soakBatch) setBudget(slots int) {
	if err := b.s.st.SetSetting("batch_max_concurrent_jobs", strconv.Itoa(slots)); err != nil {
		b.t.Fatal(err)
	}
}

// seed fills the queue: soakBatchHeld long runs take every slot of the budget, and backlog more jobs stay queued
// behind them. The scheduler itself admits the held ones.
func (b *soakBatch) seed(backlog int) {
	b.t.Helper()
	b.setBudget(b.held)
	for i := 0; i < b.held+backlog; i++ {
		if _, err := b.s.st.CreateBatchJob(b.holdTarget, 1, 0, b.user, []map[string]string{{"symbol": "600000"}}, "50"); err != nil {
			b.t.Fatal(err)
		}
	}
	b.s.scheduleTick()
}

// probe enqueues one job and returns the time from its enqueue to the executor's request reaching Dify.
func (b *soakBatch) probe() (time.Duration, error) {
	key := "p" + strconv.Itoa(b.next)
	b.next++
	arrived, gate := b.dify.expect(key)
	began := time.Now()
	id, err := b.s.st.CreateBatchJob(b.probeTarget, 1, 0, b.user, []map[string]string{{soakProbeInput: key}}, soakProbePriority)
	if err != nil {
		return 0, err
	}
	// The probe's slot: the budget is raised for it and lowered before its run ends, so nothing else is admitted into it.
	if err := b.s.st.SetSetting("batch_max_concurrent_jobs", strconv.Itoa(b.held+1)); err != nil {
		return 0, err
	}
	b.s.scheduleTick()
	var took time.Duration
	select {
	case at := <-arrived:
		took = at.Sub(began)
	case <-time.After(b.wait):
		b.setBudget(b.held)
		close(gate)
		return 0, fmt.Errorf("the scheduler did not start probe job %d within %v", id, b.wait)
	}
	b.setBudget(b.held)
	close(gate)
	for deadline := time.Now().Add(b.wait); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if job, ok := b.s.st.GetBatchJob(id); ok && job.Status == "finished" {
			return took, nil
		}
	}
	return 0, fmt.Errorf("probe job %d did not finish", id)
}

func newSoakBatchServer(t *testing.T) *Server {
	t.Helper()
	return &Server{st: newTestStore(t), cfg: &config.Config{SecretKey: rotOldKey}}
}

func TestSoakBatchProbeIsStartedByTheRealSchedulerAndLeavesTheBacklogQueued(t *testing.T) {
	s := newSoakBatchServer(t)
	b := newSoakBatch(t, s, "admin")
	defer b.close()
	b.seed(12)
	if running := s.st.RunningItemCount(""); running != soakBatchHeld {
		t.Fatalf("%d runs held, want %d", running, soakBatchHeld)
	}
	if queued := len(s.queuedItems()); queued != 12 {
		t.Fatalf("the backlog holds %d queued jobs, want 12", queued)
	}
	for i := 0; i < 4; i++ {
		took, err := b.probe()
		if err != nil {
			t.Fatal(err)
		}
		if took <= 0 || took > 5*time.Second {
			t.Fatalf("probe %d took %v", i, took)
		}
	}
	// Four probes later the backlog is exactly as it was: nothing was drained into the probes' slots, and every held
	// run still holds its own.
	if queued := len(s.queuedItems()); queued != 12 {
		t.Fatalf("the backlog holds %d queued jobs after the probes, want 12", queued)
	}
	if running := s.st.RunningItemCount(""); running != soakBatchHeld {
		t.Fatalf("%d runs held after the probes, want %d", running, soakBatchHeld)
	}
	if got := s.st.GetSetting("batch_max_concurrent_jobs", ""); got != strconv.Itoa(soakBatchHeld) {
		t.Fatalf("the budget is left at %q", got)
	}
}

func TestSoakBatchProbeReportsAScheduleThatNeverStarts(t *testing.T) {
	s := newSoakBatchServer(t)
	b := newSoakBatch(t, s, "admin")
	defer b.close()
	// No seed: with the budget at the held slots and no long runs the probe still gets its slot, so break the stub
	// instead: a probe against a closed stub cannot reach Dify and never starts a Dify request.
	b.dify.server.Close()
	b.wait = 500 * time.Millisecond
	b.setBudget(b.held)
	if _, err := b.probe(); err == nil {
		t.Fatal("a probe whose run never reached Dify was measured")
	}
}
