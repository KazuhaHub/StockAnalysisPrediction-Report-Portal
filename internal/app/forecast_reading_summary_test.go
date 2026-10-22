package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStockAndRunPayloadCarryPredictionSummary(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertReadingSummary(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "users")
		s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
		s.names.fetch = func(string) string { return "" }
		assertReadingSummary(t, s)
	})
}

func assertReadingSummary(t *testing.T, s *Server) {
	t.Helper()
	s.forecastEval.now = func() time.Time { return dateAtClose("2026-10-22") }
	for _, scope := range []string{scopeAll, scopeOwn, scopeNone} {
		seedForecastReader(t, s, "summary-"+scope, scope, false)
	}
	seedForecastReader(t, s, "summary-external", scopeAll, true)
	mustAddUser(t, s, "summary-admin", true)
	report, set := seedReadableMembershipReport(t, s, gsSeed{
		Symbol: "600001", MadeOn: "2026-09-17", MadeAt: "2026-09-17T16:00:00+08:00",
		Bench: defaultForecastBench, S0Fen: ip(10000), TerminalFen: ip(12000), HorizonStd: true,
		Marks: map[string]gsMark{"d5": {Status: markResolved}, "d20": {Status: markPending}},
	}, "Reading summary")
	if _, err := s.st.exec(`UPDATE prediction_sets SET s0_date=? WHERE id=?`, "2026-09-18", set); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE prediction_claims SET weight_bp=? WHERE set_id=? AND kind=?`, 6500, set, kindStance); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveVersion(ReportVersion{Name: "summary-fixture", Visibility: VisibilityGroup}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("summary-fixture", []string{userPrincipal("summary-external")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE reports SET version=? WHERE id=?`, "summary-fixture", report); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetReportViewers(report, "2026-09-17", []string{userPrincipal("summary-external")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteVersion("summary-fixture"); err != nil {
			t.Error(err)
		}
		if err := s.st.DeleteSetting(setForecastReader); err != nil {
			t.Error(err)
		}
	})
	for _, stage := range []string{"off", "card", "full", "unknown"} {
		if err := s.st.SetSetting(setForecastReader, stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"summary-all", "summary-own", "summary-none", "summary-external", "summary-admin"} {
			for _, route := range []string{"stock", "run"} {
				for _, prefetch := range []bool{false, true} {
					out := readingSummaryResponse(t, s, user, route, report, prefetch)
					raw, exists := out["prediction"]
					want := !prefetch && (user == "summary-admin" || stage == "full" && user != "summary-none")
					if exists != want {
						t.Fatalf("stage=%s user=%s route=%s prefetch=%v summary=%v, want %v", stage, user, route, prefetch, exists, want)
					}
					if !want {
						continue
					}
					got := raw.(map[string]any)
					if got["display_set_id"] != float64(set) || got["stance"] != "long" || got["weight_bp"] != float64(6500) || got["upside_bp"] != float64(2000) || got["body_changed"] != false {
						t.Fatalf("summary lost selected prediction: %v", got)
					}
					checkpoint := got["next_checkpoint"].(map[string]any)
					if checkpoint["checkpoint"] != "d20" || checkpoint["phase"] != predPhaseSettling || checkpoint["not_before"] != "2026-10-26" || checkpoint["expected"] != true {
						t.Fatalf("next checkpoint lost conservative phase/date: %v", checkpoint)
					}
					internalAll := user == "summary-all" || user == "summary-admin"
					_, hasMember := got["membership"]
					_, hasCounted := got["counted_set_id"]
					if hasMember != internalAll || hasCounted != internalAll {
						t.Fatalf("%s membership disclosure=%v", user, got)
					}
					if internalAll && (got["membership"] != membershipMember || got["counted_set_id"] != float64(set)) {
						t.Fatalf("wrong member: %v", got)
					}
					for _, forbidden := range []string{"payload", "errors", "claims", "marks", "attributed_ou"} {
						if _, ok := got[forbidden]; ok {
							t.Fatalf("summary disclosed %s", forbidden)
						}
					}
				}
			}
		}
	}
}

func readingSummaryResponse(t *testing.T, s *Server, user, route string, report int64, prefetch bool) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/"+route+"/fixture?r="+itoa64(report), nil)
	if prefetch {
		r.Header.Set("X-Prefetch", "1")
	}
	w := httptest.NewRecorder()
	if route == "stock" {
		r.SetPathValue("symbol", "600001")
		s.apiStock(w, r, user)
	} else {
		r.SetPathValue("key", itoa64(report))
		s.apiRun(w, r, user)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("%s status=%d body=%s: %v", route, w.Code, w.Body.String(), err)
	}
	if out["rep"].(map[string]any)["id"] != float64(report) {
		t.Fatal("summary request changed selected report")
	}
	return out
}

