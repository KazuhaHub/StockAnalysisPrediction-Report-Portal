package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func platformRunnerUnavailableBusinessCheck(
	t *testing.T, mux *http.ServeMux, cookie, username string,
) {
	t.Helper()
	for iteration := 0; iteration < 10; iteration++ {
		health := sessionTestRequest(mux, http.MethodGet, "/healthz", "", "")
		if health.Code != http.StatusOK ||
			!strings.Contains(health.Body.String(), `"ok":true`) ||
			!strings.Contains(health.Body.String(), `"db":"ok"`) {
			t.Fatalf("runner outage made /healthz unhealthy: %d %s",
				health.Code, health.Body.String())
		}

		site := sessionTestRequest(mux, http.MethodGet, "/api/site", "", "")
		if site.Code != http.StatusOK ||
			!strings.Contains(site.Body.String(), `"siteTitle":"Availability drill"`) {
			t.Fatalf("runner outage made the public site unavailable: %d %s",
				site.Code, site.Body.String())
		}

		me := sessionTestRequest(mux, http.MethodGet, "/api/me", cookie, "")
		if me.Code != http.StatusOK ||
			!strings.Contains(me.Body.String(), `"user":"`+username+`"`) {
			t.Fatalf("runner outage broke the authenticated session: %d %s",
				me.Code, me.Body.String())
		}

		home := sessionTestRequest(mux, http.MethodGet, "/api/home", cookie, "")
		var homeBody map[string]json.RawMessage
		if home.Code != http.StatusOK ||
			json.Unmarshal(home.Body.Bytes(), &homeBody) != nil ||
			homeBody["groups"] == nil {
			t.Fatalf("runner outage broke the Portal home feed: %d %s",
				home.Code, home.Body.String())
		}

		report := sessionTestRequest(
			mux, http.MethodGet, "/api/stock/600519", cookie, "")
		if report.Code != http.StatusOK ||
			!strings.Contains(report.Body.String(), "Portal remains readable") ||
			!strings.Contains(report.Body.String(), "ordinary report body") {
			t.Fatalf("runner outage broke an existing report read: %d %s",
				report.Code, report.Body.String())
		}

		queue := sessionTestRequest(
			mux, http.MethodGet, "/api/notifications/unread", cookie, "")
		if queue.Code != http.StatusOK ||
			!strings.Contains(queue.Body.String(), `"unread":`) {
			t.Fatalf("runner outage broke the central queue surface: %d %s",
				queue.Code, queue.Body.String())
		}
	}
}

