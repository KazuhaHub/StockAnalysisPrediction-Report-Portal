package app

import (
	"fmt"
	"net/http"
	"testing"
)

// My tasks follow-up (ADR 0047): ownership on every run_batch route, finish-time ranking, the
// caller's own queue summary, and capability-gated actions.

// 11. Every run_batch route that takes a job or recurring-task id answers a non-admin non-owner with
// 404, the same answer as an unknown id, so ids do not leak existence.
func TestRunBatchRoutesAnswer404ToNonOwners(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.addOperator("op2")
	job := e.run("op")
	rec := seedRecurring(t, e.s.st, e.target, "op", RecurringTask{Name: "n", Freq: "daily", AtTime: "09:00", Enabled: true})
	jp := fmt.Sprintf("/api/admin/batch/jobs/%d", job)
	rp := fmt.Sprintf("/api/admin/batch/recurring/%d", rec)
	routes := []struct{ method, path, body string }{
		{"GET", jp, ""},
		{"POST", jp + "/cancel", ""},
		{"POST", jp + "/items/cancel", `{"item_ids":[1]}`},
		{"GET", rp, ""},
		{"PUT", rp, `{"name":"x"}`},
		{"POST", rp + "/enable", `{"enabled":false}`},
		{"POST", rp + "/run", ""},
		{"DELETE", rp, ""},
	}
	for _, r := range routes {
		if w := e.do("op2", r.method, r.path, r.body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s as a non-owner = %d, want 404", r.method, r.path, w.Code)
		}
	}
	// Nothing was changed by the refused calls.
	if j, _ := e.s.st.GetBatchJob(job); j.Status != "queued" {
		t.Errorf("job status %q after a refused cancel", j.Status)
	}
	if _, ok := e.s.st.GetRecurringTask(rec); !ok {
		t.Error("recurring task deleted by a non-owner")
	}
	// The owner and an administrator still get through.
	for _, u := range []string{"op", "admin"} {
		if w := e.do(u, "GET", jp, ""); w.Code != http.StatusOK {
			t.Errorf("%s detail = %d", u, w.Code)
		}
		if w := e.do(u, "GET", rp, ""); w.Code != http.StatusOK {
			t.Errorf("%s recurring detail = %d", u, w.Code)
		}
	}
}

// 12. Finished tasks are ranked by finish time before the limit cut.
func TestMyTasksTerminalRankedByFinishTime(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	old := e.run("op")
	if _, err := e.s.st.exec(`UPDATE batch_jobs SET status='finished',succeeded=1,created_at=?,finished_at=? WHERE id=?`,
		"2026-10-01 10:00:00", "2026-10-29 12:00:00", old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // created later than the run, finished earlier
		id := e.export("op", true, exportDone, int64(1793000000000+i))
		if _, err := e.s.st.exec(`UPDATE export_jobs SET finished_at=? WHERE id=?`, int64(1780000001000+i), id); err != nil {
			t.Fatal(err)
		}
	}
	ts := e.tasks("op", "?limit=3")
	if find(ts, "run", fmt.Sprint(old)) == nil {
		t.Fatalf("the late-finishing old run was cut: %+v", ts)
	}
	if ts[0].Kind != "run" {
		t.Errorf("the last to finish should lead: %+v", ts[0])
	}
}

// 13. A non-admin's queue summary reports their own counts; an administrator's stays global.
func TestBatchQueueSummaryIsOwnForNonAdmins(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.addOperator("op2")
	e.run("op")
	e.run("op2")
	e.run("op2")
	running := e.run("op2")
	if _, err := e.s.st.exec(`UPDATE batch_jobs SET status='running' WHERE id=?`, running); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"op", "op2"} {
		id := e.run(u)
		if _, err := e.s.st.exec(`UPDATE batch_jobs SET status='finished',finished_at=? WHERE id=?`, nowStr(), id); err != nil {
			t.Fatal(err)
		}
	}
	get := func(u string) map[string]any {
		w := e.do(u, "GET", "/api/admin/batch/queue", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", u, w.Code)
		}
		return decodeBody(t, w)
	}
	op := get("op")
	if op["waiting"] != float64(1) || op["running"] != float64(0) || op["done_today"] != float64(1) || op["scheduled"] != float64(0) || op["running_rows"] != float64(0) {
		t.Errorf("operator summary: %v", op)
	}
	for _, k := range []string{"budget", "reserved", "my_priority"} {
		if _, ok := op[k]; !ok {
			t.Errorf("%s dropped from the operator summary", k)
		}
	}
	op2 := get("op2")
	if op2["waiting"] != float64(2) || op2["running"] != float64(1) || op2["done_today"] != float64(1) {
		t.Errorf("op2 summary: %v", op2)
	}
	ad := get("admin")
	if ad["waiting"] != float64(3) || ad["running"] != float64(1) || ad["done_today"] != float64(2) {
		t.Errorf("admin summary must stay global: %v", ad)
	}
}

// 14. A run item carries cancel and link only for a caller who holds run_batch.
func TestMyTasksRunActionsNeedRunBatch(t *testing.T) {
	e := newTasksEnv(t)
	id := e.run("bob") // bob holds the plain user role: no run_batch
	g := find(e.tasks("bob", ""), "run", fmt.Sprint(id))
	if g == nil {
		t.Fatal("the run is still listed")
	}
	if g.Cancel != "" || g.Link != "" {
		t.Errorf("run actions for a caller without run_batch: %+v", g)
	}
}

// 15. A reader export is downloadable only while the caller's policy allows export; an
// administrator's own admin export keeps its download.
func TestMyTasksReaderDownloadNeedsExportPolicy(t *testing.T) {
	e := newTasksEnv(t)
	done := e.export("rita", true, exportDone, 100)
	adminDone := e.export("admin", false, exportDone, 101)
	if g := find(e.tasks("rita", ""), "export", done); g.Download == "" {
		t.Fatal("download missing while export is allowed")
	}
	e.setPolicy(e.ou, 0, 100, 1000)
	if g := find(e.tasks("rita", ""), "export", done); g == nil || g.Download != "" {
		t.Fatalf("download offered after export was switched off: %+v", g)
	}
	if g := find(e.tasks("admin", ""), "export", adminDone); g.Download == "" {
		t.Fatal("an administrator's own export lost its download")
	}
}