func TestReadingSummarySelectionAndTerminalCheckpoints(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "summary-reader", scopeAll, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	g := gsSeed{Symbol: "600001", MadeOn: "2026-09-17", S0Fen: ip(10000), TerminalFen: ip(12000)}
	report, original := seedReadableMembershipReport(t, s, g, "Summary selection")
	newer := seedGroupSet(t, s, g)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,body_sha256=?,payload_sha256=? WHERE id=?`, report, "different-body", "newer-summary-revision", newer); err != nil {
		t.Fatal(err)
	}
	get := func() map[string]any {
		t.Helper()
		return readingSummaryResponse(t, s, "summary-reader", "stock", report, false)["prediction"].(map[string]any)
	}
	if got := get(); got["display_set_id"] != float64(original) || got["body_changed"] != false {
		t.Fatalf("latest revision displaced a body match: %v", got)
	}
	if _, err := s.st.exec(`UPDATE reports SET body_md=? WHERE id=?`, "changed body", report); err != nil {
		t.Fatal(err)
	}
	if got := get(); got["display_set_id"] != float64(newer) || got["body_changed"] != true {
		t.Fatalf("missing fallback warning: %v", got)
	}
	for _, cp := range predCheckpoints {
		if _, err := s.st.exec(`INSERT INTO prediction_marks(set_id,checkpoint,status) VALUES(?,?,?)`, newer, cp, markResolved); err != nil {
			t.Fatal(err)
		}
	}
	if got := get(); got["next_checkpoint"] != nil {
		t.Fatalf("resolved set has a next checkpoint: %v", got)
	}
	if _, err := s.st.exec(`UPDATE prediction_marks SET status=? WHERE set_id=? AND checkpoint=?`, markRefusedFinal, newer, "d20"); err != nil {
		t.Fatal(err)
	}
	if got := get(); got["next_checkpoint"] != nil {
		t.Fatalf("final refusal became outstanding: %v", got)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET state=? WHERE id=?`, predStateRejected, newer); err != nil {
		t.Fatal(err)
	}
	if got := get(); got["display_set_id"] != float64(original) {
		t.Fatalf("rejected revision became the chip: %v", got)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET state=? WHERE id=?`, predStateRejected, original); err != nil {
		t.Fatal(err)
	}
	if out := readingSummaryResponse(t, s, "summary-reader", "stock", report, false); out["prediction"] != nil {
		t.Fatalf("report without accepted sets got a summary: %v", out["prediction"])
	}
}

func TestReadingSummaryLedgerFailureNeverHidesTheBody(t *testing.T) {
	for _, table := range []string{"prediction_sets", "prediction_claims", "prediction_marks"} {
		t.Run(table, func(t *testing.T) {
			s := newV1Server(t)
			seedForecastReader(t, s, "summary-reader", scopeAll, false)
			if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
				t.Fatal(err)
			}
			report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}, "Ledger failure")
			if _, err := s.st.exec("DROP TABLE " + table); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"stock", "run"} {
				out := readingSummaryResponse(t, s, "summary-reader", route, report, false)
				if _, exists := out["prediction"]; exists {
					t.Fatalf("failed %s read published a partial summary", table)
				}
			}
		})
	}
}

func TestPrefetchOmitsTheSummaryButKeepsTheTimeline(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "summary-reader", scopeAll, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}, "Prefetch")
	real := readingSummaryResponse(t, s, "summary-reader", "stock", report, false)
	prefetch := readingSummaryResponse(t, s, "summary-reader", "stock", report, true)
	if real["prediction"] == nil {
		t.Fatal("positive real request has no summary")
	}
	if _, exists := prefetch["prediction"]; exists {
		t.Fatal("prefetch loaded the heavy summary")
	}
	for _, out := range []map[string]any{real, prefetch} {
		if out["timeline"].([]any)[0].(map[string]any)["predicted"] != true {
			t.Fatal("prefetch removed the scoped timeline flag")
		}
	}
}

func TestReadingSummaryWithUnknownTimingMakesNoDatePromise(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "summary-own", scopeOwn, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	report, set := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}, "Unknown timing")
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at=?,made_on=? WHERE id=?`, "invalid", "invalid", set); err != nil {
		t.Fatal(err)
	}
	out := readingSummaryResponse(t, s, "summary-own", "stock", report, false)["prediction"].(map[string]any)
	next := out["next_checkpoint"].(map[string]any)
	if next["checkpoint"] != "d5" || next["expected"] != false {
		t.Fatalf("lost unknown obligation: %v", next)
	}
	for _, field := range []string{"phase", "not_before"} {
		if _, exists := next[field]; exists {
			t.Fatalf("unknown timing fabricated %s: %v", field, next)
		}
	}
}

