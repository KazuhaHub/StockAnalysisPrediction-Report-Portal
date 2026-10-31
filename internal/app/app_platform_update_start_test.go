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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformUpdateStartTestObservation struct {
	reservation  journal.UpdateStartReservation
	running      journal.UpdateStartRunningReceipt
	readiness    journal.UpdateStartReadiness
	registration journal.UpdateStartRegistrationReceipt
	action       transport.WritableLifecycleAction
	authority    control.Authority
	endpoint     transport.CandidateReceiptEndpoint
	receivedAt   time.Time
	commandSHA   string
}

type platformUpdateTargetRemovalTestObservation struct {
	receipt    journal.ManagedRemovalReceipt
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformUpdateTargetRemovalTestObservation) ManagedRemovalReceipt() (journal.ManagedRemovalReceipt, error) {
	return o.receipt, nil
}
func (o platformUpdateTargetRemovalTestObservation) Controller() control.Authority {
	return o.authority
}
func (o platformUpdateTargetRemovalTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformUpdateTargetRemovalTestObservation) ReceivedAt() time.Time {
	return o.receivedAt
}
func (o platformUpdateTargetRemovalTestObservation) CommandSHA256() string {
	return o.commandSHA
}

type platformUpdateStartHandshakeTestObservation struct {
	evidence   journal.UpdateStartBackendHandshakeEvidence
	authority  control.Authority
	endpoint   transport.CandidateReceiptEndpoint
	receivedAt time.Time
	commandSHA string
}

func (o platformUpdateStartHandshakeTestObservation) Evidence() journal.UpdateStartBackendHandshakeEvidence {
	return o.evidence
}
func (o platformUpdateStartHandshakeTestObservation) Controller() control.Authority {
	return o.authority
}
func (o platformUpdateStartHandshakeTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformUpdateStartHandshakeTestObservation) ReceivedAt() time.Time {
	return o.receivedAt
}
func (o platformUpdateStartHandshakeTestObservation) CommandSHA256() string {
	return o.commandSHA
}

func (o platformUpdateStartTestObservation) UpdateStartReservation() (journal.UpdateStartReservation, error) {
	return o.reservation, nil
}
func (o platformUpdateStartTestObservation) UpdateStartRunningReceipt() (journal.UpdateStartRunningReceipt, error) {
	return o.running, nil
}
func (o platformUpdateStartTestObservation) UpdateStartReadiness() (journal.UpdateStartReadiness, error) {
	return o.readiness, nil
}
func (o platformUpdateStartTestObservation) UpdateStartRegistrationReceipt() (journal.UpdateStartRegistrationReceipt, error) {
	return o.registration, nil
}
func (o platformUpdateStartTestObservation) Controller() control.Authority { return o.authority }
func (o platformUpdateStartTestObservation) Endpoint() transport.CandidateReceiptEndpoint {
	return o.endpoint
}
func (o platformUpdateStartTestObservation) ReceivedAt() time.Time { return o.receivedAt }
func (o platformUpdateStartTestObservation) CommandSHA256() string { return o.commandSHA }
func (o platformUpdateStartTestObservation) Action() transport.WritableLifecycleAction {
	return o.action
}

func platformUpdateStartEndpointFixture() platformRuntimeEndpoint {
	return platformRuntimeEndpoint{path: "/run/portal-test/update-start.sock", controllerUID: 1000, controllerGID: 1001}
}

