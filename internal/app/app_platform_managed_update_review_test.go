package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformManagedUpdateReviewFixture struct {
	server       *Server
	actor        platformActorSession
	lease        platformOperationLease
	source       platformManagedUpdateSource
	target       *platformManagedInstallTarget
	sourcePlan   platformInstallProjection
	release      platformStagedRelease
	revision     int64
	policy       int64
	format       int64
	activeEpoch  string
	installation string
}

func platformManagedUpdateProjectionFixture(t *testing.T, pg bool) platformManagedUpdateReviewFixture {
	t.Helper()
	s, actor, lease := platformManagedAccessFixture(t, pg)
	var planRaw string
	var revision, policy, format int64
	var active, epoch, runtimeID string
	installation := lease.installationID
	if err := s.st.queryRow(`SELECT revision,policy_revision,
		controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`,
		installation).
		Scan(&revision, &policy, &epoch, &runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT release_sha256,data_format
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`, installation).
		Scan(&active, &format); err != nil {
		t.Fatal(err)
	}
	if err := s.st.queryRow(`SELECT p.plan_json FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install' AND o.managed=1
		AND o.stage='complete' ORDER BY o.target_generation DESC LIMIT 1`,
		installation).Scan(&planRaw); err != nil {
		t.Fatal(err)
	}
	var sourcePlan platformInstallProjection
	if err := json.Unmarshal([]byte(planRaw), &sourcePlan); err != nil ||
		!platformManagedFormat(sourcePlan.Format) ||
		sourcePlan.Runtime == nil ||
		sourcePlan.Runtime.RuntimeID != runtimeID {
		t.Fatal("managed source plan", err)
	}
	target := *sourcePlan.Runtime
	raw, archive := platformManagedReleaseFixture(t, target.Profile.Platform)
	var manifest appplatform.Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ID = sourcePlan.AppID
	manifest.Publisher = sourcePlan.PublisherID
	manifest.Version = "1.0.0-beta.4"
	manifest.Data.Schema = int(format)
	manifest.Data.ReadableSchemas = []int{int(format)}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	release, err := s.stagePlatformRelease(
		actor, raw, platformSignTestDescriptor(t, raw), archive)
	if err != nil {
		t.Fatal(err)
	}
	e := s.st.poolExec()
	source, err := loadPlatformManagedUpdateSource(
		e, installation, active, 1, &target)
	if err != nil {
		t.Fatal("load managed update source", err)
	}
	return platformManagedUpdateReviewFixture{
		server: s, actor: actor, lease: lease, source: source, target: &target,
		sourcePlan: sourcePlan, release: release, revision: revision,
		policy: policy, format: format, activeEpoch: epoch,
		installation: installation,
	}
}

func TestPlatformManagedUpdateProjectionBindsSourceAndRuntimeSQLite(t *testing.T) {
	testPlatformManagedUpdateProjectionBindsSourceAndRuntime(t, false)
}

func TestPlatformManagedUpdateProjectionBindsSourceAndRuntimePostgres(t *testing.T) {
	testPlatformManagedUpdateProjectionBindsSourceAndRuntime(t, true)
}

func testPlatformManagedUpdateProjectionBindsSourceAndRuntime(t *testing.T, pg bool) {
	fixture := platformManagedUpdateProjectionFixture(t, pg)
	projection, err := platformBuildManagedUpdateProjection(
		fixture.server.st.poolExec(), fixture.installation, fixture.source,
		fixture.revision, fixture.policy, fixture.format,
		platformActorFacts{authorityFingerprint: fixture.sourcePlan.ActorAuthority},
		platformActorControl{epoch: fixture.activeEpoch, nowMS: 1},
		fixture.target, fixture.release)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Format != platformUpdateReviewFormat ||
		projection.InstallationID != fixture.installation ||
		projection.SourceReleaseSHA256 != fixture.source.release ||
		projection.Target.ReleaseSHA256 != fixture.release.sha256 ||
		projection.SourceGeneration != 1 ||
		projection.TargetGeneration != 2 ||
		projection.TargetPolicyRevision != fixture.policy+1 ||
		projection.Target.Runtime == nil ||
		*projection.Target.Runtime != *fixture.target ||
		projection.Target.Format != fixture.source.format {
		t.Fatal("managed update projection lost exact source/runtime binding")
	}
}

func TestPlatformManagedUpdateProjectionRefusesChangedSourceSQLite(t *testing.T) {
	testPlatformManagedUpdateProjectionRefusesChangedSource(t, false)
}

func TestPlatformManagedUpdateProjectionRefusesChangedSourcePostgres(t *testing.T) {
	testPlatformManagedUpdateProjectionRefusesChangedSource(t, true)
}

func testPlatformManagedUpdateProjectionRefusesChangedSource(t *testing.T, pg bool) {
	fixture := platformManagedUpdateProjectionFixture(t, pg)
	for name, mutate := range map[string]func(*platformManagedUpdateReviewFixture){
		"runtime": func(f *platformManagedUpdateReviewFixture) {
			changed := *f.target
			changed.RuntimeID = platformFixtureID(907)
			f.target = &changed
		},
		"format": func(f *platformManagedUpdateReviewFixture) {
			f.format = 2
		},
		"release": func(f *platformManagedUpdateReviewFixture) {
			f.source.release = f.release.sha256
		},
		"backend": func(f *platformManagedUpdateReviewFixture) {
			descriptor := scalar[[]byte](t, f.server.st,
				`SELECT descriptor FROM app_platform_releases WHERE sha256=?`,
				f.release.sha256)
			var manifest appplatform.Manifest
			if err := json.Unmarshal(descriptor, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Backend = nil
			changed, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.server.st, `UPDATE app_platform_releases
				SET descriptor=? WHERE sha256=?`,
				changed, f.release.sha256)
		},
	} {
		t.Run(name, func(t *testing.T) {
			local := fixture
			mutate(&local)
			if _, err := platformBuildManagedUpdateProjection(
				local.server.st.poolExec(), local.installation, local.source,
				local.revision, local.policy, local.format,
				platformActorFacts{authorityFingerprint: local.sourcePlan.ActorAuthority},
				platformActorControl{epoch: local.activeEpoch, nowMS: 1},
				local.target, local.release); err == nil {
				t.Fatal("changed managed update source admitted")
			}
		})
	}
}

func TestPlatformManagedUpdateReviewRejectsMissingOpaqueRuntime(t *testing.T) {
	s, session, _ := platformManagedAccessFixture(t, false)
	if _, err := s.createPlatformManagedUpdateReview(
		context.Background(), session, platformFixtureID(901),
		strings.Repeat("a", 64), 1, platformRuntimeEndpoint{}); err == nil {
		t.Fatal("missing managed runtime minted update review")
	}
	if _, err := s.consumePlatformManagedUpdateReview(
		context.Background(), session, platformFixtureID(902),
		strings.Repeat("a", 64), platformFixtureID(903),
		platformRuntimeEndpoint{}); err == nil {
		t.Fatal("missing managed runtime consumed update review")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations
		WHERE action='update'`) != 0 {
		t.Fatal("missing runtime created managed update operation")
	}
}

