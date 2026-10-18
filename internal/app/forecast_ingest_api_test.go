package app

// The ledger through the HTTP surface (ADR 0029 §3, plan §3.5, design §11 #1–9, #21).
//
// The classifier's own tests pin the rules. These pin the things only the request path can get wrong:
// that a refusal never costs a report, that the write happens exactly once per payload, that the
// ledger rows actually land, and that a store failure is reported rather than raised.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ingestBody builds the report envelope a producer sends, with predictions spliced in as raw JSON.
func ingestBody(extra string) string {
	body := `{"symbol":"600160","date":"2026-10-17","subtype":"` + predTestRtype + `","kind":"投资决策",` +
		`"title":"600160 t","body_md":"# body"}`
	if extra == "" {
		return body
	}
	return strings.TrimSuffix(body, "}") + `,"predictions":` + extra + "}"
}

func ingestPost(t *testing.T, s *Server, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/reports", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok-all")
	rec := httptest.NewRecorder()
	s.v1Ingest(rec, req)
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON (%d): %q", rec.Code, rec.Body.String())
	}
	return rec.Code, m
}

func predRowCount(t *testing.T, st *Store, table string) int {
	t.Helper()
	var n int
	if err := st.queryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func lastSet(t *testing.T, st *Store) predictionSetRow {
	t.Helper()
	var r predictionSetRow
	row := st.queryRow(`SELECT id, report_id, state, symbol, rtype, made_on, cohort, zone,
			horizon_end, horizon_std, bench_code, payload_sha256, errors
		FROM prediction_sets ORDER BY id DESC LIMIT 1`)
	var reportID sql.NullInt64
	if err := row.Scan(&r.ID, &reportID, &r.State, &r.Symbol, &r.RType, &r.MadeOn, &r.Cohort, &r.Zone,
		&r.HorizonEnd, &r.HorizonStd, &r.BenchCode, &r.PayloadSHA, &r.Errors); err != nil {
		t.Fatalf("read set: %v", err)
	}
	if reportID.Valid {
		r.ReportID = reportID.Int64
	}
	return r
}

// predictionSetRow is the slice of the table these tests read back.
type predictionSetRow struct {
	ID         int64
	ReportID   int64
	State      string
	Symbol     string
	RType      string
	MadeOn     string
	Cohort     string
	Zone       string
	HorizonEnd string
	HorizonStd int
	BenchCode  string
	PayloadSHA string
	Errors     string
}

// TestV1IngestStoresAnAcceptedSet is the end-to-end happy path: a covered report with a valid payload
// leaves one accepted set, its claims, and one pending mark per checkpoint.
func TestV1IngestStoresAnAcceptedSet(t *testing.T) {
	s := newV1Server(t)
	code, m := ingestPost(t, s, ingestBody(string(predWire(nil))))

	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, m)
	}
	if m["predictions_status"] != predStateAccepted {
		t.Fatalf("predictions_status = %v, want %q", m["predictions_status"], predStateAccepted)
	}
	if got := predRowCount(t, s.st, "prediction_sets"); got != 1 {
		t.Fatalf("sets = %d, want 1", got)
	}
	if got := predRowCount(t, s.st, "prediction_claims"); got != 6 {
		t.Fatalf("claims = %d, want 6", got)
	}
	// Five checkpoints, because the payload's horizon is the standard twelve months.
	if got := predRowCount(t, s.st, "prediction_marks"); got != len(predCheckpoints) {
		t.Fatalf("marks = %d, want %d", got, len(predCheckpoints))
	}
	for _, cp := range predCheckpoints {
		var status string
		if err := s.st.queryRow("SELECT status FROM prediction_marks WHERE checkpoint=?", cp).Scan(&status); err != nil {
			t.Fatalf("mark %s: %v", cp, err)
		}
		if status != markPending {
			t.Errorf("mark %s status = %q, want %q: a new set has nothing resolved", cp, status, markPending)
		}
	}
	set := lastSet(t, s.st)
	if set.Cohort == "" || set.MadeOn == "" {
		t.Errorf("set has no cohort/made_on: %+v", set)
	}
	if set.Zone != predZone {
		t.Errorf("zone = %q, want %q", set.Zone, predZone)
	}
	if set.BenchCode != defaultForecastBench {
		t.Errorf("bench_code = %q, want the D1 default %q", set.BenchCode, defaultForecastBench)
	}
}

