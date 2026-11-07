package app

import (
	"context"
	"errors"
	"testing"

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
