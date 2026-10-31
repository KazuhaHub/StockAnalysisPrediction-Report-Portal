package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func platformManagementFixture(t *testing.T, pg bool) (*Server, *http.ServeMux, platformActorSession, string, string) {
	t.Helper()
	s, actor, release := platformInstallFixture(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	return s, mux, actor, release.sha256, cookie
}

func platformManagementPlanBody(release, username string) string {
	return fmt.Sprintf(`{"action":"install","release_sha256":%q,"trust_revision":"1","audience":[{"type":"user","username":%q}]}`, release, username)
}

func testPlatformManagementInstallHTTP(t *testing.T, pg bool) {
	s, mux, actor, release, cookie := platformManagementFixture(t, pg)
	plan := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, platformManagementPlanBody(release, actor.username))
	if plan.Code != 200 {
		t.Fatalf("review: %d %s", plan.Code, plan.Body.String())
	}
	var review struct {
		ID      string `json:"plan_id"`
		SHA     string `json:"plan_sha256"`
		Expires string `json:"expires_ms"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil || !platformValidUUID(review.ID) || !platformValidSHA(review.SHA) || review.Expires == "" {
		t.Fatal("invalid review", plan.Body.String(), err)
	}
	if strings.Contains(plan.Body.String(), "actor_authority") || strings.Contains(plan.Body.String(), "controller_epoch") || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations`) != 0 {
		t.Fatal("review exposed internal authority or installed before confirmation")
	}
	body := fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA, platformFixtureID(970))
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, body)
	if commit.Code != 202 {
		t.Fatalf("commit: %d %s", commit.Code, commit.Body.String())
	}
	var admitted struct {
		ID           string `json:"operation_id"`
		Installation string `json:"installation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil || !platformValidUUID(admitted.ID) || !platformValidUUID(admitted.Installation) {
		t.Fatal("invalid admission", commit.Body.String(), err)
	}
	replayed := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, body)
	if replayed.Code != 202 || !strings.Contains(replayed.Body.String(), admitted.ID) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 1 {
		t.Fatal("retry duplicated or lost operation", replayed.Body.String())
	}
	conflict := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, strings.Replace(body, review.SHA, strings.Repeat("f", 64), 1))
	if conflict.Code != 409 || !strings.Contains(conflict.Body.String(), "app_platform_operation_conflict") {
		t.Fatal("idempotency key accepted different confirmation", conflict.Code, conflict.Body.String())
	}
	path := "/api/admin/app-platform/operations/" + admitted.ID
	pending := sessionTestRequest(mux, "GET", path, cookie, "")
	if pending.Code != 200 || !strings.Contains(pending.Body.String(), `"stage":"queued"`) || strings.Contains(pending.Body.String(), "lease_owner") {
		t.Fatal("invalid durable progress", pending.Code, pending.Body.String())
	}
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")
	complete := sessionTestRequest(mux, "GET", path, cookie, "")
	if complete.Code != 200 || !strings.Contains(complete.Body.String(), `"stage":"complete"`) || !strings.Contains(complete.Body.String(), `"terminal":true`) {
		t.Fatal("completed operation unavailable", complete.Code, complete.Body.String())
	}
	if _, err := s.inspectPlatformLaunchAccess(actor, admitted.Installation); err != nil {
		t.Fatal("HTTP-installed app cannot launch", err)
	}
	for _, response := range []*httptest.ResponseRecorder{plan, commit, pending, complete} {
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("management response can be cached/sniffed")
		}
	}
}

func TestPlatformManagementInstallHTTPSQLite(t *testing.T) {
	testPlatformManagementInstallHTTP(t, false)
}
func TestPlatformManagementInstallHTTPPostgres(t *testing.T) {
	testPlatformManagementInstallHTTP(t, true)
}

func TestPlatformManagementUpdateHTTP(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	source := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	body := fmt.Sprintf(`{"action":"update","release_sha256":%q,"trust_revision":"1","installation_id":%q}`, target.sha256, installation)
	plan := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	if plan.Code != 200 || !strings.Contains(plan.Body.String(), `"action":"update"`) || !strings.Contains(plan.Body.String(), `"target_generation":"2"`) || strings.Contains(plan.Body.String(), "actor_authority") {
		t.Fatal("update review", plan.Code, plan.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil || !platformValidUUID(review.ID) || !platformValidSHA(review.SHA) {
		t.Fatal("invalid update review", plan.Body.String(), err)
	}
	if scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation) != source || scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 1 {
		t.Fatal("update review changed the live generation")
	}
	commitBody := fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA, platformFixtureID(978))
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, commitBody)
	if commit.Code != 202 || !strings.Contains(commit.Body.String(), `"operation_id"`) {
		t.Fatal("update commit", commit.Code, commit.Body.String())
	}
	var admitted struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")
	progress := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations/"+admitted.ID, cookie, "")
	if progress.Code != 200 || !strings.Contains(progress.Body.String(), `"action":"update"`) || !strings.Contains(progress.Body.String(), `"stage":"complete"`) {
		t.Fatal("update progress", progress.Code, progress.Body.String())
	}
	if binding, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil || binding.generation != "2" || binding.releaseSHA != target.sha256 {
		t.Fatal("HTTP update cannot launch", binding, err)
	}
}

func TestPlatformManagementUninstallHTTPClearsPluginData(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"uninstall via management HTTP"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	body := fmt.Sprintf(`{"action":"uninstall","installation_id":%q}`, installation)
	plan := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	if plan.Code != 200 || !strings.Contains(plan.Body.String(), `"action":"uninstall"`) || !strings.Contains(plan.Body.String(), `"plugin_data_cleared":true`) || strings.Contains(plan.Body.String(), "actor_authority") {
		t.Fatal("uninstall review", plan.Code, plan.Body.String())
	}
	if !s.st.tableExists(pluginTable) {
		t.Fatal("uninstall review cleared data before confirmation")
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil || !platformValidUUID(review.ID) || !platformValidSHA(review.SHA) {
		t.Fatal("invalid uninstall review", plan.Body.String(), err)
	}
	commitBody := fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA, platformFixtureID(979))
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, commitBody)
	if commit.Code != 202 {
		t.Fatal("uninstall commit", commit.Code, commit.Body.String())
	}
	if s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation) != 0 {
		t.Fatal("confirmed uninstall retained plugin data or live authority")
	}
	var admitted struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil {
		t.Fatal(err)
	}
	platformStartFrontendService(t, s)
	platformAwaitFrontendService(t, s, "complete")
	progress := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations/"+admitted.ID, cookie, "")
	if progress.Code != 200 || !strings.Contains(progress.Body.String(), `"action":"uninstall"`) || !strings.Contains(progress.Body.String(), `"stage":"complete"`) {
		t.Fatal("uninstall progress", progress.Code, progress.Body.String())
	}
	if binding, err := s.inspectPlatformLaunchAccess(reader, installation); err == nil {
		t.Fatal("uninstalled app remained launchable", binding)
	}
}

func TestPlatformManagementUninstallReviewsManagedNamespace(t *testing.T) {
	s, admin, lease := platformManagedAccessFixture(t, false)
	installation := lease.installationID
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	body := fmt.Sprintf(`{"action":"uninstall","installation_id":%q}`, installation)
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"managed":true`) || !strings.Contains(got.Body.String(), `"managed_data_retained":true`) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans WHERE action='uninstall'`) != 1 {
		t.Fatal("managed uninstall review", got.Code, got.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if json.Unmarshal(got.Body.Bytes(), &review) != nil {
		t.Fatal(got.Body.String())
	}
	commit := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA, platformFixtureID(996)))
	if commit.Code != 202 || scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "removed:pending" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND action='uninstall' AND managed=1 AND stage='queued'`, installation) != 1 {
		t.Fatal("managed uninstall admission", commit.Code, commit.Body.String())
	}
}