func platformManagedUpdateStartFixture(t *testing.T, pg bool) (*Server, platformOperationLease, journal.CheckpointReceipt, platformManagedUpdatePlan) {
	t.Helper()
	s, checkpointLease := platformCheckpointUpdateFixture(t, pg)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(
		operation_id,stage,effect_number,effect_id,kind,payload_sha256,
		runtime_id,controller_epoch,admitted_fence,observed_fence,state,
		receipt_json,created_ms,updated_ms)
		SELECT o.id,'quiesce',1,?,'stop',?,i.runtime_id,o.controller_epoch,
		o.fence,o.fence,'applied','{}',o.created_ms,o.updated_ms
		FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		WHERE o.id=?`,
		platformFixtureID(1299), strings.Repeat("9", 64),
		checkpointLease.id)
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
	var sourcePlanRaw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install' AND o.managed=1
		ORDER BY o.target_generation DESC LIMIT 1`,
		startLease.installationID).Scan(&sourcePlanRaw); err != nil {
		t.Fatal(err)
	}
	var sourcePlan platformInstallProjection
	if err := json.Unmarshal([]byte(sourcePlanRaw), &sourcePlan); err != nil ||
		sourcePlan.Runtime == nil {
		t.Fatal("source runtime plan", err)
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
		TargetRuntime:     sourcePlan.Runtime,
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

func platformUpdateStartRunningFixture(intent platformUpdateStartIntent, reservation journal.UpdateStartReservation, endpoint platformRuntimeEndpoint) platformUpdateStartTestObservation {
	command := intent.command
	observed := mustPlatformCheckpointInt(command.IssuedMS)
	running := journal.UpdateStartRunningReceipt{
		Format: "portal.runner.update-start-running-receipt/1-dev.1",
		Claim: journal.UpdateStartClaimMarker{
			Format:   "portal.runner.update-start-claim/1-dev.1",
			ClaimID:  "70000000-0000-4000-8000-000000001401",
			EffectID: intent.effectID, ResourceID: intent.resourceID,
			PayloadSHA256: command.PayloadSHA256, OwnershipSHA256: reservation.OwnershipSHA256,
			CheckpointReceiptSHA256: command.Payload.SourceCheckpointReceiptSHA256,
			PolicyRevision:          command.Payload.TargetPolicyRevision,
			ControlRevision:         command.ControlRevision, AdmittedFence: command.InstallationFence,
			SandboxProfile: command.Payload.SandboxProfile,
			ClaimedMS:      command.IssuedMS, ExpiresMS: command.ExpiresMS,
			Preflight: &journal.UpdateStartPreflightEvidence{
				Format:       "portal.runner.update-start-preflight/1-dev.1",
				ImageID:      "sha256:" + strings.Repeat("a", 64),
				Platform:     command.Payload.Platform,
				PolicySHA256: strings.Repeat("b", 64), MountsSHA256: strings.Repeat("c", 64),
				RootFilesystemUUID: command.Payload.RootFilesystemUUID,
				RootInode:          command.Payload.RootInode, ProjectID: command.Payload.ProjectID,
				Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
				QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes,
				SeccompSHA256: driver.UpdateStartSeccompSHA256(command.Payload.SandboxProfile),
			},
			PreflightBoundMS: command.IssuedMS,
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		NamespaceID:         command.Payload.NamespaceID,
		SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
		SourceGeneration:    command.Payload.SourceGeneration,
		TargetGeneration:    command.TargetGeneration, ControlFence: command.InstallationFence,
		SourceWriterFence: command.Payload.SourceWriterFence,
		TargetWriterFence: command.Payload.TargetWriterFence,
		DataFormat:        command.Payload.DataFormat, NativeID: strings.Repeat("e", 64),
		NativeObservedMS: strconv.FormatInt(observed, 10),
		SettledMS:        strconv.FormatInt(observed, 10),
	}
	observation := platformUpdateStartReservationFixture(intent, endpoint)
	observation.running = running
	return observation
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

func testPlatformUpdateStartPersistsReservation(t *testing.T, pg bool) {
	s, lease, _, _ := platformManagedUpdateStartFixture(t, pg)
	endpoint := platformUpdateStartEndpointFixture()
	intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformUpdateStartReservationFixture(intent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(observation, intent, endpoint)
	if !ok {
		t.Fatal("reservation observation refused")
	}
	admitted, err := s.commitPlatformUpdateStartReservation(context.Background(), lease, intent, endpoint, observation, reservation)
	if err != nil || admitted.reservation.ResourceID != reservation.ResourceID ||
		len(admitted.intent.raw) == 0 || len(admitted.intent.signature) != 64 {
		t.Fatal("persist update-start reservation", admitted, err)
	}
	var raw string
	var observedFence int64
	if err := s.st.queryRow(`SELECT receipt_json,observed_fence FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start'`, lease.id).Scan(&raw, &observedFence); err != nil {
		t.Fatal(err)
	}
	parsed, err := journal.ParseUpdateStartReservation([]byte(raw))
	if err != nil || !platformUpdateStartReservationsEqual(parsed, reservation) ||
		observedFence != lease.fence ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" {
		t.Fatal("reservation did not persist as prepared evidence", parsed, observedFence, err)
	}
	replay, err := s.commitPlatformUpdateStartReservation(context.Background(), lease, intent, endpoint, observation, reservation)
	if err != nil || !platformUpdateStartReservationsEqual(replay.reservation, reservation) ||
		len(replay.intent.raw) == 0 || len(replay.intent.signature) != 64 {
		t.Fatal("reservation replay", replay, err)
	}
	again, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil || again.reservation == nil || !platformUpdateStartReservationsEqual(*again.reservation, reservation) {
		t.Fatal("prepared intent lost reservation", again.reservation, err)
	}
}

func TestPlatformUpdateStartPersistsReservationSQLite(t *testing.T) {
	testPlatformUpdateStartPersistsReservation(t, false)
}

func TestPlatformUpdateStartPersistsReservationPostgres(t *testing.T) {
	testPlatformUpdateStartPersistsReservation(t, true)
}

func platformUpdateStartRunningCommitFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformUpdateStartIntent, platformRuntimeEndpoint, platformUpdateStartTestObservation) {
	t.Helper()
	s, lease, _, _ := platformManagedUpdateStartFixture(t, pg)
	endpoint := platformUpdateStartEndpointFixture()
	intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	reservationObservation := platformUpdateStartReservationFixture(intent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(reservationObservation, intent, endpoint)
	if !ok {
		t.Fatal("reservation observation refused")
	}
	admission, err := s.commitPlatformUpdateStartReservation(context.Background(), lease, intent, endpoint, reservationObservation, reservation)
	if err != nil {
		t.Fatal(err)
	}
	intent = admission.intent
	return s, lease, intent, endpoint, platformUpdateStartRunningFixture(intent, reservation, endpoint)
}

func testPlatformUpdateStartCommitsRunningReceipt(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation := platformUpdateStartRunningCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartReceipt(context.Background(), lease, intent, endpoint, observation)
	if err != nil || next.snapshot.Stage != appplatform.StageProbe ||
		next.snapshot.Revision != lease.snapshot.Revision+1 {
		t.Fatal("update-start running commit", next.snapshot, err)
	}
	var raw string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start'`, lease.id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	receipt, err := journal.ParseUpdateStartRunningReceipt([]byte(raw))
	if err != nil || receipt.NativeID != observation.running.NativeID ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "applied" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "probe" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_running' AND target_id=?`, lease.id) != 1 {
		t.Fatal("running receipt did not commit with stage advance", receipt, err)
	}
}

func TestPlatformUpdateStartCommitsRunningReceiptSQLite(t *testing.T) {
	testPlatformUpdateStartCommitsRunningReceipt(t, false)
}

func TestPlatformUpdateStartCommitsRunningReceiptPostgres(t *testing.T) {
	testPlatformUpdateStartCommitsRunningReceipt(t, true)
}

func testPlatformUpdateStartRunningReceiptRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.UpdateStartRunningReceipt){
		"deployment":     func(r *journal.UpdateStartRunningReceipt) { r.DeploymentID = platformFixtureID(1410) },
		"operation":      func(r *journal.UpdateStartRunningReceipt) { r.OperationID = platformFixtureID(1411) },
		"namespace":      func(r *journal.UpdateStartRunningReceipt) { r.NamespaceID = platformFixtureID(1412) },
		"target release": func(r *journal.UpdateStartRunningReceipt) { r.TargetReleaseSHA256 = strings.Repeat("f", 64) },
		"target gen":     func(r *journal.UpdateStartRunningReceipt) { r.TargetGeneration = "3" },
		"control fence":  func(r *journal.UpdateStartRunningReceipt) { r.ControlFence = "3" },
		"source writer":  func(r *journal.UpdateStartRunningReceipt) { r.SourceWriterFence = "2" },
		"target writer":  func(r *journal.UpdateStartRunningReceipt) { r.TargetWriterFence = "3" },
		"resource":       func(r *journal.UpdateStartRunningReceipt) { r.Claim.ResourceID = platformFixtureID(1413) },
		"effect":         func(r *journal.UpdateStartRunningReceipt) { r.Claim.EffectID = platformFixtureID(1414) },
		"payload":        func(r *journal.UpdateStartRunningReceipt) { r.Claim.PayloadSHA256 = strings.Repeat("a", 64) },
		"ownership":      func(r *journal.UpdateStartRunningReceipt) { r.Claim.OwnershipSHA256 = strings.Repeat("b", 64) },
		"checkpoint":     func(r *journal.UpdateStartRunningReceipt) { r.Claim.CheckpointReceiptSHA256 = strings.Repeat("c", 64) },
		"preflight":      func(r *journal.UpdateStartRunningReceipt) { r.Claim.Preflight = nil },
		"preflight data": func(r *journal.UpdateStartRunningReceipt) {
			r.Claim.Preflight.NamespaceInode = "12"
		},
		"future observed": func(r *journal.UpdateStartRunningReceipt) {
			future := strconv.FormatInt(time.Now().Add(10*time.Second).UnixMilli(), 10)
			r.NativeObservedMS, r.SettledMS = future, future
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, intent, endpoint, observation := platformUpdateStartRunningCommitFixture(t, pg)
			preflight := *observation.running.Claim.Preflight
			observation.running.Claim.Preflight = &preflight
			mutate(&observation.running)
			if _, err := s.commitPlatformUpdateStartReceipt(context.Background(), lease, intent, endpoint, observation); err == nil {
				t.Fatal("changed running receipt committed")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "prepared" ||
				scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" {
				t.Fatal("refused running receipt changed durable state")
			}
		})
	}
}

func TestPlatformUpdateStartRunningReceiptRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartRunningReceiptRefusals(t, false)
}

func TestPlatformUpdateStartRunningReceiptRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartRunningReceiptRefusals(t, true)
}

func testPlatformUpdateStartRunningRollback(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation := platformUpdateStartRunningCommitFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_start_running_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture running failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_running_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.update_start_running') EXECUTE FUNCTION fixture_update_start_running_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_running_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_start_running_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_running_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.update_start_running' BEGIN SELECT RAISE(ABORT,'fixture running failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_running_failure`)
	}
	auditBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_running' AND target_id=?`, lease.id)
	if _, err := s.commitPlatformUpdateStartReceipt(context.Background(), lease, intent, endpoint, observation); err == nil {
		t.Fatal("failed update-start running audit committed")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "start" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_running' AND target_id=?`, lease.id) != auditBefore {
		t.Fatal("failed running receipt commit was partial")
	}
}

func TestPlatformUpdateStartRunningRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartRunningRollback(t, false)
}

func TestPlatformUpdateStartRunningRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartRunningRollback(t, true)
}

func platformUpdateStartReadinessCommitFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformUpdateStartIntent, platformRuntimeEndpoint, platformUpdateStartTestObservation) {
	t.Helper()
	s, lease, runningIntent, endpoint, runningObservation := platformUpdateStartRunningCommitFixture(t, pg)
	probeLease, err := s.commitPlatformUpdateStartReceipt(context.Background(), lease, runningIntent, endpoint, runningObservation)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.preparePlatformUpdateProbe(context.Background(), probeLease)
	if err != nil || intent.running == nil {
		t.Fatal("prepare update-start probe", err)
	}
	observation := platformUpdateStartReservationFixture(intent, endpoint)
	observation.readiness = journal.UpdateStartReadiness{
		Format:               "portal.runner.update-start-readiness/1-dev.1",
		PayloadSHA256:        intent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(mustMarshalPlatformUpdateStartReceipt(*intent.running)),
		NativeID:             intent.running.NativeID, Protocol: "portal.backend/1",
		DataFormat: intent.command.Payload.DataFormat,
		ObservedMS: intent.running.NativeObservedMS,
	}
	return s, probeLease, intent, endpoint, observation
}

