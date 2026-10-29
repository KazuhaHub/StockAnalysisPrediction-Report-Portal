package app

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------- fixtures ----------

type exportEnv struct {
	t   *testing.T
	s   *Server
	mux *http.ServeMux
}

func newExportEnv(t *testing.T) *exportEnv {
	t.Helper()
	s := tokenServer(t)
	s.cfg.DBPath = filepath.Join(t.TempDir(), "portal.db")
	return finishExportEnv(t, s)
}

func finishExportEnv(t *testing.T, s *Server) *exportEnv {
	t.Helper()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	mustAddUser(t, s, "admin", true)
	mustAddUser(t, s, "bob", false)
	return &exportEnv{t: t, s: s, mux: mux}
}

func (e *exportEnv) do(user, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var req *http.Request
	if user == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = sessionForPath(e.t, e.s, user, "", path)
		req.Method = method
	}
	if body != "" {
		req.Body = io.NopCloser(strings.NewReader(body))
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, req)
	return w
}

func (e *exportEnv) create(body string) exportJobJSON {
	e.t.Helper()
	w := e.do("admin", "POST", "/api/admin/exports", body)
	if w.Code != http.StatusCreated {
		e.t.Fatalf("create export: %d %s", w.Code, w.Body.String())
	}
	var j exportJobJSON
	if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil {
		e.t.Fatal(err)
	}
	return j
}

// run executes the queued job synchronously and returns its final state.
func (e *exportEnv) run(id string) exportJobJSON {
	e.t.Helper()
	if !e.s.exportRunNext() {
		e.t.Fatal("no queued job to run")
	}
	j := e.s.st.getExportJob(id)
	if j == nil {
		e.t.Fatalf("job %s vanished", id)
	}
	return j.view()
}

func (e *exportEnv) fileOf(j exportJobJSON) []byte {
	e.t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.s.exportDir(), j.ID+exportExt(j.Format)))
	if err != nil {
		e.t.Fatal(err)
	}
	return raw
}

