package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// Fails if the admin surface advertises a capability. Only the backend transport is admin-scoped in this
// version (doc 191, section 9); every other transport still goes through audience-gated reader endpoints,
// so the admin launch must not offer what the admin frame cannot use.
func TestPlatformLaunchCapabilitiesBySurface(t *testing.T) {
	t.Parallel()
	granted := []string{"network.http", "storage.files"}
	if got := platformLaunchCapabilities(true, granted); got == nil || len(got) != 0 {
		t.Fatalf("admin surface capabilities = %#v, want an empty list", got)
	}
	if got := platformLaunchCapabilities(false, granted); strings.Join(got, ",") != "network.http,storage.files" {
		t.Fatalf("reader surface capabilities = %v", got)
	}
	if got := platformLaunchCapabilities(false, nil); got == nil || len(got) != 0 {
		t.Fatalf("no grants must encode as [] and not null: %#v", got)
	}
	e := platformAdminPageFixture(t, true)
	w := e.adminLaunch(e.adminCookie)
	var raw map[string]json.RawMessage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &raw) != nil {
		t.Fatalf("admin launch: %d", w.Code)
	}
	if string(raw["capabilities"]) != "[]" {
		t.Fatalf("admin launch capabilities = %s", raw["capabilities"])
	}
	if _, ok := raw["backendAvailable"]; !ok {
		t.Fatal("backendAvailable is missing from the admin launch")
	}
}

