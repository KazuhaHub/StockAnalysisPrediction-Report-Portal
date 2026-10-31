package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformObjectsFixturePG(t *testing.T, pg bool) (*Server, platformActorSession, platformActorSession, string) {
	t.Helper()
	s, admin := platformTrustTestServer(t, pg)
	platformRegisterTestPublisher(t, s, admin)
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.1")
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	compatibility, ok := manifest["compatibility"].(map[string]any)
	if !ok {
		t.Fatal("missing compatibility fixture")
	}
	compatibility["platform_api"] = ">=2.0.0-0 <3.0.0"
	manifest["capabilities"] = map[string]any{"required": []string{"storage.files"}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(admin, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	s, reader, installation := completePlatformLaunchFixture(t, s, admin, release.sha256)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='storage.files'`, installation)
	platformGrantLaunchUser(t, s, reader, installation)
	return s, admin, reader, installation
}
func platformObjectsFixture(t *testing.T) (*Server, platformActorSession, platformActorSession, string) {
	return platformObjectsFixturePG(t, false)
}

func platformObjectBindingMap(t *testing.T, s *Server, session platformActorSession, installation string) map[string]string {
	t.Helper()
	binding, err := s.inspectPlatformLaunchAccess(session, installation)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"generation": binding.generation, "policyRevision": binding.policyRevision, "releaseSha256": binding.releaseSHA}
}

func platformObjectCall(t *testing.T, s *Server, session platformActorSession, installation string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())
	return sessionTestRequest(mux, "POST", "/api/app-platform/installations/"+installation+"/objects", cookie, string(payload))
}

func platformObjectCreateBody(binding map[string]string, content string) map[string]any {
	return map[string]any{
		"binding": binding, "action": "create", "requestId": "create-1",
		"collection": "note", "label": "First note", "mediaType": "application/json",
		"contentBase64": base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func platformPluginTableForInstallation(t *testing.T, st *Store, installation string) string {
	t.Helper()
	appID := scalar[string](t, st, `SELECT app_id FROM app_platform_installations WHERE id=?`, installation)
	table := platformPluginDataTableName(appID)
	if got := scalar[string](t, st, `SELECT table_name FROM app_platform_plugin_tables WHERE app_id=?`, appID); got != table {
		t.Fatalf("plugin table = %q, want %q", got, table)
	}
	return table
}

func TestPlatformObjectsCreateListGetUpdateDelete(t *testing.T) {
	s, _, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	list := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "list", "collection": "note"})
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("initial object list: %d %s", list.Code, list.Body.String())
	}
	create := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"first"}`))
	var created struct {
		Object platformObjectSummary `json:"object"`
	}
	if create.Code != http.StatusOK || json.Unmarshal(create.Body.Bytes(), &created) != nil || !platformValidUUID(created.Object.ID) || created.Object.Revision != 1 {
		t.Fatalf("create object: %d %s", create.Code, create.Body.String())
	}
	repeat := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"first"}`))
	var repeated struct {
		Object platformObjectSummary `json:"object"`
	}
	if repeat.Code != http.StatusOK || json.Unmarshal(repeat.Body.Bytes(), &repeated) != nil || repeated.Object.ID != created.Object.ID {
		t.Fatalf("idempotent object create: %d %s", repeat.Code, repeat.Body.String())
	}
	conflict := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"different"}`))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting idempotency key: %d", conflict.Code)
	}
	list = platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "list", "collection": "note"})
	var listed struct {
		Items []platformObjectSummary `json:"items"`
	}
	if list.Code != http.StatusOK || json.Unmarshal(list.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || listed.Items[0].ID != created.Object.ID {
		t.Fatalf("object list: %d %s", list.Code, list.Body.String())
	}
	get := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "get", "id": created.Object.ID})
	var got struct {
		Object        platformObjectSummary `json:"object"`
		ContentBase64 string                `json:"contentBase64"`
	}
	if get.Code != http.StatusOK || json.Unmarshal(get.Body.Bytes(), &got) != nil || got.ContentBase64 != base64.StdEncoding.EncodeToString([]byte(`{"body":"first"}`)) {
		t.Fatalf("get object: %d %s", get.Code, get.Body.String())
	}
	update := platformObjectCall(t, s, reader, installation, map[string]any{
		"binding": binding, "action": "update", "id": created.Object.ID, "expectedRevision": 1,
		"label": "Updated note", "mediaType": "application/json",
		"contentBase64": base64.StdEncoding.EncodeToString([]byte(`{"body":"updated"}`)),
	})
	var updated struct {
		Object platformObjectSummary `json:"object"`
	}
	if update.Code != http.StatusOK || json.Unmarshal(update.Body.Bytes(), &updated) != nil || updated.Object.Revision != 2 {
		t.Fatalf("update object: %d %s", update.Code, update.Body.String())
	}
	stale := platformObjectCall(t, s, reader, installation, map[string]any{
		"binding": binding, "action": "update", "id": created.Object.ID, "expectedRevision": 1,
		"label": "Stale", "mediaType": "application/json", "contentBase64": base64.StdEncoding.EncodeToString([]byte("stale")),
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale object update: %d", stale.Code)
	}
	deleteResponse := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "delete", "id": created.Object.ID, "expectedRevision": 2})
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete object: %d %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	list = platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "list", "collection": "note"})
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("object list after delete: %d %s", list.Code, list.Body.String())
	}
	if scalar[int64](t, s.st, `SELECT used_bytes FROM app_platform_object_storage WHERE id=1`) != 0 || scalar[int64](t, s.st, `SELECT used_objects FROM app_platform_object_storage WHERE id=1`) != 0 {
		t.Fatal("object counters did not return to zero")
	}
}

