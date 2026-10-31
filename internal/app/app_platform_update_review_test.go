package app

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformUpdateFixture(t *testing.T, readable []int) (*Server, platformActorSession, string, platformStagedRelease) {
	return platformUpdateFixturePG(t, readable, false)
}

func platformUpdateFixturePG(t *testing.T, readable []int, pg bool) (*Server, platformActorSession, string, platformStagedRelease) {
	t.Helper()
	s, actor, source := platformInstallFixture(t, pg)
	review, err := s.createPlatformFrontendInstallReview(actor, source.sha256, 1, []platformAudienceTarget{{kind: "user", username: actor.username}})
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := s.consumePlatformInstallReview(actor, review.ID, review.SHA256, platformFixtureID(5301))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(admitted.OperationID, platformFixtureID(5302))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runPlatformStaticInstall(context.Background(), lease, platformRuntimeEndpoint{}); err != nil {
		t.Fatal(err)
	}
	raw, _, archive := platformSignedReleaseFixture(t, "1.0.0-beta.2")
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	manifest.Data.Schema = readable[len(readable)-1]
	manifest.Data.ReadableSchemas = append([]int(nil), readable...)
	manifest.Capabilities.Required = nil
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	target, err := s.stagePlatformRelease(actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	return s, actor, admitted.InstallationID, target
}

func TestPlatformUpdateReviewQueuesTargetGenerationWithoutBreakingCurrent(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	source := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=?`, installation) != 1 {
		t.Fatal("update review installed or queued before confirmation")
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5310))
	if err != nil {
		t.Fatal(err)
	}
	if commit.InstallationID != installation || commit.Generation != 2 || commit.Stage != appplatform.StageQueued || commit.Revision != 1 {
		t.Fatal("update admission", commit)
	}
	var action string
	var sourceGeneration, targetGeneration int64
	if err := s.st.queryRow(`SELECT action,source_generation,target_generation FROM app_platform_operations WHERE id=?`, commit.OperationID).Scan(&action, &sourceGeneration, &targetGeneration); err != nil {
		t.Fatal(err)
	} else if action != "update" || sourceGeneration != 1 || targetGeneration != 2 {
		t.Fatal("update operation tuple", action, sourceGeneration, targetGeneration)
	}
	if scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 1 || scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation) != source {
		t.Fatal("update admission changed the live generation")
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, installation) != "ready" {
		t.Fatal("update admission changed live phase")
	}
}

func TestPlatformUpdateReviewRefusesIncompatibleDataAndStaleSource(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{2})
	if _, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1); err == nil {
		t.Fatal("incompatible data upgrade was admitted")
	}
	s, actor, installation, target = platformUpdateFixture(t, []int{1})
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=2 WHERE id=?`, installation)
	if _, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1); err == nil {
		t.Fatal("stale installation generation admitted an update")
	}
}

func TestPlatformStaticUpdateActivatesCandidateAndRetiresSource(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	source := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5320))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5321))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runPlatformStaticUpdate(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 2 || scalar[int64](t, s.st, `SELECT policy_revision FROM app_platform_installations WHERE id=?`, installation) != 2 {
		t.Fatal("update did not activate target generation")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND release_sha256=? AND generation=2 AND policy_revision=2`, installation, target.sha256) != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='retired' AND release_sha256=?`, installation, source) != 1 {
		t.Fatal("update activation retirement tuple")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" || scalar[int64](t, s.st, `SELECT completed_ms FROM app_platform_operations WHERE id=?`, commit.OperationID) <= 0 {
		t.Fatal("update operation not complete")
	}
	var descriptor []byte
	if err := s.st.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, target.sha256).Scan(&descriptor); err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	if err := verifyPlatformLaunchCapabilitySnapshot(s.st.poolExec(), installation, target.sha256, epoch, 2, parsed.Manifest); err != nil {
		t.Fatalf("update capability snapshot: %v operation=%s plan=%s", err, scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID), scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, commit.OperationID))
	}
	binding, err := s.inspectPlatformLaunchAccess(actor, installation)
	if err != nil || binding.generation != "2" || binding.policyRevision != "2" || binding.releaseSHA != target.sha256 {
		t.Fatal("updated app cannot launch", binding, err)
	}
	var proof platformFrontendReadiness
	if err := json.Unmarshal([]byte(scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)), &proof); err != nil {
		t.Fatal(err)
	}
	proof.Generation = "1"
	rawProof, _ := json.Marshal(proof)
	mustExec(t, s.st, `UPDATE app_platform_activations SET readiness_evidence=? WHERE installation_id=? AND state='active'`, string(rawProof), installation)
	if _, err := s.inspectPlatformLaunchAccess(actor, installation); err == nil {
		t.Fatal("updated app launched with stale generation evidence")
	}
}

