package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformResumeFixtureState is a restored, paused Portal whose one installation has already had its
// checkpoint imported onto the replacement runtime. Everything after the import is the resume.
type platformResumeFixtureState struct {
	*platformImportFixtureState
	importOperation string
	endpoint        platformRuntimeEndpoint
}

func platformResumeFixture(t *testing.T, pg bool) *platformResumeFixtureState {
	t.Helper()
	f := &platformResumeFixtureState{platformImportFixtureState: platformRestoreImportFixture(t, pg), endpoint: platformUpdateStartEndpointFixture()}
	commit := f.reviewAndConsume(t)
	if lease, err := f.drive(t, commit.OperationID); err != nil || lease.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("fixture import did not complete: %v", err)
	}
	f.importOperation = commit.OperationID
	return f
}

// target is what an observation of the replacement runner yields: its binding and the identity of the
// data root the import landed on.
func (f *platformResumeFixtureState) target() *platformManagedInstallTarget {
	r := f.replacement
	return &platformManagedInstallTarget{
		RuntimeID: r.id, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, BindingRevision: r.revision,
		ControlRevision: r.controlRevision, Fingerprint: r.fingerprint,
		Profile: transport.ManagedRuntimeProfile{
			Protocol: transport.ManagedRunnerProtocol, Platform: "linux/amd64", BackendProtocol: transport.ManagedBackendProtocol,
			SandboxProfile: control.CandidateSandboxProfile, RootFilesystemUUID: platformFixtureID(77), RootInode: "555",
		},
	}
}

func (f *platformResumeFixtureState) reviewAndConsumeResume(t *testing.T) platformRemovalCommit {
	t.Helper()
	review, err := f.s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, f.target())
	if err != nil {
		t.Fatal("resume review:", err)
	}
	commit, err := f.s.consumePlatformRestoreResumeReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5600))
	if err != nil {
		t.Fatal("resume consume:", err)
	}
	return commit
}

// driveResume claims the resume operation and runs its evidence-only stages until it reaches the stage.
func (f *platformResumeFixtureState) driveResume(t *testing.T, operation string, until appplatform.Stage) platformOperationLease {
	t.Helper()
	lease, err := f.s.st.claimPlatformOperation(operation, platformFixtureID(5601))
	if err != nil {
		t.Fatal("claim resume:", err)
	}
	for lease.snapshot.Stage != until {
		next, err := f.s.runPlatformManagedRestore(context.Background(), lease, f.endpoint)
		if err != nil {
			t.Fatalf("resume stage %s: %v", lease.snapshot.Stage, err)
		}
		lease = next
	}
	return lease
}

