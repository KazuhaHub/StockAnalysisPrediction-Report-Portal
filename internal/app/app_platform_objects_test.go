package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	plan, err := s.createPlatformRemovalReview(admin, installation, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(admin, plan.ID, plan.SHA256, platformFixtureID(890)); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_storage_objects`) != 0 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_object_storage WHERE id=1`) != 0 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_object_namespaces WHERE state='deleted' AND owner_installation_id IS NULL`) != 1 {
		t.Fatal("uninstall did not clear object data and counters")
	}

	s, _, reader, installation = platformObjectsFixture(t)
	binding = platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"delete with account"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if err := s.st.DeleteUser(reader.username); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_storage_objects WHERE owner_username=?`, reader.username) != 0 || scalar[int64](t, s.st, `SELECT used_bytes+used_objects FROM app_platform_object_storage WHERE id=1`) != 0 {
		t.Fatal("account deletion left object data or counters")
	}
}

func TestPlatformObjectsNamespaceQuotaIsTransactional(t *testing.T) {
	s, _, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, "abc")); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	namespace := scalar[string](t, s.st, `SELECT id FROM app_platform_object_namespaces WHERE owner_installation_id=?`, installation)
	if namespace == "" {
		t.Fatal("object namespace not created")
	}
	mustExec(t, s.st, `UPDATE app_platform_object_namespaces SET quota_bytes=3 WHERE id=?`, namespace)
	body := platformObjectCreateBody(binding, "x")
	body["requestId"] = "create-2"
	response := platformObjectCall(t, s, reader, installation, body)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("namespace object quota: %d %s", response.Code, response.Body.String())
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_storage_objects WHERE namespace_id=?`, namespace) != 1 {
		t.Fatal("quota refusal partially wrote an object")
	}
}

func testPlatformObjectSchemaCounters(t *testing.T, pg bool) {
	st := platformTestStore(t, pg)
	if scalar[int](t, st, `SELECT COUNT(*) FROM app_platform_object_storage WHERE id=1`) != 1 {
		t.Fatal("object storage singleton missing")
	}
	if err := st.verifyMigrationProducts(st.poolExec(), []migration{migration0017()}); err != nil {
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

func TestPlatformObjectBackupAndOlderDumpRestore(t *testing.T) {
	s, _, reader, installation := platformObjectsFixture(t)
	binding := platformObjectBindingMap(t, s, reader, installation)
	if response := platformObjectCall(t, s, reader, installation, platformObjectCreateBody(binding, `{"body":"backup"}`)); response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	dump := dumpOf(t, s.st)
	dst := newTestStore(t)
	if _, err := dst.restoreFrom(bytes.NewReader(dump), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, dst, `SELECT COUNT(*) FROM app_platform_storage_objects`) != 1 || scalar[int](t, dst, `SELECT used_objects FROM app_platform_object_storage WHERE id=1`) != 1 {
		t.Fatal("new object backup did not restore content and counters")
	}

	older := withoutSections(t, dump, migration0017().tables, []string{"0017"})
	oldTarget := newTestStore(t)
	if _, err := oldTarget.restoreFrom(bytes.NewReader(older), true); err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, oldTarget, `SELECT COUNT(*) FROM app_platform_storage_objects`) != 0 || scalar[int](t, oldTarget, `SELECT COUNT(*) FROM app_platform_object_storage WHERE id=1`) != 1 {
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
	list := platformObjectCall(t, s, reader, installation, map[string]any{"binding": binding, "action": "list", "collection": "note"})
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"label":"First note"`)) {
		t.Fatalf("postgres object list: %d %s", list.Code, list.Body.String())
	}
}
