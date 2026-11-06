package app

import (
	"context"
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

type platformCheckpointTestObservation struct {
	receipt    journal.CheckpointReceipt
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformCheckpointTestObservation) CheckpointReceipt() (journal.CheckpointReceipt, error) {
	return o.receipt, nil
}
func (o platformCheckpointTestObservation) Controller() control.Authority { return o.authority }
func (o platformCheckpointTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformCheckpointTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformCheckpointTestObservation) CommandSHA256() string { return o.commandSHA }

func platformCheckpointEndpointFixture() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-test/checkpoint.sock", controllerUID: 1000, controllerGID: 1001}
}

func platformCheckpointObservationFixture(intent platformCheckpointIntent, endpoint platformRuntimeEndpoint) platformCheckpointTestObservation {
	now := time.Now().UTC().UnixMilli()
	return platformCheckpointTestObservation{
		receipt: journal.CheckpointReceipt{
			Format:       "portal.runner.data-checkpoint-receipt/1-dev.1",
			DeploymentID: intent.command.DeploymentID, ControllerEpoch: intent.command.ControllerEpoch,
			InstallationID: intent.command.InstallationID, OperationID: intent.command.OperationID,
			TargetGeneration: intent.command.TargetGeneration, Stage: "snapshot",
			EffectID: intent.command.EffectID, PayloadSHA256: intent.command.PayloadSHA256,
			CheckpointID: intent.command.Payload.CheckpointID, NamespaceID: intent.command.Payload.NamespaceID,
			ResourceID: intent.command.Payload.ResourceID, ReleaseSHA256: intent.command.Payload.ReleaseSHA256,
			SourceOperationID:       intent.command.Payload.SourceOperationID,
			SourceStartEffectID:     intent.command.Payload.SourceStartEffectID,
			SourceReceiptSHA256:     intent.command.Payload.SourceReceiptSHA256,
			SourceDataEffectID:      intent.command.Payload.SourceDataEffectID,
			SourceDataReceiptSHA256: intent.command.Payload.SourceDataReceiptSHA256,
			ProjectID:               intent.command.Payload.ProjectID, NamespaceInode: intent.command.Payload.NamespaceInode,
			Device: intent.command.Payload.Device, DataFormat: intent.command.Payload.DataFormat,
			WriterFence:  intent.command.Payload.WriterFence,
			ArchiveBytes: "4096", Entries: "3", ManifestSHA256: strings.Repeat("a", 64),
			ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
		},
		authority: intent.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now().UTC(),
		commandSHA: platformDigest(intent.raw),
	}
}

func platformCheckpointUpdateFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, _, install := platformManagedAccessFixture(t, pg)
	return platformCheckpointUpdateForInstall(t, s, install)
}

func platformWritableCheckpointUpdateFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, _, install := platformWritableAccessFixture(t, pg)
	return platformCheckpointUpdateForInstall(t, s, install)
}

func platformCheckpointUpdateForInstall(t *testing.T, s *Server, install platformOperationLease) (*Server, platformOperationLease) {
	t.Helper()
	var sourceOperation, planID, actorScope, epoch string
	var sourceGeneration, sourceFence int64
	if err := s.st.queryRow(`SELECT id,review_plan_id,actor_scope,controller_epoch,target_generation,fence
		FROM app_platform_operations WHERE installation_id=? AND action='install' AND managed=1 AND stage='complete'`,
		install.installationID).Scan(&sourceOperation, &planID, &actorScope, &epoch, &sourceGeneration, &sourceFence); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	updatePlanID := platformFixtureID(1302)
	if _, err := s.st.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'update',target_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=?`, updatePlanID, planID); err != nil {
		t.Fatal(err)
	}
	operationID := platformFixtureID(1300)
	if _, err := s.st.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,'update',1,?,?,'snapshot',6,1,NULL,NULL,0,?,NULL,NULL,?,?)`,
		operationID, install.installationID, updatePlanID, actorScope, strings.Repeat("1", 64), strings.Repeat("2", 64),
		sourceGeneration, sourceGeneration+1, epoch, now, now); err != nil {
		t.Fatal(err)
	}
	lease, err := s.st.claimPlatformOperation(operationID, platformFixtureID(1301))
	if err != nil || lease.snapshot.Action != appplatform.ActionUpdate || !lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageSnapshot || lease.snapshot.Generation != uint64(sourceGeneration+1) ||
		lease.fence <= sourceFence {
		t.Fatal("checkpoint update lease", lease, sourceOperation, err)
	}
	return s, lease
}