func TestPlatformManagementHTTPRefusals(t *testing.T) {
	s, mux, actor, release, cookie := platformManagementFixture(t, false)
	path := "/api/admin/app-platform/plans"
	valid := platformManagementPlanBody(release, actor.username)
	for _, body := range []string{
		strings.Replace(strings.Replace(valid, `"action":"install"`, `"action":"update"`, 1), `"audience":[]`, `"audience":[]`, 1),
		strings.Replace(strings.Replace(valid, `"action":"install"`, `"action":"update"`, 1), `"audience":[`, `"installation_id":"bad","audience":[`, 1),
		strings.Replace(valid, `"action":"install"`, `"action":"disable"`, 1),
		strings.Replace(valid, `"trust_revision":"1"`, `"trust_revision":1`, 1),
		strings.Replace(valid, `"trust_revision":"1"`, `"trust_revision":"01"`, 1),
		strings.Replace(valid, `"action":"install"`, `"action":"install","action":"install"`, 1),
		strings.Replace(valid, `"action":"install"`, `"Action":"install"`, 1),
		strings.Replace(valid, `"action":"install"`, `"action":"install","endpoint":"/tmp/escape.sock"`, 1),
		valid + `{}`, `null`, `{}`, strings.Repeat(" ", 32769) + valid,
		strings.Replace(valid, `"audience":[`, `"audience":null,"ignored":[`, 1),
	} {
		if got := sessionTestRequest(mux, "POST", path, cookie, body); got.Code != 400 {
			t.Fatalf("malformed request admitted: %d %s body=%s", got.Code, got.Body.String(), body)
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != 0 {
		t.Fatal("invalid request saved plan")
	}
	for _, headers := range []map[string]string{
		{"Origin": "https://foreign.example"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Content-Type": "text/plain"},
	} {
		r := httptest.NewRequest("POST", path, strings.NewReader(valid))
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		r.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 403 && w.Code != 415 {
			t.Fatalf("unsafe origin/type allowed: %d", w.Code)
		}
	}
	if got := sessionTestRequest(mux, "POST", path, "", valid); got.Code != 401 {
		t.Fatal("anonymous management allowed", got.Code)
	}
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	if got := sessionTestRequest(mux, "POST", path, cookie, valid); got.Code != 403 {
		t.Fatal("ordinary user management allowed", got.Code)
	}
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations/"+platformFixtureID(971), cookie, ""); got.Code != 403 {
		t.Fatal("ordinary user progress allowed", got.Code)
	}
}

func TestPlatformManagementCommitRefusals(t *testing.T) {
	for _, kind := range []string{"expired", "changed", "role", "native", "native_replay", "enrolment", "empty_audience"} {
		t.Run(kind, func(t *testing.T) {
			s, mux, actor, release, cookie := platformManagementFixture(t, false)
			var review platformRemovalReview
			var err error
			if kind == "native" || kind == "native_replay" {
				review, err = s.createPlatformNativeInstallReview(actor, release, 1)
			} else {
				review, err = s.createPlatformInstallReview(actor, release, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			key := platformFixtureID(972)
			want := 409
			switch kind {
			case "expired":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET expires_ms=0`)
			case "changed":
				review.SHA256 = strings.Repeat("f", 64)
			case "role":
				mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
				want = 403
			case "native_replay":
				if _, err = s.consumePlatformInstallReview(actor, review.ID, review.SHA256, key); err != nil {
					t.Fatal(err)
				}
			case "enrolment":
				var ou int64
				if err = s.st.queryRow(`SELECT id FROM user_groups WHERE is_default=1`).Scan(&ou); err != nil {
					t.Fatal(err)
				}
				if err = s.st.SetGroupRequire2FA(ou, true); err != nil {
					t.Fatal(err)
				}
				want = 403
			case "empty_audience":
				want = 202
			}
			body := fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA256, key)
			got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, body)
			if got.Code != want {
				t.Fatalf("%s: %d %s", kind, got.Code, got.Body.String())
			}
			if kind == "empty_audience" {
				platformStartFrontendService(t, s)
				platformAwaitFrontendService(t, s, "complete")
				if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants`) != 0 {
					t.Fatal("installer received implicit app-use grant")
				}
			} else if kind != "native_replay" && scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
				t.Fatal("refused confirmation created operation")
			}
		})
	}
}

func TestPlatformManagementProgressPrivacyAndPause(t *testing.T) {
	s, actor, id := platformFrontendQueuedFixture(t, false)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	path := "/api/admin/app-platform/operations/" + id
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	got := sessionTestRequest(mux, "GET", path, cookie, "")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"control_paused":true`) {
		t.Fatal("paused progress not inspectable", got.Code, got.Body.String())
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='failed',state_revision=2,error_code='private sql endpoint credential',completed_ms=1`)
	got = sessionTestRequest(mux, "GET", path, cookie, "")
	if got.Code != 200 || strings.Contains(got.Body.String(), "private sql") || !strings.Contains(got.Body.String(), `"error_code":"app_platform_operation_failed"`) {
		t.Fatal("progress leaked raw error", got.Body.String())
	}
	for _, suffix := range []string{"?debug=true", "?"} {
		if got := sessionTestRequest(mux, "GET", path+suffix, cookie, ""); got.Code != 400 {
			t.Fatal("query accepted", got.Code)
		}
	}
	if got := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations/"+platformFixtureID(974), cookie, ""); got.Code != 404 {
		t.Fatal("missing operation status", got.Code)
	}
	mustExec(t, s.st, `UPDATE user_sessions SET revoked=1 WHERE id=?`, actor.sessionID)
	if got := sessionTestRequest(mux, "GET", path, cookie, ""); got.Code != 401 {
		t.Fatal("revoked session read progress", got.Code)
	}
}

