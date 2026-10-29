package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------- date semantics: after is on-or-after, before is strictly before ----------

func TestExportDateOnlyAfterIsOnOrAfterAndBeforeIsStrictlyBefore(t *testing.T) {
	e := newExportEnv(t)
	e.s.st.SetSetting("timezone", "UTC")
	stamps := []string{"2026-09-30T23:59:59Z", "2026-10-01T05:00:00Z", "2026-10-02T00:00:00Z"}
	seedAudit(t, e.s.st, len(stamps), func(i int) AuditEntry {
		return AuditEntry{At: stamps[i], Actor: "a", Action: AuditLogin, IP: "10.0.0.1"}
	})
	got := func(op, vals string) string {
		w := e.do("admin", "POST", "/api/admin/exports/preview",
			`{"source":"audit","columns":["at"],"filter":{"op":"and","items":[{"field":"at","operator":"`+op+`","values":`+vals+`}]}}`)
		var out struct{ Rows []map[string]any }
		json.Unmarshal(w.Body.Bytes(), &out)
		var at []string
		for _, r := range out.Rows {
			at = append(at, r["at"].(string))
		}
		return strings.Join(at, " ")
	}
	// A row at 05:00Z on the since day is included by "after <that day>".
	if g := got("after", `["2026-10-01"]`); g != "2026-10-02T00:00:00Z 2026-10-01T05:00:00Z" {
		t.Errorf("after the day = %q", g)
	}
	if g := got("before", `["2026-10-01"]`); g != "2026-09-30T23:59:59Z" {
		t.Errorf("before the day = %q", g)
	}
	// An RFC 3339 instant keeps the strict comparison.
	if g := got("after", `["2026-10-01T05:00:00Z"]`); g != "2026-10-02T00:00:00Z" {
		t.Errorf("after an instant = %q", g)
	}
}

func TestExportDateColumnAfterIsOnOrAfter(t *testing.T) {
	e := newExportEnv(t)
	seedReports(t, e.s.st, 3, func(i int) Rep {
		return Rep{Symbol: fmt.Sprintf("60000%d", i), Name: "N", Date: fmt.Sprintf("2026-10-0%d", i+1), RType: predTestRtype, Kind: "K", Title: fmt.Sprintf("t%d", i), MD: "b"}
	})
	w := e.do("admin", "POST", "/api/admin/exports/preview",
		`{"source":"reports","filter":{"op":"and","items":[{"field":"date","operator":"after","values":["2026-10-02"]}]}}`)
	var out struct{ Total int64 }
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 2 {
		t.Fatalf("date after 2026-10-02 counted %d (%s), want 2 (on or after)", out.Total, w.Body.String())
	}
	w = e.do("admin", "POST", "/api/admin/exports/preview",
		`{"source":"reports","filter":{"op":"and","items":[{"field":"date","operator":"before","values":["2026-10-02"]}]}}`)
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Total != 1 {
		t.Fatalf("date before 2026-10-02 counted %d, want 1", out.Total)
	}
}

// ---------- node bound ----------

func TestExportFilterBoundsTheTotalNodeCount(t *testing.T) {
	root := exportNode{Op: "and"}
	for i := 0; i < 5000; i++ {
		root.Items = append(root.Items, exportNode{Op: "or"})
	}
	err := validateExportFilter(exportSourceByKey("audit"), root)
	var fe *exportFilterError
	if err == nil || !asFilterErr(err, &fe) {
		t.Fatalf("5000 empty groups: err = %v, want a filter error", err)
	}
	e := newExportEnv(t)
	body, _ := json.Marshal(map[string]any{"source": "audit", "filter": root})
	if w := e.do("admin", "POST", "/api/admin/exports/preview", string(body)); w.Code != http.StatusBadRequest {
		t.Fatalf("over HTTP: %d, want 400", w.Code)
	}
	ok := exportNode{Op: "and"}
	for i := 0; i < exportMaxNodes-1; i++ {
		ok.Items = append(ok.Items, exportNode{Op: "or"})
	}
	if err := validateExportFilter(exportSourceByKey("audit"), ok); err != nil {
		t.Errorf("a filter exactly at the node limit was refused: %v", err)
	}
}

// ---------- audit rows are RFC 3339 UTC ----------