func seedAudit(t *testing.T, st *Store, n int, mk func(i int) AuditEntry) {
	t.Helper()
	tx, err := st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(st.bind(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,?,?,?,?,?)`))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		e := mk(i)
		if _, err := stmt.Exec(e.At, e.Actor, e.ActorOU, e.Action, e.TargetType, e.TargetID, e.Detail, e.IP); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

var auditEpoch = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func atStamp(sec int) string {
	return auditEpoch.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
}

func parseCSV(t *testing.T, raw []byte) [][]string {
	t.Helper()
	if !bytes.HasPrefix(raw, []byte("\xef\xbb\xbf")) {
		t.Fatalf("CSV is missing the UTF-8 byte order mark")
	}
	recs, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func colIndex(t *testing.T, header []string, key string) int {
	t.Helper()
	for i, h := range header {
		if h == key {
			return i
		}
	}
	t.Fatalf("column %q not in %v", key, header)
	return -1
}

func auditRowsOfType(t *testing.T, st *Store, action string) int {
	t.Helper()
	return scalar[int](t, st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, action)
}

// ---------- 1. memory ----------

func TestExportMemoryDoesNotScaleWithRows(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds 50,000 audit rows")
	}
	dir := t.TempDir()
	st, err := OpenStore("sqlite", filepath.Join(dir, "portal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := tokenServer(t)
	s.st = st
	s.cfg.DBPath = filepath.Join(dir, "portal.db")
	pad := strings.Repeat("x", 700)
	seedAudit(t, st, 50000, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i / 3), Actor: "u" + strconv.Itoa(i%50), Action: AuditReportRead, TargetType: "report",
			TargetID: strconv.Itoa(i), Detail: `{"n":` + strconv.Itoa(i) + `,"pad":"` + pad + `"}`, IP: "10.0.0.1"}
	})
	params, _ := json.Marshal(exportParams{Name: "mem", Filter: exportNode{Op: "and"}, Columns: s.exportDefault("audit"), TZ: "utc"})
	j := &exportJob{ID: newExportID(), Source: "audit", Format: "csv", Params: string(params), RequestedBy: "admin",
		Status: exportQueued, FileName: "mem.csv", CreatedAt: time.Now().UnixMilli()}
	if err := st.insertExportJob(j); err != nil {
		t.Fatal(err)
	}

	// A tight GC target keeps uncollected garbage out of the measurement: what is left is memory the
	// export genuinely holds.
	old := debug.SetGCPercent(10)
	defer debug.SetGCPercent(old)
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	if !s.exportRunNext() {
		t.Fatal("job did not run")
	}
	close(stop)
	<-done

	got := st.getExportJob(j.ID)
	if got.Status != exportDone || got.RowsDone != 50000 {
		t.Fatalf("job = %s rows=%d err=%q", got.Status, got.RowsDone, got.Error)
	}
	const bound = 16 << 20
	growth := int64(peak.Load()) - int64(base.HeapAlloc)
	t.Logf("heap grew %d bytes while writing a %d byte file", growth, got.Bytes)
	if got.Bytes < 2*bound {
		t.Fatalf("the file is only %d bytes: too small for the bound to prove anything", got.Bytes)
	}
	if growth > bound {
		t.Fatalf("heap grew by %d bytes while exporting a %d byte file: memory scales with rows", growth, got.Bytes)
	}
}

// exportDefault is a test shorthand for a source's default column list.
func (s *Server) exportDefault(key string) []string { return exportSourceByKey(key).defaultColumns() }

// ---------- 2. equality with ListAudit ----------

func TestExportAuditEqualsListAuditInOrder(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 70 // many batch boundaries, several inside one run of equal timestamps
	seedAudit(t, e.s.st, 900, func(i int) AuditEntry {
		action := AuditReportRead
		if i%4 == 0 {
			action = AuditUserChange
		}
		at := atStamp(i / 5) // five rows share each second: the id tie-break decides their order
		if i < 40 {
			at = "2026-08-02 1" + strconv.Itoa(i%10) + ":00:00" // legacy stamps from the format change
		}
		return AuditEntry{At: at, Actor: "u" + strconv.Itoa(i%7), Action: action, TargetType: "report", TargetID: strconv.Itoa(i), IP: "10.0.0.1"}
	})
	var want []int64
	for off := 0; ; off += 500 {
		page, total := e.s.st.ListAudit(AuditFilter{Action: AuditReportRead, Limit: 500, Offset: off})
		for _, r := range page {
			want = append(want, r.ID)
		}
		if off+500 >= total {
			break
		}
	}
	j := e.create(`{"source":"audit","format":"csv","columns":["id","at","action"],"tz":"utc",
		"filter":{"op":"and","items":[{"field":"action","operator":"is","values":["report.read"]}]}}`)
	if j.RowsEstimate != int64(len(want)) {
		t.Fatalf("estimate %d, ListAudit total %d", j.RowsEstimate, len(want))
	}
	j = e.run(j.ID)
	if j.Status != exportDone {
		t.Fatalf("export: %+v", j)
	}
	recs := parseCSV(t, e.fileOf(j))
	var got []int64
	for _, rec := range recs[1:] {
		id, _ := strconv.ParseInt(rec[colIndex(t, recs[0], "id")], 10, 64)
		got = append(got, id)
	}
	if len(got) != len(want) {
		t.Fatalf("exported %d rows, ListAudit has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d: export id %d, ListAudit id %d", i, got[i], want[i])
		}
	}
}

// ---------- 3. bounded queries ----------

func seedReports(t *testing.T, st *Store, n int, mk func(i int) Rep) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, _, err := st.UpsertReport(mk(i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExportReportsQueryCountIsBatchedNotPerReport(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 50
	seedReports(t, e.s.st, 230, func(i int) Rep {
		return Rep{Symbol: fmt.Sprintf("60%04d", i), Name: "N", Date: "2026-10-18", RType: "投资决策建议", Kind: "投资决策",
			Title: fmt.Sprintf("title %d", i), MD: "# body " + strconv.Itoa(i)}
	})
	before := atomic.LoadInt64(&e.s.exports.queries)
	j := e.create(`{"source":"reports","format":"md_zip"}`)
	j = e.run(j.ID)
	if j.Status != exportDone || j.RowsDone != 230 {
		t.Fatalf("export: %+v", j)
	}
	queries := atomic.LoadInt64(&e.s.exports.queries) - before
	// 230 reports at 50 per batch is five full batches and one short one. A per-report read would be 230+.
	if queries > 6 {
		t.Fatalf("%d batch queries for 230 reports: reads are not batched", queries)
	}
	zr, err := zip.NewReader(bytes.NewReader(e.fileOf(j)), int64(len(e.fileOf(j))))
	if err != nil || len(zr.File) != 230 {
		t.Fatalf("zip: %v entries=%d", err, len(zr.File))
	}
}

// ---------- 4. restart ----------

func TestExportInterruptedJobIsRequeuedAndItsPartFileRemoved(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 10, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditLogin, IP: "10.0.0.1"}
	})
	j := e.create(`{"source":"audit","format":"csv"}`)
	// Simulate a crash mid-run: claimed, partly written, never finished.
	if _, err := e.s.st.claimNextExportJob(time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.s.exportDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	part := e.s.exportPartPath(j.ID)
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := e.s.st.getExportJob(j.ID).Status; st != exportRunning {
		t.Fatalf("setup: status %s", st)
	}

	e.s.exportRecover()

	if got := e.s.st.getExportJob(j.ID); got.Status != exportQueued || got.RowsDone != 0 {
		t.Fatalf("after restart: %+v", got)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("the .part file survived the restart: %v", err)
	}
	if done := e.run(j.ID); done.Status != exportDone {
		t.Fatalf("requeued job did not complete: %+v", done)
	}
}

// ---------- 5. cancel ----------

func TestExportCancelRunningStopsWithinOneBatchAndLeavesNoFile(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 100
	seedAudit(t, e.s.st, 1000, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, IP: "10.0.0.1"}
	})
	j := e.create(`{"source":"audit","format":"csv"}`)
	before := atomic.LoadInt64(&e.s.exports.queries)
	e.s.exports.afterBatch = func(id string, batch int) {
		if batch == 2 {
			e.s.exports.cancelRunning(id)
		}
	}
	done := e.run(j.ID)
	if done.Status != exportCanceled {
		t.Fatalf("status %s, want canceled (%+v)", done.Status, done)
	}
	if q := atomic.LoadInt64(&e.s.exports.queries) - before; q != 2 {
		t.Fatalf("%d batch queries after a cancel in batch 2: it did not stop at the batch boundary", q)
	}
	entries, _ := os.ReadDir(e.s.exportDir())
	if len(entries) != 0 {
		t.Fatalf("canceled job left files behind: %v", entries)
	}
}

func TestExportCancelQueuedThroughTheAPI(t *testing.T) {
	e := newExportEnv(t)
	j := e.create(`{"source":"cleanup","format":"csv"}`)
	w := e.do("admin", "POST", "/api/admin/exports/"+j.ID+"/cancel", "")
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var got exportJobJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != exportCanceled {
		t.Fatalf("status %s", got.Status)
	}
	if e.s.exportRunNext() {
		t.Fatal("the worker ran a canceled job")
	}
	if w := e.do("admin", "POST", "/api/admin/exports/"+j.ID+"/cancel", ""); w.Code != http.StatusConflict {
		t.Fatalf("second cancel: %d, want 409", w.Code)
	}
	if w := e.do("admin", "POST", "/api/admin/exports/nope/cancel", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown cancel: %d, want 404", w.Code)
	}
}

// ---------- 6. expiry ----------

func TestExportExpiredFileIsDeletedAndDownloadAnswers410(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 5, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditLogin, IP: "10.0.0.1"}
	})
	j := e.run2(`{"source":"audit","format":"jsonl"}`)
	path := filepath.Join(e.s.exportDir(), j.ID+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if w := e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != 200 {
		t.Fatalf("download before expiry: %d", w.Code)
	}
	if j.ExpiresAt-j.FinishedAt != exportFileTTL.Milliseconds() {
		t.Fatalf("expiry window = %dms, want 7 days", j.ExpiresAt-j.FinishedAt)
	}
	e.s.exports.now = func() time.Time { return time.UnixMilli(j.ExpiresAt).Add(time.Minute) }
	e.s.exportSweep(e.s.exports.clock())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the sweep left the expired file: %v", err)
	}
	if got := e.s.st.getExportJob(j.ID); got.Status != exportExpired {
		t.Fatalf("status %s", got.Status)
	}
	if w := e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != http.StatusGone {
		t.Fatalf("download after expiry: %d, want 410", w.Code)
	}
}

func TestExportDownloadEnforcesExpiryBeforeTheSweepRuns(t *testing.T) {
	e := newExportEnv(t)
	j := e.run2(`{"source":"cleanup","format":"csv"}`)
	e.s.exports.now = func() time.Time { return time.UnixMilli(j.ExpiresAt).Add(time.Second) }
	if w := e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != http.StatusGone {
		t.Fatalf("status %d, want 410", w.Code)
	}
	if _, err := os.Stat(filepath.Join(e.s.exportDir(), j.ID+".csv")); !os.IsNotExist(err) {
		t.Fatal("the file was not removed")
	}
}

func TestExportSweepDeletesRowsOlderThanRetention(t *testing.T) {
	e := newExportEnv(t)
	j := e.run2(`{"source":"cleanup","format":"csv"}`)
	e.s.exportSweep(time.UnixMilli(j.CreatedAt).Add(exportRowTTL + time.Hour))
	if e.s.st.getExportJob(j.ID) != nil {
		t.Fatal("a 90-day-old job row survived the sweep")
	}
}

// run2 creates a job and runs it.
func (e *exportEnv) run2(body string) exportJobJSON {
	e.t.Helper()
	j := e.create(body)
	return e.run(j.ID)
}

func TestExportDownloadStates(t *testing.T) {
	e := newExportEnv(t)
	if w := e.do("admin", "GET", "/api/admin/exports/missing/file", ""); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	j := e.create(`{"source":"cleanup","format":"csv"}`)
	if w := e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != http.StatusConflict {
		t.Fatalf("queued: %d, want 409", w.Code)
	}
}

// ---------- 7. admin only ----------

func TestExportEndpointsRefuseNonAdministrators(t *testing.T) {
	e := newExportEnv(t)
	j := e.run2(`{"source":"cleanup","format":"csv"}`)
	reqs := []struct{ method, path, body string }{
		{"GET", "/api/admin/exports/sources", ""},
		{"GET", "/api/admin/exports", ""},
		{"GET", "/api/admin/exports/" + j.ID, ""},
		{"GET", "/api/admin/exports/" + j.ID + "/file", ""},
		{"POST", "/api/admin/exports", `{"source":"cleanup","format":"csv"}`},
		{"POST", "/api/admin/exports/preview", `{"source":"cleanup"}`},
		{"POST", "/api/admin/exports/" + j.ID + "/cancel", ""},
	}
	for _, r := range reqs {
		if w := e.do("bob", r.method, r.path, r.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user: %d, want 403", r.method, r.path, w.Code)
		}
		if w := e.do("", r.method, r.path, r.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s anonymous: %d, want 401", r.method, r.path, w.Code)
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs`); n != 1 {
		t.Fatalf("a refused request created a job: %d rows", n)
	}
}

