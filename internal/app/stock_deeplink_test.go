package app

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestStockDeepLinkByReportID(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) { assertStockDeepLinkByReportID(t, newV1Server(t)) })
	t.Run("postgres", func(t *testing.T) {
		st := pgStore(t, "reports", "users")
		s := &Server{st: st, names: LoadNames(t.TempDir(), st)}
		s.names.fetch = func(string) string { return "" }
		assertStockDeepLinkByReportID(t, s)
	})
}

func assertStockDeepLinkByReportID(t *testing.T, s *Server) {
	t.Helper()
	mustAddUser(t, s, "deep-reader", false)
	add := func(symbol, date, kind, title string) int64 {
		t.Helper()
		id, _, err := s.st.UpsertReport(Rep{Symbol: symbol, Date: date, Kind: kind, RType: predTestRtype, Version: "deep-link-fixture", Title: title, MD: "body"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	latest := add("600001", "2026-10-20", kindOrder[0], "Latest report")
	target := add("600001", "2026-09-17", kindOrder[1], "Linked report")
	foreign := add("600002", "2026-09-17", kindOrder[1], "Other symbol")
	for _, tc := range []struct {
		name, id string
		wantID   int64
		wantDate string
		wantKind string
	}{
		{"readable id overrides stale date and kind", itoa64(target), target, "2026-09-17", kindOrder[1]},
		{"other symbol is ignored", itoa64(foreign), latest, "2026-10-20", kindOrder[0]},
		{"unknown id is ignored", "999999999", latest, "2026-10-20", kindOrder[0]},
		{"invalid id is ignored", "invalid", latest, "2026-10-20", kindOrder[0]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"r": {tc.id}, "date": {"2026-10-20"}, "kind": {kindOrder[0]}}
			r := httptest.NewRequest("GET", "/api/stock/600001?"+q.Encode(), nil)
			r.SetPathValue("symbol", "600001")
			w := httptest.NewRecorder()
			s.apiStock(w, r, "deep-reader")
			var out struct {
				SelID   int64              `json:"selId"`
				SelDate string             `json:"selDate"`
				SelKind string             `json:"selKind"`
				Rep     struct{ ID int64 } `json:"rep"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
				t.Fatalf("stock response=%d %s: %v", w.Code, w.Body.String(), err)
			}
			if out.SelID != tc.wantID || out.Rep.ID != tc.wantID || out.SelDate != tc.wantDate || out.SelKind != tc.wantKind {
				t.Fatalf("deep link selection=%+v, want id=%d date=%s kind=%s", out, tc.wantID, tc.wantDate, tc.wantKind)
			}
		})
	}
	seedForecastReader(t, s, "deep-external", scopeAll, true)
	if err := s.st.SaveVersion(ReportVersion{Name: "deep-link-fixture", Visibility: VisibilityGroup}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.st.DeleteVersion("deep-link-fixture"); err != nil {
			t.Error(err)
		}
	})
	if err := s.st.SetVersionGrants("deep-link-fixture", []string{userPrincipal("deep-external")}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetReportViewers(latest, "2026-10-20", []string{userPrincipal("deep-external")}); err != nil {
		t.Fatal(err)
	}
	if s.canReadReport(target, s.viewerScope("deep-external")) {
		t.Fatal("negative fixture unexpectedly grants the linked report")
	}
	r := httptest.NewRequest("GET", "/api/stock/600001?r="+itoa64(target), nil)
	r.SetPathValue("symbol", "600001")
	w := httptest.NewRecorder()
	s.apiStock(w, r, "deep-external")
	var out struct {
		SelID    int64 `json:"selId"`
		Timeline []struct {
			Date string `json:"date"`
		} `json:"timeline"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.SelID != latest {
		t.Fatalf("unreadable deep link changed default selection: %d %s: %v", w.Code, w.Body.String(), err)
	}
	if len(out.Timeline) != 1 || out.Timeline[0].Date != "2026-10-20" {
		t.Fatalf("unreadable report leaked its date: %+v", out.Timeline)
	}
}