func TestPlatformFrontendUpdateSchedulerClaimsAndCompletes(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5330))
	if err != nil {
		t.Fatal(err)
	}
	worker := platformFixtureID(5331)
	scheduler, err := newPlatformInstallScheduler(s, platformRuntimeEndpoint{}, worker, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" || scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 2 {
		t.Fatal("scheduler did not complete the reviewed update")
	}
	if status := scheduler.currentStatus(); status.code != "complete" || status.operationID != commit.OperationID {
		t.Fatal("scheduler status", status)
	}
}

func TestPlatformFrontendUpdateRetryExhaustionKeepsSourceGeneration(t *testing.T) {
	s, actor, installation, target := platformUpdateFixture(t, []int{1})
	source := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation)
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5340))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendUpdate(context.Background(), platformFixtureID(5341))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("claim update", selected, err)
	}
	if err := s.finishPlatformUpdateForReview(context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" || scalar[string](t, s.st, `SELECT error_code FROM app_platform_operations WHERE id=?`, commit.OperationID) != "update_retry_exhausted" {
		t.Fatal("update failure was not closed")
	}
	if scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 1 || scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, installation) != source {
		t.Fatal("failed update changed the live source generation")
	}
	if binding, err := s.inspectPlatformLaunchAccess(actor, installation); err != nil || binding.generation != "1" || binding.releaseSHA != source {
		t.Fatal("failed update broke source launch", binding, err)
	}
}

func testPlatformFrontendUpdateConcurrentClaim(t *testing.T, pg bool) {
	s, actor, installation, target := platformUpdateFixturePG(t, []int{1}, pg)
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5350))
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
			selected, err := s.claimNextPlatformFrontendUpdate(context.Background(), platformFixtureID(5351+i))
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
				t.Fatal("wrong update operation claimed")
			}
		}
	}
	if claimed != 1 || scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_claimed' AND target_id=?`, commit.OperationID) != 1 {
		t.Fatalf("concurrent workers claimed update %d times", claimed)
	}
}

func TestPlatformFrontendUpdateConcurrentClaimSQLite(t *testing.T) {
	testPlatformFrontendUpdateConcurrentClaim(t, false)
}

func TestPlatformFrontendUpdateConcurrentClaimPostgres(t *testing.T) {
	testPlatformFrontendUpdateConcurrentClaim(t, true)
}

func testPlatformFrontendUpdateResumesAfterExpiredWorker(t *testing.T, pg bool) {
	s, actor, installation, target := platformUpdateFixturePG(t, []int{1}, pg)
	review, err := s.createPlatformFrontendUpdateReviewContext(context.Background(), actor, installation, target.sha256, 1)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformUpdateReview(actor, review.ID, review.SHA256, platformFixtureID(5370))
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.claimNextPlatformFrontendUpdate(context.Background(), platformFixtureID(5371))
	if err != nil || first.lease.id != commit.OperationID {
		t.Fatal("initial update claim", first, err)
	}
	advanced, err := s.st.advancePlatformUpdateStage(context.Background(), first.lease)
	if err != nil || advanced.snapshot.Stage != appplatform.StageValidate {
		t.Fatal("advance before simulated crash", advanced, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, commit.OperationID)
	resumed, err := s.claimNextPlatformFrontendUpdate(context.Background(), platformFixtureID(5372))
	if err != nil || resumed.lease.id != commit.OperationID || resumed.lease.snapshot.Stage != appplatform.StageValidate || resumed.lease.fence <= advanced.fence {
		t.Fatal("expired update was not reclaimed with a new fence", resumed, err)
	}
	if _, err := s.runPlatformStaticUpdate(context.Background(), resumed.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "complete" || scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, installation) != 2 {
		t.Fatal("resumed update did not finish")
	}
}

func TestPlatformFrontendUpdateResumesAfterExpiredWorkerSQLite(t *testing.T) {
	testPlatformFrontendUpdateResumesAfterExpiredWorker(t, false)
}

func TestPlatformFrontendUpdateResumesAfterExpiredWorkerPostgres(t *testing.T) {
	testPlatformFrontendUpdateResumesAfterExpiredWorker(t, true)
}
