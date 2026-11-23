package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type appAdminEnv struct {
	t            *testing.T
	s            *Server
	mux          *http.ServeMux
	id           string
	admin        platformActorSession
	adminCookie  string
	reader       platformActorSession
	readerCookie string
}

// platformAdminPageFixture installs a static release; withPage gives it two admin routes. The audience
// stays closed, so nobody is a reader of it until a test opens it.
func platformAdminPageFixture(t *testing.T, withPage bool) *appAdminEnv {
	t.Helper()
	var edit func(*appplatform.Manifest)
	if withPage {
		edit = func(m *appplatform.Manifest) {
			m.Presentation.AdminRoutes = []appplatform.Route{{ID: "admin", Path: "/admin", Layout: "viewport"}, {ID: "admin-runs", Path: "/admin/runs", Layout: "document"}}
		}
	}
	s, admin, release := platformInstallFixtureWith(t, false, edit)
	s, reader, id := completePlatformLaunchFixture(t, s, admin, release.sha256)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	e := &appAdminEnv{t: t, s: s, mux: mux, id: id, admin: admin, reader: reader}
	e.adminCookie = s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	e.readerCookie = s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	return e
}

// newUser creates an active user with a session and returns its cookie.
func (e *appAdminEnv) newUser(name, role string) (platformActorSession, string) {
	e.t.Helper()
	if err := e.s.st.UpsertUser(User{Username: name, PasswordHash: "fixture", Role: role, Active: true}); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { e.s.st.DeleteUser(name) })
	account := e.s.st.GetUser(name)
	generation, err := notificationAccountGeneration(e.s.st.poolExec(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	session := platformActorSession{username: name, created: account.CreatedAt, sessionID: appAdminSessionID(name), sessionRevision: account.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(e.t, e.s.st, session)
	return session, e.s.managedCookie(*account, session.sessionID, session.expires.Unix())
}

func (e *appAdminEnv) call(method, path, cookie, body string) *httptest.ResponseRecorder {
	return sessionTestRequest(e.mux, method, path, cookie, body)
}
func (e *appAdminEnv) adminLaunch(cookie string) *httptest.ResponseRecorder {
	return e.call("GET", "/api/app-platform/installations/"+e.id+"/admin/launch", cookie, "")
}
func (e *appAdminEnv) readerLaunch(cookie string) *httptest.ResponseRecorder {
	return e.call("GET", "/api/app-platform/installations/"+e.id+"/launch", cookie, "")
}
func (e *appAdminEnv) appAdmins(method, cookie, body string) *httptest.ResponseRecorder {
	return e.call(method, "/api/admin/app-platform/installations/"+e.id+"/app-admins", cookie, body)
}
func (e *appAdminEnv) grant(cookie, username string) *httptest.ResponseRecorder {
	return e.appAdmins("POST", cookie, `{"username":"`+username+`"}`)
}
func (e *appAdminEnv) revoke(cookie, username string) *httptest.ResponseRecorder {
	return e.call("DELETE", "/api/admin/app-platform/installations/"+e.id+"/app-admins/"+username, cookie, "")
}
func (e *appAdminEnv) audits(action string) int {
	return scalar[int](e.t, e.s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, action)
}

type adminLaunchBody struct {
	Source    string `json:"source"`
	Surface   string `json:"surface"`
	EntryPath string `json:"entryPath"`
	Layout    string `json:"layout"`
	Routes    []struct {
		ID, Path, Layout string
	} `json:"routes"`
	Binding struct {
		Generation string `json:"generation"`
	} `json:"binding"`
}

func decodeLaunch(t *testing.T, w *httptest.ResponseRecorder) adminLaunchBody {
	t.Helper()
	var body adminLaunchBody
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatalf("launch: %d %s", w.Code, w.Body.String())
	}
	return body
}

// Fails if the admin surface needs audience membership, if a reader gets an admin page, or if the reader
// launch stopped following the audience.
func TestPlatformAppAdminLaunchAndDocument(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	// Nobody is in the audience. A user that is neither a Portal administrator nor delegated is refused on
	// every admin entry, with the answer an unknown installation gets.
	if w := e.adminLaunch(e.readerCookie); w.Code != 404 {
		t.Fatalf("plain user opened the admin launch: %d", w.Code)
	}
	if w := e.adminLaunch(""); w.Code != 401 {
		t.Fatalf("anonymous admin launch: %d", w.Code)
	}
	unknown := e.call("GET", "/api/app-platform/installations/"+platformFixtureID(77)+"/admin/launch", e.readerCookie, "")
	if unknown.Code != 404 || unknown.Body.String() != e.adminLaunch(e.readerCookie).Body.String() {
		t.Fatal("a refusal reveals whether the installation exists")
	}
	// A Portal administrator outside the audience may open the admin page and still not the reader page.
	launch := decodeLaunch(t, e.adminLaunch(e.adminCookie))
	if launch.Surface != "admin" || launch.EntryPath != "/admin" || launch.Layout != "viewport" || len(launch.Routes) != 2 || launch.Routes[1].Path != "/admin/runs" || launch.Binding.Generation != "1" {
		t.Fatalf("admin launch shape: %+v", launch)
	}
	if !strings.HasPrefix(launch.Source, "/app-documents/"+e.id+"/admin/1/1/") || !strings.HasSuffix(launch.Source, "/index.html") {
		t.Fatalf("admin document source: %s", launch.Source)
	}
	if w := e.readerLaunch(e.adminCookie); w.Code != 404 {
		t.Fatalf("a Portal administrator outside the audience opened the reader launch: %d", w.Code)
	}
	document := e.call("GET", launch.Source, e.adminCookie, "")
	if document.Code != 200 || !strings.Contains(document.Body.String(), "Fixture") || document.Header().Get("Cache-Control") != "no-store" || !strings.Contains(document.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatalf("admin document: %d", document.Code)
	}
	if w := e.call("GET", launch.Source, e.readerCookie, ""); w.Code != 404 || strings.Contains(w.Body.String(), "Fixture") {
		t.Fatal("plain user read the admin document")
	}
	// The admin document URL is not a reader URL and the reader URL is not an admin URL.
	readerSource := strings.Replace(launch.Source, "/admin/1/", "/1/", 1)
	if w := e.call("GET", readerSource, e.adminCookie, ""); w.Code != 404 {
		t.Fatalf("the reader document opened for an administrator outside the audience: %d", w.Code)
	}
	for _, bad := range []string{strings.Replace(launch.Source, "/1/1/", "/2/1/", 1), launch.Source + "?x=1", strings.Replace(launch.Source, "/admin/", "/Admin/", 1)} {
		if w := e.call("GET", bad, e.adminCookie, ""); w.Code != 404 {
			t.Fatalf("invalid admin document identity served: %s %d", bad, w.Code)
		}
	}
	// Once the audience opens, the reader launch says so.
	platformOpenDefault(t, e.s, e.id)
	reader := decodeLaunch(t, e.readerLaunch(e.readerCookie))
	if reader.Surface != "reader" || reader.EntryPath != "" || !strings.HasPrefix(reader.Source, "/app-documents/"+e.id+"/1/") {
		t.Fatalf("reader launch shape: %+v", reader)
	}
	if w := e.adminLaunch(e.readerCookie); w.Code != 404 {
		t.Fatal("audience membership alone opened the admin launch")
	}
	// Disabling the installation closes the admin surface like the reader one.
	mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='disabled' WHERE id=?`, e.id)
	if w := e.adminLaunch(e.adminCookie); w.Code != 404 {
		t.Fatalf("disabled installation kept an admin surface: %d", w.Code)
	}
}

// Fails if a release without admin_routes can be opened as an admin page.
func TestPlatformAppAdminLaunchRefusedWithoutAdminRoutes(t *testing.T) {
	e := platformAdminPageFixture(t, false)
	if w := e.adminLaunch(e.adminCookie); w.Code != 404 {
		t.Fatalf("a release without admin_routes opened: %d", w.Code)
	}
	binding, err := e.s.inspectPlatformLaunchAccess(e.admin, e.id)
	if err == nil {
		t.Fatalf("the reader gate admitted an administrator outside the audience: %+v", binding)
	}
	platformOpenDefault(t, e.s, e.id)
	binding, err = e.s.inspectPlatformLaunchAccess(e.admin, e.id)
	if err != nil {
		t.Fatal(err)
	}
	source := platformAdminDocumentSource(binding)
	if w := e.call("GET", source, e.adminCookie, ""); w.Code != 404 {
		t.Fatalf("admin document of a release without an admin page: %d", w.Code)
	}
	if w := e.readerLaunch(e.adminCookie); w.Code != 200 {
		t.Fatalf("reader launch of the same release: %d", w.Code)
	}
}

// Fails if a delegation does not open the admin page outside the audience, survives revocation, or is
// inherited by an account that was recreated under the same name.
func TestPlatformAppAdminDelegation(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	_, delegateCookie := e.newUser("app-delegate", "user")
	if w := e.adminLaunch(delegateCookie); w.Code != 404 {
		t.Fatal("undelegated user opened the admin page")
	}
	if w := e.grant(e.adminCookie, "app-delegate"); w.Code != 200 {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	launch := decodeLaunch(t, e.adminLaunch(delegateCookie))
	if launch.Surface != "admin" {
		t.Fatal("delegated launch is not an admin launch")
	}
	if w := e.call("GET", launch.Source, delegateCookie, ""); w.Code != 200 {
		t.Fatalf("delegated document: %d", w.Code)
	}
	if w := e.readerLaunch(delegateCookie); w.Code != 404 {
		t.Fatal("a delegation opened the reader surface")
	}
	// A delegation of another installation grants nothing here.
	mustExec(t, e.s.st, `UPDATE app_platform_app_admins SET installation_id=? WHERE installation_id=?`, platformFixtureID(78), e.id)
	if w := e.adminLaunch(delegateCookie); w.Code != 404 {
		t.Fatal("a delegation of another installation opened this one")
	}
	mustExec(t, e.s.st, `UPDATE app_platform_app_admins SET installation_id=? WHERE installation_id=?`, e.id, platformFixtureID(78))
	// A recreated account carries another created_at and inherits nothing.
	mustExec(t, e.s.st, `UPDATE app_platform_app_admins SET account_created='an earlier account' WHERE username='app-delegate'`)
	if w := e.adminLaunch(delegateCookie); w.Code != 404 {
		t.Fatal("a delegation row of a recreated account opened the admin page")
	}
	if list := e.appAdmins("GET", e.adminCookie, ""); strings.Contains(list.Body.String(), "app-delegate") {
		t.Fatalf("a stale delegation is listed: %s", list.Body.String())
	}
	// Granting again replaces the stale row.
	if w := e.grant(e.adminCookie, "app-delegate"); w.Code != 200 {
		t.Fatal("regrant refused", w.Code)
	}
	if w := e.adminLaunch(delegateCookie); w.Code != 200 {
		t.Fatal("regranted delegation refused")
	}
	// Revocation takes effect on the next request, document included.
	if w := e.revoke(e.adminCookie, "app-delegate"); w.Code != 204 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := e.call("GET", launch.Source, delegateCookie, ""); w.Code != 404 {
		t.Fatal("revoked delegation still read the document")
	}
	if w := e.adminLaunch(delegateCookie); w.Code != 404 {
		t.Fatal("revoked delegation still opened the launch")
	}
	// A deactivated user loses the page even with a row.
	e.grant(e.adminCookie, "app-delegate")
	mustExec(t, e.s.st, `UPDATE users SET active=0 WHERE username='app-delegate'`)
	if w := e.adminLaunch(delegateCookie); w.Code == 200 {
		t.Fatal("an inactive user opened the admin page")
	}
}

// Fails if a delegated administrator can grant or revoke, if grants are not idempotent, if an unknown user
// is accepted, or if the audit rows are not written exactly once per change.
func TestPlatformAppAdminGrantAPI(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	_, delegateCookie := e.newUser("app-delegate", "user")
	e.newUser("second-delegate", "user")
	_, plainCookie := e.newUser("plain-user", "user")
	if w := e.appAdmins("GET", e.adminCookie, ""); w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"admins":[]}` {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
	w := e.grant(e.adminCookie, "app-delegate")
	var granted struct {
		Admin platformAppAdminView `json:"admin"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &granted) != nil || granted.Admin.Username != "app-delegate" || granted.Admin.GrantedBy != e.admin.username || granted.Admin.GrantedAt < 1 {
		t.Fatalf("grant: %d %s", w.Code, w.Body.String())
	}
	// Idempotent: the same row, no second event.
	again := e.grant(e.adminCookie, "app-delegate")
	if again.Code != 200 || again.Body.String() != w.Body.String() || e.audits(platformAppAdminGrantedAudit) != 1 {
		t.Fatalf("regrant changed something: %d %s", again.Code, again.Body.String())
	}
	if w := e.grant(e.adminCookie, "nobody-here"); w.Code != 404 || !strings.Contains(w.Body.String(), "app_platform_user_not_found") {
		t.Fatalf("unknown user: %d %s", w.Code, w.Body.String())
	}
	mustExec(t, e.s.st, `UPDATE users SET active=0 WHERE username='second-delegate'`)
	if w := e.grant(e.adminCookie, "second-delegate"); w.Code != 404 || !strings.Contains(w.Body.String(), "app_platform_user_not_found") {
		t.Fatalf("inactive user: %d", w.Code)
	}
	unknown := e.call("POST", "/api/admin/app-platform/installations/"+platformFixtureID(77)+"/app-admins", e.adminCookie, `{"username":"plain-user"}`)
	if unknown.Code != 404 {
		t.Fatalf("unknown installation: %d", unknown.Code)
	}
	for _, body := range []string{`{}`, `{"username":""}`, `{"username":"a","x":1}`, `{"username":null}`, `[]`} {
		if w := e.appAdmins("POST", e.adminCookie, body); w.Code != 400 {
			t.Fatalf("body %s: %d", body, w.Code)
		}
	}
	list := e.appAdmins("GET", e.adminCookie, "")
	var listed struct {
		Admins []platformAppAdminView `json:"admins"`
	}
	if json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Admins) != 1 || listed.Admins[0].Username != "app-delegate" {
		t.Fatalf("list: %s", list.Body.String())
	}
	// Neither a delegated administrator nor a plain user may manage delegations.
	for _, cookie := range []string{delegateCookie, plainCookie} {
		if w := e.grant(cookie, "plain-user"); w.Code != 403 {
			t.Fatalf("non-Portal-admin granted: %d", w.Code)
		}
		if w := e.revoke(cookie, "app-delegate"); w.Code != 403 {
			t.Fatalf("non-Portal-admin revoked: %d", w.Code)
		}
		if w := e.appAdmins("GET", cookie, ""); w.Code != 403 {
			t.Fatalf("non-Portal-admin listed: %d", w.Code)
		}
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_app_admins`) != 1 || e.audits(platformAppAdminGrantedAudit) != 1 {
		t.Fatal("a refused request changed state")
	}
	// The event names actor, installation, app id and target user.
	var actor, target, detail string
	if err := e.s.st.queryRow(`SELECT actor,target_id,detail FROM audit_log WHERE action=?`, platformAppAdminGrantedAudit).Scan(&actor, &target, &detail); err != nil || actor != e.admin.username || target != e.id || !strings.Contains(detail, `"username":"app-delegate"`) || !strings.Contains(detail, `"app_id":"notes-sample"`) {
		t.Fatalf("grant audit: %s %s %s %v", actor, target, detail, err)
	}
	// Revoke is idempotent; only the effective revoke writes an event.
	if w := e.revoke(e.adminCookie, "app-delegate"); w.Code != 204 || e.audits(platformAppAdminRevokedAudit) != 1 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := e.revoke(e.adminCookie, "app-delegate"); w.Code != 204 || e.audits(platformAppAdminRevokedAudit) != 1 {
		t.Fatalf("second revoke: %d, %d events", w.Code, e.audits(platformAppAdminRevokedAudit))
	}
	if w := e.revoke(e.adminCookie, "never-granted"); w.Code != 204 || e.audits(platformAppAdminRevokedAudit) != 1 {
		t.Fatal("revoking a row that does not exist wrote an event")
	}
}

// Fails if uninstalling leaves delegations or deleting a user leaves theirs behind.
func TestPlatformAppAdminRowsFollowTheirOwners(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	e.newUser("app-delegate", "user")
	e.newUser("other-delegate", "user")
	for _, name := range []string{"app-delegate", "other-delegate"} {
		if w := e.grant(e.adminCookie, name); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if err := e.s.st.DeleteUser("app-delegate"); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_app_admins WHERE username='app-delegate'`) != 0 || scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_app_admins WHERE username='other-delegate'`) != 1 {
		t.Fatal("deleting a user did not remove exactly their delegations")
	}
	// The installation's removal path deletes its rows with the installation's other per-installation rows.
	tx, err := e.s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := deletePlatformAppRows(migExec{s: e.s.st, ex: tx}, e.id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, e.s.st, `SELECT COUNT(*) FROM app_platform_app_admins`) != 0 {
		t.Fatal("removing the installation left delegations")
	}
}

// Fails if a removed installation can be delegated or listed.
func TestPlatformAppAdminRefusesRemovedInstallation(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	e.newUser("app-delegate", "user")
	mustExec(t, e.s.st, `UPDATE app_platform_installations SET desired_state='removed' WHERE id=?`, e.id)
	if w := e.grant(e.adminCookie, "app-delegate"); w.Code != 404 {
		t.Fatalf("grant on a removed installation: %d", w.Code)
	}
	if w := e.appAdmins("GET", e.adminCookie, ""); w.Code != 404 {
		t.Fatalf("list on a removed installation: %d", w.Code)
	}
}

// Fails if the settings projection does not say whether the release declares an admin page.
func TestPlatformAppAdminSettingsProjection(t *testing.T) {
	for _, withPage := range []bool{true, false} {
		e := platformAdminPageFixture(t, withPage)
		w := e.call("GET", "/api/admin/app-platform/installations/"+e.id+"/settings", e.adminCookie, "")
		var settings map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &settings) != nil || settings["admin_page"] != withPage {
			t.Fatalf("withPage=%v: %d admin_page=%v", withPage, w.Code, settings["admin_page"])
		}
	}
}

// Fails if a request that did not pass the admin gate is audited: the audit row says "an administrator
// sent this", so refusals of non-administrators (and of an installation with no backend to reach) keep only
// the server log. The admitted paths are covered by TestPlatformWritableAdminSurfaceBackend and
// TestPlatformAdminRequestAuditWriter.
func TestPlatformAppAdminRequestAudit(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	body := func(method, path string) string {
		raw, _ := json.Marshal(map[string]any{"binding": map[string]string{"generation": "1", "policyRevision": "1", "releaseSha256": e.releaseSHA()}, "method": method, "path": path, "body": ""})
		return string(raw)
	}
	url := "/api/app-platform/installations/" + e.id + "/admin/backend"
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"GET": func() *httptest.ResponseRecorder {
			return e.call("POST", url, e.adminCookie, body("GET", "/v1/status"))
		},
		"POST without a backend": func() *httptest.ResponseRecorder {
			return e.call("POST", url, e.adminCookie, body("POST", "/v1/run-now"))
		},
		"plain user": func() *httptest.ResponseRecorder {
			return e.call("POST", url, e.readerCookie, body("DELETE", "/v1/runs/1"))
		},
		"malformed": func() *httptest.ResponseRecorder { return e.call("POST", url, e.adminCookie, `{"method":"POST"}`) },
	} {
		if w := call(); w.Code != 404 && w.Code != 400 {
			t.Fatalf("%s: %d", name, w.Code)
		}
		if n := e.audits(platformAdminRequestAudit); n != 0 {
			t.Fatalf("%s wrote %d admin_request rows", name, n)
		}
	}
}

