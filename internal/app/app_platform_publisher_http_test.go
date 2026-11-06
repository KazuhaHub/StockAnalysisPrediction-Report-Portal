package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testPlatformPublisherManagementHTTP(t *testing.T, pg bool) {
	s, actor := platformActorTestServer(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	read := sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"initialized":false`) {
		t.Fatal("fresh publisher directory failed", read.Code, read.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM meta WHERE k LIKE 'app_platform_%'`) != 0 {
		t.Fatal("publisher read initialized control")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM meta WHERE k IN (?,?)`, setKeyringSalt, setKeyringDEK) != 0 {
		t.Fatal("publisher read initialized a keyring")
	}
	init := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`)
	if init.Code != 200 {
		t.Fatal("explicit control initialization failed", init.Code, init.Body.String())
	}
	key := platformTrustFixtureKey(7, "first-key")
	public, _ := json.Marshal(map[string]any{"id": key.ID, "public_key": key.PublicKey, "not_before_ms": "0", "not_after_ms": nil})
	body := fmt.Sprintf(`{"action":"register","publisher_id":"fixture-publisher","enabled":true,"namespaces":[{"kind":"prefix","value":"notes-"}],"key":%s}`, public)
	registered := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, body)
	if registered.Code != 200 || !strings.Contains(registered.Body.String(), `"revision":"1"`) {
		t.Fatal("publisher registration failed", registered.Code, registered.Body.String())
	}
	epoch := scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_controller_epoch'`)
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`); got.Code != 200 || scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_controller_epoch'`) != epoch {
		t.Fatal("initialization retry replaced control identity", got.Code)
	}
	read = sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), key.PublicKey) || strings.Contains(read.Body.String(), "actor_seed") || strings.Contains(read.Body.String(), "controller_epoch") {
		t.Fatal("unsafe trust directory", read.Code, read.Body.String())
	}
	policy := `{"action":"policy","publisher_id":"fixture-publisher","expected_revision":"1","enabled":false,"namespaces":[{"kind":"prefix","value":"notes-"}]}`
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, policy); got.Code != 200 {
		t.Fatal("disable failed", got.Code, got.Body.String())
	}
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, policy); got.Code != 409 {
		t.Fatal("stale revision accepted", got.Code)
	}
	revoke := `{"action":"revoke_key","publisher_id":"fixture-publisher","expected_revision":"2","key_id":"first-key","expected_key_revision":"1"}`
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, revoke); got.Code != 200 {
		t.Fatal("revoke failed", got.Code, got.Body.String())
	}
	second := platformTrustFixtureKey(8, "second-key")
	secondRaw, _ := json.Marshal(map[string]any{"id": second.ID, "public_key": second.PublicKey, "not_before_ms": "0", "not_after_ms": nil})
	rotation := fmt.Sprintf(`{"action":"add_key","publisher_id":"fixture-publisher","expected_revision":"3","key":%s}`, secondRaw)
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, rotation); got.Code != 200 || !strings.Contains(got.Body.String(), `"revision":"4"`) {
		t.Fatal("key rotation failed", got.Code, got.Body.String())
	}
	if scalar[int](t, s.st, `SELECT enabled FROM app_platform_publishers WHERE id='fixture-publisher'`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_publisher_keys WHERE revoked_ms IS NOT NULL`) != 1 {
		t.Fatal("key rotation enabled publisher or erased revocation")
	}
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	read = sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"paused":true`) {
		t.Fatal("paused trust not inspectable", read.Code)
	}
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`); got.Code != 503 {
		t.Fatal("initialization unpaused restored control", got.Code)
	}
	if scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "1" {
		t.Fatal("restore pause cleared")
	}
}

func TestPlatformPublisherInitializeRefusesOrphanedTrust(t *testing.T) {
	s, mux, _, _, cookie := platformManagementFixture(t, false)
	mustExec(t, s.st, `DELETE FROM meta WHERE k IN ('app_platform_actor_seed','app_platform_controller_epoch','app_platform_restore_paused')`)
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`)
	if got.Code != 503 || scalar[int](t, s.st, `SELECT COUNT(*) FROM meta WHERE k IN ('app_platform_actor_seed','app_platform_controller_epoch','app_platform_restore_paused')`) != 0 {
		t.Fatal("orphaned trust/package history reinitialized", got.Code, got.Body.String())
	}
}

