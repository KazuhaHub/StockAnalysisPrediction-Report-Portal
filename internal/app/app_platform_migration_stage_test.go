package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func effectState(t *testing.T, s *Server, operation, stage string, number int) string {
	t.Helper()
	return scalar[string](t, s.st, `SELECT COALESCE((SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?),'')`, operation, stage, number)
}

func operationStage(t *testing.T, s *Server, operation string) string {
	t.Helper()
	return scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation)
}

// Fixture A: a compatible update with no migration. The migrate stage is the source removal alone and
// moves straight to the start, exactly as before the migration was introduced.
func testMigrateStageNoStepUnchanged(t *testing.T, pg bool) {
	s, lease, quiesce, _ := platformManagedUpdateQuiesceFixtureWith(t, pg, nil)
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), lease, quiesce, platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	migrate, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, checkpointEndpoint, platformCheckpointObservationFixture(checkpoint, checkpointEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	removal, err := s.preparePlatformManagedUpdateMigrate(context.Background(), migrate)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.commitPlatformManagedUpdateQuiesce(context.Background(), migrate, removal, platformManagedUpdateQuiesceReceipt(removal))
	if err != nil || next.snapshot.Stage != appplatform.StageStart || operationStage(t, s, lease.id) != "start" {
		t.Fatalf("a plan without a migration did not move to the start: %v %v", next.snapshot.Stage, err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate'`, lease.id) != 1 {
		t.Fatal("a plan without a migration recorded a migration effect")
	}
}

func TestMigrateStageNoStepUnchangedSQLite(t *testing.T)   { testMigrateStageNoStepUnchanged(t, false) }
func TestMigrateStageNoStepUnchangedPostgres(t *testing.T) { testMigrateStageNoStepUnchanged(t, true) }

func testMigrateStageSendsMigrateDataAfterRemoveCode(t *testing.T, pg bool) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
	if lease.snapshot.Stage != appplatform.StageMigrate || operationStage(t, s, lease.id) != "migrate" ||
		effectState(t, s, lease.id, "migrate", 1) != "applied" || effectState(t, s, lease.id, "migrate", 2) != "" {
		t.Fatal("the removal moved the operation or recorded the migration early")
	}
	var sent platformMigrateIntent
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		sent = i
		if effectState(t, s, lease.id, "migrate", 2) != "prepared" {
			t.Error("the migration was sent before its effect was recorded")
		}
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	next, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || operationStage(t, s, lease.id) != "start" ||
		effectState(t, s, lease.id, "migrate", 2) != "applied" {
		t.Fatalf("migrate-data did not apply and advance: %v %v", next.snapshot.Stage, err)
	}
	c := sent.command
	var plan platformManagedUpdatePlan
	raw := scalar[string](t, s.st, `SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, lease.id)
	if err := jsonUnmarshalPlan(raw, &plan); err != nil || plan.Migration == nil {
		t.Fatal("the finalized plan lost the migration", err)
	}
	p := c.Payload
	if c.Stage != "migrate" || c.EffectNumber != "2" || c.Kind != "migrate-data" || c.OperationID != lease.id ||
		p.SourceSchema != "1" || p.TargetSchema != "2" || p.WallClockSeconds != "600" ||
		p.EntrypointSHA256 != control.MigrationEntrypointSHA256([]string{"/bin/migrate", "--apply"}) ||
		p.WriterFence != itoa64(plan.SourceWriterFence+1) || p.StartWriterFence != itoa64(plan.SourceWriterFence+2) ||
		p.CheckpointWriterFence != itoa64(plan.SourceWriterFence) || p.RemoveCodeEffectID != scalar[string](t, s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=1`, lease.id) ||
		p.ResourceID == p.NamespaceID || p.TargetReleaseSHA256 != plan.TargetReleaseSHA256 || c.PayloadSHA256 == "" {
		t.Fatalf("migrate-data command is not bound to the reviewed plan: %+v", c)
	}
	if len(sent.readSignature) != 64 || len(sent.signature) != 64 || len(sent.descriptor) == 0 || string(sent.readSignature) == string(sent.signature) {
		t.Fatal("the execute and read signatures were not both produced")
	}
	if fake.count("execute-migrate") != 1 {
		t.Fatalf("calls: %v", fake.calls)
	}
}