// TestV1IngestRejectionKeepsTheReport is D3, and it is the one rule that must not regress: an
// unusable payload costs the CLAIMS, never the report. A 4xx here would destroy a finished run that
// nothing re-runs, and the producer's only signal would be a batch failure.
func TestV1IngestRejectionKeepsTheReport(t *testing.T) {
	s := newV1Server(t)
	bad := predWire(func(m map[string]any) {
		claim(m, "scenario.bull", func(c map[string]any) { c["probability"] = "1.0001" })
	})
	code, m := ingestPost(t, s, ingestBody(string(bad)))

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a bad payload must not fail the ingest", code)
	}
	if m["predictions_status"] != predStateRejected {
		t.Fatalf("predictions_status = %v, want %q", m["predictions_status"], predStateRejected)
	}
	if m["ok"] != true {
		t.Error("ok is not true")
	}
	// The report exists and is readable.
	var reports int
	if err := s.st.queryRow("SELECT COUNT(*) FROM reports").Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Fatalf("reports = %d, want 1: the report must survive a refused payload", reports)
	}
	// The rejection is on the ledger with its reasons, has no claims and no marks.
	set := lastSet(t, s.st)
	if set.State != predStateRejected {
		t.Errorf("state = %q, want %q", set.State, predStateRejected)
	}
	if set.Errors == "" {
		t.Error("the reason for the refusal was not recorded on the set")
	}
	if got := predRowCount(t, s.st, "prediction_claims"); got != 0 {
		t.Errorf("claims = %d, want 0 for a rejected set", got)
	}
	if got := predRowCount(t, s.st, "prediction_marks"); got != 0 {
		t.Errorf("marks = %d, want 0 for a rejected set: nothing about it will ever resolve", got)
	}
	// The response names the failing rule, so the producer's operator does not have to guess.
	errs, ok := m["predictions_errors"].([]any)
	if !ok || len(errs) == 0 {
		t.Fatalf("predictions_errors = %v, want at least one", m["predictions_errors"])
	}
	first, _ := errs[0].(map[string]any)
	if first["reason"] != reasonBadProbability {
		t.Errorf("reason = %v, want %q", first["reason"], reasonBadProbability)
	}
}

// TestV1IngestRejectionIsAuditedAndBroadcast: a rejection is the producer's bug and the report reads
// normally, so the audit row and the event are the only ways it reaches a person.
func TestV1IngestRejectionIsAuditedAndBroadcast(t *testing.T) {
	s := newV1Server(t)
	bad := predWire(func(m map[string]any) { m["schema"] = "predictions.v99" })
	if code, m := ingestPost(t, s, ingestBody(string(bad))); code != http.StatusOK || m["predictions_status"] != predStateRejected {
		t.Fatalf("setup: status %d body %v", code, m)
	}

	var actions []string
	rows, err := s.st.query(`SELECT action FROM audit_log WHERE action IN (?,?)`,
		AuditPredictionsRejected, AuditPredictionsStoreFailed)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, a)
	}
	if len(actions) != 1 || actions[0] != AuditPredictionsRejected {
		t.Fatalf("audit actions = %v, want exactly [%s]", actions, AuditPredictionsRejected)
	}

	// The event is offered to subscribers, so an admin can select it in the webhook UI.
	if !containsStr(availableEvents(), EventPredictionsRejected) {
		t.Error("predictions.rejected is not in availableEvents(); it cannot be subscribed to")
	}
}

// TestV1IngestCoverageRows: a covered report with no payload still leaves a row, an uncovered one
// leaves none. This is what makes coverage computable from the ledger alone (ADR 0029 §9).
func TestV1IngestCoverageRows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rtype   string
		extra   string
		wantSet int
		status  string
	}{
		{"covered, no payload", predTestRtype, "", 1, predStateAbsent},
		{"covered, valid payload", predTestRtype, string(predWire(nil)), 1, predStateAccepted},
		{"uncovered, no payload", "行业分析", "", 0, "none"},
		{"uncovered, payload ignored", "行业分析", string(predWire(nil)), 1, predStateAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newV1Server(t)
			body := ingestBody(tc.extra)
			body = strings.Replace(body, `"subtype":"`+predTestRtype+`"`, `"subtype":"`+tc.rtype+`"`, 1)
			code, m := ingestPost(t, s, body)
			if code != http.StatusOK {
				t.Fatalf("status %d: %v", code, m)
			}
			if m["predictions_status"] != tc.status {
				t.Errorf("predictions_status = %v, want %q", m["predictions_status"], tc.status)
			}
			if got := predRowCount(t, s.st, "prediction_sets"); got != tc.wantSet {
				t.Errorf("sets = %d, want %d", got, tc.wantSet)
			}
		})
	}
}