func testPlatformUpdateStartCommitsReadiness(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation := platformUpdateStartReadinessCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartReadiness(context.Background(), lease, intent, endpoint, observation)
	if err != nil || next.snapshot.Stage != appplatform.StageActivate ||
		next.snapshot.Revision != lease.snapshot.Revision+1 {
		t.Fatal("update-start readiness commit", next.snapshot, err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "activate" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != "candidate" ||
		scalar[int64](t, s.st, `SELECT observed_ms FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != mustPlatformCheckpointInt(observation.readiness.ObservedMS) ||
		scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) == "{}" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_ready' AND target_id=?`, lease.id) != 1 {
		t.Fatal("update-start readiness did not commit to the candidate")
	}
}

func TestPlatformUpdateStartCommitsReadinessSQLite(t *testing.T) {
	testPlatformUpdateStartCommitsReadiness(t, false)
}

func TestPlatformUpdateStartCommitsReadinessPostgres(t *testing.T) {
	testPlatformUpdateStartCommitsReadiness(t, true)
}

func platformUpdateStartRegistrationCommitFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformUpdateStartRegistrationIntent, platformRuntimeEndpoint, platformUpdateStartTestObservation) {
	t.Helper()
	s, probeLease, probeIntent, endpoint, readinessObservation := platformUpdateStartReadinessCommitFixture(t, pg)
	lease, err := s.commitPlatformUpdateStartReadiness(
		context.Background(), probeLease, probeIntent, endpoint, readinessObservation)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.preparePlatformUpdateStartRegistration(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformUpdateStartRegistrationObservationForIntent(
		t, intent, endpoint)
	return s, lease, intent, endpoint, observation
}

func platformUpdateStartRegistrationObservationForIntent(t *testing.T, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint) platformUpdateStartTestObservation {
	t.Helper()
	var receipt journal.UpdateStartRegistrationReceipt
	if err := json.Unmarshal([]byte(`{"claim":{}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	receipt.Format = "portal.runner.update-start-registration-delivery/1-dev.1"
	claim := &receipt.Claim
	claim.Format = "portal.runner.update-start-registration-claim/1-dev.1"
	claim.ClaimID = platformFixtureID(1410)
	claim.EffectID = intent.command.EffectID
	claim.PayloadSHA256 = intent.command.PayloadSHA256
	claim.SourceSHA256 = intent.command.RunningReceiptSHA256
	claim.DocumentSHA256 = intent.command.DocumentSHA256
	claim.NativeID = intent.start.running.NativeID
	claim.ControlRevision = intent.start.command.ControlRevision
	claim.ClaimedMS = strconv.FormatInt(now, 10)
	claim.ExpiresMS = intent.start.command.ExpiresMS
	claimRaw, _ := json.Marshal(receipt.Claim)
	receipt.BindingSHA256 = platformDigest(claimRaw)
	receipt.Device = "9"
	receipt.Inode = "10"
	receipt.ObservedMS = strconv.FormatInt(now, 10)
	receipt.SettledMS = receipt.ObservedMS
	observation := platformUpdateStartReservationFixture(intent.start, endpoint)
	observation.commandSHA = platformDigest(intent.raw)
	observation.action = transport.UpdateStartRegistrationDeliver
	observation.registration = receipt
	if !validPlatformUpdateStartRegistrationObservation(observation, intent, endpoint) {
		t.Fatal("registration observation fixture refused")
	}
	return observation
}

func platformUpdateRecoveryRestoreFixture(t *testing.T, pg bool) (*Server, platformOperationLease, journal.CheckpointReceipt) {
	t.Helper()
	s, updateLease, registrationIntent, _, _ :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET stage='recovery_required',state_revision=10,row_revision=row_revision+1,
		error_code='update_recovery_required',completed_ms=?,updated_ms=?,
		lease_owner=NULL,lease_until_ms=NULL WHERE id=?`,
		now, now, updateLease.id)
	mustExec(t, s.st, `UPDATE app_platform_installations
		SET phase='recovery_required',revision=revision+1,updated_ms=?
		WHERE id=?`, now, updateLease.installationID)
	var checkpointRaw string
	if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND state='applied'`,
		updateLease.id).Scan(&checkpointRaw); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := journal.ParseCheckpointReceipt([]byte(checkpointRaw))
	if err != nil {
		t.Fatal(err)
	}
	var recoverySetID string
	if err := s.st.queryRow(`SELECT id FROM app_platform_recovery_sets
		WHERE operation_id=? AND state='verified'`, updateLease.id).
		Scan(&recoverySetID); err != nil {
		t.Fatal(err)
	}
	var sourcePlanID, actorScope, epoch string
	if err := s.st.queryRow(`SELECT review_plan_id,actor_scope,controller_epoch
		FROM app_platform_operations WHERE id=?`, updateLease.id).
		Scan(&sourcePlanID, &actorScope, &epoch); err != nil {
		t.Fatal(err)
	}
	restorePlanID, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`INSERT INTO app_platform_review_plans(
		id,installation_id,app_id,publisher_id,action,source_release_sha256,
		target_release_sha256,installation_revision,policy_revision,
		trust_revision,controller_epoch,actor_scope,username,account_created,
		account_generation,authority_fingerprint,plan_sha256,plan_json,
		expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'restore',
		source_release_sha256,source_release_sha256,installation_revision,
		policy_revision,trust_revision,controller_epoch,actor_scope,username,
		account_created,account_generation,authority_fingerprint,plan_sha256,
		plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=?`, restorePlanID, sourcePlanID); err != nil {
		t.Fatal(err)
	}
	restorePlan := platformManagedRestorePlan{
		Format:                platformManagedRestoreFormat,
		InstallationID:        updateLease.installationID,
		RecoverySetID:         recoverySetID,
		CheckpointOperationID: checkpoint.OperationID,
		CheckpointEffectID:    checkpoint.EffectID,
		CheckpointID:          checkpoint.CheckpointID,
		ReleaseSHA256:         checkpoint.ReleaseSHA256,
		ManifestSHA256:        checkpoint.ManifestSHA256,
		ArchiveBytes:          mustPlatformCheckpointInt(checkpoint.ArchiveBytes),
		SourceGeneration:      1, TargetGeneration: 2,
		ControllerEpoch: epoch,
	}
	restorePlanRaw, _ := json.Marshal(restorePlan)
	mustExec(t, s.st, `UPDATE app_platform_review_plans
		SET installation_id=?,target_release_sha256=?,plan_json=?,plan_sha256=?
		WHERE id=?`, updateLease.installationID, checkpoint.ReleaseSHA256,
		string(restorePlanRaw), platformDigest(restorePlanRaw), restorePlanID)
	restoreID, err := platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`INSERT INTO app_platform_operations(
		id,installation_id,review_plan_id,actor_scope,idempotency_sha256,
		request_sha256,action,managed,source_generation,target_generation,
		stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,
		controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,'restore',1,1,2,'quiesce',4,1,NULL,NULL,0,?,
		NULL,NULL,?,?)`, restoreID, updateLease.installationID, restorePlanID,
		actorScope, strings.Repeat("5", 64), strings.Repeat("6", 64),
		epoch, now, now); err != nil {
		t.Fatal(err)
	}
	selected, err := s.claimNextPlatformManagedRestore(
		context.Background(), platformFixtureID(1601))
	if err != nil {
		t.Fatal(err)
	}
	restoreLease := selected.lease
	if restoreLease.id != restoreID ||
		restoreLease.snapshot.Action != appplatform.ActionRestore ||
		!restoreLease.snapshot.Managed ||
		restoreLease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("restore quiesce lease", restoreLease, err)
	}
	_ = registrationIntent
	return s, restoreLease, checkpoint
}

func platformUpdateStartBootFixture(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartRegistrationIntent, platformUpdateStartBootObservation) {
	t.Helper()
	applied, err := s.preparePlatformUpdateStartRegistration(
		context.Background(), lease)
	if err != nil || !applied.applied {
		t.Fatal("prepare applied registration", err)
	}
	signer, err := applied.registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	now := time.Now()
	challenge, err := signer.NewChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	bootID := "update-boot/42"
	answer, err := signer.AnswerChallenge(challenge, bootID, now)
	if err != nil {
		t.Fatal(err)
	}
	receiptRaw, _ := json.Marshal(applied.receipt)
	proof := platformUpdateStartHandshakeTestObservation{
		evidence: journal.UpdateStartBackendHandshakeEvidence{
			Format:                "portal.runner.update-start-backend-handshake/1-dev.1",
			PayloadSHA256:         applied.command.PayloadSHA256,
			DeliveryReceiptSHA256: platformDigest(receiptRaw),
			NativeID:              applied.start.running.NativeID,
			ChallengeSHA256:       platformDigest([]byte(challenge)),
			Answer:                answer, ObservedMS: strconv.FormatInt(now.UnixMilli(), 10),
		},
		authority: applied.start.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0,
			ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now(), commandSHA: platformDigest(applied.raw),
	}
	if !validPlatformUpdateStartHandshakeObservation(
		proof, applied, endpoint, challenge) {
		t.Fatal("update-start handshake fixture refused")
	}
	boot, err := s.finishPlatformUpdateStartBoot(
		context.Background(), lease, applied, endpoint, challenge, proof)
	if err != nil {
		t.Fatal(err)
	}
	return applied, boot
}

func platformRestoreStartRegistrationObservationFixture(t *testing.T, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint) platformUpdateStartTestObservation {
	t.Helper()
	var receipt journal.UpdateStartRegistrationReceipt
	if err := json.Unmarshal([]byte(`{"claim":{}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	receipt.Format = "portal.runner.update-start-registration-delivery/1-dev.1"
	claim := &receipt.Claim
	claim.Format = "portal.runner.update-start-registration-claim/1-dev.1"
	claim.ClaimID = platformFixtureID(1602)
	claim.EffectID = intent.command.EffectID
	claim.PayloadSHA256 = intent.command.PayloadSHA256
	claim.SourceSHA256 = intent.command.RunningReceiptSHA256
	claim.DocumentSHA256 = intent.command.DocumentSHA256
	claim.NativeID = intent.start.running.NativeID
	claim.ControlRevision = intent.start.command.ControlRevision
	claim.ClaimedMS = strconv.FormatInt(now, 10)
	claim.ExpiresMS = intent.start.command.ExpiresMS
	claimRaw, _ := json.Marshal(receipt.Claim)
	receipt.BindingSHA256 = platformDigest(claimRaw)
	receipt.Device = "9"
	receipt.Inode = "10"
	receipt.ObservedMS = strconv.FormatInt(now, 10)
	receipt.SettledMS = receipt.ObservedMS
	observation := platformUpdateStartReservationFixture(
		intent.start, endpoint)
	observation.commandSHA = platformDigest(intent.raw)
	observation.action = transport.UpdateStartRegistrationDeliver
	observation.registration = receipt
	if !validPlatformUpdateStartRegistrationObservation(
		observation, intent, endpoint) {
		t.Fatal("restore registration observation fixture refused")
	}
	return observation
}

func platformRestoreStartBootFixture(t *testing.T, s *Server, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartRegistrationIntent, platformUpdateStartBootObservation) {
	t.Helper()
	applied, err := s.preparePlatformRestoreStartRegistration(
		context.Background(), lease)
	if err != nil || !applied.applied {
		t.Fatal("prepare applied restore registration", err)
	}
	signer, err := applied.registration.Signer("")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	now := time.Now()
	challenge, err := signer.NewChallenge(now)
	if err != nil {
		t.Fatal(err)
	}
	bootID := "restore-boot/42"
	answer, err := signer.AnswerChallenge(challenge, bootID, now)
	if err != nil {
		t.Fatal(err)
	}
	receiptRaw, _ := json.Marshal(applied.receipt)
	proof := platformUpdateStartHandshakeTestObservation{
		evidence: journal.UpdateStartBackendHandshakeEvidence{
			Format:                "portal.runner.update-start-backend-handshake/1-dev.1",
			PayloadSHA256:         applied.command.PayloadSHA256,
			DeliveryReceiptSHA256: platformDigest(receiptRaw),
			NativeID:              applied.start.running.NativeID,
			ChallengeSHA256:       platformDigest([]byte(challenge)),
			Answer:                answer, ObservedMS: strconv.FormatInt(now.UnixMilli(), 10),
		},
		authority: applied.start.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0,
			ControllerUID: endpoint.controllerUID,
			ControllerGID: endpoint.controllerGID, RootController: true,
		},
		receivedAt: time.Now(), commandSHA: platformDigest(applied.raw),
	}
	boot, err := s.finishPlatformRestoreStartBoot(
		context.Background(), lease, applied, endpoint, challenge, proof)
	if err != nil {
		t.Fatal(err)
	}
	if boot.bootID != bootID {
		t.Fatal("restore boot identity", boot)
	}
	return applied, boot
}

func platformRestoreStartReadyFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformRuntimeEndpoint) {
	t.Helper()
	s, lease, _ := platformUpdateRecoveryRestoreFixture(t, pg)
	endpoint := platformUpdateStartEndpointFixture()
	stop, err := s.preparePlatformUpdateTargetRemoval(
		context.Background(), lease, "stop", 1)
	if err != nil {
		t.Fatal(err)
	}
	stopProof := platformUpdateTargetRemovalObservationFixture(
		stop, endpoint, "stopped")
	next, err := s.commitPlatformUpdateTargetRemoval(
		context.Background(), lease, stop, endpoint, stopProof)
	if err != nil {
		t.Fatal(err)
	}
	remove, err := s.preparePlatformUpdateTargetRemoval(
		context.Background(), next, "remove_code", 2)
	if err != nil {
		t.Fatal(err)
	}
	removeProof := platformUpdateTargetRemovalObservationFixture(
		remove, endpoint, "absent")
	removed, err := s.commitPlatformUpdateTargetRemoval(
		context.Background(), next, remove, endpoint, removeProof)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.advancePlatformRestoreStage(
		context.Background(), removed, appplatform.StageSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoreData, err := s.advancePlatformRestoreStage(
		context.Background(), snapshot, appplatform.StageRestoreData)
	if err != nil {
		t.Fatal(err)
	}
	restoreIntent, err := s.preparePlatformRestore(
		context.Background(), restoreData)
	if err != nil {
		t.Fatal(err)
	}
	restoreProof := platformRestoreObservationFixture(
		restoreIntent, endpoint)
	startLease, err := s.commitPlatformRestore(
		context.Background(), restoreData, restoreIntent,
		endpoint, restoreProof)
	if err != nil {
		t.Fatal(err)
	}
	startIntent, err := s.preparePlatformRestoreStart(
		context.Background(), startLease)
	if err != nil {
		t.Fatal(err)
	}
	reservation := platformUpdateStartReservationFixture(
		startIntent, endpoint).reservation
	reserved, err := s.commitPlatformRestoreStartReservation(
		context.Background(), startLease, startIntent,
		endpoint, reservation)
	if err != nil {
		t.Fatal(err)
	}
	runningProof := platformUpdateStartRunningFixture(
		reserved.intent, reservation, endpoint)
	probeLease, err := s.commitPlatformRestoreStartRunning(
		context.Background(), reserved.lease, reserved.intent,
		endpoint, runningProof)
	if err != nil {
		t.Fatal(err)
	}
	probeIntent, err := s.preparePlatformRestoreProbe(
		context.Background(), probeLease)
	if err != nil {
		t.Fatal(err)
	}
	readinessProof := platformUpdateStartReservationFixture(
		probeIntent, endpoint)
	readinessProof.readiness = journal.UpdateStartReadiness{
		Format:        "portal.runner.update-start-readiness/1-dev.1",
		PayloadSHA256: probeIntent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(
			mustMarshalPlatformUpdateStartReceipt(*probeIntent.running)),
		NativeID:   probeIntent.running.NativeID,
		Protocol:   "portal.backend/1",
		DataFormat: probeIntent.command.Payload.DataFormat,
		ObservedMS: probeIntent.running.NativeObservedMS,
	}
	activateLease, err := s.commitPlatformRestoreStartReadiness(
		context.Background(), probeLease, probeIntent,
		endpoint, readinessProof)
	if err != nil {
		t.Fatal(err)
	}
	return s, activateLease, endpoint
}

func platformRestoreStartRegistrationCommitFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformUpdateStartRegistrationIntent, platformRuntimeEndpoint, platformUpdateStartTestObservation) {
	t.Helper()
	s, lease, endpoint := platformRestoreStartReadyFixture(t, pg)
	intent, err := s.preparePlatformRestoreStartRegistration(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformRestoreStartRegistrationObservationFixture(
		t, intent, endpoint)
	return s, lease, intent, endpoint, observation
}

func platformUpdateTargetRemovalObservationFixture(intent platformUpdateTargetRemovalIntent, endpoint platformRuntimeEndpoint, state string) platformUpdateTargetRemovalTestObservation {
	now := time.Now().UnixMilli()
	issued, _ := strconv.ParseInt(
		intent.start.command.IssuedMS, 10, 64)
	if now < issued {
		now = issued
	}
	receipt := journal.ManagedRemovalReceipt{
		Format:            "portal.runner.managed-removal-receipt/1-dev.1",
		DeploymentID:      intent.start.command.DeploymentID,
		ControllerEpoch:   intent.start.command.ControllerEpoch,
		InstallationID:    intent.start.command.InstallationID,
		OperationID:       intent.command.OperationID,
		SourceOperationID: intent.start.command.OperationID,
		SourceEffectID:    intent.start.effectID,
		TargetGeneration:  intent.start.command.TargetGeneration,
		Stage:             intent.command.Stage, EffectID: intent.command.EffectID,
		PayloadSHA256:       intent.command.PayloadSHA256,
		SourceReceiptSHA256: intent.command.RunningReceiptSHA256,
		ResourceID:          intent.start.resourceID,
		DataNamespaceID:     intent.start.command.Payload.NamespaceID,
		ReleaseSHA256:       intent.start.command.Payload.TargetReleaseSHA256,
		NativeID:            intent.nativeID, State: state,
		ObservedMS: strconv.FormatInt(now, 10),
		SettledMS:  strconv.FormatInt(now, 10),
	}
	return platformUpdateTargetRemovalTestObservation{
		receipt: receipt, authority: intent.authority,
		endpoint: transport.CandidateReceiptEndpoint{
			Path: endpoint.path, RunnerUID: 0,
			ControllerUID:  endpoint.controllerUID,
			ControllerGID:  endpoint.controllerGID,
			RootController: true,
		},
		receivedAt: time.Now(), commandSHA: platformDigest(intent.raw),
	}
}

func TestPlatformUpdateStartRegistrationCommitSQLite(t *testing.T) {
	s, lease, intent, endpoint, observation := platformUpdateStartRegistrationCommitFixture(t, false)
	grants := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint, observation)
	if err != nil || next.snapshot.Stage != appplatform.StageActivate ||
		next.rowRevision != lease.rowRevision+1 {
		t.Fatal("update-start registration commit", next, err)
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != "applied" ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "activate" ||
		scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != grants {
		t.Fatal("registration advanced activation or grants")
	}
	applied, err := s.preparePlatformUpdateStartRegistration(context.Background(), next)
	if err != nil || !applied.applied || applied.receipt != observation.registration {
		t.Fatal("registration replay", applied.applied, err)
	}
	defer applied.registration.Close()
}

func TestPlatformUpdateStartBootBindsRegistrationSQLite(t *testing.T) {
	testPlatformUpdateStartBootBindsRegistration(t, false)
}

func TestPlatformUpdateStartBootBindsRegistrationPostgres(t *testing.T) {
	testPlatformUpdateStartBootBindsRegistration(t, true)
}

func testPlatformUpdateStartBootBindsRegistration(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
	defer applied.registration.Close()
	receiptRaw, _ := json.Marshal(applied.receipt)
	if boot.bootID != "update-boot/42" ||
		boot.nativeID != applied.start.running.NativeID ||
		boot.deliverySHA256 != platformDigest(receiptRaw) {
		t.Fatal("update-start boot", boot)
	}
}

func TestPlatformUpdateStartActivationSQLite(t *testing.T) {
	testPlatformUpdateStartActivation(t, false)
}

func TestPlatformUpdateTargetRemovalSQLite(t *testing.T) {
	testPlatformUpdateTargetRemoval(t, false)
}

func TestPlatformUpdateTargetRemovalPostgres(t *testing.T) {
	testPlatformUpdateTargetRemoval(t, true)
}

func TestPlatformRestoreRetryExhaustionSQLite(t *testing.T) {
	testPlatformRestoreRetryExhaustion(t, false)
}

func TestPlatformRestoreRetryExhaustionPostgres(t *testing.T) {
	testPlatformRestoreRetryExhaustion(t, true)
}

func testPlatformRestoreRetryExhaustion(t *testing.T, pg bool) {
	s, lease, _ := platformUpdateRecoveryRestoreFixture(t, pg)
	if err := s.finishPlatformManagedRestoreForReview(
		context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code
		FROM app_platform_operations WHERE id=?`, lease.id) !=
		"recovery_required:restore_recovery_required" ||
		scalar[string](t, s.st, `SELECT phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "recovery_required" {
		t.Fatal("restore retry exhaustion was not retained")
	}
}

func testPlatformUpdateTargetRemoval(t *testing.T, pg bool) {
	s, lease, _ := platformUpdateRecoveryRestoreFixture(t, pg)
	endpoint := platformUpdateStartEndpointFixture()
	stop, err := s.preparePlatformUpdateTargetRemoval(
		context.Background(), lease, "stop", 1)
	if err != nil {
		t.Fatal(err)
	}
	stopProof := platformUpdateTargetRemovalObservationFixture(
		stop, endpoint, "stopped")
	next, err := s.commitPlatformUpdateTargetRemoval(
		context.Background(), lease, stop, endpoint, stopProof)
	if err != nil || next.rowRevision != lease.rowRevision+1 {
		t.Fatal("stop update target", next, err)
	}
	if scalar[string](t, s.st, `SELECT state
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
		lease.id) != "applied" {
		t.Fatal("stop effect not applied")
	}
	remove, err := s.preparePlatformUpdateTargetRemoval(
		context.Background(), next, "remove_code", 2)
	if err != nil {
		t.Fatal(err)
	}
	removeProof := platformUpdateTargetRemovalObservationFixture(
		remove, endpoint, "absent")
	removed, err := s.commitPlatformUpdateTargetRemoval(
		context.Background(), next, remove, endpoint, removeProof)
	if err != nil {
		t.Fatal(err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce' AND state='applied'`,
		lease.id) != 2 {
		t.Fatal("remove-code effect not applied")
	}
	snapshot, err := s.advancePlatformRestoreStage(
		context.Background(), removed, appplatform.StageSnapshot)
	if err != nil || snapshot.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("restore quiesce completion", snapshot, err)
	}
	restoreData, err := s.advancePlatformRestoreStage(
		context.Background(), snapshot, appplatform.StageRestoreData)
	if err != nil || restoreData.snapshot.Stage != appplatform.StageRestoreData {
		t.Fatal("restore snapshot progression", restoreData, err)
	}
	restoreIntent, err := s.preparePlatformRestore(
		context.Background(), restoreData)
	if err != nil {
		t.Fatal(err)
	}
	restoreProof := platformRestoreObservationFixture(
		restoreIntent, endpoint)
	startLease, err := s.commitPlatformRestore(
		context.Background(), restoreData, restoreIntent,
		endpoint, restoreProof)
	if err != nil || startLease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("restore data did not advance to start", startLease, err)
	}
	startIntent, err := s.preparePlatformRestoreStart(
		context.Background(), startLease)
	if err != nil || startIntent.command.Kind != "restore-start" ||
		startIntent.command.OperationID != startLease.id ||
		startIntent.command.Payload.SourceCheckpointOperationID ==
			startLease.id ||
		startIntent.command.Payload.TargetReleaseSHA256 !=
			startIntent.checkpoint.ReleaseSHA256 {
		t.Fatal("restore-start intent", startIntent, err)
	}
	again, err := s.preparePlatformRestoreStart(
		context.Background(), startLease)
	if err != nil || again.effectID != startIntent.effectID ||
		again.resourceID != startIntent.resourceID ||
		again.command.PayloadSHA256 !=
			startIntent.command.PayloadSHA256 {
		t.Fatal("restore-start replay", again, err)
	}
	reservation := platformUpdateStartReservationFixture(
		startIntent, endpoint).reservation
	reserved, err := s.commitPlatformRestoreStartReservation(
		context.Background(), startLease, startIntent,
		endpoint, reservation)
	if err != nil || reserved.intent.reservation == nil {
		t.Fatal("restore-start reservation", reserved, err)
	}
	runningProof := platformUpdateStartRunningFixture(
		reserved.intent, reservation, endpoint)
	probeLease, err := s.commitPlatformRestoreStartRunning(
		context.Background(), reserved.lease, reserved.intent,
		endpoint, runningProof)
	if err != nil || probeLease.snapshot.Stage != appplatform.StageProbe {
		t.Fatal("restore-start running receipt", probeLease, err)
	}
	probeIntent, err := s.preparePlatformRestoreProbe(
		context.Background(), probeLease)
	if err != nil || probeIntent.running == nil {
		t.Fatal("restore-start probe intent", err)
	}
	readinessProof := platformUpdateStartReservationFixture(
		probeIntent, endpoint)
	readinessProof.readiness = journal.UpdateStartReadiness{
		Format:        "portal.runner.update-start-readiness/1-dev.1",
		PayloadSHA256: probeIntent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(
			mustMarshalPlatformUpdateStartReceipt(*probeIntent.running)),
		NativeID:   probeIntent.running.NativeID,
		Protocol:   "portal.backend/1",
		DataFormat: probeIntent.command.Payload.DataFormat,
		ObservedMS: probeIntent.running.NativeObservedMS,
	}
	activateLease, err := s.commitPlatformRestoreStartReadiness(
		context.Background(), probeLease, probeIntent,
		endpoint, readinessProof)
	if err != nil || activateLease.snapshot.Stage !=
		appplatform.StageActivate {
		t.Fatal("restore-start readiness", activateLease, err)
	}
	registration, err := s.preparePlatformRestoreStartRegistration(
		context.Background(), activateLease)
	if err != nil {
		t.Fatal(err)
	}
	registrationProof :=
		platformRestoreStartRegistrationObservationFixture(
			t, registration, endpoint)
	registered, err := s.commitPlatformRestoreStartRegistration(
		context.Background(), activateLease, registration,
		endpoint, registrationProof)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformRestoreStartBootFixture(
		t, s, registered, endpoint)
	defer applied.registration.Close()
	result, err := s.commitPlatformRestoreStartActivation(
		context.Background(), registered, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("restore activation", result, err)
	}
	if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision
		FROM app_platform_installations WHERE id=?`,
		lease.installationID) != "ready:2:1" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='retired' AND generation=1`,
			lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active' AND generation=2`,
			lease.installationID) != 1 ||
		scalar[int64](t, s.st, `SELECT writer_fence
			FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`,
			lease.installationID) != 2 ||
		scalar[string](t, s.st, `SELECT stage
			FROM app_platform_operations WHERE id=?`,
			lease.id) != "complete" {
		t.Fatal("restore did not activate the restored generation")
	}
}

func TestPlatformRestoreStartRegistrationRequiresAppliedStartEffectSQLite(t *testing.T) {
	testPlatformRestoreStartRegistrationRequiresAppliedStartEffect(t, false)
}

func TestPlatformRestoreStartRegistrationRequiresAppliedStartEffectPostgres(t *testing.T) {
	testPlatformRestoreStartRegistrationRequiresAppliedStartEffect(t, true)
}

func testPlatformRestoreStartRegistrationRequiresAppliedStartEffect(t *testing.T, pg bool) {
	s, lease, _ := platformRestoreStartReadyFixture(t, pg)
	mustExec(t, s.st, `DELETE FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start'`, lease.id)
	if _, err := s.preparePlatformRestoreStartRegistration(
		context.Background(), lease); err == nil {
		t.Fatal("restore registration synthesized a missing start effect")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start'`, lease.id) != 0 {
		t.Fatal("missing restore-start effect was recreated at activate")
	}
}

func TestPlatformRestoreStartRegistrationRefusalsSQLite(t *testing.T) {
	testPlatformRestoreStartRegistrationRefusals(t, false)
}

func TestPlatformRestoreStartRegistrationRefusalsPostgres(t *testing.T) {
	testPlatformRestoreStartRegistrationRefusals(t, true)
}

func testPlatformRestoreStartRegistrationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.UpdateStartRegistrationReceipt){
		"payload": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.PayloadSHA256 = strings.Repeat("1", 64)
		},
		"running": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.SourceSHA256 = strings.Repeat("2", 64)
		},
		"document": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.DocumentSHA256 = strings.Repeat("3", 64)
		},
		"native": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.NativeID = strings.Repeat("4", 64)
		},
		"future": func(r *journal.UpdateStartRegistrationReceipt) {
			r.ObservedMS = strconv.FormatInt(
				time.Now().Add(10*time.Second).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, intent, endpoint, observation :=
				platformRestoreStartRegistrationCommitFixture(t, pg)
			mutate(&observation.registration)
			if _, err := s.commitPlatformRestoreStartRegistration(
				context.Background(), lease, intent, endpoint,
				observation); err == nil {
				t.Fatal("changed restore registration committed")
			}
			if scalar[string](t, s.st, `SELECT state
				FROM app_platform_operation_effects
				WHERE operation_id=? AND stage='activate'`,
				lease.id) != "prepared" ||
				scalar[int64](t, s.st, `SELECT row_revision
					FROM app_platform_operations WHERE id=?`,
					lease.id) != lease.rowRevision {
				t.Fatal("refused restore registration changed durable state")
			}
		})
	}
}