func TestReadingSummaryRechecksReportVisibility(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "summary-external", scopeAll, true)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	id, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}, "Not granted")
	report, err := s.st.GetNew(id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.canReadReport(id, s.viewerScope("summary-external")) {
		t.Fatal("negative fixture grants the report")
	}
	if got := s.forecastReadingSummary(httptest.NewRequest("GET", "/api/stock/600001", nil), "summary-external", report); got != nil {
		t.Fatalf("summary trusted an unscoped report object: %+v", got)
	}
}

func TestReadingSummaryNonstandardHorizonHasNoNextH12(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { assertReadingSummaryH12(t, driver, false) })
	}
}

func TestReadingSummaryStandardHorizonKeepsPendingH12(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { assertReadingSummaryH12(t, driver, true) })
	}
}

func assertReadingSummaryH12(t *testing.T, driver string, standard bool) {
	t.Helper()
	for _, storedH12 := range []bool{false, true} {
		t.Run(fmt.Sprint("stored-h12-", storedH12), func(t *testing.T) {
			s := readingSummaryBoundaryServer(t, driver)
			seedForecastReader(t, s, "summary-own", scopeOwn, false)
			if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
				t.Fatal(err)
			}
			marks := map[string]gsMark{}
			for _, checkpoint := range []string{"d5", "d20", "d60", "d120"} {
				marks[checkpoint] = gsMark{Status: markResolved}
			}
			if storedH12 {
				marks["h12"] = gsMark{Status: markPending}
			}
			report, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17", HorizonStd: standard, Marks: marks}, "Horizon applicability")
			for _, route := range []string{"stock", "run"} {
				summary := readingSummaryResponse(t, s, "summary-own", route, report, false)["prediction"].(map[string]any)
				next, exists := summary["next_checkpoint"]
				if standard {
					if !exists || next.(map[string]any)["checkpoint"] != "h12" {
						t.Fatalf("%s lost a standard-horizon obligation: %v", route, summary)
					}
				} else if exists {
					t.Fatalf("%s invented an inapplicable checkpoint: %v", route, summary)
				}
			}
		})
	}
}

func TestReadingSummaryTerminalMarksSurviveUnknownTiming(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) { assertReadingSummaryUnknownTiming(t, readingSummaryBoundaryServer(t, driver)) })
	}
}

func readingSummaryBoundaryServer(t *testing.T, driver string) *Server {
	t.Helper()
	if driver == "sqlite" {
		return newV1Server(t)
	}
	st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "users")
	s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
	s.names.fetch = func(string) string { return "" }
	t.Cleanup(func() {
		if err := st.DeleteSetting(setForecastReader); err != nil {
			t.Error(err)
		}
	})
	return s
}

func assertReadingSummaryUnknownTiming(t *testing.T, s *Server) {
	t.Helper()
	seedForecastReader(t, s, "summary-own", scopeOwn, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	marks := map[string]gsMark{}
	for _, checkpoint := range predCheckpoints {
		marks[checkpoint] = gsMark{Status: markResolved}
	}
	report, set := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17", HorizonStd: true, Marks: marks}, "Terminal despite unknown timing")
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at=?,made_on=? WHERE id=?`, "invalid", "invalid", set); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{markResolved, markRefusedFinal} {
		if _, err := s.st.exec(`UPDATE prediction_marks SET status=? WHERE set_id=?`, status, set); err != nil {
			t.Fatal(err)
		}
		for _, route := range []string{"stock", "run"} {
			summary := readingSummaryResponse(t, s, "summary-own", route, report, false)["prediction"].(map[string]any)
			if _, exists := summary["next_checkpoint"]; exists {
				t.Fatalf("%s reactivated %s marks: %v", route, status, summary)
			}
		}
	}
}

func TestReadingSummaryMatchesTheBodyAlreadySelectedForTheResponse(t *testing.T) {
	s := newV1Server(t)
	seedForecastReader(t, s, "summary-reader", scopeAll, false)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	g := gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}
	id, original := seedReadableMembershipReport(t, s, g, "Selected response body")
	report, err := s.st.GetNew(id, nil)
	if err != nil {
		t.Fatal(err)
	}
	newer := seedGroupSet(t, s, g)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,payload_sha256=?,body_sha256=? WHERE id=?`, id, "future-revision", sha256Hex([]byte("future body\x00")), newer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE reports SET body_md=? WHERE id=?`, "future body", id); err != nil {
		t.Fatal(err)
	}
	summary := s.forecastReadingSummary(httptest.NewRequest("GET", "/api/stock/600001", nil), "summary-reader", report)
	if summary == nil || summary.DisplaySetID != original || summary.BodyChanged {
		t.Fatalf("concurrent ingest paired a future prediction with the selected old body: %+v", summary)
	}
}
