package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformReenablePublisherRequest is the ordinary trust review request that turns one publisher back
// on, built from the publisher's current policy so that only the enabled flag changes.
func platformReenablePublisherRequest(t *testing.T, s *Server, publisher string) string {
	t.Helper()
	trust, err := loadPlatformPublisherTrust(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range trust.Publishers {
		if p.ID != publisher {
			continue
		}
		namespaces := make([]map[string]string, 0, len(p.Namespaces))
		for _, n := range p.Namespaces {
			namespaces = append(namespaces, map[string]string{"kind": n.Kind, "value": n.Value})
		}
		raw, _ := json.Marshal(map[string]any{"action": "policy", "publisher_id": publisher, "expected_revision": p.Revision, "enabled": true, "namespaces": namespaces})
		return string(raw)
	}
	t.Fatal("publisher not found")
	return ""
}

// A managed app resumed on a replacement runtime is an ordinary ready installation once recovery has
// ended and the administrator has trusted its publisher again: it launches, and the backend gateway
// admits a request for it. Until that trust decision it does neither.
func testResumedManagedAppLaunchesAndServesBackend(t *testing.T, pg bool) {
	// The restore moved the installation's policy revision past its retained capability rows, and this
	// fixture keeps them there: carrying them is the resume's job.
	f := platformResumeFixture(t, pg)
	actor, s := f.actor, f.s
	commit := f.reviewAndConsumeResume(t)
	startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	if done := platformResumeStartToReady(t, s, startLease, f.endpoint); done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the resume did not complete")
	}
	h := platformRecoveryActionHTTP(t, s, actor)
	h.endRecovery(200)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	launchPath := "/api/app-platform/installations/" + f.installation + "/launch"

	// Recovery has ended but the publisher is still disabled: nothing is served.
	if got := sessionTestRequest(mux, "GET", launchPath, cookie, ""); got.Code == 200 {
		t.Fatalf("a resumed app of an untrusted publisher launched: %s", got.Body.String())
	}
	if a, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, f.installation); err == nil {
		a.Close()
		t.Fatal("a resumed app of an untrusted publisher was admitted to the backend gateway")
	} else if !strings.Contains(err.Error(), "stored release") {
		// The refusal is the trust check on the release, not some other gap in the resumed state.
		t.Fatalf("the gateway refused an untrusted publisher for another reason: %v", err)
	}

	publisher := scalar[string](t, s.st, `SELECT publisher_id FROM app_platform_installations WHERE id=?`, f.installation)
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, platformReenablePublisherRequest(t, s, publisher)); got.Code != 200 {
		t.Fatalf("trust review: %d %s", got.Code, got.Body.String())
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "enabled:ready" {
		t.Fatal("trusting the publisher changed the resumed installation")
	}
	if got := sessionTestRequest(mux, "GET", launchPath, cookie, ""); got.Code != 200 {
		_, why := s.inspectPlatformLaunchAccess(actor, f.installation)
		t.Fatalf("a resumed managed app did not launch: %d %s (%v)", got.Code, got.Body.String(), why)
	}
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, f.installation)
	if err != nil {
		t.Fatal("a resumed managed app was refused by the backend gateway:", err)
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	policy := scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, f.installation)
	release := platformDigest(access.intent.start.writer.descriptor)
	access.Close()
	request, _ := json.Marshal(map[string]any{
		"binding": platformBackendRequestBinding{Generation: itoa(generation), PolicyRevision: itoa(policy), ReleaseSHA256: release},
		"method":  "POST", "path": "/v1/echo", "body": `{"value":"fixture"}`})
	// The test server has no runner endpoint, so an admitted request answers unavailable rather than
	// not found: the gateway accepted the binding of the resumed activation.
	got := platformBackendHTTPCall(mux, "/api/app-platform/installations/"+f.installation+"/backend", cookie, string(request), nil)
	if got.Code != 503 {
		t.Fatalf("the backend route did not admit the resumed activation: %d %s", got.Code, got.Body.String())
	}
}

func TestResumedManagedAppLaunchesAndServesBackendSQLite(t *testing.T) {
	testResumedManagedAppLaunchesAndServesBackend(t, false)
}
func TestResumedManagedAppLaunchesAndServesBackendPostgres(t *testing.T) {
	testResumedManagedAppLaunchesAndServesBackend(t, true)
}