func TestPlatformRestoreStartActivationRefusesChangedBootSQLite(t *testing.T) {
	testPlatformRestoreStartActivationRefusesChangedBoot(t, false)
}

func TestPlatformRestoreStartActivationRefusesChangedBootPostgres(t *testing.T) {
	testPlatformRestoreStartActivationRefusesChangedBoot(t, true)
}

func testPlatformRestoreStartActivationRefusesChangedBoot(t *testing.T, pg bool) {
	s, lease, intent, endpoint, registrationObservation :=
		platformRestoreStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformRestoreStartRegistration(
		context.Background(), lease, intent, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformRestoreStartBootFixture(
		t, s, next, endpoint)
	defer applied.registration.Close()
	for name, mutate := range map[string]func(*platformUpdateStartBootObservation){
		"native": func(b *platformUpdateStartBootObservation) {
			b.nativeID = strings.Repeat("1", 64)
		},
		"document": func(b *platformUpdateStartBootObservation) {
			b.documentSHA256 = strings.Repeat("2", 64)
		},
		"delivery": func(b *platformUpdateStartBootObservation) {
			b.deliverySHA256 = strings.Repeat("3", 64)
		},
		"boot": func(b *platformUpdateStartBootObservation) {
			b.bootID = ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := boot
			mutate(&changed)
			if _, err := s.commitPlatformRestoreStartActivation(
				context.Background(), next, applied, changed); err == nil {
				t.Fatal("changed restore boot activated")
			}
			if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision
				FROM app_platform_installations WHERE id=?`,
				lease.installationID) != "recovery_required:1:1" ||
				scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
					WHERE installation_id=? AND state='candidate'
					AND generation=2`, lease.installationID) != 1 {
				t.Fatal("refused restore boot changed the active tuple")
			}
		})
	}
}