func TestSettingsAuditRowsAreRFC3339UTC(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.saveForecastFallback(ctx, "shadow", AuditEntry{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	if err := st.saveQuoteOrder(ctx, quoteSourceSina, AuditEntry{Actor: "admin"}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{AuditForecastFallback, AuditQuoteOrder} {
		var at string
		if err := st.db.QueryRow(st.bind(`SELECT at FROM audit_log WHERE action=?`), action).Scan(&at); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil || !strings.HasSuffix(at, "Z") {
			t.Errorf("%s wrote at=%q, want UTC RFC 3339", action, at)
		}
		if d := time.Since(ts); d < -time.Minute || d > time.Minute {
			t.Errorf("%s at=%q is not now", action, at)
		}
	}
}

// ---------- HEAD is not a download ----------

func TestExportHeadRequestIsNotRecordedAsADownload(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 3, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditLogin, IP: "10.0.0.1"}
	})
	j := e.run(e.create(`{"source":"audit","format":"csv"}`).ID)
	if w := e.do("admin", "HEAD", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != 200 {
		t.Fatalf("HEAD: %d", w.Code)
	}
	if n := auditRowsOfType(t, e.s.st, AuditExportDownload); n != 0 {
		t.Fatalf("HEAD recorded %d download rows", n)
	}
	if w := e.do("admin", "GET", "/api/admin/exports/"+j.ID+"/file", ""); w.Code != 200 {
		t.Fatalf("GET: %d", w.Code)
	}
	if n := auditRowsOfType(t, e.s.st, AuditExportDownload); n != 1 {
		t.Fatalf("GET recorded %d download rows, want 1", n)
	}
}

// ---------- pricing: only what is shown, never the whole candidate set ----------

// countingVendor wraps a price table and records how many symbols each upstream batch asked for.
func (e *exportEnv) countingVendor(prices map[string]int64) (v *quoteVendorStub, asked func() []int) {
	v = newExportQuoteVendor(prices)
	inner := v.parseBatch
	var mu sync.Mutex
	var sizes []int
	v.parseBatch = func(targets []quoteTarget) (map[string]*QuoteResp, error) {
		mu.Lock()
		sizes = append(sizes, len(targets))
		mu.Unlock()
		return inner(targets)
	}
	wireQuoteSources(e.s, v, failingStub("sina has no batch endpoint"))
	return v, func() []int { mu.Lock(); defer mu.Unlock(); return append([]int(nil), sizes...) }
}

func TestExportForecastsLatestModePreviewKeepsPricesForRowsFromEarlierBatches(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 2
	prices := map[string]int64{}
	for i := 0; i < 7; i++ {
		sym := fmt.Sprintf("60010%d", i)
		prices[sym] = 900
		seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: fmt.Sprintf("2026-10-%02dT14:40:12Z", 10+i), entry: 1000, term: 1})
	}
	e.vendor(prices)
	pv, code, body := e.preview(`{"source":"forecasts","filter":{"op":"and","items":[` + yesFilter("latest_per_symbol") + `]}}`)
	if code != 200 || len(pv.Rows) != 7 {
		t.Fatalf("%d rows=%d %s", code, len(pv.Rows), body)
	}
	for _, r := range pv.Rows {
		if r["latest_price"] == nil || r["latest_at"] == nil || r["vs_entry_pct"] == nil {
			t.Errorf("row %v lost its price columns", r["symbol"])
		}
	}
}

func TestExportForecastsCountNeverCallsTheVendorWithoutBelowEntry(t *testing.T) {
	e := newExportEnv(t)
	prices := map[string]int64{}
	for i := 0; i < 120; i++ {
		sym := fmt.Sprintf("60%04d", i)
		prices[sym] = 900
		seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: "2026-10-12T14:40:12Z", entry: 1000, term: 1})
	}
	v, asked := e.countingVendor(prices)
	for _, filter := range []string{`{"op":"and","items":[]}`, `{"op":"and","items":[` + yesFilter("latest_per_symbol") + `]}`} {
		plan, err := e.s.newExportPlan(exportSourceByKey("forecasts"), mustFilter(t, filter))
		if err != nil {
			t.Fatal(err)
		}
		plan.withColumns([]string{"symbol", "latest_price", "vs_entry_pct"})
		if n, err := plan.count(context.Background()); err != nil || n != 120 {
			t.Fatalf("count = %d, %v", n, err)
		}
	}
	if v.calls != 0 {
		t.Fatalf("count reached the vendor %d times without a below_entry condition", v.calls)
	}
	// With below_entry, membership needs prices, so the count prices the candidates.
	plan, _ := e.s.newExportPlan(exportSourceByKey("forecasts"), mustFilter(t, belowFilter()))
	if n, err := plan.count(context.Background()); err != nil || n != 120 {
		t.Fatalf("below count = %d, %v", n, err)
	}
	if len(asked()) == 0 {
		t.Fatal("below_entry count did not price the candidates")
	}
}