func testResumeAfterImportAdmittedWhilePaused(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	s := f.s
	if scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "1" {
		t.Fatal("fixture is not paused")
	}
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	review, err := s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, f.target())
	if err != nil {
		t.Fatal("a paused Portal refused the resume review:", err)
	}
	var plan platformRestoreResumePlan
	if json.Unmarshal([]byte(review.JSON), &plan) != nil || plan.Format != platformRestoreResumeFormat || plan.ImportOperationID != f.importOperation ||
		plan.RecoverySetID != f.recoverySet || plan.RuntimeID != f.replacement.id || plan.SourceGeneration != generation || plan.TargetGeneration != generation+1 {
		t.Fatalf("resume plan: %+v", plan)
	}
	if scalar[string](t, s.st, `SELECT action FROM app_platform_review_plans WHERE id=?`, review.ID) != "restore" {
		t.Fatal("a resume is reviewed as a restore")
	}
	commit, err := s.consumePlatformRestoreResumeReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5600))
	if err != nil || commit.Replayed || commit.Generation != generation+1 || commit.Stage != appplatform.StageRevoke {
		t.Fatalf("resume consume: %+v %v", commit, err)
	}
	if scalar[string](t, s.st, `SELECT action||':'||managed FROM app_platform_operations WHERE id=?`, commit.OperationID) != "restore:1" {
		t.Fatal("the resume is a managed restore operation")
	}
	replay, err := s.consumePlatformRestoreResumeReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5600))
	if err != nil || !replay.Replayed || replay.OperationID != commit.OperationID {
		t.Fatalf("resume replay: %+v %v", replay, err)
	}
	if _, err := s.consumePlatformRestoreResumeReview(context.Background(), f.actor, review.ID, review.SHA256, platformFixtureID(5601)); !errors.Is(err, errAppPlatformOperationConflict) {
		t.Fatalf("a second key consumed the review: %v", err)
	}
	// Only the resume selector picks it up, and only while paused.
	if other, err := s.claimNextPlatformManagedRestore(context.Background(), platformFixtureID(5602)); err != nil || other.lease.id != "" {
		t.Fatalf("the ordinary restore queue took a resume: %+v %v", other, err)
	}
	selected, err := s.claimNextPlatformRestoreResume(context.Background(), platformFixtureID(5603))
	if err != nil || selected.lease.id != commit.OperationID || selected.lease.snapshot.Stage != appplatform.StageRevoke {
		t.Fatalf("resume was not leased while paused: %+v %v", selected, err)
	}
	// An ordinary restore review still refuses on the pause: the exception is the resume alone.
	if _, err := s.createPlatformManagedRestoreReview(context.Background(), f.actor, f.installation, f.recoverySet); err == nil {
		t.Fatal("an ordinary restore review ran on a paused Portal")
	}
	// Outside recovery a resume has no standing.
	mustExec(t, s.st, `UPDATE app_platform_operations SET lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, commit.OperationID)
	platformSetPause(t, s.st, "0")
	if _, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5604)); !errors.Is(err, errAppPlatformControlPaused) {
		t.Fatalf("an unpaused Portal leased a resume: %v", err)
	}
}

func TestResumeAfterImportAdmittedWhilePausedSQLite(t *testing.T) {
	testResumeAfterImportAdmittedWhilePaused(t, false)
}
func TestResumeAfterImportAdmittedWhilePausedPostgres(t *testing.T) {
	testResumeAfterImportAdmittedWhilePaused(t, true)
}

func testResumeRefusedWithoutConsumedGrant(t *testing.T, pg bool) {
	cases := map[string]func(*testing.T, *platformResumeFixtureState){
		"grant issued again": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='issued',consumed_ms=NULL`)
		},
		"grant revoked": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked',consumed_ms=NULL`)
		},
		"grant consumed at another time": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET consumed_ms=consumed_ms+1`)
		},
		"grant for another runtime": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET runtime_id=?`, f.oldRuntime)
		},
		"import receipt not applied": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET state='unknown' WHERE operation_id=?`, f.importOperation)
		},
		"import did not complete": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='recovery_required',error_code='restore_import_recovery_required' WHERE id=?`, f.importOperation)
		},
		"installation on the old runtime": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_installations SET runtime_id=?`, f.oldRuntime)
		},
		"work in flight": func(t *testing.T, f *platformResumeFixtureState) {
			mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='quiesce',completed_ms=NULL,error_code=NULL,state_revision=4 WHERE id=?`, f.checkpointOperation)
		},
		"target on another root": func(t *testing.T, f *platformResumeFixtureState) {
			// handled below through the target argument
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := platformResumeFixture(t, pg)
			mutate(t, f)
			target := f.target()
			if name == "target on another root" {
				target.Profile.RootInode = "556"
			}
			before := dumpOf(t, f.s.st)
			if review, err := f.s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, target); err == nil {
				t.Fatalf("a resume was reviewed despite %q: %+v", name, review)
			}
			if string(stripHeader(before)) != string(stripHeader(dumpOf(t, f.s.st))) {
				t.Fatal("a refused review changed state")
			}
		})
	}
	// An import that never ran has no consumed grant either.
	f := platformRestoreImportFixture(t, pg)
	f.reviewAndConsume(t)
	if _, err := f.s.createPlatformRestoreResumeReview(context.Background(), f.actor, f.installation, (&platformResumeFixtureState{platformImportFixtureState: f}).target()); err == nil {
		t.Fatal("a resume was reviewed for an import that never ran")
	}
}

func TestResumeRefusedWithoutConsumedGrantSQLite(t *testing.T) {
	testResumeRefusedWithoutConsumedGrant(t, false)
}
func TestResumeRefusedWithoutConsumedGrantPostgres(t *testing.T) {
	testResumeRefusedWithoutConsumedGrant(t, true)
}