// TestV1IngestIsIdempotentPerPayload: a retried request must not double the ledger, and a payload that
// genuinely changed must get its own row. Both are properties the producer relies on: it retries on
// timeouts it cannot distinguish from failures, and it re-emits when a number moves.
func TestV1IngestIsIdempotentPerPayload(t *testing.T) {
	s := newV1Server(t)
	payload := string(predWire(nil))

	if code, m := ingestPost(t, s, ingestBody(payload)); code != http.StatusOK {
		t.Fatalf("first: %d %v", code, m)
	}
	// Identical payload, byte for byte.
	if code, m := ingestPost(t, s, ingestBody(payload)); code != http.StatusOK {
		t.Fatalf("retry: %d %v", code, m)
	}
	if got := predRowCount(t, s.st, "prediction_sets"); got != 1 {
		t.Fatalf("sets after an identical retry = %d, want 1", got)
	}

	// A changed number is a revision: same report, a second set, and the earlier one untouched.
	moved := predWire(func(m map[string]any) {
		claim(m, "expected_terminal", func(c map[string]any) { c["price"] = "11.20" })
	})
	first := lastSet(t, s.st)
	if code, m := ingestPost(t, s, ingestBody(string(moved))); code != http.StatusOK {
		t.Fatalf("revised: %d %v", code, m)
	}
	if got := predRowCount(t, s.st, "prediction_sets"); got != 2 {
		t.Fatalf("sets after a changed payload = %d, want 2", got)
	}
	if after := lastSet(t, s.st); after.PayloadSHA == first.PayloadSHA {
		t.Error("the revised set has the same payload hash as the first")
	}
}

// TestV1IngestWithoutPredictionsNeverTouchesTheLedger: an uncovered, payload-less ingest is the case
// that must cost nothing at all — no row, no audit, no event.
func TestV1IngestWithoutPredictionsNeverTouchesTheLedger(t *testing.T) {
	s := newV1Server(t)
	code, m := ingestPost(t, s, ingestBody(""))
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, m)
	}
	// The subtype is covered here, so an `absent` row IS expected; the point is that it is a row and
	// nothing more.
	if m["predictions_status"] != predStateAbsent {
		t.Errorf("predictions_status = %v, want %q", m["predictions_status"], predStateAbsent)
	}
	if got := predRowCount(t, s.st, "prediction_marks"); got != 0 {
		t.Errorf("marks = %d, want 0", got)
	}

	// And an uncovered subtype writes nothing whatsoever.
	s2 := newV1Server(t)
	body := strings.Replace(ingestBody(""), `"subtype":"`+predTestRtype+`"`, `"subtype":"行业分析"`, 1)
	if code, m := ingestPost(t, s2, body); code != http.StatusOK || m["predictions_status"] != "none" {
		t.Fatalf("uncovered: %d %v", code, m)
	}
	if got := predRowCount(t, s2.st, "prediction_sets"); got != 0 {
		t.Errorf("sets = %d, want 0 for an uncovered rtype with no payload", got)
	}
}