func TestPlatformPublisherInitializeRefusesRetainedArtifact(t *testing.T) {
	s, actor := platformActorTestServer(t, false)
	mustExec(t, s.st, `INSERT INTO app_platform_frontend_artifacts(sha256,archive_bytes,content,created_ms) VALUES(?,1,?,1)`, strings.Repeat("a", 64), []byte{1})
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	read := sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, "")
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"paused":true`) || !strings.Contains(read.Body.String(), `"initialized":false`) {
		t.Fatal("retained history misidentified as fresh", read.Code, read.Body.String())
	}
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`)
	if got.Code != 503 || scalar[int](t, s.st, `SELECT COUNT(*) FROM meta WHERE k IN ('app_platform_actor_seed','app_platform_controller_epoch','app_platform_restore_paused')`) != 0 {
		t.Fatal("retained artifact was treated as a new deployment", got.Code)
	}
}

func TestPlatformPublisherHTTPRoleAndOrigin(t *testing.T) {
	s, mux, actor, _, cookie := platformManagementFixture(t, false)
	request := httptest.NewRequest("POST", "/api/admin/app-platform/control/initialize", strings.NewReader(`{"action":"initialize"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://foreign.example")
	request.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, request)
	if w.Code != 403 {
		t.Fatal("cross-origin initialization allowed", w.Code)
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", cookie, ""); got.Code != 403 {
		t.Fatal("ordinary user read publisher trust", got.Code)
	}
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/control/initialize", cookie, `{"action":"initialize"}`); got.Code != 403 {
		t.Fatal("ordinary user initialized control", got.Code)
	}
}

func TestPlatformPublisherManagementCancellationPostgres(t *testing.T) {
	s, mux, _, _, cookie := platformManagementFixture(t, true)
	for _, kind := range []string{"read", "write", "initialize"} {
		t.Run(kind, func(t *testing.T) {
			audits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.%'`)
			method, path, body := "GET", "/api/admin/app-platform/publishers", ""
			if kind == "write" {
				method = "POST"
				body = `{"action":"policy","publisher_id":"fixture-publisher","expected_revision":"1","enabled":false,"namespaces":[{"kind":"prefix","value":"notes-"}]}`
			}
			if kind == "initialize" {
				method, path, body = "POST", "/api/admin/app-platform/control/initialize", `{"action":"initialize"}`
			}
			holder, err := s.st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback()
			if _, err = holder.Exec(`SELECT pg_advisory_xact_lock(780716)`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
			done := make(chan int, 1)
			go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); done <- w.Code }()
			observed := false
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				var n int
				if err := s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock(780716)%'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					observed = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			if !observed {
				holder.Rollback()
				<-done
				t.Fatal("publisher request did not wait on lifecycle lock")
			}
			select {
			case code := <-done:
				if code != 503 {
					t.Fatal("canceled publisher request returned unsafe status", code)
				}
			case <-time.After(2 * time.Second):
				holder.Rollback()
				<-done
				t.Fatal("publisher request ignored cancellation")
			}
			if scalar[int](t, s.st, `SELECT enabled FROM app_platform_publishers WHERE id='fixture-publisher'`) != 1 {
				t.Fatal("canceled policy changed trust")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action LIKE 'app_platform.%'`) != audits {
				t.Fatal("canceled publisher request committed an audit")
			}
		})
	}
}

func TestPlatformPublisherManagementHTTPSQLite(t *testing.T) {
	testPlatformPublisherManagementHTTP(t, false)
}
func TestPlatformPublisherManagementHTTPPostgres(t *testing.T) {
	testPlatformPublisherManagementHTTP(t, true)
}

func TestPlatformPublisherHTTPInputRefusals(t *testing.T) {
	_, mux, _, _, cookie := platformManagementFixture(t, false)
	for _, body := range []string{
		`{}`, `null`, `{"action":"register","action":"register"}`,
		`{"Action":"register"}`, `{"action":"register","publisher_id":"x","private_key":"secret"}`,
		`{"action":"policy","publisher_id":"fixture-publisher","expected_revision":1,"enabled":false,"namespaces":[]}`,
		`{"action":"policy","publisher_id":"fixture-publisher","expected_revision":"01","enabled":false,"namespaces":[]}`,
		`{"action":"revoke_key","publisher_id":"fixture-publisher","expected_revision":"1","key_id":"first-key","expected_key_revision":"1","enabled":true}`,
	} {
		got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, body)
		if got.Code != 400 {
			t.Fatal("invalid publisher request accepted", got.Code, got.Body.String())
		}
	}
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/publishers", "", ""); got.Code != 401 {
		t.Fatal("anonymous publisher read", got.Code)
	}
}
