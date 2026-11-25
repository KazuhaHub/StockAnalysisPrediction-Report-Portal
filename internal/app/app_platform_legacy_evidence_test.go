package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Readiness evidence written before access became an OU policy (doc 190, D7) carries the digest of the
// install-time audience as the old code computed it: the canonical JSON of the granted principals. Access
// is not install evidence any more, so that component is no longer compared and an installation activated
// before the upgrade must keep launching and keep verifying for an update.
func TestPlatformLaunchAndUpdateVerifyEvidenceWrittenWithAnOldAudienceDigest(t *testing.T) {
	t.Parallel()
	s, actor, id := platformFrontendQueuedFixture(t, false)
	w, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, platformFixtureID(953), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.tick(context.Background()); err != nil || w.currentStatus().code != "complete" {
		t.Fatal("install did not complete", err, w.currentStatus())
	}
	installation := scalar[string](t, s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, id)

	// The previous code's computation: the digest of the audience as a list of granted principals.
	oldAudience, _ := json.Marshal([]platformInstallAudience{{Type: "user", Username: actor.username, AccountCreated: actor.created, AccountGeneration: "1"}})
	var proof platformFrontendReadiness
	raw := scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	if err := json.Unmarshal([]byte(raw), &proof); err != nil {
		t.Fatal(err)
	}
	if proof.AudienceSHA == platformDigest(oldAudience) {
		t.Fatal("the fixture already carries the old digest")
	}
	proof.AudienceSHA = platformDigest(oldAudience)
	legacy, _ := json.Marshal(proof)
	mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=? WHERE installation_id=? AND state='active'`, string(legacy), installation)

	if _, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil {
		t.Fatal("launch refused evidence with an old audience digest:", err)
	}

	e := s.st.poolExec()
	var release string
	var generation, policy, observed int64
	if err := s.st.queryRow(`SELECT release_sha256,generation,policy_revision,observed_ms FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation).Scan(&release, &generation, &policy, &observed); err != nil {
		t.Fatal(err)
	}
	descriptor, err := platformReleaseDescriptor(e, release)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_controller_epoch'`)
	if err := verifyPlatformFrontendActivation(e, installation, release, epoch, generation, policy, observed, string(legacy), parsed.Manifest); err != nil {
		t.Fatal("the update source check refused evidence with an old audience digest:", err)
	}
}

// An install admitted before the upgrade whose stored plan names a user principal can never activate:
// the claim finalises it for review instead of failing on every tick.
func TestAdmittedInstallWithAUserPrincipalIsFinalisedForReview(t *testing.T) {
	t.Parallel()
	s, actor, id := platformFrontendQueuedFixture(t, false)
	if _, err := s.st.claimPlatformOperation(id, platformFixtureID(955)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=1 WHERE id=?`, id)
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, id)
	var plan platformInstallProjection
	if err := json.Unmarshal([]byte(scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=?`, planID)), &plan); err != nil {
		t.Fatal(err)
	}
	plan.Audience = []platformInstallAudience{{Type: "user", Username: actor.username, AccountCreated: actor.created, AccountGeneration: "1"}}
	encoded, _ := json.Marshal(plan)
	sha := platformDigest(encoded)
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, sha})
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE id=?`, string(encoded), sha, planID)
	mustExec(t, s.st, `UPDATE app_platform_operations SET request_sha256=? WHERE id=?`, platformDigest(request), id)

	selection, err := s.claimNextPlatformInstallForFormat(context.Background(), platformFixtureID(956), platformFrontendInstallFormat)
	if err != nil || !selection.reviewed || selection.operationID != id {
		t.Fatalf("the stale plan was not finalised for review: %+v %v", selection, err)
	}
	if scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, id) != "install_authority_review_required" {
		t.Fatal("the install was not closed with the authority review code")
	}
}