// ---------- 8. audit ----------

func TestExportWritesExactlyOneCreateAuditRowAndOneDownloadRowPerDownload(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 2500, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, TargetType: "report", TargetID: strconv.Itoa(i), IP: "10.0.0.1"}
	})
	reads := auditRowsOfType(t, e.s.st, AuditReportRead)
	// The export.create row the submission itself writes is audit data too, so the filter pins the 2500.
	j := e.run2(`{"source":"audit","format":"csv","filter":{"op":"and","items":[{"field":"action","operator":"is","values":["report.read"]}]}}`)
	if j.RowsDone != 2500 {
		t.Fatalf("rows %d", j.RowsDone)
	}
	if n := auditRowsOfType(t, e.s.st, AuditExportCreate); n != 1 {
		t.Fatalf("%d export.create rows for one 2500-row export", n)
	}
	if n := auditRowsOfType(t, e.s.st, AuditReportRead); n != reads {
		t.Fatalf("export added %d report.read rows", n-reads)
	}
	var detail map[string]any
	raw := scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=?`, AuditExportCreate)
	if err := json.Unmarshal([]byte(raw), &detail); err != nil || detail["source"] != "audit" || detail["estimate"] != float64(2500) || detail["format"] != "csv" {
		t.Fatalf("export.create detail = %q (%v)", raw, err)
	}
	for i := 0; i < 2; i++ {
		e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", "")
	}
	if n := auditRowsOfType(t, e.s.st, AuditExportDownload); n != 2 {
		t.Fatalf("%d export.download rows for two downloads", n)
	}
	// A resumed transfer re-requests ranges of the same file: only the first is a download.
	e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", "", "Range", "bytes=100-")
	if n := auditRowsOfType(t, e.s.st, AuditExportDownload); n != 2 {
		t.Fatalf("a mid-file range request wrote a download row (%d)", n)
	}
}

// ---------- 9. range / length / gzip ----------

func TestExportDownloadSupportsRangeAndExactLengthBypassingGzip(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 300, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, Detail: strings.Repeat("d", 100), IP: "10.0.0.1"}
	})
	j := e.run2(`{"source":"audit","format":"csv"}`)
	handler := gzipMiddleware(e.mux)
	get := func(headers ...string) *httptest.ResponseRecorder {
		req := sessionForPath(t, e.s, "admin", "", "/api/admin/exports/"+j.ID+"/file")
		req.Header.Set("Accept-Encoding", "gzip")
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	full := get()
	if full.Code != 200 || full.Header().Get("Content-Encoding") != "" {
		t.Fatalf("full download: %d encoding=%q", full.Code, full.Header().Get("Content-Encoding"))
	}
	if got := full.Header().Get("Content-Length"); got != strconv.FormatInt(j.Bytes, 10) || int64(full.Body.Len()) != j.Bytes {
		t.Fatalf("Content-Length %q, body %d, job bytes %d", got, full.Body.Len(), j.Bytes)
	}
	if cd := full.Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") || !strings.Contains(cd, j.FileName) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	part := get("Range", "bytes=10-59")
	if part.Code != http.StatusPartialContent || part.Header().Get("Content-Length") != "50" ||
		part.Header().Get("Content-Range") != fmt.Sprintf("bytes 10-59/%d", j.Bytes) {
		t.Fatalf("range: %d len=%q range=%q", part.Code, part.Header().Get("Content-Length"), part.Header().Get("Content-Range"))
	}
	if !bytes.Equal(part.Body.Bytes(), full.Body.Bytes()[10:60]) {
		t.Fatal("range body differs from the file")
	}
}

// ---------- 11. filter limits ----------

func TestExportFilterLimitsAndWhitelistAreRefusedBeforeAnySQLIsBuilt(t *testing.T) {
	st := newTestStore(t)
	s := &Server{st: st}
	st.Close() // any query would now fail: a 400 below proves none was attempted
	cond := func(field, op string, vals ...string) exportNode {
		return exportNode{Field: field, Operator: op, Values: vals}
	}
	many := exportNode{Op: "and"}
	for i := 0; i < 21; i++ {
		many.Items = append(many.Items, cond("actor", "is", "a"))
	}
	deep := exportNode{Op: "and", Items: []exportNode{{Op: "or", Items: []exportNode{{Op: "and", Items: []exportNode{{Op: "or",
		Items: []exportNode{cond("actor", "is", "a")}}}}}}}}
	cases := map[string]exportNode{
		"21 conditions":       many,
		"4 levels":            deep,
		"unknown field":       {Op: "and", Items: []exportNode{cond("password", "is", "x")}},
		"unknown operator":    {Op: "and", Items: []exportNode{cond("actor", "matches", "x")}},
		"operator not on it":  {Op: "and", Items: []exportNode{cond("action", "contains", "x")}},
		"arity":               {Op: "and", Items: []exportNode{cond("actor", "is", "a", "b")}},
		"between arity":       {Op: "and", Items: []exportNode{cond("at", "between", "2026-10-01")}},
		"bad date":            {Op: "and", Items: []exportNode{cond("at", "after", "yesterday")}},
		"bad group op":        {Op: "xor"},
		"root is a condition": cond("actor", "is", "a"),
	}
	for name, f := range cases {
		_, err := s.newExportPlan(exportSourceByKey("audit"), f)
		var fe *exportFilterError
		if err == nil || !asFilterErr(err, &fe) {
			t.Errorf("%s: err = %v, want a filter error", name, err)
		}
	}
	// Exactly at the limits is accepted.
	ok := exportNode{Op: "and"}
	for i := 0; i < 20; i++ {
		ok.Items = append(ok.Items, cond("actor", "is", "a"))
	}
	if err := validateExportFilter(exportSourceByKey("audit"), ok); err != nil {
		t.Errorf("20 conditions refused: %v", err)
	}
	three := exportNode{Op: "and", Items: []exportNode{{Op: "or", Items: []exportNode{{Op: "and", Items: []exportNode{cond("actor", "is", "a")}}}}}}
	if err := validateExportFilter(exportSourceByKey("audit"), three); err != nil {
		t.Errorf("3 levels refused: %v", err)
	}
}

func asFilterErr(err error, target **exportFilterError) bool {
	fe, ok := err.(*exportFilterError)
	*target = fe
	return ok
}

func TestExportFilterErrorsAre400OverHTTP(t *testing.T) {
	e := newExportEnv(t)
	bad := `{"source":"audit","format":"csv","filter":{"op":"and","items":[{"field":"nope","operator":"is","values":["x"]}]}}`
	for _, path := range []string{"/api/admin/exports", "/api/admin/exports/preview"} {
		if w := e.do("admin", "POST", path, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", path, w.Code)
		}
	}
	for _, body := range []string{
		`{"source":"nope"}`,
		`{"source":"audit","format":"md_zip"}`,
		`{"source":"audit","format":"csv","columns":["nope"]}`,
		`{"source":"audit","format":"csv","tz":"mars"}`,
		`{"source":"reports","format":"md_zip","options":{"layout":"spiral"}}`,
		`not json`,
	} {
		if w := e.do("admin", "POST", "/api/admin/exports", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs`); n != 0 {
		t.Fatalf("a refused request created %d jobs", n)
	}
}

