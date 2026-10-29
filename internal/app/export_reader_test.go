package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Reader report export (ADR 0046). The acceptance conditions are numbered as in the ADR.

type readerEnv struct {
	*exportEnv
	ou int64 // the OU the readers live in; export is allowed there unless a test says otherwise
}

// newReaderEnv is an export environment with one OU that allows export (max 100 per export, 1000
// per day) and a plain reader "rita" in it.
func newReaderEnv(t *testing.T) *readerEnv {
	t.Helper()
	e := &readerEnv{exportEnv: newExportEnv(t)}
	e.ou = seedOU(t, e.s.st, 0, nil)
	e.setPolicy(e.ou, 1, 100, 1000)
	e.addReader("rita", false)
	return e
}

func (e *readerEnv) setPolicy(ou int64, allowed, max, quota any) {
	e.t.Helper()
	if _, err := e.s.st.exec(`UPDATE user_groups SET export_allowed=?,export_max_reports=?,export_daily_quota=? WHERE id=?`,
		allowed, max, quota, ou); err != nil {
		e.t.Fatal(err)
	}
}

func (e *readerEnv) addReader(name string, restricted bool) {
	e.t.Helper()
	mustAddUser(e.t, e.s, name, false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=?,restricted=? WHERE username=?`, e.ou, boolInt(restricted), name); err != nil {
		e.t.Fatal(err)
	}
}

func (e *readerEnv) reports(n int, version string) {
	e.t.Helper()
	seedReports(e.t, e.s.st, n, func(i int) Rep {
		return Rep{Symbol: fmt.Sprintf("%s%04d", version, i), Name: "N", Date: "2026-10-18", RType: "note", Kind: "alpha",
			Title: fmt.Sprintf("%s title %d", version, i), MD: "# body", Version: version}
	})
}

const readerCreate = `{"source":"reports","format":"md_zip","name":"mine"}`

func (e *readerEnv) submit(user, body string) *httptest.ResponseRecorder {
	return e.do(user, "POST", "/api/exports", body)
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return out
}

func (e *readerEnv) policyOf(user string) map[string]any {
	e.t.Helper()
	w := e.do(user, "GET", "/api/exports/policy", "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("policy: %d %s", w.Code, w.Body.String())
	}
	return decodeBody(e.t, w)
}

func zipNames(t *testing.T, raw []byte) []string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// ---------- 1. step 0013 ----------

func TestReaderExportStep0013AddsExactlyTheDeclaredShape(t *testing.T) {
	path := fileStore(t, "m13.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := migration0013()
	var cols []string
	for _, c := range m.columns {
		if c.table != "user_groups" {
			t.Errorf("step touches %s", c.table)
		}
		cols = append(cols, c.name)
	}
	if got, want := strings.Join(cols, ","), "export_allowed,export_max_reports,export_daily_quota"; got != want {
		t.Fatalf("columns = %s, want %s", got, want)
	}
	if len(m.tables) != 0 || strings.Join(m.indices, ",") != "idx_export_jobs_requester" {
		t.Fatalf("step declares tables %v indices %v", m.tables, m.indices)
	}
	if !m.restore.olderDumpOK {
		t.Error("a step that writes no data must restore from an older dump")
	}
	if !recorded(st, "0013") || !st.indexExists("idx_export_jobs_requester") {
		t.Fatal("the step or its index is missing after a normal open")
	}
	for _, c := range cols {
		if !st.columnExists("user_groups", c) {
			t.Errorf("user_groups.%s missing", c)
		}
	}
	for i := 0; i < 2; i++ { // a re-run after a crash between the DDL and the ledger row
		if err := m.up(st.poolExec()); err != nil {
			t.Fatalf("re-run %d: %v", i, err)
		}
	}
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{m}); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range st.baseSchemaStmts() {
		if strings.Contains(stmt, "export_allowed") || strings.Contains(stmt, "idx_export_jobs_requester") {
			t.Fatalf("the frozen baseline declares the step's products: %s", stmt)
		}
	}
}

// windBackBeforeStep0013 leaves a database the release before the step could have written.
func windBackBeforeStep0013(t *testing.T, st *Store) {
	t.Helper()
	for _, q := range []string{
		`DROP INDEX IF EXISTS idx_export_jobs_requester`,
		`ALTER TABLE user_groups DROP COLUMN export_allowed`,
		`ALTER TABLE user_groups DROP COLUMN export_max_reports`,
		`ALTER TABLE user_groups DROP COLUMN export_daily_quota`,
	} {
		if _, err := st.exec(q); err != nil {
			t.Fatalf("winding back: %v", err)
		}
	}
	for _, id := range []string{"0013", "0014"} { // 0014 follows 0013 in the list
		if _, err := st.exec(`DELETE FROM meta WHERE k=?`, migrationLedgerKey(id)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReaderExportOlderDumpRestores(t *testing.T) {
	src := newTestStore(t)
	windBackBeforeStep0013(t, src)
	dump := dumpOf(t, src)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("an older dump must restore: %v", err)
	}
	if !dst.columnExists("user_groups", "export_allowed") {
		t.Fatal("the restored database lost the step's columns")
	}
	// 2. the restored Default OU resolves to false / 200 / 1000
	assertDefaultExportPolicy(t, dst)
}

// ---------- 2. the Default OU ----------

func assertDefaultExportPolicy(t *testing.T, st *Store) {
	t.Helper()
	def := st.DefaultGroupID()
	if def == 0 {
		t.Fatal("no Default OU")
	}
	snap, err := st.loadOUPolicySnapshotFrom(st.db)
	if err != nil {
		t.Fatal(err)
	}
	p, err := snap.resolve(def)
	if err != nil {
		t.Fatal(err)
	}
	if p.ExportAllowed.Effective || p.ExportMax.Effective != 200 || p.ExportQuota.Effective != 1000 {
		t.Fatalf("Default resolves to %v / %d / %d, want false / 200 / 1000",
			p.ExportAllowed.Effective, p.ExportMax.Effective, p.ExportQuota.Effective)
	}
	if p.ExportAllowed.Own == nil || p.ExportMax.Own == nil || p.ExportQuota.Own == nil {
		t.Fatal("the Default OU still has NULL columns: it is the root of inheritance and must not inherit")
	}
}

func TestReaderExportDefaultOUPolicyOnAFreshDatabase(t *testing.T) {
	assertDefaultExportPolicy(t, newTestStore(t))
}

func TestReaderExportDefaultOUPolicyIsNormalisedAtBootButNeverOverwrites(t *testing.T) {
	path := fileStore(t, "boot13.db")
	st, err := OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	def := st.DefaultGroupID()
	if _, err := st.exec(`UPDATE user_groups SET export_allowed=NULL,export_max_reports=NULL,export_daily_quota=NULL WHERE id=?`, def); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = OpenStore("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	assertDefaultExportPolicy(t, st)
	if _, err := st.exec(`UPDATE user_groups SET export_allowed=1,export_max_reports=7 WHERE id=?`, def); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureDefaultExportPolicy(); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, st, `SELECT export_max_reports FROM user_groups WHERE id=?`, def); n != 7 {
		t.Fatalf("a second call overwrote a decision: %d", n)
	}
	st.Close()
}

// ---------- 3. inheritance ----------

func TestReaderExportPolicyInheritsAndReportsItsSource(t *testing.T) {
	e := newReaderEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	grandchild := seedOU(t, e.s.st, child, nil)
	snap, err := e.s.st.loadOUPolicySnapshotFrom(e.s.st.db)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := snap.resolve(grandchild)
	if !p.ExportAllowed.Effective || p.ExportMax.Effective != 100 || p.ExportQuota.Effective != 1000 {
		t.Fatalf("grandchild = %v/%d/%d, want the parent's 1/100/1000", p.ExportAllowed.Effective, p.ExportMax.Effective, p.ExportQuota.Effective)
	}
	if p.ExportMax.SourceOU != e.ou || !p.ExportMax.Inherited || p.ExportMax.Own != nil {
		t.Fatalf("source = %+v", p.ExportMax)
	}
	// An override wins for the OU and everything below it that does not override in turn.
	e.setPolicy(child, 0, 10, nil)
	snap, _ = e.s.st.loadOUPolicySnapshotFrom(e.s.st.db)
	p, _ = snap.resolve(grandchild)
	if p.ExportAllowed.Effective || p.ExportMax.Effective != 10 || p.ExportMax.SourceOU != child {
		t.Fatalf("override not honoured: %+v %+v", p.ExportAllowed, p.ExportMax)
	}
	if p.ExportQuota.Effective != 1000 || p.ExportQuota.SourceOU != e.ou {
		t.Fatalf("an unset column must still inherit: %+v", p.ExportQuota)
	}
	// Not sticky: a sub-OU may switch export on while its parent has it off.
	e.setPolicy(e.ou, 0, 100, 1000)
	e.setPolicy(child, 1, nil, nil)
	snap, _ = e.s.st.loadOUPolicySnapshotFrom(e.s.st.db)
	if p, _ = snap.resolve(grandchild); !p.ExportAllowed.Effective {
		t.Fatal("allowed is not sticky: a child's explicit 1 must win over its parent's 0")
	}
	// The console endpoint reports the same.
	w := e.do("admin", "GET", "/api/admin/exports/policy", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out struct {
		Groups []struct {
			ID      int64
			Allowed struct {
				Effective bool
				SourceOU  int64 `json:"source_ou"`
			}
		}
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	found := false
	for _, g := range out.Groups {
		if g.ID == grandchild {
			found = true
			if !g.Allowed.Effective || g.Allowed.SourceOU != child {
				t.Fatalf("console entry = %+v", g)
			}
		}
	}
	if !found {
		t.Fatal("the grandchild is missing from the console listing")
	}
}

// ---------- 4. disabled ----------

func TestReaderExportDisabledAnswers403(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	e.setPolicy(e.ou, 0, 100, 1000)
	w := e.submit("rita", readerCreate)
	if w.Code != http.StatusForbidden || decodeBody(t, w)["error"] != "export_disabled" {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	if p := e.policyOf("rita"); p["allowed"] != false {
		t.Fatalf("policy = %v", p)
	}
	for _, c := range [][2]string{{"GET", "/api/exports/sources"}, {"POST", "/api/exports/preview"}} {
		if w := e.do("rita", c[0], c[1], `{"source":"reports"}`); w.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", c[0], c[1], w.Code)
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs`); n != 0 {
		t.Fatalf("%d jobs were created for a disabled reader", n)
	}
}

