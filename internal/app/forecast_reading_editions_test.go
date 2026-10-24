package app

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/config"
)

func TestEditionWithoutASetLinksItsReadableSibling(t *testing.T) {
	eachReaderServer(t, assertReadingEditions)
}

// TestSiblingEditionMemberReadsCountedOnEdition pins the C17 membership display: when the group member
// is a sibling edition's set, an internal all reader or an administrator reads "counted on edition X"
// instead of a duplicate, and nobody else learns that the sibling exists through the count.
func TestSiblingEditionMemberReadsCountedOnEdition(t *testing.T) {
	eachReaderServer(t, func(t *testing.T, s *Server) {
		fx := seedEditionFixture(t, s)
		grantSiblingEdition(t, s)
		assertSiblingEditionMemberReadsCountedOnEdition(t, s, fx.current, fx.sibling, fx.member, fx.g)
	})
}

// eachReaderServer runs a reading scenario on SQLite and, when configured, on Postgres. Both servers
// carry the persistent query token "tok-all", as newV1Server does.
func eachReaderServer(t *testing.T, run func(t *testing.T, s *Server)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { run(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "prediction_marks", "prediction_claims", "prediction_sets", "report_viewers", "reports", "users")
		if err := st.CreateToken("tok-all", "test", "all", ""); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = st.exec(`DELETE FROM api_tokens WHERE token_hash=?`, tokenDigest("tok-all")) })
		s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
		s.names.fetch = func(string) string { return "" }
		run(t, s)
	})
}

type editionFixture struct {
	current, sibling, member int64
	g                        gsSeed
}

