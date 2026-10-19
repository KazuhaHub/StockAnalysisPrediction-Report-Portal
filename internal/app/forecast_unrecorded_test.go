package app

// The unrecorded count (P2, plan B1).
//
// The count exists to catch store_failed: a covered ingest is supposed to leave a ledger row in every
// outcome, so a covered report with none means the ledger never heard about it. Before P2 it counted
// the entire history, so production showed 1259 pre-ledger reports as failures. The ledger start
// (P5) and a floor captured with it bound the count to the ledger's own lifetime.

import (
	"io"
	"net/http"
	"net/http/httptest"
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

// TestUnrecordedFloorFromIngestAudit is the B1 pin: pre-ledger covered reports count 0, a covered
// report created after the ledger start with no set counts 1, a manual version counts 0, and deleting
// the audit rows afterwards does not move the floor.
func TestUnrecordedFloorFromIngestAudit(t *testing.T) {
	st := newTestStore(t)
	// A batch of pre-ledger covered reports. (The production number was 1259; the shape is the same.)
	const preLedger = 40
	lastPre := int64(0)
	for i := 0; i < preLedger; i++ {
		lastPre = seedReportOnly(t, st, "600"+pad3(i), "2020-01-05", "")
	}
	// The ledger start is set now; there are no sets, so the bound and maturity checks are vacuous and
	// the floor falls back to MAX(reports.id) = the last pre-ledger report.
	now := time.Now().UTC()
	if _, _, code, err := st.SaveForecastPopulationFrom(now, now); err != nil || code != "" {
		t.Fatalf("save: code=%q err=%v", code, err)
	}
	if n, _, ok, err := st.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 0 {
		t.Fatalf("pre-ledger count = %d ok=%v err=%v, want 0", n, ok, err)
	}

	// A covered report created after the ledger start with no set counts 1.
	postID := seedReportOnly(t, st, "600999", "2020-01-06", "")
	if postID <= lastPre {
		t.Fatalf("setup: post-ledger report id %d is not after the floor %d", postID, lastPre)
	}
	if n, since, ok, err := st.countForecastUnrecorded(predTestRtype); err != nil || !ok || n != 1 {
		t.Fatalf("post-ledger count = %d since=%q ok=%v err=%v, want 1", n, since, ok, err)
	}

	// A hand-written (manual) report counts 0: it is not a workflow ingest and never was.
	seedReportOnly(t, st, "600998", "2020-01-06", manualVersionName)
	if n, _, _, _ := st.countForecastUnrecorded(predTestRtype); n != 1 {
		t.Errorf("count = %d after a manual report, want 1 (manual is excluded)", n)
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

// TestStoreFailedEmitsWebhook: the second-failure branch fires predictions.store_failed with the
// report identity, and the event is offered to subscribers.
func TestStoreFailedEmitsWebhook(t *testing.T) {
	if !containsStr(availableEvents(), EventPredictionsStoreFailed) {
		t.Fatal("predictions.store_failed is not in availableEvents()")
	}
	s := newV1Server(t)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer recv.Close()
	whID, err := s.st.CreateWebhook(recv.URL, []string{EventPredictionsStoreFailed}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Break the ledger write so the ingest takes the store-failed branch.
	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}
	code, m := ingestPost(t, s, ingestBody(string(predWire(nil))))
	if code != http.StatusOK || m["predictions_status"] != "store_failed" {
		t.Fatalf("ingest: %d %v", code, m)
	}
	// The delivery is asynchronous; poll the subscriber's recorded status for a 2xx.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status int
		if err := s.st.queryRow(`SELECT last_status FROM webhooks WHERE id=?`, whID).Scan(&status); err == nil && status == http.StatusOK {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no successful predictions.store_failed delivery")
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
