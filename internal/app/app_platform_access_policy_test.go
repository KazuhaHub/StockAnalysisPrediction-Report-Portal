package app

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Platform installations are governed by the OU policy of ADR 0048 (doc 190, D7): key
// platform:<installation_id> in app_ou_access, a closed baseline, no per-user grants, and the legacy
// app_platform_access_grants table neither read nor written.

type platformAccessEnv struct {
	t            *testing.T
	s            *Server
	mux          *http.ServeMux
	reader       platformActorSession
	admin        platformActorSession
	id           string
	adminCookie  string
	readerCookie string
}

func newPlatformAccessEnv(t *testing.T, pg bool) *platformAccessEnv {
	t.Helper()
	s, reader, id := platformLaunchFixture(t, pg)
	if err := s.st.UpsertUser(User{Username: "pa-admin", PasswordHash: "fixture", Role: "admin", Active: true}); err != nil {
		t.Fatal(err)
	}
	admin := platformReportSession(t, s, "pa-admin")
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return &platformAccessEnv{
		t: t, s: s, mux: mux, reader: reader, admin: admin, id: id,
		adminCookie:  s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix()),
		readerCookie: s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix()),
	}
}

func (e *platformAccessEnv) defaultOU() int64 {
	e.t.Helper()
	return platformOpenTargets(e.t, e.s)[0].ouID
}

// surfaces is every place that decides whether a user may use the installation.
type platformSurfaces struct{ launch, directory, status bool }

func (e *platformAccessEnv) surfaces(session platformActorSession) platformSurfaces {
	e.t.Helper()
	var out platformSurfaces
	_, err := e.s.inspectPlatformLaunchAccess(session, e.id)
	out.launch = err == nil
	page, err := e.s.listPlatformApps(context.Background(), session, "")
	if err != nil {
		e.t.Fatal("directory", err)
	}
	for _, item := range page.Items {
		if item.InstallationID == e.id {
			out.directory = true
		}
	}
	_, err = e.s.readPlatformInstallationStatus(context.Background(), session, e.id)
	out.status = err == nil
	return out
}

func (e *platformAccessEnv) request(cookie, method, path, body string) string {
	e.t.Helper()
	w := sessionTestRequest(e.mux, method, path, cookie, body)
	if w.Code != http.StatusOK {
		e.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

// A7: a new installation is closed for everyone, administrators included; built-in features with no
// rows stay open; an edit through the admin endpoint opens it at once; nothing touches the legacy table.
func TestPlatformInstallationStartsClosedForEveryoneAndOpensByPolicyEdit(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testPlatformInstallationStartsClosedForEveryoneAndOpensByPolicyEdit)
}

func testPlatformInstallationStartsClosedForEveryoneAndOpensByPolicyEdit(t *testing.T, pg bool) {
	e := newPlatformAccessEnv(t, pg)
	for _, who := range []platformActorSession{e.reader, e.admin} {
		if got := e.surfaces(who); got != (platformSurfaces{}) {
			t.Fatalf("%s: a new installation is open: %+v", who.username, got)
		}
		if open, err := e.s.st.appOpenForKey(who.username, platformAppKey(e.id)); err != nil || open {
			t.Fatalf("%s: platform baseline must be closed: %v %v", who.username, open, err)
		}
		for _, key := range []string{"builtin:batch", "builtin:recurring", "builtin:quotes"} {
			if open, err := e.s.st.appOpenForKey(who.username, key); err != nil || !open {
				t.Fatalf("%s: built-in %s with no rows must stay open: %v %v", who.username, key, open, err)
			}
		}
	}
	// The admin endpoint accepts a platform key and audits it under the existing action.
	path := "/api/admin/apps/" + platformAppKey(e.id) + "/access/" + strconv.FormatInt(e.defaultOU(), 10)
	e.request(e.adminCookie, "PUT", path, `{"enabled":true}`)
	for _, who := range []platformActorSession{e.reader, e.admin} {
		if got := e.surfaces(who); got != (platformSurfaces{true, true, true}) {
			t.Fatalf("%s: opened installation is not usable everywhere: %+v", who.username, got)
		}
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app.access' AND target_id=?`, platformAppKey(e.id)) != 1 {
		t.Fatal("the platform access change was not audited as app.access")
	}
	e.request(e.adminCookie, "PUT", path, `{"enabled":null}`)
	if got := e.surfaces(e.reader); got != (platformSurfaces{}) {
		t.Fatalf("removing the row did not close the installation: %+v", got)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("the legacy audience table was written")
	}
}

// A7: an OU opened at install time is open the moment activation commits, and only that OU's members.
func TestPlatformInstallOpenToOUIsOpenRightAfterActivation(t *testing.T) {
	t.Parallel()
	s, actor, release := platformInstallFixture(t, false)
	actor0 := actor.username
	group, err := s.st.CreateUserGroup("Open at install", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.SetPrimaryGroups([]string{actor.username}, group); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.st.SetPrimaryGroups([]string{actor.username}, 0) //nolint:errcheck // cleanup
		s.st.DeleteUserGroup(group)                        //nolint:errcheck // cleanup
	})
	review, err := s.createPlatformNativeInstallAudienceReview(actor, release.sha256, 1, []platformAudienceTarget{{kind: "ou", ouID: group}})
	if err != nil {
		t.Fatal(err)
	}
	s, _, lease := platformFinishActivationFixture(t, s, actor, review)
	other := platformActivationReader(t, s, "install-open-other")
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err == nil {
		t.Fatal("pending installation opened before activation")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key=?`, platformAppKey(lease.installationID)) != 0 {
		t.Fatal("rows were written before activation")
	}
	if _, err := s.activatePlatformStaticInstall(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, lease.installationID); err != nil {
		t.Fatal("a member of the OU opened at install cannot use the app:", err)
	}
	if _, err := s.inspectPlatformLaunchAccess(other, lease.installationID); err == nil {
		t.Fatal("a user outside the OU opened at install can use the app")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key=? AND group_id=? AND enabled=1`, platformAppKey(lease.installationID), group) != 1 {
		t.Fatal("activation did not write exactly the reviewed OU row")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("activation wrote the legacy audience table")
	}
	// Each row activation writes is audited as app.access by the administrator who reviewed the plan.
	var auditActor, detail string
	if err := s.st.queryRow(`SELECT actor,detail FROM audit_log WHERE action='app.access' AND target_id=?`, platformAppKey(lease.installationID)).Scan(&auditActor, &detail); err != nil {
		t.Fatal("activation wrote no app.access audit row:", err)
	}
	if auditActor != actor0 || !strings.Contains(detail, `"field":"access"`) || !strings.Contains(detail, `"to":true`) || !strings.Contains(detail, `"from":null`) {
		t.Fatalf("activation audit = %q %s", auditActor, detail)
	}
}