func testResumeStagesSkipNativeDataEffects(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	s := f.s
	commit := f.reviewAndConsumeResume(t)
	importCalls, importReads := f.executor.calls, f.executor.reads
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	var stages []appplatform.Stage
	lease, err := s.st.claimPlatformOperation(commit.OperationID, platformFixtureID(5601))
	if err != nil {
		t.Fatal(err)
	}
	for lease.snapshot.Stage != appplatform.StageStart {
		stages = append(stages, lease.snapshot.Stage)
		if lease, err = s.runPlatformManagedRestore(context.Background(), lease, f.endpoint); err != nil {
			t.Fatalf("stage %s: %v", lease.snapshot.Stage, err)
		}
	}
	want := []appplatform.Stage{appplatform.StageRevoke, appplatform.StageQuiesce, appplatform.StageSnapshot, appplatform.StageRestoreData}
	if len(stages) != len(want) {
		t.Fatalf("stages: %v", stages)
	}
	for i := range want {
		if stages[i] != want[i] {
			t.Fatalf("stages: %v", stages)
		}
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, commit.OperationID) != 0 {
		t.Fatal("a stage that the import already performed issued a native effect")
	}
	if f.executor.calls != importCalls || f.executor.reads != importReads {
		t.Fatal("resume reached the runner's import action")
	}
	// The data stage settled on evidence: the namespace is the installation's again and the retained
	// recovery set is usable, and the install is staged for the unchanged start/probe/activate stages.
	var phase, desired, nsState, nsOwner, recoveryState, candidate string
	var writer int64
	if err := s.st.queryRow(`SELECT i.phase,i.desired_state,n.state,COALESCE(n.owner_installation_id,''),n.writer_fence,r.state
		FROM app_platform_installations i JOIN app_platform_data_namespaces n ON n.id=? JOIN app_platform_recovery_sets r ON r.id=? WHERE i.id=?`,
		f.namespaceID(t), f.recoverySet, f.installation).Scan(&phase, &desired, &nsState, &nsOwner, &writer, &recoveryState); err != nil {
		t.Fatal(err)
	}
	if phase != "recovery_required" || desired != "enabled" || nsState != "active" || nsOwner != f.installation || recoveryState != "verified" {
		t.Fatalf("settled state: %s %s %s %s %s", phase, desired, nsState, nsOwner, recoveryState)
	}
	if err := s.st.queryRow(`SELECT state FROM app_platform_activations WHERE installation_id=? AND generation=?`, f.installation, generation+1).Scan(&candidate); err != nil || candidate != "candidate" {
		t.Fatalf("candidate activation: %q %v", candidate, err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation) != 0 {
		t.Fatal("an activation was active before the resume started")
	}
	// A resume that lost its import evidence cannot advance, even on a stage that has no native effect.
	for name, statement := range map[string]string{
		"grant revoked":          `UPDATE app_platform_import_grants SET state='revoked',consumed_ms=NULL`,
		"import receipt altered": `UPDATE app_platform_operation_effects SET receipt_json=REPLACE(receipt_json,'"entries":"3"','"entries":"4"') WHERE kind='import-checkpoint'`,
	} {
		f2 := platformResumeFixture(t, pg)
		commit2 := f2.reviewAndConsumeResume(t)
		mustExec(t, f2.s.st, statement)
		lease2, err := f2.s.st.claimPlatformOperation(commit2.OperationID, platformFixtureID(5601))
		if err != nil {
			t.Fatal(name, err)
		}
		if _, err := f2.s.runPlatformManagedRestore(context.Background(), lease2, f2.endpoint); !errors.Is(err, errAppPlatformStageEvidence) {
			t.Fatalf("a resume advanced after %s: %v", name, err)
		}
	}
}

func (f *platformResumeFixtureState) namespaceID(t *testing.T) string {
	return scalar[string](t, f.s.st, `SELECT data_namespace_id FROM app_platform_recovery_sets WHERE id=?`, f.recoverySet)
}

func TestResumeStagesSkipNativeDataEffectsSQLite(t *testing.T) {
	testResumeStagesSkipNativeDataEffects(t, false)
}
func TestResumeStagesSkipNativeDataEffectsPostgres(t *testing.T) {
	testResumeStagesSkipNativeDataEffects(t, true)
}

// platformResumeStartToReady runs the unchanged start, probe and activate stages with the same
// observations the managed restore tests use, and returns the lease of the completed operation.
func platformResumeStartToReady(t *testing.T, s *Server, startLease platformOperationLease, endpoint platformRuntimeEndpoint) platformOperationLease {
	t.Helper()
	ctx := context.Background()
	startIntent, err := s.preparePlatformRestoreStart(ctx, startLease)
	if err != nil {
		t.Fatal("resume start intent:", err)
	}
	if startIntent.command.Kind != "restore-start" || startIntent.command.Payload.SourceCheckpointOperationID == startLease.id {
		t.Fatalf("resume start command: %+v", startIntent.command)
	}
	reservation := platformUpdateStartReservationFixture(startIntent, endpoint).reservation
	reserved, err := s.commitPlatformRestoreStartReservation(ctx, startLease, startIntent, endpoint, reservation)
	if err != nil {
		t.Fatal(err)
	}
	runningProof := platformUpdateStartRunningFixture(reserved.intent, reservation, endpoint)
	probeLease, err := s.commitPlatformRestoreStartRunning(ctx, reserved.lease, reserved.intent, endpoint, runningProof)
	if err != nil {
		t.Fatal(err)
	}
	probeIntent, err := s.preparePlatformRestoreProbe(ctx, probeLease)
	if err != nil {
		t.Fatal(err)
	}
	readinessProof := platformUpdateStartReservationFixture(probeIntent, endpoint)
	readinessProof.readiness = journal.UpdateStartReadiness{
		Format: "portal.runner.update-start-readiness/1-dev.1", PayloadSHA256: probeIntent.command.PayloadSHA256,
		RunningReceiptSHA256: platformDigest(mustMarshalPlatformUpdateStartReceipt(*probeIntent.running)),
		NativeID:             probeIntent.running.NativeID, Protocol: "portal.backend/1", DataFormat: probeIntent.command.Payload.DataFormat,
		ObservedMS: probeIntent.running.NativeObservedMS,
	}
	activateLease, err := s.commitPlatformRestoreStartReadiness(ctx, probeLease, probeIntent, endpoint, readinessProof)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := s.preparePlatformRestoreStartRegistration(ctx, activateLease)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := s.commitPlatformRestoreStartRegistration(ctx, activateLease, registration, endpoint, platformRestoreStartRegistrationObservationFixture(t, registration, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	applied, boot := platformRestoreStartBootFixture(t, s, registered, endpoint)
	defer applied.registration.Close()
	result, err := s.commitPlatformRestoreStartActivation(ctx, registered, applied, boot)
	if err != nil {
		t.Fatal("resume activation:", err)
	}
	done := registered
	done.snapshot = result.snapshot
	return done
}

func testResumeReachesReady(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	s := f.s
	commit := f.reviewAndConsumeResume(t)
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	checkpointWriter := scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_recovery_sets WHERE id=?`, f.recoverySet)
	namespaceWriter := scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE id=?`, f.namespaceID(t))
	startLease := f.driveResume(t, commit.OperationID, appplatform.StageStart)
	done := platformResumeStartToReady(t, s, startLease, f.endpoint)
	if done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("resume did not complete: %v", done.snapshot.Stage)
	}
	var phase, desired, runtime string
	var installationGeneration int64
	if err := s.st.queryRow(`SELECT phase,desired_state,runtime_id,generation FROM app_platform_installations WHERE id=?`, f.installation).
		Scan(&phase, &desired, &runtime, &installationGeneration); err != nil ||
		phase != "ready" || desired != "enabled" || runtime != f.replacement.id || installationGeneration != generation+1 {
		t.Fatalf("installation: %s %s %s %d %v", phase, desired, runtime, installationGeneration, err)
	}
	var release string
	var activeGeneration int64
	if err := s.st.queryRow(`SELECT release_sha256,generation FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation).Scan(&release, &activeGeneration); err != nil ||
		activeGeneration != generation+1 || release != scalar[string](t, s.st, `SELECT release_sha256 FROM app_platform_recovery_sets WHERE id=?`, f.recoverySet) {
		t.Fatalf("active activation: %s %d %v", release, activeGeneration, err)
	}
	if got := scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE id=?`, f.namespaceID(t)); got != namespaceWriter+1 {
		t.Fatalf("the namespace writer fence moved %d -> %d", namespaceWriter, got)
	}
	// The start was cut from the import receipt and fenced above the Portal's own namespace fence, with
	// the checkpoint's older fence named separately.
	intentRaw := scalar[string](t, s.st, `SELECT plan_json FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, commit.OperationID)
	var plan platformRestoreResumePlan
	if json.Unmarshal([]byte(intentRaw), &plan) != nil || plan.CheckpointWriterFence != checkpointWriter || plan.NamespaceWriterFence != namespaceWriter || namespaceWriter <= checkpointWriter {
		t.Fatalf("plan fences: %+v checkpoint=%d namespace=%d", plan, checkpointWriter, namespaceWriter)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_activated' AND target_id=?`, f.installation) != 1 {
		t.Fatal("the activation left no audit row")
	}
	if scalar[string](t, s.st, `SELECT v FROM meta WHERE k='app_platform_restore_paused'`) != "1" {
		t.Fatal("resuming an installation ended recovery")
	}
}

func TestResumeReachesReadySQLite(t *testing.T)   { testResumeReachesReady(t, false) }
func TestResumeReachesReadyPostgres(t *testing.T) { testResumeReachesReady(t, true) }

// The scheduler of a paused Portal carries a resume across its evidence-only stages unattended.
func TestResumeSchedulerDrivesEvidenceStages(t *testing.T) {
	f := platformResumeFixture(t, false)
	commit := f.reviewAndConsumeResume(t)
	worker, err := newPlatformWritableInstallScheduler(f.s, f.endpoint, platformFixtureID(5605), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := worker.tick(context.Background()); err != nil {
			t.Fatal(i, err)
		}
	}
	if got := scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID); got != "start" {
		t.Fatalf("the scheduler left the resume at %s", got)
	}
}

