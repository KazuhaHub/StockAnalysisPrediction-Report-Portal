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
	if effectState(t, s, operation, "settle", 3) != "applied" || effectState(t, s, operation, "settle", 6) != "applied" || fake.count("sweep") != 0 && fake.count("settle-start") != 1 {
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
