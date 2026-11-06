package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

type platformManagementReviewBody struct {
	ID  string `json:"plan_id"`
	SHA string `json:"plan_sha256"`
}

func platformManagementReviewAndCommit(
	t *testing.T,
	mux *http.ServeMux,
	cookie, planBody, key string,
) (platformManagementReviewBody, string) {
	t.Helper()
	plan := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/plans",
		cookie, planBody)
	if plan.Code != http.StatusOK {
		t.Fatalf("management review: %d %s", plan.Code, plan.Body.String())
	}
	var review platformManagementReviewBody
	if json.Unmarshal(plan.Body.Bytes(), &review) != nil ||
		!platformValidUUID(review.ID) ||
		!platformValidSHA(review.SHA) {
		t.Fatalf("invalid management review: %s", plan.Body.String())
	}
	commit := sessionTestRequest(
		mux, http.MethodPost, "/api/admin/app-platform/operations",
		cookie,
		fmt.Sprintf(
			`{"plan_id":%q,"plan_sha256":%q,"idempotency_key":%q}`,
			review.ID, review.SHA, key))
	if commit.Code != http.StatusAccepted {
		t.Fatalf("management commit: %d %s", commit.Code, commit.Body.String())
	}
	var admitted struct {
		OperationID    string `json:"operation_id"`
		InstallationID string `json:"installation_id"`
	}
	if json.Unmarshal(commit.Body.Bytes(), &admitted) != nil ||
		!platformValidUUID(admitted.OperationID) ||
		!platformValidUUID(admitted.InstallationID) {
		t.Fatalf("invalid management admission: %s", commit.Body.String())
	}
	return review, admitted.OperationID
}

func platformRunOperationToCompletion(
	t *testing.T,
	s *Server,
	worker *platformInstallScheduler,
	operation string,
) {
	t.Helper()
	for attempt := 0; attempt < 12; attempt++ {
		_ = worker.tick(context.Background())
		if scalar[string](t, s.st,
			`SELECT stage FROM app_platform_operations WHERE id=?`,
			operation) == "complete" {
			return
		}
	}
	t.Fatalf("operation %s did not complete: action=%s managed=%d stage=%s error=%s worker=%+v",
		operation,
		scalar[string](t, s.st,
			`SELECT action FROM app_platform_operations WHERE id=?`,
			operation),
		scalar[int](t, s.st,
			`SELECT managed FROM app_platform_operations WHERE id=?`,
			operation),
		scalar[string](t, s.st,
			`SELECT stage FROM app_platform_operations WHERE id=?`,
			operation),
		scalar[string](t, s.st,
			`SELECT COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`,
			operation),
		worker.currentStatus())
}

