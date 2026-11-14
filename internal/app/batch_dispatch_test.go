package app

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/batch"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// ---------- reference implementation ----------
//
// The dispatch decision as it was before it was made constant-cost: settings read per job and
// one QueuedItems query per schedulable job. It is kept verbatim (apart from taking `now`) so the
// equivalence tests can prove the production path chooses exactly the same runs.

func (s *Server) refJobFactors(j BatchJob, now time.Time, usage map[string]float64) queue.Factors {
	base, urgent, idle := parsePriority(j.Priority)
	enqueue := j.CreatedAt
	if j.RunAt != "" {
		enqueue = j.RunAt
	}
	wait := now.Sub(time.Unix(parseEnqueueUnix(enqueue), 0)).Seconds()
	age := wait / (s.prioAgeHours() * 3600)
	if age < 0 {
		age = 0
	} else if age > 1 {
		age = 1
	}
	return queue.Factors{
		Base:   float64(base) / baseMax,
		Age:    age,
		Fair:   math.Exp2(-usage[j.CreatedBy]),
		Urgent: urgent,
		Idle:   idle,
	}
}

func (s *Server) refQueuedItems(now time.Time) []queue.Item {
	loc := s.panelLocation()
	w := s.prioWeights()
	usage := s.userUsage(now)
	jobs := s.st.QueuedJobs()
	items := make([]queue.Item, 0, len(jobs))
	for _, j := range jobs {
		if !runAtDue(j.RunAt, now) {
			continue
		}
		if invertBlocksNow(j.RunPreset, now, loc) {
			continue
		}
		f := s.refJobFactors(j, now, usage)
		items = append(items, queue.Item{ID: j.ID, Score: w.Score(f), Urgent: f.Urgent})
	}
	return items
}

func (s *Server) refItemCandidates(now time.Time) ([]queue.Item, map[int64]candMeta) {
	loc := s.panelLocation()
	w := s.prioWeights()
	usage := s.userUsage(now)
	var cands []queue.Item
	meta := map[int64]candMeta{}
	for _, j := range s.st.SchedulableJobs() {
		if !runAtDue(j.RunAt, now) {
			continue
		}
		if invertBlocksNow(j.RunPreset, now, loc) {
			continue
		}
		window := j.Concurrency
		if window < 1 {
			window = 1
		}
		if window -= j.runningItems; window <= 0 {
			continue
		}
		items, err := s.st.QueuedItems(j.ID)
		if err != nil || len(items) == 0 {
			continue
		}
		f := s.refJobFactors(j, now, usage)
		score := w.Score(f)
		for i, it := range items {
			if i >= window {
				break
			}
			cands = append(cands, queue.Item{ID: it.ID, Score: score, Urgent: f.Urgent})
			meta[it.ID] = candMeta{job: j, item: it}
		}
	}
	return cands, meta
}

// ---------- statement-counting driver ----------

type stmtCounter struct{ n atomic.Int64 }

type countingConnector struct {
	inner driver.Driver
	dsn   string
	c     *stmtCounter
}

func (cc countingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := cc.inner.Open(cc.dsn)
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, c: cc.c}, nil
}
func (cc countingConnector) Driver() driver.Driver { return cc.inner }

type countingConn struct {
	driver.Conn
	c *stmtCounter
}

func (c *countingConn) Serialize() ([]byte, error) { return c.Conn.(sqliteImageConn).Serialize() }
func (c *countingConn) Deserialize(b []byte) error { return c.Conn.(sqliteImageConn).Deserialize(b) }

func (c *countingConn) Prepare(q string) (driver.Stmt, error) {
	c.c.n.Add(1)
	return c.Conn.Prepare(q)
}
func (c *countingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.c.n.Add(1)
	if p, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}
func (c *countingConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := c.Conn.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	rows, err := qc.QueryContext(ctx, q, args)
	if !errors.Is(err, driver.ErrSkip) {
		c.c.n.Add(1)
	}
	return rows, err
}
func (c *countingConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := c.Conn.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	res, err := ec.ExecContext(ctx, q, args)
	if !errors.Is(err, driver.ErrSkip) {
		c.c.n.Add(1)
	}
	return res, err
}
func (c *countingConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, o)
}