func TestManagedRestoreRootResolvesUpdateRuntime(t *testing.T) {
	s, lease, _ := platformUpdateRecoveryRestoreFixture(t, false)
	target, err := platformManagedTargetForOperation(
		s.st.poolExec(), lease.id)
	if err != nil || target == nil ||
		target.RuntimeID == "" ||
		target.Profile.Platform != "linux/amd64" {
		t.Fatal("restore root runtime target", target, err)
	}
}

func platformManagedUpdateOperationFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	fixture := platformManagedUpdateProjectionFixture(t, pg)
	projection, err := platformBuildManagedUpdateProjection(
		fixture.server.st.poolExec(), fixture.installation, fixture.source,
		fixture.revision, fixture.policy, fixture.format,
		platformActorFacts{authorityFingerprint: fixture.sourcePlan.ActorAuthority},
		platformActorControl{epoch: fixture.activeEpoch, nowMS: 1},
		fixture.target, fixture.release)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var scope, username, created, authority, epoch string
	var accountGeneration, sourceRevision, sourcePolicy, trustRevision,
		now int64
	if err := fixture.server.st.queryRow(`SELECT actor_scope,username,
		account_created,account_generation,authority_fingerprint,
		installation_revision,policy_revision,trust_revision,controller_epoch,
		created_ms FROM app_platform_review_plans
		WHERE id=(SELECT review_plan_id FROM app_platform_operations
			WHERE installation_id=? AND action='install' AND managed=1
			ORDER BY target_generation DESC LIMIT 1)`,
		fixture.installation).
		Scan(&scope, &username, &created, &accountGeneration, &authority,
			&sourceRevision, &sourcePolicy, &trustRevision, &epoch,
			&now); err != nil {
		t.Fatal(err)
	}
	planID, operationID := platformFixtureID(6110), platformFixtureID(6111)
	digest := platformDigest(raw)
	idempotency, request := platformDigest([]byte("managed-update-key")),
		platformDigest([]byte("managed-update-request"))
	mustExec(t, fixture.server.st, `INSERT INTO app_platform_review_plans(
		id,installation_id,app_id,publisher_id,action,source_release_sha256,
		target_release_sha256,installation_revision,policy_revision,
		trust_revision,controller_epoch,actor_scope,username,account_created,
		account_generation,authority_fingerprint,plan_sha256,plan_json,
		expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,'update',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		planID, fixture.installation, projection.AppID,
		projection.PublisherID, projection.SourceReleaseSHA256,
		projection.Target.ReleaseSHA256, projection.InstallationRevision,
		projection.PolicyRevision, projection.TrustRevision,
		projection.ControllerEpoch, scope, username, created,
		accountGeneration, authority, digest, string(raw),
		now+900000, now, now, now)
	mustExec(t, fixture.server.st, `INSERT INTO app_platform_operations(
		id,installation_id,review_plan_id,actor_scope,idempotency_sha256,
		request_sha256,action,managed,source_generation,target_generation,
		stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,
		controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,'update',1,?,?,'queued',1,1,NULL,NULL,0,?,
		NULL,NULL,?,?)`,
		operationID, fixture.installation, planID, scope, idempotency,
		request, projection.SourceGeneration, projection.TargetGeneration,
		projection.ControllerEpoch, now, now)
	return fixture.server, platformOperationLease{
		id: operationID, installationID: fixture.installation,
		controllerEpoch: projection.ControllerEpoch,
	}
}

func TestPlatformManagedUpdateQueueClaimsExactAdmissionSQLite(t *testing.T) {
	testPlatformManagedUpdateQueueClaimsExactAdmission(t, false)
}

func TestPlatformManagedUpdateQueueClaimsExactAdmissionPostgres(t *testing.T) {
	testPlatformManagedUpdateQueueClaimsExactAdmission(t, true)
}

func testPlatformManagedUpdateQueueClaimsExactAdmission(t *testing.T, pg bool) {
	s, lease := platformManagedUpdateOperationFixture(t, pg)
	if target, err := platformManagedTargetForOperation(
		s.st.poolExec(), lease.id); err != nil || target == nil {
		t.Fatal("managed update root target", target, err)
	}
	beforeGeneration := scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	selected, err := s.claimNextPlatformManagedUpdate(
		context.Background(), platformFixtureID(6113))
	if err != nil || selected.lease.id != lease.id ||
		selected.lease.snapshot.Action != appplatform.ActionUpdate ||
		!selected.lease.snapshot.Managed ||
		selected.lease.snapshot.Stage != appplatform.StageQueued {
		t.Fatal("managed update claim", selected, err)
	}
	if scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`,
		lease.installationID) != beforeGeneration ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations
			WHERE installation_id=? AND action='update'
			AND managed=1 AND stage='queued'
			AND lease_owner=?`, lease.installationID,
			platformFixtureID(6113)) != 1 {
		t.Fatal("managed update claim changed the live source")
	}
}

func TestPlatformManagedUpdateQueueRefusesChangedAdmissionSQLite(t *testing.T) {
	testPlatformManagedUpdateQueueRefusesChangedAdmission(t, false)
}

func TestPlatformManagedUpdateQueueRefusesChangedAdmissionPostgres(t *testing.T) {
	testPlatformManagedUpdateQueueRefusesChangedAdmission(t, true)
}

func testPlatformManagedUpdateQueueRefusesChangedAdmission(t *testing.T, pg bool) {
	for name, mutate := range map[string]func(*testing.T, *Server, platformOperationLease){
		"runtime": func(t *testing.T, s *Server, lease platformOperationLease) {
			var raw string
			if err := s.st.queryRow(`SELECT plan_json
				FROM app_platform_review_plans WHERE id=(
				SELECT review_plan_id FROM app_platform_operations
				WHERE id=?)`, lease.id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plan platformUpdateProjection
			if err := json.Unmarshal([]byte(raw), &plan); err != nil {
				t.Fatal(err)
			}
			plan.Target.Runtime.RuntimeID = platformFixtureID(999)
			changed, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			mustExec(t, s.st, `UPDATE app_platform_review_plans
				SET plan_json=?,plan_sha256=? WHERE id=(
				SELECT review_plan_id FROM app_platform_operations
				WHERE id=?)`, string(changed), platformDigest(changed),
				lease.id)
		},
		"policy": func(t *testing.T, s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_installations
				SET policy_revision=policy_revision+1 WHERE id=?`,
				lease.installationID)
		},
		"runtime binding": func(t *testing.T, s *Server, lease platformOperationLease) {
			mustExec(t, s.st, `UPDATE app_platform_runtime_bindings
				SET enabled=0`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, lease := platformManagedUpdateOperationFixture(t, pg)
			mutate(t, s, lease)
			if selected, err := s.claimNextPlatformManagedUpdate(
				context.Background(), platformFixtureID(6114)); err == nil &&
				selected.lease.id != "" {
				t.Fatal("changed managed update admission was claimed")
			}
		})
	}
}

