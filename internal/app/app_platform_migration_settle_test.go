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

type settleFixture struct {
	s        *Server
	lease    platformOperationLease
	endpoint platformRuntimeEndpoint
	fake     *platformFakeMigrationRunner
	op       string
}

func (f *settleFixture) step(t *testing.T) bool {
	t.Helper()
	done, err := f.s.settlePlatformMigration(context.Background(), f.endpoint, f.op)
	if err != nil {
		t.Fatal(err)
	}
	return done
}

func (f *settleFixture) settleState(t *testing.T) string {
	t.Helper()
	state, err := platformMigrationSettlementState(f.s.st.poolExec(), f.op)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func restoreAnswer(i platformRestoreIntent, endpoint platformRuntimeEndpoint) platformSettlingRestoreObservation {
	o := platformRestoreObservationFixture(i, endpoint)
	obs := platformFakeProof(i.authority, endpoint, i.raw)
	receipt := o.receipt
	receipt.ObservedMS = platformFakeFirstObserved(i.effectID)
	receipt.SettledMS = receipt.ObservedMS
	obs.restore = &receipt
	return obs
}

// discardAnswer is the receipt of a discard, with the time the runner first settled it.
func discardAnswer(i platformDiscardIntent, endpoint platformRuntimeEndpoint) platformDiscardObservation {
	o := platformFakeProof(i.authority, endpoint, i.raw)
	c := i.command
	at := platformFakeFirstObserved(c.EffectID)
	o.discard = &journal.DiscardMigrationReceipt{
		Format: "portal.runner.discard-migration-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "migrate", EffectID: c.EffectID,
		PayloadSHA256: c.PayloadSHA256, NamespaceID: c.Payload.NamespaceID, ResourceID: c.Payload.ResourceID, MigrateEffectID: c.Payload.MigrateEffectID,
		MigratePayloadSHA256: c.Payload.MigratePayloadSHA256, MigrateReceiptSHA256: platformDigest([]byte("migrate-receipt")), State: "discarded",
		ObservedMS: at, SettledMS: at,
	}
	return o
}

func restoreStatus(i platformRestoreIntent, endpoint platformRuntimeEndpoint, state string) platformSettlingRestoreObservation {
	obs := platformFakeProof(i.authority, endpoint, i.raw)
	c := i.command
	obs.status = platformMigrateStatusFor(control.CommandHeader{
		DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
		TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, Kind: "restore",
	}, state)
	return obs
}

// a migration that failed for one of the known reasons or was left unknown, ended for review
func settleAfterEnd(t *testing.T, pg bool, script func(f *platformFakeMigrationRunner, endpoint platformRuntimeEndpoint)) *settleFixture {
	t.Helper()
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
	script(fake, endpoint)
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	fake.calls = nil
	fake.executeMigrate, fake.readMigrate = nil, nil
	return &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
}

func orphanedScript(f *platformFakeMigrationRunner, endpoint platformRuntimeEndpoint) {
	f.executeMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errors.New("runner killed")
	}
	f.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return platformMigrateStatusAnswer(i, endpoint, "orphaned"), nil
	}
}

