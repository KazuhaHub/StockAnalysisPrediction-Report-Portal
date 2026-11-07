package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A lost acknowledgement is resolved on a later tick by reading the effect back. What comes back is the receipt
// the runner settled earlier, observed before the read was signed; it settles the effect all the same.

func TestMigrateReadBackOfEarlierSettledReceiptSettles(t *testing.T) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		_ = platformMigrateAnswers(i, endpoint, "migrated", "") // the job finished and was recorded
		return nil, errors.New("acknowledgement lost")
	}
	reads := 0
	fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		reads++
		if reads == 1 {
			return nil, errors.New("runner restarting")
		}
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); err == nil {
		t.Fatal("a lost acknowledgement with an unreadable runner settled the effect")
	}
	time.Sleep(5 * time.Millisecond)
	next, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart || effectState(t, s, lease.id, "migrate", 2) != "applied" {
		t.Fatalf("the earlier receipt did not settle the migration: %v %s", err, effectState(t, s, lease.id, "migrate", 2))
	}
	if fake.count("execute-migrate") != 1 {
		t.Fatal("a migration whose receipt exists was sent again")
	}
}

func TestSettlementDiscardReadBackOfEarlierReceiptSettles(t *testing.T) {
	f := settleAfterEnd(t, false, orphanedScript)
	f.fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateStatusAnswer(i, f.endpoint, "orphaned"), nil
	}
	f.step(t) // orphaned: the discard is opened
	f.fake.readDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	f.fake.executeDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) {
		_ = discardAnswer(i, f.endpoint) // the runner discarded it and recorded the receipt
		return nil, errors.New("acknowledgement lost")
	}
	f.step(t)
	if effectState(t, f.s, f.op, "migrate", 3) != "prepared" {
		t.Fatal("a lost discard acknowledgement was taken as done")
	}
	time.Sleep(5 * time.Millisecond)
	f.fake.readDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) {
		return discardAnswer(i, f.endpoint), nil
	}
	if !f.step(t) || effectState(t, f.s, f.op, "migrate", 3) != "applied" || effectState(t, f.s, f.op, "migrate", 2) != "reverted" {
		t.Fatal("the earlier discard receipt did not settle the discard")
	}
	if f.fake.count("execute-discard") != 1 {
		t.Fatal("a discard whose receipt exists was sent again")
	}
}

func TestSettlementRestoreReadBackOfEarlierReceiptSettles(t *testing.T) {
	f := settleAfterEnd(t, false, failedScript)
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		_ = restoreAnswer(i, f.endpoint) // the restore finished and was recorded
		return nil, errors.New("acknowledgement lost")
	}
	f.step(t)
	if effectState(t, f.s, f.op, "restore_data", 1) != "prepared" {
		t.Fatal("a lost restore acknowledgement was taken as done")
	}
	time.Sleep(5 * time.Millisecond)
	f.fake.readRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	if !f.step(t) || effectState(t, f.s, f.op, "restore_data", 1) != "applied" {
		t.Fatal("the earlier restore receipt did not settle the restore")
	}
	f.step(t)
	if f.settleState(t) != "settled" || f.fake.count("execute-restore") != 1 {
		t.Fatalf("settlement after the read-back: %s %v", f.settleState(t), f.fake.calls)
	}
}