func TestPlatformRestoreStartActivationRollbackSQLite(t *testing.T) {
	testPlatformRestoreStartActivationRollback(t, false)
}

func TestPlatformRestoreStartActivationRollbackPostgres(t *testing.T) {
	testPlatformRestoreStartActivationRollback(t, true)
}

func testPlatformRestoreStartActivationRollback(t *testing.T, pg bool) {
	s, lease, intent, endpoint, registrationObservation :=
		platformRestoreStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformRestoreStartRegistration(
		context.Background(), lease, intent, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformRestoreStartBootFixture(
		t, s, next, endpoint)
	defer applied.registration.Close()
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_restore_activation_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture restore activation failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_activation_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.restore_activated') EXECUTE FUNCTION fixture_restore_activation_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_restore_activation_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_restore_activation_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_restore_activation_failure
			BEFORE INSERT ON audit_log
			WHEN NEW.action='app_platform.restore_activated'
			BEGIN SELECT RAISE(ABORT,'fixture restore activation failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS
			fixture_restore_activation_failure`)
	}
	if _, err := s.commitPlatformRestoreStartActivation(
		context.Background(), next, applied, boot); err == nil {
		t.Fatal("failed restore activation audit committed")
	}
	if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision
		FROM app_platform_installations WHERE id=?`,
		lease.installationID) != "recovery_required:1:1" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active' AND generation=1`,
			lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='candidate' AND generation=2`,
			lease.installationID) != 1 ||
		scalar[int64](t, s.st, `SELECT writer_fence
			FROM app_platform_data_namespaces
			WHERE owner_installation_id=?`,
			lease.installationID) != 1 ||
		scalar[string](t, s.st, `SELECT stage
			FROM app_platform_operations WHERE id=?`,
			lease.id) != "activate" {
		t.Fatal("failed restore activation was partial")
	}
}

func TestPlatformUpdateStartActivationPostgres(t *testing.T) {
	testPlatformUpdateStartActivation(t, true)
}

func testPlatformUpdateStartActivation(t *testing.T, pg bool) {
	s, lease, intent, endpoint, registrationObservation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
	defer applied.registration.Close()
	accessBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID)
	result, err := s.commitPlatformUpdateStartActivation(
		context.Background(), next, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete ||
		result.activationID != applied.start.resourceID {
		t.Fatal("update activation", result, err)
	}
	if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision FROM app_platform_installations WHERE id=?`, lease.installationID) != "ready:2:2" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND generation=2 AND policy_revision=2`, lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='retired' AND generation=1`, lease.installationID) != 1 ||
		scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != 2 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision=2`, lease.installationID) == 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?`, lease.installationID) != accessBefore ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "complete" {
		t.Fatal("active generation tuple not committed atomically")
	}
}