// C3 and C4: an unknown migration is discarded once the runner reports it orphaned, and only then is the
// checkpoint restored, with no target ever activated. Settlement then offers both choices.
func testSettleUnknownDiscardsThenRestores(t *testing.T, pg bool) {
	f := settleAfterEnd(t, pg, orphanedScript)
	if f.settleState(t) != "settling" {
		t.Fatalf("an unknown migration is %s", f.settleState(t))
	}
	pending := true
	f.fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		if pending {
			pending = false // C4: the job may still be writing
			return platformMigrateStatusAnswer(i, f.endpoint, "pending"), nil
		}
		return platformMigrateStatusAnswer(i, f.endpoint, "orphaned"), nil
	}
	if f.step(t) {
		t.Fatal("a migration the runner still holds made progress")
	}
	if !f.step(t) || effectState(t, f.s, f.op, "migrate", 3) != "prepared" || effectState(t, f.s, f.op, "migrate", 2) != "unknown" {
		t.Fatal("an orphaned migration did not open its discard")
	}
	f.fake.readDiscard = func(platformDiscardIntent) (platformDiscardObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	f.fake.executeDiscard = func(i platformDiscardIntent) (platformDiscardObservation, error) {
		if f.fake.count("execute-restore") != 0 {
			t.Error("the checkpoint was restored before the migration was discarded")
		}
		o := platformFakeProof(i.authority, f.endpoint, i.raw)
		c := i.command
		now := strconv.FormatInt(time.Now().UTC().UnixMilli(), 10)
		o.discard = &journal.DiscardMigrationReceipt{
			Format: "portal.runner.discard-migration-receipt/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
			InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: "migrate", EffectID: c.EffectID,
			PayloadSHA256: c.PayloadSHA256, NamespaceID: c.Payload.NamespaceID, ResourceID: c.Payload.ResourceID, MigrateEffectID: c.Payload.MigrateEffectID,
			MigratePayloadSHA256: c.Payload.MigratePayloadSHA256, MigrateReceiptSHA256: platformDigest([]byte("migrate-receipt")), State: "discarded",
			ObservedMS: now, SettledMS: now,
		}
		return o, nil
	}
	if !f.step(t) || effectState(t, f.s, f.op, "migrate", 3) != "applied" || effectState(t, f.s, f.op, "migrate", 2) != "reverted" {
		t.Fatal("the discard did not revert the migration")
	}
	var restoreEffect string
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		restoreEffect = i.effectID
		if i.command.OperationID != f.op || i.command.Stage != "restore_data" || i.command.EffectNumber != "1" {
			t.Errorf("restore is not on the failed update's slot: %s %s %s", i.command.OperationID, i.command.Stage, i.command.EffectNumber)
		}
		return restoreAnswer(i, f.endpoint), nil
	}
	if !f.step(t) || effectState(t, f.s, f.op, "restore_data", 1) != "applied" || restoreEffect == "" ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_recovery_sets WHERE operation_id=?`, f.op) != "verified" {
		t.Fatal("the checkpoint was not restored")
	}
	if f.settleState(t) == "settled" {
		t.Fatal("settled before the marker")
	}
	if !f.step(t) || f.settleState(t) != "settled" {
		t.Fatal("settlement did not complete")
	}
	if f.step(t) && f.fake.count("execute-restore") != 1 {
		t.Fatal("a settled migration was settled again")
	}
	if got := f.fake.calls; len(got) == 0 || scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, f.op) != 0 {
		t.Fatal("a target was started during settlement")
	}
}

func TestSettleUnknownDiscardsThenRestoresSQLite(t *testing.T) {
	testSettleUnknownDiscardsThenRestores(t, false)
}
func TestSettleUnknownDiscardsThenRestoresPostgres(t *testing.T) {
	testSettleUnknownDiscardsThenRestores(t, true)
}

// C5: readiness failed on migrated data. The started target is stopped and removed before anything else, and
// the applied migration is restored away without a discard (the runner refuses one).
// readinessFailedFixture is C5: the migration was applied, the target was started, and readiness failed, so the
// update ended for review with an applied migration and a running target.
func readinessFailedFixture(t *testing.T, pg bool) *settleFixture {
	t.Helper()
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, true)
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
	reservationObservation := platformUpdateStartReservationFixture(intent, startEndpoint)
	reservation, ok := validPlatformUpdateStartObservation(reservationObservation, intent, startEndpoint)
	if !ok {
		t.Fatal("reservation refused")
	}
	admission, err := s.commitPlatformUpdateStartReservation(context.Background(), start, intent, startEndpoint, reservationObservation, reservation)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := s.commitPlatformUpdateStartReceipt(context.Background(), start, admission.intent, startEndpoint, platformUpdateStartRunningFixture(admission.intent, reservation, startEndpoint))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.finishPlatformManagedUpdateForReview(context.Background(), probe); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	return &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
}

func testSettleReadinessFailureRemovesTargetFirst(t *testing.T, pg bool) {
	f := readinessFailedFixture(t, pg)
	s, endpoint, fake := f.s, f.endpoint, f.fake
	fake.removeTarget = func(i platformUpdateTargetRemovalIntent) (platformUpdateTargetRemovalObservation, error) {
		if i.command.OperationID == f.op {
			t.Error("the removal names the operation of the start it removes")
		}
		state := "stopped"
		if i.command.Stage == "remove_code" {
			state = "absent"
		}
		return platformUpdateTargetRemovalObservationFixture(i, endpoint, state), nil
	}
	fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		if fake.count("remove-stop") != 1 || fake.count("remove-remove_code") != 1 {
			t.Error("the checkpoint was restored while the target was still there")
		}
		return restoreAnswer(i, endpoint), nil
	}
	if f.settleState(t) != "settling" {
		t.Fatalf("a failed readiness is %s", f.settleState(t))
	}
	for range 4 {
		f.step(t)
	}
	want := []string{"check-root", "remove-stop", "check-root", "remove-remove_code", "check-root", "execute-restore", "check-root", "execute-restore"}
	_ = want
	if f.settleState(t) != "settled" || fake.count("remove-stop") != 1 || fake.count("remove-remove_code") != 1 || fake.count("execute-discard") != 0 ||
		fake.count("read-migrate") != 0 || effectState(t, s, f.op, "migrate", 2) != "applied" {
		t.Fatalf("readiness-failure settlement: %s %v", f.settleState(t), fake.calls)
	}
	if effectState(t, s, f.op, "settle", 1) != "applied" || effectState(t, s, f.op, "settle", 2) != "applied" {
		t.Fatal("the target removal was not recorded on the failed update")
	}
}

func TestSettleReadinessFailureRemovesTargetFirstSQLite(t *testing.T) {
	testSettleReadinessFailureRemovesTargetFirst(t, false)
}
func TestSettleReadinessFailureRemovesTargetFirstPostgres(t *testing.T) {
	testSettleReadinessFailureRemovesTargetFirst(t, true)
}

// C6: the runner is killed during the settling restore. The lost restore is read back under its own identity;
// pending is waited for and an orphaned one is completed under the same effect, never under a new one.
func testSettleRestoreUnknownReadBack(t *testing.T, pg bool) {
	f := settleAfterEnd(t, pg, func(fake *platformFakeMigrationRunner, endpoint platformRuntimeEndpoint) {
		fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
			return platformMigrateAnswers(i, endpoint, "failed", "exit_nonzero"), nil
		}
	})
	var effects []string
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		effects = append(effects, i.effectID)
		if len(effects) == 1 {
			return nil, errors.New("runner killed during the restore")
		}
		return restoreAnswer(i, f.endpoint), nil
	}
	reads := 0
	f.fake.readRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		reads++
		if reads == 1 {
			return restoreStatus(i, f.endpoint, "pending"), nil
		}
		return restoreStatus(i, f.endpoint, "orphaned"), nil
	}
	f.step(t) // the first send is lost
	if effectState(t, f.s, f.op, "restore_data", 1) != "prepared" || f.settleState(t) == "settled" {
		t.Fatal("a lost restore was taken as done")
	}
	if f.step(t) && len(effects) != 1 {
		t.Fatal("a restore the runner still holds was sent again")
	}
	f.step(t) // orphaned: completed under the same effect
	if len(effects) != 2 || effects[0] != effects[1] || effectState(t, f.s, f.op, "restore_data", 1) != "applied" {
		t.Fatalf("an orphaned restore was not completed under its own identity: %v", effects)
	}
	f.step(t)
	if f.settleState(t) != "settled" {
		t.Fatal("not settled after the restore")
	}
}

func TestSettleRestoreUnknownReadBackSQLite(t *testing.T) { testSettleRestoreUnknownReadBack(t, false) }
func TestSettleRestoreUnknownReadBackPostgres(t *testing.T) {
	testSettleRestoreUnknownReadBack(t, true)
}

// An absence is definite only after every command signed for the effect has expired. Until then the command may
// still reach the runner, so nothing is sent again.
func testSettleAbsenceOnlyAfterSignedWindow(t *testing.T, pg bool) {
	f := settleAfterEnd(t, pg, func(fake *platformFakeMigrationRunner, endpoint platformRuntimeEndpoint) {
		fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
			return platformMigrateAnswers(i, endpoint, "failed", "wall_clock"), nil
		}
	})
	sends := 0
	f.fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		sends++
		if sends == 1 {
			return nil, errors.New("lost")
		}
		return restoreAnswer(i, f.endpoint), nil
	}
	f.fake.readRestore = func(platformRestoreIntent) (platformSettlingRestoreObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	f.step(t)
	if sends != 1 {
		t.Fatal("the first restore was not sent once")
	}
	if f.step(t) || sends != 1 {
		t.Fatal("an absence inside the signed window was acted on")
	}
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='restore_data' AND effect_number=1`,
		`{"signed_until_ms":`+past+`}`, f.op)
	if !f.step(t) || sends != 2 || effectState(t, f.s, f.op, "restore_data", 1) != "applied" {
		t.Fatalf("a final absence did not let the restore be sent again: sends=%d", sends)
	}
}

