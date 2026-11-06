package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func platformManagedRestoreReviewFixture(
	t *testing.T, pg bool,
) (*Server, platformActorSession, platformOperationLease, string) {
	t.Helper()
	s, actor, install := platformManagedAccessFixture(t, pg)
	s, updateLease := platformCheckpointUpdateForInstall(t, s, install)
	intent, err := s.preparePlatformCheckpoint(
		context.Background(), updateLease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformCheckpointObservationFixture(
		intent, platformCheckpointEndpointFixture())
	if _, err := s.commitPlatformCheckpoint(
		context.Background(), updateLease, intent,
		platformCheckpointEndpointFixture(), observation); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET stage='recovery_required',state_revision=7,
		row_revision=row_revision+1,error_code='update_recovery_required',
		completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=?`, now, now, updateLease.id)
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET phase='recovery_required',revision=revision+1,updated_ms=?
		WHERE id=?`, now, updateLease.installationID)
	recoverySetID := scalar[string](t, s.st,
		`SELECT id FROM app_platform_recovery_sets
		WHERE operation_id=? AND state='verified'`, updateLease.id)
	if !platformValidUUID(recoverySetID) {
		t.Fatal("verified recovery set missing")
	}
	return s, actor, updateLease, recoverySetID
}

func testPlatformManagedRestoreReview(t *testing.T, pg bool) {
	s, actor, updateLease, recoverySetID :=
		platformManagedRestoreReviewFixture(t, pg)
	review, err := s.createPlatformManagedRestoreReview(
		context.Background(), actor, updateLease.installationID,
		recoverySetID)
	if err != nil {
		t.Fatal("restore review", err)
	}
	var plan platformManagedRestorePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil ||
		plan.Format != platformManagedRestoreFormat ||
		plan.RecoverySetID != recoverySetID ||
		plan.CheckpointOperationID != updateLease.id ||
		plan.SourceGeneration != 1 || plan.TargetGeneration != 2 {
		t.Fatal("restore review projection", plan)
	}
	key := platformFixtureID(1700)
	commit, err := s.consumePlatformManagedRestoreReview(
		context.Background(), actor, review.ID, review.SHA256, key)
	if err != nil || commit.Replayed ||
		commit.InstallationID != updateLease.installationID ||
		commit.Generation != 2 || commit.Revision != 4 ||
		commit.Stage != appplatform.StageQuiesce {
		t.Fatal("restore review consume", commit, err)
	}
	if scalar[string](t, s.st, `SELECT action||':'||stage
		FROM app_platform_operations WHERE id=?`, commit.OperationID) !=
		"restore:quiesce" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM
			app_platform_review_plans WHERE id=? AND consumed_ms IS NOT NULL`,
			review.ID) != 1 {
		t.Fatal("restore review did not queue exact operation")
	}
	replay, err := s.consumePlatformManagedRestoreReview(
		context.Background(), actor, review.ID, review.SHA256, key)
	if err != nil || !replay.Replayed ||
		replay.OperationID != commit.OperationID {
		t.Fatal("restore review replay", replay, err)
	}
	if _, err := s.consumePlatformManagedRestoreReview(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1701)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatal("second restore key accepted", err)
	}
	selected, err := s.claimNextPlatformManagedRestore(
		context.Background(), platformFixtureID(1702))
	if err != nil || selected.lease.id != commit.OperationID ||
		selected.lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("restore queue claim", selected, err)
	}
}

func TestPlatformManagedRestoreReviewSQLite(t *testing.T) {
	testPlatformManagedRestoreReview(t, false)
}

func TestPlatformManagedRestoreReviewPostgres(t *testing.T) {
	testPlatformManagedRestoreReview(t, true)
}

func testPlatformManagedRestoreReviewRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(
		*testing.T, *Server, platformActorSession,
		platformOperationLease, string) error{
		"restore paused": func(
			t *testing.T, s *Server, _ platformActorSession,
			_ platformOperationLease, _ string,
		) error {
			mustExec(t, s.st, `UPDATE meta SET v='1'
				WHERE k='app_platform_restore_paused'`)
			return errAppPlatformControlPaused
		},
		"recovery unverified": func(
			t *testing.T, s *Server, _ platformActorSession,
			_ platformOperationLease, recoverySetID string,
		) error {
			mustExec(t, s.st, `UPDATE app_platform_recovery_sets
				SET state='preparing',manifest_sha256=NULL,verified_ms=NULL
				WHERE id=?`, recoverySetID)
			return errAppPlatformPlanChanged
		},
		"update not recovered": func(
			t *testing.T, s *Server, _ platformActorSession,
			update platformOperationLease, _ string,
		) error {
			mustExec(t, s.st, `UPDATE app_platform_operations
				SET stage='failed',completed_ms=1,error_code=NULL
				WHERE id=?`, update.id)
			return errAppPlatformPlanChanged
		},
		"installation not reviewing": func(
			t *testing.T, s *Server, _ platformActorSession,
			update platformOperationLease, _ string,
		) error {
			mustExec(t, s.st, `UPDATE app_platform_installations
				SET phase='ready' WHERE id=?`, update.installationID)
			return errAppPlatformPlanChanged
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, actor, update, recoverySetID :=
				platformManagedRestoreReviewFixture(t, pg)
			want := mutate(t, s, actor, update, recoverySetID)
			if _, err := s.createPlatformManagedRestoreReview(
				context.Background(), actor, update.installationID,
				recoverySetID); err == nil ||
				(want != nil && !errors.Is(err, want)) {
				t.Fatalf("restore review refusal: %v, want %v", err, want)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM
				app_platform_review_plans WHERE action='restore'`) != 0 {
				t.Fatal("refused restore review was persisted")
			}
		})
	}
}

func TestPlatformManagedRestoreReviewRefusalsSQLite(t *testing.T) {
	testPlatformManagedRestoreReviewRefusals(t, false)
}

func TestPlatformManagedRestoreReviewRefusalsPostgres(t *testing.T) {
	testPlatformManagedRestoreReviewRefusals(t, true)
}

func testPlatformManagedRestoreReviewStaleTrust(t *testing.T, pg bool) {
	s, actor, update, recoverySetID :=
		platformManagedRestoreReviewFixture(t, pg)
	review, err := s.createPlatformManagedRestoreReview(
		context.Background(), actor, update.installationID,
		recoverySetID)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_publishers
		SET revision=revision+1 WHERE id=(
			SELECT publisher_id FROM app_platform_installations
			WHERE id=?)`, update.installationID)
	if _, err := s.consumePlatformManagedRestoreReview(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1703)); !errors.Is(err, errAppPlatformPlanChanged) {
		t.Fatalf("stale restore trust: %v", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_operations WHERE action='restore'`) != 0 {
		t.Fatal("stale restore review queued work")
	}
}

func TestPlatformManagedRestoreReviewStaleTrustSQLite(t *testing.T) {
	testPlatformManagedRestoreReviewStaleTrust(t, false)
}

func TestPlatformManagedRestoreReviewStaleTrustPostgres(t *testing.T) {
	testPlatformManagedRestoreReviewStaleTrust(t, true)
}
