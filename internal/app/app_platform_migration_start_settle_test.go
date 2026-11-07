package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// startPendingFixture is an update whose migration was applied and whose start was prepared (and, when reserved,
// reserved) before the runner became unreachable: the scheduler gave up and ended it for review, with the start
// effect still open.
func startPendingFixture(t *testing.T, reserved bool) *settleFixture {
	t.Helper()
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	start, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	startEndpoint := platformUpdateStartEndpointFixture()
	intent, err := s.preparePlatformUpdateStart(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if reserved {
		observation := platformUpdateStartReservationFixture(intent, startEndpoint)
		reservation, ok := validPlatformUpdateStartObservation(observation, intent, startEndpoint)
		if !ok {
			t.Fatal("reservation refused")
		}
		if _, err := s.commitPlatformUpdateStartReservation(context.Background(), start, intent, startEndpoint, observation, reservation); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finishPlatformManagedUpdateForReview(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, endpoint), nil
	}
	fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		state := "stopped"
		if i.command.Stage == "remove_code" {
			state = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, endpoint, state), nil
	}
	return &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
}

// makeStartWindowPassed ages the end of the update past every window a command for its start could have been signed
// for, which is what makes an absence definite.
func makeStartWindowPassed(t *testing.T, f *settleFixture) {
	t.Helper()
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=completed_ms-3600000 WHERE id=?`, f.op)
}

func TestSettlementReadsAPreparedStartAndNeverSendsIt(t *testing.T) {
	f := startPendingFixture(t, false)
	reads, sends := 0, 0
	f.s.platformReadStartReceipt = func(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		reads++
		if reads <= 2 {
			return nil, errors.New("runner unreachable")
		}
		return nil, errPlatformStartAbsent
	}
	if f.step(t) || effectState(t, f.s, f.op, "start", 1) != "prepared" {
		t.Fatal("an unreachable runner settled the start")
	}
	f.step(t)
	if f.settleState(t) == "settled" || reads != 2 {
		t.Fatalf("an unreachable runner was read past: reads=%d", reads)
	}
	// The runner is back and says it never admitted the start; the window has not passed, so nothing is concluded.
	if f.step(t) || effectState(t, f.s, f.op, "start", 1) != "prepared" {
		t.Fatal("an absence inside the signed window was acted on")
	}
	makeStartWindowPassed(t, f)
	for range 4 {
		f.step(t)
	}
	if effectState(t, f.s, f.op, "start", 1) != "reverted" || f.settleState(t) != "settled" {
		t.Fatalf("a prepared start the runner never admitted did not settle: start=%s settle=%s", effectState(t, f.s, f.op, "start", 1), f.settleState(t))
	}
	if f.fake.count("execute-start") != 0 || sends != 0 {
		t.Fatal("settlement sent the start")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='candidate'`, f.lease.installationID) != 0 {
		t.Fatal("the candidate of a start that never ran was left behind")
	}
}

func TestSettlementAppliedStartTakesTheRemoveTargetPath(t *testing.T) {
	f := startPendingFixture(t, true)
	f.s.platformReadStartReceipt = func(_ context.Context, endpoint platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if i.reservation == nil {
			t.Error("the read did not carry the recorded reservation")
		}
		return platformUpdateStartRunningFixture(i, *i.reservation, f.endpoint), nil
	}
	for range 6 {
		f.step(t)
	}
	if effectState(t, f.s, f.op, "start", 1) != "applied" || f.fake.count("remove-stop") != 1 || f.fake.count("remove-remove_code") != 1 || f.settleState(t) != "settled" {
		t.Fatalf("an applied start was not removed before the restore: %s %v", f.settleState(t), f.fake.calls)
	}
}

func TestSettlementAbsentStartWithAReservationIsNotReverted(t *testing.T) {
	f := startPendingFixture(t, true)
	f.s.platformReadStartReceipt = func(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		return nil, errPlatformStartAbsent
	}
	makeStartWindowPassed(t, f)
	for range 3 {
		f.step(t)
	}
	if effectState(t, f.s, f.op, "start", 1) != "prepared" || f.fake.count("execute-restore") != 0 {
		t.Fatal("a start the runner reserved was treated as never admitted: its journal lost it, and nothing may be undone on a guess")
	}
}

