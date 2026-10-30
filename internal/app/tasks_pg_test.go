package app

import (
	"encoding/json"
	"os"
	"testing"
)

// The my-tasks queries on Postgres (ADR 0047): the own-filtered listings and the export "ahead" count.
// Skipped unless TEST_POSTGRES_DSN is set. The database is shared, so the test only ever touches rows
// under its own user names and removes them afterwards.
func TestPostgresMyTasksQueries(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping Postgres integration test")
	}
	st, err := OpenStore("postgres", dsn)
	if err != nil {
		t.Fatalf("OpenStore postgres: %v", err)
	}
	const me, other = "pg_tasks_me", "pg_tasks_other"
	clean := func() {
		st.exec(`DELETE FROM export_jobs WHERE requested_by IN (?,?)`, me, other)
		st.exec(`DELETE FROM batch_items WHERE job_id IN (SELECT id FROM batch_jobs WHERE created_by IN (?,?))`, me, other)
		st.exec(`DELETE FROM batch_jobs WHERE created_by IN (?,?)`, me, other)
	}
	clean()
	t.Cleanup(clean)

	// Exports: reader and non-reader rows of two requesters at fixed creation times.
	add := func(user string, reader bool, status string, createdAt, finishedAt int64) *exportJob {
		p, _ := json.Marshal(exportParams{Name: "pg-" + user, Reader: reader})
		j := &exportJob{ID: newExportID(), Source: "reports", Format: "md_zip", Params: string(p), RequestedBy: user,
			Status: status, CreatedAt: createdAt, FinishedAt: finishedAt}
		if err := st.insertExportJob(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	add(other, true, exportRunning, 1, 0)
	add(other, true, exportQueued, 2, 0)
	mine := add(me, true, exportQueued, 3, 0)
	add(me, false, exportDone, 4, 40) // an administrator-style row: hidden from a reader-only listing
	doneOld := add(me, true, exportDone, 5, 60)
	doneNew := add(me, true, exportDone, 6, 50)

	jobs, err := st.listOwnExportTasks(me, true, 5)
	if err != nil {
		t.Fatalf("listOwnExportTasks: %v", err)
	}
	ids := map[string]bool{}
	for _, j := range jobs {
		ids[j.ID] = true
	}
	if !ids[mine.ID] || !ids[doneOld.ID] || !ids[doneNew.ID] || len(jobs) != 3 {
		t.Fatalf("reader-only own listing = %d jobs %v", len(jobs), ids)
	}
	if all, err := st.listOwnExportTasks(me, false, 5); err != nil || len(all) != 4 {
		t.Fatalf("admin-style own listing = %d (%v), want 4", len(all), err)
	}
	// Ranked by finish time: the one that finished last comes first, whatever its creation order.
	if one, _ := st.listOwnExportTasks(me, true, 1); len(one) != 2 || one[1].ID != doneOld.ID {
		t.Fatalf("limit 1 kept %v", one)
	}

	// ahead: the running job and the earlier queued one, not later ones.
	n, err := st.exportJobsAhead(mine)
	if err != nil {
		t.Fatalf("exportJobsAhead: %v", err)
	}
	if n < 2 { // other deployments' rows may add to it; ours alone contribute the two
		t.Fatalf("ahead = %d, want at least 2", n)
	}

	// Runs: own-only listing and total, and the task listing without a total.
	tgt := seedTarget(t, st)
	row := []map[string]string{{"symbol": "600000"}}
	if _, err := st.CreateBatchJob(tgt, 1, 0, me, row, "50"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBatchJob(tgt, 1, 0, other, row, "50"); err != nil {
		t.Fatal(err)
	}
	runs, total := st.ListOwnQueueJobs(me, 10, "")
	if len(runs) != 1 || total != 1 || runs[0].CreatedBy != me {
		t.Fatalf("ListOwnQueueJobs = %d jobs, total %d", len(runs), total)
	}
	if runs, total := st.ListOwnQueueJobs(me, 10, "pg_tasks_oth"); len(runs) != 0 || total != 1 {
		t.Fatalf("search leaked across owners: %d jobs, total %d", len(runs), total)
	}
	if own := st.ListOwnTaskJobs(me, 10); len(own) != 1 || own[0].CreatedBy != me {
		t.Fatalf("ListOwnTaskJobs = %+v", own)
	}
}