func TestPlatformObjectsCrossAccountAndBindingIsolation(t *testing.T) {
	s, _, first, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, first, installation)
	create := platformObjectCall(t, s, first, installation, platformObjectCreateBody(binding, `{"private":true}`))
	var created struct {
		Object platformObjectSummary `json:"object"`
	}
	if create.Code != http.StatusOK || json.Unmarshal(create.Body.Bytes(), &created) != nil {
		t.Fatal(create.Code, create.Body.String())
	}
	if err := s.st.UpsertUser(User{Username: "objects-second-reader", PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	second := platformReportSession(t, s, "objects-second-reader")
	grantID, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": grantID, "installation_id": installation, "principal_type": "user", "username": second.username, "account_created": second.created, "account_generation": second.generation, "group_id": nil, "group_created": nil, "revision": 1, "created_ms": 1, "updated_ms": 1})
	secondBinding := platformObjectBindingMap(t, s, second, installation)
	list := platformObjectCall(t, s, second, installation, map[string]any{"binding": secondBinding, "action": "list", "collection": "note"})
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"items":[]`)) {
		t.Fatalf("second account saw first account objects: %d %s", list.Code, list.Body.String())
	}
	get := platformObjectCall(t, s, second, installation, map[string]any{"binding": secondBinding, "action": "get", "id": created.Object.ID})
	if get.Code != http.StatusNotFound {
		t.Fatalf("second account read first object: %d %s", get.Code, get.Body.String())
	}
	oldBinding := map[string]string{"generation": "2", "policyRevision": binding["policyRevision"], "releaseSha256": binding["releaseSha256"]}
	old := platformObjectCall(t, s, first, installation, map[string]any{"binding": oldBinding, "action": "get", "id": created.Object.ID})
	if old.Code != http.StatusNotFound {
		t.Fatalf("old object binding remained readable: %d", old.Code)
	}
}

func TestPlatformObjectsUninstallAndAccountDeletionClearData(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"delete with app"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(890)); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_object_storage WHERE id=1`) != 0 || s.st.tableExists(pluginTable) {
		t.Fatal("uninstall did not clear object data, counters and the plugin table")
	}

	s, _, reader, installation = platformObjectsFixture(t)
	binding = platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"delete with account"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable = platformPluginTableForInstallation(t, s.st, installation)
	if err := s.st.DeleteUser(reader.username); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM `+pluginTable+` WHERE owner_username=?`, reader.username) != 0 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_object_storage WHERE id=1`) != 0 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_plugin_tables WHERE table_name=?`, pluginTable) != 0 {
		t.Fatal("account deletion left object data or counters")
	}
}

func TestPlatformObjectUninstallRollsBackWhenAdmissionFails(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"must survive rollback"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `CREATE TRIGGER fixture_object_uninstall_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.operation_admitted' BEGIN SELECT RAISE(ABORT,'fixture object uninstall failure'); END`)
	defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_object_uninstall_failure`)
	if _, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(891)); err == nil {
		t.Fatal("failed uninstall admission committed")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM `+pluginTable) != 1 || !s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables WHERE table_name=?`, pluginTable) != 1 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_object_storage WHERE id=1`) <= 0 {
		t.Fatal("failed uninstall cleared object data or counters")
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND (action='uninstall' OR completed_ms IS NULL)`, installation) != 0 {
		t.Fatal("failed uninstall changed authority or queued work")
	}
}

func TestPlatformFrontendUninstallSchedulerClearsPluginDataAndAccess(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"clear on uninstall"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	// Object storage is a frontend provider and does not own a managed data
	// namespace. Remove the launch fixture's unrelated namespace before the
	// frontend uninstall lifecycle assertion.
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(892))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(893), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID)
	state := scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation)
	if stage != "complete" || state != "removed:removed" {
		var opEpoch, installationEpoch string
		var managed, source, target int64
		var runtime any
		if err := s.st.queryRow(`SELECT managed,source_generation,target_generation,controller_epoch FROM app_platform_operations WHERE id=?`, commit.OperationID).Scan(&managed, &source, &target, &opEpoch); err != nil {
			t.Fatal(err)
		}
		if err := s.st.queryRow(`SELECT controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`, installation).Scan(&installationEpoch, &runtime); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("frontend uninstall did not complete: stage=%s state=%s status=%+v managed=%d source=%d target=%d opEpoch=%s installEpoch=%s runtime=%v", stage, state, scheduler.currentStatus(), managed, source, target, opEpoch, installationEpoch, runtime)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, installation)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, installation) != 0 || s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 {
		t.Fatal("uninstall retained access or plugin data")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err == nil {
		t.Fatal("uninstalled app remained launchable")
	}
}

func TestPlatformFrontendUninstallRetryExhaustionRemainsUnavailable(t *testing.T) {
	s, admin, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"failed uninstall stays denied"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(894))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendUninstall(context.Background(), platformFixtureID(895))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("claim uninstall", selected, err)
	}
	if err := s.finishPlatformFrontendUninstallForReview(context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" || scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "uninstall_retry_exhausted" {
		t.Fatal("uninstall retry exhaustion was not recorded")
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "removed:pending" {
		t.Fatal("failed uninstall reopened the removed installation")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err == nil {
		t.Fatal("failed uninstall restored launch authority")
	}
}

func testPlatformFrontendUninstallConcurrentClaim(t *testing.T, pg bool) {
	s, admin, reader, installation := platformObjectsFixturePG(t, pg)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"concurrent uninstall claim"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(896))
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		selection platformInstallSelection
		err       error
	}
	results := make(chan outcome, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			selected, err := s.claimNextPlatformFrontendUninstall(context.Background(), platformFixtureID(897+i))
			results <- outcome{selected, err}
		})
	}
	wg.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.selection.lease.id != "" {
			claimed++
			if result.selection.lease.id != commit.OperationID {
				t.Fatal("wrong uninstall operation claimed")
			}
		}
	}
	if claimed != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.uninstall_claimed' AND target_id=?`, commit.OperationID) != 1 {
		t.Fatalf("concurrent workers claimed uninstall %d times", claimed)
	}
}