func TestSettlementStartReadAnswerMustBeTheStartsOwn(t *testing.T) {
	f := startPendingFixture(t, true)
	f.s.platformReadStartReceipt = func(_ context.Context, endpoint platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		o := platformUpdateStartRunningFixture(i, *i.reservation, f.endpoint)
		o.running.Claim.EffectID = platformFixtureID(8830)
		return o, nil
	}
	if _, err := f.s.settlePlatformMigration(context.Background(), f.endpoint, f.op); err == nil || effectState(t, f.s, f.op, "start", 1) != "prepared" {
		t.Fatalf("an answer for another effect settled the start: %v", err)
	}
}

var _ = journal.UpdateStartReservation{}

// heldStartRunner scripts the runner's answers about a start it holds without a running receipt: what the start read
// says as the settlement proceeds, and what a settlement of an orphan does.
type heldStartRunner struct {
	f        *settleFixture
	reads    []string
	settled  int
	read     func(h *heldStartRunner, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error)
	removals []string
}

func newHeldStartRunner(f *settleFixture, read func(h *heldStartRunner, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error)) *heldStartRunner {
	h := &heldStartRunner{f: f, read: read}
	f.s.platformReadStartReceipt = func(_ context.Context, _ platformRuntimeEndpoint, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		return h.read(h, i)
	}
	f.s.platformSettleStart = platformFakeSettleStart(f.fake, func(_ context.Context, _ platformRuntimeEndpoint, intent platformSettleStartIntent) (journal.SettleUpdateStartReceipt, error) {
		h.settled++
		verified, err := control.VerifySettleUpdateStart(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
		if err != nil || verified.Command().StartReadSignature == nil {
			return journal.SettleUpdateStartReceipt{}, errors.New("the settlement was not a valid command over a read-signed start")
		}
		return journal.SettleUpdateStartReceipt{}, nil
	})
	return h
}

func held(state string) error { return &journal.UpdateStartHeldError{State: state} }

// scriptRemoval makes the runner remove a target and records how each removal is bound.
func (h *heldStartRunner) scriptRemoval(t *testing.T, reservedOnly bool) {
	h.f.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		if (i.command.RunningReceiptSHA256 == control.ReservedStartReceiptSHA256) != reservedOnly || (i.start.running == nil) != reservedOnly {
			t.Errorf("the removal is bound the wrong way (reserved only: %v): %+v", reservedOnly, i.command)
		}
		h.removals = append(h.removals, i.command.Stage)
		result := "stopped"
		if i.command.Stage == "remove_code" {
			result = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, h.f.endpoint, result), nil
	}
}

func (h *heldStartRunner) retryLaunches(t *testing.T) {
	t.Helper()
	f := h.f
	f.fake.calls = nil
	hh := newMigrationHTTP(t, f)
	reportManagedRuntime(f, lastPlatformUpdateFixture.target.Profile)
	if hh.launch() == 200 {
		t.Fatal("an application launched while its failed update was unresolved")
	}
	operation := hh.retryOverHTTP(platformFixtureID(7680))
	lease := driveRetryToMigrate(t, f, operation)
	hh.activateMigrated(t, lease, "migrated")
	if got := hh.launch(); got != 200 {
		t.Fatalf("launch after the retry answered %d", got)
	}
}

func (h *heldStartRunner) settle(t *testing.T, ticks int) {
	t.Helper()
	for range ticks {
		h.f.step(t)
	}
	if h.f.settleState(t) != "settled" {
		t.Fatalf("a held start did not settle: %s %v", h.f.settleState(t), h.f.fake.calls)
	}
	if h.f.fake.count("execute-start") != 0 || h.f.fake.count("execute-restore") != 1 {
		t.Fatalf("settlement sent the start or skipped the restore: %v", h.f.fake.calls)
	}
}

