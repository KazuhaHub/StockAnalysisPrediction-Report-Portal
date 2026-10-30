package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// My tasks (ADR 0047). The acceptance conditions are numbered as in the ADR.

type tasksEnv struct {
	*readerEnv
	target int64
}

type taskItem struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Ahead      *int   `json:"ahead"`
	Scheduled  bool   `json:"scheduled"`
	Error      string `json:"error"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at"`
	Format     string `json:"format"`
	Bytes      int64  `json:"bytes"`
	Download   string `json:"download"`
	Cancel     string `json:"cancel"`
	Link       string `json:"link"`
}

func newTasksEnv(t *testing.T) *tasksEnv {
	t.Helper()
	e := &tasksEnv{readerEnv: newReaderEnv(t)}
	e.target = seedTarget(t, e.s.st)
	return e
}

func (e *tasksEnv) addOperator(name string) {
	e.t.Helper()
	if err := e.s.st.UpsertUser(User{Username: name, PasswordHash: "hash", Role: "operator", Active: true}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *tasksEnv) run(user string) int64 {
	e.t.Helper()
	id, err := e.s.st.CreateBatchJob(e.target, 1, 0, user, []map[string]string{{"symbol": "600000"}}, "50")
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// export inserts a job row directly: status, creation time and the reader flag are what the tests vary.
func (e *tasksEnv) export(user string, reader bool, status string, createdAt int64) string {
	e.t.Helper()
	p, _ := json.Marshal(exportParams{Name: "x-" + user, Reader: reader})
	j := &exportJob{ID: newExportID(), Source: "reports", Format: "md_zip", Params: string(p), RequestedBy: user,
		Status: status, RowsEstimate: 10, RowsDone: 4, CreatedAt: createdAt}
	if status == exportDone || status == exportFailed {
		j.FinishedAt = createdAt + 1
		if status == exportDone {
			j.Bytes = 3400
		}
	}
	if err := e.s.st.insertExportJob(j); err != nil {
		e.t.Fatal(err)
	}
	return j.ID
}

func (e *tasksEnv) tasks(user, query string) []taskItem {
	e.t.Helper()
	w := e.do(user, "GET", "/api/tasks"+query, "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /api/tasks%s as %s = %d %s", query, user, w.Code, w.Body.String())
	}
	var out struct {
		Tasks []taskItem `json:"tasks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	if out.Tasks == nil {
		e.t.Fatal("tasks must be an array, never null")
	}
	return out.Tasks
}

func find(ts []taskItem, kind, id string) *taskItem {
	for i := range ts {
		if ts[i].Kind == kind && ts[i].ID == id {
			return &ts[i]
		}
	}
	return nil
}

// 1. Only the caller's own tasks, of both kinds.
func TestMyTasksListsOnlyTheCallersOwnRunsAndExports(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.addOperator("op2")
	mine := e.run("op")
	theirs := e.run("op2")
	myExp := e.export("op", true, exportQueued, 100)
	theirExp := e.export("rita", true, exportQueued, 101)

	ts := e.tasks("op", "")
	if find(ts, "run", fmt.Sprint(mine)) == nil || find(ts, "export", myExp) == nil {
		t.Fatalf("own tasks missing: %+v", ts)
	}
	if find(ts, "run", fmt.Sprint(theirs)) != nil || find(ts, "export", theirExp) != nil {
		t.Fatalf("another user's task leaked: %+v", ts)
	}
	if len(ts) != 2 {
		t.Fatalf("got %d tasks, want 2", len(ts))
	}
}

// An export item says its format, and its size once the file exists; a run carries neither.
func TestMyTasksExportItemsCarryFormatAndBytesRunsDoNot(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	runID := e.run("op")
	done := e.export("op", true, exportDone, 100)
	live := e.export("op", true, exportRunning, 101)
	ts := e.tasks("op", "")
	if x := find(ts, "export", done); x == nil || x.Format != "md_zip" || x.Bytes != 3400 {
		t.Fatalf("done export = %+v, want format md_zip and 3400 bytes", x)
	}
	if x := find(ts, "export", live); x == nil || x.Format != "md_zip" || x.Bytes != 0 {
		t.Fatalf("running export = %+v, want format md_zip and no bytes", x)
	}
	var raw struct {
		Tasks []map[string]any `json:"tasks"`
	}
	if err := json.Unmarshal(e.do("op", "GET", "/api/tasks", "").Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, m := range raw.Tasks {
		if m["kind"] != "run" {
			continue
		}
		if _, ok := m["format"]; ok {
			t.Errorf("run %v carries format", m["id"])
		}
		if _, ok := m["bytes"]; ok {
			t.Errorf("run %v carries bytes", m["id"])
		}
	}
	_ = runID
}

// 2. A non-admin sees an export only if it is theirs AND was submitted through the reader endpoints.
func TestMyTasksReaderSeesOnlyReaderExportsUnderTheirName(t *testing.T) {
	e := newTasksEnv(t)
	readerJob := e.export("rita", true, exportDone, 100)
	stray := e.export("rita", false, exportDone, 101)
	ts := e.tasks("rita", "")
	if find(ts, "export", readerJob) == nil {
		t.Fatalf("reader export missing: %+v", ts)
	}
	if find(ts, "export", stray) != nil {
		t.Fatal("a non-reader row under a reader's name must stay out of reach")
	}
}

// 3. An administrator sees their own exports of either kind, and nobody else's.
func TestMyTasksAdminSeesOwnExportsOfEitherKindButNotOthers(t *testing.T) {
	e := newTasksEnv(t)
	adminJob := e.export("admin", false, exportDone, 100)
	adminReader := e.export("admin", true, exportDone, 101)
	other := e.export("rita", true, exportDone, 102)
	ts := e.tasks("admin", "")
	if find(ts, "export", adminJob) == nil || find(ts, "export", adminReader) == nil {
		t.Fatalf("admin's own exports missing: %+v", ts)
	}
	if find(ts, "export", other) != nil {
		t.Fatal("the tray lists the admin's own tasks only")
	}
}

// 4. Download and cancel point at the endpoint family that accepts the job.
func TestMyTasksDownloadAndCancelUrlsPerFamily(t *testing.T) {
	e := newTasksEnv(t)
	adminDone := e.export("admin", false, exportDone, 100)
	adminQueued := e.export("admin", false, exportQueued, 101)
	readerDone := e.export("admin", true, exportDone, 102)
	ts := e.tasks("admin", "")
	if g := find(ts, "export", adminDone); g.Download != "/api/admin/exports/"+adminDone+"/file" || g.Cancel != "" || g.Link != "/manage/exports" {
		t.Errorf("admin done: %+v", g)
	}
	if g := find(ts, "export", adminQueued); g.Cancel != "/api/admin/exports/"+adminQueued+"/cancel" || g.Download != "" {
		t.Errorf("admin queued: %+v", g)
	}
	if g := find(ts, "export", readerDone); g.Download != "/api/exports/"+readerDone+"/file" || g.Link != "/exports" {
		t.Errorf("reader job: %+v", g)
	}
	rq := e.export("rita", true, exportQueued, 103)
	g := find(e.tasks("rita", ""), "export", rq)
	if g.Cancel != "/api/exports/"+rq+"/cancel" {
		t.Errorf("reader cancel: %+v", g)
	}
	// The advertised cancel URL is accepted by the endpoint it names.
	if w := e.do("rita", "POST", g.Cancel, ""); w.Code != http.StatusOK {
		t.Errorf("cancel via the advertised url = %d %s", w.Code, w.Body.String())
	}
	if w := e.do("admin", "POST", find(ts, "export", adminQueued).Cancel, ""); w.Code != http.StatusOK {
		t.Errorf("admin cancel via the advertised url = %d %s", w.Code, w.Body.String())
	}
}

func TestMyTasksRunUrlsAndName(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	id := e.run("op")
	g := find(e.tasks("op", ""), "run", fmt.Sprint(id))
	if g == nil || g.Cancel != fmt.Sprintf("/api/admin/batch/jobs/%d/cancel", id) || g.Link != "/queue" {
		t.Fatalf("run item: %+v", g)
	}
	if g.Name != "My Workflow" || g.Status != "queued" || g.Total != 1 {
		t.Fatalf("run item: %+v", g)
	}
}

// 5. "N tasks ahead" for runs mirrors the queue console; a scheduled run has none.
func TestMyTasksRunAheadCountAndScheduled(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.addOperator("op2")
	e.run("op2")
	second := e.run("op")
	sched := e.run("op")
	if _, err := e.s.st.exec(`UPDATE batch_jobs SET run_at='2099-01-01 00:00:00' WHERE id=?`, sched); err != nil {
		t.Fatal(err)
	}
	ts := e.tasks("op", "")
	g := find(ts, "run", fmt.Sprint(second))
	if g.Ahead == nil || *g.Ahead != 1 {
		t.Fatalf("ahead = %v, want 1 (op2's earlier job)", g.Ahead)
	}
	s := find(ts, "run", fmt.Sprint(sched))
	if s.Ahead != nil || !s.Scheduled {
		t.Fatalf("scheduled run: %+v", s)
	}
}

// 6. "N tasks ahead" for exports: older queued plus the running one; never later ones or other states.
func TestMyTasksExportAheadCount(t *testing.T) {
	e := newTasksEnv(t)
	e.export("bob", true, exportRunning, 10)
	e.export("bob", true, exportQueued, 20)
	e.export("bob", true, exportDone, 5)
	mine := e.export("rita", true, exportQueued, 30)
	e.export("bob", true, exportQueued, 40) // later: not ahead
	g := find(e.tasks("rita", ""), "export", mine)
	if g.Ahead == nil || *g.Ahead != 2 {
		t.Fatalf("ahead = %v, want 2", g.Ahead)
	}
	// Ties on created_at are ordered by id, exactly as the worker claims them.
	a := e.export("rita", true, exportQueued, 50)
	b := e.export("rita", true, exportQueued, 50)
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	ts := e.tasks("rita", "")
	if g := find(ts, "export", lo); g.Ahead == nil || *g.Ahead != 4 { // running + 20 + 30 + 40
		t.Errorf("lo ahead = %v, want 4", g.Ahead)
	}
	if g := find(ts, "export", hi); g.Ahead == nil || *g.Ahead != 5 {
		t.Errorf("hi ahead = %v, want 5", g.Ahead)
	}
}

func TestMyTasksRunningExportHasNoAhead(t *testing.T) {
	e := newTasksEnv(t)
	id := e.export("rita", true, exportRunning, 10)
	if g := find(e.tasks("rita", ""), "export", id); g.Ahead != nil {
		t.Fatalf("running export has ahead %v", *g.Ahead)
	}
}

// 7. Every active task is returned; limit bounds the terminal ones; active first, then newest first.
func TestMyTasksLimitBoundsTerminalOnly(t *testing.T) {
	e := newTasksEnv(t)
	for i := 0; i < 6; i++ {
		e.export("rita", true, exportDone, int64(100+i))
	}
	for i := 0; i < 3; i++ {
		e.export("rita", true, exportQueued, int64(1+i))
	}
	ts := e.tasks("rita", "?limit=4")
	if len(ts) != 3+4 {
		t.Fatalf("got %d, want 3 active + 4 terminal", len(ts))
	}
	for i := 0; i < 3; i++ {
		if ts[i].Status != exportQueued {
			t.Fatalf("active must come first: %+v", ts)
		}
	}
	for i := 3; i+1 < len(ts); i++ {
		if ts[i].CreatedAt < ts[i+1].CreatedAt {
			t.Fatalf("terminal must be newest first: %+v", ts)
		}
	}
	if n := len(e.tasks("rita", "")); n != 3+6 {
		t.Errorf("default limit 10 should return all 9, got %d", n)
	}
	if n := len(e.tasks("rita", "?limit=9999")); n != 9 {
		t.Errorf("max clamp: %d", n)
	}
}

// 8. Terminal runs carry unix-ms times and counts, and no cancel URL.
func TestMyTasksTerminalRunsCarryTimesAndCounts(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	id := e.run("op")
	if _, err := e.s.st.exec(`UPDATE batch_jobs SET status='finished',succeeded=0,failed=1,finished_at=? WHERE id=?`, "2026-10-29 10:00:00", id); err != nil {
		t.Fatal(err)
	}
	g := find(e.tasks("op", ""), "run", fmt.Sprint(id))
	if g.Status != "finished" || g.Done != 1 || g.FinishedAt <= 0 || g.CreatedAt <= 0 || g.Cancel != "" {
		t.Fatalf("%+v", g)
	}
}

// 9. A user with nothing gets a valid empty list; anonymous callers are refused.
func TestMyTasksEmptyListForAUserWithNothing(t *testing.T) {
	e := newTasksEnv(t)
	if ts := e.tasks("bob", ""); len(ts) != 0 {
		t.Fatalf("%+v", ts)
	}
	if w := e.do("", "GET", "/api/tasks", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", w.Code)
	}
}

// 10. The run queue shows a non-admin run_batch holder only their own jobs; total counts only theirs.
func TestBatchJobsListIsOwnOnlyForNonAdmins(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.addOperator("op2")
	mine := e.run("op")
	e.run("op2")
	e.run("op2")
	list := func(user, q string) (ids []float64, total float64) {
		w := e.do(user, "GET", "/api/admin/batch/jobs"+q, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", user, w.Code, w.Body.String())
		}
		body := decodeBody(t, w)
		for _, j := range body["jobs"].([]any) {
			ids = append(ids, j.(map[string]any)["id"].(float64))
		}
		return ids, body["total"].(float64)
	}
	ids, total := list("op", "")
	if len(ids) != 1 || ids[0] != float64(mine) || total != 1 {
		t.Fatalf("operator sees %v total %v", ids, total)
	}
	// Search stays inside their own jobs, even for another user's name.
	if ids, _ := list("op", "?q=op2"); len(ids) != 0 {
		t.Fatalf("search reached another user's jobs: %v", ids)
	}
	if ids, _ := list("op", "?q=op"); len(ids) != 1 {
		t.Fatalf("own search: %v", ids)
	}
	ids, total = list("admin", "")
	if len(ids) != 3 || total != 3 {
		t.Fatalf("admin sees %v total %v", ids, total)
	}
}