// ---------- 12. kind includes later subtypes ----------

func TestExportKindFilterIncludesASubtypeAddedAfterTheFilterWasSaved(t *testing.T) {
	e := newExportEnv(t)
	st := e.s.st
	seedReports(t, st, 1, func(int) Rep {
		return Rep{Symbol: "600001", Date: "2026-10-01", RType: "老子类", Kind: "投资决策", Title: "old", MD: "m"}
	})
	filter := `{"op":"and","items":[{"field":"kind","operator":"in","values":["投资决策"]}]}`
	count := func() int64 {
		w := e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"reports","columns":["id","rtype"],"filter":`+filter+`}`)
		var out struct{ Total int64 }
		json.Unmarshal(w.Body.Bytes(), &out)
		return out.Total
	}
	if n := count(); n != 1 {
		t.Fatalf("before: %d", n)
	}
	// The filter text above is unchanged; the kind gains a subtype and another kind gets a report.
	seedReports(t, st, 1, func(int) Rep {
		return Rep{Symbol: "600002", Date: "2026-10-02", RType: "新子类", Kind: "投资决策", Title: "new", MD: "m"}
	})
	seedReports(t, st, 1, func(int) Rep {
		return Rep{Symbol: "600003", Date: "2026-10-02", RType: "别的", Kind: "重组决策", Title: "other", MD: "m"}
	})
	if n := count(); n != 2 {
		t.Fatalf("after the kind gained a subtype: %d, want 2", n)
	}
	// Choosing the subtype itself selects only it.
	w := e.do("admin", "POST", "/api/admin/exports/preview",
		`{"source":"reports","columns":["rtype"],"filter":{"op":"and","items":[{"field":"rtype","operator":"in","values":["老子类"]}]}}`)
	var out struct {
		Total int64
		Rows  []map[string]any
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 1 || out.Rows[0]["rtype"] != "老子类" {
		t.Fatalf("subtype filter: %s", w.Body.String())
	}
}

// ---------- generator version ----------

func TestExportGeneratorVersionIsExactEvenThoughSQLOnlyPrefilters(t *testing.T) {
	e := newExportEnv(t)
	mk := func(sym, rtype, source, title string) Rep {
		return Rep{Symbol: sym, Name: "N", Date: "2026-10-18", RType: rtype, Kind: "k", Source: source, Title: title, MD: "m"}
	}
	st := e.s.st
	st.UpsertReport(mk("1", "A", "1-6-4 V3.17", "t1"))
	st.UpsertReport(mk("2", "A", "1-6-4 V3.1", "t2"))
	st.UpsertReport(mk("3", "B", "", "t3 v3.17"))
	st.UpsertReport(mk("4", "B", "x", "no version here V3.170 extra"))
	st.UpsertReport(mk("5", "C", "", "plain"))
	symbols := func(filter string) string {
		w := e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"reports","columns":["symbol"],"filter":`+filter+`}`)
		var out struct {
			Total int64
			Rows  []map[string]any
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("%s: %d %s", filter, w.Code, w.Body.String())
		}
		var syms []string
		for _, r := range out.Rows {
			syms = append(syms, r["symbol"].(string))
		}
		if int64(len(syms)) != out.Total {
			t.Fatalf("%s: total %d but %d rows", filter, out.Total, len(syms))
		}
		return strings.Join(syms, ",")
	}
	gv := func(op string, v ...string) string {
		b, _ := json.Marshal(v)
		return `{"field":"generator_version","operator":"` + op + `","values":` + string(b) + `}`
	}
	// Newest first: ids descend, so the symbols read 5..1.
	if got := symbols(`{"op":"and","items":[` + gv("is", "V3.17") + `]}`); got != "3,1" {
		t.Errorf("is V3.17 = %q, want 3,1 (V3.1 and V3.170 must not match)", got)
	}
	if got := symbols(`{"op":"and","items":[` + gv("in", "V3.1", "v3.17") + `]}`); got != "3,2,1" {
		t.Errorf("in = %q, want 3,2,1", got)
	}
	if got := symbols(`{"op":"and","items":[` + gv("is_not", "V3.17") + `]}`); got != "5,4,2" {
		t.Errorf("is_not = %q, want 5,4,2", got)
	}
	// Under OR the derived leaf cannot be applied after the fact: (rtype is C) OR (version is V3.1).
	or := `{"op":"or","items":[{"field":"rtype","operator":"is","values":["C"]},` + gv("is", "V3.1") + `]}`
	if got := symbols(or); got != "5,2" {
		t.Errorf("or = %q, want 5,2", got)
	}
	and := `{"op":"and","items":[{"field":"rtype","operator":"is","values":["A"]},` + gv("is", "V3.17") + `]}`
	if got := symbols(and); got != "1" {
		t.Errorf("and = %q, want 1", got)
	}
	// The exported file agrees with the preview.
	j := e.run2(`{"source":"reports","format":"csv","columns":["symbol","generator_version"],"filter":` + or + `}`)
	if j.RowsDone != 2 {
		t.Fatalf("export rows %d, want 2", j.RowsDone)
	}
}

