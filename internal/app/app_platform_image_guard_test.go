package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// addEffectRow puts one extra applied effect row on an operation.
func addEffectRow(t *testing.T, s *Server, operation, kind, stage string, number int, id string) {
	t.Helper()
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT id,?,?,?,?,?,NULL,controller_epoch,MAX(fence,1),MAX(fence,1),'applied','{}',created_ms,updated_ms FROM app_platform_operations WHERE id=?`,
		stage, number, id, kind, platformDigest([]byte(id)), operation)
}

// The exact-history reader that demands exactly three effects (count != 3) must ignore a load_image row and must
// still refuse any other fourth effect. Fails if the load blocks managed access, or if the exclusion widened to
// every kind.
func TestManagedAccessHistoryCountIgnoresALoadImageRowButNotAnotherKind(t *testing.T) {
	s, actor, lease := platformManagedAccessFixture(t, false)
	if a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID); err != nil {
		t.Fatal(err)
	} else {
		a.Close()
	}
	addEffectRow(t, s, lease.id, "load_image", "stage", 1, platformFixtureID(9101))
	if a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID); err != nil {
		t.Fatal("a load_image row blocked managed access:", err)
	} else {
		a.Close()
	}
	addEffectRow(t, s, lease.id, "probe", "stage", 2, platformFixtureID(9102))
	if a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID); err == nil {
		a.Close()
		t.Fatal("an unexpected fourth effect of another kind no longer refuses")
	}
}

// The candidate intent requires exactly the data effect before it creates its own (effects == 1). Fails if a
// load_image row blocks the install's first start, or if the exclusion stopped refusing an effect of another kind.
func TestCandidateIntentEffectCountIgnoresALoadImageRowButNotAnotherKind(t *testing.T) {
	s, lease := platformCandidateIntentFixture(t, false)
	addEffectRow(t, s, lease.id, "load_image", "stage", 1, platformFixtureID(9201))
	if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err != nil {
		t.Fatal("a load_image row blocked the candidate intent:", err)
	}
	other, olease := platformCandidateIntentFixture(t, false)
	addEffectRow(t, other, olease.id, "probe", "stage", 2, platformFixtureID(9202))
	if _, err := other.preparePlatformInstallCandidate(context.Background(), olease); err == nil {
		t.Fatal("an unexpected effect of another kind no longer refuses the candidate intent")
	}
}

// The admitted-install check demands no resources at all (resources == 0). Fails if a load_image row counts as a
// resource, or if another kind stops counting.
func TestInstallAdmissionResourceCountIgnoresALoadImageRowButNotAnotherKind(t *testing.T) {
	check := func(kind string, number int, id string) error {
		s, _, lease := platformInstallStageFixture(t, false)
		addEffectRow(t, s, lease.id, kind, "stage", number, id)
		e := s.st.poolExec()
		trust, err := loadPlatformPublisherTrust(e)
		if err != nil {
			t.Fatal(err)
		}
		now, err := platformDatabaseNow(e)
		if err != nil {
			t.Fatal(err)
		}
		return validatePlatformAdmittedInstall(e, lease, trust, now)
	}
	if err := check("load_image", 1, platformFixtureID(9301)); err != nil {
		t.Fatal("a load_image row blocked the install:", err)
	}
	if err := check("probe", 2, platformFixtureID(9302)); err == nil {
		t.Fatal("an effect of another kind no longer blocks the install")
	}
}

// The selection of an in-progress disable or uninstall skips an operation with a not-yet-applied effect of a
// stage other than its own. A load_image row of stage "stage" (an enable or restore operation never passes
// through it) must not hold the operation back, and another kind still must.
func TestRemovalSelectionAdvanceRuleToleratesALoadImageRowOfStageStageButNotAnotherKind(t *testing.T) {
	held := func(kind, state string) bool {
		s, actor, enabled := testPlatformManagedEnableSyntheticStart(t, false)
		review, err := s.createPlatformRemovalReview(actor, enabled.installationID, appplatform.ActionDisable)
		if err != nil {
			t.Fatal(err)
		}
		commit, err := s.consumePlatformRemovalReview(actor, review.ID, review.SHA256, platformFixtureID(9401))
		if err != nil {
			t.Fatal(err)
		}
		mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT o.id,'stage',1,?,?,?,i.runtime_id,o.controller_epoch,1,0,?,'{}',o.created_ms,o.updated_ms
			FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.id=?`, platformFixtureID(9403), kind, platformDigest([]byte(kind)), state, commit.OperationID)
		return scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations o WHERE o.id=? AND `+platformRemovalAdvanceRule, commit.OperationID) == 0
	}
	if held("load_image", "unknown") {
		t.Fatal("a load_image row of stage stage held the removal back")
	}
	if !held("probe", "unknown") {
		t.Fatal("an unsettled effect of another kind no longer holds the removal back")
	}
}

// An enable operation never passes through stage "stage", yet its load_image row carries that stage. Fails if the
// enable's own stages (snapshot, start, probe, activate) or its exact-history readers refuse the operation
// because of a settled load row they do not expect.
func TestEnableOperationCompletesWithALoadImageRowOfStageStage(t *testing.T) {
	platformTestAfterEnableClaim = func(t *testing.T, s *Server, lease platformOperationLease) {
		addEffectRow(t, s, lease.id, "load_image", "stage", 1, platformFixtureID(9501))
	}
	defer func() { platformTestAfterEnableClaim = nil }()
	testPlatformManagedEnableSecondCycle(t, false)
}