// A resume whose start fails ends where an import that failed ends: the installation is awaiting
// review again, nothing the attempt staged survives, the failed operation cannot be continued, and a
// new resume review is admitted. A resume still in flight keeps refusing a second one.
func testResumeRetryAfterFailedStart(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	s := f.s
	ctx := context.Background()
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	namespaceWriter := scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE id=?`, f.namespaceID(t))
	first := f.reviewAndConsumeResume(t)
	lease := f.driveResume(t, first.OperationID, appplatform.StageStart)
	if review, err := s.createPlatformRestoreResumeReview(ctx, f.actor, f.installation, f.target()); err == nil {
		t.Fatalf("a second resume was reviewed while one was in flight: %+v", review)
	}
	if err := s.finishPlatformManagedRestoreForReview(ctx, lease); err != nil {
		t.Fatal("ending the failed resume:", err)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL AND lease_owner IS NULL`, first.OperationID) != 1 {
		t.Fatal("the failed resume was left in flight")
	}
	if _, err := s.st.claimPlatformOperation(first.OperationID, platformFixtureID(5710)); err == nil {
		t.Fatal("a failed resume regained a lease")
	}
	if next, err := s.claimNextPlatformRestoreResume(ctx, platformFixtureID(5711)); err != nil || next.lease.id != "" {
		t.Fatalf("the scheduler selected a failed resume: %+v %v", next, err)
	}
	var phase, desired, nsState, recoveryState string
	var owner *string
	if err := s.st.queryRow(`SELECT i.phase,i.desired_state,n.state,n.owner_installation_id,r.state
		FROM app_platform_installations i JOIN app_platform_data_namespaces n ON n.id=? JOIN app_platform_recovery_sets r ON r.id=? WHERE i.id=?`,
		f.namespaceID(t), f.recoverySet, f.installation).Scan(&phase, &desired, &nsState, &owner, &recoveryState); err != nil {
		t.Fatal(err)
	}
	if phase != "review_required" || desired != "disabled" || nsState != "review_required" || owner != nil || recoveryState != "review_required" {
		t.Fatalf("failed resume state: %s %s %s %v %s", phase, desired, nsState, owner, recoveryState)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND generation=?`, f.installation, generation+1) != 0 {
		t.Fatal("the failed resume left an activation behind")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_resume_ended' AND target_id=?`, first.OperationID) != 1 {
		t.Fatal("the failed resume left no audit row")
	}

	second := func() platformRemovalCommit {
		review, err := s.createPlatformRestoreResumeReview(ctx, f.actor, f.installation, f.target())
		if err != nil {
			t.Fatal("a resume could not be retried:", err)
		}
		commit, err := s.consumePlatformRestoreResumeReview(ctx, f.actor, review.ID, review.SHA256, platformFixtureID(5713))
		if err != nil || commit.OperationID == first.OperationID {
			t.Fatalf("retry consume: %+v %v", commit, err)
		}
		return commit
	}()
	startLease := f.driveResume(t, second.OperationID, appplatform.StageStart)
	done := platformResumeStartToReady(t, s, startLease, f.endpoint)
	if done.snapshot.Stage != appplatform.StageComplete {
		t.Fatalf("the retried resume did not complete: %v", done.snapshot.Stage)
	}
	if got := scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation); got != "ready" {
		t.Fatalf("installation after retry: %s", got)
	}
	if scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation) != generation+1 ||
		scalar[int64](t, s.st, `SELECT writer_fence FROM app_platform_data_namespaces WHERE id=?`, f.namespaceID(t)) != namespaceWriter+1 {
		t.Fatal("the retry did not advance the generation and the writer fence exactly once")
	}
}

