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
)

// orphanedEnable is the repair left by orphanedEnableFixture, with what a second repair of the same installation needs.
type orphanedEnable struct {
	s        *Server
	op       string
	endpoint platformRuntimeEndpoint
	fake     *platformFakeMigrationRunner
	actor    platformActorSession
	plan     platformManagedUpdatePlan
	runtime  platformRuntimeRegistration
}

// orphanedEnableFixture leaves a repair (whose start is an enable-start) that was ended for review after the
// runner reserved its start, so the start effect is open and the runner may hold it claimed.
func orphanedEnableFixture(t *testing.T, pg bool) (*Server, string, platformRuntimeEndpoint, *platformFakeMigrationRunner) {
	t.Helper()
	o := orphanedEnableState(t, pg)
	return o.s, o.op, o.endpoint, o.fake
}

// driveRepairToStart runs a claimed repair to its start stage and reserves the start.
func driveRepairToStart(t *testing.T, s *Server, lease platformOperationLease) (platformOperationLease, platformRuntimeEndpoint, platformUpdateStartIntent) {
	t.Helper()
	ctx := context.Background()
	current := lease
	var err error
	for current.snapshot.Stage != appplatform.StageQuiesce {
		if current, err = s.advancePlatformManagedEnableStage(ctx, current); err != nil {
			t.Fatal(err)
		}
	}
	checkpoint, err := s.preparePlatformCheckpoint(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	checkpointEndpoint := platformCheckpointEndpointFixture()
	if current, err = s.commitPlatformCheckpoint(ctx, current, checkpoint, checkpointEndpoint, platformCheckpointObservationFixture(checkpoint, checkpointEndpoint)); err != nil {
		t.Fatal(err)
	}
	endpoint := platformUpdateStartEndpointFixture()
	intent, err := s.preparePlatformUpdateStart(ctx, current)
	if err != nil {
		t.Fatal(err)
	}
	return current, endpoint, intent
}

func orphanedEnableState(t *testing.T, pg bool) orphanedEnable {
	t.Helper()
	s, actor, lease, plan, runtime := platformManagedRepairReviewFixture(t, pg)
	ctx := context.Background()
	current, endpoint, intent := driveRepairToStart(t, s, lease)
	observation := platformUpdateStartReservationFixture(intent, endpoint)
	reservation, ok := validPlatformUpdateStartObservation(observation, intent, endpoint)
	if !ok {
		t.Fatal("reservation refused")
	}
	if _, err := s.commitPlatformUpdateStartReservation(ctx, current, intent, endpoint, observation, reservation); err != nil {
		t.Fatal(err)
	}
	if err := s.finishPlatformManagedRepairForReview(ctx, current); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, s.st, `SELECT kind||':'||state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id) != "enable-start:prepared" {
		t.Fatal("fixture: the start effect is not an open enable-start")
	}
	fake := &platformFakeMigrationRunner{t: t}
	s.platformMigration = fake
	// Every window a command for the start could have been signed for has passed.
	mustExec(t, s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms-3600000 WHERE id=?`, lease.id)
	return orphanedEnable{s: s, op: lease.id, endpoint: endpoint, fake: fake, actor: actor, plan: plan, runtime: runtime}
}

// reRepair reviews and admits a second repair of the same installation and drives it to the point its start is
// prepared. It is what an administrator does after the first one was closed; it fails the test if the start cannot be
// prepared.
func (o orphanedEnable) reRepair(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	installation := scalar[string](t, o.s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, o.op)
	review, err := o.s.createPlatformManagedRepairReviewWithRuntimeContext(ctx, o.actor, installation, o.plan.TargetRuntime, o.runtime)
	if err != nil {
		t.Fatalf("the closed repair left the installation unable to be repaired again: %v", err)
	}
	commit, err := o.s.consumePlatformManagedRepairReviewWithRuntimeContext(ctx, o.actor, review.ID, review.SHA256, platformFixtureID(1340), o.plan.TargetRuntime, o.runtime)
	if err != nil {
		t.Fatalf("the second repair was not admitted: %v", err)
	}
	selected, err := o.s.claimNextPlatformManagedRepair(ctx, platformFixtureID(1341))
	if err != nil || selected.lease.id != commit.OperationID {
		t.Fatal("the second repair was not claimed", selected.lease.id, err)
	}
	driveRepairToStart(t, o.s, selected.lease)
}

