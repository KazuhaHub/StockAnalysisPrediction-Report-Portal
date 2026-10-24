package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

const memberPopulationTarget = "/?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5"

func TestMembersAllPopulationExplainsExclusionsWithoutOutcomes(t *testing.T) {
	s := newV1Server(t)
	want := map[int64]string{}
	for i, tc := range []struct {
		status       string
		corp         bool
		base, reason string
	}{
		{markResolved, false, baseMatch, ""},
		{markPending, false, baseMatch, "pending"},
		{markRefused, false, baseMatch, "refused"},
		{markRefusedFinal, false, baseMatch, "refused_final"},
		{markResolved, true, baseMatch, "corp_action"},
		{"", false, baseMatch, "missing_mark"},
		{markPending, false, baseCheckPending, "pending_baseline"},
	} {
		marks := map[string]gsMark{}
		if tc.status != "" {
			marks["d5"] = gsMark{Status: tc.status, CorpAction: tc.corp, Close: ip(11000), RetBp: bp(100), BenchRetBp: bp(10)}
		}
		id := seedGroupSet(t, s, gsSeed{Symbol: ingestSymbol(i), MadeOn: "2025-06-10", BaseCheck: tc.base, S0Fen: ip(10000), TerminalFen: ip(12000), Marks: marks})
		want[id] = tc.reason
	}
	// A rerun is never promoted to an additional member by a display mode.
	seedGroupSet(t, s, gsSeed{Symbol: ingestSymbol(0), MadeOn: "2025-06-11"})
	w := httptest.NewRecorder()
	s.apiForecastGroupMembers(w, httptest.NewRequest("GET", memberPopulationTarget+"&population=all", nil), "admin")
	var out struct {
		State   string
		Members []map[string]any
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out.State != "pending" || len(out.Members) != len(want) {
		t.Fatalf("all population = %d %s", w.Code, w.Body.String())
	}
	for _, row := range out.Members {
		id := int64(row["set_id"].(float64))
		reason, ok := want[id]
		if !ok || row["excluded_reason"] != nil && row["excluded_reason"] != reason || reason != "" && row["excluded_reason"] == nil {
			t.Fatalf("unexpected member/reason: %v, want %q", row, reason)
		}
		for _, field := range []string{"close_fen", "ret_bp", "bench_ret_bp", "excess_bp", "s0_fen", "terminal_fen", "upside", "direction"} {
			if _, ok := row[field]; ok {
				t.Errorf("non-mature all population leaked %s: %v", field, row)
			}
		}
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("missing members: %v", want)
	}
	legacy := doMembers(t, s, memberPopulationTarget)
	if len(legacy.Members) != 1 {
		t.Fatalf("legacy scored population changed: %+v", legacy)
	}
}

func TestMembersRejectUnknownPopulation(t *testing.T) {
	s := newV1Server(t)
	w := httptest.NewRecorder()
	s.apiForecastGroupMembers(w, httptest.NewRequest("GET", memberPopulationTarget+"&population=invalid", nil), "admin")
	if w.Code != 400 {
		t.Fatalf("invalid population = %d %s", w.Code, w.Body.String())
	}
}

func TestMembersAllMatureExclusionsNeverCarryOutcomes(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, 35, func(i int, g *gsSeed) {
		m := g.Marks["d5"]
		if i == 0 {
			m.CorpAction = true
		}
		if i == 1 {
			m.Status = markRefusedFinal
		}
		g.Marks["d5"] = m
	})
	calls := 0
	previous := forecastPerHook
	forecastPerHook = func() { calls++ }
	t.Cleanup(func() { forecastPerHook = previous })
	all := doMembers(t, s, memberPopulationTarget+"&population=all")
	if calls != 0 || all.State != "mature" || len(all.Members) != 35 {
		t.Fatalf("all mature population = %+v, scorer calls %d", all, calls)
	}
	for _, member := range all.Members {
		if member.ExcludedReason != "" {
			if member.CloseFen != nil || member.RetBp != nil || member.ExcessBp != nil || member.Upside != nil || member.Direction != "" {
				t.Fatalf("excluded member leaked outcomes: %+v", member)
			}
		} else if member.ExcessBp == nil {
			t.Fatalf("scored member lost its mature result: %+v", member)
		}
	}
	legacy := doMembers(t, s, memberPopulationTarget)
	if len(legacy.Members) != 33 {
		t.Fatalf("legacy population changed: %+v", legacy)
	}
}

func TestMembersAllExplainsNonStandardHorizon(t *testing.T) {
	s := newV1Server(t)
	id := seedGroupSet(t, s, gsSeed{Symbol: "600001", MadeOn: "2025-06-10", HorizonStd: false})
	target := strings.ReplaceAll(memberPopulationTarget, "checkpoint=d5", "checkpoint=h12")
	all := doMembers(t, s, target+"&population=all")
	if len(all.Members) != 1 || all.Members[0].SetID != id || all.Members[0].ExcludedReason != "non_standard_horizon" {
		t.Fatalf("h12 population = %+v", all)
	}
	if legacy := doMembers(t, s, target); len(legacy.Members) != 0 {
		t.Fatalf("nonstandard set entered h12 score population: %+v", legacy)
	}
}

func TestMemberLinksUseReadableReportsAndCanonicalKinds(t *testing.T) {
	eachDriver(t, func(t *testing.T, st *Store) {
		for _, table := range []string{"prediction_marks", "prediction_claims", "prediction_sets"} {
			if _, err := st.exec("DELETE FROM " + table); err != nil {
				t.Fatal(err)
			}
		}
		assertMemberLinks(t, &Server{st: st})
	})
}

func assertMemberLinks(t *testing.T, s *Server) {
	t.Helper()
	if err := s.st.UpsertUser(User{Username: "reader", Role: "user", PasswordHash: "unused"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetUserRestricted("reader", true); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SaveVersion(ReportVersion{Name: s.st.DefaultVersion(), Visibility: VisibilityOwner}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetVersionGrants(s.st.DefaultVersion(), []string{userPrincipal("reader")}); err != nil {
		t.Fatal(err)
	}
	visible, visibleSet := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600001", MadeOn: "2025-06-10", Marks: map[string]gsMark{"d5": {Status: markResolved}}}, "Visible")
	_, hiddenSet := seedReadableMembershipReport(t, s, gsSeed{Symbol: "600002", MadeOn: "2025-06-10", Marks: map[string]gsMark{"d5": {Status: markResolved}}}, "Hidden")
	if err := s.st.AddReportViewer(visible, "2025-06-10", "reader", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE reports SET kind=? WHERE id=?`, "每日推荐", visible); err != nil {
		t.Fatal(err)
	}
	if !s.canReadReport(visible, s.viewerScope("reader")) {
		t.Fatal("positive control: viewer cannot read granted report")
	}
	w := httptest.NewRecorder()
	s.apiForecastGroupMembers(w, httptest.NewRequest("GET", memberPopulationTarget+"&population=all", nil), "reader")
	var out forecastMembersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out.Members) != 2 {
		t.Fatalf("members = %d %s", w.Code, w.Body.String())
	}
	for _, m := range out.Members {
		switch m.SetID {
		case visibleSet:
			if m.ReportID == nil || *m.ReportID != visible || m.Kind != repKind(Rep{Kind: "每日推荐"}) {
				t.Fatalf("visible report link = %+v", m)
			}
		case hiddenSet:
			if m.ReportID != nil || m.Kind != "" || m.Name != "" {
				t.Fatalf("unreadable report link fields = %+v", m)
			}
		default:
			t.Fatalf("unexpected member %+v", m)
		}
	}
}
