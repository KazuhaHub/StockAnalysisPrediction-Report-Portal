package app

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStatisticsQueryValidation(t *testing.T) {
	for _, raw := range []string{"dataset=secrets", "dataset=reports&group=password_hash", "dataset=reports&from=2026-02-30", "dataset=reports&from=2026-10-26&to=2026-10-25", "dataset=runs&group=kind", "interval=quarter", "view=invalid"} {
		if _, err := parseStatisticsQuery(httptest.NewRequest("GET", "/?"+raw, nil)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func TestStatisticsAggregationPaginationAndExport(t *testing.T) {
	st := newTestStore(t)
	for _, r := range []Rep{{Symbol: "A", RType: "analysis", Kind: "Research", Date: "2026-10-25"}, {Symbol: "B", RType: "analysis", Kind: "Research", Date: "2026-10-25"}, {Symbol: "C", RType: "analysis", Kind: "=Formula", Date: "2026-10-24"}} {
		if _, _, err := st.UpsertReport(r); err != nil {
			t.Fatal(err)
		}
	}
	for typ := range internalTypes {
		st.UpsertReport(Rep{Symbol: "X", RType: typ, Kind: "Internal", Date: "2026-10-25"})
	}
	s := &Server{st: st}
	w := httptest.NewRecorder()
	s.apiStatistics(w, httptest.NewRequest("GET", "/?dataset=reports&group=kind&page_size=1", nil), "admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) || !strings.Contains(w.Body.String(), `"records":3`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.apiStatistics(w, httptest.NewRequest("GET", "/?dataset=reports&group=kind&page_size=1&format=csv", nil), "admin")
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 3 || rows[2][0] != "'=Formula" {
		t.Fatal(rows, err)
	}
	w = httptest.NewRecorder()
	s.apiStatistics(w, httptest.NewRequest("GET", "/?dataset=reports&group=kind&from=2026-10-25&to=2026-10-25&q=Research", nil), "admin")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"records":2`) {
		t.Fatal(w.Body.String())
	}
}

func TestStatisticsEveryRegisteredDimension(t *testing.T) {
	st := newTestStore(t)
	for dataset, dimensions := range statisticsDimensions {
		for dimension := range dimensions {
			w := httptest.NewRecorder()
			(&Server{st: st}).apiStatistics(w, httptest.NewRequest("GET", "/?dataset="+dataset+"&group="+dimension, nil), "admin")
			if w.Code != 200 {
				t.Fatalf("%s/%s: %d %s", dataset, dimension, w.Code, w.Body.String())
			}
		}
	}
}
func TestStatisticsRequiresAdministrator(t *testing.T) {
	s := accountServer(t)
	mux := http.NewServeMux()
	s.admin(mux, "GET /api/admin/statistics", s.apiStatistics)
	for _, user := range []string{"", "alice"} {
		r := httptest.NewRequest("GET", "/api/admin/statistics?format=csv", nil)
		if user != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: s.sign(user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		want := 401
		if user != "" {
			want = 403
		}
		if w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestStatisticsPanelDateBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	q, _ := parseStatisticsQuery(httptest.NewRequest("GET", "/?dataset=runs&from=2026-03-08&to=2026-03-08", nil))
	_, args := q.sql(loc)
	start, _ := time.ParseInLocation("2006-01-02 15:04:05", args[0].(string), time.Local)
	end, _ := time.ParseInLocation("2006-01-02 15:04:05", args[1].(string), time.Local)
	if end.Sub(start) != 23*time.Hour {
		t.Fatal(start, end)
	}
}

func TestStatisticsBoundsPageSizeAndStreamsAllGroups(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 125; i++ {
		if _, _, err := st.UpsertReport(Rep{Symbol: fmt.Sprintf("S%03d", i), RType: "analysis", Date: "2026-10-25"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{st: st}
	w := httptest.NewRecorder()
	s.apiStatistics(w, httptest.NewRequest("GET", "/?group=symbol&page_size=999", nil), "admin")
	var data struct {
		Items []statisticsRow `json:"items"`
		Total int             `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 100 || data.Total != 125 {
		t.Fatal(len(data.Items), data.Total)
	}
	w = httptest.NewRecorder()
	s.apiStatistics(w, httptest.NewRequest("GET", "/?group=symbol&page=2&page_size=25&format=csv", nil), "admin")
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(w.Body.String(), "\ufeff"))).ReadAll()
	if err != nil || len(rows) != 126 {
		t.Fatal(len(rows), err)
	}
}

func TestStatisticsChartUsesAllGroupsInsteadOfTablePage(t *testing.T) {
	st := newTestStore(t)
	for i := 0; i < 25; i++ {
		st.UpsertReport(Rep{Symbol: fmt.Sprint(i), RType: "analysis", Kind: fmt.Sprintf("K%02d", i), Date: "2026-10-25"})
	}
	w := httptest.NewRecorder()
	(&Server{st: st}).apiStatistics(w, httptest.NewRequest("GET", "/?group=kind&view=chart&page=2&page_size=1", nil), "admin")
	var data struct {
		Items []statisticsRow `json:"items"`
		Other int             `json:"other"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Items) != 20 || data.Items[0].Label != "K00" || data.Other != 5 {
		t.Fatal(data)
	}
}
func TestStatisticsDateChartUsesChronologicalBuckets(t *testing.T) {
	st := newTestStore(t)
	for _, r := range []Rep{{Symbol: "A", RType: "analysis", Date: "2026-01-03"}, {Symbol: "B", RType: "analysis", Date: "2026-01-04"}, {Symbol: "C", RType: "analysis", Date: "2026-02-01"}} {
		st.UpsertReport(r)
	}
	w := httptest.NewRecorder()
	(&Server{st: st}).apiStatistics(w, httptest.NewRequest("GET", "/?group=date&view=chart&interval=month", nil), "admin")
	var data struct {
		Items []statisticsRow `json:"items"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	if len(data.Items) != 2 || data.Items[0].Label != "2026-01" || data.Items[0].Count != 2 {
		t.Fatal(w.Body.String())
	}
}

func TestStatisticsDoesNotDrawTruncatedTimeSeries(t *testing.T) {
	st := newTestStore(t)
	start, _ := time.Parse("2006-01-02", "2025-01-01")
	for i := 0; i < 401; i++ {
		st.UpsertReport(Rep{Symbol: "A", RType: "analysis", Date: start.AddDate(0, 0, i).Format("2006-01-02")})
	}
	w := httptest.NewRecorder()
	(&Server{st: st}).apiStatistics(w, httptest.NewRequest("GET", "/?group=date&view=chart", nil), "admin")
	var data struct {
		Items   []statisticsRow `json:"items"`
		TooMany bool            `json:"too_many_points"`
		Total   int             `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	if !data.TooMany || len(data.Items) != 0 || data.Total != 401 {
		t.Fatal(w.Body.String())
	}
}