// Reserved: the runner holds a pending target and no claim was ever made. Once the removal stopped it the runner
// reads the start as never run, which does not end the removal: the target is still to be removed.
func TestSettlementOfAReservedStartThatNeverRanEndsSettledAndALaunchingRetry(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if len(h.removals) > 0 {
			return nil, held("never_ran")
		}
		return nil, held("reserved")
	})
	h.scriptRemoval(t, true)
	h.settle(t, 8)
	if len(h.removals) != 2 || h.removals[0] != "stop" || h.removals[1] != "remove_code" || h.settled != 0 {
		t.Fatalf("a reserved start was not removed by the reserved-only removal alone: %v settlements=%d", h.removals, h.settled)
	}
	if effectState(t, f.s, f.op, "start", 1) != "reverted" || effectState(t, f.s, f.op, "settle", 4) != "applied" || effectState(t, f.s, f.op, "settle", 5) != "applied" ||
		effectState(t, f.s, f.op, "settle", 6) != "applied" || effectState(t, f.s, f.op, "settle", 1) != "" {
		t.Fatal("the start was not closed as never run after its target was removed and its namespace swept")
	}
	h.retryLaunches(t)
}

// Orphaned and nothing exists: the runner settles the start as not run. Nothing is removed, the checkpoint is
// restored, and a retry runs to a launching application.
func TestSettlementOfAnOrphanedStartWithNoContainerGoesThroughTheRunnersSettlement(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if h.settled > 0 {
			return nil, held("never_ran")
		}
		return nil, held("orphaned")
	})
	h.scriptRemoval(t, true)
	h.settle(t, 8)
	if h.settled != 1 || len(h.removals) != 0 {
		t.Fatalf("an orphan was removed as if it were reserved: settlements=%d removals=%v", h.settled, h.removals)
	}
	if effectState(t, f.s, f.op, "start", 1) != "reverted" {
		t.Fatal("the start was not closed as never run")
	}
	h.retryLaunches(t)
}

// Orphaned and a container exists: the runner settles the start as applied, the read then shows its running receipt,
// and the ordinary removal, bound to that receipt, takes the target away before the checkpoint is restored.
func TestSettlementOfAnOrphanedStartWithAContainerRemovesItLikeARunningTarget(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if h.settled == 0 {
			return nil, held("orphaned")
		}
		return platformUpdateStartRunningFixture(i, *i.reservation, f.endpoint), nil
	})
	h.scriptRemoval(t, false)
	h.settle(t, 8)
	if h.settled != 1 || len(h.removals) != 2 {
		t.Fatalf("settlements=%d removals=%v", h.settled, h.removals)
	}
	if effectState(t, f.s, f.op, "start", 1) != "applied" {
		t.Fatal("the settled start is not applied")
	}
	h.retryLaunches(t)
}

// The start gets its running receipt after the reserved removal began being recorded: the next read shows it, and
// settlement goes the ordinary way instead of removing a reservation that is no longer one.
func TestSettlementFollowsAStartThatBecameRunningAfterItWasHeld(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		h.reads = append(h.reads, "read")
		if len(h.reads) == 1 {
			return nil, held("reserved")
		}
		return platformUpdateStartRunningFixture(i, *i.reservation, f.endpoint), nil
	})
	h.scriptRemoval(t, false)
	h.settle(t, 8)
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='settle' AND effect_number=3 AND kind='held-start'`, f.op) != 1 {
		t.Fatal("the held start was not recorded")
	}
	if len(h.removals) != 2 || h.settled != 0 || effectState(t, f.s, f.op, "start", 1) != "applied" {
		t.Fatalf("a start that began running was not removed as running: removals=%v settlements=%d", h.removals, h.settled)
	}
	h.retryLaunches(t)
}

// A start the runner may be executing is waited for: nothing is removed and nothing is recorded.
func TestSettlementWaitsForAStartThatMayBeExecuting(t *testing.T) {
	f := startPendingFixture(t, true)
	h := newHeldStartRunner(f, func(*heldStartRunner, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		return nil, held("pending")
	})
	for range 3 {
		if f.step(t) {
			t.Fatal("a pending start made progress")
		}
	}
	if f.fake.count("remove-stop") != 0 || h.settled != 0 || scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='settle'`, f.op) != 0 {
		t.Fatal("a start that may be executing was touched")
	}
}

