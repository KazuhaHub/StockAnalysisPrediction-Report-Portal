package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformUpdateStartTestObservation struct {
	reservation journal.UpdateStartReservation
	authority   control.Authority
	endpoint    transport.CandidateReceiptEndpoint
	receivedAt  time.Time
	commandSHA  string
}

func (o platformUpdateStartTestObservation) UpdateStartReservation() (journal.UpdateStartReservation, error) {
	return o.reservation, nil
}
func (o platformUpdateStartTestObservation) Controller() control.Authority { return o.authority }
func (o platformUpdateStartTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformUpdateStartTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformUpdateStartTestObservation) CommandSHA256() string { return o.commandSHA }

func platformUpdateStartEndpointFixture() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-test/update-start.sock", controllerUID: 1000, controllerGID: 1001}
}

func platformManagedUpdateStartFixture(t *testing.T, pg bool) (*Server, platformOperationLease, journal.CheckpointReceipt, platformManagedUpdatePlan) {
	t.Helper()
	s, checkpointLease := platformCheckpointUpdateFixture(t, pg)
	checkpointIntent, err := s.preparePlatformCheckpoint(context.Background(), checkpointLease)
	if err != nil {
		t.Fatal(err)
	}
	checkpointObservation := platformCheckpointObservationFixture(checkpointIntent, platformCheckpointEndpointFixture())
	migrateLease, err := s.commitPlatformCheckpoint(context.Background(), checkpointLease, checkpointIntent, platformCheckpointEndpointFixture(), checkpointObservation)
	if err != nil {
		t.Fatal(err)
	}
	model, err := appplatform.OperationFromSnapshot(migrateLease.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	startModel, err := model.Advance(migrateLease.snapshot.Revision, appplatform.StageStart)
	if err != nil {
		t.Fatal(err)
	}
	startLease := migrateLease
	startLease.snapshot = startModel.Snapshot()
	startLease.rowRevision++
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='start',state_revision=?,row_revision=?,updated_ms=? WHERE id=?`,
		int64(startLease.snapshot.Revision), startLease.rowRevision, now, startLease.id)
	var checkpointRaw string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='snapshot' AND state='applied'`,
		startLease.id).Scan(&checkpointRaw); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := journal.ParseCheckpointReceipt([]byte(checkpointRaw))
	if err != nil {
		t.Fatal(err)
	}
	var sourceApp, publisher string
	if err := s.st.queryRow(`SELECT app_id,publisher_id FROM app_platform_installations WHERE id=?`,
		startLease.installationID).Scan(&sourceApp, &publisher); err != nil {
		t.Fatal(err)
	}
	session := platformManagedFixtureSession(t, s)
	rawManifest, archive := platformManagedReleaseFixture(t, "linux/amd64")
	var manifest map[string]any
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["id"] = sourceApp
	manifest["version"] = "1.1.0-beta.1"
	rawManifest, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	targetRelease, err := s.stagePlatformRelease(session, rawManifest, platformSignTestDescriptor(t, rawManifest), archive)
	if err != nil {
		t.Fatal(err)
	}
	var recoverySetID string
	if err := s.st.queryRow(`SELECT id FROM app_platform_recovery_sets WHERE operation_id=? AND state='verified'`,
		startLease.id).Scan(&recoverySetID); err != nil {
		t.Fatal(err)
	}
	plan := platformManagedUpdatePlan{
		Format: platformManagedUpdateFormat, InstallationID: startLease.installationID,
		AppID: sourceApp, PublisherID: publisher,
		SourceReleaseSHA256: checkpoint.ReleaseSHA256, TargetReleaseSHA256: targetRelease.sha256,
		SourceGeneration: 1, TargetGeneration: 2,
		SourcePolicyRevision: 1, TargetPolicyRevision: 2,
		ControllerEpoch: startLease.controllerEpoch, RecoverySetID: recoverySetID,
		CheckpointOperationID: startLease.id, CheckpointEffectID: checkpoint.EffectID,
		CheckpointID: checkpoint.CheckpointID, CheckpointManifestSHA256: checkpoint.ManifestSHA256,
		CheckpointArchiveBytes: mustPlatformCheckpointInt(checkpoint.ArchiveBytes),
		DataNamespaceID:        checkpoint.NamespaceID, DataFormat: mustPlatformCheckpointInt(checkpoint.DataFormat),
		SourceWriterFence: mustPlatformCheckpointInt(checkpoint.WriterFence),
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planID := scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, startLease.id)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET action='update',source_release_sha256=?,target_release_sha256=?,installation_revision=?,policy_revision=?,trust_revision=?,controller_epoch=?,plan_json=?,plan_sha256=?,updated_ms=? WHERE id=?`,
		checkpoint.ReleaseSHA256, targetRelease.sha256, startLease.rowRevision, 1,
		targetRelease.trustRevision, startLease.controllerEpoch, string(planRaw),
		platformDigest(planRaw), now, planID)
	return s, startLease, checkpoint, plan
}

func platformUpdateStartReservationFixture(intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint) platformUpdateStartTestObservation {
	command := intent.command
	return platformUpdateStartTestObservation{
		reservation: journal.UpdateStartReservation{
			ResourceID: intent.resourceID, InstallationID: command.InstallationID,
			NamespaceID: command.Payload.NamespaceID, EffectID: intent.effectID,
			OwnershipSHA256:     strings.Repeat("a", 64),
			SourceGeneration:    mustPlatformCheckpointInt(command.Payload.SourceGeneration),
			TargetGeneration:    mustPlatformCheckpointInt(command.TargetGeneration),
			SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
			TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
			ControlFence:        mustPlatformCheckpointInt(command.InstallationFence),
			SourceWriterFence:   mustPlatformCheckpointInt(command.Payload.SourceWriterFence),
			TargetWriterFence:   mustPlatformCheckpointInt(command.Payload.TargetWriterFence),
		},
		authority: intent.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0, ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now().UTC(), commandSHA: platformDigest(intent.raw),
	}
}

func testPlatformUpdateStartPreparesExactIntent(t *testing.T, pg bool) {
	s, lease, checkpoint, plan := platformManagedUpdateStartFixture(t, pg)
	intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if !platformValidUUID(intent.resourceID) || !platformValidUUID(intent.effectID) ||
		intent.command.Payload.ResourceID != intent.resourceID ||
		intent.command.Payload.CheckpointID != checkpoint.CheckpointID ||
		intent.command.Payload.TargetReleaseSHA256 != plan.TargetReleaseSHA256 ||
		intent.command.Payload.SourceGeneration != "1" ||
		intent.command.TargetGeneration != "2" ||
		intent.command.Payload.TargetWriterFence != "2" ||
		intent.command.Payload.TargetPolicyRevision != "2" ||
		len(intent.raw) == 0 || len(intent.signature) != 64 {
		t.Fatalf("update-start intent: %+v", intent)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != "candidate" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" {
		t.Fatal("update-start preparation advanced past durable intent")
	}
	replay, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil || replay.resourceID != intent.resourceID || replay.effectID != intent.effectID ||
		replay.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		t.Fatal("update-start intent replay", replay, err)
	}
	observation := platformUpdateStartReservationFixture(intent, platformUpdateStartEndpointFixture())
	if reservation, ok := validPlatformUpdateStartObservation(observation, intent, platformUpdateStartEndpointFixture()); !ok ||
		reservation.ResourceID != intent.resourceID {
		t.Fatal("update-start reservation observation", reservation, ok)
	}
}

func TestPlatformUpdateStartPreparesExactIntentSQLite(t *testing.T) {
	testPlatformUpdateStartPreparesExactIntent(t, false)
}

func TestPlatformUpdateStartPreparesExactIntentPostgres(t *testing.T) {
	testPlatformUpdateStartPreparesExactIntent(t, true)
}

func testPlatformUpdateStartRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*Server, platformManagedUpdatePlan){
		"checkpoint state": func(s *Server, plan platformManagedUpdatePlan) {
			mustExec(t, s.st, `UPDATE app_platform_recovery_sets SET state='preparing' WHERE id=?`, plan.RecoverySetID)
		},
		"checkpoint receipt": func(s *Server, plan platformManagedUpdatePlan) {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE effect_id=?`, plan.CheckpointEffectID)
		},
		"target release": func(s *Server, plan platformManagedUpdatePlan) {
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_sha256=? WHERE plan_json LIKE ?`,
				strings.Repeat("b", 64), `%`+plan.RecoverySetID+`%`)
		},
		"target policy": func(s *Server, plan platformManagedUpdatePlan) {
			plan.TargetPolicyRevision = 1
			raw, _ := json.Marshal(plan)
			mustExec(t, s.st, `UPDATE app_platform_review_plans SET plan_json=?,plan_sha256=? WHERE plan_json LIKE ?`,
				string(raw), platformDigest(raw), `%`+plan.RecoverySetID+`%`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, _, plan := platformManagedUpdateStartFixture(t, pg)
			mutate(s, plan)
			if _, err := s.preparePlatformUpdateStart(context.Background(), lease); err == nil {
				t.Fatal("changed update-start source admitted")
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != 0 {
				t.Fatal("refused update-start wrote effect")
			}
		})
	}
}

func TestPlatformUpdateStartRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartRefusals(t, false)
}

func TestPlatformUpdateStartRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartRefusals(t, true)
}

func testPlatformUpdateStartPrepareRollback(t *testing.T, pg bool) {
	s, lease, _, _ := platformManagedUpdateStartFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_start_prepare_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture update start prepare failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_prepare_failure BEFORE INSERT ON app_platform_operation_effects FOR EACH ROW WHEN (NEW.stage='start' AND NEW.kind='update-start') EXECUTE FUNCTION fixture_update_start_prepare_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_prepare_failure ON app_platform_operation_effects`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_start_prepare_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_prepare_failure BEFORE INSERT ON app_platform_operation_effects WHEN NEW.stage='start' AND NEW.kind='update-start' BEGIN SELECT RAISE(ABORT,'fixture update start prepare failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_prepare_failure`)
	}
	if _, err := s.preparePlatformUpdateStart(context.Background(), lease); err == nil {
		t.Fatal("failed update-start intent insert committed")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != 0 {
		t.Fatal("failed update-start preparation left partial rows")
	}
}

func TestPlatformUpdateStartPrepareRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartPrepareRollback(t, false)
}

func TestPlatformUpdateStartPrepareRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartPrepareRollback(t, true)
}

func TestPlatformUpdateStartEndpointRefusesUnverifiedRuntimeSQLite(t *testing.T) {
	endpoint := platformRuntimeEndpoint{path: "/run/portal-test/missing-update-start.sock", controllerUID: 1000, controllerGID: 1001}
	s, lease, _, _ := platformManagedUpdateStartFixture(t, false)
	if _, err := s.reservePlatformUpdateStart(context.Background(), lease, endpoint); err == nil {
		t.Fatal("update-start reserve accepted an unverified endpoint")
	}
	s, lease, _, _ = platformManagedUpdateStartFixture(t, false)
	if _, err := s.readPlatformUpdateStartReservation(context.Background(), lease, endpoint); err == nil {
		t.Fatal("update-start read accepted an unverified endpoint")
	}
}

func TestPlatformUpdateStartTargetPolicyDifference(t *testing.T) {
	s, lease, _, plan := platformManagedUpdateStartFixture(t, false)
	if plan.TargetPolicyRevision <= plan.SourcePolicyRevision {
		t.Fatal("update-start fixture did not advance policy")
	}
	intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil || intent.command.Payload.TargetPolicyRevision != strconv.FormatInt(plan.TargetPolicyRevision, 10) {
		t.Fatal("update-start policy binding", intent.command.Payload.TargetPolicyRevision, err)
	}
}
