package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestReaderMembersUseFrozenCounts(t *testing.T) {
	for _, status := range []string{markResolved, markPending} {
		t.Run(status, func(t *testing.T) {
			s := newV1Server(t)
			ou := seedForecastReader(t, s, "reader", scopeOwn, false)
			sampleMembers(t, s, 32, nil)
			if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", ou); err != nil {
				t.Fatal(err)
			}
			readerGroups(t, s, "reader", "")
			id := seedGroupSet(t, s, gsSeed{Symbol: "600519", MadeOn: "2025-06-11", S0Fen: ip(10000),
				Marks: map[string]gsMark{"d5": {Status: status, RetBp: bp(100), BenchRetBp: bp(50)}}})
			if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=? WHERE id=?", ou, id); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.apiReaderForecastGroupMembers(w, httptest.NewRequest("GET", "/api/forecast/groups/members?label=2025-06&checkpoint=d5", nil), "reader")
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || out["state"] != "mature" || out["n"] != float64(32) || out["total"] != float64(32) || out["resolved"] != float64(32) {
				t.Fatalf("drawer counts moved away from frozen matrix: %d %v", w.Code, out)
			}
		})
	}
}

func TestReaderMembersFrozenCountsIncludeExcluded(t *testing.T) {
	s := newV1Server(t)
	ou := seedForecastReader(t, s, "reader", scopeOwn, false)
	sampleMembers(t, s, 32, func(i int, g *gsSeed) {
		m := g.Marks["d5"]
		if i == 0 {
			m.CorpAction = true
		}
		if i == 1 {
			m.Status = markRefusedFinal
		}
		g.Marks["d5"] = m
	})
	if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", ou); err != nil {
		t.Fatal(err)
	}
	readerGroups(t, s, "reader", "")
	id := seedGroupSet(t, s, gsSeed{Symbol: "600519", MadeOn: "2025-06-11", S0Fen: ip(10000)})
	if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=? WHERE id=?", ou, id); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.apiReaderForecastGroupMembers(w, httptest.NewRequest("GET", "/api/forecast/groups/members?label=2025-06&checkpoint=d5", nil), "reader")
	var out forecastReaderMembersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || out.State != "mature" || out.N == nil || *out.N != 30 ||
		out.Resolved == nil || *out.Resolved != 31 || out.Total == nil || *out.Total != 32 ||
		out.CorpAction == nil || *out.CorpAction != 1 || out.Refused == nil || *out.Refused != 1 {
		t.Fatalf("frozen counts lost excluded outcomes: %d %s", w.Code, w.Body.String())
	}
}

func TestExternalDrawerListsOnlyReadableReports(t *testing.T) {
	f := seedReadFixture(t, VisibilityGroup)
	s := f.s
	if _, err := s.st.exec("UPDATE users SET active=1 WHERE username=?", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeAll, f.ou); err != nil {
		t.Fatal(err)
	}
	sets := sampleMembers(t, s, 32, nil)
	for i, reportID := range []int64{f.pub, f.mateReport, f.strangerReport} {
		if _, err := s.st.exec("UPDATE prediction_sets SET report_id=? WHERE id=?", reportID, sets[i].ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.st.exec("UPDATE prediction_sets SET attributed_ou=?", f.ou); err != nil {
		t.Fatal(err)
	}
	read := func(extra string) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		s.apiReaderForecastGroupMembers(w, httptest.NewRequest("GET", "/api/forecast/groups/members?label=2025-06&checkpoint=d5"+extra, nil), "alice")
		if w.Code != http.StatusOK {
			t.Fatalf("members: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := read("&limit=1")
	for _, key := range []string{"n", "total", "resolved", "refused", "corp_action"} {
		if _, ok := first[key]; ok {
			t.Errorf("external all members has %s", key)
		}
	}
	rows := first["members"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["report_id"] != float64(f.pub) {
		t.Fatal(rows)
	}
	cursor, _ := first["next_cursor"].(string)
	if cursor == "" {
		t.Fatal("readable next member has no cursor")
	}
	second := read("&limit=1&cursor=" + url.QueryEscape(cursor))
	rows = second["members"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["report_id"] != float64(f.mateReport) {
		t.Fatal(rows)
	}
	if _, ok := second["next_cursor"]; ok {
		t.Fatal("unreadable or purged members caused a next page")
	}
	if _, err := s.st.exec("UPDATE user_groups SET forecast_scope=? WHERE id=?", scopeOwn, f.ou); err != nil {
		t.Fatal(err)
	}
	ensurePurgeEvidenceTable(t, s.st)
	own := read("")
	if own["n"] != float64(32) || len(own["members"].([]any)) != 2 {
		t.Fatalf("own lost counts/readability filter: %v", own)
	}
}

// A halted stock settles on its last traded close. The drawer tags that row so a reader does not
// mistake a flat return for a quiet stock, but only on a scored row of a mature cell: a halt flag
// on a cell that has not matured would be an outcome the matrix itself refuses to publish.
func TestMembersFlagHaltedMarksOnScoredRowsOfMatureCells(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+2, func(i int, g *gsSeed) {
		m := g.Marks["d5"]
		m.Halted = i == 0
		g.Marks["d5"] = m
	})
	resp := doMembers(t, s, "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5")
	if resp.State != "mature" {
		t.Fatalf("state = %q", resp.State)
	}
	halted := 0
	for _, m := range resp.Members {
		if m.Halted {
			halted++
		}
	}
	if halted != 1 {
		t.Fatalf("halted rows = %d, want 1", halted)
	}
	raw := httptest.NewRecorder()
	s.apiForecastGroupMembers(raw, httptest.NewRequest("GET", "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5", nil), "admin")
	var wire struct {
		Members []map[string]any `json:"members"`
	}
	if err := json.Unmarshal(raw.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	flagged := 0
	for _, m := range wire.Members {
		if _, ok := m["halted"]; ok {
			flagged++
		}
	}
	if flagged != 1 {
		t.Fatalf("rows carrying the halted key = %d, want only the halted one", flagged)
	}
}

func TestMembersOfANonMatureCellCarryNoHaltedFlag(t *testing.T) {
	s := newV1Server(t)
	sampleMembers(t, s, predMatureMinN+2, func(i int, g *gsSeed) {
		m := g.Marks["d5"]
		m.Halted = true
		if i == 0 {
			m.Status = markPending
		}
		g.Marks["d5"] = m
	})
	resp := doMembers(t, s, "/api/admin/forecast/groups/members?mode=custom&from=2025-06-01&to=2025-06-30&group=month&label=2025-06&checkpoint=d5&population=all")
	if resp.State != "pending" {
		t.Fatalf("state = %q, want pending", resp.State)
	}
	for _, m := range resp.Members {
		if m.Halted {
			t.Fatalf("a member of a pending cell is tagged halted: %+v", m)
		}
	}
}