// settleScript scripts the runner for a start it holds: what the read says before and after a settlement.
type settleScript struct {
	settled  int
	before   error
	after    error
	receipts []journal.SettleUpdateStartReceipt
}

func (c *settleScript) install(s *Server, fake *platformFakeMigrationRunner) {
	s.platformReadStartReceipt = func(_ context.Context, _ platformRuntimeEndpoint, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if c.settled > 0 {
			return nil, c.after
		}
		return nil, c.before
	}
	s.platformSettleStart = platformFakeSettleStart(fake, func(_ context.Context, _ platformRuntimeEndpoint, intent platformSettleStartIntent) (journal.SettleUpdateStartReceipt, error) {
		verified, err := control.VerifySettleUpdateStart(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
		if err != nil {
			return journal.SettleUpdateStartReceipt{}, errors.New("the settlement was not a valid command")
		}
		if kind := verified.Start().Command().Kind; kind != "enable-start" && kind != "restore-start" {
			return journal.SettleUpdateStartReceipt{}, errors.New("the settlement names a start of kind " + kind)
		}
		c.settled++
		at := strconv.FormatInt(time.Now().UnixMilli(), 10)
		command, start := verified.Command(), verified.Start().Command()
		receipt := journal.SettleUpdateStartReceipt{
			Format: "portal.runner.settle-update-start-receipt/1-dev.1", DeploymentID: start.DeploymentID, ControllerEpoch: start.ControllerEpoch,
			InstallationID: start.InstallationID, OperationID: command.OperationID, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
			StartOperationID: start.OperationID, StartEffectID: start.EffectID, ResourceID: start.Payload.ResourceID, NamespaceID: start.Payload.NamespaceID,
			Outcome: "never_ran", NativeID: control.ReservedStartNativeID(start.Payload.ResourceID), ObservedMS: at, SettledMS: at,
		}
		c.receipts = append(c.receipts, receipt)
		return receipt, nil
	})
}

// A claimed enable start with nothing executing it is settled by the runner, once, from the idle settlement tick,
// and its effect is closed on what the runner then reads.
func TestSettlementTickSettlesAnOrphanedEnableStart(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		script := &settleScript{before: held("orphaned"), after: held("never_ran")}
		script.install(s, fake)
		for range 6 {
			if _, err := s.settlePlatformMigrations(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if script.settled != 1 {
			t.Fatalf("the runner was asked to settle the orphan %d times", script.settled)
		}
		if effectState(t, s, operation, "start", 1) != "reverted" {
			t.Fatalf("the enable start was not closed: %s", effectState(t, s, operation, "start", 1))
		}
		if effectState(t, s, operation, "settle", 3) != "applied" || effectState(t, s, operation, "settle", 6) != "applied" || fake.count("settle-start") != 1 {
			t.Fatal("the orphan was not recorded and the namespace was not swept before the start was closed")
		}
		requireSameReleaseOperationClosed(t, s, operation)
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations a JOIN app_platform_operations o ON o.installation_id=a.installation_id WHERE o.id=? AND a.state='candidate'`, operation) != 0 {
			t.Fatal("the candidate of a start that never ran was left behind")
		}
		// A closed start is not driven again, and the operation is not closed a second time.
		before := script.settled
		if progressed, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil || progressed != 0 || script.settled != before {
			t.Fatalf("a closed start was settled again: %d %v", progressed, err)
		}
		requireSameReleaseOperationClosed(t, s, operation)
	})
}

// requireSameReleaseOperationClosed is what a start proven never to have run leaves behind: the operation failed
// with the typed reason the status view shows, the installation in recovery_required, the phase a repair is reviewed from
// (so a repair can be reviewed again), and one audit entry carrying the evidence.
func requireSameReleaseOperationClosed(t *testing.T, s *Server, operation string) {
	t.Helper()
	if got := scalar[string](t, s.st, `SELECT stage||':'||COALESCE(error_code,'') FROM app_platform_operations WHERE id=?`, operation); got != "failed:start_never_ran" {
		t.Fatalf("the operation was not closed failed with the typed reason: %s", got)
	}
	if got := scalar[string](t, s.st, `SELECT i.desired_state||':'||i.phase FROM app_platform_installations i JOIN app_platform_operations o ON o.installation_id=i.id WHERE o.id=?`, operation); got != "disabled:recovery_required" {
		t.Fatalf("the installation left the phase a repair is reviewed from: %s", got)
	}
	if platformPublicOperationErrorCode("start_never_ran") != "start_never_ran" {
		t.Fatal("the reason is not one the views show")
	}
	var detail string
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.start_never_ran_closed' AND target_id=?`, operation) != 1 {
		t.Fatal("the closing left no single audit entry")
	}
	if err := s.st.queryRow(`SELECT detail FROM audit_log WHERE action='app_platform.start_never_ran_closed' AND target_id=?`, operation).Scan(&detail); err != nil ||
		!strings.Contains(detail, `"start_effect_id"`) || !strings.Contains(detail, `"proof"`) {
		t.Fatalf("the evidence was not recorded: %s %v", detail, err)
	}
}

// requireOperationStillInRecovery is what a start that is not proven never to have run leaves: nothing closed.
func requireOperationStillInRecovery(t *testing.T, s *Server, operation string) {
	t.Helper()
	if got := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation); got != "recovery_required" {
		t.Fatalf("an operation whose start was not proven never to have run was closed: %s", got)
	}
	if got := scalar[string](t, s.st, `SELECT phase FROM app_platform_installations i JOIN app_platform_operations o ON o.installation_id=i.id WHERE o.id=?`, operation); got != "recovery_required" {
		t.Fatalf("the installation left recovery: %s", got)
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.start_never_ran_closed' AND target_id=?`, operation) != 0 {
		t.Fatal("a closing was audited")
	}
}

// Nothing is settled while the runner may still be executing the start.
func TestSettlementTickLeavesAnEnableStartThatMayBeExecuting(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		script := &settleScript{before: held("pending"), after: held("pending")}
		script.install(s, fake)
		for range 3 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if script.settled != 0 || effectState(t, s, operation, "start", 1) != "prepared" {
			t.Fatal("an executing start was settled")
		}
	})
}