func TestReaderExportDefaultsToOffForAnOUThatNeverSetIt(t *testing.T) {
	e := newReaderEnv(t)
	e.setPolicy(e.ou, nil, nil, nil)
	if p := e.policyOf("rita"); p["allowed"] != false || p["max_reports"] != float64(200) || p["daily_quota"] != float64(1000) {
		t.Fatalf("policy = %v", p)
	}
}

// ---------- 5. scope ----------

func TestReaderExportNeverLeavesTheReadScope(t *testing.T) {
	e := newReaderEnv(t)
	e.addReader("ext", true)
	e.reports(3, "pub")
	e.reports(4, "internal")
	for _, q := range []string{
		`INSERT INTO report_versions(name,label,ord,visibility) VALUES('pub','',1,'all') ON CONFLICT(name) DO UPDATE SET visibility='all'`,
		`INSERT INTO version_grants(version,principal) VALUES('pub','u:ext')`,
	} {
		if _, err := e.s.st.exec(q); err != nil {
			t.Fatal(err)
		}
	}

	total := func(user string) float64 {
		w := e.do(user, "POST", "/api/exports/preview", `{"source":"reports","page_size":200}`)
		if w.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", w.Code, w.Body.String())
		}
		return decodeBody(t, w)["total"].(float64)
	}
	if got := total("rita"); got != 7 {
		t.Fatalf("an unrestricted reader previews %v, want all 7", got)
	}
	if got := total("ext"); got != 3 {
		t.Fatalf("the restricted reader previews %v, want only the 3 granted", got)
	}
	w := e.do("ext", "POST", "/api/exports/preview", `{"source":"reports","page_size":200}`)
	for _, row := range decodeBody(t, w)["rows"].([]any) {
		if v := row.(map[string]any)["version"]; v != "pub" {
			t.Fatalf("preview leaked a %v report", v)
		}
	}

	// The sources document counts and names only what the reader can read.
	w = e.do("ext", "GET", "/api/exports/sources", "")
	var src struct {
		Sources []struct {
			Key     string
			Total   int
			Formats []string
			Columns []struct{ Key string }
			Fields  []struct {
				Key     string
				Options []string
			}
		}
	}
	json.Unmarshal(w.Body.Bytes(), &src)
	if len(src.Sources) != 1 || src.Sources[0].Key != "reports" || src.Sources[0].Total != 3 ||
		strings.Join(src.Sources[0].Formats, ",") != "md_zip" {
		t.Fatalf("sources = %s", w.Body.String())
	}
	for _, c := range src.Sources[0].Columns {
		if c.Key == "source" || c.Key == "run_id" {
			t.Errorf("a reader is offered the %s column", c.Key)
		}
	}

	// The file holds exactly the readable reports, and the job is charged for them alone.
	w = e.submit("ext", readerCreate)
	if w.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", w.Code, w.Body.String())
	}
	job := decodeBody(t, w)
	if job["rows_estimate"] != float64(3) {
		t.Fatalf("estimate = %v", job["rows_estimate"])
	}
	done := e.run(job["id"].(string))
	if done.Status != exportDone || done.RowsDone != 3 {
		t.Fatalf("run: %+v", done)
	}
	for _, name := range zipNames(t, e.fileOf(done)) {
		if !strings.Contains(name, "pub") || strings.Contains(name, "internal") {
			t.Fatalf("archive entry %q is outside the reader's scope", name)
		}
	}
}

