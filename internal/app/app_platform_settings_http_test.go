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