func TestPlatformManagedUpdateSchedulerClaimsBeforeRemoval(t *testing.T) {
	s, operation := platformManagedUpdateOperationFixture(t, false)
	worker := platformFixtureID(6121)
	scheduler, err := newPlatformWritableInstallScheduler(
		s, platformManagedRemovalFixtureEndpoint(), worker, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.active.id != operation.id ||
		scheduler.active.snapshot.Stage != appplatform.StageValidate ||
		scalar[string](t, s.st, `SELECT stage||':'||lease_owner
			FROM app_platform_operations WHERE id=?`,
			operation.id) != "validate:"+worker {
		t.Fatal("managed update scheduler claim")
	}
}

func platformManagedUpdateQuiesceFixture(t *testing.T, pg bool) (*Server, platformOperationLease, platformManagedUpdateQuiesceIntent, platformRuntimeEndpoint) {
	t.Helper()
	s, operation := platformManagedUpdateOperationFixture(t, pg)
	selected, err := s.claimNextPlatformManagedUpdate(
		context.Background(), platformFixtureID(6120))
	if err != nil || selected.lease.id != operation.id {
		t.Fatal("managed update claim", selected, err)
	}
	lease := selected.lease
	for range 4 {
		lease, err = s.advancePlatformManagedUpdateStage(
			context.Background(), lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	if lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatal("managed update did not reach quiesce", lease.snapshot)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	intent, err := s.preparePlatformManagedUpdateQuiesce(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	return s, lease, intent, endpoint
}

func TestPlatformManagedUpdateQuiescePersistsExactStopIntentSQLite(t *testing.T) {
	testPlatformManagedUpdateQuiescePersistsExactStopIntent(t, false)
}

func TestPlatformManagedUpdateQuiescePersistsExactStopIntentPostgres(t *testing.T) {
	testPlatformManagedUpdateQuiescePersistsExactStopIntent(t, true)
}

func testPlatformManagedUpdateQuiescePersistsExactStopIntent(t *testing.T, pg bool) {
	s, lease, intent, _ := platformManagedUpdateQuiesceFixture(t, pg)
	if intent.command.Stage != "stop" ||
		intent.command.OperationID != lease.id ||
		intent.command.Payload.SourceOperationID == "" ||
		intent.command.Payload.SourceEffectID == "" ||
		intent.command.Payload.SourceReceiptSHA256 == "" ||
		intent.source.nativeID == "" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='quiesce' AND kind='stop'`,
			lease.id) != "prepared" ||
		scalar[string](t, s.st, `SELECT stage
			FROM app_platform_operations WHERE id=?`,
			lease.id) != "quiesce" {
		t.Fatal("managed update quiesce intent", intent)
	}
}

func TestPlatformManagedUpdateQuiesceCommitsSnapshotStageSQLite(t *testing.T) {
	testPlatformManagedUpdateQuiesceCommitsSnapshotStage(t, false)
}

func TestPlatformManagedUpdateQuiesceCommitsSnapshotStagePostgres(t *testing.T) {
	testPlatformManagedUpdateQuiesceCommitsSnapshotStage(t, true)
}

func testPlatformManagedUpdateQuiesceCommitsSnapshotStage(t *testing.T, pg bool) {
	s, lease, intent, _ := platformManagedUpdateQuiesceFixture(t, pg)
	receipt := platformManagedUpdateQuiesceReceipt(intent)
	next, err := s.commitPlatformManagedUpdateQuiesce(
		context.Background(), lease, intent, receipt)
	if err != nil || next.snapshot.Stage != appplatform.StageSnapshot ||
		scalar[string](t, s.st, `SELECT stage
			FROM app_platform_operations WHERE id=?`, lease.id) != "snapshot" ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='quiesce'`, lease.id) != "applied" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log
			WHERE action='app_platform.managed_update_quiesced'
			AND target_id=?`, lease.id) != 1 {
		t.Fatal("managed update quiesce commit", next, err)
	}
}

func platformManagedUpdateQuiesceReceipt(intent platformManagedUpdateQuiesceIntent) journal.ManagedRemovalReceipt {
	now := time.Now().UTC().UnixMilli()
	state := "stopped"
	if intent.command.Stage == "remove_code" {
		state = "absent"
	}
	return journal.ManagedRemovalReceipt{
		Format:            "portal.runner.managed-removal-receipt/1-dev.1",
		DeploymentID:      intent.command.DeploymentID,
		ControllerEpoch:   intent.command.ControllerEpoch,
		InstallationID:    intent.command.InstallationID,
		OperationID:       intent.command.OperationID,
		SourceOperationID: intent.command.Payload.SourceOperationID,
		SourceEffectID:    intent.command.Payload.SourceEffectID,
		TargetGeneration:  intent.command.TargetGeneration,
		Stage:             intent.command.Stage, EffectID: intent.command.EffectID,
		PayloadSHA256:       intent.command.PayloadSHA256,
		SourceReceiptSHA256: intent.command.Payload.SourceReceiptSHA256,
		ResourceID:          intent.command.Payload.ResourceID,
		DataNamespaceID:     intent.command.Payload.DataNamespaceID,
		ReleaseSHA256:       intent.command.Payload.ReleaseSHA256,
		NativeID:            intent.source.nativeID, State: state,
		ObservedMS: strconv.FormatInt(now, 10),
		SettledMS:  strconv.FormatInt(now, 10),
	}
}

func TestPlatformManagedUpdateAdvancesCheckpointToStartSQLite(t *testing.T) {
	testPlatformManagedUpdateAdvancesCheckpointToStart(t, false)
}

func TestPlatformManagedUpdateAdvancesCheckpointToStartPostgres(t *testing.T) {
	testPlatformManagedUpdateAdvancesCheckpointToStart(t, true)
}

func testPlatformManagedUpdateAdvancesCheckpointToStart(t *testing.T, pg bool) {
	s, lease, quiesce, _ := platformManagedUpdateQuiesceFixture(t, pg)
	snapshot, err := s.commitPlatformManagedUpdateQuiesce(
		context.Background(), lease, quiesce,
		platformManagedUpdateQuiesceReceipt(quiesce))
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpointObservation := platformCheckpointObservationFixture(
		checkpoint, checkpointEndpoint)
	migrate, err := s.commitPlatformCheckpoint(
		context.Background(), snapshot, checkpoint,
		checkpointEndpoint, checkpointObservation)
	if err != nil || migrate.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("managed checkpoint commit", migrate, err)
	}
	var rawPlan string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`, lease.id).Scan(&rawPlan); err != nil {
		t.Fatal(err)
	}
	var finalized platformManagedUpdatePlan
	if err := json.Unmarshal([]byte(rawPlan), &finalized); err != nil ||
		finalized.Format != platformManagedUpdateFormat ||
		finalized.TargetRuntime == nil ||
		finalized.CheckpointOperationID != lease.id {
		t.Fatal("managed update plan was not finalized", err)
	}
	if target, err := platformManagedTargetForOperation(
		s.st.poolExec(), lease.id); err != nil ||
		target == nil || *target != *finalized.TargetRuntime {
		t.Fatal("finalized managed update root target", target, err)
	}
	trust, err := loadPlatformPublisherTrust(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadPlatformAdmittedManagedUpdate(
		s.st.poolExec(), migrate, trust, time.Now().UnixMilli()); err != nil {
		t.Fatal("finalized managed update admission", err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET lease_until_ms=1 WHERE id=?`, migrate.id)
	reclaimed, err := s.claimNextPlatformManagedUpdate(
		context.Background(), platformFixtureID(6122))
	if err != nil || reclaimed.lease.id != migrate.id ||
		reclaimed.lease.snapshot.Stage != appplatform.StageMigrate {
		t.Fatal("finalized managed update was not reclaimable", reclaimed, err)
	}
	remove, err := s.preparePlatformManagedUpdateMigrate(
		context.Background(), reclaimed.lease)
	if err != nil {
		t.Fatal(err)
	}
	start, err := s.commitPlatformManagedUpdateQuiesce(
		context.Background(), reclaimed.lease, remove,
		platformManagedUpdateQuiesceReceipt(remove))
	if err != nil || start.snapshot.Stage != appplatform.StageStart {
		t.Fatal("managed migrate transition", start, err)
	}
	intent, err := s.preparePlatformUpdateStart(
		context.Background(), start)
	if err != nil || intent.command.Kind != "update-start" ||
		intent.command.Payload.SourceCheckpointOperationID != lease.id {
		t.Fatal("managed update start after checkpoint", intent, err)
	}
	startEndpoint := platformUpdateStartEndpointFixture()
	reservationObservation := platformUpdateStartReservationFixture(
		intent, startEndpoint)
	reservation, ok := validPlatformUpdateStartObservation(
		reservationObservation, intent, startEndpoint)
	if !ok {
		t.Fatal("managed update reservation fixture")
	}
	reserved, err := s.commitPlatformUpdateStartReservation(
		context.Background(), start, intent, startEndpoint,
		reservationObservation, reservation)
	if err != nil {
		t.Fatal(err)
	}
	runningProof := platformUpdateStartRunningFixture(
		reserved.intent, reservation, startEndpoint)
	probeLease, err := s.commitPlatformUpdateStartReceipt(
		context.Background(), start, reserved.intent,
		startEndpoint, runningProof)
	if err != nil || probeLease.snapshot.Stage != appplatform.StageProbe {
		t.Fatal("managed update running", probeLease, err)
	}
	probeIntent, err := s.preparePlatformUpdateProbe(
		context.Background(), probeLease)
	if err != nil {
		t.Fatal(err)
	}
	readinessProof := platformUpdateStartReservationFixture(
		probeIntent, startEndpoint)
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
	activateLease, err := s.commitPlatformUpdateStartReadiness(
		context.Background(), probeLease, probeIntent,
		startEndpoint, readinessProof)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.preparePlatformUpdateStartRegistration(
		context.Background(), activateLease)
	if err != nil {
		t.Fatal(err)
	}
	registrationProof := platformUpdateStartRegistrationObservationForIntent(
		t, registration, startEndpoint)
	registered, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), activateLease, registration,
		startEndpoint, registrationProof)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(
		t, s, registered, startEndpoint)
	defer applied.registration.Close()
	result, err := s.commitPlatformUpdateStartActivation(
		context.Background(), registered, applied, boot)
	if err != nil || result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT phase||':'||generation||':'||policy_revision
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "ready:2:2" ||
		scalar[string](t, s.st, `SELECT release_sha256
			FROM app_platform_activations
			WHERE installation_id=? AND state='active'`,
			lease.installationID) != finalized.TargetReleaseSHA256 {
		t.Fatal("managed update activation", result, err)
	}
}
