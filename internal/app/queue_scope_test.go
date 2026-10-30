package app

import (
	"net/http"
	"testing"
)

// An administrator chooses whether the queue shows their own runs or everyone's (scope=mine); the
// default stays everyone's. An operator is always scoped to their own, whatever they ask for.
func TestAdminQueueScopeMine(t *testing.T) {
	e := newTasksEnv(t)
	e.addOperator("op")
	e.run("op")
	e.run("op")
	e.run("admin")
	jobs := func(u, path string) map[string]any {
		w := e.do(u, "GET", path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d", u, path, w.Code)
		}
		return decodeBody(t, w)
	}
	count := func(m map[string]any) int { return len(m["jobs"].([]any)) }

	if all := jobs("admin", "/api/admin/batch/jobs"); count(all) != 3 || all["total"] != float64(3) {
		t.Errorf("admin default must stay everyone's: %v", all)
	}
	mine := jobs("admin", "/api/admin/batch/jobs?scope=mine")
	if count(mine) != 1 || mine["total"] != float64(1) {
		t.Errorf("admin scope=mine must list only the admin's runs: %v", mine)
	}
	if op := jobs("op", "/api/admin/batch/jobs?scope=all"); count(op) != 2 || op["total"] != float64(2) {
		t.Errorf("an operator cannot widen the scope: %v", op)
	}

	if s := jobs("admin", "/api/admin/batch/queue"); s["waiting"] != float64(3) {
		t.Errorf("admin summary default must stay everyone's: %v", s)
	}
	s := jobs("admin", "/api/admin/batch/queue?scope=mine")
	if s["waiting"] != float64(1) || s["queue_waiting"] != float64(3) {
		t.Errorf("admin scope=mine tiles must be own, capacity global: %v", s)
	}
	if s := jobs("op", "/api/admin/batch/queue?scope=all"); s["waiting"] != float64(2) {
		t.Errorf("an operator's summary cannot be widened: %v", s)
	}
}