func TestSettleAbsenceOnlyAfterSignedWindowSQLite(t *testing.T) {
	testSettleAbsenceOnlyAfterSignedWindow(t, false)
}
func TestSettleAbsenceOnlyAfterSignedWindowPostgres(t *testing.T) {
	testSettleAbsenceOnlyAfterSignedWindow(t, true)
}

// The same holds for the migration itself: an absent migrate-data settles it as never claimed, and then the
// namespace needs no restore.
func TestSettleAbsentMigrationNeedsNoRestore(t *testing.T) {
	s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
	fake.executeMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) { return nil, errors.New("lost") }
	fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationBusy) {
		t.Fatal(err)
	}
	// The scheduler gave up on the update: the effect is left unknown with a signed window still open.
	if err := s.finishPlatformManagedUpdateForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	fake.calls = nil
	f := &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
	f.fake.readMigrate = func(platformMigrateIntent) (platformMigrateObservation, error) {
		return nil, errPlatformMigrationAbsent
	}
	if f.step(t) && effectState(t, f.s, f.op, "migrate", 2) != "reverted" {
		t.Fatal("an absence inside the window was acted on")
	}
	past := strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10)
	mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=2`, `{"signed_until_ms":`+past+`}`, f.op)
	f.step(t)
	f.step(t)
	if f.settleState(t) != "settled" || f.fake.count("execute-restore") != 0 {
		t.Fatalf("absent migration: %s %v", f.settleState(t), f.fake.calls)
	}
}

// The installation's fence moving past the failed update's means something else holds the installation:
// nothing is sent.
func TestSettleRefusedAfterFenceMoved(t *testing.T) {
	f := settleAfterEnd(t, false, orphanedScript)
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, f.lease.installationID)
	if _, err := f.s.settlePlatformMigration(context.Background(), f.endpoint, f.op); err == nil {
		t.Fatal("settlement ran after the fence moved")
	}
	if len(f.fake.calls) != 0 {
		t.Fatalf("the runner was called after the fence moved: %v", f.fake.calls)
	}
	if f.settleState(t) != "blocked" {
		t.Fatalf("a moved fence is %s", f.settleState(t))
	}
}

var _ = appplatform.StageStart