// The settlement reads a finished update's start for a read: no execute-capable start command is signed after the
// update ended, and the signature made is not one for an execute.
func TestSettlementStartReadSignsOnlyTheReadPurpose(t *testing.T) {
	f := startPendingFixture(t, true)
	lease, err := platformSettlementLease(f.s.st.poolExec(), f.op)
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.s.signPlatformSettlementStartRead(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.intent.signature) != 0 {
		t.Fatal("an execute signature was made for a start that finished")
	}
	now := time.Now().UnixMilli()
	if _, err := control.VerifyUpdateStartRead(read.intent.raw, read.intent.readSignature, read.intent.authority, now); err != nil {
		t.Fatalf("the read signature is not valid: %v", err)
	}
	if _, err := control.VerifyUpdateStart(read.intent.raw, read.intent.readSignature, read.intent.authority, now); err == nil {
		t.Fatal("the read signature verifies as an execute")
	}
	// The command that settles an orphan carries the start under that read signature, never an execute one.
	intent, err := f.s.signPlatformSettlementOrphan(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := control.VerifySettleUpdateStart(intent.raw, intent.signature, intent.authority, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	command := verified.Command()
	if _, err := control.VerifyUpdateStart(command.StartRaw, command.StartReadSignature, intent.authority, time.Now().UnixMilli()); err == nil {
		t.Fatal("the settlement carries an execute-capable start")
	}
}

// The window a command for the start was signed for, the margin, and the time a container create the runner gave up on
// may still be carried out all have to pass before anything is done to a start the runner holds: until then a claim may
// arrive, and a reserved start is not yet known to stay reserved.
func TestSettlementDoesNotActOnAHeldStartBeforeItsWindowAndCreateGraceHavePassed(t *testing.T) {
	if platformCreateGraceMS < driver.DockerRequestTimeout.Milliseconds() {
		t.Fatalf("the create grace %dms is shorter than the driver's Docker request timeout", platformCreateGraceMS)
	}
	for _, state := range []string{"reserved", "orphaned", "never_ran"} {
		t.Run(state, func(t *testing.T) {
			f := startPendingFixture(t, true)
			h := newHeldStartRunner(f, func(*heldStartRunner, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
				return nil, held(state)
			})
			h.scriptRemoval(t, true)
			now := time.Now().UnixMilli()
			// Past the window and the margin, but inside the create grace and the clock-skew allowance.
			total := platformSigningSlackMS + platformStartCommandWindowMS + platformImportAbsenceMarginMS + platformCreateGraceMS + control.CommandClockSkewMS
			inside := now - total + 2000
			mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=? WHERE id=?`, inside, f.op)
			for range 3 {
				if f.step(t) {
					t.Fatalf("a %s start was acted on before the create grace passed", state)
				}
			}
			if len(h.removals) != 0 || h.settled != 0 || f.fake.count("sweep") != 0 ||
				scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='settle'`, f.op) != 0 {
				t.Fatalf("a %s start was touched early: removals=%v settlements=%d", state, h.removals, h.settled)
			}
			// Past the grace it is acted on.
			mustExec(t, f.s.st, `UPDATE app_platform_operations SET completed_ms=? WHERE id=?`, inside-4000, f.op)
			if !f.step(t) {
				t.Fatalf("a %s start was not acted on after the create grace passed", state)
			}
		})
	}
}

// A claim that arrives after the reserved read turns the start into an orphan. The removal the Portal began is
// refused by the runner, is closed as not applied, and the runner's own settlement of the orphan ends the start: its
// target is already gone, so the reserved stop is never retried and the update settles.
func TestSettlementOfAReservedStartClaimedAfterTheReadIsSettledAsAnOrphan(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	claimed := false
	h := newHeldStartRunner(f, func(h *heldStartRunner, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		switch {
		case h.settled > 0:
			return nil, held("never_ran")
		case claimed:
			return nil, held("orphaned")
		}
		return nil, held("reserved")
	})
	f.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		if i.command.Stage != "stop" || i.command.RunningReceiptSHA256 != control.ReservedStartReceiptSHA256 {
			t.Errorf("a removal was sent after the claim: %+v", i.command)
		}
		h.removals = append(h.removals, i.command.Stage)
		claimed = true
		return nil, errors.New("the start is not reserved")
	}
	h.settle(t, 12)
	if len(h.removals) != 1 || h.settled != 1 {
		t.Fatalf("removals=%v settlements=%d", h.removals, h.settled)
	}
	if effectState(t, f.s, f.op, "settle", 4) != "reverted" || effectState(t, f.s, f.op, "settle", 5) != "" ||
		effectState(t, f.s, f.op, "start", 1) != "reverted" || effectState(t, f.s, f.op, "settle", 6) != "applied" {
		t.Fatal("the refused removal was not closed as not applied, or the start was not settled")
	}
	h.retryLaunches(t)
}

