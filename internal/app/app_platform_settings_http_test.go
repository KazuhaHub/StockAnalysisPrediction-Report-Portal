package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func testPlatformInstallationSettingsHTTP(t *testing.T, pg bool) {
	t.Helper()
	s, admin, reader, installation := platformObjectsFixturePG(t, pg)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(admin.username), admin.sessionID, admin.expires.Unix())
	path := "/api/admin/app-platform/installations/" + installation + "/settings"
	response := sessionTestRequest(mux, "GET", path, cookie, "")
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var settings platformInstallationSettings
	if err := json.Unmarshal(response.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	release := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	if settings.InstallationID != installation ||
		settings.AppID != "notes-sample" ||
		settings.DesiredState != "enabled" ||
		settings.Phase != "ready" ||
		settings.CurrentReleaseSHA256 != release ||
		settings.Backend.Required ||
		settings.Data == nil ||
		!platformValidUUID(settings.Data.ID) ||
		settings.Data.State != "active" ||
		settings.Recovery.Count != 0 ||
		settings.RepairEligible ||
		len(settings.ConfigurationGaps) != 0 {
		t.Fatalf("unexpected settings projection: %+v", settings)
	}
	if len(settings.Audience) != 1 ||
		settings.Audience[0].Type != "user" ||
		settings.Audience[0].ID != reader.username {
		t.Fatalf("audience projection: %+v", settings.Audience)
	}
	if len(settings.Capabilities) != 1 ||
		settings.Capabilities[0].Capability != "storage.files" ||
		!settings.Capabilities[0].Granted ||
		!settings.Capabilities[0].Required {
		t.Fatalf("capability projection: %+v", settings.Capabilities)
	}
	body := response.Body.String()
	for _, forbidden := range []string{"controller_epoch", "sealed_identity_seed", "policy_json", "verification_key_fingerprint"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("settings exposed %s: %s", forbidden, body)
		}
	}

	readerCookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	if got := sessionTestRequest(mux, "GET", path, readerCookie, ""); got.Code != http.StatusForbidden {
		t.Fatalf("non-admin settings read: %d %s", got.Code, got.Body.String())
	}
	missing := sessionTestRequest(mux, "GET", "/api/admin/app-platform/installations/"+platformFixtureID(1999)+"/settings", cookie, "")
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), `"app_platform_installation_missing"`) {
		t.Fatalf("missing installation: %d %s", missing.Code, missing.Body.String())
	}
}

func TestPlatformInstallationSettingsHTTPSQLite(t *testing.T) {
	testPlatformInstallationSettingsHTTP(t, false)
}

func TestPlatformInstallationSettingsHTTPPostgres(t *testing.T) {
	testPlatformInstallationSettingsHTTP(t, true)
}

func testPlatformInstallationSettingsRepairEligibility(
	t *testing.T, pg bool,
) {
	t.Helper()
	s, actor, enabled := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, enabled)
	settings, err := s.platformInstallationSettings(
		actor, enabled.installationID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.DesiredState != "disabled" ||
		settings.Phase != "disabled" ||
		!settings.RepairEligible ||
		settings.Data == nil ||
		settings.Data.State != "active" {
		t.Fatalf("repair eligibility projection: %+v", settings)
	}
}

func TestPlatformInstallationSettingsRepairEligibilitySQLite(t *testing.T) {
	testPlatformInstallationSettingsRepairEligibility(t, false)
}

func TestPlatformInstallationSettingsRepairEligibilityPostgres(t *testing.T) {
	testPlatformInstallationSettingsRepairEligibility(t, true)
}

func testPlatformInstallationSettingsRecovery(
	t *testing.T, pg bool,
) {
	t.Helper()
	s, actor, update, recoverySetID :=
		platformManagedRestoreReviewFixture(t, pg)
	settings, err := s.platformInstallationSettings(
		actor, update.installationID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.ControlPaused ||
		settings.Phase != "recovery_required" ||
		len(settings.Recovery.Points) != 1 ||
		settings.Recovery.Points[0].ID != recoverySetID ||
		!settings.Recovery.Points[0].RestoreEligible {
		t.Fatalf("restore projection: %+v", settings.Recovery)
	}
	mustExec(t, s.st, `UPDATE meta SET v='1'
		WHERE k='app_platform_restore_paused'`)
	settings, err = s.platformInstallationSettings(
		actor, update.installationID)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.ControlPaused ||
		settings.RepairEligible ||
		len(settings.Recovery.Points) != 1 ||
		settings.Recovery.Points[0].RestoreEligible {
		t.Fatalf("paused restore projection: %+v", settings)
	}
	foundGap := false
	for _, gap := range settings.ConfigurationGaps {
		foundGap = foundGap || gap == "control_paused"
	}
	if !foundGap {
		t.Fatalf("restore pause gap missing: %+v",
			settings.ConfigurationGaps)
	}
}

func TestPlatformInstallationSettingsRecoverySQLite(t *testing.T) {
	testPlatformInstallationSettingsRecovery(t, false)
}

func TestPlatformInstallationSettingsRecoveryPostgres(t *testing.T) {
	testPlatformInstallationSettingsRecovery(t, true)
}