// TestV1IngestInvalidPayloadWritesNothingItShouldNot: the classifier runs before RegisterType, so a
// payload that is refused must not have registered a report type on its way in. The check is a diff
// against the same request without predictions.
func TestV1IngestInvalidPayloadWritesNothingItShouldNot(t *testing.T) {
	count := func(s *Server, table string) int {
		var n int
		if err := s.st.queryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A report type that appears nowhere else, so the only thing that could register it is this call.
	s1 := newV1Server(t)
	body := ingestBody(string(json.RawMessage(`{"schema":"nope"}`)))
	body = strings.Replace(body, `"subtype":"`+predTestRtype+`"`, `"subtype":"配股分析"`, 1)
	if code, _ := ingestPost(t, s1, body); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	rejectedTypes, rejectedVersions := count(s1, "type_config"), count(s1, "report_versions")

	s2 := newV1Server(t)
	clean := strings.Replace(ingestBody(""), `"subtype":"`+predTestRtype+`"`, `"subtype":"配股分析"`, 1)
	if code, _ := ingestPost(t, s2, clean); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got := count(s2, "type_config"); got != rejectedTypes {
		t.Errorf("type_config: %d with a rejected payload vs %d without, want equal", rejectedTypes, got)
	}
	if got := count(s2, "report_versions"); got != rejectedVersions {
		t.Errorf("report_versions: %d with a rejected payload vs %d without, want equal", rejectedVersions, got)
	}
}

// TestV1IngestStoreFailureIsReportedNotRaised (design §11 #21): if the set cannot be written twice
// over, the response says so and stays 200. A 5xx would make the producer retry a run whose report
// already exists, and a started run is never re-run.
func TestV1IngestStoreFailureIsReportedNotRaised(t *testing.T) {
	s := newV1Server(t)
	// The set cannot be written because its table is gone. Breaking the schema rather than injecting
	// through a test-only hook keeps the failure real: the code path exercised here is exactly the
	// production one, and there is no seam to keep in sync as the write path changes.
	if _, err := s.st.exec(`DROP TABLE prediction_claims`); err != nil {
		t.Fatal(err)
	}

	code, m := ingestPost(t, s, ingestBody(string(predWire(nil))))
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200: a ledger write failure must not fail the ingest", code)
	}
	if m["predictions_status"] != "store_failed" {
		t.Fatalf("predictions_status = %v, want store_failed", m["predictions_status"])
	}
	if m["ok"] != true {
		t.Error("ok is not true: the report was stored")
	}
	var reports int
	if err := s.st.queryRow("SELECT COUNT(*) FROM reports").Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Errorf("reports = %d, want 1", reports)
	}
	if got := predRowCount(t, s.st, "prediction_sets"); got != 0 {
		t.Errorf("sets = %d, want 0 (the write failed)", got)
	}
}

// TestV1IngestBacktestIsStoredAndMarked: a run over history is recorded and listed apart, and gets no
// marks — nothing about it is a forecast.
func TestV1IngestBacktestIsStoredAndMarked(t *testing.T) {
	s := newV1Server(t)
	raw := predWire(func(m map[string]any) {
		m["information_cutoff"] = "2026-06-01T00:00:00Z"
	})
	code, m := ingestPost(t, s, ingestBody(string(raw)))
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, m)
	}
	if m["predictions_status"] != predStateBacktest {
		t.Fatalf("predictions_status = %v, want %q", m["predictions_status"], predStateBacktest)
	}
	if got := predRowCount(t, s.st, "prediction_sets"); got != 1 {
		t.Errorf("sets = %d, want 1: a backtest is stored, not discarded", got)
	}
	if got := predRowCount(t, s.st, "prediction_marks"); got != 0 {
		t.Errorf("marks = %d, want 0", got)
	}
	// Its claims are kept: a backtest is worth reading, it is just never counted.
	if got := predRowCount(t, s.st, "prediction_claims"); got != 6 {
		t.Errorf("claims = %d, want 6", got)
	}
}

// TestV1IngestSymbolScopeRefusesOnlyThePredictions: a Beijing code is a fine report and an unscorable
// claim set, so the report is stored and the set is refused.
func TestV1IngestSymbolScopeRefusesOnlyThePredictions(t *testing.T) {
	s := newV1Server(t)
	body := strings.Replace(ingestBody(string(predWire(nil))), `"symbol":"600160"`, `"symbol":"830799"`, 1)
	code, m := ingestPost(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("status = %d: a Beijing code must not fail the ingest", code)
	}
	if m["predictions_status"] != predStateRejected {
		t.Fatalf("predictions_status = %v, want %q", m["predictions_status"], predStateRejected)
	}
	errs, _ := m["predictions_errors"].([]any)
	if len(errs) == 0 {
		t.Fatal("no errors reported")
	}
	first, _ := errs[0].(map[string]any)
	if first["reason"] != reasonSymbolScope {
		t.Errorf("reason = %v, want %q", first["reason"], reasonSymbolScope)
	}
	var reports int
	if err := s.st.queryRow("SELECT COUNT(*) FROM reports").Scan(&reports); err != nil {
		t.Fatal(err)
	}
	if reports != 1 {
		t.Errorf("reports = %d, want 1", reports)
	}
}