// A claim that arrives after the Portal wrote its reserved stop makes the start run. The stop is closed as not applied
// and the running target is removed by the ordinary removal, a new effect bound to the running receipt.
func TestSettlementOfAReservedStopThatTheStartOutranIsClosedAndTheTargetRemovedOrdinarily(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	running := false
	h := newHeldStartRunner(f, func(h *heldStartRunner, i platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if running {
			return platformUpdateStartRunningFixture(i, *i.reservation, f.endpoint), nil
		}
		return nil, held("reserved")
	})
	var stopDigest string
	f.fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		if i.command.RunningReceiptSHA256 == control.ReservedStartReceiptSHA256 {
			// The runner refuses the reserved stop: the start was claimed and ran meanwhile.
			running = true
			stopDigest = i.command.PayloadSHA256
			return nil, errors.New("the start is not reserved")
		}
		h.removals = append(h.removals, i.command.Stage)
		if i.command.PayloadSHA256 == stopDigest {
			t.Error("the ordinary removal reused the digest of the reserved one")
		}
		result := "stopped"
		if i.command.Stage == "remove_code" {
			result = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, f.endpoint, result), nil
	}
	h.settle(t, 12)
	if len(h.removals) != 2 || h.settled != 0 {
		t.Fatalf("removals=%v settlements=%d", h.removals, h.settled)
	}
	if effectState(t, f.s, f.op, "settle", 4) != "reverted" || effectState(t, f.s, f.op, "settle", 1) != "applied" ||
		effectState(t, f.s, f.op, "settle", 2) != "applied" || effectState(t, f.s, f.op, "start", 1) != "applied" {
		t.Fatal("the reserved stop was not closed, or the ordinary removal did not follow")
	}
	if scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='settle' AND effect_number=4`, f.op) ==
		scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='settle' AND effect_number=1`, f.op) {
		t.Fatal("the ordinary removal is the reserved effect")
	}
	h.retryLaunches(t)
}

// The checkpoint is not restored while a container of the namespace may survive: the sweep is an effect of its own,
// sent again under the same identity until the runner answers it.
func TestSettlementSweepsTheNamespaceBeforeTheRestore(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if len(h.removals) > 1 && h.f.fake.count("sweep") > 0 {
			return nil, held("never_ran")
		}
		return nil, held("reserved")
	})
	h.scriptRemoval(t, true)
	attempts := 0
	var first string
	f.s.platformSettleStart = platformFakeSettleStart(f.fake, nil)
	inner := f.s.platformSettleStart
	f.s.platformSettleStart = func(ctx context.Context, e platformRuntimeEndpoint, intent platformSettleStartIntent) (journal.SettleUpdateStartReceipt, error) {
		if intent.mode == control.SettleModeSweep {
			attempts++
			if first == "" {
				first = intent.effectID + intent.digest
			} else if first != intent.effectID+intent.digest {
				t.Error("a sweep sent again is not the same effect")
			}
			if attempts < 3 {
				return journal.SettleUpdateStartReceipt{}, errors.New("the runner could not list the namespace")
			}
		}
		return inner(ctx, e, intent)
	}
	for range 12 {
		f.step(t)
		if attempts > 0 && attempts < 3 && f.fake.count("execute-restore") != 0 {
			t.Fatal("the checkpoint was restored before the namespace was swept")
		}
	}
	if f.settleState(t) != "settled" || attempts != 3 || f.fake.count("sweep") != 1 {
		t.Fatalf("state=%s attempts=%d calls=%v", f.settleState(t), attempts, f.fake.calls)
	}
	if effectState(t, f.s, f.op, "settle", 6) != "applied" {
		t.Fatal("the sweep was not recorded")
	}
}

