package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformRestoreTestObservation struct {
	receipt    journal.RestoreReceipt
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformRestoreTestObservation) RestoreReceipt() (journal.RestoreReceipt, error) {
	return o.receipt, nil
}
func (o platformRestoreTestObservation) Controller() control.Authority { return o.authority }
func (o platformRestoreTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformRestoreTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformRestoreTestObservation) CommandSHA256() string { return o.commandSHA }

func platformRestoreEndpointFixture() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-test/restore.sock", controllerUID: 1000, controllerGID: 1001}
}

func platformVerifiedCheckpointUpdateFixture(t *testing.T, pg bool) (*Server, platformOperationLease, journal.CheckpointReceipt) {
	t.Helper()
	s, checkpointLease := platformCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformCheckpoint(context.Background(), checkpointLease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture())
	if _, err := s.commitPlatformCheckpoint(context.Background(), checkpointLease, intent, platformCheckpointEndpointFixture(), observation); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required',state_revision=7,completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=? WHERE id=?`,
		now, now, checkpointLease.id)
	var planID, actorScope, epoch string
	if err := s.st.queryRow(`SELECT review_plan_id,actor_scope,controller_epoch FROM app_platform_operations WHERE id=?`,
		checkpointLease.id).Scan(&planID, &actorScope, &epoch); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND state='applied'`, checkpointLease.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := journal.ParseCheckpointReceipt([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var recoverySetID string
	if err := s.st.queryRow(`SELECT id FROM app_platform_recovery_sets WHERE operation_id=? AND checkpoint_id=?`,
		checkpointLease.id, checkpoint.CheckpointID).Scan(&recoverySetID); err != nil {
		t.Fatal(err)
	}
	restorePlanID := platformFixtureID(1400)
	if _, err := s.st.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'restore',source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=?`, restorePlanID, planID); err != nil {
		t.Fatal(err)
	}
	restorePlan := platformManagedRestorePlan{
		Format: platformManagedRestoreFormat, InstallationID: checkpointLease.installationID,
		RecoverySetID: recoverySetID, CheckpointOperationID: checkpoint.OperationID,
		CheckpointEffectID: checkpoint.EffectID, CheckpointID: checkpoint.CheckpointID,
		ReleaseSHA256: checkpoint.ReleaseSHA256, ManifestSHA256: checkpoint.ManifestSHA256,
		ArchiveBytes: mustPlatformCheckpointInt(checkpoint.ArchiveBytes), SourceGeneration: 1,
		TargetGeneration: 2, ControllerEpoch: epoch,
	}
	restorePlanRaw, err := json.Marshal(restorePlan)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET installation_id=?,plan_json=?,plan_sha256=? WHERE id=?`,
		checkpointLease.installationID, string(restorePlanRaw), platformDigest(restorePlanRaw), restorePlanID)
	operationID := platformFixtureID(1401)
	if _, err := s.st.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,'restore',1,1,2,'restore_data',6,1,NULL,NULL,0,?,NULL,NULL,?,?)`,
		operationID, checkpointLease.installationID, restorePlanID, actorScope,
		strings.Repeat("3", 64), strings.Repeat("4", 64), epoch, now, now); err != nil {
		t.Fatal(err)
	}
	restoreLease, err := s.st.claimPlatformOperation(operationID, platformFixtureID(1402))
	if err != nil || restoreLease.snapshot.Action != appplatform.ActionRestore ||
		!restoreLease.snapshot.Managed || restoreLease.snapshot.Stage != appplatform.StageRestoreData {
		t.Fatal("restore lease", restoreLease, err)
	}
	return s, restoreLease, checkpoint
}