func TestPlatformUpdateStartActivationRefusesChangedBootSQLite(t *testing.T) {
	testPlatformUpdateStartActivationRefusesChangedBoot(t, false)
}

func TestPlatformUpdateStartActivationRefusesChangedBootPostgres(t *testing.T) {
	testPlatformUpdateStartActivationRefusesChangedBoot(t, true)
}

func testPlatformUpdateStartActivationRefusesChangedBoot(t *testing.T, pg bool) {
	s, lease, intent, endpoint, registrationObservation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
	defer applied.registration.Close()
	for name, mutate := range map[string]func(*platformUpdateStartBootObservation){
		"native":   func(b *platformUpdateStartBootObservation) { b.nativeID = strings.Repeat("1", 64) },
		"document": func(b *platformUpdateStartBootObservation) { b.documentSHA256 = strings.Repeat("2", 64) },
		"delivery": func(b *platformUpdateStartBootObservation) { b.deliverySHA256 = strings.Repeat("3", 64) },
		"boot":     func(b *platformUpdateStartBootObservation) { b.bootID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := boot
			mutate(&changed)
			if _, err := s.commitPlatformUpdateStartActivation(
				context.Background(), next, applied, changed); err == nil {
				t.Fatal("changed boot activated")
			}
			if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision FROM app_platform_installations WHERE id=?`, lease.installationID) != "ready:1:1" ||
				scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND generation=2`, lease.installationID) != 1 {
				t.Fatal("refused boot changed active tuple")
			}
		})
	}
}

func TestPlatformUpdateStartActivationRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartActivationRollback(t, false)
}

func TestPlatformUpdateStartActivationRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartActivationRollback(t, true)
}

func testPlatformUpdateStartActivationRollback(t *testing.T, pg bool) {
	s, lease, intent, endpoint, registrationObservation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
	defer applied.registration.Close()
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_managed_update_activation_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture managed update activation failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_managed_update_activation_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.managed_update_activated') EXECUTE FUNCTION fixture_managed_update_activation_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_managed_update_activation_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_managed_update_activation_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_managed_update_activation_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.managed_update_activated' BEGIN SELECT RAISE(ABORT,'fixture managed update activation failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_managed_update_activation_failure`)
	}
	if _, err := s.commitPlatformUpdateStartActivation(
		context.Background(), next, applied, boot); err == nil {
		t.Fatal("failed update activation audit committed")
	}
	if scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision FROM app_platform_installations WHERE id=?`, lease.installationID) != "ready:1:1" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND generation=1`, lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND generation=2`, lease.installationID) != 1 ||
		scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE owner_installation_id=?`, lease.installationID) != 1 ||
		scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "activate" {
		t.Fatal("failed update activation was partial")
	}
}

func TestPlatformManagedUpdateRetryExhaustionSQLite(t *testing.T) {
	testPlatformManagedUpdateRetryExhaustion(t, false)
}

func TestPlatformManagedUpdateRetryExhaustionPostgres(t *testing.T) {
	testPlatformManagedUpdateRetryExhaustion(t, true)
}

func testPlatformManagedUpdateRetryExhaustion(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	activeBefore := scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, lease.installationID)
	capabilitiesBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision=1`, lease.installationID)
	if err := s.finishPlatformUpdateForReview(
		context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code FROM app_platform_operations WHERE id=?`, lease.id) != "recovery_required:update_recovery_required" ||
		scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "recovery_required" ||
		scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, lease.installationID) != activeBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate' AND generation=2`, lease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND policy_revision=1`, lease.installationID) != capabilitiesBefore ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_recovery_required' AND target_id=?`, lease.id) == 0 {
		t.Fatal("managed update exhaustion did not preserve source/recovery state")
	}
}

func TestPlatformUpdateStartRegistrationCommitPostgres(t *testing.T) {
	testPlatformUpdateStartRegistrationCommit(t, true)
}

func testPlatformUpdateStartRegistrationCommit(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	next, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint, observation)
	if err != nil || next.rowRevision != lease.rowRevision+1 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != "applied" {
		t.Fatal("update-start registration commit", next, err)
	}
}

func testPlatformUpdateStartRegistrationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.UpdateStartRegistrationReceipt){
		"payload": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.PayloadSHA256 = strings.Repeat("1", 64)
		},
		"running": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.SourceSHA256 = strings.Repeat("2", 64)
		},
		"document": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.DocumentSHA256 = strings.Repeat("3", 64)
		},
		"native": func(r *journal.UpdateStartRegistrationReceipt) {
			r.Claim.NativeID = strings.Repeat("4", 64)
		},
		"future": func(r *journal.UpdateStartRegistrationReceipt) {
			r.ObservedMS = strconv.FormatInt(time.Now().Add(10*time.Second).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, intent, endpoint, observation :=
				platformUpdateStartRegistrationCommitFixture(t, pg)
			mutate(&observation.registration)
			if _, err := s.commitPlatformUpdateStartRegistration(
				context.Background(), lease, intent, endpoint,
				observation); err == nil {
				t.Fatal("changed registration receipt committed")
			}
			if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != "prepared" ||
				scalar[int64](t, s.st, `SELECT row_revision FROM app_platform_operations WHERE id=?`, lease.id) != lease.rowRevision {
				t.Fatal("refused registration changed durable state")
			}
		})
	}
}

func TestPlatformUpdateStartRegistrationRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartRegistrationRefusals(t, false)
}

func TestPlatformUpdateStartRegistrationRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartRegistrationRefusals(t, true)
}

func testPlatformUpdateStartRegistrationSourceRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*testing.T, *Server, platformOperationLease){
		"stale readiness": func(t *testing.T, s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_activations SET observed_ms=? WHERE installation_id=? AND generation=2`,
				time.Now().Add(-10*time.Second).UnixMilli(), lease.installationID)
		},
		"candidate tuple": func(t *testing.T, s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_activations SET policy_revision=policy_revision+1 WHERE installation_id=? AND generation=2`,
				lease.installationID)
		},
		"running receipt": func(t *testing.T, s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}' WHERE operation_id=? AND stage='start'`, lease.id)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, probeLease, probeIntent, endpoint, readinessObservation :=
				platformUpdateStartReadinessCommitFixture(t, pg)
			lease, err := s.commitPlatformUpdateStartReadiness(
				context.Background(), probeLease, probeIntent, endpoint,
				readinessObservation)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, s, lease)
			if _, err := s.preparePlatformUpdateStartRegistration(
				context.Background(), lease); err == nil {
				t.Fatal("changed registration source admitted")
			}
		})
	}
}

func TestPlatformUpdateStartRegistrationSourceRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartRegistrationSourceRefusals(t, false)
}

func TestPlatformUpdateStartRegistrationSourceRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartRegistrationSourceRefusals(t, true)
}

func testPlatformUpdateStartRegistrationPrepareRollback(t *testing.T, pg bool) {
	s, probeLease, probeIntent, endpoint, readinessObservation :=
		platformUpdateStartReadinessCommitFixture(t, pg)
	lease, err := s.commitPlatformUpdateStartReadiness(
		context.Background(), probeLease, probeIntent, endpoint,
		readinessObservation)
	if err != nil {
		t.Fatal(err)
	}
	auditBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_registration_intent' AND target_id=?`, lease.id)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_registration_prepare_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture registration prepare failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_registration_prepare_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.update_start_registration_intent') EXECUTE FUNCTION fixture_update_registration_prepare_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_registration_prepare_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_registration_prepare_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_registration_prepare_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.update_start_registration_intent' BEGIN SELECT RAISE(ABORT,'fixture registration prepare failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_registration_prepare_failure`)
	}
	if _, err := s.preparePlatformUpdateStartRegistration(
		context.Background(), lease); err == nil {
		t.Fatal("failed registration audit committed")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != 0 ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_registration_intent' AND target_id=?`, lease.id) != auditBefore {
		t.Fatal("failed registration preparation left partial rows")
	}
}