// A7a: launch, directory and status agree for the same user across the policy scenarios. The
// principal directory joins them in TestPlatformPrincipalDirectoryAgreesWithLaunch.
func TestPlatformLaunchDirectoryAndStatusAgreeAcrossPolicyScenarios(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testPlatformLaunchDirectoryAndStatusAgreeAcrossPolicyScenarios)
}

func testPlatformLaunchDirectoryAndStatusAgreeAcrossPolicyScenarios(t *testing.T, pg bool) {
	e := newPlatformAccessEnv(t, pg)
	parent, err := e.s.st.CreateUserGroup("Agree parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.s.st.CreateUserGroup("Agree child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, e.s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	t.Cleanup(func() {
		e.s.st.SetPrimaryGroups([]string{e.reader.username}, 0) //nolint:errcheck // cleanup
		e.s.st.DeleteUserGroup(child)                           //nolint:errcheck // cleanup
		e.s.st.DeleteUserGroup(parent)                          //nolint:errcheck // cleanup
	})
	move := func(ou int64) {
		if _, err := e.s.st.SetPrimaryGroups([]string{e.reader.username}, ou); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(step string, want bool) {
		t.Helper()
		got := e.surfaces(e.reader)
		if got != (platformSurfaces{want, want, want}) {
			t.Fatalf("%s: surfaces = %+v, want all %v", step, got, want)
		}
	}
	move(child)
	expect("no row anywhere", false)
	platformSetAccess(t, e.s, e.id, parent, true)
	expect("parent open, child inherits", true)
	platformSetAccess(t, e.s, e.id, child, false)
	expect("child overrides closed", false)
	mustExec(t, e.s.st, `DELETE FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(e.id), child)
	expect("override removed, inherits again", true)
	move(0)
	expect("user moved to the Default OU", false)
	platformOpenDefault(t, e.s, e.id)
	expect("user without an OU is governed by the Default OU", true)
	move(child)
	platformCloseAccess(t, e.s, e.id)
	platformSetAccess(t, e.s, e.id, child, true)
	expect("OU opened directly", true)
	move(0)
	if _, _, err := e.s.st.DeleteUserGroup(child); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(e.id), child) != 0 {
		t.Fatal("deleting an OU left its platform access row")
	}
	expect("OU deleted: the user falls to the Default OU, which has no row", false)
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
		t.Fatal("the legacy audience table was written")
	}
}

// A7a: for every user the principal directory lists exactly the users launch admits, across scenarios.
func TestPlatformPrincipalDirectoryAgreesWithLaunch(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testPlatformPrincipalDirectoryAgreesWithLaunch)
}

func testPlatformPrincipalDirectoryAgreesWithLaunch(t *testing.T, pg bool) {
	p := platformPrincipalEnvironment(t, pg)
	label := map[string]string{"alice": "Alice Analyst", "bob": "bob", "carol": "carol", "dave": "dave", p.admin.username: p.admin.username}
	sessions := map[string]platformActorSession{p.admin.username: p.admin}
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		sessions[name] = platformReportSession(t, p.s, name)
	}
	check := func(step string, want ...string) {
		t.Helper()
		listed := p.searchLabels(t, "user")
		page := map[string]bool{}
		for name, session := range sessions {
			_, launchErr := p.s.inspectPlatformLaunchAccess(session, p.installation)
			dir, err := p.s.listPlatformApps(context.Background(), session, "")
			if err != nil {
				t.Fatal(step, err)
			}
			inDirectory := len(dir.Items) == 1 && dir.Items[0].InstallationID == p.installation
			_, statusErr := p.s.readPlatformInstallationStatus(context.Background(), session, p.installation)
			inPrincipals := slices.Contains(listed, label[name])
			if (launchErr == nil) != inPrincipals || inDirectory != inPrincipals || (statusErr == nil) != inPrincipals {
				t.Fatalf("%s: %s disagrees: launch=%v directory=%v status=%v principals=%v", step, name, launchErr == nil, inDirectory, statusErr == nil, inPrincipals)
			}
			if inPrincipals {
				page[name] = true
			}
		}
		got := make([]string, 0, len(page))
		for name := range page {
			got = append(got, name)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: admitted %v, want %v", step, got, want)
		}
	}
	admin := p.admin.username
	check("OU open, child inherits", "alice", "bob", "dave", admin)
	platformSetAccess(t, p.s, p.installation, p.groups["Research Lab"], false)
	check("child overrides closed", "alice", "dave", admin)
	mustExec(t, p.s.st, `DELETE FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(p.installation), p.groups["Research Lab"])
	mustExec(t, p.s.st, `UPDATE users SET group_id=? WHERE username='alice'`, p.groups["Other"])
	check("user moved to a closed OU", "bob", "dave", admin)
	platformSetAccess(t, p.s, p.installation, p.groups["Other"], true)
	check("second OU opened", "alice", "bob", "carol", "dave", admin)
	mustExec(t, p.s.st, `UPDATE users SET group_id=NULL WHERE group_id=?`, p.groups["Research Lab"])
	if _, _, err := p.s.st.DeleteUserGroup(p.groups["Research Lab"]); err != nil {
		t.Fatal(err)
	}
	check("OU deleted: its members fall to the Default OU", "alice", "carol", "dave", admin)
}

// A5: the admin list holds the built-in features and every platform installation in the saved order,
// the reader list follows the same order, and reorder accepts platform keys only for live installations.
func TestAppListsMergeBuiltinAndPlatformInOrder(t *testing.T) {
	t.Parallel()
	eachPlatformDriver(t, testAppListsMergeBuiltinAndPlatformInOrder)
}

func testAppListsMergeBuiltinAndPlatformInOrder(t *testing.T, pg bool) {
	e := newPlatformAccessEnv(t, pg)
	key := platformAppKey(e.id)
	layout := func() []struct {
		Key, Kind, Name, State string
		PublisherID            string `json:"publisher_id"`
		Access                 []struct {
			OU        int64 `json:"ou"`
			Effective bool  `json:"effective"`
		}
	} {
		var out struct {
			Apps []struct {
				Key, Kind, Name, State string
				PublisherID            string `json:"publisher_id"`
				Access                 []struct {
					OU        int64 `json:"ou"`
					Effective bool  `json:"effective"`
				}
			} `json:"apps"`
		}
		if err := json.Unmarshal([]byte(e.request(e.adminCookie, "GET", "/api/admin/apps/layout", "")), &out); err != nil {
			t.Fatal(err)
		}
		return out.Apps
	}
	keys := func() []string {
		var out []string
		for _, a := range layout() {
			out = append(out, a.Key)
		}
		return out
	}
	if got := keys(); !slices.Equal(got, []string{"builtin:batch", "builtin:recurring", "builtin:quotes", key}) {
		t.Fatalf("default order = %v", got)
	}
	for _, a := range layout() {
		if a.Key != key {
			continue
		}
		if a.Kind != "platform" || a.Name != "Notes" || a.State != "installed" || a.PublisherID != "fixture-publisher" {
			t.Fatalf("platform entry = %+v", a)
		}
		for _, row := range a.Access {
			if row.Effective {
				t.Fatalf("a new platform installation is open in OU %d", row.OU)
			}
		}
	}
	readerOrder := func() []string {
		var out struct {
			Order []string `json:"order"`
		}
		if err := json.Unmarshal([]byte(e.request(e.readerCookie, "GET", "/api/apps", "")), &out); err != nil {
			t.Fatal(err)
		}
		return out.Order
	}
	if slices.Contains(readerOrder(), key) {
		t.Fatal("the reader list shows a closed platform installation")
	}
	platformOpenDefault(t, e.s, e.id)
	e.request(e.adminCookie, "PUT", "/api/admin/apps/order", `{"keys":["`+key+`","builtin:quotes","builtin:batch","builtin:recurring"]}`)
	want := []string{key, "builtin:quotes", "builtin:batch", "builtin:recurring"}
	if got := keys(); !slices.Equal(got, want) {
		t.Fatalf("admin order after reorder = %v", got)
	}
	if got := readerOrder(); !slices.Equal(got, want) {
		t.Fatalf("reader order after reorder = %v, want the admin's %v", got, want)
	}
	// Unknown, malformed and removed keys are 400 and write nothing.
	before := scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order`)
	for _, bad := range []string{platformAppKey(platformFixtureID(77)), "platform:not-a-uuid", "platform:"} {
		w := sessionTestRequest(e.mux, "PUT", "/api/admin/apps/order", e.adminCookie, `{"keys":["`+bad+`"]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("order with %q: %d", bad, w.Code)
		}
	}
	mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, e.id)
	if w := sessionTestRequest(e.mux, "PUT", "/api/admin/apps/order", e.adminCookie, `{"keys":["`+key+`"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("order with a removed installation: %d", w.Code)
	}
	if w := sessionTestRequest(e.mux, "PUT", "/api/admin/apps/"+key+"/access/"+strconv.FormatInt(e.defaultOU(), 10), e.adminCookie, `{"enabled":true}`); w.Code != http.StatusNotFound {
		t.Fatalf("access for a removed installation: %d", w.Code)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_order`) != before {
		t.Fatal("a refused order wrote rows")
	}
	if got := keys(); slices.Contains(got, key) {
		t.Fatalf("a removed installation is still listed: %v", got)
	}
}

