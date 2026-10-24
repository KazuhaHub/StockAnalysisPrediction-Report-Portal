package app

// The unrecorded count (P2, plan B1).
//
// The count exists to catch store_failed: a covered ingest is supposed to leave a ledger row in every
// outcome, so a covered report with none means the ledger never heard about it. Before P2 it counted
// the entire history, so production showed 1259 pre-ledger reports as failures. The ledger start
// (P5) and a floor captured with it bound the count to the ledger's own lifetime.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// seedReportOnly inserts a report with no ledger row, as a failed ledger write leaves behind.
func seedReportOnly(t *testing.T, st *Store, symbol, date, version string) int64 {
	t.Helper()
	id, _, err := st.UpsertReport(Rep{
		Symbol: symbol, Date: date, RType: predTestRtype, Kind: "投资决策",
		Title: "unrecorded " + symbol + " " + date, MD: "body", Version: version,
	})
	if err != nil {
		t.Fatalf("seed report %s: %v", symbol, err)
	}
	return id
}

// TestUnrecordedIsHiddenUntilTheLedgerStartIsSet: without a boundary there is no floor, so the store
// reports ok=false and the page says "set the ledger start" rather than showing a number. FC.
func TestUnrecordedIsHiddenUntilTheLedgerStartIsSet(t *testing.T) {
	st := newTestStore(t)
	seedReportOnly(t, st, "600001", "2020-01-05", "")
	n, since, ok, err := st.countForecastUnrecorded(predTestRtype)
	if err != nil {
		t.Fatal(err)
	}
	if ok || n != 0 || since != "" {
		t.Fatalf("count = %d since %q ok=%v, want hidden while the start is unset", n, since, ok)
	}
}

// TestUnrecordedFloorFromIngestAudit is the B1 pin: the floor comes from the created ingest audit
// rows after the ledger start, and the count excludes pre-ledger reports, reports that have a set,
// hand-written reports, and other report types.
func TestUnrecordedFloorFromIngestAudit(t *testing.T) {
	st := newTestStore(t)
	from := time.Date(2020, time.June, 8, 0, 0, 0, 0, time.UTC)
	// Pre-ledger covered reports, with audit rows before the boundary.
	for i := 0; i < 40; i++ {
		id := seedReportOnly(t, st, "600"+pad3(i), "2020-01-05", "")
		seedIngestAudit(t, st, id, from.Add(-time.Hour))
	}
	// Two reports created after the boundary but WITH a set: they must not be counted, and their ids
	// pin the floor's "min - 1" arithmetic.
	for i := 0; i < 2; i++ {
		sym := "602" + pad3(i)
		id := seedReportOnly(t, st, sym, "2020-06-08", "")
		seedIngestAudit(t, st, id, from.Add(time.Hour))
		if _, _, err := st.InsertPredictionSet(id, &predSetRow{
			Symbol: sym, Rtype: predTestRtype, MadeAt: "2020-06-08T13:00:00Z", MadeOn: "2020-06-08",
			Cohort: "2020-06", Zone: predZone, State: predStateAccepted, PayloadSHA: "sha-" + sym,
		}, nil); err != nil {
			t.Fatalf("seed set: %v", err)
		}
	}
	if _, _, code, err := st.SaveForecastPopulationFrom(from, time.Now().UTC()); err != nil || code != "" {
		t.Fatalf("save: code=%q err=%v", code, err)
	}
	if floor, ok := st.forecastUnrecordedFloor(); !ok || floor != 40 {
		t.Fatalf("floor = %d ok=%v, want 40 (min post-boundary id 41 minus one; the audit branch)", floor, ok)
	}
	if n, _, ok, err := st.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 0 {
		t.Fatalf("count = %d ok=%v err=%v, want 0 (the two post-boundary reports have sets)", n, ok, err)
	}

	// A covered report after the boundary with no set counts 1.
	seedReportOnly(t, st, "602999", "2020-06-09", "")
	// A manual report counts 0.
	seedReportOnly(t, st, "602998", "2020-06-09", manualVersionName)
	// A report of another rtype counts 0 for this rtype.
	otherID, _, err := st.UpsertReport(Rep{Symbol: "602997", Date: "2020-06-09", RType: "行业分析", MD: "b"})
	if err != nil {
		t.Fatal(err)
	}
	_ = otherID
	if n, since, ok, err := st.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 1 {
		t.Fatalf("count = %d since=%q ok=%v err=%v, want 1", n, since, ok, err)
	}

	// Deleting every ingest audit row does not move the stored floor.
	if _, err := st.exec(`DELETE FROM audit_log WHERE action=?`, AuditReportIngest); err != nil {
		t.Fatal(err)
	}
	if n, _, _, _ := st.countForecastUnrecorded(predTestRtype); n != 1 {
		t.Errorf("count = %d after deleting the audit rows, want 1 (the floor is captured once)", n)
	}
}

// seedIngestAudit writes a report.ingest audit row with detail.created, as v1Ingest does, so the
// floor can be re-derived from the audit trail after a restore.
func seedIngestAudit(t *testing.T, st *Store, reportID int64, at time.Time) {
	t.Helper()
	if _, err := st.exec(`INSERT INTO audit_log(at, action, target_type, target_id, detail)
		VALUES(?,?,?,?,?)`, at.UTC().Format(time.RFC3339), AuditReportIngest, "report",
		strconv.FormatInt(reportID, 10), `{"created":true}`); err != nil {
		t.Fatalf("seed ingest audit: %v", err)
	}
}