// ---------- panel time zone ----------

func TestExportDateBoundariesFollowThePanelTimeZone(t *testing.T) {
	e := newExportEnv(t)
	e.s.st.SetSetting("timezone", "Asia/Shanghai")
	stamps := []string{"2026-09-30T15:59:59Z", "2026-09-30T16:00:00Z", "2026-10-01T15:59:59Z", "2026-10-01T16:00:00Z"}
	seedAudit(t, e.s.st, len(stamps), func(i int) AuditEntry {
		return AuditEntry{At: stamps[i], Actor: "a", Action: AuditLogin, IP: "10.0.0.1"}
	})
	atCount := func(op, vals string) string {
		w := e.do("admin", "POST", "/api/admin/exports/preview",
			`{"source":"audit","columns":["at"],"filter":{"op":"and","items":[{"field":"at","operator":"`+op+`","values":`+vals+`}]}}`)
		var out struct{ Rows []map[string]any }
		json.Unmarshal(w.Body.Bytes(), &out)
		var got []string
		for _, r := range out.Rows {
			got = append(got, r["at"].(string))
		}
		return strings.Join(got, " ")
	}
	// 2026-10-01 in Shanghai is [09-30T16:00Z, 10-01T16:00Z).
	if got := atCount("between", `["2026-10-01","2026-10-01"]`); got != "2026-10-01T15:59:59Z 2026-09-30T16:00:00Z" {
		t.Errorf("between = %q", got)
	}
	if got := atCount("after", `["2026-10-01"]`); got != "2026-10-01T16:00:00Z" {
		t.Errorf("after the day = %q", got)
	}
	if got := atCount("before", `["2026-10-01"]`); got != "2026-09-30T15:59:59Z" {
		t.Errorf("before the day = %q", got)
	}
	// Rendering follows the tz option.
	j := e.run2(`{"source":"audit","format":"csv","columns":["at"],"tz":"panel"}`)
	recs := parseCSV(t, e.fileOf(j))
	if recs[len(recs)-1][0] != "2026-09-30T23:59:59+08:00" {
		t.Errorf("panel rendering = %q", recs[len(recs)-1][0])
	}
	j = e.run2(`{"source":"audit","format":"csv","columns":["at"],"tz":"utc"}`)
	recs = parseCSV(t, e.fileOf(j))
	if recs[len(recs)-1][0] != "2026-09-30T15:59:59Z" {
		t.Errorf("utc rendering = %q", recs[len(recs)-1][0])
	}
}

// ---------- formats ----------

func TestExportCSVNeutralisesFormulasAndJSONLKeepsTypes(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 1, func(int) AuditEntry {
		return AuditEntry{At: atStamp(0), Actor: "=HYPERLINK(\"x\")", Action: AuditLogin, TargetID: "-5", Detail: `{"a":"<b>"}`, IP: "10.0.0.1", ActorOU: 0}
	})
	j := e.run2(`{"source":"audit","format":"csv","columns":["actor","target_id","detail"],"tz":"utc","filter":{"op":"and","items":[{"field":"action","operator":"is","values":["auth.login"]}]}}`)
	rec := parseCSV(t, e.fileOf(j))[1]
	if rec[0] != `'=HYPERLINK("x")` || rec[1] != "-5" || rec[2] != `{"a":"<b>"}` {
		t.Fatalf("csv cells = %q", rec)
	}
	j = e.run2(`{"source":"audit","format":"jsonl","columns":["id","actor","detail"],"tz":"utc","filter":{"op":"and","items":[{"field":"action","operator":"is","values":["auth.login"]}]}}`)
	line := strings.TrimSpace(string(e.fileOf(j)))
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	if _, isNum := obj["id"].(float64); !isNum || obj["actor"] != `=HYPERLINK("x")` || !strings.HasPrefix(line, `{"id":`) || strings.Contains(line, `\u003c`) {
		t.Fatalf("jsonl = %s", line)
	}
}

func TestExportReportArchiveFrontMatterIndexAndLayout(t *testing.T) {
	e := newExportEnv(t)
	tricky := "Q3: \"best\" - 2026\n--- tricky"
	id1, _, _ := e.s.st.UpsertReport(Rep{Symbol: "600001", Name: "Alpha: Co", Date: "2026-10-18", RType: "投资决策建议", Kind: "投资决策",
		Title: tricky, Source: "1-6-4 V3.17", Version: "", MD: "# one\n"})
	e.s.st.UpsertReport(Rep{Symbol: "", Date: "2026-10-19", RType: "宏观", Kind: "宏观", Title: "thematic", MD: "# two\n"})

	j := e.run2(`{"source":"reports","format":"md_zip","options":{"front_matter":true,"index_csv":true,"layout":"date"}}`)
	raw := e.fileOf(j)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		r.Close()
		files[f.Name] = string(b)
	}
	if len(files) != 3 {
		t.Fatalf("entries: %v", keysOfStr(files))
	}
	var md string
	for name, body := range files {
		if strings.HasPrefix(name, "2026-10-18/") && strings.HasSuffix(name, "_"+strconv.FormatInt(id1, 10)+".md") {
			md = body
		}
	}
	if md == "" {
		t.Fatalf("no 2026-10-18 entry: %v", keysOfStr(files))
	}
	parts := strings.SplitN(md, "---\n", 3)
	if len(parts) != 3 || !strings.HasSuffix(md, "# one\n") {
		t.Fatalf("front matter framing: %q", md)
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		t.Fatalf("front matter is not YAML: %v\n%s", err, parts[1])
	}
	if fm["title"] != tricky || fm["name"] != "Alpha: Co" || fm["symbol"] != "600001" || fm["generator_version"] != "V3.17" ||
		fm["id"] != int(id1) || fm["kind"] != "投资决策" || fm["rtype"] != "投资决策建议" || fm["date"] != "2026-10-18" {
		t.Fatalf("front matter = %v", fm)
	}
	idx, ok := files["index.csv"]
	if !ok {
		t.Fatal("no index.csv")
	}
	recs := parseCSV(t, []byte(idx))
	if len(recs) != 3 || recs[0][0] != "path" || recs[0][1] != "id" {
		t.Fatalf("index.csv = %q", recs)
	}
	for _, rec := range recs[1:] {
		if _, ok := files[rec[0]]; !ok {
			t.Errorf("index.csv lists %q which is not in the archive", rec[0])
		}
	}
	// Symbol layout puts a symbol-less thematic report under a placeholder folder; no front matter, no index.
	j = e.run2(`{"source":"reports","format":"md_zip","options":{"layout":"symbol"}}`)
	raw = e.fileOf(j)
	zr, _ = zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if f.Name == "index.csv" {
			t.Error("index.csv written although not requested")
		}
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "600001/") || !strings.Contains(joined, "_no-symbol/") {
		t.Errorf("symbol layout = %v", names)
	}
}

