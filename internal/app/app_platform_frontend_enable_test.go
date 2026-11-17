package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformFrontendDisabledFixture(t *testing.T, pg bool) (*Server, platformActorSession, platformActorSession, string) {
	t.Helper()
	s, admin, reader, installation := platformObjectsFixturePG(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_activations
		SET data_namespace_id=NULL,data_format=0 WHERE installation_id=?`,
		installation)
	mustExec(t, s.st, `DELETE FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`, installation)
	plan, err := s.createPlatformRemovalReview(
		admin, installation, appplatform.ActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformRemovalReview(
		admin, plan.ID, plan.SHA256, platformFixtureID(1290)); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendUninstall(
		context.Background(), platformFixtureID(1291))
	if err != nil || selected.lease.id == "" {
		t.Fatal("frontend disable claim", selected, err)
	}
	if _, err := s.runPlatformFrontendDisable(
		context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	return s, admin, reader, installation
}

func testPlatformFrontendEnableRestoresSameTuple(t *testing.T, pg bool) {
	s, actor, reader, installation := platformFrontendDisabledFixture(t, pg)
	before := scalar[string](t, s.st, `SELECT desired_state||':'||phase||':'||generation||':'||revision||':'||policy_revision||':'||control_fence
		FROM app_platform_installations WHERE id=?`, installation)
	review, err := s.createPlatformFrontendEnableReviewContext(
		context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	var plan platformFrontendEnablePlan
	if err := json.Unmarshal([]byte(review.JSON), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Format != platformFrontendEnableFormat ||
		plan.Action != appplatform.ActionEnable ||
		plan.SourceGeneration != 2 || plan.TargetGeneration != 3 ||
		plan.SourceActivationGeneration != 1 ||
		plan.SourceReleaseSHA256 == "" ||
		plan.SourcePolicyRevision != plan.TargetPolicyRevision ||
		plan.TargetDesiredState != "enabled" ||
		plan.DataEffect != "preserve" {
		t.Fatalf("invalid frontend enable plan: %+v", plan)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase||':'||generation||':'||revision||':'||policy_revision||':'||control_fence
		FROM app_platform_installations WHERE id=?`, installation) != before {
		t.Fatal("enable review mutated installation state")
	}
	commit, err := s.consumePlatformFrontendEnableReviewContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1292))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendEnable(
		context.Background(), platformFixtureID(1293))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("frontend enable claim", selected, err)
	}
	if _, err := s.runPlatformFrontendEnable(
		context.Background(), selected.lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`, installation) != "enabled:ready" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`,
			commit.OperationID) != "complete" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active' AND generation=3`,
			installation) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='retired' AND generation=1`,
			installation) != 1 {
		t.Fatal("frontend enable did not activate the same release tuple")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err != nil {
		t.Fatal("enabled app is not launchable", err)
	}
}

func testPlatformFrontendEnableRefusesGrantDrift(t *testing.T, pg bool) {
	s, actor, _, installation := platformFrontendDisabledFixture(t, pg)
	review, err := s.createPlatformFrontendEnableReviewContext(
		context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.consumePlatformFrontendEnableReviewContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1294)); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformFrontendEnable(
		context.Background(), platformFixtureID(1295))
	if err != nil || selected.lease.id == "" {
		t.Fatal("enable claim", selected, err)
	}
	mustExec(t, s.st, `UPDATE app_platform_access_grants
		SET revision=revision+1 WHERE installation_id=?`, installation)
	_, err = s.runPlatformFrontendEnable(context.Background(), selected.lease)
	if !errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("enable accepted grant drift: %v", err)
	}
	if err := s.finishPlatformFrontendEnableForReview(
		context.Background(), selected.lease); err != nil {
		t.Fatal("finish refused to close drifted enable", err)
	}
	if scalar[string](t, s.st, `SELECT desired_state||':'||phase
		FROM app_platform_installations WHERE id=?`, installation) != "disabled:disabled" ||
		scalar[string](t, s.st, `SELECT stage||':'||error_code
			FROM app_platform_operations WHERE id=?`, selected.lease.id) !=
			"failed:frontend_enable_retry_exhausted" ||
		scalar[int](t, s.st, `SELECT generation FROM app_platform_installations
			WHERE id=?`, installation) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active'`, installation) != 0 {
		t.Fatal("grant drift exposed the disabled app")
	}
}

func TestPlatformFrontendEnableSchedulerCompletesWithoutReinstall(t *testing.T) {
	s, actor, reader, installation := platformFrontendDisabledFixture(t, false)
	review, err := s.createPlatformFrontendEnableReviewContext(
		context.Background(), actor, installation)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformFrontendEnableReviewContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1297))
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := newPlatformInstallScheduler(
		s, platformRuntimeEndpoint{}, platformFixtureID(1298), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.currentStatus().code != "complete" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations
			WHERE id=?`, commit.OperationID) != "complete" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_installations
			WHERE id=? AND desired_state='enabled' AND phase='ready'`,
			installation) != 1 {
		t.Fatal("frontend enable scheduler did not complete")
	}
	if _, err := s.inspectPlatformLaunchAccess(reader, installation); err != nil {
		t.Fatal("scheduler-enabled app is not launchable", err)
	}
}

func TestPlatformFrontendEnableRestoresSameTupleSQLite(t *testing.T) {
	testPlatformFrontendEnableRestoresSameTuple(t, false)
}

func TestPlatformFrontendEnableRestoresSameTuplePostgres(t *testing.T) {
	testPlatformFrontendEnableRestoresSameTuple(t, true)
}

func TestPlatformFrontendEnableRefusesGrantDriftSQLite(t *testing.T) {
	testPlatformFrontendEnableRefusesGrantDrift(t, false)
}

func TestPlatformFrontendEnableRefusesGrantDriftPostgres(t *testing.T) {
	testPlatformFrontendEnableRefusesGrantDrift(t, true)
}
