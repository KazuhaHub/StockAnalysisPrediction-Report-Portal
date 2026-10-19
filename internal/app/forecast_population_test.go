package app

// The ledger start (ADR 0029, decisions V3, plan §4.4).
//
// The rules under test are the ones that make a boundary safe to introduce late: it is bounded by the
// ledger's own oldest set, it is written exactly once, it never touches a row, and it survives a
// restore without being moved. Every guard here fails closed — a refusal writes nothing.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cst builds an instant in the exchange's zone, which is what the boundary maths reasons in.
func cst(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, chinaZone)
}

// TestPopulationBoundIsTheFirstWeekdayCloseAfterTheEarliestSet pins the bound's arithmetic. The
// production shape (earliest set Sunday 2026-10-18 evening CST) gives Monday 2026-10-19 15:00 CST =
// 07:00Z; a report received before a weekday's close can be cut at that day's close.
func TestPopulationBoundIsTheFirstWeekdayCloseAfterTheEarliestSet(t *testing.T) {
	cases := []struct {
		name string
		at   time.Time
		want time.Time
	}{
		{"production: Sunday 2026-10-18 21:00 CST -> Monday 15:00 CST", cst(2026, time.October, 18, 21, 0),
			time.Date(2026, time.October, 19, 7, 0, 0, 0, time.UTC)},
		{"Friday 16:00 CST (after the close) -> Monday 07:00Z", cst(2026, time.October, 16, 16, 0),
			time.Date(2026, time.October, 19, 7, 0, 0, 0, time.UTC)},
		{"Friday 14:00 CST (before the close) -> that day's close", cst(2026, time.October, 16, 14, 0),
			time.Date(2026, time.October, 16, 7, 0, 0, 0, time.UTC)},
		{"Saturday -> Monday", cst(2026, time.October, 17, 10, 0),
			time.Date(2026, time.October, 19, 7, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := populationBound(tc.at).UTC(); !got.Equal(tc.want) {
			t.Errorf("%s: bound = %s, want %s", tc.name, got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
		}
	}
}

// TestLedgerStartIsBoundedWriteOnceAndAudited walks the whole save: the refusals, the success, the
// audit row, and the coverage effect.
func TestLedgerStartIsBoundedWriteOnceAndAudited(t *testing.T) {
	s := newV1Server(t)
	// A set on Sunday 2026-10-18 evening CST, the production shape. Its bound is 2026-10-19T07:00:00Z.
	seedAcceptedSetAt(t, s, "600160", "2026-10-18")
	// A second set, well after, so the two are distinguishable.
	seedAcceptedSetAt(t, s, "600161", "2026-10-25")

	post := func(pop string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.apiForecastSettingsSave(rec, httptest.NewRequest("POST", "/api/admin/forecast",
			strBody(`{"population_from":"`+pop+`"}`)), "admin")
		return rec
	}

	// The bound is the Monday close; anything after it is too late.
	if rec := post("2026-10-19T07:00:01Z"); rec.Code != http.StatusConflict || !hasCode(rec.Body.Bytes(), "forecast_population_too_late") {
		t.Fatalf("past the bound: %d %s", rec.Code, rec.Body.String())
	}
	// The future is the caller's error.
	if rec := post("2030-01-01T00:00:00Z"); rec.Code != http.StatusBadRequest || !hasCode(rec.Body.Bytes(), "forecast_population_future") {
		t.Fatalf("future instant: %d %s", rec.Code, rec.Body.String())
	}
	// Malformed is the caller's error too.
	if rec := post("not-a-time"); rec.Code != http.StatusBadRequest || !hasCode(rec.Body.Bytes(), "invalid_param") {
		t.Fatalf("malformed: %d %s", rec.Code, rec.Body.String())
	}

	// The decided value succeeds and excludes the 2026-10-18 set.
	if rec := post("2026-10-18T16:00:00Z"); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	got, ok := s.st.ForecastPopulationFrom()
	if !ok || !got.Equal(time.Date(2026, time.October, 18, 16, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored ledger start = %v (ok=%v)", got, ok)
	}
	// Write-once: a second save is refused.
	if rec := post("2026-10-18T17:00:00Z"); rec.Code != http.StatusConflict || !hasCode(rec.Body.Bytes(), "forecast_population_set") {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body.String())
	}

	// The audit row names the instant and the excluded set, with its state.
	rows := auditRows(t, s, AuditForecastPopulation)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	var detail struct {
		From          string         `json:"from"`
		ExcludedIDs   []int64        `json:"excluded_set_ids"`
		CountsByState map[string]int `json:"counts_by_state"`
	}
	if err := json.Unmarshal([]byte(rows[0].Detail), &detail); err != nil {
		t.Fatalf("audit detail: %v (%s)", err, rows[0].Detail)
	}
	if len(detail.ExcludedIDs) != 1 {
		t.Errorf("excluded ids = %v, want one", detail.ExcludedIDs)
	}
	if detail.CountsByState[predStateAccepted] != 1 {
		t.Errorf("counts_by_state = %v, want one accepted", detail.CountsByState)
	}
}

// TestPreLiveSetsNeverCountOrDuplicate: a set before the boundary is pre-live — counted in pre_live,
// absent from every other bucket, and unable to make a later real set a duplicate (plan B4).
func TestPreLiveSetsNeverCountOrDuplicate(t *testing.T) {
	s := newV1Server(t)
	// The early set, same symbol as the later one, before the boundary.
	earlyID := seedAcceptedSetAt(t, s, "600160", "2026-10-18")
	// The later set, same symbol, after the boundary. If the early one counted, this would be a
	// duplicate; it must be the symbol's decision.
	lateID := seedAcceptedSetAt(t, s, "600160", "2026-10-25")
	// Give both a settled baseline so the decision is a plain member/duplicate one.
	if _, err := s.st.exec(`UPDATE prediction_sets SET base_check='match', s0_fen=10000 WHERE id IN (?,?)`,
		earlyID, lateID); err != nil {
		t.Fatal(err)
	}

	boundary := time.Date(2026, time.October, 18, 16, 0, 0, 0, time.UTC)
	sets, err := s.st.loadForecastGroupSets(predTestRtype, "2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	_, _, cov := forecastMembership(sets, boundary)
	if cov.PreLive != 1 {
		t.Errorf("pre_live = %d, want 1", cov.PreLive)
	}
	if cov.Duplicate != 0 {
		t.Errorf("duplicate = %d, want 0: a pre-live set must not make a later one a duplicate", cov.Duplicate)
	}
	if cov.Member != 1 {
		t.Errorf("member = %d, want 1: the later set is the symbol's decision", cov.Member)
	}
	if cov.Reports != 2 {
		t.Errorf("reports = %d, want 2: coverage still accounts for every loaded row", cov.Reports)
	}

	// Without a boundary the early set IS the symbol's decision (D4), so the later real set becomes a
	// duplicate — exactly the B4 failure the boundary exists to prevent.
	_, _, covNo := forecastMembership(sets, time.Time{})
	if covNo.PreLive != 0 || covNo.Duplicate != 1 {
		t.Errorf("no boundary: pre_live=%d duplicate=%d, want 0/1", covNo.PreLive, covNo.Duplicate)
	}
}

// TestLedgerStartSaveNeverCallsThePoolInsideTheTransaction: the store's SQLite side is one connection,
// so a pooled call inside the save's transaction would deadlock. The 2-second deadline turns that
// deadlock into a failure rather than a hang.
func TestLedgerStartSaveNeverCallsThePoolInsideTheTransaction(t *testing.T) {
	s := newV1Server(t)
	seedAcceptedSetAt(t, s, "600160", "2026-10-18")

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, code, err := s.st.SaveForecastPopulationFrom(
			time.Date(2026, time.October, 18, 16, 0, 0, 0, time.UTC), time.Now().UTC()); err != nil || code != "" {
			t.Errorf("save: code=%q err=%v", code, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SaveForecastPopulationFrom did not return in 2s: a pooled call inside the transaction")
	}
}

// TestRestoreRulesForForecastKeys pins the restore rules (§3.3) for the write-once ledger start. A
// later PR adds its own rows here.
func TestRestoreRulesForForecastKeys(t *testing.T) {
	// A dump taken BEFORE the key was set: restoring it into a database that has the key carries the
	// live value forward rather than dropping it.
	src := newTestStore(t)
	seedLedgerStart(t, src, "2026-10-18T16:00:00Z")
	dump := dropMetaRow(t, dumpOf(t, src), setForecastPopulationFrom)

	dst := newTestStore(t)
	seedLedgerStart(t, dst, "2026-10-18T16:00:00Z")
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatalf("restore of a dump without the key: %v", err)
	}
	if got, ok := dst.ForecastPopulationFrom(); !ok || !got.Equal(ledgerStartValue) {
		t.Fatalf("the ledger start was not carried from live: %v (ok=%v)", got, ok)
	}

	// A dump and a live database that both set it and disagree are refused: the key is write-once.
	// Source sets one value; the destination holds another.
	srcB := newTestStore(t)
	seedLedgerStart(t, srcB, "2026-10-19T00:00:00Z")
	dumpB := dumpOf(t, srcB)
	dstB := newTestStore(t)
	seedLedgerStart(t, dstB, "2026-10-18T16:00:00Z")
	if _, err := dstB.restoreFrom(bytes.NewReader(dumpB), true); err == nil {
		t.Fatal("a restore that would move the ledger start was not refused")
	}

	// A dump and a live database that agree restore cleanly.
	srcC := newTestStore(t)
	seedLedgerStart(t, srcC, "2026-10-18T16:00:00Z")
	dumpC := dumpOf(t, srcC)
	dstC := newTestStore(t)
	seedLedgerStart(t, dstC, "2026-10-18T16:00:00Z")
	if _, err := dstC.restoreFrom(bytes.NewReader(dumpC), true); err != nil {
		t.Fatalf("a matching ledger start was refused: %v", err)
	}
}

// ledgerStartValue is the decided V3 value, used by several tests.
var ledgerStartValue = time.Date(2026, time.October, 18, 16, 0, 0, 0, time.UTC)

// seedLedgerStart writes the meta key directly, so a restore test can set up either side without
// going through the handler's bounds.
func seedLedgerStart(t *testing.T, st *Store, v string) {
	t.Helper()
	if err := st.SetSetting(setForecastPopulationFrom, v); err != nil {
		t.Fatalf("seed ledger start: %v", err)
	}
}

// dropMetaRow removes one meta row (identified by its `k` value) from a JSON-lines dump, simulating a
// backup taken before the key existed. The format is one object per line: a header, then a
// `{"table":...}` section header per table followed by one `{"row":[...]}` line per row.
func dropMetaRow(t *testing.T, dump []byte, key string) []byte {
	t.Helper()
	var out []string
	inTable := false
	colIdx := -1
	for _, line := range strings.Split(string(dump), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, `{"table":`) {
			var sec backupSection
			mustUnmarshal(t, line, &sec)
			inTable = sec.Table == "meta"
			colIdx = -1
			if inTable {
				for i, c := range sec.Columns {
					if c == "k" {
						colIdx = i
					}
				}
			}
			out = append(out, line)
			continue
		}
		if inTable && colIdx >= 0 {
			var row struct {
				Row []any `json:"row"`
			}
			mustUnmarshal(t, line, &row)
			if colIdx < len(row.Row) && row.Row[colIdx] == key {
				continue // drop this row
			}
		}
		out = append(out, line)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}