// The runner judges a command by its own clock, and does not bound how far that clock lags the Portal's. A runner that
// lags by the skew allowed for accepts the last command signed for the start until the Portal's own expiry plus that
// skew, so the time after which the Portal acts on the start is later than that, by the absence margin and the create
// grace as well.
func TestSettlementWindowAllowsForARunnerClockThatLags(t *testing.T) {
	f := startPendingFixture(t, true)
	lease, err := platformSettlementLease(f.s.st.poolExec(), f.op)
	if err != nil {
		t.Fatal(err)
	}
	read, err := f.s.signPlatformSettlementStartRead(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	completed := scalar[int64](t, f.s.st, `SELECT completed_ms FROM app_platform_operations WHERE id=?`, f.op)
	// The latest a start command can have been signed: issued a second past the clock of the update's last moment.
	lastExpiry := completed + platformSigningSlackMS + platformStartCommandWindowMS
	lagging := lastExpiry + control.CommandClockSkewMS
	if read.definiteAfter < lagging+platformImportAbsenceMarginMS+platformCreateGraceMS {
		t.Fatalf("the Portal would act %dms before a command could still be accepted by a runner that lags", lagging+platformImportAbsenceMarginMS+platformCreateGraceMS-read.definiteAfter)
	}
}

// A container of the namespace that is not the runner's blocks the sweep with a reason the settlement projection
// shows. Nothing is skipped and the checkpoint is not restored; once the runner can sweep again the settlement goes on.
func TestSettlementShowsWhyASweepIsBlockedByAForeignContainer(t *testing.T) {
	platformWritableUpdateFixtures(t)
	f := startPendingFixture(t, true)
	makeStartWindowPassed(t, f)
	h := newHeldStartRunner(f, func(h *heldStartRunner, _ platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
		if len(h.removals) > 1 && h.f.fake.count("sweep") > 0 {
			return nil, held("never_ran")
		}
		return nil, held("reserved")
	})
	h.scriptRemoval(t, true)
	blocked := true
	inner := platformFakeSettleStart(f.fake, nil)
	f.s.platformSettleStart = func(ctx context.Context, e platformRuntimeEndpoint, intent platformSettleStartIntent) (journal.SettleUpdateStartReceipt, error) {
		if intent.mode == control.SettleModeSweep && blocked {
			receipt, err := platformFakeSweepReceipt(intent, 0)
			receipt.Outcome, receipt.Reason, receipt.Removed = "blocked", journal.SettleReasonForeignContainer, ""
			return receipt, err
		}
		return inner(ctx, e, intent)
	}
	for range 8 {
		f.step(t)
	}
	if f.fake.count("execute-restore") != 0 {
		t.Fatal("the checkpoint was restored beside a container nothing accounts for")
	}
	state, err := platformMigrationSettlementState(f.s.st.poolExec(), f.op)
	if err != nil || state != "blocked" {
		t.Fatalf("a blocked sweep projects as %q %v", state, err)
	}
	reason, err := platformMigrationSettlementReason(f.s.st.poolExec(), f.op)
	if err != nil || reason != journal.SettleReasonForeignContainer {
		t.Fatalf("the reason is %q %v", reason, err)
	}
	projection, err := platformMigrationRecoveryProjection(f.s.st.poolExec(), f.lease.installationID)
	if err != nil || projection == nil || projection.State != "blocked" || projection.Reason != "foreign_container" {
		t.Fatalf("the projection does not show why: %+v %v", projection, err)
	}
	blocked = false
	h.settle(t, 10)
	if effectState(t, f.s, f.op, "settle", 6) != "applied" {
		t.Fatal("the sweep did not go on once it could")
	}
}
