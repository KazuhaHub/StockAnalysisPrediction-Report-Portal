package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A remove_image row must be invisible to every installation-wide effect reader that ignores load_image rows, so the
// removal step can never hold back a start-bearing gate. Each test fails if its reader starts counting the row, and
// the load_image tests beside them keep failing if the exclusion widened to every kind.

func TestManagedAccessHistoryCountIgnoresARemoveImageRow(t *testing.T) {
	s, actor, lease := platformManagedAccessFixture(t, false)
	addEffectRow(t, s, lease.id, "remove_image", "stage", 2, platformFixtureID(9601))
	if a, err := s.preparePlatformManagedBackendAccess(context.Background(), actor, lease.installationID); err != nil {
		t.Fatal("a remove_image row blocked managed access:", err)
	} else {
		a.Close()
	}
}

func TestCandidateIntentEffectCountIgnoresARemoveImageRow(t *testing.T) {
	s, lease := platformCandidateIntentFixture(t, false)
	addEffectRow(t, s, lease.id, "remove_image", "stage", 2, platformFixtureID(9602))
	if _, err := s.preparePlatformInstallCandidate(context.Background(), lease); err != nil {
		t.Fatal("a remove_image row blocked the candidate intent:", err)
	}
}

func TestInstallAdmissionResourceCountIgnoresARemoveImageRow(t *testing.T) {
	s, _, lease := platformInstallStageFixture(t, false)
	addEffectRow(t, s, lease.id, "remove_image", "stage", 2, platformFixtureID(9603))
	e := s.st.poolExec()
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformAdmittedInstall(e, lease, trust, now); err != nil {
		t.Fatal("a remove_image row blocked the install:", err)
	}
}

func TestRemovalSelectionAdvanceRuleToleratesARemoveImageRow(t *testing.T) {
	s, actor, enabled := testPlatformManagedEnableSyntheticStart(t, false)
	review, err := s.createPlatformRemovalReview(actor, enabled.installationID, appplatform.ActionDisable)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformRemovalReview(actor, review.ID, review.SHA256, platformFixtureID(9604))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT o.id,'stage',2,?,'remove_image',?,i.runtime_id,o.controller_epoch,1,0,'unknown','{}',o.created_ms,o.updated_ms
		FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id WHERE o.id=?`, platformFixtureID(9605), platformDigest([]byte("remove_image")), commit.OperationID)
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations o WHERE o.id=? AND `+platformRemovalAdvanceRule, commit.OperationID) == 0 {
		t.Fatal("an unsettled remove_image row held the removal back")
	}
}

func TestEnableOperationCompletesWithARemoveImageRow(t *testing.T) {
	platformTestAfterEnableClaim = func(t *testing.T, s *Server, lease platformOperationLease) {
		addEffectRow(t, s, lease.id, "remove_image", "stage", 2, platformFixtureID(9606))
	}
	defer func() { platformTestAfterEnableClaim = nil }()
	testPlatformManagedEnableSecondCycle(t, false)
}