func (e *appAdminEnv) releaseSHA() string {
	return scalar[string](e.t, e.s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, e.id)
}

// Fails if the admin directory entries are wrong: manageable follows the app-admin check and the release,
// an administrator outside the audience is listed as not openable, and a plain user sees neither.
func TestPlatformAppAdminDirectory(t *testing.T) {
	e := platformAdminPageFixture(t, true)
	_, delegateCookie := e.newUser("app-delegate", "user")
	list := func(cookie string) []platformAppDirectoryItem {
		w := e.call("GET", "/api/app-platform/apps", cookie, "")
		var dir platformAppDirectory
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &dir) != nil {
			t.Fatalf("directory: %d %s", w.Code, w.Body.String())
		}
		return dir.Items
	}
	// Audience closed: a plain user sees nothing; a Portal administrator sees it manageable, not openable.
	if items := list(e.readerCookie); len(items) != 0 {
		t.Fatalf("plain user listing: %+v", items)
	}
	items := list(e.adminCookie)
	if len(items) != 1 || items[0].InstallationID != e.id || !items[0].Manageable || items[0].Openable || items[0].State != "installed" {
		t.Fatalf("administrator listing: %+v", items)
	}
	// A delegation lists it for the delegate, and a revocation removes it again.
	if items := list(delegateCookie); len(items) != 0 {
		t.Fatalf("undelegated listing: %+v", items)
	}
	e.grant(e.adminCookie, "app-delegate")
	items = list(delegateCookie)
	if len(items) != 1 || !items[0].Manageable || items[0].Openable {
		t.Fatalf("delegate listing: %+v", items)
	}
	// Audience open: openable and manageable are independent.
	platformOpenDefault(t, e.s, e.id)
	if items := list(delegateCookie); len(items) != 1 || !items[0].Manageable || !items[0].Openable {
		t.Fatalf("delegate in audience: %+v", items)
	}
	if items := list(e.readerCookie); len(items) != 1 || items[0].Manageable || !items[0].Openable {
		t.Fatalf("reader in audience: %+v", items)
	}
	e.revoke(e.adminCookie, "app-delegate")
	if items := list(delegateCookie); len(items) != 1 || items[0].Manageable {
		t.Fatalf("revoked delegate: %+v", items)
	}
	// A release without admin routes is never manageable, not even for a Portal administrator.
	plain := platformAdminPageFixture(t, false)
	platformOpenDefault(t, plain.s, plain.id)
	w := plain.call("GET", "/api/app-platform/apps", plain.adminCookie, "")
	var dir platformAppDirectory
	if json.Unmarshal(w.Body.Bytes(), &dir) != nil || len(dir.Items) != 1 || dir.Items[0].Manageable || !dir.Items[0].Openable {
		t.Fatalf("release without admin page: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"manageable":false`) {
		t.Fatal("manageable is omitted instead of false")
	}
}