// The resumed history is re-proven on every access: damage to any piece of the evidence it rests on
// refuses the backend gateway rather than serving a resume nobody can vouch for.
func testResumedManagedAppRefusedOnBrokenEvidence(t *testing.T, pg bool) {
	cases := map[string]string{
		"a capability row left at an older policy": `UPDATE app_platform_capability_grants SET policy_revision=policy_revision-1`,
		"a revoked import grant":                   `UPDATE app_platform_import_grants SET state='revoked',consumed_ms=NULL`,
		"an altered restore start":                 `UPDATE app_platform_operation_effects SET payload_sha256='` + platformDigest([]byte("other")) + `' WHERE kind='restore-start'`,
		"a lowered namespace writer fence":         `UPDATE app_platform_data_namespaces SET writer_fence=writer_fence-1`,
	}
	for name, statement := range cases {
		f := platformResumeFixture(t, pg)
		commit := f.reviewAndConsumeResume(t)
		startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
		platformResumeStartToReady(t, f.s, startLease, f.endpoint)
		platformRecoveryActionHTTP(t, f.s, f.actor).endRecovery(200)
		mustExec(t, f.s.st, `UPDATE app_platform_publishers SET enabled=1,revision=revision+1`)
		if a, err := f.s.preparePlatformWritableBackendAccess(context.Background(), f.actor, f.installation); err != nil {
			t.Fatalf("%s: the intact resume was refused: %v", name, err)
		} else {
			a.Close()
		}
		mustExec(t, f.s.st, statement)
		if a, err := f.s.preparePlatformWritableBackendAccess(context.Background(), f.actor, f.installation); err == nil {
			a.Close()
			t.Fatalf("a resumed app with %s was admitted", name)
		}
	}
}

func TestResumedManagedAppRefusedOnBrokenEvidenceSQLite(t *testing.T) {
	testResumedManagedAppRefusedOnBrokenEvidence(t, false)
}
func TestResumedManagedAppRefusedOnBrokenEvidencePostgres(t *testing.T) {
	testResumedManagedAppRefusedOnBrokenEvidence(t, true)
}

// An abandoned runtime that had applied its import leaves a consumed grant in the recovery set beside the
// replacement's own. The resume, the end of recovery and the resumed backend's history each name the grant
// by identity, so the retired runtime's grant does not make them refuse.
func testResumeAfterAbandonedAppliedImport(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	if _, err := f.abandon(t); err != nil {
		t.Fatal(err)
	}
	f.executor.mutate = func(r *journal.ImportCheckpointReceipt) { r.Claim.RootFilesystemUUID = platformFixtureID(78) }
	operation, err := f.reenrollAndImport(t, platformFixtureID(5102), platformFixtureID(5621))
	if err != nil || scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "complete" {
		t.Fatalf("the import on the new replacement did not complete: %v", err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_import_grants WHERE state='consumed'`) != 2 {
		t.Fatal("fixture: the recovery set does not carry two consumed grants")
	}
	f.rootUUID = platformFixtureID(78)
	s, actor := f.s, f.actor
	commit := f.reviewAndConsumeResume(t)
	startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	if done := platformResumeStartToReady(t, s, startLease, f.endpoint); done.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("the resume did not complete")
	}
	platformRecoveryActionHTTP(t, s, actor).endRecovery(200)
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())
	publisher := scalar[string](t, s.st, `SELECT publisher_id FROM app_platform_installations WHERE id=?`, f.installation)
	if got := sessionTestRequest(mux, "POST", "/api/admin/app-platform/publishers", cookie, platformReenablePublisherRequest(t, s, publisher)); got.Code != 200 {
		t.Fatalf("trust review: %d %s", got.Code, got.Body.String())
	}
	if got := sessionTestRequest(mux, "GET", "/api/app-platform/installations/"+f.installation+"/launch", cookie, ""); got.Code != 200 {
		_, why := s.inspectPlatformLaunchAccess(actor, f.installation)
		t.Fatalf("a resumed app did not launch after an abandoned applied import: %d %s (%v)", got.Code, got.Body.String(), why)
	}
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, f.installation)
	if err != nil {
		t.Fatal("the resumed backend history was refused:", err)
	}
	access.Close()
}

func TestResumeAfterAbandonedAppliedImportSQLite(t *testing.T) {
	testResumeAfterAbandonedAppliedImport(t, false)
}
func TestResumeAfterAbandonedAppliedImportPostgres(t *testing.T) {
	testResumeAfterAbandonedAppliedImport(t, true)
}