// Fails if the reader launch does not say whether the viewer can manage the app, if it says so for a
// viewer who is not an app administrator, or if a release without an admin page is ever manageable.
func TestPlatformReaderLaunchManageable(t *testing.T) {
	t.Parallel()
	e := platformAdminPageFixture(t, true)
	_, delegateCookie := e.newUser("app-delegate", "user")
	platformOpenDefault(t, e.s, e.id)
	manageable := func(cookie string) bool {
		t.Helper()
		var raw map[string]json.RawMessage
		w := e.readerLaunch(cookie)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &raw) != nil {
			t.Fatalf("reader launch: %d", w.Code)
		}
		v, ok := raw["manageable"]
		if !ok {
			t.Fatal("manageable is missing from the reader launch")
		}
		return string(v) == "true"
	}
	if manageable(e.readerCookie) {
		t.Fatal("a plain reader is manageable")
	}
	if !manageable(e.adminCookie) {
		t.Fatal("a Portal administrator is not manageable")
	}
	if manageable(delegateCookie) {
		t.Fatal("an undelegated user is manageable")
	}
	if w := e.grant(e.adminCookie, "app-delegate"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if !manageable(delegateCookie) {
		t.Fatal("a delegate is not manageable")
	}
	// A broken OU chain refuses the delegate on the reader launch altogether; the Portal administrator is
	// unaffected here (the delegate rule is covered by TestPlatformAppAdminDelegateNeedsAccessChain).
	plain := platformAdminPageFixture(t, false)
	platformOpenDefault(t, plain.s, plain.id)
	if w := plain.readerLaunch(plain.adminCookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"manageable":false`) {
		t.Fatalf("release without an admin page: %d %s", w.Code, w.Body.String())
	}
}

// Fails if the "release declares admin_routes" bit is parsed again for a release that was already seen,
// or if the cache is unbounded. Releases are immutable, so the SHA-256 is a sound key.
func TestPlatformAdminRoutesCache(t *testing.T) {
	t.Parallel()
	e := platformAdminPageFixture(t, true)
	sha := e.releaseSHA()
	platformAdminRoutesCacheReset()
	administered := func() bool {
		t.Helper()
		m, err := platformAdministeredInstallations(migExec{s: e.s.st, ex: e.s.st.db}, platformActorAccount{username: e.admin.username, role: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		return m[e.id]
	}
	if !administered() {
		t.Fatal("a release with admin_routes is not reported as having an admin page")
	}
	// Corrupt the stored descriptor: a second call that re-parsed it would now answer false.
	mustExec(t, e.s.st, `UPDATE app_platform_releases SET descriptor=? WHERE sha256=?`, []byte("not json"), sha)
	if !administered() {
		t.Fatal("the descriptor was parsed again for a release already seen")
	}
	// Bounded: more distinct releases than the limit never grow the cache past it.
	for i := 0; i < platformAdminRoutesCacheMax*2; i++ {
		platformAdminRoutesCacheStore(platformFixtureID(1000+i), true)
	}
	if n := platformAdminRoutesCacheLen(); n > platformAdminRoutesCacheMax {
		t.Fatalf("cache holds %d entries, limit %d", n, platformAdminRoutesCacheMax)
	}
}

// Fails if a delegate with a broken OU chain still opens the admin surface (the reader path refuses a
// viewer whose chain is broken, and so must the admin path).
func TestPlatformAppAdminDelegateNeedsAccessChain(t *testing.T) {
	t.Parallel()
	e := platformAdminPageFixture(t, true)
	_, delegateCookie := e.newUser("app-delegate", "user")
	if w := e.grant(e.adminCookie, "app-delegate"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := e.adminLaunch(delegateCookie); w.Code != 200 {
		t.Fatalf("delegate before the break: %d", w.Code)
	}
	var defaultID int64
	var created string
	if err := e.s.st.queryRow(`SELECT id,created_at FROM user_groups WHERE is_default=1`).Scan(&defaultID, &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mustExec(t, e.s.st, `UPDATE user_groups SET created_at=? WHERE id=?`, created, defaultID) })
	mustExec(t, e.s.st, `UPDATE user_groups SET created_at='' WHERE id=?`, defaultID)
	if w := e.adminLaunch(delegateCookie); w.Code != 404 {
		t.Fatalf("a delegate with a broken OU chain opened the admin page: %d", w.Code)
	}
}

// Fails if the writer of the admin_request row loses the outcome: ok records the app's status, uncertain
// records 502 and no backend status, refused records the Portal's status.
func TestPlatformAdminRequestAuditWriter(t *testing.T) {
	t.Parallel()
	e := platformAdminPageFixture(t, true)
	r := httptest.NewRequest("POST", "/api/app-platform/installations/"+e.id+"/admin/backend", nil)
	detail := func() map[string]any {
		t.Helper()
		var raw string
		if err := e.s.st.queryRow(`SELECT detail FROM audit_log WHERE action=? ORDER BY id DESC LIMIT 1`, platformAdminRequestAudit).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var d map[string]any
		if json.Unmarshal([]byte(raw), &d) != nil {
			t.Fatal(raw)
		}
		return d
	}
	e.s.auditPlatformAdminRequest(r, e.admin.username, e.id, "POST", "/v1/run-now", 200, 201, "ok")
	if d := detail(); d["outcome"] != "ok" || d["status"] != float64(200) || d["backend_status"] != float64(201) || d["method"] != "POST" || d["path"] != "/v1/run-now" || d["app_id"] != "notes-sample" {
		t.Fatalf("ok: %v", d)
	}
	e.s.auditPlatformAdminRequest(r, e.admin.username, e.id, "DELETE", "/v1/runs/1", 502, 0, "uncertain")
	if d := detail(); d["outcome"] != "uncertain" || d["status"] != float64(502) || d["method"] != "DELETE" {
		t.Fatalf("uncertain: %v", d)
	} else if _, has := d["backend_status"]; has {
		t.Fatalf("uncertain recorded a backend status: %v", d)
	}
	e.s.auditPlatformAdminRequest(r, e.admin.username, e.id, "POST", "/v1/run-now", 503, 0, "refused")
	if d := detail(); d["outcome"] != "refused" || d["status"] != float64(503) {
		t.Fatalf("refused: %v", d)
	}
	if e.audits(platformAdminRequestAudit) != 3 {
		t.Fatal("expected one row per call")
	}
}

// Fails if the reader backend endpoint can hand the signer an admin surface. The handler is driven with
// the cookie of a Portal administrator and of a delegate, up to the signer, through the gate and the
// signing function the runner round trip would call; the assertion must carry no surface. The admin
// endpoint is the control: the same sessions arrive with surface=admin.
func TestPlatformReaderBackendEndpointNeverSignsAdmin(t *testing.T) {
	t.Parallel()
	withAdminRoutesHook(t)
	s, actor, lease := platformWritableAccessFixture(t, false)
	installation := lease.installationID
	platformOpenDefault(t, s, installation)
	delegate, delegateCookie := appAdminSession(t, s, "app-delegate")
	if _, err := s.grantPlatformAppAdmin(context.Background(), actor, installation, delegate.username); err != nil {
		t.Fatal(err)
	}
	a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	release := platformDigest(a.intent.start.writer.descriptor)
	a.Close()
	adminCookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	var surfaces []string
	var adminFlags []bool
	s.platformBackendSend = func(ctx context.Context, session platformActorSession, installation string, _ platformBackendRequestBinding, method, path string, body []byte, _ platformRuntimeEndpoint) (driver.BackendResponse, error) {
		adminFlags = append(adminFlags, session.adminSurface)
		var token string
		a, err := s.withPlatformWritableBackendAccess(ctx, session, installation, func(a platformWritableBackendAccess, seed []byte) error {
			var err error
			_, _, token, _, err = signPlatformWritableBackendRequest(a, seed, method, path, body, "fixture-boot", nil)
			return err
		})
		if err != nil {
			return driver.BackendResponse{}, err
		}
		a.Close()
		claims := decodeAssertionClaims(t, token)
		surfaces = append(surfaces, claims.Surface)
		return driver.BackendResponse{Status: 200, Body: []byte(`{}`)}, nil
	}
	s.platformBackendEndpoint.Store(&platformRuntimeEndpoint{})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	raw, _ := json.Marshal(map[string]any{"binding": map[string]string{"generation": "1", "policyRevision": "1", "releaseSha256": release}, "method": "POST", "path": "/v1/run-now", "body": ""})
	for _, cookie := range []string{adminCookie, delegateCookie} {
		if w := sessionTestRequest(mux, "POST", "/api/app-platform/installations/"+installation+"/backend", cookie, string(raw)); w.Code != 200 {
			t.Fatalf("reader backend: %d %s", w.Code, w.Body.String())
		}
	}
	if len(surfaces) != 2 || surfaces[0] != "" || surfaces[1] != "" || adminFlags[0] || adminFlags[1] {
		t.Fatalf("the reader endpoint reached the signer with surfaces %q admin=%v", surfaces, adminFlags)
	}
	surfaces, adminFlags = nil, nil
	for _, cookie := range []string{adminCookie, delegateCookie} {
		if w := sessionTestRequest(mux, "POST", "/api/app-platform/installations/"+installation+"/admin/backend", cookie, string(raw)); w.Code != 200 {
			t.Fatalf("admin backend: %d %s", w.Code, w.Body.String())
		}
	}
	if len(surfaces) != 2 || surfaces[0] != "admin" || surfaces[1] != "admin" || !adminFlags[0] || !adminFlags[1] {
		t.Fatalf("the admin endpoint reached the signer with surfaces %q admin=%v", surfaces, adminFlags)
	}
}

// Fails if a pre-existing table of the wrong shape is accepted by step 0025, or if the verifier rejects
// the table the step creates.
func TestMigration0025VerifiesTheTableShape(t *testing.T) {
	t.Parallel()
	step := migration0025()
	if step.verify == nil {
		t.Fatal("step 0025 has no shape check")
	}
	st := newTestStore(t)
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{step}); err != nil {
		t.Fatalf("the table the step creates was refused: %v", err)
	}
	for name, ddl := range map[string]string{
		"wrong column type":      `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_by TEXT NOT NULL, granted_at TEXT NOT NULL, PRIMARY KEY(installation_id, username))`,
		"nullable column":        `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT, granted_by TEXT NOT NULL, granted_at BIGINT NOT NULL, PRIMARY KEY(installation_id, username))`,
		"no primary key":         `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_by TEXT NOT NULL, granted_at BIGINT NOT NULL)`,
		"key on one column":      `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_by TEXT NOT NULL, granted_at BIGINT NOT NULL, PRIMARY KEY(installation_id))`,
		"extra column":           `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_by TEXT NOT NULL, granted_at BIGINT NOT NULL, note TEXT, PRIMARY KEY(installation_id, username))`,
		"missing column":         `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_at BIGINT NOT NULL, PRIMARY KEY(installation_id, username))`,
		"key in the wrong order": `CREATE TABLE app_platform_app_admins(installation_id TEXT NOT NULL, username TEXT NOT NULL, account_created TEXT NOT NULL, granted_by TEXT NOT NULL, granted_at BIGINT NOT NULL, PRIMARY KEY(username, installation_id))`,
	} {
		bad := newTestStore(t)
		mustExec(t, bad, `DROP TABLE app_platform_app_admins`)
		mustExec(t, bad, `DELETE FROM meta WHERE k='`+migrationLedgerKey("0025")+`'`)
		mustExec(t, bad, ddl)
		if err := bad.runMigrations(); err == nil {
			t.Fatalf("%s: step 0025 accepted a wrong-shape table", name)
		}
		if err := bad.verifyMigrationProducts(bad.poolExec(), []migration{step}); err == nil {
			t.Fatalf("%s: the verifier accepted a wrong-shape table", name)
		}
	}
}

func platformAdminRoutesCacheReset() {
	platformAdminRoutesCache.Lock()
	defer platformAdminRoutesCache.Unlock()
	platformAdminRoutesCache.bits = map[string]bool{}
}