// The install plan names OUs only: the retired audience field and user principals are refused by code.
func TestPlatformInstallPlanOpenToOUsHTTP(t *testing.T) {
	t.Parallel()
	s, mux, _, release, cookie := platformManagementFixture(t, false)
	post := func(body string) (int, string) {
		w := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
		return w.Code, w.Body.String()
	}
	prefix := `{"action":"install","release_sha256":"` + release + `","trust_revision":"1",`
	if code, body := post(prefix + `"audience":[{"type":"user","username":"someone"}]}`); code != 400 || !strings.Contains(body, "app_platform_user_audience_unsupported") {
		t.Fatalf("user principal: %d %s", code, body)
	}
	for name, field := range map[string]string{
		"non-numeric": `"open_to_ous":["abc"]}`,
		"zero":        `"open_to_ous":["0"]}`,
		"number":      `"open_to_ous":[1]}`,
		"duplicate":   `"open_to_ous":["1","1"]}`,
		"missing":     `"open_to_ous":["987654"]}`,
	} {
		if code, _ := post(prefix + field); code < 400 {
			t.Fatalf("%s accepted: %d", name, code)
		}
	}
	if code, body := post(prefix + `"open_to_ous":[]}`); code != 200 || !strings.Contains(body, `"open_to_ous":[]`) {
		t.Fatalf("empty open_to_ous (closed for everyone) refused: %d %s", code, body)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key LIKE 'platform:%'`) != 0 {
		t.Fatal("planning wrote access rows")
	}
}

// Deleting an OU deletes its access rows and nothing else: it must not move the installation's policy
// revision, which would orphan the capability grants and refuse every user until a capability review.
func TestDeletingAnOUWithAPlatformRowKeepsTheInstallationLaunchable(t *testing.T) {
	t.Parallel()
	e := newPlatformAccessEnv(t, false)
	a, err := e.s.st.CreateUserGroup("Keep A", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.s.st.CreateUserGroup("Keep B", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.s.st.SetPrimaryGroups([]string{e.reader.username}, 0) //nolint:errcheck // cleanup
		e.s.st.DeleteUserGroup(a)                               //nolint:errcheck // cleanup
	})
	if _, err := e.s.st.SetPrimaryGroups([]string{e.reader.username}, a); err != nil {
		t.Fatal(err)
	}
	platformSetAccess(t, e.s, e.id, a, true)
	platformSetAccess(t, e.s, e.id, b, true)
	revision := scalar[int](t, e.s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, e.id)
	if _, _, err := e.s.st.DeleteUserGroup(b); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_ou_access WHERE app_key=? AND group_id=?`, platformAppKey(e.id), b) != 0 {
		t.Fatal("the deleted OU's row survived")
	}
	if scalar[int](t, e.s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, e.id) != revision {
		t.Fatal("deleting an OU moved the installation's policy revision")
	}
	if got := e.surfaces(e.reader); got != (platformSurfaces{true, true, true}) {
		t.Fatalf("a member of the remaining open OU lost access: %+v", got)
	}
}