func platformRestoreObservationFixture(intent platformRestoreIntent, endpoint platformRuntimeEndpoint) platformRestoreTestObservation {
	now := time.Now().UTC().UnixMilli()
	command := intent.command
	return platformRestoreTestObservation{
		receipt: journal.RestoreReceipt{
			Format:       "portal.runner.data-restore-receipt/1-dev.1",
			DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
			InstallationID: command.InstallationID, OperationID: command.OperationID,
			TargetGeneration: command.TargetGeneration, Stage: command.Stage,
			EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
			RecoverySetID: command.Payload.RecoverySetID, CheckpointID: command.Payload.CheckpointID,
			NamespaceID: command.Payload.NamespaceID, ReleaseSHA256: command.Payload.ReleaseSHA256,
			ManifestSHA256: command.Payload.ManifestSHA256, ArchiveBytes: command.Payload.ArchiveBytes,
			ProjectID: command.Payload.ProjectID, NamespaceInode: command.Payload.NamespaceInode,
			Device: command.Payload.Device, DataFormat: command.Payload.DataFormat,
			WriterFence:                 command.Payload.WriterFence,
			SourceCheckpointOperationID: command.Payload.SourceCheckpointOperationID,
			SourceCheckpointEffectID:    command.Payload.SourceCheckpointEffectID,
			SourceCheckpointReceiptSHA:  command.Payload.SourceCheckpointReceiptSHA,
			Entries:                     "3", UsedBytes: "4096", State: "restored", ObservedMS: strconv.FormatInt(now, 10),
			SettledMS: strconv.FormatInt(now, 10),
		},
		authority: intent.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

func testPlatformRestorePreparesExactIntent(t *testing.T, pg bool) {
	s, lease, checkpoint := platformVerifiedCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformRestore(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if !platformValidUUID(intent.recoverySetID) || !platformValidUUID(intent.effectID) ||
		intent.command.Payload.CheckpointID != checkpoint.CheckpointID ||
		intent.command.Payload.ManifestSHA256 != checkpoint.ManifestSHA256 ||
		intent.command.Payload.SourceCheckpointOperationID != checkpoint.OperationID ||
		intent.command.Payload.SourceCheckpointEffectID != checkpoint.EffectID ||
		len(intent.raw) == 0 || len(intent.signature) != 64 {
		t.Fatalf("restore intent: %+v", intent)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "restoring" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "restore_data" {
		t.Fatal("restore preparation advanced past durable intent")
	}
	replay, err := s.preparePlatformRestore(context.Background(), lease)
	if err != nil || replay.effectID != intent.effectID || replay.recoverySetID != intent.recoverySetID ||
		replay.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("restore intent replay", replay, err)
	}
}

func TestPlatformRestorePreparesExactIntentSQLite(t *testing.T) {
	testPlatformRestorePreparesExactIntent(t, false)
}

func TestPlatformRestorePreparesExactIntentPostgres(t *testing.T) {
	testPlatformRestorePreparesExactIntent(t, true)
}

func testPlatformRestoreSettlesVerifiedReceipt(t *testing.T, pg bool) {
	s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformRestore(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformRestoreObservationFixture(intent, platformRestoreEndpointFixture())
	next, err := s.commitPlatformRestore(context.Background(), lease, intent, platformRestoreEndpointFixture(), observation)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || next.snapshot.Revision != lease.snapshot.Revision+1 {
		t.Fatal("restore settlement", next.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "verified" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != "applied" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" ||
		scalar[int64](t, s.st, `SELECT observed_bytes FROM app_platform_data_namespaces WHERE id=?`, intent.command.Payload.NamespaceID) != 4096 {
		t.Fatal("restore receipt/recovery/operation did not commit together")
	}
}

func TestPlatformRestoreSettlesVerifiedReceiptSQLite(t *testing.T) {
	testPlatformRestoreSettlesVerifiedReceipt(t, false)
}

func TestPlatformRestoreSettlesVerifiedReceiptPostgres(t *testing.T) {
	testPlatformRestoreSettlesVerifiedReceipt(t, true)
}

func testPlatformRestoreRejectsChangedObservation(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.RestoreReceipt){
		"recovery set":     func(r *journal.RestoreReceipt) { r.RecoverySetID = platformFixtureID(1410) },
		"checkpoint":       func(r *journal.RestoreReceipt) { r.CheckpointID = platformFixtureID(1411) },
		"manifest":         func(r *journal.RestoreReceipt) { r.ManifestSHA256 = strings.Repeat("f", 64) },
		"archive":          func(r *journal.RestoreReceipt) { r.ArchiveBytes = "8192" },
		"source operation": func(r *journal.RestoreReceipt) { r.SourceCheckpointOperationID = platformFixtureID(1412) },
		"source effect":    func(r *journal.RestoreReceipt) { r.SourceCheckpointEffectID = platformFixtureID(1413) },
		"source receipt":   func(r *journal.RestoreReceipt) { r.SourceCheckpointReceiptSHA = strings.Repeat("a", 64) },
		"namespace":        func(r *journal.RestoreReceipt) { r.NamespaceID = platformFixtureID(1414) },
		"inode":            func(r *journal.RestoreReceipt) { r.NamespaceInode = "99" },
		"state":            func(r *journal.RestoreReceipt) { r.State = "verified" },
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, pg)
			intent, err := s.preparePlatformRestore(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			observation := platformRestoreObservationFixture(intent, platformRestoreEndpointFixture())
			mutate(&observation.receipt)
			if _, err := s.commitPlatformRestore(context.Background(), lease, intent, platformRestoreEndpointFixture(), observation); err == nil {
				t.Fatal("changed restore observation committed")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "restoring" ||
				scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "restore_data" {
				t.Fatal("refused restore observation changed state")
			}
		})
	}
}

func TestPlatformRestoreRejectsChangedObservationSQLite(t *testing.T) {
	testPlatformRestoreRejectsChangedObservation(t, false)
}

func TestPlatformRestoreRejectsChangedObservationPostgres(t *testing.T) {
	testPlatformRestoreRejectsChangedObservation(t, true)
}

func testPlatformRestoreCommitRollbackAndPause(t *testing.T, pg bool) {
	s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformRestore(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformRestoreObservationFixture(intent, platformRestoreEndpointFixture())
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_restore_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture restore failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_commit_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.restore_verified') EXECUTE FUNCTION fixture_restore_commit_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_commit_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_restore_commit_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_commit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.restore_verified' BEGIN SELECT RAISE(ABORT,'fixture restore failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_commit_failure`)
	}
	if _, err := s.commitPlatformRestore(context.Background(), lease, intent, platformRestoreEndpointFixture(), observation); err == nil {
		t.Fatal("failed restore audit committed")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "restoring" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "restore_data" {
		t.Fatal("failed restore commit was partial")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_commit_failure ON audit_log`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_commit_failure`)
	}
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	if _, err := s.commitPlatformRestore(context.Background(), lease, intent, platformRestoreEndpointFixture(), observation); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("restore pause did not refuse commit: %v", err)
	}
}

func TestPlatformRestoreCommitRollbackAndPauseSQLite(t *testing.T) {
	testPlatformRestoreCommitRollbackAndPause(t, false)
}

func TestPlatformRestoreCommitRollbackAndPausePostgres(t *testing.T) {
	testPlatformRestoreCommitRollbackAndPause(t, true)
}

func testPlatformRestorePreparationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*Server, platformOperationLease, journal.CheckpointReceipt){
		"recovery state": func(s *Server, _ platformOperationLease, _ journal.CheckpointReceipt) {
			mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET state='preparing',manifest_sha256=NULL,verified_ms=NULL`)
		},
		"manifest": func(s *Server, _ platformOperationLease, checkpoint journal.CheckpointReceipt) {
			mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET manifest_sha256=? WHERE checkpoint_id=?`, strings.Repeat("b", 64), checkpoint.CheckpointID)
		},
		"source receipt": func(s *Server, _ platformOperationLease, checkpoint journal.CheckpointReceipt) {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE effect_id=?`, checkpoint.EffectID)
		},
		"plan digest": func(s *Server, lease platformOperationLease, _ journal.CheckpointReceipt) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`,
				strings.Repeat("b", 64), lease.id)
		},
		"plan installation": func(s *Server, lease platformOperationLease, _ journal.CheckpointReceipt) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET installation_id=NULL WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, lease.id)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, checkpoint := platformVerifiedCheckpointUpdateFixture(t, pg)
			mutate(s, lease, checkpoint)
			if _, err := s.preparePlatformRestore(context.Background(), lease); err == nil {
				t.Fatal("changed restore source admitted")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != 0 {
				t.Fatal("refused restore wrote effect")
			}
		})
	}
}