func TestPlatformUpdateStartRegistrationPrepareRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartRegistrationPrepareRollback(t, false)
}

func TestPlatformUpdateStartRegistrationPrepareRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartRegistrationPrepareRollback(t, true)
}

func testPlatformUpdateStartRegistrationCommitRollback(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation :=
		platformUpdateStartRegistrationCommitFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_registration_commit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture registration commit failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_registration_commit_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.update_start_registration_observed') EXECUTE FUNCTION fixture_update_registration_commit_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_registration_commit_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_registration_commit_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_registration_commit_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.update_start_registration_observed' BEGIN SELECT RAISE(ABORT,'fixture registration commit failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_registration_commit_failure`)
	}
	if _, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), lease, intent, endpoint, observation); err == nil {
		t.Fatal("failed registration commit audit succeeded")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate'`, lease.id) != "prepared" ||
		scalar[int64](t, s.st, `SELECT row_revision FROM app_platform_operations WHERE id=?`, lease.id) != lease.rowRevision {
		t.Fatal("failed registration commit was partial")
	}
}

func TestPlatformUpdateStartRegistrationCommitRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartRegistrationCommitRollback(t, false)
}

func TestPlatformUpdateStartRegistrationCommitRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartRegistrationCommitRollback(t, true)
}

func testPlatformUpdateStartReadinessRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.UpdateStartReadiness){
		"payload": func(r *journal.UpdateStartReadiness) { r.PayloadSHA256 = strings.Repeat("a", 64) },
		"running receipt": func(r *journal.UpdateStartReadiness) {
			r.RunningReceiptSHA256 = strings.Repeat("b", 64)
		},
		"native":   func(r *journal.UpdateStartReadiness) { r.NativeID = strings.Repeat("c", 64) },
		"protocol": func(r *journal.UpdateStartReadiness) { r.Protocol = "portal.backend/2" },
		"format":   func(r *journal.UpdateStartReadiness) { r.DataFormat = "2" },
		"future observed": func(r *journal.UpdateStartReadiness) {
			r.ObservedMS = strconv.FormatInt(time.Now().Add(10*time.Second).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, intent, endpoint, observation := platformUpdateStartReadinessCommitFixture(t, pg)
			mutate(&observation.readiness)
			if _, err := s.commitPlatformUpdateStartReadiness(context.Background(), lease, intent, endpoint, observation); err == nil {
				t.Fatal("changed update-start readiness committed")
			}
			if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "probe" ||
				scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != "{}" {
				t.Fatal("refused readiness changed durable state")
			}
		})
	}
}

func TestPlatformUpdateStartReadinessRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartReadinessRefusals(t, false)
}

func TestPlatformUpdateStartReadinessRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartReadinessRefusals(t, true)
}

func testPlatformUpdateStartReadinessRollback(t *testing.T, pg bool) {
	s, lease, intent, endpoint, observation := platformUpdateStartReadinessCommitFixture(t, pg)
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_start_ready_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture ready failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_ready_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.update_start_ready') EXECUTE FUNCTION fixture_update_start_ready_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_ready_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_start_ready_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_ready_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.update_start_ready' BEGIN SELECT RAISE(ABORT,'fixture ready failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_ready_failure`)
	}
	if _, err := s.commitPlatformUpdateStartReadiness(context.Background(), lease, intent, endpoint, observation); err == nil {
		t.Fatal("failed readiness audit committed")
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id) != "probe" ||
		scalar[string](t, s.st, `SELECT readiness_evidence FROM app_platform_activations WHERE installation_id=? AND generation=2`, lease.installationID) != "{}" {
		t.Fatal("failed readiness commit was partial")
	}
}

func TestPlatformUpdateStartReadinessRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartReadinessRollback(t, false)
}

func TestPlatformUpdateStartReadinessRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartReadinessRollback(t, true)
}

func testPlatformUpdateStartReservationRefusals(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*journal.UpdateStartReservation){
		"resource":          func(r *journal.UpdateStartReservation) { r.ResourceID = platformFixtureID(1370) },
		"effect":            func(r *journal.UpdateStartReservation) { r.EffectID = platformFixtureID(1371) },
		"namespace":         func(r *journal.UpdateStartReservation) { r.NamespaceID = platformFixtureID(1372) },
		"source release":    func(r *journal.UpdateStartReservation) { r.SourceReleaseSHA256 = strings.Repeat("e", 64) },
		"target release":    func(r *journal.UpdateStartReservation) { r.TargetReleaseSHA256 = strings.Repeat("f", 64) },
		"control fence":     func(r *journal.UpdateStartReservation) { r.ControlFence++ },
		"source writer":     func(r *journal.UpdateStartReservation) { r.SourceWriterFence++ },
		"target generation": func(r *journal.UpdateStartReservation) { r.TargetGeneration++ },
	} {
		t.Run(name, func(t *testing.T) {
			s, lease, _, _ := platformManagedUpdateStartFixture(t, pg)
			endpoint := platformUpdateStartEndpointFixture()
			intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
			if err != nil {
				t.Fatal(err)
			}
			observation := platformUpdateStartReservationFixture(intent, endpoint)
			reservation := observation.reservation
			mutate(&reservation)
			if _, err := s.commitPlatformUpdateStartReservation(context.Background(), lease, intent, endpoint, observation, reservation); err == nil {
				t.Fatal("changed reservation committed")
			}
			if scalar[int64](t, s.st, `SELECT observed_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != 0 ||
				scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "{}" {
				t.Fatal("refused reservation changed durable evidence")
			}
		})
	}
}

func TestPlatformUpdateStartReservationRefusalsSQLite(t *testing.T) {
	testPlatformUpdateStartReservationRefusals(t, false)
}

func TestPlatformUpdateStartReservationRefusalsPostgres(t *testing.T) {
	testPlatformUpdateStartReservationRefusals(t, true)
}

func testPlatformUpdateStartReservationRollback(t *testing.T, pg bool) {
	s, lease, _, _ := platformManagedUpdateStartFixture(t, pg)
	endpoint := platformUpdateStartEndpointFixture()
	intent, err := s.preparePlatformUpdateStart(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	observation := platformUpdateStartReservationFixture(intent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(observation, intent, endpoint)
	if !ok {
		t.Fatal("reservation observation refused")
	}
	if pg {
		mustExec(t, s.st, `CREATE FUNCTION fixture_update_start_reservation_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture reservation failure'; END $$`)
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_reservation_failure BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action='app_platform.update_start_reserved') EXECUTE FUNCTION fixture_update_start_reservation_failure()`)
		defer func() {
			mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_reservation_failure ON audit_log`)
			mustExec(t, s.st, `DROP FUNCTION IF EXISTS fixture_update_start_reservation_failure()`)
		}()
	} else {
		mustExec(t, s.st, `CREATE TRIGGER fixture_update_start_reservation_failure BEFORE INSERT ON audit_log WHEN NEW.action='app_platform.update_start_reserved' BEGIN SELECT RAISE(ABORT,'fixture reservation failure'); END`)
		defer mustExec(t, s.st, `DROP TRIGGER IF EXISTS fixture_update_start_reservation_failure`)
	}
	auditBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_reserved' AND target_id=?`, lease.id)
	if _, err := s.commitPlatformUpdateStartReservation(context.Background(), lease, intent, endpoint, observation, reservation); err == nil {
		t.Fatal("failed reservation audit committed")
	}
	if scalar[int64](t, s.st, `SELECT observed_fence FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != 0 ||
		scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "{}" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.update_start_reserved' AND target_id=?`, lease.id) != auditBefore {
		t.Fatal("failed reservation commit was partial")
	}
}

func TestPlatformUpdateStartReservationRollbackSQLite(t *testing.T) {
	testPlatformUpdateStartReservationRollback(t, false)
}

func TestPlatformUpdateStartReservationRollbackPostgres(t *testing.T) {
	testPlatformUpdateStartReservationRollback(t, true)
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
