package app

import (
	"context"
	"slices"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// orphanedRestoreStartFixture leaves an ordinary roll back (a managed restore that is not a recovery resume) that
// ended for review after the runner reserved its start: the start effect is open, and the runner may hold it claimed
// with nothing executing it.
func orphanedRestoreStartFixture(t *testing.T, pg bool) (*settleFixture, string) {
	t.Helper()
	platformWritableUpdateFixtures(t)
	f := settleOrphaned(t, pg)
	h := newMigrationHTTP(t, f)
	ctx := context.Background()
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	set := recoverySetOf(t, f.s, f.op)
	review := h.json(h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`), 200)
	commit := h.json(h.post(operations, `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7400)+`"}`), 202)
	selected, err := f.s.claimNextPlatformManagedRestore(ctx, platformFixtureID(7401))
	if err != nil || selected.lease.id != h.str(commit, "operation_id") {
		t.Fatal("the roll back was not claimed", selected.lease.id, err)
	}
	lease := selected.lease
	for lease.snapshot.Stage != appplatform.StageStart {
		if lease, err = f.s.runPlatformManagedRestore(ctx, lease, f.endpoint); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := platformUpdateStartEndpointFixture()
	intent, err := f.s.preparePlatformRestoreStart(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	reservation := platformUpdateStartReservationFixture(intent, endpoint).reservation
	reserved, err := f.s.commitPlatformRestoreStartReservation(ctx, lease, intent, endpoint, reservation)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformManagedRestoreForReview(ctx, reserved.lease); err != nil {
		t.Fatal(err)
	}
	operation := lease.id
	if scalar[string](t, f.s.st, `SELECT kind||':'||state FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, operation) != "restore-start:prepared" ||
		scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, operation) != "recovery_required" {
		t.Fatal("fixture: the roll back did not end recovery_required with an open restore start")
	}
	f.op = operation
	f.s.platformMigration = f.fake
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms-3600000 WHERE id=?`, operation)
	return f, operation
}

// A managed restore that is not a recovery resume holds its target reservation from the claim of its start. When the
// restore ends with the start orphaned, the idle settlement asks the runner to settle it (once), and closes the start
// effect on what the runner then reads; the reservation is released by the runner's settlement.
func TestSettlementTickSettlesAnOrphanedRestoreStartOutsideARecoveryResume(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f, operation := orphanedRestoreStartFixture(t, pg)
		script := &settleScript{before: held("orphaned"), after: held("never_ran")}
		script.install(f.s, f.fake)
		candidates, err := f.s.platformHeldStartsToSettle(context.Background())
		if err != nil || !slices.Contains(candidates, operation) {
			t.Fatalf("a finished roll back with an open start is not a candidate: %v %v", candidates, err)
		}
		for range 6 {
			if _, err := f.s.settlePlatformHeldStarts(context.Background(), f.endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if script.settled != 1 {
			t.Fatalf("the runner was asked to settle the orphan %d times", script.settled)
		}
		if script.receipts[0].Outcome != "never_ran" || script.receipts[0].OperationID == "" {
			t.Fatal("unexpected scripted receipt")
		}
		if effectState(t, f.s, operation, "start", 1) != "reverted" {
			t.Fatalf("the restore start was not closed: %s", effectState(t, f.s, operation, "start", 1))
		}
		if effectState(t, f.s, operation, "settle", 3) != "applied" || effectState(t, f.s, operation, "settle", 6) != "applied" {
			t.Fatal("the orphan was not recorded and the namespace was not swept before the start was closed")
		}
		if f.fake.count("execute-start") != 0 {
			t.Fatalf("the settlement sent the start: %v", f.fake.calls)
		}
		// A closed start is not driven again.
		before := script.settled
		if progressed, err := f.s.settlePlatformHeldStarts(context.Background(), f.endpoint); err != nil || progressed != 0 || script.settled != before {
			t.Fatalf("a closed start was settled again: %d %v", progressed, err)
		}
	})
}

// Nothing is settled while the runner may still be executing the start, or inside the start's signed window.
func TestSettlementTickLeavesARestoreStartThatMayBeExecuting(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f, operation := orphanedRestoreStartFixture(t, pg)
		script := &settleScript{before: held("pending"), after: held("pending")}
		script.install(f.s, f.fake)
		for range 3 {
			if _, err := f.s.settlePlatformHeldStarts(context.Background(), f.endpoint); err != nil {
				t.Fatal(err)
			}
		}
		if script.settled != 0 || effectState(t, f.s, operation, "start", 1) != "prepared" {
			t.Fatal("an executing restore start was settled")
		}
		mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms+7200000 WHERE id=?`, operation)
		script = &settleScript{before: held("orphaned"), after: held("never_ran")}
		script.install(f.s, f.fake)
		if _, err := f.s.settlePlatformHeldStarts(context.Background(), f.endpoint); err != nil || script.settled != 0 {
			t.Fatalf("a restore start inside its window was settled: %v", err)
		}
	})
}

// A recovery resume keeps its own path, the recovery reconciliation, which runs only while the Portal is paused: the
// held-start settlement never selects it.
func TestHeldStartSettlementLeavesARecoveryResumeToItsOwnReconciliation(t *testing.T) {
	eachPlatformDriver(t, func(t *testing.T, pg bool) {
		f, operation, _ := unknownResumeStartFixtureFor(t, pg)
		candidates, err := f.s.platformHeldStartsToSettle(context.Background())
		if err != nil || slices.Contains(candidates, operation) {
			t.Fatalf("a recovery resume was selected by the held-start settlement: %v %v", candidates, err)
		}
	})
}