func TestPlatformRestorePreparationRefusalsSQLite(t *testing.T) {
	testPlatformRestorePreparationRefusals(t, false)
}

func TestPlatformRestorePreparationRefusalsPostgres(t *testing.T) {
	testPlatformRestorePreparationRefusals(t, true)
}

func testPlatformRestorePrepareRollback(t *testing.T, pg bool) {
	s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, pg)
	beforeRecovery := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE state='verified'`)
	beforeAudits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_intent' AND target_id=?`, lease.id)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_restore_prepare_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture restore prepare failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_prepare_failure BEFORE INSERT ON app_platform_operation_effects FOR EACH ROW WHEN (NEW.stage='restore_data') EXECUTE FUNCTION fixture_restore_prepare_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_prepare_failure ON app_platform_operation_effects`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_restore_prepare_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_prepare_failure BEFORE INSERT ON app_platform_operation_effects WHEN NEW.stage='restore_data' BEGIN SELECT RAISE(ABORT,'fixture restore prepare failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_prepare_failure`)
	}
	if _, err := s.preparePlatformRestore(context.Background(), lease); err == nil {
		t.Fatal("failed restore intent insert committed")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data'`, lease.id) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE state='verified'`) != beforeRecovery ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_intent' AND target_id=?`, lease.id) != beforeAudits {
		t.Fatal("failed restore preparation left partial rows")
	}
}

func TestPlatformRestorePrepareRollbackSQLite(t *testing.T) {
	testPlatformRestorePrepareRollback(t, false)
}

func TestPlatformRestorePrepareRollbackPostgres(t *testing.T) {
	testPlatformRestorePrepareRollback(t, true)
}

func TestPlatformRestoreEndpointRefusesUnverifiedRuntimeSQLite(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/run/portal-test/missing-restore.sock", controllerUID: 1000, controllerGID: 1001}
	s, lease, _ := platformVerifiedCheckpointUpdateFixture(t, false)
	if _, err := s.dispatchPlatformRestore(context.Background(), lease, endpoint); err == nil {
		t.Fatal("restore dispatch accepted an unverified endpoint")
	}
	s, lease, _ = platformVerifiedCheckpointUpdateFixture(t, false)
	if _, err := s.readPlatformRestoreReceipt(context.Background(), lease, endpoint); err == nil {
		t.Fatal("restore read accepted an unverified endpoint")
	}
}