func testPlatformCheckpointPreparesExactIntent(t *testing.T, pg bool) {
	s, lease := platformCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if !platformValidUUID(intent.recoverySetID) || !platformValidUUID(intent.effectID) ||
		intent.command.OperationID != lease.id || intent.command.InstallationID != lease.installationID ||
		intent.command.TargetGeneration != strconv.FormatUint(lease.snapshot.Generation, 10) ||
		intent.command.InstallationFence != strconv.FormatInt(lease.fence, 10) ||
		intent.command.Payload.SourceDataReceiptSHA256 == "" || intent.command.Payload.SourceReceiptSHA256 == "" ||
		len(intent.raw) == 0 || len(intent.signature) != 64 || len(intent.source.descriptor) == 0 {
		t.Fatalf("checkpoint intent: %+v", intent)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "preparing" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "snapshot" {
		t.Fatal("checkpoint preparation advanced beyond durable intent")
	}
	replay, err := s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil || replay.recoverySetID != intent.recoverySetID || replay.effectID != intent.effectID ||
		replay.command.PayloadSHA256 != intent.command.PayloadSHA256 ||
		replay.command.Payload.CheckpointID != intent.command.Payload.CheckpointID {
		t.Fatal("checkpoint intent replay", replay, err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=?`, lease.id) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id) != 1 {
		t.Fatal("checkpoint replay duplicated identity")
	}
}

func TestPlatformCheckpointPreparesExactIntentSQLite(t *testing.T) {
	testPlatformCheckpointPreparesExactIntent(t, false)
}

func TestPlatformCheckpointPreparesExactIntentPostgres(t *testing.T) {
	testPlatformCheckpointPreparesExactIntent(t, true)
}

func testPlatformWritableCheckpointPreparesIntent(t *testing.T, pg bool) {
	s, lease := platformWritableCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if !platformValidUUID(intent.recoverySetID) || intent.source.resourceID == "" ||
		intent.command.Payload.SourceOperationID == "" || intent.command.Payload.SourceStartEffectID == "" ||
		intent.command.Payload.SourceDataEffectID == "" || len(intent.raw) == 0 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "preparing" {
		t.Fatal("writable checkpoint intent", intent)
	}
}

func TestPlatformWritableCheckpointPreparesIntentSQLite(t *testing.T) {
	testPlatformWritableCheckpointPreparesIntent(t, false)
}

func TestPlatformWritableCheckpointPreparesIntentPostgres(t *testing.T) {
	testPlatformWritableCheckpointPreparesIntent(t, true)
}

func testPlatformCheckpointSettlesVerifiedReceipt(t *testing.T, pg bool) {
	s, lease := platformCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	receipt := platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture())
	if got, ok := validPlatformCheckpointObservation(receipt, intent, platformCheckpointEndpointFixture()); !ok || got.ManifestSHA256 != receipt.receipt.ManifestSHA256 {
		t.Fatal("exact checkpoint observation refused")
	}
	next, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), receipt)
	if err != nil || next.snapshot.Stage != appplatform.StageMigrate || next.snapshot.Revision != lease.snapshot.Revision+1 {
		t.Fatal("checkpoint settlement", next.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT state||':'||manifest_sha256||':'||CAST(bytes AS TEXT) FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) !=
		"verified:"+receipt.receipt.ManifestSHA256+":4096" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE id=? AND verified_ms IS NOT NULL`, intent.recoverySetID) != 1 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id) != "applied" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "migrate" {
		t.Fatal("recovery set/effect/operation did not commit together")
	}
	if _, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), receipt); err == nil {
		t.Fatal("settled checkpoint was accepted twice")
	}
}

func TestPlatformCheckpointSettlesVerifiedReceiptSQLite(t *testing.T) {
	testPlatformCheckpointSettlesVerifiedReceipt(t, false)
}

func TestPlatformCheckpointSettlesVerifiedReceiptPostgres(t *testing.T) {
	testPlatformCheckpointSettlesVerifiedReceipt(t, true)
}

