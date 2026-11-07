package app

import (
	"context"
	"errors"
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
	f.fake.readDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) { return nil, errPlatformMigrationAbsent }
	f.fake.executeDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) {
		_ = discardAnswer(i, f.endpoint) // the runner discarded it and recorded the receipt
		return nil, errors.New("acknowledgement lost")
	}
	f.step(t)
	if effectState(t, f.s, f.op, "migrate", 3) != "prepared" {
		t.Fatal("a lost discard acknowledgement was taken as done")
	}
	time.Sleep(5 * time.Millisecond)
	f.fake.readDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) { return discardAnswer(i, f.endpoint), nil }
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