// A window that has not passed settles nothing either: a claim may still arrive.
func TestSettlementTickWaitsOutTheStartWindowOfAnEnable(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		mustExec(t, s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms+3600000 WHERE id=?`, operation)
		script := &settleScript{before: held("orphaned"), after: held("never_ran")}
		script.install(s, fake)
		if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil || script.settled != 0 {
			t.Fatalf("a start inside its window was settled: %v", err)
		}
	})
}

// An enable orphan the runner finds a container for is closed as applied and nothing is removed or restored.
func TestSettlementTickClosesAnEnableStartTheRunnerFoundRunning(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		s.platformReadStartReceipt = func(_ context.Context, ep platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			return platformUpdateStartRunningFixture(i, *i.reservation, endpoint), nil
		}
		for range 2 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if effectState(t, s, operation, "start", 1) != "applied" || fake.count("remove-stop") != 0 || fake.count("execute-restore") != 0 || fake.count("execute-start") != 0 {
			t.Fatalf("running start: %s %v", effectState(t, s, operation, "start", 1), fake.calls)
		}
		requireOperationStillInRecovery(t, s, operation)
	})
}

// A resume whose restore start the runner holds claimed with nothing executing it is settled from the recovery
// reconciliation: the runner is asked once, and the start it then reads as never run reverts the effect and returns
// the installation to review.
func TestRecoveryReconcileSettlesAnOrphanedRestoreStart(t *testing.T) {
	f, operation, _ := unknownResumeStartFixture(t)
	script := &settleScript{before: held("orphaned"), after: held("never_ran")}
	script.install(f.s, &platformFakeMigrationRunner{t: t})
	f.s.platformMigration = &platformFakeMigrationRunner{t: t}
	reconcile := func() {
		t.Helper()
		if selected, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil || selected.lease.id != "" {
			t.Fatalf("reconcile: %+v %v", selected, err)
		}
	}
	// Inside the start's window nothing is settled.
	reconcile()
	if script.settled != 0 || effectState(t, f.s, operation, "start", 1) != "unknown" {
		t.Fatal("a start inside its window was settled")
	}
	ageRecoveryStart(t, f.s, operation)
	reconcile()
	if script.settled != 1 || effectState(t, f.s, operation, "start", 1) != "unknown" {
		t.Fatalf("the orphan was not settled exactly once: %d", script.settled)
	}
	if script.receipts[0].Outcome != "never_ran" {
		t.Fatal("unexpected scripted receipt")
	}
	ageRecoveryStart(t, f.s, operation)
	reconcile()
	if effectState(t, f.s, operation, "start", 1) != "reverted" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "failed" ||
		scalar[string](t, f.s.st, `SELECT desired_state||':'||phase FROM app_platform_installations WHERE id=?`, f.installation) != "disabled:review_required" {
		t.Fatal("a settled orphan did not revert the start and return the installation to review")
	}
	if script.settled != 1 {
		t.Fatal("a closed start was settled again")
	}
}

// A start the runner holds as reserved (never claimed) or pending (maybe executing) is only read again.
func TestRecoveryReconcileLeavesAReservedOrPendingRestoreStart(t *testing.T) {
	for _, state := range []string{"reserved", "pending"} {
		t.Run(state, func(t *testing.T) {
			f, operation, _ := unknownResumeStartFixture(t)
			script := &settleScript{before: held(state), after: held(state)}
			script.install(f.s, &platformFakeMigrationRunner{t: t})
			f.s.platformMigration = &platformFakeMigrationRunner{t: t}
			ageRecoveryStart(t, f.s, operation)
			if _, err := f.s.reconcilePlatformRecoveryResumes(context.Background(), f.endpoint, platformFixtureID(5640)); err != nil {
				t.Fatal(err)
			}
			if script.settled != 0 || effectState(t, f.s, operation, "start", 1) != "unknown" {
				t.Fatalf("a %s start was acted on", state)
			}
		})
	}
}

// ageRecoveryStart moves the end of the resume and every recorded read past all the windows a start could be
// admitted in.
func ageRecoveryStart(t *testing.T, s *Server, operation string) {
	t.Helper()
	mustExec(t, s.st, `UPDATE app_platform_operations SET completed_ms=1 WHERE id=?`, operation)
	raw := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation)
	if reservation, _, ok := platformStartRecoveryReceipt(raw); ok {
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='start'`, platformStartPendingReceipt(reservation, 1), operation)
	}
}