// TestUnrecordedFloorReDerivedOnRestore: a dump without the floor keys still counts correctly after
// a restore, because the floor is re-derived from the ledger start and the restored audit rows.
func TestUnrecordedFloorReDerivedOnRestore(t *testing.T) {
	src := newTestStore(t)
	from := time.Date(2020, time.January, 6, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		id := seedReportOnly(t, src, "601"+pad3(i), "2020-01-05", "")
		seedIngestAudit(t, src, id, from.Add(-time.Hour))
	}
	if _, _, code, err := src.SaveForecastPopulationFrom(from, time.Now().UTC()); err != nil || code != "" {
		t.Fatalf("save: code=%q err=%v", code, err)
	}
	postID := seedReportOnly(t, src, "601999", "2020-01-06", "")
	seedIngestAudit(t, src, postID, from.Add(time.Hour))
	wantN, _, _, _ := src.countForecastUnrecorded(predTestRtype)

	// Remove the floor keys from the dump, as a dump written by a build that did not capture them.
	dump := dropMetaRow(t, dumpOf(t, src), setForecastUnrecordedFloorID)
	dump = dropMetaRow(t, dump, setForecastUnrecordedFloorAt)
	dump = dropMetaRow(t, dump, setForecastUnrecordedFloorMethod)

	dst := newTestStore(t)
	if _, err := dst.restoreFrom(strings.NewReader(string(dump)), true); err != nil {
		t.Fatalf("restore: %v", err)
	}
	gotN, _, ok, err := dst.countForecastUnrecorded(predTestRtype)
	if err != nil || !ok {
		t.Fatalf("count after restore: ok=%v err=%v", ok, err)
	}
	if gotN != wantN {
		t.Errorf("count after restore = %d, want %d (floor re-derived)", gotN, wantN)
	}
}

// TestStoreFailedEmitsWebhook: the second-failure branch fires predictions.store_failed exactly
// once, with the report identity, and a successful ingest sends nothing.
func TestStoreFailedEmitsWebhook(t *testing.T) {
	if !containsStr(availableEvents(), EventPredictionsStoreFailed) {
		t.Fatal("predictions.store_failed is not in availableEvents()")
	}
	s := newV1Server(t)
	type delivery struct{ event, body string }
	got := make(chan delivery, 4)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- delivery{r.Header.Get("X-Report-Portal-Event"), string(b)}
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()
	if _, err := s.st.CreateWebhook(recv.URL, []string{EventPredictionsStoreFailed}, ""); err != nil {
		t.Fatal(err)
	}

	// A successful ingest must deliver nothing.
	startNotificationRuntimeForTest(t, s)
	if code, m := ingestPost(t, s, ingestBody(string(predWire(nil)))); code != http.StatusOK || m["predictions_status"] != predStateAccepted {
		t.Fatalf("setup ingest: %d %v", code, m)
	}
	select {
	case d := <-got:
		t.Fatalf("a successful ingest delivered %s: %s", d.event, d.body)
	case <-time.After(300 * time.Millisecond):
	}

	// Break the ledger write so the next ingest takes the store-failed branch.
	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}
	// A DIFFERENT identity, so the ledger actually attempts the insert (an identical one is a
	// duplicate and never touches the dropped table).
	failing := strings.Replace(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"600161"`, 1)
	code, m := ingestPost(t, s, failing)
	if code != http.StatusOK || m["predictions_status"] != "store_failed" {
		t.Fatalf("ingest: %d %v", code, m)
	}
	select {
	case d := <-got:
		if d.event != EventPredictionsStoreFailed {
			t.Fatalf("event = %q, want %q", d.event, EventPredictionsStoreFailed)
		}
		for _, k := range []string{"report_id", "symbol", "rtype", "made_at", "error"} {
			if !strings.Contains(d.body, `"`+k+`"`) {
				t.Errorf("payload missing %q: %s", k, d.body)
			}
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no predictions.store_failed delivery")
	}
	// Exactly one: a duplicate would page twice.
	select {
	case d := <-got:
		t.Fatalf("a second delivery arrived: %s", d.event)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestEveryWebhookEventHasALabel keeps the event registry and the webhook UI in step: the UI labels
// each event with `webhook.event.<code>`, so an event without a label renders as a raw identifier.
func TestEveryWebhookEventHasALabel(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "src", "locales")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("locale bundles not present: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		var labels map[string]string
		if err := json.Unmarshal(raw, &labels); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		checked++
		for _, event := range availableEvents() {
			if strings.TrimSpace(labels["webhook.event."+event]) == "" {
				t.Errorf("%s has no webhook.event.%s, so the UI would show the raw event code", e.Name(), event)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no locale bundles were checked")
	}
}

func pad3(i int) string {
	s := strings.Repeat("0", 3)
	digits := []byte(s)
	n := i
	for j := 2; j >= 0; j-- {
		digits[j] = byte('0' + n%10)
		n /= 10
	}
	return string(digits)
}