func TestReaderExportScopeIsReResolvedWhenTheWorkerStarts(t *testing.T) {
	e := newReaderEnv(t)
	e.addReader("ext", true)
	e.reports(3, "pub")
	for _, q := range []string{
		`INSERT INTO report_versions(name,label,ord,visibility) VALUES('pub','',1,'all') ON CONFLICT(name) DO UPDATE SET visibility='all'`,
		`INSERT INTO version_grants(version,principal) VALUES('pub','u:ext')`,
	} {
		if _, err := e.s.st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	job := decodeBody(t, e.submit("ext", readerCreate))
	if _, err := e.s.st.exec(`DELETE FROM version_grants WHERE principal='u:ext'`); err != nil {
		t.Fatal(err)
	}
	done := e.run(job["id"].(string))
	if done.Status != exportDone || done.RowsDone != 0 {
		t.Fatalf("a revoked grant was not honoured by a queued job: %+v", done)
	}
}

// ---------- 6. limits ----------

func TestReaderExportLimits(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(10, "V1")

	e.setPolicy(e.ou, 1, 9, 1000)
	w := e.submit("rita", readerCreate)
	body := decodeBody(t, w)
	if w.Code != http.StatusUnprocessableEntity || body["error"] != "export_too_large" || body["limit"] != float64(9) || body["count"] != float64(10) {
		t.Fatalf("over the per-export maximum: %d %s", w.Code, w.Body.String())
	}

	e.setPolicy(e.ou, 1, 10, 10)
	if w := e.submit("rita", readerCreate); w.Code != http.StatusCreated {
		t.Fatalf("exactly at both limits must pass: %d %s", w.Code, w.Body.String())
	}
	w = e.submit("rita", readerCreate)
	body = decodeBody(t, w)
	if w.Code != http.StatusTooManyRequests || body["error"] != "export_quota" ||
		body["limit"] != float64(10) || body["used"] != float64(10) || body["count"] != float64(10) {
		t.Fatalf("over the remaining quota: %d %s", w.Code, w.Body.String())
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(10) || p["remaining"] != float64(0) {
		t.Fatalf("policy = %v", p)
	}
	// Another reader has a quota of their own.
	e.addReader("rob", false)
	if w := e.submit("rob", readerCreate); w.Code != http.StatusCreated {
		t.Fatalf("quota is per user: %d %s", w.Code, w.Body.String())
	}
}

func TestReaderExportEmptyMatchIsRefused(t *testing.T) {
	e := newReaderEnv(t)
	w := e.submit("rita", readerCreate)
	if w.Code != http.StatusBadRequest || decodeBody(t, w)["error"] != "export_empty" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestReaderExportInvalidFilterIsA400(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(1, "V1")
	w := e.submit("rita", `{"source":"reports","filter":{"op":"and","items":[{"field":"nope","operator":"is","values":["x"]}]}}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// ---------- 7. concurrency ----------

func TestReaderExportConcurrentSubmitsCannotBothPassTheQuota(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(6, "V1")
	e.setPolicy(e.ou, 1, 100, 10)
	const n = 4 // each fits (6 <= 10); together they do not, and exactly one can be admitted
	reqs := make([]*http.Request, n)
	for i := range reqs {
		r := sessionForPath(t, e.s, "rita", "", "/api/exports")
		r.Method = "POST"
		r.Body = io.NopCloser(strings.NewReader(readerCreate))
		r.Header.Set("Content-Type", "application/json")
		reqs[i] = r
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, reqs[i])
			codes[i] = w.Code
		}()
	}
	wg.Wait()
	created, refused := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusTooManyRequests:
			refused++
		}
	}
	if created != 1 || refused != n-1 {
		t.Fatalf("codes = %v, want exactly one 201 and the rest 429", codes)
	}
	if got := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs`); got != 1 {
		t.Fatalf("%d jobs stored", got)
	}
}

// ---------- 8. usage and refunds ----------

func TestReaderExportUsageRefundsDownToWhatWasWritten(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(5, "V1")

	job := decodeBody(t, e.submit("rita", readerCreate))
	id := job["id"].(string)
	if p := e.policyOf("rita"); p["used_today"] != float64(5) {
		t.Fatalf("a queued job is charged its estimate: %v", p)
	}
	// Running with 2 written, then cancelled: charged 2, not 5.
	if _, err := e.s.st.exec(`UPDATE export_jobs SET status='running',rows_done=2 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(5) {
		t.Fatalf("a running job is still charged its estimate: %v", p)
	}
	if w := e.do("rita", "POST", "/api/exports/"+id+"/cancel", ""); w.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(2) || p["remaining"] != float64(998) {
		t.Fatalf("a cancelled job must be charged rows_done: %v", p)
	}

	// A job cancelled before it ever ran is refunded in full; a failed one is charged what it wrote.
	queued := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)
	e.do("rita", "POST", "/api/exports/"+queued+"/cancel", "")
	if p := e.policyOf("rita"); p["used_today"] != float64(2) {
		t.Fatalf("used = %v", p["used_today"])
	}
}

func TestReaderExportUsageIsPerPanelDay(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(5, "V1")
	decodeBody(t, e.submit("rita", readerCreate))
	// A job from before today's panel-timezone midnight is not charged to today.
	from, _ := e.s.exportDayBounds()
	if _, err := e.s.st.exec(`UPDATE export_jobs SET created_at=? WHERE requested_by='rita'`, from-1); err != nil {
		t.Fatal(err)
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(0) {
		t.Fatalf("yesterday's job is charged today: %v", p)
	}
}

// ---------- 9. cap ----------

func TestReaderExportNeverExceedsTheCapItWasChargedFor(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	job := decodeBody(t, e.submit("rita", readerCreate))
	e.reports(2, "V1late") // ingested after submit, before the worker starts
	done := e.run(job["id"].(string))
	if done.Status != exportDone || done.RowsDone != 3 {
		t.Fatalf("rows = %d status %s, want exactly the 3 it was charged for", done.RowsDone, done.Status)
	}
	if got := len(zipNames(t, e.fileOf(done))); got != 3 {
		t.Fatalf("archive holds %d entries", got)
	}
	var p exportParams
	json.Unmarshal([]byte(e.s.st.getExportJob(done.ID).Params), &p)
	if !p.Reader || p.Cap != 3 {
		t.Fatalf("params = %+v", p)
	}
}

// ---------- 10. switched off after submit ----------

func TestReaderExportSwitchedOffAfterSubmitFailsTheQueuedJob(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	job := decodeBody(t, e.submit("rita", readerCreate))
	e.setPolicy(e.ou, 0, 100, 1000)
	done := e.run(job["id"].(string))
	if done.Status != exportFailed || done.Error != "export_disabled" {
		t.Fatalf("%+v", done)
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(0) {
		t.Fatalf("a job that never wrote was not refunded: %v", p)
	}
	// A file that exists is not served once export is off.
	e.setPolicy(e.ou, 1, 100, 1000)
	ok := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)
	e.run(ok)
	e.setPolicy(e.ou, 0, 100, 1000)
	if w := e.do("rita", "GET", "/api/exports/"+ok+"/file", ""); w.Code != http.StatusForbidden {
		t.Fatalf("download after switch-off = %d", w.Code)
	}
}

// ---------- 11. other people's tasks ----------

func TestReaderExportOtherUsersTasksAre404(t *testing.T) {
	e := newReaderEnv(t)
	e.addReader("rob", false)
	e.reports(3, "V1")
	id := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)
	e.run(id)

	for _, c := range [][2]string{
		{"GET", "/api/exports/" + id},
		{"POST", "/api/exports/" + id + "/cancel"},
		{"GET", "/api/exports/" + id + "/file"},
	} {
		if w := e.do("rob", c[0], c[1], ""); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (never 403)", c[0], c[1], w.Code)
		}
	}
	w := e.do("rob", "GET", "/api/exports?limit=50", "")
	if n := len(decodeBody(t, w)["jobs"].([]any)); n != 0 {
		t.Fatalf("rob lists %d of rita's tasks", n)
	}
	// An unknown id is indistinguishable.
	if w := e.do("rob", "GET", "/api/exports/"+strings.Repeat("0", 32), ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id = %d", w.Code)
	}
	// The owner and the administrator both reach it.
	for _, u := range []string{"rita", "admin"} {
		if w := e.do(u, "GET", "/api/exports/"+id, ""); w.Code != http.StatusOK {
			t.Errorf("%s: %d", u, w.Code)
		}
	}
	if w := e.do("rita", "GET", "/api/exports?active=1", ""); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if n := len(decodeBody(t, e.do("admin", "GET", "/api/admin/exports", ""))["jobs"].([]any)); n != 1 {
		t.Fatalf("the admin console sees %d tasks", n)
	}
	// The unsigned-in are refused outright.
	if w := e.do("", "GET", "/api/exports", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", w.Code)
	}
}

func TestReaderCannotUseTheAdminExportRoutes(t *testing.T) {
	e := newReaderEnv(t)
	for _, p := range []string{"/api/admin/exports", "/api/admin/exports/policy", "/api/admin/exports/sources"} {
		if w := e.do("rita", "GET", p, ""); w.Code != http.StatusForbidden {
			t.Errorf("%s = %d", p, w.Code)
		}
	}
	if w := e.do("rita", "PUT", fmt.Sprintf("/api/admin/exports/policy/%d", e.ou), `{"allowed":true}`); w.Code != http.StatusForbidden {
		t.Errorf("a reader edited the policy: %d", w.Code)
	}
}

// ---------- 12. source and format ----------

func TestReaderExportRefusesOtherSourcesAndFormats(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(2, "V1")
	for name, body := range map[string]string{
		"audit source":  `{"source":"audit","format":"csv"}`,
		"forecasts":     `{"source":"forecasts","format":"csv"}`,
		"no source":     `{"format":"md_zip"}`,
		"csv format":    `{"source":"reports","format":"csv"}`,
		"jsonl format":  `{"source":"reports","format":"jsonl"}`,
		"unknown":       `{"source":"reports","format":"pdf"}`,
		"layout option": `{"source":"reports","options":{"layout":"spiral"}}`,
	} {
		if w := e.submit("rita", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := e.do("rita", "POST", "/api/exports/preview", `{"source":"audit"}`); w.Code != http.StatusBadRequest {
		t.Errorf("preview of audit = %d", w.Code)
	}
	// A reader cannot pick columns: the request is honoured with the fixed set.
	w := e.submit("rita", `{"source":"reports","columns":["run_id","source"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	cols := decodeBody(t, w)["columns"].([]any)
	if len(cols) != len(frontMatterColumns) {
		t.Fatalf("columns = %v", cols)
	}
	w = e.do("rita", "POST", "/api/exports/preview", `{"source":"reports","columns":["run_id"]}`)
	for _, row := range decodeBody(t, w)["rows"].([]any) {
		if _, leaked := row.(map[string]any)["run_id"]; leaked {
			t.Fatal("preview honoured a column choice")
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs`); n != 1 {
		t.Fatalf("%d jobs: a refused request created one", n)
	}
}

// ---------- 13. audit ----------

func TestReaderExportAuditRowsCarryTheReaderScope(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	job := decodeBody(t, e.submit("rita", readerCreate))
	id := job["id"].(string)
	e.run(id)
	if n := auditRowsOfType(t, e.s.st, AuditExportCreate); n != 1 {
		t.Fatalf("%d create rows", n)
	}
	detail := func(action string) map[string]any {
		var raw string
		if err := e.s.st.queryRow(`SELECT detail FROM audit_log WHERE action=?`, action).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		json.Unmarshal([]byte(raw), &d)
		return d
	}
	if detail(AuditExportCreate)["scope"] != "reader" {
		t.Fatalf("create detail = %v", detail(AuditExportCreate))
	}
	// A full download is one row; a resumed range is not another.
	for i := 0; i < 1; i++ {
		if w := e.do("rita", "GET", "/api/exports/"+id+"/file", ""); w.Code != http.StatusOK {
			t.Fatalf("download: %d %s", w.Code, w.Body.String())
		}
	}
	if w := e.do("rita", "GET", "/api/exports/"+id+"/file", "", "Range", "bytes=10-"); w.Code != http.StatusPartialContent {
		t.Fatalf("range = %d", w.Code)
	}
	if n := auditRowsOfType(t, e.s.st, AuditExportDownload); n != 1 {
		t.Fatalf("%d download rows, want 1", n)
	}
	if detail(AuditExportDownload)["scope"] != "reader" {
		t.Fatalf("download detail = %v", detail(AuditExportDownload))
	}
	// An administrator's export keeps the ADR 0045 shape, without the marker.
	e.create(`{"source":"reports","format":"md_zip"}`)
	var raw string
	e.s.st.queryRow(`SELECT detail FROM audit_log WHERE action=? ORDER BY id DESC LIMIT 1`, AuditExportCreate).Scan(&raw)
	if strings.Contains(raw, `"scope"`) {
		t.Fatalf("admin audit row carries a scope: %s", raw)
	}
}

// ---------- 14. the policy console ----------

func (e *readerEnv) putPolicy(ou int64, body string) *httptest.ResponseRecorder {
	return e.do("admin", "PUT", fmt.Sprintf("/api/admin/exports/policy/%d", ou), body)
}

func TestReaderExportPolicyEditRefusesInheritOnDefaultAndEnforcesBounds(t *testing.T) {
	e := newReaderEnv(t)
	def := e.s.st.DefaultGroupID()
	for _, body := range []string{`{"allowed":null}`, `{"max_reports":null}`, `{"daily_quota":null}`} {
		if w := e.putPolicy(def, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s on the Default OU = %d, want 400", body, w.Code)
		}
	}
	for _, body := range []string{
		`{"max_reports":0}`, `{"max_reports":5001}`, `{"max_reports":-3}`, `{"max_reports":1.5}`, `{"max_reports":"9"}`,
		`{"daily_quota":0}`, `{"daily_quota":100001}`, `{"allowed":"yes"}`, `{"allowed":2}`, `{bad`,
	} {
		if w := e.putPolicy(e.ou, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, w.Code)
		}
	}
	// A refused request changes nothing, even when another field in it was valid.
	if w := e.putPolicy(e.ou, `{"allowed":false,"max_reports":0}`); w.Code != http.StatusBadRequest {
		t.Fatalf("%d", w.Code)
	}
	if p := e.policyOf("rita"); p["allowed"] != true {
		t.Fatal("a refused edit was partly applied")
	}
	for _, body := range []string{`{"max_reports":1,"daily_quota":1}`, `{"max_reports":5000,"daily_quota":100000}`} {
		if w := e.putPolicy(e.ou, body); w.Code != http.StatusOK {
			t.Errorf("%s = %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := e.putPolicy(99999, `{"allowed":true}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown OU = %d", w.Code)
	}
}

func TestReaderExportPolicyEditIsTriStateAndAudited(t *testing.T) {
	e := newReaderEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	e.addReader("kid", false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username='kid'`, child); err != nil {
		t.Fatal(err)
	}
	// Absent = unchanged; a value = override.
	w := e.putPolicy(child, `{"max_reports":7}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var entry exportPolicyEntry
	json.Unmarshal(w.Body.Bytes(), &entry)
	if entry.MaxReports.Own == nil || *entry.MaxReports.Own != 7 || entry.Allowed.Own != nil || !entry.Allowed.Inherited {
		t.Fatalf("entry = %+v", entry)
	}
	if p := e.policyOf("kid"); p["max_reports"] != float64(7) || p["allowed"] != true {
		t.Fatalf("policy = %v", p)
	}
	// null = inherit again.
	if w := e.putPolicy(child, `{"max_reports":null}`); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if p := e.policyOf("kid"); p["max_reports"] != float64(100) {
		t.Fatalf("policy = %v", p)
	}
	// One audit row per changed field, with the field and both values; an unchanged value is not one.
	e.putPolicy(child, `{"allowed":false}`)
	e.putPolicy(child, `{"allowed":false}`)
	rows, err := e.s.st.query(`SELECT target_id,detail FROM audit_log WHERE action=? ORDER BY id`, AuditGroupChange)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []map[string]any
	for rows.Next() {
		var target, raw string
		rows.Scan(&target, &raw)
		var d map[string]any
		json.Unmarshal([]byte(raw), &d)
		if target != itoa64(child) {
			t.Fatalf("audited against %s", target)
		}
		details = append(details, d)
	}
	if len(details) != 3 {
		t.Fatalf("%d audit rows, want 3: %v", len(details), details)
	}
	if details[0]["field"] != "max_reports" || details[0]["from"] != nil || details[0]["to"] != float64(7) {
		t.Errorf("first = %v", details[0])
	}
	if details[1]["to"] != nil || details[1]["from"] != float64(7) {
		t.Errorf("second = %v", details[1])
	}
	if details[2]["field"] != "allowed" || details[2]["from"] != nil || details[2]["to"] != false {
		t.Errorf("third = %v", details[2])
	}
}

// ---------- administrators ----------

func TestReaderExportAdministratorsAreNotGovernedByThePolicy(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	p := e.policyOf("admin")
	if p["allowed"] != true || p["unlimited"] != true {
		t.Fatalf("policy = %v", p)
	}
	w := e.do("admin", "GET", "/api/exports/sources", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"audit"`) {
		t.Fatalf("an administrator is answered from the admin rules: %d", w.Code)
	}
	w = e.do("admin", "POST", "/api/exports", `{"source":"reports","format":"csv"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin submit: %d %s", w.Code, w.Body.String())
	}
	var params exportParams
	id := decodeBody(t, w)["id"].(string)
	json.Unmarshal([]byte(e.s.st.getExportJob(id).Params), &params)
	if params.Reader {
		t.Fatal("an administrator's job was marked as a reader job")
	}
}

// ---------- review fixes ----------

func archiveIDs(t *testing.T, raw []byte) []string {
	t.Helper()
	var ids []string
	for _, name := range zipNames(t, raw) {
		base := strings.TrimSuffix(name[strings.LastIndex(name, "/")+1:], ".md")
		ids = append(ids, base[strings.LastIndex(base, "_")+1:])
	}
	slices.Sort(ids)
	return ids
}

// Reports ingested after submit are newer than everything the job was charged for. Scanning newest
// first under a cap would drop the oldest counted reports to make room for them, so the job is bound
// to the ids that existed at submit.
func TestReaderExportContainsExactlyTheReportsCountedAtSubmit(t *testing.T) {
	e := newReaderEnv(t)
	e.reports(3, "V1")
	var want []string
	rows, err := e.s.st.query(`SELECT id FROM reports`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		want = append(want, id)
	}
	rows.Close()
	slices.Sort(want)

	job := decodeBody(t, e.submit("rita", readerCreate))
	e.reports(2, "V1late")
	done := e.run(job["id"].(string))
	if done.Status != exportDone || done.RowsDone != 3 {
		t.Fatalf("%+v", done)
	}
	if got := archiveIDs(t, e.fileOf(done)); !slices.Equal(got, want) {
		t.Fatalf("archive holds ids %v, want exactly the counted %v", got, want)
	}
	var p exportParams
	json.Unmarshal([]byte(e.s.st.getExportJob(done.ID).Params), &p)
	if p.MaxID == 0 {
		t.Fatal("the job did not record the highest report id at submit")
	}
}

// A username is re-registerable: a later holder must not inherit the previous one's tasks or usage.
func TestReaderExportDeletedUsersTasksDoNotPassToTheNextHolder(t *testing.T) {
	e := newReaderEnv(t)
	e.s.wireExportCleanup()
	e.reports(3, "V1")
	id := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)
	done := e.run(id)
	file := e.s.exportPath(e.s.st.getExportJob(done.ID))
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
	queued := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)

	if err := e.s.st.DeleteUser("rita"); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM export_jobs WHERE requested_by='rita'`); n != 0 {
		t.Fatalf("%d jobs survived the deletion", n)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("the finished file survived: %v", err)
	}
	e.addReader("rita", false)
	if n := len(decodeBody(t, e.do("rita", "GET", "/api/exports", ""))["jobs"].([]any)); n != 0 {
		t.Fatalf("the new holder lists %d inherited tasks", n)
	}
	for _, old := range []string{id, queued} {
		if w := e.do("rita", "GET", "/api/exports/"+old, ""); w.Code != http.StatusNotFound {
			t.Errorf("old task %s = %d, want 404", old, w.Code)
		}
	}
	if p := e.policyOf("rita"); p["used_today"] != float64(0) {
		t.Fatalf("usage inherited: %v", p)
	}
}

func TestReaderExportRunningJobOfADeletedUserIsNotResurrected(t *testing.T) {
	e := newReaderEnv(t)
	e.s.wireExportCleanup()
	e.s.exports.batchSize = 2
	e.reports(6, "V1")
	id := decodeBody(t, e.submit("rita", readerCreate))["id"].(string)
	e.s.exports.afterBatch = func(string, int) {
		e.s.exports.afterBatch = nil
		if err := e.s.st.DeleteUser("rita"); err != nil {
			t.Error(err)
		}
	}
	e.s.exportRunNext()
	if e.s.st.getExportJob(id) != nil {
		t.Fatal("the worker brought the deleted user's job back")
	}
	if entries, _ := os.ReadDir(e.s.exportDir()); len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}

// Pipeline plumbing types are not reports a reader can see anywhere else, so they are not exportable.
func TestReaderExportExcludesInternalPlumbingTypes(t *testing.T) {
	e := newReaderEnv(t)
	e.addReader("ext", true)
	var internal string
	for k := range internalTypes {
		internal = k
	}
	seedReports(e.t, e.s.st, 4, func(i int) Rep {
		rt, v := "note", "pub"
		if i < 2 {
			rt = internal
		}
		return Rep{Symbol: fmt.Sprintf("S%04d", i), Name: "N", Date: "2026-10-18", RType: rt, Kind: "alpha",
			Title: fmt.Sprintf("t%d", i), MD: "# body", Version: v}
	})
	for _, q := range []string{
		`INSERT INTO report_versions(name,label,ord,visibility) VALUES('pub','',1,'all') ON CONFLICT(name) DO UPDATE SET visibility='all'`,
		`INSERT INTO version_grants(version,principal) VALUES('pub','u:ext')`,
	} {
		if _, err := e.s.st.exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []string{"rita", "ext"} {
		w := e.do(user, "POST", "/api/exports/preview", `{"source":"reports","page_size":200}`)
		if got := decodeBody(t, w)["total"]; got != float64(2) {
			t.Fatalf("%s previews %v, want the 2 real reports", user, got)
		}
		for _, row := range decodeBody(t, w)["rows"].([]any) {
			if row.(map[string]any)["rtype"] == internal {
				t.Fatalf("%s preview leaked the plumbing type", user)
			}
		}
		w = e.do(user, "GET", "/api/exports/sources", "")
		if strings.Contains(w.Body.String(), internal) {
			t.Fatalf("%s sources name the plumbing type: %s", user, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"total":2`) {
			t.Fatalf("%s sources total: %s", user, w.Body.String())
		}
		sub := decodeBody(t, e.submit(user, readerCreate))
		if sub["rows_estimate"] != float64(2) {
			t.Fatalf("%s estimate = %v", user, sub["rows_estimate"])
		}
		done := e.run(sub["id"].(string))
		if done.RowsDone != 2 || len(zipNames(t, e.fileOf(done))) != 2 {
			t.Fatalf("%s archive: %+v", user, done)
		}
	}
	// The administrator path is unchanged: it still sees and counts everything.
	w := e.do("admin", "POST", "/api/exports/preview", `{"source":"reports","page_size":200}`)
	if got := decodeBody(t, w)["total"]; got != float64(4) {
		t.Fatalf("admin previews %v, want all 4", got)
	}
	if w := e.do("admin", "GET", "/api/admin/exports/sources", ""); !strings.Contains(w.Body.String(), internal) {
		t.Fatal("the admin type tree lost the plumbing type")
	}
}

// ---------- Postgres ----------

// TestPostgresReaderExport runs the parts that differ between the drivers: the step's columns, the
// policy read, the usage sum and the per-requester lock that serialises the quota check.
func TestPostgresReaderExport(t *testing.T) {
	st := pgStore(t, "export_jobs")
	for _, c := range []string{"export_allowed", "export_max_reports", "export_daily_quota"} {
		if !st.columnExists("user_groups", c) {
			t.Fatalf("user_groups.%s missing on Postgres", c)
		}
	}
	if !st.indexExists("idx_export_jobs_requester") {
		t.Fatal("idx_export_jobs_requester missing on Postgres")
	}
	s := &Server{st: st}
	ou, err := st.insertID(`INSERT INTO user_groups(name,description,created_at,weight,urgent_unlimited,is_default,export_allowed,
		export_max_reports,export_daily_quota) VALUES(?,?,?,0,0,0,1,50,10)`, fmt.Sprintf("pg-export-%d", ouNameSeq), "", nowStr())
	if err != nil {
		t.Fatal(err)
	}
	const user = "pg-export-reader"
	if err := st.UpsertUser(User{Username: user, PasswordHash: "hash", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.exec(`DELETE FROM export_jobs WHERE requested_by=?`, user)
		st.exec(`DELETE FROM users WHERE username=?`, user)
		st.exec(`DELETE FROM user_groups WHERE id=?`, ou)
	})
	if _, err := st.exec(`UPDATE users SET group_id=? WHERE username=?`, ou, user); err != nil {
		t.Fatal(err)
	}
	pol, err := st.readerExportPolicy(user)
	if err != nil || !pol.Allowed || pol.MaxReports != 50 || pol.DailyQuota != 10 {
		t.Fatalf("policy = %+v err=%v", pol, err)
	}
	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := &exportJob{ID: newExportID(), Source: "reports", Format: "md_zip", Params: "{}", RequestedBy: user,
				Status: exportQueued, RowsEstimate: 6, FileName: "x.zip", CreatedAt: s.exports.clock().UnixMilli()}
			_, errs[i] = s.submitReaderExport(j, 6, 10)
		}()
	}
	wg.Wait()
	admitted := 0
	for _, e := range errs {
		switch {
		case e == nil:
			admitted++
		case !errors.Is(e, errExportQuota):
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if admitted != 1 {
		t.Fatalf("%d of %d concurrent submits were admitted, want exactly 1", admitted, n)
	}
	from, to := s.exportDayBounds()
	if used, err := st.exportUsage(st.db, user, from, to); err != nil || used != 6 {
		t.Fatalf("usage = %d err=%v", used, err)
	}
}

// A job not submitted through the reader endpoints is never a reader's, even under the reader's
// name: an administrator deleted mid-submit can leave such a row behind, and a reader who later takes
// the name must not reach it (it can be any source, the audit log included).
func TestReaderExportNonReaderJobsUnderTheNameAreNotTheReaders(t *testing.T) {
	e := newReaderEnv(t)
	j := &exportJob{ID: strings.Repeat("a", 32), Source: "audit", Format: "csv", Params: `{"name":"x"}`,
		RequestedBy: "rita", Status: "queued", CreatedAt: 1}
	if err := e.s.st.insertExportJob(j); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{"GET", "/api/exports/" + j.ID},
		{"POST", "/api/exports/" + j.ID + "/cancel"},
		{"GET", "/api/exports/" + j.ID + "/file"},
	} {
		if w := e.do("rita", c[0], c[1], ""); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", c[0], c[1], w.Code)
		}
	}
	if n := len(decodeBody(t, e.do("rita", "GET", "/api/exports?limit=50", ""))["jobs"].([]any)); n != 0 {
		t.Fatalf("rita lists %d non-reader tasks", n)
	}
}

// A submit whose requester was deleted after authentication inserts nothing, on both drivers.
func TestReaderExportSubmitForADeletedRequesterInsertsNothing(t *testing.T) {
	e := newReaderEnv(t)
	j := &exportJob{ID: strings.Repeat("b", 32), Source: "reports", Format: "md_zip",
		Params: `{"name":"x","reader":true}`, RequestedBy: "ghost", Status: "queued", CreatedAt: 1}
	if _, err := e.s.submitReaderExport(j, 1, 1000); err == nil {
		t.Fatal("a submit for a missing account was accepted")
	}
	if got := e.s.st.getExportJob(j.ID); got != nil {
		t.Fatal("the job row was inserted")
	}
}