func decodeAssertionClaims(t *testing.T, token string) protocol.Claims {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.SplitN(token, ".", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	var claims protocol.Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

// Fails if a reader request carries a surface claim, or an admin-surface request does not carry
// surface=admin, at either signing site.
func TestPlatformBackendAssertionSurface(t *testing.T) {
	body := []byte(`{"v":1}`)
	check := func(t *testing.T, sign func(admin bool) string) {
		t.Helper()
		reader, admin := decodeAssertionClaims(t, sign(false)), decodeAssertionClaims(t, sign(true))
		if reader.Surface != "" || reader.Admin() {
			t.Fatalf("a reader assertion carries a surface: %q", reader.Surface)
		}
		if admin.Surface != "admin" || !admin.Admin() {
			t.Fatalf("an admin assertion lacks the admin surface: %q", admin.Surface)
		}
	}
	t.Run("writable", func(t *testing.T) {
		s, actor, lease := platformWritableAccessFixture(t, false)
		access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
		if err != nil {
			t.Fatal(err)
		}
		defer access.Close()
		if access.admin {
			t.Fatal("a reader session produced an admin access")
		}
		check(t, func(admin bool) string {
			var assertion string
			if _, err := s.withPlatformWritableBackendAccess(context.Background(), actor, lease.installationID, func(got platformWritableBackendAccess, seed []byte) error {
				got.admin = admin
				_, _, token, _, err := signPlatformWritableBackendRequest(got, seed, "POST", "/v1/echo", body, "fixture-boot", nil)
				assertion = token
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return assertion
		})
	})
	t.Run("managed", func(t *testing.T) {
		s, actor, lease := platformManagedAccessFixture(t, false)
		check(t, func(admin bool) string {
			var assertion string
			a, err := s.withPlatformManagedBackendAccess(context.Background(), actor, lease.installationID, func(got platformManagedBackendAccess, seed []byte) error {
				got.admin = admin
				_, _, token, _, err := signPlatformManagedBackendRequest(got, seed, "POST", "/v1/echo", body, "fixture-boot")
				assertion = token
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			a.Close()
			return assertion
		})
	})
}

// Fails if the admin surface is admitted for a backend whose release declares no admin page, or if the
// session of a reader request is admitted for the admin check by anything but its own marker.
func TestPlatformBackendAdminSurfaceNeedsAnAdminPage(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	admin := actor
	admin.adminSurface = true
	if a, err := s.preparePlatformWritableBackendAccess(context.Background(), admin, lease.installationID); err == nil {
		a.Close()
		t.Fatal("the admin surface was admitted for a release without admin_routes")
	}
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, lease.installationID)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	// The same holds on the managed gate.
	ms, mactor, mlease := platformManagedAccessFixture(t, false)
	madmin := mactor
	madmin.adminSurface = true
	if a, err := ms.preparePlatformManagedBackendAccess(context.Background(), madmin, mlease.installationID); err == nil {
		a.Close()
		t.Fatal("the admin surface was admitted on the managed gate for a release without admin_routes")
	}
}

// appAdminSessionID derives a distinct 64-character session id per user name.
func appAdminSessionID(name string) string { return platformDigest([]byte("session:" + name)) }

// Admin-surface backend tests with a signed release that declares admin_routes. The native runner exchange
// (root-owned socket, handshake, controller provenance) exists only in the Linux native fixtures, so these
// drive every host-side step up to it: the gate the handler and the after-IO recheck both call, the
// assertion the backend would receive, and the handler's refusal and audit behaviour.
func withAdminRoutesHook(t *testing.T) {
	t.Helper()
	platformFixtureManifestHook = func(m *appplatform.Manifest) {
		m.Presentation.AdminRoutes = []appplatform.Route{{ID: "admin", Path: "/admin", Layout: "viewport"}}
	}
	t.Cleanup(func() { platformFixtureManifestHook = nil })
}

func appAdminSession(t *testing.T, s *Server, name string) (platformActorSession, string) {
	t.Helper()
	if err := s.st.UpsertUser(User{Username: name, PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.st.DeleteUser(name) })
	account := s.st.GetUser(name)
	generation, err := notificationAccountGeneration(s.st.poolExec(), name)
	if err != nil {
		t.Fatal(err)
	}
	session := platformActorSession{username: name, created: account.CreatedAt, sessionID: appAdminSessionID(name), sessionRevision: account.SessionRev, generation: generation, expires: time.Unix(time.Now().Add(time.Hour).Unix(), 0)}
	platformSeedActorSession(t, s.st, session)
	return session, s.managedCookie(*account, session.sessionID, session.expires.Unix())
}

func TestPlatformWritableAdminSurfaceBackend(t *testing.T) {
	withAdminRoutesHook(t)
	s, actor, lease := platformWritableAccessFixture(t, false)
	installation := lease.installationID
	platformCloseAccess(t, s, installation) // nobody is in the audience
	ctx := context.Background()
	delegate, delegateCookie := appAdminSession(t, s, "app-delegate")
	body := []byte(`{"run":"now"}`)
	admin := func(session platformActorSession) platformActorSession { session.adminSurface = true; return session }
	// signed returns the claims of the assertion the backend would receive for the session.
	signed := func(session platformActorSession) (protocol.Claims, error) {
		var token string
		a, err := s.withPlatformWritableBackendAccess(ctx, session, installation, func(a platformWritableBackendAccess, seed []byte) error {
			var err error
			_, _, token, _, err = signPlatformWritableBackendRequest(a, seed, "POST", "/v1/run-now", body, "fixture-boot", nil)
			return err
		})
		if err != nil {
			return protocol.Claims{}, err
		}
		a.Close()
		return decodeAssertionClaims(t, token), nil
	}
	// (a) A Portal administrator outside the audience: admin surface yes, reader surface no.
	if claims, err := signed(admin(actor)); err != nil || !claims.Admin() {
		t.Fatalf("Portal administrator refused or unsigned as admin: %v %q", err, claims.Surface)
	}
	if _, err := signed(actor); err == nil {
		t.Fatal("a Portal administrator outside the audience reached the reader backend")
	}
	// (b) A delegated user outside the audience.
	if _, err := signed(admin(delegate)); err == nil {
		t.Fatal("an undelegated user reached the admin backend")
	}
	if _, err := s.grantPlatformAppAdmin(ctx, actor, installation, delegate.username); err != nil {
		t.Fatal(err)
	}
	claims, err := signed(admin(delegate))
	if err != nil || !claims.Admin() || claims.Method != "POST" || claims.Path != "/v1/run-now" {
		t.Fatalf("delegate: %v %+v", err, claims)
	}
	if _, err := signed(delegate); err == nil {
		t.Fatal("a delegation opened the reader backend")
	}
	// The handler: past the gate it answers 503 (no runner endpoint here) and audits an admitted POST once, a GET never.
	a, err := s.preparePlatformWritableBackendAccess(ctx, actor, installation)
	if err != nil {
		platformOpenDefault(t, s, installation)
		a, err = s.preparePlatformWritableBackendAccess(ctx, actor, installation)
		platformCloseAccess(t, s, installation)
	}
	if err != nil {
		t.Fatal(err)
	}
	release := platformDigest(a.intent.start.writer.descriptor)
	a.Close()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	request := func(method string) string {
		raw, _ := json.Marshal(map[string]any{"binding": map[string]string{"generation": "1", "policyRevision": "1", "releaseSha256": release}, "method": method, "path": "/v1/run-now", "body": ""})
		return string(raw)
	}
	url := "/api/app-platform/installations/" + installation + "/admin/backend"
	count := func() int {
		return scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action=?`, platformAdminRequestAudit)
	}
	if w := sessionTestRequest(mux, "POST", url, delegateCookie, request("GET")); w.Code != 503 || count() != 0 {
		t.Fatalf("GET: %d, %d audit rows", w.Code, count())
	}
	if w := sessionTestRequest(mux, "POST", url, delegateCookie, request("POST")); w.Code != 503 || count() != 1 {
		t.Fatalf("POST: %d, %d audit rows", w.Code, count())
	}
	// Revocation: the recheck after the IO is this same gate, and it now refuses the delegate, so the
	// buffered response would be withheld as uncertain.
	if err := s.revokePlatformAppAdmin(ctx, actor, installation, delegate.username); err != nil {
		t.Fatal(err)
	}
	if _, err := signed(admin(delegate)); err == nil {
		t.Fatal("a revoked delegation still passes the gate")
	}
	// A refusal at the gate was not an administrator's request: no new row.
	if w := sessionTestRequest(mux, "POST", url, delegateCookie, request("POST")); w.Code != 404 || count() != 1 {
		t.Fatalf("revoked POST: %d, %d audit rows", w.Code, count())
	}
}

// The managed gate admits the admin surface under the same rules.
func TestPlatformManagedAdminSurfaceBackend(t *testing.T) {
	withAdminRoutesHook(t)
	s, actor, lease := platformManagedAccessFixture(t, false)
	installation := lease.installationID
	platformCloseAccess(t, s, installation)
	ctx := context.Background()
	delegate, _ := appAdminSession(t, s, "app-delegate")
	admin := func(session platformActorSession) platformActorSession { session.adminSurface = true; return session }
	try := func(session platformActorSession) (bool, error) {
		a, err := s.preparePlatformManagedBackendAccess(ctx, session, installation)
		if err != nil {
			return false, err
		}
		defer a.Close()
		return a.admin, nil
	}
	if ok, err := try(admin(actor)); err != nil || !ok {
		t.Fatalf("Portal administrator: %v", err)
	}
	if _, err := try(actor); err == nil {
		t.Fatal("reader gate admitted a Portal administrator outside the audience")
	}
	if _, err := try(admin(delegate)); err == nil {
		t.Fatal("undelegated user admitted")
	}
	if _, err := s.grantPlatformAppAdmin(ctx, actor, installation, delegate.username); err != nil {
		t.Fatal(err)
	}
	if ok, err := try(admin(delegate)); err != nil || !ok {
		t.Fatalf("delegate: %v", err)
	}
	if _, err := try(delegate); err == nil {
		t.Fatal("delegation opened the reader gate")
	}
	if err := s.revokePlatformAppAdmin(ctx, actor, installation, delegate.username); err != nil {
		t.Fatal(err)
	}
	if _, err := try(admin(delegate)); err == nil {
		t.Fatal("revoked delegation admitted")
	}
}