func TestMigrateStageSendsMigrateDataAfterRemoveCodeSQLite(t *testing.T) {
	testMigrateStageSendsMigrateDataAfterRemoveCode(t, false)
}
func TestMigrateStageSendsMigrateDataAfterRemoveCodePostgres(t *testing.T) {
	testMigrateStageSendsMigrateDataAfterRemoveCode(t, true)
}

// A lost acknowledgement says nothing about the job: the effect is read back under its own identity and the
// receipt it returns is committed, without the command ever being sent again.
func testMigrateDataLostAckReadBackCommits(t *testing.T, pg bool) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
	fake.executeMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errors.New("connection reset after the job exited")
	}
	pending := 0
	fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		if pending == 0 {
			pending++
			return platformMigrateStatusAnswer(i, endpoint, "pending"), nil
		}
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	platformMigrationPollInterval = 1
	t.Cleanup(func() { platformMigrationPollInterval = 2 * 1e9 })
	next, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || effectState(t, s, lease.id, "migrate", 2) != "applied" {
		t.Fatalf("read-back did not commit: %v %v", next.snapshot.Stage, err)
	}
	if fake.count("execute-migrate") != 1 || fake.count("read-migrate") != 2 {
		t.Fatalf("calls: %v", fake.calls)
	}
}

func TestMigrateDataLostAckReadBackCommitsSQLite(t *testing.T) {
	testMigrateDataLostAckReadBackCommits(t, false)
}
func TestMigrateDataLostAckReadBackCommitsPostgres(t *testing.T) {
	testMigrateDataLostAckReadBackCommits(t, true)
}

// C1 and C2: a job that exits non-zero, or writes no marker, is a known failure. The effect is reverted, the
// operation waits in recovery_required with the installation, and nothing starts.
func testMigrateKnownFailureRecoveryRequired(t *testing.T, pg bool) {
	for name, outcome := range map[string]string{"C1 exit nonzero": "exit_nonzero", "C2 marker missing": "marker_missing"} {
		t.Run(name, func(t *testing.T) {
			s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
			fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
				return platformMigrateAnswers(i, endpoint, "failed", outcome), nil
			}
			if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
				t.Fatal(err)
			}
			if operationStage(t, s, lease.id) != "recovery_required" || effectState(t, s, lease.id, "migrate", 2) != "reverted" ||
				scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "recovery_required" ||
				scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != 0 {
				t.Fatal("a known failure did not wait for settlement")
			}
			var receipt string
			if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id).Scan(&receipt); err != nil || !strings.Contains(receipt, outcome) {
				t.Fatal("the failure outcome was not kept", receipt, err)
			}
		})
	}
}

func TestMigrateKnownFailureRecoveryRequiredSQLite(t *testing.T) {
	testMigrateKnownFailureRecoveryRequired(t, false)
}
func TestMigrateKnownFailureRecoveryRequiredPostgres(t *testing.T) {
	testMigrateKnownFailureRecoveryRequired(t, true)
}

// An orphaned migration (the runner restarted, nothing is working on it) is never sent again: the operation
// waits for settlement with the effect unknown.
func TestMigrateOrphanedReadBackEndsForSettlement(t *testing.T) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	fake.executeMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errors.New("runner killed")
	}
	fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateStatusAnswer(i, endpoint, "orphaned"), nil
	}
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	if operationStage(t, s, lease.id) != "recovery_required" || effectState(t, s, lease.id, "migrate", 2) != "unknown" || fake.count("execute-migrate") != 1 {
		t.Fatalf("orphaned migration: %s %s %v", operationStage(t, s, lease.id), effectState(t, s, lease.id, "migrate", 2), fake.calls)
	}
}