func TestPlatformManagementHTTPVerificationBudget(t *testing.T) {
	s, mux, actor, release, cookie := platformManagementFixture(t, false)
	for range 2 {
		releaseSlot, ok := s.platformDocuments.acquire()
		if !ok {
			t.Fatal("verification slot unavailable")
		}
		defer releaseSlot()
	}
	body := platformManagementPlanBody(release, actor.username)
	path := "/api/admin/app-platform/plans"
	got := sessionTestRequest(mux, "POST", path, cookie, body)
	if got.Code != 429 || got.Header().Get("Retry-After") != "1" {
		t.Fatal("unbounded archive verification", got.Code)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_review_plans`) != 0 {
		t.Fatal("busy review persisted")
	}
	if got := sessionTestRequest(mux, "POST", path, "", body); got.Code != 401 {
		t.Fatal("budget replaced authentication", got.Code)
	}
}

func TestPlatformManagementOUReviewBinding(t *testing.T) {
	s, mux, _, release, cookie := platformManagementFixture(t, false)
	var group int64
	if err := s.st.queryRow(`SELECT id FROM user_groups WHERE is_default=1`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action":"install","release_sha256":%q,"trust_revision":"1","audience":[{"type":"ou","group_id":"%d"}]}`, release, group)
	got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/plans", cookie, body)
	if got.Code != 200 || !strings.Contains(got.Body.String(), fmt.Sprintf(`"group_id":"%d"`, group)) {
		t.Fatal("OU review unavailable", got.Code, got.Body.String())
	}
	var plan struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET created_at='different-creation' WHERE id=?`, group)
	commit := fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, plan.ID, plan.SHA, platformFixtureID(976))
	got = sessionTestRequest(mux, "POST", "/api/admin/app-platform/operations", cookie, commit)
	if got.Code != 409 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations`) != 0 {
		t.Fatal("replaced OU inherited reviewed access", got.Code, got.Body.String())
	}
}