// newCountingStore is newTestStore with every statement counted.
func newCountingStore(t testing.TB) (*Store, *stmtCounter) {
	t.Helper()
	img, err := testStoreImage()
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	probe, err := sql.Open(portalSQLiteDriver, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	inner := probe.Driver()
	probe.Close()
	counter := &stmtCounter{}
	db := sql.OpenDB(countingConnector{inner: inner, dsn: ":memory:", c: counter})
	db.SetMaxOpenConns(1)
	st := &Store{db: db, driver: "sqlite"}
	if err := withSQLiteConn(db, func(c sqliteImageConn) error { return c.Deserialize(img) }); err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, counter
}

// ---------- backlog builder ----------

func seedDispatchTarget(t testing.TB, st *Store) int64 {
	t.Helper()
	if err := st.UpsertPlugin("dify", "Dify", "1.0.0", "{}", "bundled"); err != nil {
		t.Fatalf("UpsertPlugin: %v", err)
	}
	id, err := st.CreateTarget("dify", "My Workflow", `{"base_url":"x","api_key":"k"}`)
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}
	return id
}

type backlogJob struct {
	user        string
	priority    string
	age         time.Duration // created this long before `now`
	runAtOffset time.Duration // 0 = unscheduled; else run_at = now+offset
	preset      string
	rows        int
	concurrency int
	running     int // rows already running
}

const (
	presetInsideNow  = `{"freq":"daily","intervals":[{"start":{"time":"09:00"},"stop":{"time":"18:00"}}],"on_overrun":"continue","invert":true}`
	presetOutsideNow = `{"freq":"daily","intervals":[{"start":{"time":"20:00"},"stop":{"time":"23:00"}}],"on_overrun":"continue","invert":true}`
)

func dispatchNow() time.Time {
	return time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
}

func seedBacklog(t testing.TB, st *Store, now time.Time, jobs []backlogJob) {
	t.Helper()
	st.SetSetting("timezone", "UTC")
	tgt := seedDispatchTarget(t, st)
	ts := func(x time.Time) string { return x.In(time.Local).Format("2006-01-02 15:04:05") }
	for _, bj := range jobs {
		rows := bj.rows
		if rows == 0 {
			rows = 1
		}
		conc := bj.concurrency
		if conc == 0 {
			conc = 1
		} else if conc < 0 {
			conc = 0 // a legacy row with no producer window; the scheduler treats it as 1
		}
		data := make([]map[string]string, rows)
		for i := range data {
			data[i] = map[string]string{"x": fmt.Sprint(i)}
		}
		id, err := st.CreateBatchJob(tgt, conc, 0, bj.user, data, bj.priority)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.exec(`UPDATE batch_jobs SET created_at=? WHERE id=?`, ts(now.Add(-bj.age)), id); err != nil {
			t.Fatal(err)
		}
		if bj.runAtOffset != 0 {
			st.ScheduleJob(id, ts(now.Add(bj.runAtOffset)))
		}
		if bj.preset != "" {
			if _, err := st.exec(`UPDATE batch_jobs SET run_preset=? WHERE id=?`, bj.preset, id); err != nil {
				t.Fatal(err)
			}
		}
		its := st.BatchJobItems(id)
		for i := 0; i < bj.running && i < len(its); i++ {
			st.MarkItemRunning(its[i].ID)
		}
		if bj.running > 0 {
			st.MarkJobRunning(id)
		}
	}
}

