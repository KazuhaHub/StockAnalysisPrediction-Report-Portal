package app

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Feature switches on feature-owned routes (docs/app-platform/190, D2, D2a, A6, A6a). The switch is
// the per-OU access row of a built-in key; the server refuses the routes only that feature uses.

const (
	featureBatch     = "builtin:batch"
	featureRecurring = "builtin:recurring"
)

// Only Scheduled tasks owns server routes. Batch and Quotes share theirs with the run dialog
// (RunAnalysisModal) and the queue (QueueTable, /queue), so closing them only hides the entry.

type featureRoute struct{ method, path string }

var recurringOwnedRoutes = []featureRoute{
	{"GET", "/api/admin/batch/recurring"},
	{"POST", "/api/admin/batch/recurring"},
	{"GET", "/api/admin/batch/recurring/1"},
	{"PUT", "/api/admin/batch/recurring/1"},
	{"POST", "/api/admin/batch/recurring/1/enable"},
	{"POST", "/api/admin/batch/recurring/1/run"},
	{"DELETE", "/api/admin/batch/recurring/1"},
}

// Called by shared web components (callers named in TestNoRouteTheWebSharesCarriesAFeatureGate), so
// never gated: the run dialog reads tickets and run-quota, the queue table posts retry/priority/schedule.
var batchSharedRoutes = []featureRoute{
	{"GET", "/api/admin/batch/tickets"},
	{"GET", "/api/admin/batch/run-quota"},
	{"POST", "/api/admin/batch/jobs/1/retry"},
	{"POST", "/api/admin/batch/jobs/1/priority"},
	{"POST", "/api/admin/batch/jobs/1/schedule"},
}

// Other shared routes. (The quote routes carry no gate either; the route-table test pins the exact
// gated set.)
var sharedBatchRoutes = []featureRoute{
	{"GET", "/api/admin/batch/targets"},
	{"GET", "/api/admin/batch/presets"},
	{"GET", "/api/admin/batch/queue"},
	{"GET", "/api/admin/batch/jobs"},
	{"GET", "/api/admin/batch/jobs/1"},
	{"POST", "/api/admin/batch/jobs"},
	{"POST", "/api/admin/batch/jobs/1/cancel"},
}

func (e *appsEnv) assertAppClosed(user string, rt featureRoute, want bool) {
	e.t.Helper()
	w := e.do(user, rt.method, rt.path, "{}")
	closed := w.Code == http.StatusForbidden && decodeBody(e.t, w)["code"] == "app_closed"
	if closed != want {
		e.t.Errorf("%s %s %s: app_closed=%v, want %v (%d %s)", user, rt.method, rt.path, closed, want, w.Code, strings.TrimSpace(w.Body.String()))
	}
}

func (e *appsEnv) putUserInOU(user string) {
	e.t.Helper()
	if _, err := e.s.st.exec(`UPDATE users SET group_id=? WHERE username=?`, e.ou, user); err != nil {
		e.t.Fatal(err)
	}
}

func TestFeatureSwitchGatesOnlyFeatureOwnedRoutes(t *testing.T) {
	t.Parallel()
	e := newAppsEnv(t)
	if err := e.s.st.UpsertUser(User{Username: "olive", PasswordHash: "hash", Role: "operator", Active: true}); err != nil {
		t.Fatal(err)
	}
	e.putUserInOU("olive")
	e.putUserInOU("admin")
	shared := append(append([]featureRoute{}, batchSharedRoutes...), sharedBatchRoutes...)

	// Nothing closed: nothing is gated.
	for _, rt := range append(append([]featureRoute{}, recurringOwnedRoutes...), shared...) {
		e.assertAppClosed("olive", rt, false)
	}

	e.mustSetAccess(featureRecurring, e.ou, "false")
	for _, user := range []string{"olive", "admin"} { // administrators are bound too (D2a)
		for _, rt := range recurringOwnedRoutes {
			e.assertAppClosed(user, rt, true)
		}
		for _, rt := range shared {
			e.assertAppClosed(user, rt, false)
		}
	}

	// Batch and Quotes own no route: closing them (and everything else) gates nothing on the server.
	e.mustSetAccess(featureRecurring, e.ou, "null")
	e.mustSetAccess(featureBatch, e.ou, "false")
	e.mustSetAccess("builtin:quotes", e.ou, "false")
	for _, user := range []string{"olive", "admin"} {
		for _, rt := range append(append([]featureRoute{}, recurringOwnedRoutes...), shared...) {
			e.assertAppClosed(user, rt, false)
		}
	}
}