func keysOfStr(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------- sources ----------

func TestExportSourcesContract(t *testing.T) {
	e := newExportEnv(t)
	e.s.st.RegisterType("宏观子类", "宏观")
	e.s.st.UpsertReport(Rep{Symbol: "1", Date: "2026-10-01", RType: "投资决策建议", Kind: "投资决策", Source: "x V3.17", Title: "t", MD: "m"})
	seedAudit(t, e.s.st, 3, func(i int) AuditEntry {
		a := AuditLogin
		if i == 2 {
			a = AuditReportRead
		}
		return AuditEntry{At: atStamp(i), Actor: "a", Action: a, IP: "10.0.0.1"}
	})
	w := e.do("admin", "GET", "/api/admin/exports/sources", "")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		Sources []struct {
			Key       string   `json:"key"`
			Formats   []string `json:"formats"`
			TimeField string   `json:"time_field"`
			Total     int64    `json:"total"`
			Fields    []struct {
				Key       string           `json:"key"`
				Type      string           `json:"type"`
				Operators []string         `json:"operators"`
				Options   []string         `json:"options"`
				Tree      []exportKindTree `json:"tree"`
			} `json:"fields"`
			Columns []struct {
				Key     string `json:"key"`
				Default bool   `json:"default"`
			} `json:"columns"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	keys := map[string]int{}
	for i, s := range out.Sources {
		keys[s.Key] = i
	}
	for _, k := range []string{"reports", "audit", "signins", "runs", "notifications", "cleanup"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("missing source %s", k)
		}
	}
	reports := out.Sources[keys["reports"]]
	if reports.Formats[0] != "md_zip" || reports.TimeField != "date" || reports.Total != 1 {
		t.Fatalf("reports: %+v", reports)
	}
	for _, f := range reports.Fields {
		switch f.Key {
		case "kind":
			found := map[string][]string{}
			for _, k := range f.Tree {
				found[k.Kind] = k.Subtypes
			}
			if len(found["投资决策"]) != 1 || len(found["宏观"]) != 1 || f.Type != "enum" {
				t.Fatalf("kind tree = %+v", f.Tree)
			}
		case "generator_version":
			if len(f.Options) != 1 || f.Options[0] != "V3.17" {
				t.Fatalf("generator versions = %v", f.Options)
			}
		}
	}
	audit := out.Sources[keys["audit"]]
	if audit.Total != 3 || audit.TimeField != "at" || audit.Formats[0] != "csv" {
		t.Fatalf("audit: %+v", audit)
	}
	for _, f := range audit.Fields {
		if f.Key == "action" && len(f.Options) != len(auditVocabulary) {
			t.Fatalf("action options %d, vocabulary %d", len(f.Options), len(auditVocabulary))
		}
		for _, op := range f.Operators {
			if !containsString([]string{"is", "is_not", "in", "not_in", "contains", "before", "after", "between"}, op) {
				t.Errorf("unknown operator %q on %s", op, f.Key)
			}
		}
	}
	signins := out.Sources[keys["signins"]]
	if signins.Total != 2 {
		t.Fatalf("signins total %d, want only the auth.* rows (2)", signins.Total)
	}
	for _, f := range signins.Fields {
		if f.Key == "action" {
			for _, o := range f.Options {
				if !strings.HasPrefix(o, "auth.") {
					t.Errorf("signins offers %q", o)
				}
			}
		}
	}
	for _, s := range out.Sources {
		def := 0
		for _, c := range s.Columns {
			if c.Default {
				def++
			}
		}
		if def == 0 || len(s.Fields) == 0 || s.TimeField == "" {
			t.Errorf("%s: %d default columns, %d fields, time_field %q", s.Key, def, len(s.Fields), s.TimeField)
		}
	}
}

func TestExportEverySourceCountsPreviewsAndExports(t *testing.T) {
	e := newExportEnv(t)
	seedEverySource(t, e.s.st)
	for _, src := range exportSourceList {
		w := e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"`+src.Key+`"}`)
		var out struct {
			Total int64
			Rows  []map[string]any
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Total < 1 || len(out.Rows) < 1 {
			t.Errorf("%s preview: %d %s", src.Key, w.Code, w.Body.String())
			continue
		}
		if len(out.Rows[0]) != len(src.defaultColumns()) {
			t.Errorf("%s preview row has %d keys, want the %d default columns", src.Key, len(out.Rows[0]), len(src.defaultColumns()))
		}
		for _, format := range src.Formats {
			j := e.run2(`{"source":"` + src.Key + `","format":"` + format + `"}`)
			// The audit log also holds the export.create rows these submissions write.
			exact := src.Key != "audit" && src.Key != "signins"
			if j.Status != exportDone || (exact && j.RowsDone != out.Total) || j.RowsDone < out.Total {
				t.Errorf("%s/%s: %+v (preview total %d)", src.Key, format, j, out.Total)
			}
		}
	}
}

func seedEverySource(t *testing.T, st *Store) {
	t.Helper()
	st.UpsertReport(Rep{Symbol: "1", Date: "2026-10-01", RType: "R", Kind: "K", Title: "t", MD: "m"})
	seedAudit(t, st, 2, func(i int) AuditEntry {
		a := AuditLogin
		d := `{"method":"password","client":{"browser":"Chrome","browser_version":"120","os":"macOS","device_type":"desktop"}}`
		if i == 1 {
			a, d = AuditReportRead, ""
		}
		return AuditEntry{At: atStamp(i), Actor: "a", Action: a, Detail: d, IP: "10.0.0.1"}
	})
	tid, err := st.insertID(`INSERT INTO batch_targets(plugin_slug,name,config,created_at,ord) VALUES('p','Target One','{}','2026-10-01 10:00:00',0)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBatchJob(tid, 1, 0, "admin", []map[string]string{{"symbol": "1"}}, "normal"); err != nil {
		t.Fatal(err)
	}
	ms := time.Now().UnixMilli()
	for _, q := range []string{
		`INSERT INTO notification_channels(id,label,adapter,created_at) VALUES('c1','Mail','smtp',1)`,
		`INSERT INTO notification_events(id,source_key,event_type,payload,created_at) VALUES('e1','k','report.ingested','{}',1)`,
	} {
		if _, err := st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,username,state,expires_at,created_at) VALUES('d1','e1','c1','bob','accepted',?,?)`, ms+1000, ms); err != nil {
		t.Fatal(err)
	}
	if _, err := st.InsertCleanupRun(CleanupRun{RanAt: nowStr(), Trigger: "manual", OK: true, BatchDeleted: 3}); err != nil {
		t.Fatal(err)
	}
}

func TestExportSigninsParseMethodAndClientFromDetail(t *testing.T) {
	e := newExportEnv(t)
	seedEverySource(t, e.s.st)
	j := e.run2(`{"source":"signins","format":"csv","columns":["action","method","browser","os","device_type"]}`)
	recs := parseCSV(t, e.fileOf(j))
	if len(recs) != 2 || recs[1][0] != AuditLogin || recs[1][1] != "password" || recs[1][2] != "Chrome 120" || recs[1][3] != "macOS" || recs[1][4] != "desktop" {
		t.Fatalf("signins = %q", recs)
	}
}

func TestExportRunsAndNotificationsResolveNames(t *testing.T) {
	e := newExportEnv(t)
	seedEverySource(t, e.s.st)
	recs := parseCSV(t, e.fileOf(e.run2(`{"source":"runs","format":"csv","columns":["target","created_by","status","total"]}`)))
	if recs[1][0] != "Target One" || recs[1][1] != "admin" || recs[1][2] != "queued" || recs[1][3] != "1" {
		t.Fatalf("runs = %q", recs)
	}
	recs = parseCSV(t, e.fileOf(e.run2(`{"source":"notifications","format":"csv","columns":["channel","state","event_type","username"],"filter":{"op":"and","items":[{"field":"channel","operator":"is","values":["Mail"]}]}}`)))
	if len(recs) != 2 || recs[1][0] != "Mail" || recs[1][1] != "accepted" || recs[1][2] != "report.ingested" {
		t.Fatalf("notifications = %q", recs)
	}
	recs = parseCSV(t, e.fileOf(e.run2(`{"source":"cleanup","format":"csv","columns":["trigger","ok","batch_deleted"],"filter":{"op":"and","items":[{"field":"ok","operator":"is","values":["true"]}]}}`)))
	if len(recs) != 2 || recs[1][0] != "manual" || recs[1][1] != "true" || recs[1][2] != "3" {
		t.Fatalf("cleanup = %q", recs)
	}
}

// ---------- storage usage ----------

func TestStorageUsageReportsTheExportsCategory(t *testing.T) {
	e := newExportEnv(t)
	j := e.run2(`{"source":"cleanup","format":"csv"}`)
	w := e.do("admin", "GET", "/api/admin/cleanup/usage", "")
	var out struct {
		Categories []map[string]any `json:"categories"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	for _, c := range out.Categories {
		if c["key"] == "exports" {
			if c["rows"] != float64(1) || c["bytes"] != float64(j.Bytes) || c["eligible"] != float64(0) {
				t.Fatalf("exports category = %v (file is %d bytes)", c, j.Bytes)
			}
			return
		}
	}
	t.Fatalf("no exports category in %v", out.Categories)
}

// ---------- 13. migration 0012 ----------

func TestMigration0012IsIdempotentAndVerifiesItsShape(t *testing.T) {
	path := fileStore(t, "m12.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	m := migration0012()
	if !recorded(st, "0012") {
		t.Fatal("0012 not in the ledger after a normal open")
	}
	for i := 0; i < 2; i++ { // a re-run after a crash between the DDL and the ledger row
		if err := m.up(st.poolExec()); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{m}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !m.restore.olderDumpOK {
		t.Error("a table nothing is back-filled into must restore from an older dump")
	}
	st.Close()
	// Reopening an already-migrated file is a no-op.
	st, err = OpenStore("sqlite", path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st.Close()
}

func TestMigration0012RefusesADatabaseMissingWhatTheLedgerSaysExists(t *testing.T) {
	for name, stmt := range map[string]string{
		"index": `DROP INDEX idx_export_jobs_status`,
		"table": `DROP TABLE export_jobs`,
		"column": `DROP TABLE export_jobs; CREATE TABLE export_jobs(id TEXT PRIMARY KEY, source TEXT, format TEXT, params TEXT,
			requested_by TEXT, status TEXT, rows_estimate BIGINT, rows_done BIGINT, bytes BIGINT, file_name TEXT,
			created_at BIGINT, started_at BIGINT, finished_at BIGINT, expires_at BIGINT);
			CREATE INDEX idx_export_jobs_created ON export_jobs(created_at); CREATE INDEX idx_export_jobs_status ON export_jobs(status)`,
		"type": `DROP TABLE export_jobs; CREATE TABLE export_jobs(id TEXT PRIMARY KEY, source TEXT, format TEXT, params TEXT,
			requested_by TEXT, status TEXT, rows_estimate BIGINT, rows_done BIGINT, bytes BIGINT, file_name TEXT, error TEXT,
			created_at TEXT, started_at BIGINT, finished_at BIGINT, expires_at BIGINT);
			CREATE INDEX idx_export_jobs_created ON export_jobs(created_at); CREATE INDEX idx_export_jobs_status ON export_jobs(status)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := fileStore(t, "m12-"+name+".db")
			mutateFile(t, path, strings.Split(stmt, "; ")...)
			if st, err := OpenStore("sqlite", path); err == nil {
				st.Close()
				t.Fatal("a database whose export_jobs shape is wrong was accepted at start")
			} else if !strings.Contains(err.Error(), "0012") {
				t.Fatalf("refusal does not name the step: %v", err)
			}
		})
	}
}

