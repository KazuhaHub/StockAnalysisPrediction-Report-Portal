package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// orphanedEnableFixture leaves a repair (whose start is an enable-start) that was ended for review after the
// runner reserved its start, so the start effect is open and the runner may hold it claimed.
func orphanedEnableFixture(t *testing.T) (*Server, string, platformRuntimeEndpoint, *platformFakeMigrationRunner) {
	t.Helper()
	s, _, lease, _, _ := platformManagedRepairReviewFixture(t, false)
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
	return s, lease.id, endpoint, fake
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
	s, operation, endpoint, fake := orphanedEnableFixture(t)
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
	if scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "recovery_required" {
		t.Fatal("settling the start changed the operation")
	}
	if scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations a JOIN app_platform_operations o ON o.installation_id=a.installation_id WHERE o.id=? AND a.state='candidate'`, operation) != 0 {
		t.Fatal("the candidate of a start that never ran was left behind")
	}
	// A closed start is not driven again.
	before := script.settled
	if progressed, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil || progressed != 0 || script.settled != before {
		t.Fatalf("a closed start was settled again: %d %v", progressed, err)
	}
}

// Nothing is settled while the runner may still be executing the start.
func TestSettlementTickLeavesAnEnableStartThatMayBeExecuting(t *testing.T) {
	s, operation, endpoint, fake := orphanedEnableFixture(t)
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
}

// A window that has not passed settles nothing either: a claim may still arrive.
func TestSettlementTickWaitsOutTheStartWindowOfAnEnable(t *testing.T) {
	s, operation, endpoint, fake := orphanedEnableFixture(t)
	mustExec(t, s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms+3600000 WHERE id=?`, operation)
	script := &settleScript{before: held("orphaned"), after: held("never_ran")}
	script.install(s, fake)
	if _, err := s.settlePlatformHeldStarts(context.Background(), endpoint); err != nil || script.settled != 0 {
		t.Fatalf("a start inside its window was settled: %v", err)
	}
}

// An enable orphan the runner finds a container for is closed as applied and nothing is removed or restored.
func TestSettlementTickClosesAnEnableStartTheRunnerFoundRunning(t *testing.T) {
	s, operation, endpoint, fake := orphanedEnableFixture(t)
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
	s, operation, _, _ := orphanedEnableFixture(t)
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
}

// Through the real tick: the runner is asked about the start of every operation, not only the first eight.
func TestHeldStartSettlementTickAsksAboutEveryStartNotOnlyTheFirstEight(t *testing.T) {
	s, operation, endpoint, fake := orphanedEnableFixture(t)
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
}