func TestResumeRetryAfterFailedStart(t *testing.T) { testResumeRetryAfterFailedStart(t, false) }
func TestResumeRetryAfterFailedStartPostgres(t *testing.T) {
	testResumeRetryAfterFailedStart(t, true)
}

// A start that was reserved may have been executed: the runner can be running the candidate with no
// Portal record. Ending the resume then keeps everything the start staged and leaves the installation
// awaiting recovery, so neither a new resume nor the end of recovery can be admitted over it.
func testResumeUnknownStartStaysRecoveryRequired(t *testing.T, pg bool) {
	f := platformResumeFixture(t, pg)
	s := f.s
	ctx := context.Background()
	generation := scalar[int64](t, s.st, `SELECT generation FROM app_platform_installations WHERE id=?`, f.installation)
	first := f.reviewAndConsumeResume(t)
	lease := f.driveResume(t, first.OperationID, appplatform.StageStart)
	intent, err := s.preparePlatformRestoreStart(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := s.commitPlatformRestoreStartReservation(ctx, lease, intent, f.endpoint, platformUpdateStartReservationFixture(intent, f.endpoint).reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishPlatformManagedRestoreForReview(ctx, reserved.lease); err != nil {
		t.Fatal("ending the resume:", err)
	}
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, first.OperationID) != "recovery_required" ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=?`, first.OperationID) != "unknown" {
		t.Fatal("an unsettled start did not leave the operation recovery_required with an unknown effect")
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation) != "recovery_required" ||
		scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND generation=?`, f.installation, generation+1) != 1 ||
		scalar[string](t, s.st, `SELECT state FROM app_platform_data_namespaces WHERE id=?`, f.namespaceID(t)) != "active" {
		t.Fatal("an unsettled start was returned to review or lost its staged rows")
	}
	if _, err := s.createPlatformRestoreResumeReview(ctx, f.actor, f.installation, f.target()); err == nil {
		t.Fatal("a new resume was reviewed over an unsettled start")
	}
	if err := s.endPlatformRecovery(f.actor); !errors.Is(err, errAppPlatformRecoveryUnsettled) {
		t.Fatalf("recovery ended over an unsettled start: %v", err)
	}
}