func testPlatformReviewedReinstallStartsEmpty(
	t *testing.T, pg bool,
) {
	t.Helper()
	s, admin, reader, oldInstallation := platformObjectsFixturePG(t, pg)
	appID := scalar[string](t, s.st,
		`SELECT app_id FROM app_platform_installations WHERE id=?`,
		oldInstallation)
	release := scalar[string](t, s.st,
		`SELECT release_sha256 FROM app_platform_activations
		 WHERE installation_id=? AND state='active'`, oldInstallation)
	if !platformValidSHA(release) {
		t.Fatal("old installation has no active release")
	}
	oldBinding := platformObjectBindingMap(t, s, reader, oldInstallation)
	oldObject := platformObjectCall(
		t, s, reader, oldInstallation,
		platformObjectCreateBody(oldBinding, `{"body":"old installation"}`))
	if oldObject.Code != http.StatusOK {
		t.Fatalf("old object create: %d %s",
			oldObject.Code, oldObject.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(
		t, s.st, oldInstallation)
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM `+pluginTable) != 1 {
		t.Fatal("old installation did not create its plugin row")
	}
	// This fixture exercises the frontend/plugin-table lifecycle. A real
	// managed namespace requires the separately tested native data executor.
	mustExec(t, s.st,
		`UPDATE app_platform_activations
		 SET data_namespace_id=NULL,data_format=0
		 WHERE installation_id=?`, oldInstallation)
	mustExec(t, s.st,
		`DELETE FROM app_platform_data_namespaces
		 WHERE origin_installation_id=? OR owner_installation_id=?`,
		oldInstallation, oldInstallation)

	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(
		*s.st.GetUser(admin.username),
		admin.sessionID, admin.expires.Unix())
	_, removeOperation := platformManagementReviewAndCommit(
		t, mux, cookie,
		fmt.Sprintf(`{"action":"uninstall","installation_id":%q}`,
			oldInstallation),
		platformFixtureID(1310))
	worker, err := newPlatformInstallScheduler(
		s, platformRuntimeEndpoint{}, platformFixtureID(1312), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	platformRunOperationToCompletion(t, s, worker, removeOperation)
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations
		WHERE id=?`, removeOperation) != "complete" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			oldInstallation) != "removed:removed" ||
		s.st.tableExists(pluginTable) ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables
			WHERE app_id=?`, appID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants
			WHERE installation_id=?`, oldInstallation) != 0 {
		t.Fatal("reviewed uninstall did not clear the selected plugin data")
	}

	_, installOperation := platformManagementReviewAndCommit(
		t, mux, cookie,
		platformManagementPlanBody(release, reader.username),
		platformFixtureID(1311))
	platformRunOperationToCompletion(t, s, worker, installOperation)
	newInstallation := scalar[string](t, s.st,
		`SELECT installation_id FROM app_platform_operations WHERE id=?`,
		installOperation)
	if !platformValidUUID(newInstallation) ||
		newInstallation == oldInstallation ||
		scalar[int](t, s.st,
			`SELECT COUNT(*) FROM app_platform_installations
			 WHERE id IN (?,?)`, oldInstallation, newInstallation) != 2 {
		t.Fatal("reinstall reused old installation identity")
	}
	newBinding := platformObjectBindingMap(t, s, reader, newInstallation)
	listed := platformObjectCall(
		t, s, reader, newInstallation,
		map[string]any{
			"binding":    newBinding,
			"action":     "list",
			"collection": "note",
		})
	if listed.Code != http.StatusOK ||
		!strings.Contains(listed.Body.String(), `"items":[]`) ||
		strings.Contains(listed.Body.String(), "old installation") {
		t.Fatalf("reinstall inherited old object data: %d %s",
			listed.Code, listed.Body.String())
	}
	if s.st.tableExists(pluginTable) ||
		scalar[int](t, s.st,
			`SELECT COUNT(*) FROM app_platform_plugin_tables
			 WHERE app_id=?`, appID) != 0 {
		t.Fatal("reinstall retained the old plugin table or registry row")
	}
	fresh := platformObjectCall(
		t, s, reader, newInstallation,
		platformObjectCreateBody(newBinding, `{"body":"fresh installation"}`))
	if fresh.Code != http.StatusOK ||
		strings.Contains(fresh.Body.String(), "old installation") {
		t.Fatalf("fresh installation object write: %d %s",
			fresh.Code, fresh.Body.String())
	}
	newPluginTable := platformPluginTableForInstallation(
		t, s.st, newInstallation)
	if scalar[int](t, s.st,
		`SELECT COUNT(*) FROM `+newPluginTable) != 1 ||
		newPluginTable != pluginTable ||
		scalar[int](t, s.st,
			`SELECT COUNT(*) FROM app_platform_access_grants
			 WHERE installation_id=?`, oldInstallation) != 0 {
		t.Fatal("reinstall revived old rows or lost fresh data")
	}
	if err := verifyPlatformObjectStorage(s.st.poolExec()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformReviewedReinstallStartsEmptySQLite(t *testing.T) {
	testPlatformReviewedReinstallStartsEmpty(t, false)
}

func TestPlatformReviewedReinstallStartsEmptyPostgres(t *testing.T) {
	testPlatformReviewedReinstallStartsEmpty(t, true)
}