// cloneHeldStarts adds n more finished repairs, each on an installation of its own, whose start effect is as open
// as the fixture's. They exist only to be listed: nothing about them can be settled.
func cloneHeldStarts(t *testing.T, s *Server, operation string, n int) []string {
	t.Helper()
	ids := []string{operation}
	for i := range n {
		installation, plan, op := platformFixtureID(9600+i), platformFixtureID(9700+i), platformFixtureID(9800+i)
		mustExec(t, s.st, `INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms)
			SELECT ?,?,i.publisher_id,i.runtime_id,i.desired_state,i.phase,i.generation,i.revision,i.policy_revision,i.control_fence,i.controller_epoch,i.sealed_identity_seed,i.created_ms,i.updated_ms
			FROM app_platform_installations i JOIN app_platform_operations o ON o.installation_id=i.id WHERE o.id=?`, installation, "com.example.held"+strconv.Itoa(i), operation)
		mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			SELECT ?,?,?,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,?,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
			FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, installation, "com.example.held"+strconv.Itoa(i), platformDigest([]byte(plan)), operation)
		mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			SELECT ?,?,?,actor_scope,?,?,action,managed,source_generation,target_generation,stage,state_revision,row_revision,NULL,NULL,fence,controller_epoch,error_code,completed_ms-?,created_ms,updated_ms
			FROM app_platform_operations WHERE id=?`, op, installation, plan, platformDigest([]byte(op)), platformDigest([]byte(plan+op)), i+1, operation)
		mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT ?,stage,effect_number,?,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms
			FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=1`, op, platformFixtureID(9900+i), operation)
		ids = append(ids, op)
	}
	return ids
}