func TestResumeUnknownStartStaysRecoveryRequired(t *testing.T) {
	testResumeUnknownStartStaysRecoveryRequired(t, false)
}
func TestResumeUnknownStartStaysRecoveryRequiredPostgres(t *testing.T) {
	testResumeUnknownStartStaysRecoveryRequired(t, true)
}

// A queued resume whose import evidence no longer loads is ended for review by the claim, rather than
// failing every claim after it and so blocking the resumes that are fine.
func TestResumeClaimEndsAQueuedResumeWhoseEvidenceFails(t *testing.T) {
	f := platformResumeFixture(t, false)
	ctx := context.Background()
	commit := f.reviewAndConsumeResume(t)
	mustExec(t, f.s.st, `UPDATE app_platform_import_grants SET state='revoked',consumed_ms=NULL`)
	next, err := f.s.claimNextPlatformRestoreResume(ctx, platformFixtureID(5800))
	if err != nil || next.lease.id != "" {
		t.Fatalf("the claim was blocked or selected a broken resume: %+v %v", next, err)
	}
	if scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, commit.OperationID) != "failed" ||
		scalar[string](t, f.s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.installation) != "review_required" ||
		scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.restore_resume_ended' AND target_id=?`, commit.OperationID) != 1 {
		t.Fatal("the broken resume was not ended for review")
	}
	if again, err := f.s.claimNextPlatformRestoreResume(ctx, platformFixtureID(5801)); err != nil || again.lease.id != "" {
		t.Fatalf("a later claim failed: %+v %v", again, err)
	}
}
