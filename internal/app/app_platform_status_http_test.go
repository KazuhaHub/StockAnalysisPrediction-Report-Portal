package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func platformSeedStatusAudits(t *testing.T, s *Server, installation string, count int) time.Time {
	t.Helper()
	var operation string
	if err := s.st.queryRow(`SELECT id FROM app_platform_operations WHERE installation_id=? ORDER BY updated_ms DESC,id DESC LIMIT 1`, installation).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < count; i++ {
		action := "app_platform.operation_stage"
		stage := "probe"
		if i%2 == 1 {
			action = "app_platform.operation_admitted"
			stage = ""
		}
		detail := fmt.Sprintf(`{"stage":%q,"error_code":"safe_error","private":"reader-must-not-see","endpoint":"/run/private.sock"}`, stage)
		mustExec(t, s.st, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,0,?,'app_platform_operation',?,?,'127.0.0.1')`,
			base.Add(time.Duration(i)*time.Second).Format(time.RFC3339),
			"private-admin", action, operation, detail)
	}
	return base
}

func testPlatformInstallationStatusReaderProjection(t *testing.T, pg bool) {
	s, reader, installation := platformLaunchFixture(t, pg)
	if result, err := s.readPlatformInstallationStatus(context.Background(), reader, installation); err == nil || !reflect.DeepEqual(result, platformInstallationStatus{}) {
		t.Fatal("ungranted reader learned installation status")
	}
	platformGrantLaunchUser(t, s, reader, installation)
	base := platformSeedStatusAudits(t, s, installation, 25)
	mustExec(t, s.st, `UPDATE app_platform_operations SET state_revision=7 WHERE installation_id=?`, installation)
	result, err := s.readPlatformInstallationStatus(context.Background(), reader, installation)
	if err != nil || result.State != "installed" || !result.Openable || result.Action != "install" || result.Stage != "complete" || result.ControlPaused {
		t.Fatalf("installed status: %+v %v", result, err)
	}
	stageEvents := 0
	for _, event := range result.Timeline {
		if event.Stage == "probe" && event.ErrorCode == "safe_error" {
			stageEvents++
		}
	}
	if len(result.Timeline) != 20 || stageEvents < 10 {
		t.Fatalf("bounded reader timeline: %+v", result.Timeline)
	}
	if _, err := s.st.DeleteAuditBefore(base.Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	result, err = s.readPlatformInstallationStatus(context.Background(), reader, installation)
	if err != nil || len(result.Timeline) >= 20 {
		t.Fatalf("audit retention did not bound the timeline: %+v %v", result.Timeline, err)
	}

	mustExec(t, s.st, `UPDATE app_platform_installations SET phase='setup_required' WHERE id=?`, installation)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='activate',state_revision=6,error_code='install_execution_review_required',completed_ms=NULL WHERE installation_id=?`, installation)
	result, err = s.readPlatformInstallationStatus(context.Background(), reader, installation)
	if err != nil || result.State != "pending" || result.Openable || result.Action != "install" || result.Stage != "activate" || result.ErrorCode != "install_execution_review_required" {
		t.Fatalf("pending status: %+v %v", result, err)
	}

	mustExec(t, s.st, `UPDATE app_platform_installations SET desired_state='disabled',phase='disabled' WHERE id=?`, installation)
	result, err = s.readPlatformInstallationStatus(context.Background(), reader, installation)
	if err != nil || result.State != "disabled" || result.Openable {
		t.Fatalf("disabled status: %+v %v", result, err)
	}

	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	result, err = s.readPlatformInstallationStatus(context.Background(), reader, installation)
	if err != nil || !result.ControlPaused {
		t.Fatalf("paused status: %+v %v", result, err)
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation)
	if result, err = s.readPlatformInstallationStatus(context.Background(), reader, installation); err == nil || !reflect.DeepEqual(result, platformInstallationStatus{}) {
		t.Fatal("revoked audience retained status access")
	}
}

func TestPlatformInstallationStatusReaderProjectionSQLite(t *testing.T) {
	testPlatformInstallationStatusReaderProjection(t, false)
}

func TestPlatformInstallationStatusReaderProjectionPostgres(t *testing.T) {
	testPlatformInstallationStatusReaderProjection(t, true)
}

func TestPlatformInstallationStatusHTTPOmitsInternalIdentity(t *testing.T) {
	s, mux, reader, installation, cookie := platformHTTPFixture(t)
	platformSeedStatusAudits(t, s, installation, 1)
	var operation, lease, actor string
	if err := s.st.queryRow(`SELECT id,COALESCE(lease_owner,''),actor_scope FROM app_platform_operations WHERE installation_id=?`, installation).Scan(&operation, &lease, &actor); err != nil {
		t.Fatal(err)
	}
	response := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/installations/"+installation+"/status", cookie, "")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status route: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
	for _, hidden := range []string{installation, operation, lease, actor, reader.username, "private-admin", "reader-must-not-see", "/run/private.sock"} {
		if hidden != "" && strings.Contains(response.Body.String(), hidden) {
			t.Fatalf("reader status exposed internal identity %q: %s", hidden, response.Body.String())
		}
	}
	var result platformInstallationStatus
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.State != "installed" || !result.Openable {
		t.Fatalf("reader status json: %s %v", response.Body.String(), err)
	}
	if got := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/installations/"+installation+"/status", "", ""); got.Code != http.StatusUnauthorized {
		t.Fatal("anonymous status read")
	}
	for _, suffix := range []string{"?operation=secret", "?after=x", "?"} {
		if got := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/installations/"+installation+"/status"+suffix, cookie, ""); got.Code != http.StatusNotFound {
			t.Fatalf("status query %q: %d %s", suffix, got.Code, got.Body.String())
		}
	}
	mustExec(t, s.st, `DELETE FROM app_platform_access_grants WHERE installation_id=?`, installation)
	if got := sessionTestRequest(mux, http.MethodGet, "/api/app-platform/installations/"+installation+"/status", cookie, ""); got.Code != http.StatusNotFound || strings.Contains(got.Body.String(), "installed") {
		t.Fatal("revoked status remained visible")
	}
}