func testPlatformCheckpointRejectsChangedObservation(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.CheckpointReceipt){
		"payload":           func(r *journal.CheckpointReceipt) { r.PayloadSHA256 = strings.Repeat("b", 64) },
		"epoch":             func(r *journal.CheckpointReceipt) { r.ControllerEpoch = platformFixtureID(1310) },
		"checkpoint":        func(r *journal.CheckpointReceipt) { r.CheckpointID = platformFixtureID(1311) },
		"source receipt":    func(r *journal.CheckpointReceipt) { r.SourceReceiptSHA256 = strings.Repeat("c", 64) },
		"source data":       func(r *journal.CheckpointReceipt) { r.SourceDataReceiptSHA256 = strings.Repeat("d", 64) },
		"namespace":         func(r *journal.CheckpointReceipt) { r.NamespaceID = platformFixtureID(1312) },
		"resource":          func(r *journal.CheckpointReceipt) { r.ResourceID = platformFixtureID(1313) },
		"data format":       func(r *journal.CheckpointReceipt) { r.DataFormat = "2" },
		"writer fence":      func(r *journal.CheckpointReceipt) { r.WriterFence = "2" },
		"target generation": func(r *journal.CheckpointReceipt) { r.TargetGeneration = "3" },
		"future observation": func(r *journal.CheckpointReceipt) {
			future := strconv.FormatInt(time.Now().Add(10*time.Second).UnixMilli(), 10)
			r.ObservedMS, r.SettledMS = future, future
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease := platformCheckpointUpdateFixture(t, pg)
			intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			observation := platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture())
			mutate(&observation.receipt)
			if _, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), observation); err == nil {
				t.Fatal("changed checkpoint observation committed")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "preparing" ||
				scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "snapshot" {
				t.Fatal("refused checkpoint changed durable state")
			}
		})
	}
}

func TestPlatformCheckpointRejectsChangedObservationSQLite(t *testing.T) {
	testPlatformCheckpointRejectsChangedObservation(t, false)
}

func TestPlatformCheckpointRejectsChangedObservationPostgres(t *testing.T) {
	testPlatformCheckpointRejectsChangedObservation(t, true)
}

func testPlatformCheckpointPreparationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*Server, platformOperationLease){
		"runtime": func(s *Server, _ platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_runtime_bindings SET enabled=0`)
		},
		"namespace": func(s *Server, _ platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_data_namespaces SET state='retained'`)
		},
		"activation generation": func(s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_activations SET generation=generation+1 WHERE installation_id=? AND state='active'`, lease.installationID)
		},
		"source start": func(s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE operation_id=(SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install') AND stage='start'`, lease.installationID)
		},
		"source data": func(s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE operation_id=(SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install') AND stage='prepare_data'`, lease.installationID)
		},
		"generation": func(s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_installations SET generation=2 WHERE id=?`, lease.installationID)
		},
		"lease": func(s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_operations SET lease_until_ms=0 WHERE id=?`, lease.id)
		},
		"paused": func(s *Server, _ platformOperationLease) {
			mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease := platformCheckpointUpdateFixture(t, pg)
			mutate(s, lease)
			if _, err := s.preparePlatformCheckpoint(context.Background(), lease); err == nil {
				t.Fatal("changed checkpoint source admitted")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=?`, lease.id) != 0 ||
				scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id) != 0 {
				t.Fatal("refused checkpoint source wrote intent")
			}
		})
	}
}

func TestPlatformCheckpointPreparationRefusalsSQLite(t *testing.T) {
	testPlatformCheckpointPreparationRefusals(t, false)
}

func TestPlatformCheckpointPreparationRefusalsPostgres(t *testing.T) {
	testPlatformCheckpointPreparationRefusals(t, true)
}

func testPlatformCheckpointCommitRollbackAndPause(t *testing.T, pg bool) {
	s, lease := platformCheckpointUpdateFixture(t, pg)
	intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture())
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_checkpoint_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture checkpoint failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_checkpoint_commit_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.checkpoint_verified') EXECUTE FUNCTION fixture_checkpoint_commit_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_commit_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_checkpoint_commit_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_checkpoint_commit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.checkpoint_verified' BEGIN SELECT RAISE(ABORT,'fixture checkpoint failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_commit_failure`)
	}
	if _, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), observation); err == nil {
		t.Fatal("failed checkpoint audit committed")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "preparing" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "snapshot" {
		t.Fatal("failed checkpoint commit was partial")
	}
	if pg {
		mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_commit_failure ON audit_log`)
	} else {
		mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_commit_failure`)
	}
	mustExec(t, s.st, `UPDATE meta SET v='1' WHERE k='app_platform_restore_paused'`)
	if _, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), observation); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("restore pause did not refuse checkpoint commit: %v", err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_recovery_sets WHERE id=?`, intent.recoverySetID) != "preparing" {
		t.Fatal("restore pause changed checkpoint state")
	}
}

func TestPlatformCheckpointCommitRollbackAndPauseSQLite(t *testing.T) {
	testPlatformCheckpointCommitRollbackAndPause(t, false)
}

func TestPlatformCheckpointCommitRollbackAndPausePostgres(t *testing.T) {
	testPlatformCheckpointCommitRollbackAndPause(t, true)
}

func TestPlatformCheckpointEndpointRefusesUnverifiedRuntimeSQLite(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/run/portal-test/missing-checkpoint.sock", controllerUID: 1000, controllerGID: 1001}
	s, dispatchLease := platformCheckpointUpdateFixture(t, false)
	if _, err := s.dispatchPlatformCheckpoint(context.Background(), dispatchLease, endpoint); err == nil {
		t.Fatal("checkpoint dispatch accepted an unverified runtime endpoint")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND state='prepared'`, dispatchLease.id) != 1 {
		t.Fatal("failed endpoint dispatch lost its durable prepared intent")
	}
	s, readLease := platformCheckpointUpdateFixture(t, false)
	if _, err := s.readPlatformCheckpoint(context.Background(), readLease, endpoint); err == nil {
		t.Fatal("checkpoint receipt read accepted an unverified runtime endpoint")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND state='prepared'`, readLease.id) != 1 {
		t.Fatal("failed endpoint read lost its durable prepared intent")
	}
}

func testPlatformCheckpointRejectsChangedDurableIntent(t *testing.T, pg bool) {
	for name, mutate := range map[string]string{
		"effect digest":       `UPDATE app_platform_operation_effects SET payload_sha256=? WHERE operation_id=? AND stage='snapshot'`,
		"recovery checkpoint": `UPDATE app_platform_recovery_sets SET checkpoint_id=? WHERE operation_id=?`,
		"recovery fence":      `UPDATE app_platform_recovery_sets SET writer_fence=2 WHERE operation_id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			s, lease := platformCheckpointUpdateFixture(t, pg)
			intent, err := s.preparePlatformCheckpoint(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "effect digest":
				mustExec(t, s.st, mutate, strings.Repeat("e", 64), lease.id)
			case "recovery checkpoint":
				mustExec(t, s.st, mutate, platformFixtureID(1320), lease.id)
			case "recovery fence":
				mustExec(t, s.st, mutate, lease.id)
			}
			observation := platformCheckpointObservationFixture(intent, platformCheckpointEndpointFixture())
			if _, err := s.commitPlatformCheckpoint(context.Background(), lease, intent, platformCheckpointEndpointFixture(), observation); err == nil {
				t.Fatal("changed durable checkpoint intent committed")
			}
		})
	}
}