func platformPortalAvailabilityHTTPFixture(
	t *testing.T, s *Server, actor platformActorSession,
) (*http.ServeMux, string) {
	t.Helper()
	s.names = LoadNames(t.TempDir(), s.st)
	mustExec(t, s.st, `INSERT INTO meta(k,v) VALUES('site_title','Availability drill')
		ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
	if _, _, err := s.st.UpsertReport(Rep{
		Symbol: "600519",
		Name:   "Portal fixture",
		Date:   "2026-11-01",
		RType:  "research",
		Kind:   "research",
		Title:  "Portal remains readable",
		MD:     "ordinary report body",
	}); err != nil {
		t.Fatal(err)
	}
	const password = "availability-drill-password"
	mustExec(t, s.st, `UPDATE users SET password_hash=? WHERE username=?`,
		mustHash(password), actor.username)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	login := sessionTestRequest(
		mux, http.MethodPost, "/api/login", "",
		fmt.Sprintf(`{"username":%q,"password":%q}`,
			actor.username, password))
	if login.Code != http.StatusOK ||
		!strings.Contains(login.Body.String(), `"user":"`+actor.username+`"`) {
		t.Fatalf("availability login failed: %d %s",
			login.Code, login.Body.String())
	}
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == cookieName {
			return mux, cookie.Value
		}
	}
	t.Fatal("availability login did not issue a managed session")
	return nil, ""
}

func testPlatformRunnerUnavailableLeavesPortalBusinessAvailable(
	t *testing.T, pg bool,
) {
	t.Helper()
	s, actor, lease := platformManagedAccessFixture(t, pg)
	mux, cookie := platformPortalAvailabilityHTTPFixture(t, s, actor)
	platformRunnerUnavailableBusinessCheck(t, mux, cookie, actor.username)

	invalidPlan := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/plans", cookie,
		`{"action":"disable","installation_id":"not-a-uuid"}`)
	if invalidPlan.Code != http.StatusBadRequest {
		t.Fatalf("invalid operator request: %d %s",
			invalidPlan.Code, invalidPlan.Body.String())
	}
	platformRunnerUnavailableBusinessCheck(t, mux, cookie, actor.username)

	plan := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/plans", cookie,
		fmt.Sprintf(`{"action":"disable","installation_id":%q}`,
			lease.installationID))
	if plan.Code != http.StatusOK ||
		!strings.Contains(plan.Body.String(), `"action":"disable"`) {
		t.Fatalf("managed disable review: %d %s", plan.Code, plan.Body.String())
	}
	var review struct {
		ID  string `json:"plan_id"`
		SHA string `json:"plan_sha256"`
	}
	if err := json.Unmarshal(plan.Body.Bytes(), &review); err != nil ||
		!platformValidUUID(review.ID) || !platformValidSHA(review.SHA) {
		t.Fatalf("invalid managed disable review: %s (%v)",
			plan.Body.String(), err)
	}
	commit := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/operations", cookie,
		fmt.Sprintf(
			`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`,
			review.ID, review.SHA, platformFixtureID(1301)))
	if commit.Code != http.StatusAccepted {
		t.Fatalf("managed disable commit: %d %s",
			commit.Code, commit.Body.String())
	}
	var admitted struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &admitted); err != nil ||
		!platformValidUUID(admitted.ID) {
		t.Fatalf("invalid managed disable admission: %s (%v)",
			commit.Body.String(), err)
	}

	progressPath := "/api/admin/app-platform/operations/" + admitted.ID
	queued := sessionTestRequest(mux, http.MethodGet, progressPath, cookie, "")
	if queued.Code != http.StatusOK ||
		!strings.Contains(queued.Body.String(), `"stage":"queued"`) ||
		!strings.Contains(queued.Body.String(), `"terminal":false`) {
		t.Fatalf("queued control path unavailable: %d %s",
			queued.Code, queued.Body.String())
	}
	platformRunnerUnavailableBusinessCheck(t, mux, cookie, actor.username)

	scheduler := platformSchedulerFixture(t, s)
	for attempt := 1; attempt <= platformInstallAttemptLimit; attempt++ {
		err := scheduler.tick(context.Background())
		if attempt < platformInstallAttemptLimit {
			if err == nil || scheduler.currentStatus().code != "retry_pending" {
				t.Fatalf("runner outage attempt %d was not retryable: %v %+v",
					attempt, err, scheduler.currentStatus())
			}
		} else if err != nil || scheduler.currentStatus().code != "review_required" {
			t.Fatalf("runner outage did not settle for review: %v %+v",
				err, scheduler.currentStatus())
		}
		platformRunnerUnavailableBusinessCheck(t, mux, cookie, actor.username)
	}

	progress := sessionTestRequest(
		mux, http.MethodGet, progressPath, cookie, "")
	var operation struct {
		Stage     string `json:"stage"`
		ErrorCode string `json:"error_code"`
		Terminal  bool   `json:"terminal"`
	}
	if progress.Code != http.StatusOK ||
		json.Unmarshal(progress.Body.Bytes(), &operation) != nil ||
		operation.Stage != "recovery_required" ||
		operation.ErrorCode != "app_platform_operation_failed" ||
		!operation.Terminal ||
		strings.Contains(progress.Body.String(), "/tmp/") ||
		strings.Contains(progress.Body.String(), "absent.sock") {
		t.Fatalf("control path did not expose bounded recovery state: %d %s",
			progress.Code, progress.Body.String())
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code
		FROM app_platform_operations WHERE id=?`, admitted.ID) !=
		"recovery_required:managed_disable_stop_pending" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "disabled:recovery_required" ||
		scalar[string](t, s.st, `SELECT state||':'||receipt_json
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='stop'`, admitted.ID) != "prepared:{}" {
		t.Fatal("runner outage fabricated a stop or changed its durable intent")
	}

	directory := sessionTestRequest(
		mux, http.MethodGet, "/api/app-platform/apps", cookie, "")
	var apps struct {
		Items []struct {
			InstallationID string `json:"installationId"`
			State          string `json:"state"`
			Openable       bool   `json:"openable"`
		} `json:"items"`
	}
	if directory.Code != http.StatusOK ||
		json.Unmarshal(directory.Body.Bytes(), &apps) != nil {
		t.Fatalf("app directory unavailable: %d %s",
			directory.Code, directory.Body.String())
	}
	found := false
	for _, item := range apps.Items {
		if item.InstallationID == lease.installationID {
			found = true
			if item.State != "disabled" || item.Openable {
				t.Fatalf("affected app exposed stale launch authority: %+v",
					item)
			}
		}
	}
	if !found {
		t.Fatal("affected installation disappeared from the app directory")
	}
	launch := sessionTestRequest(
		mux, http.MethodGet,
		"/api/app-platform/installations/"+lease.installationID+"/launch",
		cookie, "")
	if launch.Code != http.StatusNotFound {
		t.Fatalf("affected managed app did not fail closed: %d %s",
			launch.Code, launch.Body.String())
	}
	if strings.Contains(launch.Body.String(), "/tmp/") ||
		strings.Contains(launch.Body.String(), "absent.sock") {
		t.Fatal("affected managed app leaked the unavailable endpoint")
	}
	platformRunnerUnavailableBusinessCheck(t, mux, cookie, actor.username)

	restarted := &Server{st: s.st, cfg: s.cfg, names: s.names}
	restartedMux := http.NewServeMux()
	restarted.wireRoutes(restartedMux)
	platformRunnerUnavailableBusinessCheck(
		t, restartedMux, cookie, actor.username)
	restartedProgress := sessionTestRequest(
		restartedMux, http.MethodGet, progressPath, cookie, "")
	if restartedProgress.Code != http.StatusOK ||
		restartedProgress.Body.String() != progress.Body.String() {
		t.Fatalf("Portal restart lost the durable bounded state: %d %s",
			restartedProgress.Code, restartedProgress.Body.String())
	}
}

func TestPlatformRunnerUnavailableLeavesPortalBusinessAvailableSQLite(
	t *testing.T,
) {
	testPlatformRunnerUnavailableLeavesPortalBusinessAvailable(t, false)
}

func TestPlatformRunnerUnavailableLeavesPortalBusinessAvailablePostgres(
	t *testing.T,
) {
	testPlatformRunnerUnavailableLeavesPortalBusinessAvailable(t, true)
}