func TestMigration0012IsNotInTheFrozenBaseSchema(t *testing.T) {
	st := newTestStore(t)
	for _, stmt := range st.baseSchemaStmts() {
		if strings.Contains(stmt, "export_jobs") {
			t.Fatalf("export_jobs is declared in the frozen baseline: %s", stmt)
		}
	}
}

// ---------- worker robustness ----------

func TestExportFailureIsRecordedAndLeavesNoPartFile(t *testing.T) {
	e := newExportEnv(t)
	j := &exportJob{ID: newExportID(), Source: "audit", Format: "csv", Params: `{"filter":{"op":"and","items":[{"field":"nope","operator":"is","values":["x"]}]}}`,
		RequestedBy: "admin", Status: exportQueued, FileName: "x.csv", CreatedAt: time.Now().UnixMilli()}
	if err := e.s.st.insertExportJob(j); err != nil {
		t.Fatal(err)
	}
	got := e.run(j.ID)
	if got.Status != exportFailed || got.Error == "" {
		t.Fatalf("%+v", got)
	}
	if entries, _ := os.ReadDir(e.s.exportDir()); len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
}

func TestExportListAndActiveFilter(t *testing.T) {
	e := newExportEnv(t)
	a := e.run2(`{"source":"cleanup","format":"csv","name":"first"}`)
	b := e.create(`{"source":"cleanup","format":"jsonl","name":"second: ünï/code"}`)
	var out struct{ Jobs []exportJobJSON }
	w := e.do("admin", "GET", "/api/admin/exports", "")
	json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Jobs) != 2 || out.Jobs[0].ID != b.ID || out.Jobs[1].ID != a.ID {
		t.Fatalf("list = %+v", out.Jobs)
	}
	if b.FileName != "second_ ünï_code.jsonl" || a.FileName != "first.csv" || b.Name != "second_ ünï_code" {
		t.Fatalf("names: %q %q %q", a.FileName, b.FileName, b.Name)
	}
	out.Jobs = nil
	json.Unmarshal(e.do("admin", "GET", "/api/admin/exports?active=1&limit=5", "").Body.Bytes(), &out)
	if len(out.Jobs) != 1 || out.Jobs[0].ID != b.ID || out.Jobs[0].Filter.Op != "and" || out.Jobs[0].Columns == nil {
		t.Fatalf("active list = %+v", out.Jobs)
	}
	w = e.do("admin", "GET", "/api/admin/exports/"+b.ID, "")
	if !strings.Contains(w.Body.String(), `"filter":{"op":"and","items":[]}`) {
		t.Fatalf("job JSON does not carry an explicit empty filter: %s", w.Body.String())
	}
}

