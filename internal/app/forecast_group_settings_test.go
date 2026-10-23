package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroupSaveForecastScope(t *testing.T) {
	s := routeTableServer(t)
	id := seedOU(t, s.st, 0, scopeOwn)
	save := func(id int64, extra string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("PUT", "/api/admin/groups/"+itoa64(id), strings.NewReader(`{"name":"forecast-group-`+itoa64(id)+`"`+extra+`}`))
		r.SetPathValue("id", itoa64(id))
		w := httptest.NewRecorder()
		s.apiGroupSave(w, r, "admin")
		return w
	}
	read := func(id int64) sql.NullString {
		t.Helper()
		var v sql.NullString
		if err := s.st.queryRow("SELECT forecast_scope FROM user_groups WHERE id=?", id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		extra   string
		want    any
		status  int
		changed bool
	}{
		{"", scopeOwn, 200, false},
		{`,"forecast_scope":null`, nil, 200, true},
		{`,"forecast_scope":"all"`, scopeAll, 200, true},
		{`,"forecast_scope":"all"`, scopeAll, 200, false},
		{`,"forecast_scope":"none"`, scopeNone, 200, true},
		{`,"forecast_scope":"own"`, scopeOwn, 200, true},
		{`,"forecast_scope":"everyone"`, scopeOwn, 400, false},
		{`,"forecast_scope":false`, scopeOwn, 400, false},
		{`,"forecast_scope":0`, scopeOwn, 400, false},
		{`,"forecast_scope":{}`, scopeOwn, 400, false},
		{`,"forecast_scope":""`, scopeOwn, 400, false},
	} {
		before := read(id)
		w := save(id, tc.extra)
		if w.Code != tc.status || tc.status == 400 && !hasCode(w.Body.Bytes(), "forecast_bad_scope") {
			t.Fatalf("%s: %d %s", tc.extra, w.Code, w.Body.String())
		}
		v := read(id)
		if (tc.want == nil && v.Valid) || (tc.want != nil && (!v.Valid || v.String != tc.want)) {
			t.Fatalf("%s: scope %#v, want %v", tc.extra, v, tc.want)
		}
		if w.Code == http.StatusOK {
			var detail string
			if err := s.st.queryRow("SELECT detail FROM audit_log WHERE action=? AND target_id=? ORDER BY id DESC LIMIT 1", AuditGroupChange, itoa64(id)).Scan(&detail); err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(detail), &fields); err != nil {
				t.Fatal(err)
			}
			_, present := fields["forecast_scope"]
			if present != tc.changed {
				t.Fatalf("%s: change-only audit mismatch %s", tc.extra, detail)
			}
			if tc.changed {
				change := fields["forecast_scope"].(map[string]any)
				var from any
				if before.Valid {
					from = before.String
				}
				if change["from"] != from || change["to"] != tc.want {
					t.Fatalf("wrong audit delta: %v", change)
				}
			}
		}
	}
	def := s.st.DefaultGroupID()
	if w := save(def, `,"forecast_scope":null`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if v := read(def); !v.Valid || v.String != scopeNone {
		t.Fatalf("Default inherited: %#v", v)
	}
}

func TestPostgresSaveGroupForecastScope(t *testing.T) {
	st := pgStore(t)
	id := seedOU(t, st, 0, scopeOwn)
	defer st.exec("DELETE FROM user_groups WHERE id=?", id)
	change, err := st.saveGroupForecastScope(id, nil)
	if err != nil || change["from"] != scopeOwn || change["to"] != nil {
		t.Fatalf("inherit delta: %v %v", change, err)
	}
	v := scopeAll
	change, err = st.saveGroupForecastScope(id, &v)
	if err != nil || change["from"] != nil || change["to"] != scopeAll {
		t.Fatalf("override delta: %v %v", change, err)
	}
	change, err = st.saveGroupForecastScope(id, &v)
	if err != nil || change != nil {
		t.Fatalf("unchanged delta: %v %v", change, err)
	}
}

func TestAdminGroupForecastScopeIsServerResolved(t *testing.T) {
	s := routeTableServer(t)
	parent := seedOU(t, s.st, 0, scopeAll)
	child := seedOU(t, s.st, parent, nil)
	w := httptest.NewRecorder()
	s.apiAdminGroups(w, httptest.NewRequest("GET", "/api/admin/groups", nil), "admin")
	var out struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, g := range out.Groups {
		if g["id"] == float64(child) {
			if own, ok := g["forecast_scope"]; !ok || own != nil || g["forecast_scope_effective"] != scopeAll || g["forecast_scope_source"] != float64(parent) {
				t.Fatalf("inherited scope not resolved: %v", g)
			}
			return
		}
	}
	t.Fatal("child missing")
}

// The OU editor lives on the users page, whose groups come from the users list rather than the
// groups list. Both must describe forecast access identically, or the editor would show (and save
// over) a value the server never resolved.
func TestAdminUsersGroupsCarryResolvedForecastScope(t *testing.T) {
	s := routeTableServer(t)
	parent := seedOU(t, s.st, 0, scopeAll)
	child := seedOU(t, s.st, parent, nil)
	w := httptest.NewRecorder()
	s.apiAdminUsers(w, httptest.NewRequest("GET", "/api/admin/users", nil), "admin")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var out struct {
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	seen := map[float64]bool{}
	for _, g := range out.Groups {
		id, _ := g["id"].(float64)
		seen[id] = true
		for _, field := range []string{"forecast_scope", "forecast_scope_effective", "forecast_scope_source"} {
			if _, ok := g[field]; !ok {
				t.Fatalf("group %v lacks %s: %v", g["id"], field, g)
			}
		}
		if id == float64(child) && (g["forecast_scope"] != nil || g["forecast_scope_effective"] != scopeAll || g["forecast_scope_source"] != float64(parent)) {
			t.Fatalf("inherited scope not resolved in the users list: %v", g)
		}
	}
	if !seen[float64(parent)] || !seen[float64(child)] {
		t.Fatalf("seeded OUs missing from the users list: %v", seen)
	}
}

func TestBadForecastScopeDoesNotPartiallySaveGroup(t *testing.T) {
	s := routeTableServer(t)
	id := seedOU(t, s.st, 0, scopeOwn)
	var oldName string
	if err := s.st.queryRow("SELECT name FROM user_groups WHERE id=?", id).Scan(&oldName); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("PUT", "/api/admin/groups/"+itoa64(id), strings.NewReader(`{"name":"changed-name","restricted":true,"forecast_scope":"everyone"}`))
	r.SetPathValue("id", itoa64(id))
	w := httptest.NewRecorder()
	s.apiGroupSave(w, r, "admin")
	if w.Code != 400 || !hasCode(w.Body.Bytes(), "forecast_bad_scope") {
		t.Fatal(w.Body.String())
	}
	var name string
	var restricted int
	if err := s.st.queryRow("SELECT name,restricted FROM user_groups WHERE id=?", id).Scan(&name, &restricted); err != nil {
		t.Fatal(err)
	}
	if name != oldName || restricted != 0 {
		t.Fatalf("invalid scope partially mutated OU: %s %d", name, restricted)
	}
	var n int
	if err := s.st.queryRow("SELECT COUNT(*) FROM audit_log WHERE action=? AND target_id=?", AuditGroupChange, itoa64(id)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("invalid save emitted change audit")
	}
}

func TestForecastScopeInputRejectsNonStringsAndNormalizesDefault(t *testing.T) {
	for _, raw := range []string{`true`, `false`, `0`, `1`, `[]`, `{}`, `""`, `"ALL"`, `" all "`, `"everyone"`} {
		if _, err := parseForecastScopeInput(json.RawMessage(raw), false); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, def := range []bool{false, true} {
		if v, err := parseForecastScopeInput(nil, def); err != nil || v != nil {
			t.Errorf("omitted: %v %v", v, err)
		}
		v, err := parseForecastScopeInput(json.RawMessage(" \n null \t"), def)
		if err != nil || def && (v == nil || *v != scopeNone) || !def && v != nil {
			t.Errorf("null default=%v: %v %v", def, v, err)
		}
	}
}

// The users list carries the OU editor, and it used to be unable to fail. It reads the tree and the
// group list in two statements, so an OU created between them is in the list and not in the index.
// That is a race to survive, not corruption to refuse: the OU is listed without forecast access, and
// the editor hides a field the server did not resolve, so nothing is ever shown that was not read.
func TestAdminGroupsListSurvivesAnOUCreatedBetweenTheReads(t *testing.T) {
	s := routeTableServer(t)
	parent := seedOU(t, s.st, 0, scopeAll)
	ix, err := s.st.loadOUIndex()
	if err != nil {
		t.Fatal(err)
	}
	late := seedOU(t, s.st, parent, nil)

	groups := adminGroupsWithAccess(s.st.ListUserGroups(), ix)
	seen := map[int64]map[string]any{}
	for _, g := range groups {
		seen[g["id"].(int64)] = g
	}
	if seen[late] == nil || seen[parent] == nil {
		t.Fatalf("the list dropped an OU: %v", seen)
	}
	for _, field := range []string{"forecast_scope", "forecast_scope_effective", "forecast_scope_source"} {
		if _, ok := seen[late][field]; ok {
			t.Errorf("an OU the index never read carries %s: %v", field, seen[late])
		}
		if _, ok := seen[parent][field]; !ok {
			t.Errorf("an indexed OU lost %s: %v", field, seen[parent])
		}
	}
	if seen[parent]["forecast_scope_effective"] != scopeAll {
		t.Errorf("indexed OU resolved to %v", seen[parent]["forecast_scope_effective"])
	}
}