// seedEditionFixture builds one analysis in two editions: the internal edition holds the group
// member, the client edition (the one read) holds no set. Only the client edition is granted to the
// external readers, so the sibling stays unreadable to them until grantSiblingEdition.
func seedEditionFixture(t *testing.T, s *Server) editionFixture {
	t.Helper()
	seedForecastReader(t, s, "edition-all", scopeAll, false)
	seedForecastReader(t, s, "edition-own", scopeOwn, false)
	seedForecastReader(t, s, "edition-external", scopeAll, true)
	seedForecastReader(t, s, "edition-external-own", scopeOwn, true)
	mustAddUser(t, s, "edition-admin", true)
	if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"edition-a", "edition-b"} {
		if err := s.st.SaveVersion(ReportVersion{Name: version, Visibility: VisibilityAll}); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, version := range []string{"edition-a", "edition-b"} {
			if err := s.st.DeleteVersion(version); err != nil {
				t.Error(err)
			}
		}
		if err := s.st.DeleteSetting(setForecastReader); err != nil {
			t.Error(err)
		}
	})
	g := gsSeed{Symbol: "600001", MadeOn: "2026-09-17", MadeAt: "2026-09-17T10:00:00+08:00", S0Fen: ip(10000), Bench: defaultForecastBench}
	sibling, member := seedReadableMembershipReport(t, s, g, "Internal title")
	if _, err := s.st.exec(`UPDATE reports SET version=? WHERE id=?`, "edition-a", sibling); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET version=? WHERE id=?`, "edition-a", member); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.st.UpsertReport(Rep{Symbol: g.Symbol, Date: g.MadeOn, RType: predTestRtype, Version: "edition-b", Title: "Entirely different client title", MD: "# client body"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants("edition-b", []string{userPrincipal("edition-external"), userPrincipal("edition-external-own")}); err != nil {
		t.Fatal(err)
	}
	return editionFixture{current: current, sibling: sibling, member: member, g: g}
}

func grantSiblingEdition(t *testing.T, s *Server) {
	t.Helper()
	if err := s.st.SetVersionGrants("edition-a", []string{userPrincipal("edition-external"), userPrincipal("edition-external-own")}); err != nil {
		t.Fatal(err)
	}
}

func assertReadingEditions(t *testing.T, s *Server) {
	t.Helper()
	fx := seedEditionFixture(t, s)
	current, sibling, member := fx.current, fx.sibling, fx.member
	for _, route := range []string{"stock", "run"} {
		out := readingSummaryResponse(t, s, "edition-external", route, current, false)
		if _, exists := out["prediction"]; exists {
			t.Fatalf("unreadable sibling disclosed a summary: %v", out["prediction"])
		}
	}
	grantSiblingEdition(t, s)
	for _, user := range []string{"edition-all", "edition-own", "edition-external", "edition-external-own", "edition-admin"} {
		for _, route := range []string{"stock", "run"} {
			out := readingSummaryResponse(t, s, user, route, current, false)
			got, ok := out["prediction"].(map[string]any)
			if !ok || got["has_prediction"] != false {
				t.Fatalf("%s %s borrowed the other edition's set: %v", user, route, out["prediction"])
			}
			encoded, err := json.Marshal(got)
			var wire forecastReadingSummary
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &wire); err != nil || wire.ForecastReadingSetSummary != nil || wire.Sibling == nil {
				t.Fatalf("empty-edition envelope cannot round trip: %+v %v", wire, err)
			}
			for _, field := range []string{"display_set_id", "stance", "body_changed", "membership", "counted_set_id"} {
				if _, exists := got[field]; exists {
					t.Fatalf("edition without a set disclosed %s: %v", field, got)
				}
			}
			link := got["sibling"].(map[string]any)
			assertEditionLink(t, link, sibling, member, "edition-a")
			_, counted := got["counted_edition"]
			if counted != (user == "edition-all" || user == "edition-admin") {
				t.Fatalf("%s sibling membership disclosure=%v", user, got)
			}
		}
	}
	rep, err := s.st.GetNew(current, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"edition-own", "edition-external", "edition-external-own"} {
		view := forecastPredictionViewFor(s.forecastAccess(user), s.viewerScope(user))
		_, counted, err := s.forecastReadingEditionLinks(*rep, view, true, "")
		if err != nil || counted != nil {
			t.Fatalf("%s helper widened a membership request: %+v %v", user, counted, err)
		}
	}
	for _, stage := range []string{"card", "full"} {
		if err := s.st.SetSetting(setForecastReader, stage); err != nil {
			t.Fatal(err)
		}
		for _, user := range []string{"edition-all", "edition-own", "edition-external", "edition-external-own", "edition-admin"} {
			got := editionCardResponse(t, s, user, current)
			if len(got["sets"].([]any)) != 0 {
				t.Fatal("empty edition borrowed a sibling's sets")
			}
			link, ok := got["sibling"].(map[string]any)
			if !ok {
				t.Fatalf("%s %s card has no readable sibling: %v", stage, user, got)
			}
			assertEditionLink(t, link, sibling, member, "edition-a")
			_, counted := got["counted_edition"]
			if counted != (user == "edition-all" || user == "edition-admin") {
				t.Fatalf("%s card disclosed sibling membership: %v", user, got)
			}
		}
	}
	s.cfg = &config.Config{SecretKey: "0123456789abcdef0123456789abcdef"}
	w := httptest.NewRecorder()
	path := "/api/v1/reports/" + itoa64(current) + "/predictions"
	r := sessionForPath(t, s, "edition-all", "GET /api/v1/reports/{id}/predictions", path)
	r.SetPathValue("id", itoa64(current))
	s.v1ReportPredictions(w, r)
	var cookie map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cookie); err != nil || w.Code != 200 || cookie["sibling"] == nil || cookie["counted_edition"] == nil {
		t.Fatalf("v1 cookie did not retain session edition metadata: %d %s %v", w.Code, w.Body.String(), err)
	}
	token := "edition-machine-" + itoa64(current)
	if err := s.st.CreateToken(token, "Edition fixture", "query", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.st.exec(`DELETE FROM api_tokens WHERE token_hash=?`, tokenDigest(token)); err != nil {
			t.Error(err)
		}
	})
	assertEditionBearerHasNoReadingMetadata(t, s, token, current)
	if s.st.driver == "sqlite" {
		s.appTok = newAppTokens(time.Hour)
		assertEditionBridgeIsRefused(t, s, mintForecastBridge(t, s, "edition-all"), current)
	}
}

func assertEditionBearerHasNoReadingMetadata(t *testing.T, s *Server, token string, report int64) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/reports/x/predictions", nil)
	r.SetPathValue("id", itoa64(report))
	r.Header.Set("Authorization", "Bearer "+token)
	s.v1ReportPredictions(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("bearer response %d %s: %v", w.Code, w.Body.String(), err)
	}
	for _, field := range []string{"sibling", "counted_edition"} {
		if _, exists := out[field]; exists {
			t.Fatalf("bearer contract acquired reading metadata: %s", w.Body.String())
		}
	}
}

// An app-bridge token is refused on the v1 prediction read before any record is loaded, so it can
// never carry edition metadata either.
func assertEditionBridgeIsRefused(t *testing.T, s *Server, token string, report int64) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/reports/x/predictions", nil)
	r.SetPathValue("id", itoa64(report))
	r.Header.Set("Authorization", "Bearer "+token)
	s.v1ReportPredictions(w, r)
	if w.Code != 403 || forecastMatrixErrorCode(t, w.Body.Bytes()) != "forecast_unavailable" {
		t.Fatalf("bridge response %d %s; want 403 forecast_unavailable", w.Code, w.Body.String())
	}
}

func editionCardResponse(t *testing.T, s *Server, user string, report int64) map[string]any {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/report/x/predictions", nil)
	r.SetPathValue("id", itoa64(report))
	w := httptest.NewRecorder()
	s.apiReportPredictions(w, r, user)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
		t.Fatalf("card %s: %d %s: %v", user, w.Code, w.Body.String(), err)
	}
	return out
}

func assertEditionLink(t *testing.T, link map[string]any, report, set int64, version string) {
	t.Helper()
	if link["report_id"] != float64(report) || link["set_id"] != float64(set) || link["version"] != version {
		t.Fatalf("wrong edition link: %v", link)
	}
	u, err := url.Parse(link["url"].(string))
	if err != nil || u.Path != "/stock/600001" || u.Fragment != "forecast" || u.Query().Get("r") != itoa64(report) || u.Query().Get("date") != "2026-09-17" || u.Query().Get("kind") == "" {
		t.Fatalf("edition link is not an exact report deep link: %v %v", link, err)
	}
}

func assertSiblingEditionMemberReadsCountedOnEdition(t *testing.T, s *Server, current, sibling, member int64, g gsSeed) {
	t.Helper()
	g.MadeAt = "2026-09-17T11:00:00+08:00"
	duplicate := seedGroupSet(t, s, g)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,version=?,body_sha256=? WHERE id=?`, current, "edition-b", sha256Hex([]byte("# client body\x00")), duplicate); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"edition-all", "edition-own", "edition-external", "edition-external-own", "edition-admin"} {
		for _, route := range []string{"stock", "run"} {
			got := readingSummaryResponse(t, s, user, route, current, false)["prediction"].(map[string]any)
			if got["has_prediction"] != true || got["display_set_id"] != float64(duplicate) {
				t.Fatalf("current edition did not keep its own display set: %v", got)
			}
			if _, exists := got["sibling"]; exists {
				t.Fatalf("edition with a set got an empty-edition fallback: %v", got)
			}
			counted, exists := got["counted_edition"]
			internalAll := user == "edition-all" || user == "edition-admin"
			if exists != internalAll {
				t.Fatalf("%s counted-edition disclosure=%v", user, got)
			}
			if internalAll {
				assertEditionLink(t, counted.(map[string]any), sibling, member, "edition-a")
				if _, exists := got["membership"]; exists {
					t.Fatalf("sibling member was still described as duplicate: %v", got)
				}
				if got["counted_set_id"] != float64(0) {
					t.Fatalf("sibling membership changed the report's counted identity: %v", got)
				}
			}
			card := editionCardResponse(t, s, user, current)
			_, cardCounted := card["counted_edition"]
			if cardCounted != internalAll {
				t.Fatalf("%s card membership disclosure=%v", user, card)
			}
			if internalAll {
				assertEditionLink(t, card["counted_edition"].(map[string]any), sibling, member, "edition-a")
			}
		}
	}
	// A later report in the counted edition must not hide its older statistical member.
	g.MadeAt = "2026-09-17T12:00:00+08:00"
	laterReport, laterSet := seedReadableMembershipReport(t, s, g, "Later output in the internal edition")
	if _, err := s.st.exec(`UPDATE reports SET version=? WHERE id=?`, "edition-a", laterReport); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE prediction_sets SET version=? WHERE id=?`, "edition-a", laterSet); err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"stock", "run"} {
		got := readingSummaryResponse(t, s, "edition-all", route, current, false)["prediction"].(map[string]any)
		assertEditionLink(t, got["counted_edition"].(map[string]any), sibling, member, "edition-a")
		encoded, err := json.Marshal(got)
		var wire forecastReadingSummary
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, &wire); err != nil || wire.ForecastReadingSetSummary == nil || !wire.HasPrediction || wire.DisplaySetID != duplicate {
			t.Fatalf("populated-edition envelope cannot round trip: %+v %v", wire, err)
		}
	}
	// A September member cannot explain an October receipt's membership.
	future := g
	future.MadeOn, future.MadeAt = "2026-10-02", "2026-10-02T10:00:00+08:00"
	futureSet := seedGroupSet(t, s, future)
	if _, err := s.st.exec(`UPDATE prediction_sets SET report_id=?,version=?,payload_sha256=?,body_sha256=? WHERE id=?`, current, "edition-b", "future-cohort", "another-body", futureSet); err != nil {
		t.Fatal(err)
	}
	// A member from a later cohort on this report must not hide the displayed cohort's sibling.
	for _, route := range []string{"stock", "run"} {
		got := readingSummaryResponse(t, s, "edition-all", route, current, false)["prediction"].(map[string]any)
		if got["display_set_id"] != float64(duplicate) || got["counted_set_id"] != float64(futureSet) {
			t.Fatalf("cross-cohort fixture lost separate identities: %v", got)
		}
		link, ok := got["counted_edition"].(map[string]any)
		if !ok {
			t.Fatalf("future cohort hid the displayed cohort's counted edition: %v", got)
		}
		assertEditionLink(t, link, sibling, member, "edition-a")
	}
	card := editionCardResponse(t, s, "edition-all", current)
	link, ok := card["counted_edition"].(map[string]any)
	if !ok {
		t.Fatalf("card lost the displayed cohort's sibling: %v", card)
	}
	assertEditionLink(t, link, sibling, member, "edition-a")
	if _, err := s.st.exec(`UPDATE prediction_sets SET made_at=?,made_on=?,cohort=?,decision_quality=? WHERE id=?`, "2026-10-01T10:00:00+08:00", "2026-10-01", "2026-10", "degraded", duplicate); err != nil {
		t.Fatal(err)
	}
	got := readingSummaryResponse(t, s, "edition-all", "stock", current, false)["prediction"].(map[string]any)
	if _, exists := got["counted_edition"]; exists {
		t.Fatalf("sibling membership crossed receipt cohorts: %v", got)
	}
	if got["membership"] != membershipDegraded {
		t.Fatalf("cross-cohort sibling hid the actual status: %v", got)
	}
}

func TestReadingEditionLinksDoNotCrossAnalysisKeys(t *testing.T) {
	for _, different := range []string{"symbol", "date", "rtype"} {
		t.Run(different, func(t *testing.T) {
			s := newV1Server(t)
			seedForecastReader(t, s, "edition-all", scopeAll, false)
			if err := s.st.SetSetting(setForecastReader, "full"); err != nil {
				t.Fatal(err)
			}
			current, _, err := s.st.UpsertReport(Rep{Symbol: "600001", Date: "2026-09-17", RType: predTestRtype, Version: "current", Title: "No forecast", MD: "body"})
			if err != nil {
				t.Fatal(err)
			}
			foreign, _ := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2026-09-17"}, "Not the same analysis")
			column, value := "symbol", "600002"
			if different == "date" {
				column, value = "rdate", "2026-09-18"
			}
			if different == "rtype" {
				column, value = "rtype", "uncovered-fixture"
			}
			if _, err := s.st.exec("UPDATE reports SET "+column+"=? WHERE id=?", value, foreign); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"stock", "run"} {
				if out := readingSummaryResponse(t, s, "edition-all", route, current, false); out["prediction"] != nil {
					t.Fatalf("%s crossed %s: %v", route, different, out["prediction"])
				}
			}
		})
	}
}
