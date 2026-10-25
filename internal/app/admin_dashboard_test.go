package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardLayoutPersistsGloballyAndRejectsStaleChanges(t *testing.T) {
	s := userAdminServer(t)
	original := normalizedDashboard("")
	original.Blocks[0].Width = 6
	original.Blocks[1].Visible = false
	body, _ := json.Marshal(original)
	rec := httptest.NewRecorder()
	s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.apiDashboardLayout(rec, httptest.NewRequest(http.MethodGet, "/", nil), "another-admin")
	var got dashboardLayout
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Blocks[0].Width != 6 || got.Blocks[1].Visible || got.Revision == original.Revision {
		t.Fatalf("global layout not retained: %+v", got)
	}
	rec = httptest.NewRecorder()
	s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save: %d", rec.Code)
	}
}
func TestDashboardLayoutRejectsUnknownDuplicateAndEmptyLayouts(t *testing.T) {
	for _, change := range []func(*dashboardLayout){
		func(c *dashboardLayout) { c.Blocks[0].ID = "unknown" }, func(c *dashboardLayout) { c.Blocks[0].ID = c.Blocks[1].ID }, func(c *dashboardLayout) { c.Blocks[0].Width = 1 }, func(c *dashboardLayout) {
			for i := range c.Blocks {
				c.Blocks[i].Visible = false
			}
		},
	} {
		s := userAdminServer(t)
		in := normalizedDashboard("")
		change(&in)
		body, _ := json.Marshal(in)
		rec := httptest.NewRecorder()
		s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid layout accepted: %d", rec.Code)
		}
	}
}

func TestDashboardLayoutAcceptsShortcutWidgets(t *testing.T) {
	s := userAdminServer(t)
	in := normalizedDashboard("")
	in.Blocks = append(in.Blocks, dashboardBlock{ID: "shortcut:types", Visible: true, Width: 1})
	body, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("shortcut save: %d %s", rec.Code, rec.Body.String())
	}
	var got dashboardLayout
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	last := got.Blocks[len(got.Blocks)-1]
	if last.ID != "shortcut:types" || last.Width != 1 {
		t.Fatalf("shortcut not retained: %+v", got)
	}
}

func TestDashboardMetricSelectionsAreValidatedAndRetained(t *testing.T) {
	s := userAdminServer(t)
	in := normalizedDashboard("")
	in.Blocks[0].Metrics = []string{"yesterday", "year"}
	body, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics save: %d %s", rec.Code, rec.Body.String())
	}
	got := normalizedDashboard(s.st.GetSetting(dashboardSetting, ""))
	if strings.Join(got.Blocks[0].Metrics, ",") != "yesterday,year" {
		t.Fatalf("lost metrics: %+v", got)
	}
	for _, metrics := range [][]string{{}, {"unknown"}, {"today", "today"}} {
		in = got
		in.Blocks[0].Metrics = metrics
		body, _ = json.Marshal(in)
		// Empty slices are tested directly because omitempty deliberately represents defaults on the wire.
		if len(metrics) == 0 {
			if validDashboardMetrics(in.Blocks[0]) {
				t.Fatal("empty selection accepted")
			}
			continue
		}
		rec = httptest.NewRecorder()
		s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid metrics accepted: %v", metrics)
		}
	}
}

func TestDashboardChartOptionsValidation(t *testing.T) {
	valid := dashboardBlock{ID: "trend", Width: 3, Visible: true, Chart: &dashboardChart{Range: 365, Interval: "month", ShowValues: false, Cumulative: true, TopN: 5}}
	if !validDashboardBlock(valid) {
		t.Fatal("valid annual chart rejected")
	}
	valid.Chart.Range = 10000
	if validDashboardBlock(valid) {
		t.Fatal("unbounded range accepted")
	}
	valid.Chart.Range = 30
	valid.ID = "reports"
	if validDashboardBlock(valid) {
		t.Fatal("chart options accepted on metrics")
	}
}

func TestDashboardChartTypeCompatibility(t *testing.T) {
	for id, allowed := range map[string][]string{"trend": {"", "line", "column"}, "report-bars": {"", "line", "column"}, "kinds": {"", "bar", "donut"}, "report-donut": {"", "bar", "donut"}} {
		for _, kind := range []string{"", "line", "column", "bar", "donut", "unknown"} {
			want := false
			for _, candidate := range allowed {
				want = want || candidate == kind
			}
			block := dashboardBlock{ID: id, Width: 3, Visible: true, Chart: &dashboardChart{Range: 30, Interval: "auto", TopN: 5, Type: kind}}
			if validDashboardBlock(block) != want {
				t.Errorf("%s type %q: expected valid=%v", id, kind, want)
			}
		}
	}
}

func TestDashboardInstancesRetainIndependentOptions(t *testing.T) {
	s := userAdminServer(t)
	in := normalizedDashboard("")
	in.Blocks = append(in.Blocks,
		dashboardBlock{ID: "queue~123e4567-e89b-42d3-a456-426614174000", Visible: true, Width: 3, Metrics: []string{"waiting"}, Display: "bar"},
		dashboardBlock{ID: "trend~123e4567-e89b-42d3-a456-426614174001", Visible: true, Width: 6, Chart: &dashboardChart{Type: "column", Range: 30, Interval: "day", TopN: 5}})
	body, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	s.apiDashboardLayoutSave(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(string(body))), "admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	got := normalizedDashboard(s.st.GetSetting(dashboardSetting, ""))
	if len(got.Blocks) != len(in.Blocks) || got.Blocks[len(in.Blocks)-2].Display != "bar" || got.Blocks[len(in.Blocks)-1].Chart.Range != 30 {
		t.Fatalf("instance options lost: %+v", got)
	}
	for _, b := range []dashboardBlock{
		{ID: "queue~invalid", Width: 3, Visible: true},
		{ID: "unknown~123e4567-e89b-42d3-a456-426614174000", Width: 3, Visible: true},
		{ID: "queue", Width: 3, Visible: true, Display: "donut"},
		{ID: "trend", Width: 3, Visible: true, Display: "bar"},
	} {
		if validDashboardBlock(b) {
			t.Fatalf("invalid instance accepted: %+v", b)
		}
	}
}