// The runner refuses a discard once the migration is no longer unknown. A discard it never admitted reads back as a
// typed absence, which is definite only after the signed window; then the portal closes the discard as not applied,
// reads the migration again, and settles from its definite state instead of sending the discard forever.
func testSettlementDiscardRefusedAfterMigrationSettled(t *testing.T, state, outcome string) {
	f := settleAfterEnd(t, false, orphanedScript)
	f.fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateStatusAnswer(i, f.endpoint, "orphaned"), nil
	}
	f.step(t) // orphaned: the discard is opened
	sends := 0
	f.fake.executeDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) {
		sends++
		return nil, errors.New("the runner refused: the migration is no longer unknown")
	}
	f.fake.readDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	f.step(t)
	if sends != 1 {
		t.Fatalf("the discard was not sent once: %d", sends)
	}
	// Meanwhile the job finished on its own.
	f.fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateAnswers(i, f.endpoint, state, outcome), nil
	}
	if f.step(t) && sends != 1 {
		t.Fatal("an absence inside the signed window was acted on")
	}
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=3`,
		`{"signed_until_ms":`+past+`}`, f.op)
	if !f.step(t) || sends != 1 {
		t.Fatalf("a final absence sent the discard again instead of reading the migration: sends=%d", sends)
	}
	if effectState(t, f.s, f.op, "migrate", 3) != "reverted" {
		t.Fatalf("the discard was not closed as not applied: %s", effectState(t, f.s, f.op, "migrate", 3))
	}
	want := map[string]string{"migrated": "applied", "failed": "reverted"}[state]
	if effectState(t, f.s, f.op, "migrate", 2) != want {
		t.Fatalf("the migration is %s, want %s", effectState(t, f.s, f.op, "migrate", 2), want)
	}
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return restoreAnswer(i, f.endpoint), nil
	}
	f.step(t)
	f.step(t)
	if f.settleState(t) != "settled" {
		t.Fatalf("settlement did not continue from the migration's state: %s %v", f.settleState(t), f.fake.calls)
	}
}

func TestSettlementDiscardRefusedAfterMigrationRevertedSettles(t *testing.T) {
	testSettlementDiscardRefusedAfterMigrationSettled(t, "failed", "exit_nonzero")
}

func TestSettlementDiscardRefusedAfterMigrationAppliedSettles(t *testing.T) {
	testSettlementDiscardRefusedAfterMigrationSettled(t, "migrated", "")
}

// A status read back for the settling restore is held to the same checks as migrate and discard: it must answer
// this effect, this payload and this operation, and be fresh. An answer for anything else is evidence of a fault,
// never a reason to send the restore again.
func TestSettlementRestoreStatusMustAnswerTheEffect(t *testing.T) {
	for name, mutate := range map[string]func(*platformFakeObservation){
		"another effect": func(o *platformFakeObservation) { o.status.EffectID = platformFixtureID(8801) },
		"another payload": func(o *platformFakeObservation) {
			o.status.PayloadSHA256 = platformDigest([]byte("another payload"))
		},
		"another operation": func(o *platformFakeObservation) { o.status.OperationID = platformFixtureID(8802) },
		"another kind":      func(o *platformFakeObservation) { o.status.Kind = "migrate-data" },
		"stale": func(o *platformFakeObservation) {
			o.status.ObservedMS = strconv.FormatInt(time.Now().Add(-time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := settleAfterEnd(t, false, failedScript)
			sends := 0
			f.fake.executeRestore = func(platformRestoreIntent) (platformSettlingRestoreObservation, error) {
				sends++
				return nil, errors.New("acknowledgement lost")
			}
			f.step(t)
			f.fake.readRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
				o := restoreStatus(i, f.endpoint, "orphaned").(platformFakeObservation)
				mutate(&o)
				return o, nil
			}
			if _, err := f.s.settlePlatformMigration(context.Background(), f.endpoint, f.op); err == nil || sends != 1 {
				t.Fatalf("a status that does not answer the restore was acted on: sends=%d err=%v", sends, err)
			}
		})
	}
}

// A migrate-data command lost in transit is read back as a typed absence. The absence is definite only once the
// window the command was signed for has passed; then the same effect is sent again, and the update goes on.
func TestMigrateCommandLostInTransitIsSentAgainAfterItsWindow(t *testing.T) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	var effects []string
	fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		effects = append(effects, i.effectID)
		if len(effects) == 1 {
			return nil, errors.New("lost before it reached the runner")
		}
		return platformMigrateAnswers(i, endpoint, "migrated", ""), nil
	}
	fake.readMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationBusy) {
		t.Fatalf("a command that may still arrive was not waited for: %v", err)
	}
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationBusy) || len(effects) != 1 {
		t.Fatalf("an absence inside the signed window was acted on: sends=%d err=%v", len(effects), err)
	}
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=2`,
		`{"signed_until_ms":`+past+`}`, lease.id)
	next, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint)
	if err != nil || next.snapshot.Stage != appplatform.StageStart {
		t.Fatalf("a final absence did not let the command be sent again: %v", err)
	}
	if len(effects) != 2 || effects[0] != effects[1] || effectState(t, s, lease.id, "migrate", 2) != "applied" {
		t.Fatalf("the resend was not the same effect: %v", effects)
	}
}

// A reply cannot say it was observed after it was received. The fake runner stamped a reply's observation time after
// it had taken the time the reply was received, so whenever the millisecond ticked over between the two the portal
// rightly refused the reply as coming from the future: a failure of roughly one run in a few thousand, which only a
// long suite or a repeated run ever met. The fake now receives a reply when it is asked, never before what it says.
func TestFakeRunnerRepliesAreNeverObservedAfterTheyAreReceived(t *testing.T) {
	f := settleAfterEnd(t, false, orphanedScript)
	intent, err := f.s.signPlatformSettlementMigrate(context.Background(), mustSettlementLease(t, f))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300000 {
		answer := platformMigrateStatusAnswer(intent, f.endpoint, "orphaned")
		status, err := answer.MigrationStatus()
		if err != nil {
			t.Fatal(err)
		}
		observed, _ := strconv.ParseInt(status.ObservedMS, 10, 64)
		if received := answer.ReceivedAt().UnixMilli(); observed > received {
			t.Fatalf("reply %d was observed at %d, after it was received at %d", i, observed, received)
		}
	}
}

func mustSettlementLease(t *testing.T, f *settleFixture) platformOperationLease {
	t.Helper()
	lease, err := platformSettlementLease(f.s.st.poolExec(), f.op)
	if err != nil {
		t.Fatal(err)
	}
	return lease
}