// Eight starts that keep failing must not keep every later one from being tried. The window rotates, so every open
// start is reached within ceil(n/8) ticks, however the first ones behave.
func TestHeldStartSettlementReachesEveryOpenStart(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, _, _ := orphanedEnableFixture(t, pg)
		all := cloneHeldStarts(t, s, operation, 19)
		if len(all) != 20 {
			t.Fatal("fixture")
		}
		seen := map[string]int{}
		for range 3 {
			window, err := s.platformHeldStartsToSettle(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(window) > platformHeldStartWindow {
				t.Fatalf("a window of %d starts", len(window))
			}
			for _, id := range window {
				seen[id]++
			}
		}
		for _, id := range all {
			if seen[id] == 0 {
				t.Fatalf("the open start of %s was not reached in three ticks: %v", id, seen)
			}
		}
	})
}

// Through the real tick: the runner is asked about the start of every operation, not only the first eight.
func TestHeldStartSettlementTickAsksAboutEveryStartNotOnlyTheFirstEight(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		cloneHeldStarts(t, s, operation, 11)
		asked := map[string]bool{}
		s.platformReadStartReceipt = func(_ context.Context, _ platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			asked[i.command.OperationID] = true
			return nil, errors.New("the runner is unreachable")
		}
		_ = fake
		for range 3 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		// The clones are older, so they fill the first window and carry no evidence that verifies; what is proven is
		// that failing starts ahead of the fixture do not keep it from being reached.
		if !asked[operation] {
			t.Fatal("a start behind a window of failing starts was never tried")
		}
	})
}

// An enable or repair start the runner holds only as reserved (admitted, never claimed) leaves a pending target
// resource. It is removed bound to the reservation, exactly as an update's is, and the start is then closed as
// never run: the resource row is not left behind for an administrator.
func TestSettlementTickRemovesTheTargetOfAReservedEnableStart(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		var removals []string
		fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
			if i.command.RunningReceiptSHA256 != control.ReservedStartReceiptSHA256 || i.start.running != nil || i.start.command.Kind != "enable-start" {
				t.Errorf("the removal is not bound to the reserved enable start: %+v", i.command)
			}
			removals = append(removals, i.command.Stage)
			result := "stopped"
			if i.command.Stage == "remove_code" {
				result = "absent"
			}
			return platformUpdateTargetRemovalObservationFixture(i, endpoint, result), nil
		}
		script := &settleScript{before: held("reserved"), after: held("reserved")}
		script.install(s, fake)
		s.platformReadStartReceipt = func(_ context.Context, _ platformRuntimeEndpoint, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			if len(removals) > 0 {
				return nil, held("never_ran")
			}
			return nil, held("reserved")
		}
		for range 8 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if len(removals) != 2 || removals[0] != "stop" || removals[1] != "remove_code" || script.settled != 0 {
			t.Fatalf("the reserved target was not removed by the reserved-only removal alone: %v settlements=%d", removals, script.settled)
		}
		if effectState(t, s, operation, "start", 1) != "reverted" || effectState(t, s, operation, "settle", 4) != "applied" || effectState(t, s, operation, "settle", 5) != "applied" ||
			effectState(t, s, operation, "settle", 6) != "applied" {
			t.Fatal("the start was not closed as never run after its target was removed and its namespace swept")
		}
		requireSameReleaseOperationClosed(t, s, operation)
		if fake.count("execute-start") != 0 || fake.count("execute-restore") != 0 {
			t.Fatalf("the settlement sent the start: %v", fake.calls)
		}
	})
}

// A start the runner refused for capacity never ran, whatever the signed window: the operation is closed the same way.
func TestSettlementTickFailsAnEnableWhoseStartTheRunnerRefusedForCapacity(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, _ := orphanedEnableFixture(t, pg)
		s.platformReadStartReceipt = func(_ context.Context, _ platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			id := journal.CapacityIdentityOfUpdateStart(i.command)
			return nil, &journal.CapacityRefusalError{Receipt: journal.CapacityRefusal{
				Format: "portal.runner.capacity-refusal/1-dev.1", DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch,
				InstallationID: id.InstallationID, OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage,
				EffectNumber: id.EffectNumber, EffectID: id.EffectID, Kind: id.Kind, PayloadSHA256: id.PayloadSHA256,
				Outcome: "capacity_exhausted", Dimension: "memory", Requested: "256", Remaining: "100",
				SettledMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
			}}
		}
		for range 3 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if effectState(t, s, operation, "start", 1) != "reverted" {
			t.Fatalf("the refused start was not reverted: %s", effectState(t, s, operation, "start", 1))
		}
		requireSameReleaseOperationClosed(t, s, operation)
	})
}

