package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestForecastAccessOverviewExcludesExpiredAccounts(t *testing.T) {
	s := routeTableServer(t)
	s.st.SetSetting("timezone", "Pacific/Kiritimati")
	today := s.panelToday()
	d, err := time.Parse("2006-01-02", today)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, date string }{{"expired", d.AddDate(0, 0, -1).Format("2006-01-02")}, {"today", today}, {"future", d.AddDate(0, 0, 1).Format("2006-01-02")}, {"unlimited", ""}} {
		mustAddUser(t, s, tc.name, false)
		if err := s.st.SetUserExpiry(tc.name, tc.date); err != nil {
			t.Fatal(err)
		}
	}
	subjects, ix, err := s.st.loadForecastAccessOverview()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, sub := range subjects {
		seen[sub.User] = true
	}
	if seen["expired"] || !seen["today"] || !seen["future"] || !seen["unlimited"] {
		t.Fatalf("panel-day expiry mismatch: %v", seen)
	}
	if c := forecastOverview(subjects, ix).Counts; c.None != 3 {
		t.Fatalf("expired account counted: %+v", c)
	}
}

func TestPostgresForecastAccessOverviewUsesActiveAccounts(t *testing.T) {
	st := pgStore(t)
	s := &Server{st: st}
	id := seedOU(t, st, 0, scopeOwn)
	defer st.exec("DELETE FROM user_groups WHERE id=?", id)
	for _, name := range []string{"forecast-preview-active", "forecast-preview-disabled"} {
		mustAddUser(t, s, name, false)
		defer st.exec("DELETE FROM users WHERE username=?", name)
		if err := st.SetPrimaryGroup(name, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetUserActive("forecast-preview-disabled", false); err != nil {
		t.Fatal(err)
	}
	subjects, ix, err := st.loadForecastAccessOverview()
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, sub := range subjects {
		if sub.User == "forecast-preview-disabled" {
			t.Fatal("disabled account counted")
		}
		if sub.User == "forecast-preview-active" {
			seen = true
			if resolveForecastScope(sub, ix).Scope != scopeOwn {
				t.Fatal("snapshot policy missing")
			}
		}
	}
	if !seen {
		t.Fatal("active account missing")
	}
	for _, g := range forecastOverview(subjects, ix).Groups {
		if g.ID == id {
			if g.Counts.Own != 1 {
				t.Fatalf("wrong OU count: %+v", g)
			}
			return
		}
	}
	t.Fatal("OU missing")
}

func TestForecastAccessPreviewMatchesRealAccess(t *testing.T) {
	s := routeTableServer(t)
	def := s.st.DefaultGroupID()
	root := seedOU(t, s.st, 0, scopeAll)
	child := seedOU(t, s.st, root, nil)
	for _, tc := range []struct {
		user  string
		ou    int64
		admin bool
	}{{"unassigned", 0, false}, {"root-member", root, false}, {"child-member", child, false}, {"admin", child, true}, {"disabled", root, false}} {
		mustAddUser(t, s, tc.user, tc.admin)
		if tc.ou != 0 {
			if err := s.st.SetPrimaryGroup(tc.user, tc.ou); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.st.SetUserActive("disabled", false); err != nil {
		t.Fatal(err)
	}
	read := func(query string) forecastAccessOverview {
		t.Helper()
		w := httptest.NewRecorder()
		s.apiForecastAccess(w, httptest.NewRequest("GET", "/api/admin/forecast/access"+query, nil), "admin")
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		for _, name := range []string{"unassigned", "root-member", "child-member", "disabled"} {
			if strings.Contains(w.Body.String(), name) {
				t.Fatal("account identity leaked into count-only overview")
			}
		}
		var out forecastAccessOverview
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	base := read("")
	if base.Counts.All != 3 || base.Counts.None != 1 || base.Counts.Own != 0 {
		t.Fatalf("unassigned/root/admin counts: %+v", base.Counts)
	}
	preview := read("?ou=" + itoa64(root) + "&scope=own")
	if preview.Counts.All != 1 || preview.Counts.Own != 2 || preview.Counts.None != 1 || preview.Affected != 2 {
		t.Fatalf("preview: %+v", preview)
	}
	if !reflect.DeepEqual(base, read("")) {
		t.Fatal("preview mutated persistent policy")
	}
	if _, err := s.st.saveGroupForecastScope(root, scopeValue(scopeOwn)); err != nil {
		t.Fatal(err)
	}
	actual := read("")
	if !reflect.DeepEqual(preview.Counts, actual.Counts) || !reflect.DeepEqual(preview.Groups, actual.Groups) {
		t.Fatalf("hypothesis and saved policy disagree: %+v %+v", preview, actual)
	}
	defaultPreview := read("?ou=" + itoa64(def) + "&scope=all")
	if defaultPreview.Counts.All != 2 || defaultPreview.Counts.Own != 2 || defaultPreview.Affected != 1 {
		t.Fatalf("Default/unassigned preview: %+v", defaultPreview)
	}
	for _, g := range defaultPreview.Groups {
		if g.ID == def && (g.Counts.All != 1 || g.Effective != scopeAll) {
			t.Fatalf("unassigned excluded from Default: %+v", g)
		}
	}
	inherit := read("?ou=" + itoa64(root) + "&scope=inherit")
	if inherit.Counts.None != 3 || inherit.Counts.All != 1 || inherit.Counts.Own != 0 || inherit.Affected != 2 {
		t.Fatalf("inherit preview: %+v", inherit)
	}
}

func TestForecastAccessPreviewRejectsInvalidQuery(t *testing.T) {
	s := routeTableServer(t)
	for _, query := range []string{"?ou=1", "?scope=all", "?ou=0&scope=all", "?ou=abc&scope=all", "?ou=999999&scope=all", "?ou=1&scope=everyone", "?ou=1&scope=", "?ou=1&ou=2&scope=all"} {
		w := httptest.NewRecorder()
		s.apiForecastAccess(w, httptest.NewRequest("GET", "/api/admin/forecast/access"+query, nil), "admin")
		if w.Code != 400 || !hasCode(w.Body.Bytes(), "forecast_bad_scope") {
			t.Errorf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/forecast/access", nil))
	if w.Code != 401 {
		t.Fatalf("anonymous preview: %d", w.Code)
	}
	seedForecastReader(t, s, "reader", scopeAll, false)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, sessionForPath(t, s, "reader", "GET /api/admin/forecast/access", "/api/admin/forecast/access"))
	if w.Code != 403 {
		t.Fatalf("reader reached admin preview: %d", w.Code)
	}
}

func TestForecastAccessOverviewRefusesUnavailableStore(t *testing.T) {
	s := routeTableServer(t)
	if err := s.st.db.Close(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.apiForecastAccess(w, httptest.NewRequest("GET", "/api/admin/forecast/access", nil), "admin")
	if w.Code != 500 || !hasCode(w.Body.Bytes(), "db_error") {
		t.Fatalf("failed read became empty access summary: %d %s", w.Code, w.Body.String())
	}
}