func mixedBacklog(n int) []backlogJob {
	users := []string{"alice", "bob", "carol", "dave"}
	prios := []string{"50", "10", "90", "urgent", "idle", "normal", "low", "high", "35"}
	presets := []string{"", "", "", presetInsideNow, presetOutsideNow}
	out := make([]backlogJob, n)
	for i := range out {
		bj := backlogJob{
			user:        users[i%len(users)],
			priority:    prios[i%len(prios)],
			age:         time.Duration(i*37%2000) * time.Minute,
			rows:        1 + i%4,
			concurrency: 1 + i%3,
			preset:      presets[i%len(presets)],
		}
		switch i % 7 {
		case 3:
			bj.runAtOffset = time.Duration(i+1) * time.Minute // future
		case 5:
			bj.runAtOffset = -time.Duration(i+1) * time.Minute // past
		}
		if i%9 == 4 {
			bj.concurrency = -1
		}
		if i%11 == 0 {
			bj.running = 1
		}
		out[i] = bj
	}
	return out
}

// seedUsageHistory adds finished jobs so users have different recent usage.
func seedUsageHistory(t testing.TB, st *Store, now time.Time) {
	t.Helper()
	tgt := seedDispatchTarget(t, st)
	for user, n := range map[string]int{"alice": 12, "bob": 1, "dave": 5} {
		for i := 0; i < n; i++ {
			id, err := st.CreateBatchJob(tgt, 1, 0, user, []map[string]string{{"x": "1"}}, "50")
			if err != nil {
				t.Fatal(err)
			}
			created := now.Add(-time.Duration(i*9+1) * time.Hour).In(time.Local).Format("2006-01-02 15:04:05")
			if _, err := st.exec(`UPDATE batch_jobs SET created_at=?, status='finished' WHERE id=?`, created, id); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// ---------- equivalence ----------

func TestDispatchDecisionMatchesReference(t *testing.T) {
	now := dispatchNow()
	cases := []struct {
		name     string
		jobs     []backlogJob
		settings map[string]string
	}{
		{"empty", nil, nil},
		{"single", []backlogJob{{user: "alice", priority: "50", age: time.Hour}}, nil},
		{"base priorities", []backlogJob{
			{user: "a", priority: "5", age: time.Hour}, {user: "a", priority: "95", age: time.Hour}, {user: "b", priority: "50", age: time.Hour},
		}, nil},
		{"urgent and idle", []backlogJob{
			{user: "a", priority: "urgent", age: time.Minute}, {user: "b", priority: "idle", age: 30 * time.Hour},
			{user: "c", priority: "80", age: time.Hour}, {user: "d", priority: "urgent", age: time.Hour},
		}, nil},
		{"scheduled future and past", []backlogJob{
			{user: "a", priority: "50", age: 48 * time.Hour, runAtOffset: 2 * time.Hour},
			{user: "a", priority: "50", age: 48 * time.Hour, runAtOffset: -2 * time.Hour},
			{user: "b", priority: "50", age: time.Hour, runAtOffset: -time.Minute},
		}, nil},
		{"inverted presets", []backlogJob{
			{user: "a", priority: "50", age: time.Hour, preset: presetInsideNow},
			{user: "b", priority: "50", age: time.Hour, preset: presetOutsideNow},
			{user: "c", priority: "50", age: time.Hour, preset: `{not json`},
		}, nil},
		{"windows and running rows", []backlogJob{
			{user: "a", priority: "50", age: time.Hour, rows: 6, concurrency: 3, running: 1},
			{user: "b", priority: "50", age: time.Hour, rows: 2, concurrency: 5, running: 2},
			{user: "c", priority: "50", age: time.Hour, rows: 4, concurrency: 2, running: 2},
		}, nil},
		{"aging saturates", []backlogJob{
			{user: "a", priority: "50", age: 1 * time.Hour}, {user: "b", priority: "50", age: 12 * time.Hour},
			{user: "c", priority: "50", age: 24 * time.Hour}, {user: "d", priority: "50", age: 90 * time.Hour},
		}, map[string]string{"batch_prio_age_hours": "36"}},
		{"zero concurrency", []backlogJob{
			{user: "a", priority: "50", age: time.Hour, rows: 4, concurrency: -1},
			{user: "b", priority: "50", age: time.Hour, rows: 3, concurrency: -1, running: 1},
			{user: "c", priority: "50", age: time.Hour, rows: 3, concurrency: 2},
		}, nil},
		{"custom weights and halflife", mixedBacklog(30), map[string]string{
			"batch_prio_w_base": "700", "batch_prio_w_age": "2500", "batch_prio_w_fair": "300", "batch_prio_fair_halflife_hours": "48",
		}},
		{"mixed 60", mixedBacklog(60), nil},
		{"mixed 200", mixedBacklog(200), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			for k, v := range tc.settings {
				st.SetSetting(k, v)
			}
			seedBacklog(t, st, now, tc.jobs)
			seedUsageHistory(t, st, now)
			s := &Server{st: st}

			want, got := s.refQueuedItems(now), s.queuedItemsAt(now)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("queuedItems differ\nwant %+v\n got %+v", want, got)
			}

			wantC, wantM := s.refItemCandidates(now)
			gotC, gotM := s.itemCandidatesAt(now)
			if !reflect.DeepEqual(wantC, gotC) {
				t.Fatalf("candidates differ\nwant %+v\n got %+v", wantC, gotC)
			}
			if !reflect.DeepEqual(wantM, gotM) {
				t.Fatalf("candidate meta differ")
			}
			for _, plan := range []queue.Plan{{Budget: 1}, {Budget: 4, Reserved: 1}, {Budget: 12, Reserved: 3}} {
				for _, inFlight := range []int{0, 2} {
					w := queue.Admit(wantC, inFlight, plan)
					g := queue.Admit(gotC, inFlight, plan)
					if !reflect.DeepEqual(w, g) {
						t.Fatalf("admission differs for %+v/%d\nwant %+v\n got %+v", plan, inFlight, w, g)
					}
				}
			}
			if len(tc.jobs) >= 30 {
				if len(want) == 0 || len(gotC) == 0 {
					t.Fatalf("degenerate backlog: queued=%d candidates=%d", len(want), len(gotC))
				}
				if all := st.QueuedJobs(); len(want) >= len(all) {
					t.Fatalf("no queued job was filtered out (%d of %d kept)", len(want), len(all))
				}
			}
		})
	}
}

// admitAtLocked must start exactly the runs the reference chooses.
func TestAdmitStartsSameRunsAsReference(t *testing.T) {
	now := dispatchNow()
	st := newTestStore(t)
	st.SetSetting("batch_max_concurrent_jobs", "30")
	st.SetSetting("batch_reserved_slots", "2")
	seedBacklog(t, st, now, mixedBacklog(40))
	seedUsageHistory(t, st, now)
	s := &Server{st: st}
	release := make(chan struct{})
	seeded := st.RunningItemCount("") // rows seeded as running have no goroutine and never finish
	s.buildProv = func(BatchJob, func(string, string, string)) (batch.Provider, error) {
		return blockingProv(release), nil
	}
	// Runs once the test is over, pass or fail: unblock the provider goroutines and let them drain
	// so none outlives the store.
	t.Cleanup(func() {
		close(release)
		deadline := time.Now().Add(5 * time.Second)
		for st.RunningItemCount("") > seeded && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})

	runningIDs := func() map[int64]bool {
		out := map[int64]bool{}
		for _, j := range st.SchedulableJobs() {
			for _, it := range st.BatchJobItems(j.ID) {
				if it.Status == "running" {
					out[it.ID] = true
				}
			}
		}
		return out
	}
	before := runningIDs()
	wantC, _ := s.refItemCandidates(now)
	want := map[int64]bool{}
	for _, it := range queue.Admit(wantC, st.RunningItemCount(""), queue.Plan{Budget: 30, Reserved: 2}) {
		want[it.ID] = true
	}
	if len(want) == 0 {
		t.Fatal("degenerate: reference admitted nothing")
	}
	s.admitAtLocked(now)
	got := map[int64]bool{}
	for id := range runningIDs() {
		if !before[id] {
			got[id] = true
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("admitted runs differ\nwant %v\n got %v", want, got)
	}
}

// ---------- window query ----------

// windowFixture seeds jobs that pin down exactly which rows QueuedItemWindows returns, and returns
// the expected row ids per job (by position), under target tgt. user scopes the rows on a shared
// database.
func windowFixture(t testing.TB, st *Store, tgt int64, user string) (jobIDs []int64, want map[int64][]int64) {
	t.Helper()
	want = map[int64][]int64{}
	mk := func(conc, rows int, status string, running int, wantQueued int) {
		data := make([]map[string]string, rows)
		for i := range data {
			data[i] = map[string]string{"x": fmt.Sprint(i)}
		}
		id, err := st.CreateBatchJob(tgt, conc, 0, user, data, "50")
		if err != nil {
			t.Fatal(err)
		}
		if status != "queued" {
			if _, err := st.exec(`UPDATE batch_jobs SET status=? WHERE id=?`, status, id); err != nil {
				t.Fatal(err)
			}
		}
		items := st.BatchJobItems(id)
		for i := 0; i < running; i++ {
			if !st.MarkItemRunning(items[i].ID) {
				t.Fatal("MarkItemRunning")
			}
		}
		if wantQueued > 0 {
			for _, it := range items[running : running+wantQueued] {
				want[id] = append(want[id], it.ID)
			}
		}
		jobIDs = append(jobIDs, id)
	}
	mk(0, 4, "queued", 0, 1)     // legacy zero concurrency: window 1
	mk(1, 4, "queued", 0, 1)     // window 1
	mk(3, 5, "queued", 0, 3)     // window 3 of 5
	mk(3, 2, "queued", 0, 2)     // fewer rows than the window
	mk(3, 5, "running", 1, 3)    // a running job with one row in flight: window counts queued rows
	mk(2, 3, "finished", 0, 0)   // not schedulable
	mk(2, 3, "cancelling", 0, 0) // not schedulable
	mk(2, 3, "cancelled", 0, 0)  // not schedulable
	mk(2, 2, "running", 2, 0)    // nothing queued
	return jobIDs, want
}

// checkQueuedItemWindows asserts the exact rows (and so the exact count) QueuedItemWindows returns
// for the fixture jobs, that they equal the first window rows of the per-job QueuedItems, and that
// SchedulableJobs reports the in-flight counts the window logic relies on.
func checkQueuedItemWindows(t testing.TB, st *Store, jobIDs []int64, want map[int64][]int64) {
	t.Helper()
	got, err := st.QueuedItemWindows()
	if err != nil {
		t.Fatalf("QueuedItemWindows: %v", err)
	}
	for _, id := range jobIDs {
		var ids []int64
		for _, it := range got[id] {
			ids = append(ids, it.ID)
		}
		if !reflect.DeepEqual(ids, want[id]) && !(len(ids) == 0 && len(want[id]) == 0) {
			t.Errorf("job %d: window rows = %v, want %v", id, ids, want[id])
		}
		// Equal to the per-job read, truncated to the producer window.
		per, err := st.QueuedItems(id)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(want[id]); n > len(per) {
			t.Fatalf("job %d: fixture wants %d rows, only %d queued", id, n, len(per))
		} else if n > 0 {
			if len(got[id]) != n {
				t.Errorf("job %d: window has %d rows, want %d", id, len(got[id]), n)
				continue
			}
			for i, it := range per[:n] {
				if got[id][i].ID != it.ID || got[id][i].RowIndex != it.RowIndex || !reflect.DeepEqual(got[id][i].Inputs, it.Inputs) {
					t.Errorf("job %d row %d: window %+v != per-job %+v", id, i, got[id][i], it)
				}
			}
		}
	}
	// SchedulableJobs: only queued/running jobs, with the running count.
	sched := map[int64]BatchJob{}
	for _, j := range st.SchedulableJobs() {
		sched[j.ID] = j
	}
	for _, id := range jobIDs {
		j, ok := sched[id]
		job, _ := st.GetBatchJob(id)
		switch job.Status {
		case "queued", "running":
			if !ok {
				t.Errorf("job %d (%s) missing from SchedulableJobs", id, job.Status)
				continue
			}
			_, running, _, _, _, _ := st.LiveJobCounts(id)
			if j.runningItems != running {
				t.Errorf("job %d runningItems = %d, want %d", id, j.runningItems, running)
			}
		default:
			if ok {
				t.Errorf("job %d (%s) must not be schedulable", id, job.Status)
			}
		}
	}
}

func TestQueuedItemWindowsReturnsExactlyTheWindow(t *testing.T) {
	st := newTestStore(t)
	ids, want := windowFixture(t, st, seedDispatchTarget(t, st), "win")
	checkQueuedItemWindows(t, st, ids, want)
}

// ---------- query count ----------

func decisionQueries(t *testing.T, jobs int, ref bool) (queued, cands int64) {
	t.Helper()
	now := dispatchNow()
	st, counter := newCountingStore(t)
	seedBacklog(t, st, now, mixedBacklog(jobs))
	seedUsageHistory(t, st, now)
	s := &Server{st: st}

	before := counter.n.Load()
	if ref {
		s.refQueuedItems(now)
	} else {
		s.queuedItemsAt(now)
	}
	queued = counter.n.Load() - before
	before = counter.n.Load()
	if ref {
		s.refItemCandidates(now)
	} else {
		s.itemCandidatesAt(now)
	}
	cands = counter.n.Load() - before
	return
}

func TestDispatchDecisionQueryCountIsConstant(t *testing.T) {
	baseQ, baseC := decisionQueries(t, 1, false)
	if baseQ == 0 || baseC == 0 {
		t.Fatalf("counter saw no statements (queued=%d candidates=%d): the seam is broken", baseQ, baseC)
	}
	for _, n := range []int{0, 50, 200} {
		q, c := decisionQueries(t, n, false)
		if q != baseQ || c != baseC {
			t.Errorf("%d queued jobs: queuedItems=%d candidates=%d queries, want %d and %d (as for 1 job)", n, q, c, baseQ, baseC)
		}
	}
	// The seam must be able to see the problem: the reference grows with the backlog.
	if rq, rc := decisionQueries(t, 200, true); rq <= baseQ || rc <= baseC {
		t.Errorf("reference at 200 jobs issued %d/%d queries, expected more than %d/%d", rq, rc, baseQ, baseC)
	} else {
		t.Logf("reference at 200 jobs: queuedItems=%d itemCandidates=%d", rq, rc)
	}
	t.Logf("queries per decision: queuedItems=%d itemCandidates=%d", baseQ, baseC)
}

// The age-saturation setting is read once per decision, however many jobs are waiting.
func TestDispatchDecisionReadsAgeSettingOnce(t *testing.T) {
	now := dispatchNow()
	st := newTestStore(t)
	seedBacklog(t, st, now, mixedBacklog(50))
	s := &Server{st: st}
	reads := map[string]int{}
	st.onSettingRead = func(k string) { reads[k]++ }
	for name, run := range map[string]func(){
		"queuedItems":    func() { s.queuedItemsAt(now) },
		"itemCandidates": func() { s.itemCandidatesAt(now) },
	} {
		clear(reads)
		run()
		if reads["batch_prio_age_hours"] != 1 {
			t.Errorf("%s read batch_prio_age_hours %d times, want 1 (reads: %v)", name, reads["batch_prio_age_hours"], reads)
		}
	}
	clear(reads)
	s.refQueuedItems(now)
	if reads["batch_prio_age_hours"] <= 1 {
		t.Errorf("reference read the setting %d times: the check cannot see per-job reads", reads["batch_prio_age_hours"])
	}
}

// ---------- benchmarks ----------

func benchDecision(b *testing.B, ref bool) {
	now := dispatchNow()
	st, _ := newCountingStore(b)
	seedBacklog(b, st, now, mixedBacklog(200))
	seedUsageHistory(b, st, now)
	s := &Server{st: st}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ref {
			s.refQueuedItems(now)
			s.refItemCandidates(now)
		} else {
			s.queuedItemsAt(now)
			s.itemCandidatesAt(now)
		}
	}
}

func BenchmarkDispatchDecision200Reference(b *testing.B) { benchDecision(b, true) }
func BenchmarkDispatchDecision200(b *testing.B)          { benchDecision(b, false) }