func TestFeatureGateKeepsTheRouteGuardInFront(t *testing.T) {
	t.Parallel()
	e := newAppsEnv(t)
	e.mustSetAccess(featureRecurring, e.ou, "false")
	e.putUserInOU("bob")
	// bob (role user) has no run permission: refused as forbidden, not as a closed app.
	if w := e.do("bob", "GET", "/api/admin/batch/recurring", ""); w.Code != http.StatusForbidden || decodeBody(t, w)["code"] == "app_closed" {
		t.Fatalf("a caller without the permission must meet the permission guard first: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("", "GET", "/api/admin/batch/recurring", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous = %d", w.Code)
	}
}

func TestFeatureGatedRoutesAreAnnotatedInTheRouteTable(t *testing.T) {
	t.Parallel()
	s := routeTableServer(t)
	s.wireRoutes(http.NewServeMux())
	got := map[string]string{}
	for _, row := range s.routeTable {
		if strings.HasPrefix(row.gate, gateFeaturePrefix) {
			got[row.pattern] = strings.TrimPrefix(row.gate, gateFeaturePrefix)
		}
	}
	want := map[string]string{}
	for _, rt := range recurringOwnedRoutes {
		want[rt.method+" "+strings.Replace(rt.path, "/1", "/{id}", 1)] = featureRecurring
	}
	if len(got) != len(want) {
		t.Fatalf("gated routes = %v, want %v", got, want)
	}
	for p, k := range want {
		if got[p] != k {
			t.Errorf("%s gate = %q, want %q", p, got[p], k)
		}
	}
}

// Which pages call the gated routes is checked on the web side (web/src/featureGateCallers.test.ts),
// so a web-only change never needs the Go suite.

func TestAnUnreadableSwitchPolicyAnswers503(t *testing.T) {
	t.Parallel()
	e := newAppsEnv(t)
	e.putUserInOU("admin")
	e.mustSetAccess(featureRecurring, e.ou, "false")
	if _, err := e.s.st.exec(`DROP TABLE app_ou_access`); err != nil {
		t.Fatal(err)
	}
	if w := e.do("admin", "GET", "/api/admin/batch/recurring", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable policy = %d, want 503", w.Code)
	}
}

// A6a: an administrator whose OU and the Default OU have every built-in key closed is refused on a
// feature route, yet can still read the layout, reach the OU policy pages and open the switch again.
func TestAdministratorCannotBeLockedOutOfTheSwitches(t *testing.T) {
	t.Parallel()
	runLockoutGuard(t, newAppsEnv(t))
}

// The same guard on the driver production uses. Skipped unless TEST_POSTGRES_DSN is set. The tables it
// touches are emptied before and after, because the Postgres database is shared by the whole suite.
func TestPostgresAdministratorCannotBeLockedOutOfTheSwitches(t *testing.T) {
	t.Parallel()
	tables := []string{"app_ou_access", "app_order", "app_files", "apps", "users", "user_groups"}
	st := pgStore(t, tables...)
	t.Cleanup(func() {
		for _, tbl := range tables {
			st.exec("DELETE FROM " + tbl)
		}
	})
	st.EnsureDefaultGroup()
	s := tokenServer(t)
	s.st = st
	s.cfg.DBPath = filepath.Join(t.TempDir(), "portal.db")
	e := &appsEnv{exportEnv: finishExportEnv(t, s)}
	e.s.appTok = newAppTokens(appTokenTTL)
	e.ou = seedOU(t, st, 0, nil)
	mustAddUser(t, e.s, "rita", false)
	e.putUserInOU("rita")
	e.install("alpha")
	e.install("beta")
	runLockoutGuard(t, e)
}

