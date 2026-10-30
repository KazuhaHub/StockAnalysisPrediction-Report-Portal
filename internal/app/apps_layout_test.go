package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// App ordering and per-OU access (ADR 0048). The acceptance conditions are numbered as in the ADR.

type appsEnv struct {
	*exportEnv
	ou int64 // the OU "rita" lives in
}

func newAppsEnv(t *testing.T) *appsEnv {
	t.Helper()
	e := &appsEnv{exportEnv: newExportEnv(t)}
	e.s.appTok = newAppTokens(appTokenTTL)
	e.ou = seedOU(t, e.s.st, 0, nil)
	mustAddUser(t, e.s, "rita", false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username=?`, e.ou, "rita"); err != nil {
		t.Fatal(err)
	}
	e.install("alpha")
	e.install("beta")
	return e
}

func (e *appsEnv) install(id string) {
	e.t.Helper()
	if err := e.s.st.InstallApp(App{ID: id, Name: strings.ToUpper(id), Entry: "index.html", Scopes: []string{"query"}},
		map[string]AppFile{"index.html": {Ctype: "text/html", Content: []byte("<h1>" + id + "</h1>")}}); err != nil {
		e.t.Fatal(err)
	}
}

// setAccess drives the real admin endpoint. enabled is the JSON literal: true, false or null.
func (e *appsEnv) setAccess(key string, ou int64, enabled string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("admin", "PUT", "/api/admin/apps/"+key+"/access/"+itoa64(ou), `{"enabled":`+enabled+`}`)
}

func (e *appsEnv) mustSetAccess(key string, ou int64, enabled string) {
	e.t.Helper()
	if w := e.setAccess(key, ou, enabled); w.Code != http.StatusOK {
		e.t.Fatalf("set %s/%d=%s: %d %s", key, ou, enabled, w.Code, w.Body.String())
	}
}

// listed is the reader-facing answer for a user: installed ids, built-in slugs, and the key order.
func (e *appsEnv) listed(user string) (apps, builtins, order []string) {
	e.t.Helper()
	w := e.do(user, "GET", "/api/apps", "")
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /api/apps as %s: %d %s", user, w.Code, w.Body.String())
	}
	var out struct {
		Apps []struct {
			ID string `json:"id"`
		} `json:"apps"`
		Builtin []string `json:"builtin_apps"`
		Order   []string `json:"order"`
	}
	mustDecode(e.t, w, &out)
	for _, a := range out.Apps {
		apps = append(apps, a.ID)
	}
	return apps, out.Builtin, out.Order
}

// ---------- 1. nothing configured: today's behaviour ----------

func TestAppsWithNoRowsEverythingIsOpenInTodaysOrder(t *testing.T) {
	e := newAppsEnv(t)
	for _, user := range []string{"rita", "admin"} {
		apps, builtins, order := e.listed(user)
		if !slices.Equal(builtins, []string{"batch", "recurring", "quotes"}) {
			t.Errorf("%s builtins = %v", user, builtins)
		}
		if len(apps) != 2 || !slices.Contains(apps, "alpha") || !slices.Contains(apps, "beta") {
			t.Errorf("%s apps = %v", user, apps)
		}
		if len(order) != 5 || !slices.Equal(order[:3], []string{"builtin:batch", "builtin:recurring", "builtin:quotes"}) {
			t.Errorf("%s order = %v: built-in apps first, then installed ones as ListApps returns them", user, order)
		}
	}
}

// ---------- 2. resolution ----------

func TestAppAccessResolution(t *testing.T) {
	e := newAppsEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	grandchild := seedOU(t, e.s.st, child, nil)
	mustAddUser(t, e.s, "gail", false)
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username=?`, grandchild, "gail"); err != nil {
		t.Fatal(err)
	}
	open := func(user, key string) bool {
		t.Helper()
		v, err := e.s.st.appAccessFor(user)
		if err != nil {
			t.Fatal(err)
		}
		return v.open(key)
	}

	// inherit: closed on the parent reaches the grandchild
	e.mustSetAccess("alpha", e.ou, "false")
	if open("rita", "alpha") || open("gail", "alpha") {
		t.Fatal("a closed parent must close its descendants that do not override")
	}
	if !open("rita", "beta") {
		t.Fatal("closing alpha closed beta")
	}
	// override at a child: the nearest explicit row wins
	e.mustSetAccess("alpha", child, "true")
	if open("rita", "alpha") != false {
		t.Fatal("the child's override leaked up to the parent")
	}
	if !open("gail", "alpha") {
		t.Fatal("the child's open override must reach the grandchild")
	}
	e.mustSetAccess("alpha", grandchild, "false")
	if open("gail", "alpha") {
		t.Fatal("the grandchild's own closed row must win")
	}
	// null removes the override, back to inheriting from the child
	e.mustSetAccess("alpha", grandchild, "null")
	if !open("gail", "alpha") {
		t.Fatal("removing the override must inherit from the child again")
	}
	// built-in keys resolve the same way
	e.mustSetAccess("builtin:quotes", e.ou, "false")
	if open("gail", "builtin:quotes") != false || !open("gail", "builtin:batch") {
		t.Fatal("built-in key resolution is wrong")
	}
	// the Default OU is the root: a row there reaches an OU with no row of its own
	def := e.s.st.DefaultGroupID()
	e.mustSetAccess("beta", def, "false")
	if open("rita", "beta") {
		t.Fatal("a closed Default OU must close every OU below it")
	}
	// administrators are exempt from every row
	if !open("admin", "alpha") || !open("admin", "beta") || !open("admin", "builtin:quotes") {
		t.Fatal("administrators must always see every app")
	}
	// a Default row set back to null means open
	e.mustSetAccess("beta", def, "null")
	if !open("rita", "beta") {
		t.Fatal("the Default OU with no row is open")
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key='beta'`); n != 0 {
		t.Fatalf("null left %d rows for beta", n)
	}
}

func TestAppAccessRowsAreNotRequiredForAnUnassignedUser(t *testing.T) {
	e := newAppsEnv(t)
	mustAddUser(t, e.s, "una", false) // no OU: the Default OU governs
	e.mustSetAccess("alpha", e.s.st.DefaultGroupID(), "false")
	if apps, _, _ := e.listed("una"); slices.Contains(apps, "alpha") {
		t.Fatalf("an unassigned user is governed by the Default OU: %v", apps)
	}
}

// ---------- 3. enforcement ----------

func TestClosedInstalledAppRefusesTokenAndAssetsAndLeavesTheListing(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")

	apps, _, order := e.listed("rita")
	if slices.Contains(apps, "alpha") || slices.Contains(order, "alpha") || !slices.Contains(apps, "beta") {
		t.Fatalf("listing for rita: apps %v order %v", apps, order)
	}
	// token mint
	w := e.do("rita", "POST", "/api/apps/alpha/token", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("token mint for a closed app = %d %s", w.Code, w.Body.String())
	}
	if body := decodeBody(t, w); body["error"] != "app_closed" || body["code"] != "app_closed" {
		t.Fatalf("body = %v, want error and code app_closed", body)
	}
	// asset / file serving, for a request the server can attribute
	w = e.do("rita", "GET", "/app-assets/alpha/index.html", "")
	if w.Code != http.StatusForbidden || decodeBody(t, w)["error"] != "app_closed" {
		t.Fatalf("asset for a closed app = %d %s", w.Code, w.Body.String())
	}
	// the open app and other users are unaffected
	if w := e.do("rita", "POST", "/api/apps/beta/token", ""); w.Code != http.StatusOK {
		t.Fatalf("token for an open app = %d %s", w.Code, w.Body.String())
	}
	if w := e.do("rita", "GET", "/app-assets/beta/index.html", ""); w.Code != http.StatusOK {
		t.Fatalf("asset for an open app = %d", w.Code)
	}
	// bob has no OU, so the Default OU governs him; it has no row, so alpha is open for him.
	if w := e.do("bob", "POST", "/api/apps/alpha/token", ""); w.Code != http.StatusOK {
		t.Fatalf("bob (Default OU, no row) = %d", w.Code)
	}
	// administrators are exempt
	if w := e.do("admin", "POST", "/api/apps/alpha/token", ""); w.Code != http.StatusOK {
		t.Fatalf("an administrator must still open a closed app: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("admin", "GET", "/app-assets/alpha/index.html", ""); w.Code != http.StatusOK {
		t.Fatalf("an administrator must still be served a closed app: %d", w.Code)
	}
	apps, _, _ = e.listed("admin")
	if !slices.Contains(apps, "alpha") {
		t.Fatalf("admin listing = %v", apps)
	}
	// reopening restores access
	e.mustSetAccess("alpha", e.ou, "null")
	if w := e.do("rita", "POST", "/api/apps/alpha/token", ""); w.Code != http.StatusOK {
		t.Fatalf("token after reopening = %d", w.Code)
	}
}

func TestAnAnonymousAssetRequestIsStillServed(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	// Sub-resource requests from the sandboxed frame carry no cookie and cannot be attributed to an
	// OU (ADR 0048). The mint is the gate.
	if w := e.do("", "GET", "/app-assets/alpha/index.html", ""); w.Code != http.StatusOK {
		t.Fatalf("anonymous asset = %d", w.Code)
	}
}

func TestClosedBuiltinKeyIsReportedNotServerGated(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("builtin:quotes", e.ou, "false")
	_, builtins, order := e.listed("rita")
	if slices.Contains(builtins, "quotes") || slices.Contains(order, "builtin:quotes") || !slices.Contains(builtins, "batch") {
		t.Fatalf("builtins %v order %v", builtins, order)
	}
	_, builtins, _ = e.listed("admin")
	if !slices.Contains(builtins, "quotes") {
		t.Fatal("admin must see every built-in app")
	}
}

// ---------- 4. order ----------

func TestAppOrderPutValidatesAndReplaces(t *testing.T) {
	e := newAppsEnv(t)
	for name, body := range map[string]string{
		"unknown key":   `{"keys":["alpha","nope"]}`,
		"unknown built": `{"keys":["builtin:nope"]}`,
		"duplicate":     `{"keys":["alpha","alpha"]}`,
		"no keys":       `{}`,
		"not a list":    `{"keys":"alpha"}`,
	} {
		if w := e.do("admin", "PUT", "/api/admin/apps/order", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order`); n != 0 {
		t.Fatalf("a refused order wrote %d rows", n)
	}
	if w := e.do("rita", "PUT", "/api/admin/apps/order", `{"keys":["alpha"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("a reader changed the order: %d", w.Code)
	}

	w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["beta","builtin:quotes","alpha"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("put order: %d %s", w.Code, w.Body.String())
	}
	_, _, order := e.listed("rita")
	want := []string{"beta", "builtin:quotes", "alpha", "builtin:batch", "builtin:recurring"}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v: ranked apps first by position, the rest after in today's order", order, want)
	}
	// full replacement: a shorter list drops the earlier rows
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["alpha"]}`); w.Code != http.StatusOK {
		t.Fatalf("second put: %d", w.Code)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order`); n != 1 {
		t.Fatalf("rows = %d, want a full replacement", n)
	}
	if _, _, order := e.listed("rita"); order[0] != "alpha" {
		t.Fatalf("order = %v", order)
	}
	// an empty list clears the ordering: back to today's order
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order`); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestAdminLayoutIsOrderedAndShowsOwnEffectiveAndSource(t *testing.T) {
	e := newAppsEnv(t)
	child := seedOU(t, e.s.st, e.ou, nil)
	e.mustSetAccess("alpha", e.ou, "false")
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["alpha","beta"]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w := e.do("admin", "GET", "/api/admin/apps/layout", "")
	if w.Code != http.StatusOK {
		t.Fatalf("layout: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Apps []appLayoutEntry `json:"apps"`
	}
	mustDecode(t, w, &out)
	var keys []string
	for _, a := range out.Apps {
		keys = append(keys, a.Key)
	}
	if !slices.Equal(keys, []string{"alpha", "beta", "builtin:batch", "builtin:recurring", "builtin:quotes"}) {
		t.Fatalf("keys = %v", keys)
	}
	if out.Apps[0].Name != "ALPHA" || out.Apps[0].Builtin || !out.Apps[2].Builtin {
		t.Fatalf("names/builtin flags: %+v", out.Apps)
	}
	find := func(a appLayoutEntry, ou int64) appAccessOU {
		for _, x := range a.Access {
			if x.OU == ou {
				return x
			}
		}
		t.Fatalf("no access entry for OU %d in %+v", ou, a.Access)
		return appAccessOU{}
	}
	own := find(out.Apps[0], e.ou)
	if own.Own == nil || *own.Own || own.Effective || own.SourceOU != e.ou || own.Inherited {
		t.Fatalf("own = %+v", own)
	}
	inh := find(out.Apps[0], child)
	if inh.Own != nil || inh.Effective || inh.SourceOU != e.ou || !inh.Inherited {
		t.Fatalf("inherited = %+v", inh)
	}
	base := find(out.Apps[1], child)
	if base.Own != nil || !base.Effective || base.SourceOU != 0 {
		t.Fatalf("baseline = %+v", base)
	}
	if w := e.do("rita", "GET", "/api/admin/apps/layout", ""); w.Code != http.StatusForbidden {
		t.Fatalf("a reader read the admin layout: %d", w.Code)
	}
}

// ---------- 5. access PUT ----------

func TestAppAccessPutValidation(t *testing.T) {
	e := newAppsEnv(t)
	for name, tc := range map[string]struct {
		key, ou, body string
		want          int
	}{
		"bad value":     {"alpha", itoa64(e.ou), `{"enabled":"yes"}`, http.StatusBadRequest},
		"missing field": {"alpha", itoa64(e.ou), `{}`, http.StatusBadRequest},
		"unknown app":   {"ghost", itoa64(e.ou), `{"enabled":false}`, http.StatusNotFound},
		"unknown built": {"builtin:ghost", itoa64(e.ou), `{"enabled":false}`, http.StatusNotFound},
		"unknown OU":    {"alpha", "999999", `{"enabled":false}`, http.StatusNotFound},
	} {
		if w := e.do("admin", "PUT", "/api/admin/apps/"+tc.key+"/access/"+tc.ou, tc.body); w.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", name, w.Code, w.Body.String(), tc.want)
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access`); n != 0 {
		t.Fatalf("refused requests wrote %d rows", n)
	}
	if w := e.do("rita", "PUT", "/api/admin/apps/alpha/access/"+itoa64(e.ou), `{"enabled":false}`); w.Code != http.StatusForbidden {
		t.Fatalf("a reader changed access: %d", w.Code)
	}
}

func TestAppAccessPutIsTriStateAndReplacesTheRow(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	e.mustSetAccess("alpha", e.ou, "false")
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access`); n != 1 {
		t.Fatalf("rows = %d, want one row per (app, OU)", n)
	}
	e.mustSetAccess("alpha", e.ou, "true")
	if n := scalar[int](t, e.s.st, `SELECT enabled FROM app_ou_access WHERE app_key='alpha' AND group_id=?`, e.ou); n != 1 {
		t.Fatalf("enabled = %d", n)
	}
	e.mustSetAccess("alpha", e.ou, "null")
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access`); n != 0 {
		t.Fatalf("null left %d rows", n)
	}
	e.mustSetAccess("alpha", e.ou, "null") // removing what is not there is not an error
}

func TestAppChangesAreAudited(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	e.mustSetAccess("alpha", e.ou, "false") // unchanged: no second row
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["beta","alpha"]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, AuditAppAccess); n != 1 {
		t.Fatalf("app.access rows = %d, want exactly the one real change", n)
	}
	detail := scalar[string](t, e.s.st, `SELECT detail FROM audit_log WHERE action=?`, AuditAppAccess)
	for _, want := range []string{`"field":"access"`, `"from":null`, `"to":false`} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail %s lacks %s", detail, want)
		}
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, AuditAppOrder); n != 1 {
		t.Fatalf("app.order rows = %d", n)
	}
}

// ---------- 6. cleanup ----------

func TestUninstallingAnAppDeletesItsRows(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	e.mustSetAccess("beta", e.ou, "false")
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["alpha","beta"]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := e.do("admin", "DELETE", "/api/admin/apps/alpha", ""); w.Code != http.StatusOK {
		t.Fatalf("uninstall: %d %s", w.Code, w.Body.String())
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key='alpha'`); n != 0 {
		t.Fatalf("access rows left: %d", n)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order WHERE app_key='alpha'`); n != 0 {
		t.Fatalf("order rows left: %d", n)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key='beta'`) != 1 ||
		scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order WHERE app_key='beta'`) != 1 {
		t.Fatal("uninstalling alpha touched beta's rows")
	}
}

func TestReinstallingAnAppKeepsItsRows(t *testing.T) {
	e := newAppsEnv(t)
	e.mustSetAccess("alpha", e.ou, "false")
	e.install("alpha") // an upgrade replaces the bundle in place; it is the same app
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key='alpha'`); n != 1 {
		t.Fatalf("a re-install dropped the access rows: %d", n)
	}
}

func TestDeletingAnOUDeletesItsAccessRows(t *testing.T) {
	e := newAppsEnv(t)
	other := seedOU(t, e.s.st, 0, nil)
	empty := seedOU(t, e.s.st, 0, nil) // an OU with no members, which is what may be deleted
	e.mustSetAccess("alpha", empty, "false")
	e.mustSetAccess("alpha", other, "false")
	if _, _, err := e.s.st.DeleteUserGroup(empty); err != nil {
		t.Fatal(err)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE group_id=?`, empty); n != 0 {
		t.Fatalf("rows for the deleted OU left: %d", n)
	}
	if n := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE group_id=?`, other); n != 1 {
		t.Fatal("deleting one OU removed another's row")
	}
}

// ---------- 7. pure ordering ----------

func TestOrderAppKeys(t *testing.T) {
	keys := []string{"builtin:batch", "builtin:quotes", "a", "b"}
	if got := orderAppKeys(keys, nil); !slices.Equal(got, keys) {
		t.Fatalf("no rows = %v", got)
	}
	got := orderAppKeys(keys, map[string]int{"b": 1, "builtin:quotes": 2, "gone": 0})
	if want := []string{"b", "builtin:quotes", "builtin:batch", "a"}; !slices.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func mustDecode(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
}
