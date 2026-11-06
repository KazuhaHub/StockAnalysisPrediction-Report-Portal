package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// SQL history fixtures establish frontend read/refusal behavior only. Actual
// native installation followed by these HTTP reads has its separate Linux case.
func testPlatformWritableLaunchHTTP(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	mustExec(t, s.st, `UPDATE users SET role='user' WHERE username=?`, actor.username)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	launch := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+lease.installationID+"/launch", cookie, "")
	var payload struct {
		Source       string            `json:"source"`
		Capabilities []string          `json:"capabilities"`
		Binding      map[string]string `json:"binding"`
	}
	if launch.Code != 200 || json.Unmarshal(launch.Body.Bytes(), &payload) != nil || payload.Source == "" || payload.Binding["installationId"] != lease.installationID || payload.Binding["generation"] != "1" || payload.Binding["policyRevision"] != "1" || len(payload.Capabilities) != 0 {
		t.Fatal("writable launch metadata", launch.Code, launch.Body.String())
	}
	for _, forbidden := range []string{"sealed:", "private_key", "registration", "controller_uid", "root_filesystem"} {
		if strings.Contains(launch.Body.String(), forbidden) {
			t.Fatal("private metadata escaped", forbidden)
		}
	}
	doc := sessionTestRequest(mux, "GET", payload.Source, cookie, "")
	if doc.Code != 200 || !strings.Contains(doc.Body.String(), "Fixture") || doc.Header().Get("Cache-Control") != "no-store" || doc.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(doc.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") || strings.Contains(doc.Header().Get("Content-Security-Policy"), "allow-same-origin") {
		t.Fatal("writable document policy", doc.Code)
	}
	head := sessionTestRequest(mux, "HEAD", payload.Source, cookie, "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Content-Security-Policy") != doc.Header().Get("Content-Security-Policy") {
		t.Fatal("writable HEAD differs")
	}
	viewer, err := s.readPlatformLaunchViewerContext(context.Background(), actor, lease.installationID)
	if err != nil || viewer.Role != "user" || len(viewer.Subject) != 64 {
		t.Fatal("writable launch viewer", err)
	}
	directory, err := s.listPlatformApps(context.Background(), actor, "")
	if err != nil || len(directory.Items) != 1 || directory.Items[0].InstallationID != lease.installationID || directory.Items[0].State != "installed" || !directory.Items[0].Openable {
		t.Fatal("writable app not openable in directory", err)
	}
	for _, bad := range []string{payload.Source + "?token=forged", strings.Replace(payload.Source, "/1/1/", "/1/2/", 1), strings.Replace(payload.Source, "index.html", "%69ndex.html", 1)} {
		if got := sessionTestRequest(mux, "GET", bad, cookie, ""); got.Code != 404 || strings.Contains(got.Body.String(), "Fixture") {
			t.Fatal("stale or encoded writable document admitted", bad)
		}
	}
	if got := sessionTestRequest(mux, "GET", payload.Source, "", ""); got.Code != 401 {
		t.Fatal("anonymous writable document")
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	if got := sessionTestRequest(mux, "GET", payload.Source, cookie, ""); got.Code != 404 || strings.Contains(got.Body.String(), "Fixture") {
		t.Fatal("cached writable URL survived revocation")
	}
	directory, err = s.listPlatformApps(context.Background(), actor, "")
	if err != nil || len(directory.Items) != 0 {
		t.Fatal("revoked writable app stayed visible", err)
	}
}
func TestPlatformWritableLaunchHTTPSQLite(t *testing.T)   { testPlatformWritableLaunchHTTP(t, false) }
func TestPlatformWritableLaunchHTTPPostgres(t *testing.T) { testPlatformWritableLaunchHTTP(t, true) }

