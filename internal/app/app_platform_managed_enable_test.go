package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func platformManagedEnableSnapshotFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, actor, lease := platformManagedRemovalStopFixtureAction(
		t, pg, appplatform.ActionDisable)
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(
		platformManagedRemovalObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("disable stop receipt")
	}
	if _, err := s.settlePlatformManagedRemoval(
		context.Background(), lease, intent, receipt); err != nil {
		t.Fatal(err)
	}
	publisherID := scalar[string](t, s.st, `SELECT publisher_id
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	appID := scalar[string](t, s.st, `SELECT app_id
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	trustRevision := scalar[int64](t, s.st, `SELECT revision
		FROM app_platform_publishers WHERE id=?`, publisherID)
	revoked, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "policy", publisherID: publisherID,
			expectedRevision: trustRevision, enabled: false,
			namespaces: []appplatform.PublisherNamespace{{
				Kind: "exact", Value: appID,
			}},
		})
	if err != nil {
		t.Fatal("disable publisher after managed stop", err)
	}
	if _, err := s.changePlatformPublisherTrust(
		actor, platformPublisherChange{
			kind: "policy", publisherID: publisherID,
			expectedRevision: revoked.revision, enabled: true,
			namespaces: []appplatform.PublisherNamespace{{
				Kind: "prefix", Value: "notes-",
			}},
		}); err != nil {
		t.Fatal("restore publisher before managed enable", err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	generation := scalar[int](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	expected := platformOperationLease{
		installationID: lease.installationID, controllerEpoch: epoch,
		snapshot: appplatform.OperationSnapshot{
			Action: appplatform.ActionEnable, Managed: true,
			Generation: uint64(generation), Revision: 1,
			Stage: appplatform.StageQueued,
		},
	}
	source, err := loadPlatformCheckpointSourceForEnableReview(
		s.st.poolExec(), expected, runtime, now)
	if err != nil {
		t.Fatal(err)
	}
	plan, revision, err := platformBuildManagedEnablePlan(
		s.st.poolExec(), lease.installationID, source.runtimeTarget,
		runtime, source, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var actorScope, username, created, authority string
	var accountGeneration int64
	if err := s.st.queryRow(`SELECT p.actor_scope,p.username,p.account_created,
		p.account_generation,p.authority_fingerprint
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install'
		ORDER BY o.created_ms DESC LIMIT 1`, lease.installationID).
		Scan(&actorScope, &username, &created, &accountGeneration,
			&authority); err != nil {
		t.Fatal("enable actor source", err)
	}
	planID := platformFixtureID(1310)
	if _, err := s.st.exec(`INSERT INTO app_platform_review_plans(
		id,installation_id,app_id,publisher_id,action,source_release_sha256,
		target_release_sha256,installation_revision,policy_revision,
		trust_revision,controller_epoch,actor_scope,username,account_created,
		account_generation,authority_fingerprint,plan_sha256,plan_json,
		expires_ms,consumed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?, 'enable',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		planID, plan.InstallationID, plan.AppID, plan.PublisherID,
		plan.SourceReleaseSHA256, plan.TargetReleaseSHA256, revision,
		plan.SourcePolicyRevision, plan.TrustRevision, plan.ControllerEpoch,
		actorScope, username, created, accountGeneration, authority,
		platformDigest(raw), string(raw), now+900000, now, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.exec(`UPDATE app_platform_installations
		SET desired_state='enabled',phase='pending',revision=revision+1,
		control_fence=control_fence+1,updated_ms=? WHERE id=?`,
		now, lease.installationID); err != nil {
		t.Fatal(err)
	}
	operationID := platformFixtureID(1311)
	if _, err := s.st.exec(`INSERT INTO app_platform_operations(
		id,installation_id,review_plan_id,actor_scope,idempotency_sha256,
		request_sha256,action,managed,source_generation,target_generation,
		stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,
		controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?, 'enable',1,?,?,'snapshot',3,1,NULL,NULL,0,?,
		NULL,NULL,?,?)`,
		operationID, lease.installationID, planID, actorScope,
		strings.Repeat("1", 64), strings.Repeat("2", 64),
		plan.SourceGeneration, plan.TargetGeneration, epoch, now, now); err != nil {
		t.Fatal("enable operation fixture", err)
	}
	claimed, err := s.st.claimPlatformOperation(
		operationID, platformFixtureID(1312))
	if err != nil || claimed.snapshot.Action != appplatform.ActionEnable ||
		!claimed.snapshot.Managed ||
		claimed.snapshot.Stage != appplatform.StageSnapshot {
		t.Fatal("managed enable snapshot lease", claimed, err)
	}
	return s, claimed
}

func TestPlatformManagedEnableBuildsSameTupleStartPlan(t *testing.T) {
	s, actor, lease := platformManagedRemovalStopFixtureAction(
		t, false, appplatform.ActionDisable)
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(
		platformManagedRemovalObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("disable stop receipt")
	}
	if _, err := s.settlePlatformManagedRemoval(
		context.Background(), lease, intent, receipt); err != nil {
		t.Fatal(err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	generation := scalar[int](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, lease.installationID)
	expected := platformOperationLease{
		installationID: lease.installationID, controllerEpoch: epoch,
		snapshot: appplatform.OperationSnapshot{
			Action: appplatform.ActionEnable, Managed: true,
			Generation: uint64(generation), Revision: 1,
			Stage: appplatform.StageQueued,
		},
	}
	source, err := loadPlatformCheckpointSourceForEnableReview(
		s.st.poolExec(), expected, runtime, now)
	if err != nil {
		t.Fatal(err)
	}
	if source.sourceGeneration != 1 ||
		source.sourceGeneration+1 != int64(generation) ||
		source.releaseSHA256 == "" || source.namespaceID == "" ||
		source.runtimeTarget == nil {
		t.Fatalf("unexpected enable source: %+v", source)
	}
	plan, revision, err := platformBuildManagedEnablePlan(
		s.st.poolExec(), lease.installationID, source.runtimeTarget,
		runtime, source, now)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Format != platformManagedEnableFormat ||
		plan.Action != appplatform.ActionEnable ||
		plan.SourceGeneration != 1 ||
		plan.TargetGeneration != int64(generation) ||
		plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 ||
		plan.TargetPolicyRevision != plan.SourcePolicyRevision ||
		plan.SourceActivationGeneration != plan.SourceGeneration ||
		revision < 1 ||
		!platformValidSHA(plan.GrantsSHA256) ||
		!platformValidSHA(plan.AudienceSHA256) {
		t.Fatalf("invalid managed enable plan: %+v revision=%d", plan, revision)
	}
	review, err := s.createPlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, lease.installationID,
		source.runtimeTarget, runtime)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := s.consumePlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1313), source.runtimeTarget, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if commit.Generation != int64(generation) ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "enabled:pending" ||
		scalar[string](t, s.st, `SELECT action||':'||stage
			FROM app_platform_operations WHERE id=?`,
			commit.OperationID) != "enable:queued" {
		t.Fatal("managed enable admission", commit)
	}
	selected, err := s.claimNextPlatformManagedEnable(
		context.Background(), platformFixtureID(1314))
	if err != nil || selected.lease.id != commit.OperationID ||
		selected.lease.snapshot.Action != appplatform.ActionEnable ||
		!selected.lease.snapshot.Managed {
		t.Fatal("managed enable claim", selected, err)
	}
}

func TestPlatformManagedEnableOperationWaitsForSnapshotReadiness(t *testing.T) {
	op, err := appplatform.NewOperation(
		appplatform.ActionEnable, true, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []appplatform.Stage{
		appplatform.StageValidate,
		appplatform.StageSnapshot,
		appplatform.StageStart,
		appplatform.StageProbe,
		appplatform.StageActivate,
		appplatform.StageComplete,
	} {
		op, err = op.Advance(op.Snapshot().Revision, stage)
		if err != nil {
			t.Fatal(stage, err)
		}
	}
}

func TestPlatformManagedEnableCheckpointToStartUsesEnableCommand(t *testing.T) {
	s, lease := platformManagedEnableSnapshotFixture(t, false)
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformCheckpointEndpointFixture()
	observation := platformCheckpointObservationFixture(
		checkpoint, endpoint)
	next, err := s.commitPlatformCheckpoint(
		context.Background(), lease, checkpoint, endpoint, observation)
	if err != nil {
		t.Fatal(err)
	}
	if next.snapshot.Stage != appplatform.StageStart {
		t.Fatalf("enable checkpoint stage = %s", next.snapshot.Stage)
	}
	start, err := s.preparePlatformUpdateStart(
		context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	if start.command.Kind != "enable-start" ||
		start.command.Payload.SourceReleaseSHA256 !=
			start.command.Payload.TargetReleaseSHA256 ||
		start.command.Payload.SourceGeneration !=
			strconv.FormatInt(start.plan.SourceGeneration, 10) {
		t.Fatalf("invalid enable start command: %+v", start.command)
	}
}

func testPlatformManagedEnableSyntheticStart(t *testing.T, pg bool) (*Server, platformActorSession, platformOperationLease) {
	s, snapshot := platformManagedEnableSnapshotFixture(t, pg)
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpointObservation := platformCheckpointObservationFixture(
		checkpoint, checkpointEndpoint)
	startLease, err := s.commitPlatformCheckpoint(
		context.Background(), snapshot, checkpoint, checkpointEndpoint,
		checkpointObservation)
	if err != nil {
		t.Fatal(err)
	}
	capsBefore := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_capability_grants WHERE installation_id=?`,
		startLease.installationID)
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			startLease.installationID) != "enabled:ready" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations
			WHERE installation_id=? AND state='active' AND generation=2`,
			startLease.installationID) != 1 ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_capability_grants WHERE installation_id=?`,
			startLease.installationID) != capsBefore {
		t.Fatal("managed enable did not activate the retained tuple")
	}
	var descriptor []byte
	var release, epoch string
	var policy int64
	if err := s.st.queryRow(`SELECT a.release_sha256,a.policy_revision,
		i.controller_epoch,r.descriptor
		FROM app_platform_activations a
		JOIN app_platform_installations i ON i.id=a.installation_id
		JOIN app_platform_releases r ON r.sha256=a.release_sha256
		WHERE a.installation_id=? AND a.state='active'`,
		startLease.installationID).
		Scan(&release, &policy, &epoch, &descriptor); err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPlatformLaunchCapabilitySnapshot(
		s.st.poolExec(), startLease.installationID, release, epoch,
		policy, parsed.Manifest); err != nil {
		t.Fatal("managed enable capability snapshot", err)
	}
	actor := platformManagedFixtureSession(t, s)
	access, err := s.preparePlatformWritableBackendAccess(
		context.Background(), actor, startLease.installationID)
	if err != nil {
		t.Fatal("managed enable backend history", err)
	}
	defer access.Close()
	if !access.intent.applied || access.intent.update == nil ||
		access.intent.generation != 2 {
		t.Fatal("managed enable history did not retain the active tuple")
	}
	return s, actor, startLease
}

func platformRunSyntheticManagedEnable(
	t *testing.T, s *Server, startLease platformOperationLease,
) platformActivationResult {
	t.Helper()
	endpoint := platformUpdateStartEndpointFixture()
	startIntent, err := s.preparePlatformUpdateStart(
		context.Background(), startLease)
	if err != nil {
		t.Fatal(err)
	}
	reservationObservation := platformUpdateStartReservationFixture(
		startIntent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(
		reservationObservation, startIntent, endpoint)
	if !ok {
		t.Fatal("enable reservation observation")
	}
	admission, err := s.commitPlatformUpdateStartReservation(
		context.Background(), startLease, startIntent, endpoint,
		reservationObservation, reservation)
	if err != nil {
		t.Fatal(err)
	}
	probeLease, err := s.commitPlatformUpdateStartReceipt(
		context.Background(), startLease, admission.intent, endpoint,
		platformUpdateStartRunningFixture(
			admission.intent, reservation, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	probeIntent, err := s.preparePlatformUpdateProbe(
		context.Background(), probeLease)
	if err != nil {
		t.Fatal(err)
	}
	readiness := platformUpdateStartReservationFixture(
		probeIntent, endpoint)
	readiness.readiness = journal.UpdateStartReadiness{
		Format:               "portal.runner.update-start-readiness/1-dev.1",
		PayloadSHA256:        probeIntent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(mustMarshalPlatformUpdateStartReceipt(*probeIntent.running)),
		NativeID:             probeIntent.running.NativeID,
		Protocol:             "portal.backend/1",
		DataFormat:           probeIntent.command.Payload.DataFormat,
		ObservedMS:           probeIntent.running.NativeObservedMS,
	}
	activateLease, err := s.commitPlatformUpdateStartReadiness(
		context.Background(), probeLease, probeIntent, endpoint, readiness)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.preparePlatformUpdateStartRegistration(
		context.Background(), activateLease)
	if err != nil {
		t.Fatal(err)
	}
	registrationObservation :=
		platformUpdateStartRegistrationObservationForIntent(
			t, registration, endpoint)
	registered, err := s.commitPlatformUpdateStartRegistration(
		context.Background(), activateLease, registration, endpoint,
		registrationObservation)
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformUpdateStartBootFixture(
		t, s, registered, endpoint)
	result, err := s.commitPlatformUpdateStartActivation(
		context.Background(), registered, applied, boot)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPlatformManagedEnableSyntheticStartActivatesCurrentTupleSQLite(t *testing.T) {
	testPlatformManagedEnableSyntheticStart(t, false)
}

func TestPlatformManagedEnableSyntheticStartActivatesCurrentTuplePostgres(t *testing.T) {
	testPlatformManagedEnableSyntheticStart(t, true)
}

func platformCompleteSyntheticDisable(
	t *testing.T,
	s *Server,
	actor platformActorSession,
	enabled platformOperationLease,
) platformOperationLease {
	t.Helper()
	review, err := s.createPlatformRemovalReview(
		actor, enabled.installationID, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("disable review", err)
	}
	commit, err := s.consumePlatformRemovalReview(
		actor, review.ID, review.SHA256, platformFixtureID(1320))
	if err != nil {
		t.Fatal("disable admission", err)
	}
	selected, err := s.claimNextPlatformManagedUninstall(
		context.Background(), platformFixtureID(1321))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("disable claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage == appplatform.StageQueued ||
		current.snapshot.Stage == appplatform.StageValidate {
		current, err = s.st.advancePlatformRemovalControl(current)
		if err != nil {
			t.Fatal("disable control", current.snapshot.Stage, err)
		}
	}
	current, err = s.advancePlatformManagedRemovalStage(
		context.Background(), current, appplatform.StageStop)
	if err != nil {
		t.Fatal("disable stop intent", err)
	}
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), current)
	if err != nil {
		t.Fatal("disable intent", err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	receipt, ok := validPlatformManagedRemovalObservation(
		platformManagedRemovalObservationFixture(intent, endpoint),
		intent, endpoint)
	if !ok {
		t.Fatal("disable stop receipt")
	}
	completed, err := s.settlePlatformManagedRemoval(
		context.Background(), current, intent, receipt)
	if err != nil || completed.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("disable completion", completed.snapshot, err)
	}
	return completed
}

func testPlatformManagedEnableSecondCycle(t *testing.T, pg bool) {
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, firstEnable)
	var target platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable'
		AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`,
		firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &target) != nil ||
		target.TargetRuntime == nil {
		t.Fatal("first enable target", err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil || now < 0 {
		t.Fatal("second enable runtime", err)
	}
	review, err := s.createPlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, firstEnable.installationID,
		target.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("second enable review", err)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil ||
		plan.SourceGeneration != target.TargetGeneration ||
		plan.TargetGeneration != target.TargetGeneration+1 ||
		plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 {
		t.Fatal("second enable plan", plan)
	}
	commit, err := s.consumePlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1322), target.TargetRuntime, runtime)
	if err != nil || commit.Stage != appplatform.StageQueued {
		t.Fatal("second enable admission", commit, err)
	}
	selected, err := s.claimNextPlatformManagedEnable(
		context.Background(), platformFixtureID(1323))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("second enable claim", selected, err)
	}
	snapshotLease := selected.lease
	if platformTestAfterEnableClaim != nil {
		platformTestAfterEnableClaim(t, s, selected.lease)
	}
	for snapshotLease.snapshot.Stage == appplatform.StageQueued ||
		snapshotLease.snapshot.Stage == appplatform.StageValidate {
		snapshotLease, err = s.advancePlatformManagedEnableStage(
			context.Background(), snapshotLease)
		if err != nil {
			t.Fatal("second enable control", snapshotLease.snapshot.Stage, err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), snapshotLease)
	if err != nil {
		t.Fatal("second enable checkpoint", err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	startLease, err := s.commitPlatformCheckpoint(
		context.Background(), snapshotLease, checkpoint,
		checkpointEndpoint, platformCheckpointObservationFixture(
			checkpoint, checkpointEndpoint))
	if err != nil {
		t.Fatal("second enable checkpoint commit", err)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			firstEnable.installationID) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation
			FROM app_platform_installations WHERE id=?`,
			firstEnable.installationID) != plan.TargetGeneration {
		t.Fatal("second enable did not activate current tuple")
	}
}

func TestPlatformManagedEnableSecondCycleSQLite(t *testing.T) {
	testPlatformManagedEnableSecondCycle(t, false)
}

func TestPlatformManagedEnableSecondCyclePostgres(t *testing.T) {
	testPlatformManagedEnableSecondCycle(t, true)
}

func testPlatformManagedRepairCurrentTuple(t *testing.T, pg bool) {
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, firstEnable)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable'
		AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`,
		firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil ||
		previous.TargetRuntime == nil {
		t.Fatal("repair source target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	grantsBefore := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_capability_grants WHERE installation_id=?`,
		firstEnable.installationID)
	review, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, firstEnable.installationID,
		previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("repair review", err)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil ||
		plan.Format != platformManagedRepairFormat ||
		plan.Action != appplatform.ActionRepair ||
		plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 ||
		plan.SourcePolicyRevision != plan.TargetPolicyRevision ||
		plan.SourceGeneration != previous.TargetGeneration ||
		plan.TargetGeneration != plan.SourceGeneration+1 {
		t.Fatal("repair review plan", plan)
	}
	commit, err := s.consumePlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1324), previous.TargetRuntime, runtime)
	if err != nil || commit.Stage != appplatform.StageQueued {
		t.Fatal("repair admission", commit, err)
	}
	selected, err := s.claimNextPlatformManagedRepair(
		context.Background(), platformFixtureID(1325))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("repair claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage != appplatform.StageQuiesce {
		current, err = s.advancePlatformManagedEnableStage(
			context.Background(), current)
		if err != nil {
			t.Fatal("repair stage", current.snapshot.Stage, err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), current)
	if err != nil {
		t.Fatal("repair checkpoint", err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||kind FROM
		app_platform_operation_effects WHERE effect_id=?`,
		checkpoint.effectID) != "quiesce:snapshot" {
		t.Fatal("repair checkpoint has wrong durable stage")
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	startLease, err := s.commitPlatformCheckpoint(
		context.Background(), current, checkpoint, checkpointEndpoint,
		platformCheckpointObservationFixture(
			checkpoint, checkpointEndpoint))
	if err != nil || startLease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("repair checkpoint commit", startLease.snapshot, err)
	}
	start, err := s.preparePlatformUpdateStart(
		context.Background(), startLease)
	if err != nil || start.command.Kind != "enable-start" {
		t.Fatal("repair start intent", start.command.Kind, err)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			firstEnable.installationID) != "enabled:ready" ||
		scalar[int64](t, s.st, `SELECT generation
			FROM app_platform_installations WHERE id=?`,
			firstEnable.installationID) != plan.TargetGeneration ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM
			app_platform_capability_grants WHERE installation_id=?`,
			firstEnable.installationID) != grantsBefore {
		t.Fatal("repair did not activate the current tuple")
	}
	access, err := s.preparePlatformWritableBackendAccess(
		context.Background(), actor, firstEnable.installationID)
	if err != nil {
		t.Fatal("repair backend history", err)
	}
	defer access.Close()
	if !access.intent.applied ||
		access.intent.generation != plan.TargetGeneration {
		t.Fatal("repair backend history lost current generation")
	}
}

func TestPlatformManagedRepairCurrentTupleSQLite(t *testing.T) {
	testPlatformManagedRepairCurrentTuple(t, false)
}

func TestPlatformManagedRepairCurrentTuplePostgres(t *testing.T) {
	testPlatformManagedRepairCurrentTuple(t, true)
}

func platformManagedRepairReviewFixture(
	t *testing.T, pg bool,
) (*Server, platformActorSession, platformOperationLease, platformManagedUpdatePlan, platformRuntimeRegistration) {
	t.Helper()
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, firstEnable)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable'
		AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`,
		firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil ||
		previous.TargetRuntime == nil {
		t.Fatal("repair review target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, firstEnable.installationID,
		previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("repair review fixture", err)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil {
		t.Fatal("repair review fixture plan")
	}
	commit, err := s.consumePlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1330), previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("repair review fixture admission", err)
	}
	selected, err := s.claimNextPlatformManagedRepair(
		context.Background(), platformFixtureID(1331))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("repair review fixture claim", selected, err)
	}
	return s, actor, selected.lease, plan, runtime
}

func testPlatformManagedRepairGrantDriftRefused(t *testing.T, pg bool) {
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, firstEnable)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable'
		AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`,
		firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil ||
		previous.TargetRuntime == nil {
		t.Fatal("repair grant drift target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	review, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, firstEnable.installationID,
		previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("repair grant drift review", err)
	}
	installation := firstEnable.installationID
	beforeGeneration := scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, installation)
	mustExec(t, s.st, `UPDATE app_platform_capability_grants
		SET policy_revision=policy_revision+1
		WHERE installation_id=?`, installation)
	if _, err := s.consumePlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, review.ID, review.SHA256,
		platformFixtureID(1332), previous.TargetRuntime,
		runtime); err == nil {
		t.Fatal("repair accepted grant drift")
	}
	if scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`,
		installation) != beforeGeneration ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM
			app_platform_operations WHERE action='repair'`) != 0 {
		t.Fatal("refused repair changed installation or duplicated operation")
	}
}