func TestPlatformFrontendUninstallConcurrentClaimSQLite(t *testing.T) {
	testPlatformFrontendUninstallConcurrentClaim(t, false)
}

func TestPlatformFrontendUninstallConcurrentClaimPostgres(t *testing.T) {
	testPlatformFrontendUninstallConcurrentClaim(t, true)
}

func testPlatformFrontendUninstallResumesAfterExpiredWorker(t *testing.T, pg bool) {
	s, admin, reader, installation := platformObjectsFixturePG(t, pg)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"resume uninstall after crash"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`, installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces WHERE owner_installation_id=?`, installation)
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(898))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.claimNextPlatformFrontendUninstall(context.Background(), platformFixtureID(899))
	if err != nil || first.lease.id != commit.OperationID {
		t.Fatal("initial uninstall claim", first, err)
	}
	advanced, err := s.st.advancePlatformFrontendUninstall(context.Background(), first.lease)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err = s.st.advancePlatformFrontendUninstall(context.Background(), advanced)
	if err != nil || advanced.snapshot.Stage != appplatform.StageRevoke {
		t.Fatal("advance before simulated crash", advanced, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, commit.OperationID)
	resumed, err := s.claimNextPlatformFrontendUninstall(context.Background(), platformFixtureID(900))
	if err != nil || resumed.lease.id != commit.OperationID || resumed.lease.snapshot.Stage != appplatform.StageRevoke || resumed.lease.fence <= advanced.fence {
		t.Fatal("expired uninstall was not reclaimed with a new fence", resumed, err)
	}
	if _, err := s.runPlatformFrontendUninstall(context.Background(), resumed.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" || scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, installation) != "removed:removed" {
		t.Fatal("resumed uninstall did not finish")
	}
	if s.st.tableExists(pluginTable) || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, installation)+scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=?`, installation) != 0 {
		t.Fatal("resumed uninstall retained data or authority")
	}
}

func TestPlatformFrontendUninstallResumesAfterExpiredWorkerSQLite(t *testing.T) {
	testPlatformFrontendUninstallResumesAfterExpiredWorker(t, false)
}

func TestPlatformFrontendUninstallResumesAfterExpiredWorkerPostgres(t *testing.T) {
	testPlatformFrontendUninstallResumesAfterExpiredWorker(t, true)
}

func TestPlatformObjectsPluginQuotaIsTransactional(t *testing.T) {
	s, _, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, "abc")); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	mustExec(t, s.st, `UPDATE app_platform_plugin_tables SET used_bytes=? WHERE table_name=?`, platformPluginMaxBytes, pluginTable)
	body := platformObjectCreateBody(binding, "x")
	body["requestId"] = "create-2"
	response := platformObjectCall(t, s, reader, installation, body)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("namespace object quota: %d %s", response.Code, response.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM `+pluginTable) != 1 {
		t.Fatal("quota refusal partially wrote an object")
	}
}