func testPlatformWritableLaunchRefusals(t *testing.T, pg bool) {
	for _, mode := range []string{"grant", "runtime", "key", "publisher", "account", "restore", "history", "namespace", "readonly", "archive", "policy"} {
		t.Run(mode, func(t *testing.T) {
			s, actor, lease := platformWritableAccessFixture(t, pg)
			binding, err := s.inspectPlatformLaunchAccess(actor, lease.installationID)
			if err != nil {
				t.Fatal(err)
			}
			// Authentication normally refreshes old session activity. Keep this
			// fixture current so the full dump comparison isolates launch reads.
			mustExec(t, s.st, `UPDATE user_sessions SET last_seen=? WHERE id=?`, time.Now().Unix(), actor.sessionID)
			s.touchSeen(actor.username, time.Now())
			cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
			mux := http.NewServeMux()
			s.wireRoutes(mux)
			switch mode {
			case "grant":
				mustExec(t, s.st, `DELETE FROM app_platform_access_grants`)
			case "runtime":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
			case "key":
				mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET sealed_control_key='sealed:invalid'`)
			case "publisher":
				mustExec(t, s.st, `UPDATE app_platform_publishers SET enabled=0`)
			case "account":
				mustExec(t, s.st, `UPDATE users SET active=0 WHERE username=?`, actor.username)
			case "restore":
				mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
			case "history":
				mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE stage='start'`)
			case "namespace":
				mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET state='retained'`)
			case "readonly":
				mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=REPLACE(plan_json,?,?)`, platformWritableInstallFormat, platformManagedInstallFormat)
			case "archive":
				archive := scalar[[]byte](t, s.st, `SELECT content FROM app_platform_frontend_artifacts`)
				mustExec(t, s.st, `UPDATE app_platform_frontend_artifacts SET content=?`, bytes.Repeat([]byte{1}, len(archive)))
			case "policy":
				mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2`)
			}
			before := dumpOf(t, s.st)
			for _, path := range []string{"/api/app-platform/installations/" + lease.installationID + "/launch", platformDocumentSource(binding)} {
				got := sessionTestRequest(mux, "GET", path, cookie, "")
				if got.Code == 200 || strings.Contains(got.Body.String(), "Fixture") || got.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("invalid writable history exposed document", mode, got.Code)
				}
			}
			if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
				t.Fatal("refused writable read changed storage")
			}
		})
	}
}
func TestPlatformWritableLaunchRefusalsSQLite(t *testing.T) {
	testPlatformWritableLaunchRefusals(t, false)
}
func TestPlatformWritableLaunchRefusalsPostgres(t *testing.T) {
	testPlatformWritableLaunchRefusals(t, true)
}

func testPlatformWritableLaunchOU(t *testing.T, pg bool) {
	s, actor, lease := platformWritableAccessFixture(t, pg)
	parent, err := s.st.CreateUserGroup("Writable document parent", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.st.CreateUserGroup("Writable document child", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	originalGroup := scalar[int64](t, s.st, `SELECT COALESCE(group_id,0) FROM users WHERE username=?`, actor.username)
	t.Cleanup(func() {
		mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
		mustExec(t, s.st, `UPDATE users SET group_id=NULLIF(?,0) WHERE username=?`, originalGroup, actor.username)
		mustExec(t, s.st, `DELETE FROM user_groups WHERE id=?`, child)
		mustExec(t, s.st, `DELETE FROM user_groups WHERE id=?`, parent)
	})
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if _, err := s.st.SetPrimaryGroups([]string{actor.username}, child); err != nil {
		t.Fatal(err)
	}
	created := scalar[string](t, s.st, `SELECT created_at FROM user_groups WHERE id=?`, parent)
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	platformTestInsert(t, s.st.poolExec(), "access_grants", map[string]any{"id": platformFixtureID(991), "installation_id": lease.installationID, "principal_type": "ou", "username": nil, "account_created": nil, "account_generation": nil, "group_id": parent, "group_created": created, "revision": 1, "created_ms": 1, "updated_ms": 1})
	mustExec(t, s.st, `UPDATE app_platform_installations SET policy_revision=2 WHERE id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET policy_revision=2 WHERE installation_id=?`, lease.installationID)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	response := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+lease.installationID+"/launch", cookie, "")
	var launch struct {
		Source string `json:"source"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &launch) != nil || !strings.Contains(launch.Source, "/1/2/") {
		t.Fatal("ancestor OU writable launch", response.Code)
	}
	if doc := sessionTestRequest(mux, "GET", launch.Source, cookie, ""); doc.Code != 200 {
		t.Fatal("ancestor OU document", doc.Code)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=NULL WHERE id=?`, child)
	if doc := sessionTestRequest(mux, "GET", launch.Source, cookie, ""); doc.Code != 404 {
		t.Fatal("reparented OU retained writable document", doc.Code)
	}
	directory, err := s.listPlatformApps(context.Background(), actor, "")
	if err != nil || len(directory.Items) != 0 {
		t.Fatal("reparented OU leaked writable app", err)
	}
	mustExec(t, s.st, `UPDATE user_groups SET parent_id=? WHERE id=?`, parent, child)
	if doc := sessionTestRequest(mux, "GET", launch.Source, cookie, ""); doc.Code != 200 {
		t.Fatal("restored current OU grant not evaluated", doc.Code)
	}
}
func TestPlatformWritableLaunchOUSQLite(t *testing.T)   { testPlatformWritableLaunchOU(t, false) }
func TestPlatformWritableLaunchOUPostgres(t *testing.T) { testPlatformWritableLaunchOU(t, true) }

func TestPlatformWritableLaunchReadBoundarySQLite(t *testing.T) {
	s, actor, lease := platformWritableAccessFixture(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := dumpOf(t, s.st)
	called := false
	binding, err := s.withPlatformLaunchReadContext(ctx, actor, lease.installationID, func(migExec, platformLaunchAccess, appplatform.Manifest) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || binding != (platformLaunchAccess{}) || called {
		t.Fatal("canceled writable read escaped", err)
	}
	sentinel := errors.New("fixture buffered read refusal")
	binding, err = s.withPlatformLaunchReadContext(context.Background(), actor, lease.installationID, func(_ migExec, b platformLaunchAccess, m appplatform.Manifest) error {
		if m.Backend == nil || b.installationID != lease.installationID {
			t.Fatal("wrong concrete launch family")
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || binding != (platformLaunchAccess{}) {
		t.Fatal("failed callback released launch binding", err)
	}
	if !bytes.Equal(stripHeader(before), stripHeader(dumpOf(t, s.st))) {
		t.Fatal("failed direct read changed storage")
	}
}