func runLockoutGuard(t *testing.T, e *appsEnv) {
	t.Helper()
	e.putUserInOU("admin")
	def := e.s.st.DefaultGroupID()
	keys := []string{"builtin:quotes", featureBatch, featureRecurring, "alpha", "beta"}
	for _, ou := range []int64{e.ou, def} {
		for _, k := range keys {
			e.mustSetAccess(k, ou, "false")
		}
	}
	e.assertAppClosed("admin", featureRoute{"GET", "/api/admin/batch/recurring"}, true)

	// An installed v1 app closed for the administrator still reaches the console in full detail.
	if err := e.s.st.InstallApp(App{ID: "gamma", Name: "Gamma", Icon: "G", Version: "1.2.3", Entry: "main.html", Scopes: []string{"query", "notify"}},
		map[string]AppFile{"main.html": {Ctype: "text/html", Content: []byte("<h1>g</h1>")}}); err != nil {
		t.Fatal(err)
	}
	for _, ou := range []int64{e.ou, def} {
		e.mustSetAccess("gamma", ou, "false")
	}
	if apps, _, _ := e.listed("admin"); slices.Contains(apps, "gamma") {
		t.Fatalf("the reader listing still shows a closed app to an administrator: %v", apps)
	}
	var layout struct {
		Apps []appLayoutEntry `json:"apps"`
	}
	mustDecode(t, e.do("admin", "GET", "/api/admin/apps/layout", ""), &layout)
	found := false
	for _, a := range layout.Apps {
		if a.Key == "gamma" {
			found = true
			if a.Version != "1.2.3" || a.Entry != "main.html" || a.Icon != "G" || a.Name != "Gamma" || !slices.Equal(a.Scopes, []string{"notify", "query"}) {
				t.Fatalf("admin layout detail = %+v", a)
			}
		}
	}
	if !found {
		t.Fatal("the admin layout dropped a closed installed app")
	}

	if w := e.do("admin", "GET", "/api/admin/apps/layout", ""); w.Code != http.StatusOK {
		t.Fatalf("layout under a full lock-out = %d %s", w.Code, w.Body.String())
	}
	if w := e.do("admin", "PUT", "/api/admin/apps/order", `{"keys":["alpha"]}`); w.Code != http.StatusOK {
		t.Fatalf("order under a full lock-out = %d", w.Code)
	}
	if w := e.do("admin", "GET", "/api/admin/groups", ""); w.Code != http.StatusOK {
		t.Fatalf("OU policy pages under a full lock-out = %d %s", w.Code, w.Body.String())
	}
	e.mustSetAccess(featureRecurring, e.ou, "true")
	if w := e.do("admin", "GET", "/api/admin/batch/recurring", ""); w.Code != http.StatusOK {
		t.Fatalf("recurring after reopening = %d %s", w.Code, w.Body.String())
	}
}

// Closing Scheduled tasks governs interactive access only: existing schedules keep running.
func TestRecurringTickIgnoresTheSwitch(t *testing.T) {
	t.Parallel()
	st := newTestStore(t)
	tgt := seedTarget(t, st)
	s := okProviderServer(st)
	seedRecurring(t, st, tgt, "alice", RecurringTask{Name: "T", Freq: "daily", AtTime: "00:00", Enabled: true})
	if _, err := st.exec(`INSERT INTO app_ou_access(app_key,group_id,enabled) VALUES(?,?,0)`, featureRecurring, st.DefaultGroupID()); err != nil {
		t.Fatal(err)
	}
	if open, err := st.appOpenForKey("alice", featureRecurring); err != nil || open {
		t.Fatalf("precondition: recurring must be closed for alice (open=%v err=%v)", open, err)
	}
	s.recurringTick()
	if n := countRows(t, st, "batch_jobs"); n != 1 {
		t.Fatalf("a closed switch stopped an existing schedule: batch_jobs = %d, want 1", n)
	}
}