func TestPlatformCheckpointRejectsChangedDurableIntentSQLite(t *testing.T) {
	testPlatformCheckpointRejectsChangedDurableIntent(t, false)
}

func TestPlatformCheckpointRejectsChangedDurableIntentPostgres(t *testing.T) {
	testPlatformCheckpointRejectsChangedDurableIntent(t, true)
}

func testPlatformCheckpointPrepareRollback(t *testing.T, pg bool) {
	s, lease := platformCheckpointUpdateFixture(t, pg)
	beforeRecovery := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=?`, lease.id)
	beforeEffects := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id)
	beforeAudits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.checkpoint_intent' AND target_id=?`, lease.id)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_checkpoint_prepare_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture checkpoint prepare failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_checkpoint_prepare_failure BEFORE INSERT ON app_platform_operation_effects FOR EACH ROW WHEN (NEW.stage='snapshot') EXECUTE FUNCTION fixture_checkpoint_prepare_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_prepare_failure ON app_platform_operation_effects`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_checkpoint_prepare_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_checkpoint_prepare_failure BEFORE INSERT ON app_platform_operation_effects WHEN NEW.stage='snapshot' BEGIN SELECT RAISE(ABORT,'fixture checkpoint prepare failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_checkpoint_prepare_failure`)
	}
	if _, err := s.preparePlatformCheckpoint(context.Background(), lease); err == nil {
		t.Fatal("failed checkpoint intent insert committed")
	}
	recovery := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_recovery_sets WHERE operation_id=?`, lease.id)
	effects := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot'`, lease.id)
	audits := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.checkpoint_intent' AND target_id=?`, lease.id)
	if recovery != beforeRecovery || effects != beforeEffects || audits != beforeAudits {
		t.Fatalf("failed checkpoint preparation left durable rows recovery=%d/%d effects=%d/%d audits=%d/%d",
			beforeRecovery, recovery, beforeEffects, effects, beforeAudits, audits)
	}
}

func TestPlatformCheckpointPrepareRollbackSQLite(t *testing.T) {
	testPlatformCheckpointPrepareRollback(t, false)
}

func TestPlatformCheckpointPrepareRollbackPostgres(t *testing.T) {
	testPlatformCheckpointPrepareRollback(t, true)
}
