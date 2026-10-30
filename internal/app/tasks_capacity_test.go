package app

import (
	"net/http"
	"testing"
)

// 19. The summary tiles are the caller's own, but capacity is global for everyone: whether a new run
// starts at once depends on the whole queue, not on the caller's share of it. Numbers only.
func TestBatchQueueSummaryCarriesGlobalCapacityForEveryone(t *testing.T) {
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
	if _, err := e.s.st.exec(`UPDATE batch_items SET status='running' WHERE job_id=?`, running); err != nil {
		t.Fatal(err)
	}
	get := func(u string) map[string]any {
		w := e.do(u, "GET", "/api/admin/batch/queue", "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d", u, w.Code)
		}
		return decodeBody(t, w)
	}
	op := get("op")
	if op["running_rows"] != float64(0) || op["waiting"] != float64(1) {
		t.Errorf("own tiles changed: %v", op)
	}
	if op["queue_running_rows"] != float64(1) || op["queue_waiting"] != float64(3) {
		t.Errorf("operator capacity must be global: %v", op)
	}
	ad := get("admin")
	if ad["queue_running_rows"] != ad["running_rows"] || ad["queue_waiting"] != ad["waiting"] {
		t.Errorf("for an administrator the capacity fields equal the tiles: %v", ad)
	}
}
