package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func platformManagedEnableSnapshotFixture(t *testing.T, pg bool) (*Server, platformOperationLease) {
	t.Helper()
	s, _, lease := platformManagedRemovalStopFixtureAction(
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

func TestPlatformManagedEnableSyntheticStartActivatesCurrentTupleSQLite(t *testing.T) {
	testPlatformManagedEnableSyntheticStart(t, false)
}

func TestPlatformManagedEnableSyntheticStartActivatesCurrentTuplePostgres(t *testing.T) {
	testPlatformManagedEnableSyntheticStart(t, true)
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
}
