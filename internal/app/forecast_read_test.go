package app

// Reading the ledger (ADR 0029 §5, plan §4.4, design §11 #19).
//
// The rules these pin are the ones a mistake in would be invisible: a scope that is not applied still
// returns sets, just too many of them, and nothing about the response looks wrong to whoever wrote it.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// seedLedgerFor writes a report and an accepted set for it, and returns both ids.
func seedLedgerFor(t *testing.T, s *Server, symbol, date, bodyMD string) (int64, int64) {
	t.Helper()
	body := ingestBody(string(predWire(nil)))
	body = body[:len(body)-1] + `,"body_md":"` + bodyMD + `"}`
	body = replaceOnce(body, `"symbol":"600160"`, `"symbol":"`+symbol+`"`)
	body = replaceOnce(body, `"date":"2026-10-17"`, `"date":"`+date+`"`)
	code, m := ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("seed ingest: %d %v", code, m)
	}
	id, _ := m["id"].(float64)
	var setID int64
	if err := s.st.queryRow("SELECT id FROM prediction_sets WHERE report_id=? ORDER BY id DESC LIMIT 1",
		int64(id)).Scan(&setID); err != nil {
		t.Fatalf("read seeded set id: %v", err)
	}
	return int64(id), setID
}

func replaceOnce(s, old, new string) string {
	i := indexOf(s, old)
	if i < 0 {
		return s
	}
	return s[:i] + new + s[i+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestReportPredictionsRouteReturnsTheSets is the base case, and it also pins the ORDER: a report is
// read in the order it was written.
func TestReportPredictionsRouteReturnsTheSets(t *testing.T) {
	s := newV1Server(t)
	reportID, firstSet := seedLedgerFor(t, s, "600160", "2026-10-17", "# v1")
	// A revision: same report, a changed number.
	moved := predWire(func(m map[string]any) {
		claim(m, "expected_terminal", func(c map[string]any) { c["price"] = "11.20" })
	})
	body := ingestBody(string(moved))
	body = replaceOnce(body, `"body_md":"# body"`, `"body_md":"# v2"`)
	if code, m := ingestPost(t, s, body); code != http.StatusOK {
		t.Fatalf("revision ingest: %d %v", code, m)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/reports/1/predictions", nil)
	req.SetPathValue("id", itoa(reportID))
	req.Header.Set("Authorization", "Bearer tok-all")
	s.v1ReportPredictions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK           bool            `json:"ok"`
		Sets         []PredictionSet `json:"sets"`
		CountedSetID int64           `json:"counted_set_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sets) != 2 {
		t.Fatalf("sets = %d, want 2 (the first write and its revision)", len(out.Sets))
	}
	if out.Sets[0].ID != firstSet {
		t.Errorf("first set id = %d, want %d: a report reads oldest first", out.Sets[0].ID, firstSet)
	}
	if out.Sets[1].ID <= out.Sets[0].ID {
		t.Error("sets are not in write order")
	}
	// Membership is the EARLIEST accepted set, so the revision does not replace it.
	if out.CountedSetID != firstSet {
		t.Errorf("counted_set_id = %d, want the earliest accepted set %d (D4: a rerun never replaces it)",
			out.CountedSetID, firstSet)
	}
	// The claims and marks come back with each set.
	if len(out.Sets[0].Claims) != 6 {
		t.Errorf("claims = %d, want 6", len(out.Sets[0].Claims))
	}
	if len(out.Sets[0].Marks) != len(predCheckpoints) {
		t.Errorf("marks = %d, want %d", len(out.Sets[0].Marks), len(predCheckpoints))
	}
	// Marks are ordered by the vocabulary, not alphabetically: d5 before d120, h12 last.
	if out.Sets[0].Marks[0].Checkpoint != "d5" {
		t.Errorf("first mark = %q, want d5", out.Sets[0].Marks[0].Checkpoint)
	}
	if last := out.Sets[0].Marks[len(out.Sets[0].Marks)-1].Checkpoint; last != "h12" {
		t.Errorf("last mark = %q, want h12", last)
	}
	// body_changed is the whole reason the body hash is stored: the revision overwrote the body.
	if !out.Sets[0].BodyChanged {
		t.Error("body_changed = false for a set whose report body was overwritten by a later ingest")
	}
	if out.Sets[1].BodyChanged {
		t.Error("body_changed = true for the set whose body is the current one")
	}
}

// TestReportPredictionsRouteHidesAnUnreadableReport: a report the caller cannot read answers 404.
// An empty list would say "this report exists and predicted nothing", which is a different claim.
func TestReportPredictionsRouteHidesAnUnreadableReport(t *testing.T) {
	s := newV1Server(t)
	seedLedgerFor(t, s, "600160", "2026-10-17", "# body")

	// A restricted viewer with no grant on this report.
	sc := &ownerScope{self: userPrincipal("outsider"), principals: []string{userPrincipal("outsider")},
		versOwner: []string{"default"}}
	if s.canReadReport(1, sc) {
		t.Fatal("setup: the restricted scope can read the report")
	}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{ReportID: 1, Limit: 200}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Fatalf("a restricted scope saw %d sets, want 0", len(sets))
	}
	if s.canReadReport(1, nil) != true {
		t.Error("an unrestricted scope cannot read the report it was seeded with")
	}
}

// TestPurgedSetIsVisibleOnlyToAnUnrestrictedCaller is the fail-closed rule the whole design rests on.
// Once the report is gone there is nothing to scope against, so a restricted reader cannot prove
// access — and guessing in their favour is the one direction this must never fail in.
func TestPurgedSetIsVisibleOnlyToAnUnrestrictedCaller(t *testing.T) {
	s := newV1Server(t)
	reportID, setID := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")

	// Purge the report the way both real paths do.
	if _, err := s.st.DeleteReport(reportID); err != nil {
		t.Fatal(err)
	}
	var stillThere int
	if err := s.st.queryRow("SELECT COUNT(*) FROM prediction_sets WHERE id=?", setID).Scan(&stillThere); err != nil {
		t.Fatal(err)
	}
	if stillThere != 1 {
		t.Fatal("the set was deleted with its report; the ledger is supposed to outlive it")
	}
	var reportRef any
	if err := s.st.queryRow("SELECT report_id FROM prediction_sets WHERE id=?", setID).Scan(&reportRef); err != nil {
		t.Fatal(err)
	}
	if reportRef != nil {
		t.Fatalf("report_id = %v, want NULL after a purge", reportRef)
	}

	// Unrestricted: visible.
	all, err := s.st.QueryPredictionSets(PredictionFilter{Symbol: "600160"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("unrestricted scope saw %d orphaned sets, want 1", len(all))
	}

	// Restricted: invisible, because there is no report left to scope against.
	sc := &ownerScope{self: userPrincipal("outsider"), principals: []string{userPrincipal("outsider")},
		versOwner: []string{"default"}}
	none, err := s.st.QueryPredictionSets(PredictionFilter{Symbol: "600160"}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("a restricted scope saw %d orphaned sets, want 0 (fail closed)", len(none))
	}
}

// TestOwnerGroupIsNotAReadKey: owner_group is carried on the set for display, and must never be
// consulted for access. A set whose report is unreadable stays unreadable even when the copied group
// is one the caller belongs to — otherwise the column would be a second, divergent spelling of the
// security filter, which is what ADR 0024 removed it as.
func TestOwnerGroupIsNotAReadKey(t *testing.T) {
	s := newV1Server(t)
	reportID, setID := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")
	// Give the set a group the restricted viewer belongs to.
	if _, err := s.st.exec("UPDATE prediction_sets SET symbol=symbol WHERE id=?", setID); err != nil {
		t.Fatal(err)
	}
	var group int64 = 1
	if _, err := s.st.exec("UPDATE reports SET owner_group=? WHERE id=?", group, reportID); err != nil {
		t.Fatal(err)
	}

	sc := &ownerScope{self: userPrincipal("outsider"), principals: []string{userPrincipal("outsider"), groupPrincipal(group)},
		versOwner: []string{"default"}}
	sets, err := s.st.QueryPredictionSets(PredictionFilter{Symbol: "600160"}, sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Fatalf("a restricted scope saw %d sets via owner_group; owner_group must not be a read key", len(sets))
	}
}

// TestQueryPredictionsRouteFiltersAndRefuses: symbol is required, dates are validated, and the
// made_on range is inclusive on both ends.
func TestQueryPredictionsRouteFiltersAndRefuses(t *testing.T) {
	s := newV1Server(t)
	seedLedgerFor(t, s, "600160", "2026-10-17", "# a")
	seedLedgerFor(t, s, "600160", "2026-11-02", "# b")
	seedLedgerFor(t, s, "300750", "2026-10-20", "# c")

	get := func(query string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/predictions?"+query, nil)
		req.Header.Set("Authorization", "Bearer tok-all")
		s.v1QueryPredictions(rec, req)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}

	if code, _ := get("from=2026-10-01"); code != http.StatusBadRequest {
		t.Errorf("status without symbol = %d, want 400", code)
	}
	if code, _ := get("symbol=600160&from=2026-10-1"); code != http.StatusBadRequest {
		t.Errorf("status with a malformed date = %d, want 400", code)
	}

	code, m := get("symbol=600160")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, m)
	}
	if sets, _ := m["sets"].([]any); len(sets) != 2 {
		t.Errorf("sets for 600160 = %d, want 2", len(sets))
	}

	// made_on is the RECEIPT date in Shanghai, not the report's own `date` field. The two sets were
	// both received now, so they share a made_on however different their dates are — which is the
	// property that keeps a backdated report from being filed as a forecast made in the past.
	today, err := shanghaiDate(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, m = get("symbol=600160")
	sets, _ := m["sets"].([]any)
	first, _ := sets[0].(map[string]any)
	if got := first["made_on"]; got != today {
		t.Errorf("made_on = %v, want the receipt date %s (never the payload's date)", got, today)
	}
	// And the range filter is inclusive at both ends against that date.
	_, m = get("symbol=600160&from=" + today + "&to=" + today)
	if inRange, _ := m["sets"].([]any); len(inRange) != 2 {
		t.Errorf("sets inside a one-day window = %d, want 2", len(inRange))
	}
	// A window that ends the day before receipt excludes both.
	_, m = get("symbol=600160&to=2020-01-01")
	if outside, _ := m["sets"].([]any); len(outside) != 0 {
		t.Errorf("sets before receipt = %d, want 0", len(outside))
	}
}

// TestQueryPredictionsRequiresAFilter: an unfiltered query would scan the ledger, and the store
// refuses it too, so a caller that forgets cannot turn a read into a table scan.
func TestQueryPredictionsRequiresAFilter(t *testing.T) {
	var sc *ownerScope
	s := newV1Server(t)
	if _, err := s.st.QueryPredictionSets(PredictionFilter{}, sc); err == nil {
		t.Error("the store accepted a filterless query")
	}
}

// TestBrowserRouteIsASessionRoute: the SPA's route is under the session class, so the whole-portal
// session policy covers it — which the machine route is not, whatever its handlers admit.
func TestBrowserRouteIsASessionRoute(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	for _, row := range s.routeTable {
		if row.pattern == "GET /api/report/{id}/predictions" {
			if row.class != authSession {
				t.Errorf("class = %v, want authSession", row.class)
			}
			return
		}
	}
	t.Fatal("GET /api/report/{id}/predictions is not registered")
}

// TestReportPredictionsRouteRefusesAnUnreadableReportAtHTTPLevel, through the real handler, so the
// 404 the store test asserts is the one a caller actually gets.
func TestReportPredictionsRouteRefusesAnUnreadableReportAtHTTPLevel(t *testing.T) {
	s := newV1Server(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/report/999/predictions", nil)
	req.SetPathValue("id", "999")
	s.apiReportPredictions(rec, req, "someone")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status for an unknown report = %d, want 404", rec.Code)
	}
}

// TestSymbolRouteIsASessionRoute: the reading page's "other predictions" list is a browser read, so
// it must sit under the session class like the per-report route, not under the machine surface.
func TestSymbolRouteIsASessionRoute(t *testing.T) {
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	for _, row := range s.routeTable {
		if row.pattern == "GET /api/predictions" {
			if row.class != authSession {
				t.Errorf("class = %v, want authSession", row.class)
			}
			return
		}
	}
	t.Fatal("GET /api/predictions is not registered")
}

func TestSymbolPredictionsSessionRouteReturnsSets(t *testing.T) {
	s := newV1Server(t)
	seedLedgerFor(t, s, "600160", "2026-10-17", "# v1")
	seedLedgerFor(t, s, "600160", "2026-10-16", "# v2")

	rec := httptest.NewRecorder()
	s.apiQueryPredictions(rec, httptest.NewRequest("GET", "/api/predictions?symbol=600160&limit=20", nil), "someone")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sets []PredictionSet `json:"sets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Sets) != 2 {
		t.Fatalf("sets = %d, want 2", len(out.Sets))
	}
}

// TestPayloadWithheldFromScopedReader: the raw producer JSON is a diagnostic for an unrestricted
// caller. Both read routes go through serveReportPredictions/serveQueryPredictions, so pinning the
// shared implementation covers the session and v1 credential alike.
func TestPayloadWithheldFromScopedReader(t *testing.T) {
	s := newV1Server(t)
	reportID, _ := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")

	read := func(sc *ownerScope) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/report/x/predictions?include=payload", nil)
		req.SetPathValue("id", strconv.FormatInt(reportID, 10))
		rec := httptest.NewRecorder()
		s.serveReportPredictions(rec, req, sc)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Sets []map[string]any `json:"sets"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Sets) != 1 {
			t.Fatalf("sets = %d, want 1", len(out.Sets))
		}
		return out.Sets[0]
	}

	// Unrestricted: include=payload is honoured.
	if _, ok := read(nil)["payload"]; !ok {
		t.Error("an unrestricted caller did not get the payload")
	}
	// Scoped, but able to read this report (the default version is open to everyone granted it).
	sc := &ownerScope{self: userPrincipal("scoped"), versAll: []string{"default"}}
	if _, ok := read(sc)["payload"]; ok {
		t.Error("a scoped caller got the raw payload")
	}
}

// TestReadPhaseNeverClaimsRetryingWithoutEvidence: a pending or deferred mark is "waiting on or
// after <date>", never "fetch failed". Retrying needs a recorded due session, which is the evidence
// that an attempt actually happened.
func TestReadPhaseNeverClaimsRetryingWithoutEvidence(t *testing.T) {
	s := newV1Server(t)
	_, setID := seedLedgerFor(t, s, "600160", "2026-10-17", "# body")
	// One set made at 21:00 CST on 2026-10-18: S0 is Monday 2026-10-19, so d5 falls on 2026-10-26.
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at='2026-10-18T13:00:00Z',
		made_on='2026-10-18', s0_date='2026-10-19', base_check='match' WHERE id=?`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE prediction_marks SET status='refused', refusal='no_bar',
		session_date='', next_attempt_at='' WHERE set_id=? AND checkpoint IN ('d20','d60')`, setID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE prediction_marks SET status='refused', refusal='no_bar',
		session_date='2026-11-16' WHERE set_id=? AND checkpoint='d20'`, setID); err != nil {
		t.Fatal(err)
	}

	phases := func(now time.Time) map[string]PredictionMark {
		t.Helper()
		rows, err := s.st.QueryPredictionSets(PredictionFilter{Symbol: "600160", Limit: 10}, nil)
		if err != nil {
			t.Fatal(err)
		}
		var found *PredictionSet
		for i := range rows {
			if rows[i].ID == setID {
				found = &rows[i]
			}
		}
		if found == nil {
			t.Fatal("set not found")
		}
		annotateReadPhases(rows, now)
		out := map[string]PredictionMark{}
		for _, m := range found.Marks {
			out[m.Checkpoint] = m
		}
		return out
	}

	at := phases(shanghai(2026, time.October, 20, 16, 0))
	if m := at["d5"]; m.Phase != "waiting" || m.NotBefore != "2026-10-26" {
		t.Errorf("d5 = %s/%s, want waiting/2026-10-26", m.Phase, m.NotBefore)
	}
	if m := at["d20"]; m.Phase != "waiting" || m.NotBefore == "" {
		t.Errorf("d20 = %s/%s, want waiting with a date", m.Phase, m.NotBefore)
	}

	// After the due date, a deferred mark with no recorded session is still waiting; one with a
	// recorded session is retrying.
	late := phases(shanghai(2026, time.December, 1, 16, 0))
	if m := late["d5"]; m.Phase != "waiting" {
		t.Errorf("d5 after its due date = %q, want waiting (no recorded attempt)", m.Phase)
	}
	if m := late["d20"]; m.Phase != "retrying" {
		t.Errorf("d20 = %q, want retrying (a due session was recorded)", m.Phase)
	}

	// Before S0 has completed, the panel says so rather than inventing a checkpoint date.
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at=?,
		made_on=?, s0_date='' WHERE id=?`, "2026-12-01T01:00:00Z", "2026-12-01", setID); err != nil {
		t.Fatal(err)
	}
	early := phases(shanghai(2026, time.December, 1, 10, 0))
	if m := early["d5"]; m.Phase != "waiting_s0" || m.NotBefore != "2026-12-01" {
		t.Errorf("before S0: d5 = %s/%s, want waiting_s0/2026-12-01", m.Phase, m.NotBefore)
	}
}