func TestPlatformManagementHTTPBlockedRequestCancellationPostgres(t *testing.T) {
	for _, kind := range []string{"plan", "commit", "progress"} {
		t.Run(kind, func(t *testing.T) {
			s, mux, actor, release, cookie := platformManagementFixture(t, true)
			method, path, body := "POST", "/api/admin/app-platform/plans", platformManagementPlanBody(release, actor.username)
			if kind != "plan" {
				review, err := s.createPlatformInstallReview(actor, release, 1)
				if err != nil {
					t.Fatal(err)
				}
				body = fmt.Sprintf(`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`, review.ID, review.SHA256, platformFixtureID(975))
				path = "/api/admin/app-platform/operations"
				if kind == "progress" {
					result, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(975))
					if err != nil {
						t.Fatal(err)
					}
					method, path, body = "GET", path+"/"+result.OperationID, ""
				}
			}
			// The shared browser authentication layer legitimately refreshes an
			// old session's activity stamp. Perform that normal refresh first so
			// the exact dump comparison measures the canceled installation work.
			warm := sessionTestRequest(mux, "GET", "/api/admin/app-platform/operations/"+platformFixtureID(977), cookie, "")
			if warm.Code != 404 {
				t.Fatalf("session warmup: %d", warm.Code)
			}
			before := dumpOf(t, s.st)
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
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); done <- w }()
			observed := false
			until := time.Now().Add(3 * time.Second)
			for time.Now().Before(until) {
				var n int
				if err = s.st.queryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock(780716)%'`).Scan(&n); err != nil {
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
				t.Fatal("request did not wait on principal lock")
			}
			select {
			case got := <-done:
				if got.Code != 503 || strings.Contains(got.Body.String(), "context canceled") {
					t.Fatal("unsafe cancellation response", got.Code, got.Body.String())
				}
			case <-time.After(2 * time.Second):
				holder.Rollback()
				<-done
				t.Fatal("request cancellation failed to interrupt lock wait")
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("canceled management request changed durable state")
			}
		})
	}
}