func TestPlatformManagedRepairGrantDriftRefusedSQLite(t *testing.T) {
	testPlatformManagedRepairGrantDriftRefused(t, false)
}

func TestPlatformManagedRepairGrantDriftRefusedPostgres(t *testing.T) {
	testPlatformManagedRepairGrantDriftRefused(t, true)
}

func testPlatformManagedRepairFailureRequiresRecovery(t *testing.T, pg bool) {
	s, actor, lease, plan, runtime :=
		platformManagedRepairReviewFixture(t, pg)
	grants := scalar[int](t, s.st, `SELECT COUNT(*)
		FROM app_platform_capability_grants WHERE installation_id=?`,
		lease.installationID)
	namespace := scalar[string](t, s.st, `SELECT id
		FROM app_platform_data_namespaces WHERE owner_installation_id=?`,
		lease.installationID)
	current := lease
	var err error
	for current.snapshot.Stage != appplatform.StageQuiesce {
		current, err = s.advancePlatformManagedEnableStage(
			context.Background(), current)
		if err != nil {
			t.Fatal("repair failure stage", current.snapshot.Stage, err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformCheckpointEndpointFixture()
	current, err = s.commitPlatformCheckpoint(
		context.Background(), current, checkpoint, endpoint,
		platformCheckpointObservationFixture(checkpoint, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.preparePlatformUpdateStart(
		context.Background(), current); err != nil {
		t.Fatal("repair start intent", err)
	}
	if err := s.finishPlatformManagedRepairForReview(
		context.Background(), current); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT stage||':'||error_code
		FROM app_platform_operations WHERE id=?`,
		lease.id) != "recovery_required:managed_repair_recovery_required" ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase||':'||
			generation FROM app_platform_installations WHERE id=?`,
			lease.installationID) !=
			"disabled:recovery_required:"+
				platformDeleteDataDecimal(plan.SourceGeneration) ||
		scalar[string](t, s.st, `SELECT state
			FROM app_platform_data_namespaces WHERE id=?`,
			namespace) != "active" ||
		scalar[int](t, s.st, `SELECT COUNT(*)
			FROM app_platform_capability_grants WHERE installation_id=?`,
			lease.installationID) != grants {
		t.Fatal("repair failure lost data, grants or truthful phase")
	}
	retryReview, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, lease.installationID,
		plan.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("retry repair review after recovery", err)
	}
	retryCommit, err := s.consumePlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, retryReview.ID, retryReview.SHA256,
		platformFixtureID(1348), plan.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("retry repair admission", err)
	}
	retryClaim, err := s.claimNextPlatformManagedRepair(
		context.Background(), platformFixtureID(1349))
	if err != nil || retryClaim.lease.id != retryCommit.OperationID {
		t.Fatal("retry repair claim", retryClaim, err)
	}
	retryLease := retryClaim.lease
	for retryLease.snapshot.Stage != appplatform.StageQuiesce {
		retryLease, err = s.advancePlatformManagedEnableStage(
			context.Background(), retryLease)
		if err != nil {
			t.Fatal("retry repair stage", retryLease.snapshot.Stage, err)
		}
	}
	retryCheckpoint, err := s.preparePlatformCheckpoint(
		context.Background(), retryLease)
	if err != nil {
		t.Fatal("retry repair checkpoint", err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	retryStart, err := s.commitPlatformCheckpoint(
		context.Background(), retryLease, retryCheckpoint,
		checkpointEndpoint, platformCheckpointObservationFixture(
			retryCheckpoint, checkpointEndpoint))
	if err != nil {
		t.Fatal("retry repair checkpoint commit", err)
	}
	retryResult := platformRunSyntheticManagedEnable(t, s, retryStart)
	if retryResult.snapshot.Stage != appplatform.StageComplete ||
		scalar[string](t, s.st, `SELECT desired_state||':'||phase
			FROM app_platform_installations WHERE id=?`,
			lease.installationID) != "enabled:ready" {
		t.Fatal("retry repair did not recover the app")
	}
}

func TestPlatformManagedRepairFailureRequiresRecoverySQLite(t *testing.T) {
	testPlatformManagedRepairFailureRequiresRecovery(t, false)
}

func TestPlatformManagedRepairFailureRequiresRecoveryPostgres(t *testing.T) {
	testPlatformManagedRepairFailureRequiresRecovery(t, true)
}

func testPlatformManagedRepairConcurrentClaim(t *testing.T, pg bool) {
	s, _, lease, _, _ := platformManagedRepairReviewFixture(t, pg)
	mustExec(t, s.st, `UPDATE app_platform_operations
		SET lease_until_ms=0 WHERE id=?`, lease.id)
	var wg sync.WaitGroup
	selections := make(chan platformInstallSelection, 8)
	for index := range 8 {
		wg.Go(func() {
			selected, _ := s.claimNextPlatformManagedRepair(
				context.Background(),
				platformFixtureID(1340+index))
			selections <- selected
		})
	}
	wg.Wait()
	close(selections)
	claimed := 0
	for selected := range selections {
		if selected.lease.id == lease.id {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("repair operation claimed %d times", claimed)
	}
}

func TestPlatformManagedRepairConcurrentClaimSQLite(t *testing.T) {
	testPlatformManagedRepairConcurrentClaim(t, false)
}

func TestPlatformManagedRepairConcurrentClaimPostgres(t *testing.T) {
	testPlatformManagedRepairConcurrentClaim(t, true)
}

func TestPlatformManagedRepairActiveAppRefused(t *testing.T) {
	s, actor, enabled := testPlatformManagedEnableSyntheticStart(t, false)
	var target platformInstallProjection
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='install'
		ORDER BY o.completed_ms DESC LIMIT 1`,
		enabled.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &target) != nil ||
		target.Runtime == nil {
		t.Fatal("repair active target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		enabled.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, enabled.installationID,
		target.Runtime, runtime); err == nil {
		t.Fatal("repair review admitted a healthy active app")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_review_plans WHERE action='repair'`) != 0 {
		t.Fatal("refused repair review was persisted")
	}
}

func testPlatformManagedRepairEnableOperatorRace(t *testing.T, pg bool) {
	s, actor, firstEnable := testPlatformManagedEnableSyntheticStart(t, pg)
	platformCompleteSyntheticDisable(t, s, actor, firstEnable)
	var previous platformManagedUpdatePlan
	var raw string
	if err := s.st.queryRow(`SELECT p.plan_json
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.installation_id=? AND o.action='enable'
		AND o.stage='complete' ORDER BY o.completed_ms DESC LIMIT 1`,
		firstEnable.installationID).Scan(&raw); err != nil ||
		json.Unmarshal([]byte(raw), &previous) != nil ||
		previous.TargetRuntime == nil {
		t.Fatal("operator race target", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`,
		firstEnable.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal(err)
	}
	secondActor := platformSecondAdminActor(
		t, s, "managed-enable-admin-two")
	repair, err := s.createPlatformManagedRepairReviewWithRuntimeContext(
		context.Background(), actor, firstEnable.installationID,
		previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("operator repair review", err)
	}
	enable, err := s.createPlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), secondActor, firstEnable.installationID,
		previous.TargetRuntime, runtime)
	if err != nil {
		t.Fatal("operator enable review", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, item := range []struct {
		action appplatform.Action
		actor  platformActorSession
		review platformRemovalReview
		key    string
	}{
		{appplatform.ActionRepair, actor, repair, platformFixtureID(1346)},
		{appplatform.ActionEnable, secondActor, enable, platformFixtureID(1347)},
	} {
		wg.Go(func() {
			var err error
			if item.action == appplatform.ActionRepair {
				_, err = s.consumePlatformManagedRepairReviewWithRuntimeContext(
					context.Background(), item.actor, item.review.ID,
					item.review.SHA256, item.key,
					previous.TargetRuntime, runtime)
			} else {
				_, err = s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(
					context.Background(), item.actor, item.review.ID,
					item.review.SHA256, item.key,
					previous.TargetRuntime, runtime,
					appplatform.ActionEnable)
			}
			results <- err
		})
	}
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, errAppPlatformPlanChanged),
			errors.Is(err, errAppPlatformOperationConflict):
			refused++
		default:
			t.Fatal(err)
		}
	}
	pending := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_operations WHERE action IN ('enable','repair')
		AND completed_ms IS NULL`)
	consumed := scalar[int](t, s.st, `SELECT COUNT(*) FROM
		app_platform_review_plans WHERE id IN (?,?)
		AND consumed_ms IS NOT NULL`, repair.ID, enable.ID)
	if success != 1 || refused != 1 ||
		pending != 1 || consumed != 1 {
		t.Fatal("enable/repair operator race",
			success, refused, pending, consumed)
	}
}

func TestPlatformManagedRepairEnableOperatorRaceSQLite(t *testing.T) {
	testPlatformManagedRepairEnableOperatorRace(t, false)
}

func TestPlatformManagedRepairEnableOperatorRacePostgres(t *testing.T) {
	testPlatformManagedRepairEnableOperatorRace(t, true)
}

func testPlatformManagedDisablePreparesCurrentEnabledGeneration(t *testing.T, pg bool) {
	s, actor, enabled := testPlatformManagedEnableSyntheticStart(t, pg)
	review, err := s.createPlatformRemovalReview(
		actor, enabled.installationID, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("disable review", err)
	}
	commit, err := s.consumePlatformRemovalReview(
		actor, review.ID, review.SHA256, platformFixtureID(1315))
	if err != nil {
		t.Fatal("disable admission", err)
	}
	selected, err := s.claimNextPlatformManagedUninstall(
		context.Background(), platformFixtureID(1316))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("disable claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage == appplatform.StageQueued ||
		current.snapshot.Stage == appplatform.StageValidate {
		current, err = s.st.advancePlatformRemovalControl(current)
		if err != nil {
			t.Fatal("disable control stage", current.snapshot.Stage, err)
		}
	}
	current, err = s.advancePlatformManagedRemovalStage(
		context.Background(), current, appplatform.StageStop)
	if err != nil {
		t.Fatal("disable stop stage", err)
	}
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), current)
	if err != nil {
		t.Fatal("disable current-generation intent", err)
	}
	if intent.sourceOperationID != enabled.id ||
		intent.command.Payload.SourceOperationID != enabled.id ||
		intent.command.Payload.SourceEffectID == "" ||
		intent.command.Payload.SourceReceiptSHA256 == "" ||
		intent.command.Payload.ReleaseSHA256 == "" ||
		intent.command.Payload.DataNamespaceID == "" ||
		intent.nativeID == "" {
		t.Fatal("disable did not bind the enabled generation")
	}
}

func TestPlatformManagedDisablePreparesCurrentEnabledGenerationSQLite(t *testing.T) {
	testPlatformManagedDisablePreparesCurrentEnabledGeneration(t, false)
}

func TestPlatformManagedDisablePreparesCurrentEnabledGenerationPostgres(t *testing.T) {
	testPlatformManagedDisablePreparesCurrentEnabledGeneration(t, true)
}

func TestPlatformManagedEnableCheckpointToStartPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("PostgreSQL integration")
	}
	s, lease := platformManagedEnableSnapshotFixture(t, true)
	if scalar[string](t, s.st, `SELECT action||':'||stage
		FROM app_platform_operations WHERE id=?`, lease.id) !=
		"enable:snapshot" {
		t.Fatal("PostgreSQL enable snapshot fixture")
	}
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := platformCheckpointEndpointFixture()
	observation := platformCheckpointObservationFixture(
		checkpoint, endpoint)
	next, err := s.commitPlatformCheckpoint(
		context.Background(), lease, checkpoint, endpoint, observation)
	if err != nil || next.snapshot.Stage != appplatform.StageStart {
		t.Fatal("PostgreSQL enable checkpoint", next.snapshot, err)
	}
	start, err := s.preparePlatformUpdateStart(
		context.Background(), next)
	if err != nil || start.command.Kind != "enable-start" {
		t.Fatal("PostgreSQL enable start", start.command, err)
	}
}

func TestPlatformManagedEnableReviewAfterRestoredDisableUsesCurrentGeneration(t *testing.T) {
	s, restored, _ := platformRestoreStartActivatedFixture(t, false)
	actor := platformManagedFixtureSession(t, s)
	review, err := s.createPlatformRemovalReview(
		actor, restored.installationID, appplatform.ActionDisable)
	if err != nil {
		t.Fatal("restored disable review", err)
	}
	commit, err := s.consumePlatformRemovalReview(
		actor, review.ID, review.SHA256, platformFixtureID(1317))
	if err != nil {
		t.Fatal("restored disable admission", err)
	}
	selected, err := s.claimNextPlatformManagedUninstall(
		context.Background(), platformFixtureID(1318))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("restored disable claim", selected, err)
	}
	current := selected.lease
	for current.snapshot.Stage == appplatform.StageQueued ||
		current.snapshot.Stage == appplatform.StageValidate {
		current, err = s.st.advancePlatformRemovalControl(current)
		if err != nil {
			t.Fatal("restored disable control stage",
				current.snapshot.Stage, err)
		}
	}
	current, err = s.advancePlatformManagedRemovalStage(
		context.Background(), current, appplatform.StageStop)
	if err != nil {
		t.Fatal("restored disable stop stage", err)
	}
	intent, err := s.preparePlatformManagedRemovalIntent(
		context.Background(), current)
	if err != nil {
		t.Fatal("restored disable intent", err)
	}
	endpoint := platformManagedRemovalFixtureEndpoint()
	observation := platformManagedRemovalObservationFixture(intent, endpoint)
	receipt, ok := validPlatformManagedRemovalObservation(
		observation, intent, endpoint)
	if !ok {
		t.Fatal("restored disable receipt")
	}
	if _, err := s.settlePlatformManagedRemoval(
		context.Background(), current, intent, receipt); err != nil {
		t.Fatal("restored disable settlement", err)
	}
	epoch := scalar[string](t, s.st, `SELECT controller_epoch
		FROM app_platform_installations WHERE id=?`, restored.installationID)
	generation := scalar[int64](t, s.st, `SELECT generation
		FROM app_platform_installations WHERE id=?`, restored.installationID)
	dek, err := s.dek()
	if err != nil {
		t.Fatal(err)
	}
	runtime, seed, err := s.platformDataRuntime(
		s.st.poolExec(), epoch, dek)
	clear(seed)
	if err != nil {
		t.Fatal("restored enable runtime", err)
	}
	now, err := platformDatabaseNow(s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	source, err := loadPlatformCheckpointSourceForEnableReview(
		s.st.poolExec(), platformOperationLease{
			installationID: restored.installationID, controllerEpoch: epoch,
			snapshot: appplatform.OperationSnapshot{
				Action: appplatform.ActionEnable, Managed: true,
				Generation: uint64(generation), Revision: 1,
				Stage: appplatform.StageQueued,
			},
		}, runtime, now)
	if err != nil {
		t.Fatal("restored enable source", err)
	}
	enableReview, err := s.createPlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, restored.installationID,
		source.runtimeTarget, runtime)
	if err != nil {
		t.Fatal("restored enable review", err)
	}
	var plan platformManagedUpdatePlan
	currentWriterFence := scalar[int64](t, s.st, `SELECT writer_fence
		FROM app_platform_data_namespaces
		WHERE owner_installation_id=?`, restored.installationID)
	if err := json.Unmarshal([]byte(enableReview.JSON), &plan); err != nil ||
		plan.Action != appplatform.ActionEnable ||
		plan.SourceGeneration != source.sourceGeneration ||
		plan.TargetGeneration != generation ||
		plan.SourceWriterFence != currentWriterFence ||
		mustPlatformCheckpointInt(source.writerFence) ==
			currentWriterFence {
		t.Fatalf("restored enable review plan: %+v source=%+v err=%v",
			plan, source, err)
	}
	enableCommit, err := s.consumePlatformManagedEnableReviewWithRuntimeContext(
		context.Background(), actor, enableReview.ID, enableReview.SHA256,
		platformFixtureID(1319), source.runtimeTarget, runtime)
	if err != nil {
		t.Fatal("restored enable admission", err)
	}
	enableSelected, err := s.claimNextPlatformManagedEnable(
		context.Background(), platformFixtureID(1320))
	if err != nil || enableSelected.lease.id != enableCommit.OperationID {
		t.Fatal("restored enable claim", enableSelected, err)
	}
	enableLease, err := s.advancePlatformManagedEnableStage(
		context.Background(), enableSelected.lease)
	if err != nil {
		t.Fatal("restored enable validate", err)
	}
	enableLease, err = s.advancePlatformManagedEnableStage(
		context.Background(), enableLease)
	if err != nil || enableLease.snapshot.Stage !=
		appplatform.StageSnapshot {
		t.Fatal("restored enable snapshot stage", enableLease, err)
	}
	checkpoint, err := s.preparePlatformCheckpoint(
		context.Background(), enableLease)
	if err != nil {
		t.Fatal("restored enable checkpoint", err)
	}
	if checkpoint.command.Payload.WriterFence != source.writerFence ||
		checkpoint.command.Payload.WriterFence ==
			strconv.FormatInt(currentWriterFence, 10) {
		t.Fatalf("restored enable checkpoint writer fence=%s historical=%s current=%d",
			checkpoint.command.Payload.WriterFence, source.writerFence,
			currentWriterFence)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	checkpointObservation := platformCheckpointObservationFixture(
		checkpoint, checkpointEndpoint)
	startLease, err := s.commitPlatformCheckpoint(
		context.Background(), enableLease, checkpoint, checkpointEndpoint,
		checkpointObservation)
	if err != nil || startLease.snapshot.Stage != appplatform.StageStart {
		t.Fatal("restored enable checkpoint commit", startLease, err)
	}
	start, err := s.preparePlatformUpdateStart(
		context.Background(), startLease)
	if err != nil {
		t.Fatal("restored enable start intent", err)
	}
	if start.command.Kind != "enable-start" ||
		start.command.Payload.SourceGeneration !=
			strconv.FormatInt(source.sourceGeneration, 10) ||
		start.command.Payload.SourceWriterFence !=
			strconv.FormatInt(currentWriterFence, 10) {
		t.Fatalf("restored enable start command: %+v", start.command)
	}
	result := platformRunSyntheticManagedEnable(t, s, startLease)
	if result.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("restored enable did not complete: %+v", result.snapshot)
	}
	access, err := s.preparePlatformWritableBackendAccess(
		context.Background(), actor, restored.installationID)
	if err != nil {
		t.Fatalf("restored enable backend history: %v", err)
	}
	defer access.Close()
	if access.intent.generation != generation ||
		access.intent.update == nil {
		t.Fatalf("restored enable history generation=%d want=%d",
			access.intent.generation, generation)
	}
}

// platformTestAfterEnableClaim, when set, runs right after the second enable of the cycle test is claimed.
var platformTestAfterEnableClaim func(*testing.T, *Server, platformOperationLease)
