package app

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/queue"
)

// My tasks (ADR 0047): one list of the caller's own runs and exports, for the top-bar tray.

const (
	tasksDefaultLimit = 10
	tasksMaxLimit     = 50
	taskDetailMax     = 60
)

// runCounts are a run's per-row outcomes, so the client can tell a clean finish from a partial one.
type runCounts struct {
	Succeeded int `json:"succeeded"`
	Partial   int `json:"partial"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// taskJSON is the wire shape of one task. Status is the native status of its kind (run: queued,
// running, cancelling, finished, cancelled, expired; export: queued, running, done, failed, canceled,
// expired). Times are unix milliseconds for both kinds.
type taskJSON struct {
	Kind       string     `json:"kind"` // run | export
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Detail     string     `json:"detail,omitempty"` // runs: a short preview of the first row's inputs
	Status     string     `json:"status"`
	Done       int64      `json:"done"`
	Total      int64      `json:"total"`
	Ahead      *int       `json:"ahead,omitempty"` // queued and counted only; a count, never who
	Scheduled  bool       `json:"scheduled,omitempty"`
	Error      string     `json:"error,omitempty"`
	Format     string     `json:"format,omitempty"` // exports only: md_zip | csv | jsonl
	Bytes      int64      `json:"bytes,omitempty"`  // exports only: the file size once done
	Counts     *runCounts `json:"counts,omitempty"`
	CreatedAt  int64      `json:"created_at"`
	FinishedAt int64      `json:"finished_at"`
	Download   string     `json:"download,omitempty"`
	Cancel     string     `json:"cancel,omitempty"`
	Link       string     `json:"link"`
}

// rank orders tasks: in flight ones by when they were submitted, finished ones by when they finished.
func (t *taskJSON) rank() int64 {
	if !t.active() && t.FinishedAt > 0 {
		return t.FinishedAt
	}
	return t.CreatedAt
}

func (t *taskJSON) active() bool {
	switch t.Status {
	case "queued", "running", "cancelling":
		return true
	}
	return false
}

func (s *Server) apiMyTasks(w http.ResponseWriter, r *http.Request, user string) {
	limit := tasksDefaultLimit
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > tasksMaxLimit {
		limit = tasksMaxLimit
	}
	runs := s.myRunTasks(user, limit)
	exports, err := s.myExportTasks(user, limit)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "database error")
		return
	}
	all := append(runs, exports...)
	sort.SliceStable(all, func(i, j int) bool {
		if ai, aj := all[i].active(), all[j].active(); ai != aj {
			return ai
		}
		ki, kj := all[i].rank(), all[j].rank()
		if ki != kj {
			return ki > kj
		}
		return all[i].ID > all[j].ID
	})
	out := make([]taskJSON, 0, len(all))
	terminal := 0
	for _, t := range all {
		if !t.active() {
			if terminal >= limit {
				continue
			}
			terminal++
		}
		out = append(out, t)
	}
	writeJSON(w, map[string]any{"tasks": out})
}

// msFromLocal converts a batch_jobs timestamp (local "2006-01-02 15:04:05", "" = unset) to unix ms.
func msFromLocal(ts string) int64 {
	if ts == "" {
		return 0
	}
	return parseEnqueueUnix(ts) * 1000
}

func (s *Server) myRunTasks(user string, limit int) []taskJSON {
	jobs := s.st.ListOwnTaskJobs(user, limit)
	if len(jobs) == 0 {
		return nil
	}
	// Cancelling and opening a run go through run_batch routes; a caller who no longer holds the
	// permission still sees the run's history, without actions that would be refused.
	canRun := s.isAdmin(user) || s.hasPerm(user, PermRunBatch)
	ids := make([]int64, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	firstInputs := s.st.JobsFirstInputs(ids)
	var waiting []queue.Item
	for _, j := range jobs {
		if j.Status == "queued" {
			waiting = s.queuedItems() // only paid for when something of theirs is waiting
			break
		}
	}
	now := time.Now()
	names := map[int64]string{}
	out := make([]taskJSON, 0, len(jobs))
	for _, j := range jobs {
		name, ok := names[j.TargetID]
		if !ok {
			if tg, found := s.st.GetTarget(j.TargetID); found {
				name = tg.Name
			}
			names[j.TargetID] = name
		}
		c := runCounts{Succeeded: j.Succeeded, Partial: j.Partial, Failed: j.Failed}
		switch j.Status {
		case "running", "cancelling":
			_, _, c.Succeeded, c.Partial, c.Failed, c.Cancelled = s.st.LiveJobCounts(j.ID)
		case "finished", "cancelled":
			if c.Cancelled = j.Total - c.Succeeded - c.Partial - c.Failed; c.Cancelled < 0 {
				c.Cancelled = 0
			}
		}
		t := taskJSON{
			Kind: "run", ID: itoa64(j.ID), Name: name, Detail: inputsGist(firstInputs[j.ID]), Status: j.Status,
			Done: int64(c.Succeeded + c.Partial + c.Failed + c.Cancelled), Total: int64(j.Total), Counts: &c,
			CreatedAt: msFromLocal(j.CreatedAt), FinishedAt: msFromLocal(j.FinishedAt),
		}
		if canRun {
			t.Link = "/queue"
		}
		if canRun && (j.Status == "queued" || j.Status == "running") {
			t.Cancel = "/api/admin/batch/jobs/" + itoa64(j.ID) + "/cancel"
		}
		if j.Status == "queued" {
			// Same rules as the queue console: a window-blocked or not-yet-due job is scheduled, not waiting.
			if s.jobWindowBlocked(j, now, 0, 0) || !runAtDue(j.RunAt, now) {
				t.Scheduled = true
			} else {
				n := queue.Ahead(itemByID(waiting, j.ID), waiting)
				t.Ahead = &n
			}
		}
		out = append(out, t)
	}
	return out
}

func (s *Server) myExportTasks(user string, limit int) ([]taskJSON, error) {
	jobs, err := s.st.listOwnExportTasks(user, !s.isAdmin(user), limit)
	if err != nil {
		return nil, err
	}
	admin := s.isAdmin(user)
	exportAllowed := admin
	if !admin {
		if pol, err := s.st.readerExportPolicy(user); err == nil {
			exportAllowed = pol.Allowed
		}
	}
	out := make([]taskJSON, 0, len(jobs))
	for _, j := range jobs {
		base, link := "/api/admin/exports/", "/manage/exports"
		if j.isReader() {
			base, link = "/api/exports/", "/exports"
		}
		name := j.params().Name
		if name == "" {
			name = j.Source
		}
		t := taskJSON{
			Kind: "export", ID: j.ID, Name: name, Status: j.Status, Done: j.RowsDone, Total: j.RowsEstimate,
			Format: j.Format, Bytes: j.Bytes, Error: j.Error, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt, Link: link,
		}
		// A reader's finished file is served only while their policy allows export (ADR 0046).
		if j.Status == exportDone && (exportAllowed || !j.isReader()) {
			t.Download = base + j.ID + "/file"
		}
		if j.Status == exportQueued || j.Status == exportRunning {
			t.Cancel = base + j.ID + "/cancel"
		}
		if j.Status == exportQueued {
			if n, err := s.st.exportJobsAhead(j); err == nil {
				t.Ahead = &n
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// inputsGist is a short, stable one-line summary of a run's first row for the tray: the non-empty
// values in key order, bounded.
func inputsGist(raw string) string {
	preview := queueInputsPreview(raw)
	if preview == "" {
		return ""
	}
	var in map[string]string
	if json.Unmarshal([]byte(preview), &in) != nil {
		return ""
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, strings.Join(strings.Fields(in[k]), " "))
	}
	return clampRunes(strings.Join(parts, " · "), taskDetailMax)
}