func testPlatformObjectSchemaCounters(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_object_storage WHERE id=1`) != 1 {
		t.Fatal("object storage singleton missing")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{migration0017(), migration0018()}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO app_platform_object_storage(id,max_bytes,max_objects,used_bytes,used_objects,revision,updated_ms) VALUES(2,1,1,0,0,1,1)`,
		`UPDATE app_platform_object_storage SET used_bytes=max_bytes+1`,
	} {
		if _, err := st.exec(q); err == nil {
			t.Fatalf("invalid object storage row accepted: %s", q)
		}
	}
}

func TestPlatformObjectSchemaCountersSQLite(t *testing.T) {
	testPlatformObjectSchemaCounters(t, false)
}

func TestPlatformObjectSchemaCountersPostgres(t *testing.T) {
	testPlatformObjectSchemaCounters(t, true)
}

func testPlatformObjectPluginTableMigration(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	e := st.poolExec()
	_, object := seedLegacyPlatformObjectStore(t, st)
	if err := st.applyMigration(migration0018()); err != nil {
		t.Fatal(err)
	}
	table := platformPluginDataTableName("migration-app")
	if !st.tableExists(table) || scalar[int](t, st, `SELECT COUNT(*) FROM `+table+` WHERE id=?`, object) != 1 || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_plugin_tables WHERE app_id='migration-app' AND table_name=?`, table) != 1 {
		t.Fatal("legacy object was not migrated into its plugin table")
	}
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_storage_objects`)+scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_object_namespaces`) != 0 || scalar[int64](t, st, `SELECT used_bytes FROM app_platform_object_storage WHERE id=1`) != scalar[int64](t, st, `SELECT size_bytes FROM `+table+` WHERE id=?`, object) {
		t.Fatal("legacy tables or counters were not retired atomically")
	}
	if err := st.verifyMigrationProducts(e, st.migrations()); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyPlatformObjectStore(t *testing.T, st *Store) (string, string) {
	t.Helper()
	e := st.poolExec()
	platformSeedPublisher(t, e, "plugins")
	installation := platformFixtureID(903)
	if err := platformSeedInstallation(t, e, installation, "plugins", "migration-app"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertUser(User{Username: "migration-reader", PasswordHash: "fixture", Role: "user", Active: true}); err != nil {
		t.Fatal(err)
	}
	// Rewind a database that already carries the immutable 0017 product to the
	// point immediately before 0018. The old tables and ledger must remain.
	dynamic, err := platformPluginDataTables(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range dynamic {
		mustExec(t, st, `DROP TABLE IF EXISTS `+table)
	}
	mustExec(t, st, `DROP TABLE IF EXISTS app_platform_plugin_tables`)
	mustExec(t, st, `DELETE FROM meta WHERE k='mig:0018'`)
	mustExec(t, st, `DELETE FROM app_platform_storage_objects`)
	mustExec(t, st, `DELETE FROM app_platform_object_namespaces`)
	mustExec(t, st, `UPDATE app_platform_object_storage SET used_bytes=0,used_objects=0,revision=revision+1 WHERE id=1`)
	content := []byte(`{"legacy":"object"}`)
	namespace := platformFixtureID(904)
	object := platformFixtureID(905)
	now := int64(100)
	mustExec(t, st, `INSERT INTO app_platform_object_namespaces(id,origin_installation_id,owner_installation_id,state,data_format,quota_bytes,observed_bytes,object_count,revision,deleted_ms,created_ms,updated_ms) VALUES(?,?,?,'active',1,?,?,1,2,NULL,?,?)`, namespace, installation, installation, platformObjectNamespaceBytes, len(content), now, now)
	mustExec(t, st, `INSERT INTO app_platform_storage_objects(id,namespace_id,owner_username,owner_account_created,owner_account_generation,collection,label,media_type,content,content_sha256,size_bytes,revision,idempotency_key,created_ms,updated_ms) VALUES(?,?,?,?,0,'note','Legacy','application/json',?,?,?,1,'legacy-create',?,?)`, object, namespace, "migration-reader", "2026-10-30", content, platformDigest(content), len(content), now, now)
	mustExec(t, st, `UPDATE app_platform_object_storage SET used_bytes=?,used_objects=1,revision=revision+1 WHERE id=1`, len(content))
	return installation, object
}

func testPlatformObjectLegacyDumpRestoreMigratesRows(t *testing.T, pg bool) {
	src := platformTestStore(t, pg)
	_, object := seedLegacyPlatformObjectStore(t, src)
	// dumpOf enumerates the current migration products. Recreate the empty
	// registry so the dump can be generated, then remove its section and ledger
	// row to make the fixture a truthful pre-0018 dump.
	for _, table := range platformObjectSchemaTables() {
		if table.name == "plugin_tables" {
			if _, err := src.poolExec().exec(table.ddl(src.driver)); err != nil {
				t.Fatal(err)
			}
		}
	}
	dump := withoutSections(t, dumpOf(t, src), migration0018().tables, []string{"0018"})
	var header backupHeader
	if err := json.NewDecoder(bytes.NewReader(dump)).Decode(&header); err != nil {
		t.Fatal(err)
	}
	for _, id := range header.Migrations {
		if id == "0018" {
			t.Fatal("legacy dump still claims 0018")
		}
	}
	var legacyTable, legacyObject bool
	for _, table := range header.Tables {
		legacyTable = legacyTable || table == platformPrefix+"storage_objects"
	}
	legacyObject = bytes.Contains(dump, []byte(object))
	if !legacyTable || !legacyObject {
		t.Fatal("legacy dump lost its 0017 object products")
	}
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	table := platformPluginDataTableName("migration-app")
	if !dst.tableExists(table) || scalar[int](t, dst, `SELECT COUNT(*) FROM `+table+` WHERE id=?`, object) != 1 || scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_storage_objects`) != 0 {
		t.Fatal("legacy dump did not migrate its object into the plugin table")
	}
	if err := dst.verifyMigrationProducts(dst.poolExec(), dst.migrations()); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformObjectLegacyDumpRestoreMigratesRowsSQLite(t *testing.T) {
	testPlatformObjectLegacyDumpRestoreMigratesRows(t, false)
}