func TestExportForecastsPreviewPricesOnlyTheReturnedPage(t *testing.T) {
	e := newExportEnv(t)
	prices := map[string]int64{}
	for i := 0; i < 120; i++ {
		sym := fmt.Sprintf("60%04d", i)
		prices[sym] = 900
		seedForecast(t, e.s.st, forecastSeed{symbol: sym, madeAt: fmt.Sprintf("2026-10-%02dT14:40:12Z", 1+i%28), entry: 1000, term: 1})
	}
	_, asked := e.countingVendor(prices)
	for _, filter := range []string{`{"op":"and","items":[]}`, `{"op":"and","items":[` + yesFilter("latest_per_symbol") + `]}`} {
		pv, code, body := e.preview(`{"source":"forecasts","page_size":50,"filter":` + filter + `}`)
		if code != 200 || pv.Total != 120 || len(pv.Rows) != 50 {
			t.Fatalf("%d total=%d rows=%d %s", code, pv.Total, len(pv.Rows), body[:min(len(body), 200)])
		}
		for _, r := range pv.Rows {
			if r["latest_price"] == nil {
				t.Fatalf("a shown row has no price: %v", r)
			}
		}
	}
	total := 0
	for _, n := range asked() {
		total += n
	}
	if total > 100 { // two previews of 50 rows each; the other 70 candidates are never priced
		t.Fatalf("priced %d symbols across two 50-row previews (batches %v)", total, asked())
	}
}

func mustFilter(t *testing.T, raw string) exportNode {
	t.Helper()
	var n exportNode
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---------- cancel races ----------

func TestExportWorkerDoesNotFinalizeAJobCancelledAfterItsLastBatch(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 100
	seedAudit(t, e.s.st, 200, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, IP: "10.0.0.1"}
	})
	j := e.create(`{"source":"audit","format":"csv"}`)
	// The row is closed out behind the worker's back (no context signal), after the last batch.
	e.s.exports.afterBatch = func(id string, batch int) {
		if batch == 2 {
			e.s.st.exec(`UPDATE export_jobs SET status='canceled' WHERE id=?`, id)
		}
	}
	done := e.run(j.ID)
	if done.Status != exportCanceled {
		t.Fatalf("status %s, the cancel was overwritten by done", done.Status)
	}
	entries, _ := os.ReadDir(e.s.exportDir())
	if len(entries) != 0 {
		t.Fatalf("a cancelled job left its finished file behind: %v", entries)
	}
}

func TestExportCancelBetweenClaimAndRegistrationIsNotLost(t *testing.T) {
	e := newExportEnv(t)
	seedAudit(t, e.s.st, 50, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, IP: "10.0.0.1"}
	})
	j := e.create(`{"source":"audit","format":"csv"}`)
	var batches int
	e.s.exports.afterBatch = func(string, int) { batches++ }
	e.s.exports.afterClaim = func(id string) {
		w := e.do("admin", "POST", "/api/admin/exports/"+id+"/cancel", "")
		if w.Code != 200 {
			t.Errorf("cancel: %d %s", w.Code, w.Body.String())
		}
	}
	done := e.run(j.ID)
	if done.Status != exportCanceled || batches != 0 {
		t.Fatalf("status %s after %d batches: the cancel was lost", done.Status, batches)
	}
	entries, _ := os.ReadDir(e.s.exportDir())
	if len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}

func TestExportCancelOfAJobClaimedMeanwhileDoesNotAnswerRunning(t *testing.T) {
	e := newExportEnv(t)
	j := e.create(`{"source":"cleanup","format":"csv"}`)
	e.s.exports.beforeQueuedCancel = func(id string) {
		if _, err := e.s.st.claimNextExportJob(time.Now().UnixMilli()); err != nil {
			t.Error(err)
		}
	}
	w := e.do("admin", "POST", "/api/admin/exports/"+j.ID+"/cancel", "")
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	var got exportJobJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status == exportRunning {
		t.Fatalf("answered 200 with a running job: the cancel was lost")
	}
	if cur := e.s.st.getExportJob(j.ID); cur.Status != exportCanceled {
		t.Fatalf("stored status %s", cur.Status)
	}
}

func TestExportCancelOfAJobClaimedMeanwhileSignalsTheLiveWorker(t *testing.T) {
	e := newExportEnv(t)
	e.s.exports.batchSize = 50
	seedAudit(t, e.s.st, 500, func(i int) AuditEntry {
		return AuditEntry{At: atStamp(i), Actor: "a", Action: AuditReportRead, IP: "10.0.0.1"}
	})
	j := e.create(`{"source":"audit","format":"csv"}`)
	// The worker claims the job after the handler read it as queued, and is live by the time the
	// handler falls through to the running path.
	started := make(chan struct{})
	release := make(chan struct{})
	e.s.exports.afterBatch = func(string, int) {
		select {
		case <-started:
		default:
			close(started)
			<-release
		}
	}
	var ran sync.WaitGroup
	e.s.exports.beforeQueuedCancel = func(string) {
		ran.Add(1)
		go func() { defer ran.Done(); e.s.exportRunNext() }()
		<-started
		go func() { time.Sleep(100 * time.Millisecond); close(release) }()
	}
	w := e.do("admin", "POST", "/api/admin/exports/"+j.ID+"/cancel", "")
	ran.Wait()
	var got exportJobJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || got.Status != exportCanceled {
		t.Fatalf("cancel: %d status %s", w.Code, got.Status)
	}
	entries, _ := os.ReadDir(e.s.exportDir())
	if len(entries) != 0 {
		t.Fatalf("files left behind: %v", entries)
	}
}