// ---------- Postgres ----------

// TestPostgresExport runs the export store paths on the driver production uses: the keyset cursor
// with typed parameters (text and integer keys), every source's SQL, the LIKE prefilter and the
// panel-zone boundaries. It is skipped unless TEST_POSTGRES_DSN is set.
func TestPostgresExport(t *testing.T) {
	st := pgStore(t, "audit_log", "reports", "export_jobs", "batch_items", "batch_jobs", "notification_deliveries",
		"notification_channels", "notification_events", "cleanup_runs", "type_config")
	t.Cleanup(func() {
		for _, tbl := range []string{"audit_log", "reports", "export_jobs", "batch_items", "batch_jobs",
			"notification_deliveries", "notification_channels", "notification_events", "cleanup_runs", "type_config"} {
			st.exec("DELETE FROM " + tbl)
		}
	})
	s := tokenServer(t)
	s.st = st
	s.cfg.DBPath = filepath.Join(t.TempDir(), "portal.db")
	e := finishExportEnv(t, s)
	e.s.exports.batchSize = 3
	st.SetSetting("timezone", "Asia/Shanghai")

	seedAudit(t, st, 10, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i / 2), Actor: "u", Action: AuditReportRead, TargetID: strconv.Itoa(i), IP: "10.0.0.1"}
	})
	seedAudit(t, st, 1, func(int) AuditEntry {
		return AuditEntry{At: "2026-09-30T16:00:00Z", Actor: "u", Action: AuditLogin, IP: "10.0.0.1",
			Detail: `{"method":"password"}`}
	})
	st.UpsertReport(Rep{Symbol: "1", Date: "2026-10-18", RType: "A", Kind: "K", Source: "1-6-4 V3.17", Title: "t1", MD: "one"})
	st.UpsertReport(Rep{Symbol: "2", Date: "2026-10-18", RType: "A", Kind: "K", Source: "1-6-4 V3.1", Title: "t2", MD: "two"})
	st.UpsertReport(Rep{Symbol: "3", Date: "2026-10-19", RType: "B", Kind: "K", Title: "t3 V3.17", MD: "three"})
	seedEverySource(t, st)
	ms := time.Now().UnixMilli()
	for i := 0; i < 4; i++ {
		if _, err := st.exec(`INSERT INTO notification_deliveries(id,event_id,channel_id,username,state,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`,
			"extra"+strconv.Itoa(i), "e1", "c1", "bob"+strconv.Itoa(i), "accepted", ms+1000, ms); err != nil { // equal created_at: id breaks the tie
			t.Fatal(err)
		}
	}

	// Every source: count, preview, and a full export across several keyset batches.
	for _, src := range exportSourceList {
		w := e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"`+src.Key+`"}`)
		var out struct{ Total int64 }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Total < 1 {
			t.Fatalf("%s preview: %d %s", src.Key, w.Code, w.Body.String())
		}
		j := e.run2(`{"source":"` + src.Key + `","format":"csv"}`)
		if j.Status != exportDone || j.RowsDone < out.Total {
			t.Fatalf("%s export: %+v (total %d)", src.Key, j, out.Total)
		}
	}
	if got := scalar[int](t, st, `SELECT COUNT(*) FROM notification_deliveries`); got != 5 {
		t.Fatalf("seed: %d deliveries", got)
	}
	j := e.run2(`{"source":"notifications","format":"csv","columns":["created_at"]}`)
	if j.RowsDone != 5 {
		t.Fatalf("notification keyset (integer key, text tie-break) exported %d of 5 rows", j.RowsDone)
	}

	// Equality with the console's order across batches, including equal timestamps.
	var want []int64
	page, _ := st.ListAudit(AuditFilter{Action: AuditReportRead, Limit: 500})
	for _, r := range page {
		want = append(want, r.ID)
	}
	j = e.run2(`{"source":"audit","format":"csv","columns":["id"],"filter":{"op":"and","items":[{"field":"action","operator":"is","values":["report.read"]}]}}`)
	recs := parseCSV(t, e.fileOf(j))
	if len(recs)-1 != len(want) {
		t.Fatalf("exported %d, ListAudit %d", len(recs)-1, len(want))
	}
	for i, id := range want {
		if recs[i+1][0] != strconv.FormatInt(id, 10) {
			t.Fatalf("row %d: %s vs %d", i, recs[i+1][0], id)
		}
	}

	// Panel-zone date boundaries: 2026-10-01 in Shanghai starts at 09-30T16:00Z.
	w := e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"signins","columns":["at"],"filter":{"op":"and","items":[{"field":"at","operator":"between","values":["2026-10-01","2026-10-01"]}]}}`)
	var pv struct {
		Total int64
		Rows  []map[string]any
	}
	json.Unmarshal(w.Body.Bytes(), &pv)
	// The seeded sign-in at 2026-10-01T00:00:00Z is inside the day as well; the one at exactly
	// 2026-09-30T16:00:00Z is its first instant.
	if pv.Total != 2 || pv.Rows[1]["at"] != "2026-09-30T16:00:00Z" {
		t.Fatalf("panel-day boundary: %s", w.Body.String())
	}

	// The derived generator version: a LIKE prefilter plus the exact check, case-insensitive on ILIKE.
	w = e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"reports","columns":["symbol"],"filter":{"op":"and","items":[{"field":"generator_version","operator":"is","values":["v3.17"]}]}}`)
	json.Unmarshal(w.Body.Bytes(), &pv)
	if pv.Total != 2 || len(pv.Rows) != 2 {
		t.Fatalf("generator version on postgres: %s", w.Body.String())
	}
	w = e.do("admin", "POST", "/api/admin/exports/preview", `{"source":"reports","columns":["symbol"],"filter":{"op":"and","items":[{"field":"q","operator":"contains","values":["T1"]}]}}`)
	json.Unmarshal(w.Body.Bytes(), &pv)
	if pv.Total != 1 {
		t.Fatalf("q contains on postgres: %s", w.Body.String())
	}

	// The archive reads metadata and body in the same batched query.
	e.s.exports.batchSize = 2
	before := atomic.LoadInt64(&e.s.exports.queries)
	j = e.run2(`{"source":"reports","format":"md_zip","options":{"front_matter":true,"index_csv":true}}`)
	if q := atomic.LoadInt64(&e.s.exports.queries) - before; j.RowsDone != 4 || q > 3 {
		t.Fatalf("archive rows=%d queries=%d", j.RowsDone, q)
	}
}