// A start the runner holds no row for and that recorded no reservation was never admitted, once every window has
// passed: the operation is closed the same way.
func TestSettlementTickFailsAnEnableWhoseStartWasNeverAdmitted(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, _ := orphanedEnableFixture(t, pg)
		mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json='{}',observed_fence=0 WHERE operation_id=? AND stage='start' AND effect_number=1`, operation)
		s.platformReadStartReceipt = func(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			return nil, errPlatformStartAbsent
		}
		for range 3 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if effectState(t, s, operation, "start", 1) != "reverted" {
			t.Fatalf("the absent start was not reverted: %s", effectState(t, s, operation, "start", 1))
		}
		requireSameReleaseOperationClosed(t, s, operation)
	})
}

// The start is reverted, but another effect of the operation has no verdict: that is not proof the namespace was left
// alone, so the operation stays in recovery for an administrator. Nothing is closed on a guess.
func TestSettlementTickKeepsAnEnableInRecoveryWhileAnotherEffectIsUnsettled(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		s, operation, endpoint, fake := orphanedEnableFixture(t, pg)
		mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT operation_id,stage,31,?,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,0,'unknown','{}',created_ms,updated_ms
			FROM app_platform_operation_effects WHERE operation_id=? AND stage<>'start' AND state='applied' LIMIT 1`, platformFixtureID(9990), operation)
		if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state='unknown' AND stage<>'start'`, operation) != 1 {
			t.Fatal("fixture: the unsettled effect was not added")
		}
		script := &settleScript{before: held("orphaned"), after: held("never_ran")}
		script.install(s, fake)
		for range 6 {
			if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if effectState(t, s, operation, "start", 1) != "reverted" {
			t.Fatal("the fixture did not revert the start")
		}
		requireOperationStillInRecovery(t, s, operation)
	})
}

// eachPlatformDriver runs a test on SQLite and, when TEST_POSTGRES_DSN is set, on PostgreSQL.
func eachPlatformDriver(t *testing.T, fn func(t *testing.T, pg bool)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { fn(t, false) })
	t.Run("postgres", func(t *testing.T) { fn(t, true) })
}

// Closing a reserved-only start leaves no candidate activation at the target generation, so the installation can be
// repaired again: the second start finds the activation row it needs to open itself, instead of the stale one.
func TestReservedOnlyCloseLeavesNoCandidateAndTheInstallationCanBeRepairedAgain(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		o := orphanedEnableState(t, pg)
		var removals []string
		o.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
			removals = append(removals, i.command.Stage)
			result := "stopped"
			if i.command.Stage == "remove_code" {
				result = "absent"
			}
			return platformUpdateTargetRemovalObservationFixture(i, o.endpoint, result), nil
		}
		script := &settleScript{before: held("reserved"), after: held("reserved")}
		script.install(o.s, o.fake)
		o.s.platformReadStartReceipt = func(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
			if len(removals) > 0 {
				return nil, held("never_ran")
			}
			return nil, held("reserved")
		}
		for range 8 {
			if _, err := o.s.settlePlatformHeldStarts(context.Background(), o.endpoint); err != nil {
				t.Fatal(err)
			}
		}
		requireSameReleaseOperationClosed(t, o.s, o.op)
		if n := scalar[int](t, o.s.st, `SELECT COUNT(*) FROM app_platform_activations a JOIN app_platform_operations o ON o.installation_id=a.installation_id AND o.target_generation=a.generation WHERE o.id=? AND a.state='candidate'`, o.op); n != 0 {
			t.Fatalf("%d candidate activations were left at the target generation", n)
		}
		o.reRepair(t)
	})
}