func TestPlatformObjectLegacyDumpRestoreMigratesRowsPostgres(t *testing.T) {
	testPlatformObjectLegacyDumpRestoreMigratesRows(t, true)
}

func TestPlatformObjectPluginTableMigrationSQLite(t *testing.T) {
	testPlatformObjectPluginTableMigration(t, false)
}

func TestPlatformObjectPluginTableMigrationPostgres(t *testing.T) {
	testPlatformObjectPluginTableMigration(t, true)
}

func TestPlatformPluginStorageUsesOneTablePerApp(t *testing.T) {
	st := newTestStore(t)
	e := st.poolExec()
	platformSeedPublisher(t, e, "plugins")
	if err := platformSeedInstallation(t, e, platformFixtureID(901), "plugins", "notebook"); err != nil {
		t.Fatal(err)
	}
	if err := platformSeedInstallation(t, e, platformFixtureID(902), "plugins", "ledger"); err != nil {
		t.Fatal(err)
	}
	first, err := ensurePlatformPluginTable(e, "notebook")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensurePlatformPluginTable(e, "ledger")
	if err != nil {
		t.Fatal(err)
	}
	if first.TableName == second.TableName || !st.tableExists(first.TableName) || !st.tableExists(second.TableName) {
		t.Fatalf("plugins share a data table: %q %q", first.TableName, second.TableName)
	}
	mustExec(t, st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, platformFixtureID(901))
	now, err := platformDatabaseNow(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupPlatformInstallationObjects(e, platformFixtureID(901), "notebook", now); err != nil {
		t.Fatal(err)
	}
	if st.tableExists(first.TableName) || !st.tableExists(second.TableName) || scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_plugin_tables WHERE app_id='ledger'`) != 1 {
		t.Fatal("uninstall cleared the wrong plugin table")
	}
	mustExec(t, st, `UPDATE app_platform_installations SET desired_state='enabled',phase='pending' WHERE id=?`, platformFixtureID(901))
	reinstalled, err := ensurePlatformPluginTable(e, "notebook")
	if err != nil {
		t.Fatal(err)
	}
	if reinstalled.TableName != first.TableName || scalar[int](t, st, `SELECT COUNT(*) FROM `+reinstalled.TableName) != 0 {
		t.Fatal("reinstall did not create an empty plugin table")
	}
	if err := verifyPlatformObjectStorage(e); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformObjectBackupAndOlderDumpRestore(t *testing.T) {
	s, _, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"backup"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginTable := platformPluginTableForInstallation(t, s.st, installation)
	dump := dumpOf(t, s.st)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM `+pluginTable) != 1 || scalar[int](t, dst, `SELECT used_objects FROM app_platform_object_storage WHERE id=1`) != 1 || !dst.tableExists(pluginTable) {
		t.Fatal("new object backup did not restore content and counters")
	}

	olderTables := append(append([]string{}, migration0017().tables...), migration0018().tables...)
	older := withoutSections(t, dump, append(olderTables, pluginTable), []string{"0017", "0018"})
	oldTarget := newTestStore(t)
	if _, err := oldTarget.restoreFrom(bytes.NewReader(older), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, oldTarget, `SELECT COUNT(*) FROM app_platform_plugin_tables`) != 0 || oldTarget.tableExists(pluginTable) || scalar[int](t, oldTarget, `SELECT COUNT(*) FROM app_platform_object_storage WHERE id=1`) != 1 {
		t.Fatal("older object dump did not restore with an empty seeded store")
	}
}

func TestPlatformObjectsPostgres(t *testing.T) {
	s, _, reader, installation := platformObjectsFixturePG(t, true)
	binding := platformObjectBindingMap(t, s, reader, installation)
	created := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"postgres"}`))
	if created.Code != http.StatusOK {
		t.Fatalf("postgres object create: %d %s", created.Code, created.Body.String())
	}
	if err := verifyPlatformObjectStorage(s.st.poolExec()); err != nil {
		t.Fatalf("postgres plugin table verification: %v", err)
	}
	list := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "list", "collection": "note"})
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"label":"First note"`)) {
		t.Fatalf("postgres object list: %d %s", list.Code, list.Body.String())
	}
}
