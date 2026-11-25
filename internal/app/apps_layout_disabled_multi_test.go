package app

import (
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// stagePlatformFixtureRelease stages another signed release of the fixture app and returns its digest.
func stagePlatformFixtureRelease(t *testing.T, s *Server, session platformActorSession, version string) string {
	t.Helper()
	raw, _, archive := platformSignedReleaseFixture(t, version)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Capabilities.Required = []string{}
	manifest.Frontend.SHA256 = platformDigest(archive)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(session, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	return release.sha256
}

func insertFixtureActivation(t *testing.T, s *Server, n int, installation, release string, generation int, state string, updated int) {
	t.Helper()
	platformTestInsert(t, s.st.poolExec(), "activations", map[string]any{"id": platformFixtureID(700 + n), "installation_id": installation, "release_sha256": release, "generation": generation, "policy_revision": 1, "data_namespace_id": nil, "data_format": 1, "state": state, "readiness_evidence": "{}", "observed_ms": nil, "created_ms": updated, "updated_ms": updated})
}

// platformLaunchFixtureWithAdmin is platformLaunchFixture that also hands back the administrator session.
func platformLaunchFixtureWithAdmin(t *testing.T) (*Server, platformActorSession, string) {
	t.Helper()
	s, admin, release := platformInstallFixture(t, false)
	_, _, id := completePlatformLaunchFixture(t, s, admin, release.sha256)
	return s, admin, id
}

func TestLayoutDisabledAfterUpdateShowsTheUpdatedRelease(t *testing.T) {
	t.Parallel()
	s, session, id := platformLaunchFixtureWithAdmin(t)
	v2 := stagePlatformFixtureRelease(t, s, session, "1.0.0-beta.2")
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='retired',updated_ms=5 WHERE installation_id=?`, id)
	insertFixtureActivation(t, s, 1, id, v2, 2, "active", 5)
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=2 WHERE id=?`, id)
	if got := disabledLayoutEntry(t, s, id); got.Version != "1.0.0-beta.2" {
		t.Fatalf("active entry shows %q", got.Version)
	}
	disablePlatformFixture(t, s, id)
	if got := disabledLayoutEntry(t, s, id); got.Version != "1.0.0-beta.2" || got.Name == got.AppID {
		t.Fatalf("disabled entry must show the release it last ran: %+v", got)
	}
}

func TestLayoutDisabledAfterRollbackShowsTheRestoredRelease(t *testing.T) {
	t.Parallel()
	s, session, id := platformLaunchFixtureWithAdmin(t)
	v2 := stagePlatformFixtureRelease(t, s, session, "1.0.0-beta.2")
	var v1 string
	if err := s.st.queryRow(`SELECT release_sha256 FROM app_platform_activations WHERE installation_id=?`, id).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	// v1 ran, v2 ran and was retired, then v1 was restored with a later generation. The row inserted
	// last is the v2 one, so only the generation says which release the installation runs.
	mustExec(t, s.st, `UPDATE app_platform_activations SET state='retired',generation=1,updated_ms=1 WHERE installation_id=?`, id)
	insertFixtureActivation(t, s, 1, id, v2, 2, "retired", 9)
	insertFixtureActivation(t, s, 2, id, v1, 3, "active", 5)
	disablePlatformFixture(t, s, id)
	if got := disabledLayoutEntry(t, s, id); got.Version != "1.0.0-beta.1" {
		t.Fatalf("disabled entry shows %q, want the restored release", got.Version)
	}
}

func TestLayoutDisabledIgnoresAStagedCandidate(t *testing.T) {
	t.Parallel()
	s, session, id := platformLaunchFixtureWithAdmin(t)
	v2 := stagePlatformFixtureRelease(t, s, session, "1.0.0-beta.2")
	disablePlatformFixture(t, s, id)
	insertFixtureActivation(t, s, 1, id, v2, 2, "candidate", 9)
	if got := disabledLayoutEntry(t, s, id); got.Version != "1.0.0-beta.1" {
		t.Fatalf("a never-adopted candidate is shown: %q", got.Version)
	}
}
