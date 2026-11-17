package app

import (
	"encoding/json"
	"net/http"
	"testing"
)

// platformPrincipalsFixture is a ready static installation whose install review
// granted principals.read. Only a required capability is granted by install
// today (an optional one stays off until the capability review of doc 175
// exists), so the fixture declares it required.
func platformPrincipalsFixture(t *testing.T) (*Server, platformActorSession, string) {
	t.Helper()
	s, admin := platformTrustTestServer(t, false)
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
	manifest["capabilities"] = map[string]any{"required": []string{"principals.read"}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(admin, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	s, reader, id := completePlatformLaunchFixture(t, s, admin, release.sha256)
	// Activation flips the reviewed required rows on; the fixture completes it directly.
	mustExec(t, s.st, `UPDATE app_platform_capability_grants SET granted=1 WHERE installation_id=? AND capability='principals.read'`, id)
	return s, reader, id
}

func TestPlatformPrincipalsReadDoesNotBlockStaticLaunch(t *testing.T) {
	s, reader, id := platformPrincipalsFixture(t)
	platformGrantLaunchUser(t, s, reader, id)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND capability='principals.read' AND granted=1 AND policy_json='{}'`, id) != 1 {
		t.Fatal("install did not grant the required capability with an empty policy")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, id); err != nil {
		t.Fatal("a granted principals.read refused the static launch", err)
	}
	cookie := s.managedCookie(*s.st.GetUser(reader.username), reader.sessionID, reader.expires.Unix())
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	launch := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+id+"/launch", cookie, "")
	if launch.Code != 200 {
		t.Fatal("launch", launch.Code, launch.Body.String())
	}
	var payload struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(launch.Body.Bytes(), &payload); err != nil || len(payload.Capabilities) != 1 || payload.Capabilities[0] != "principals.read" {
		t.Fatal("launch did not advertise the granted capability", launch.Body.String(), err)
	}
}
