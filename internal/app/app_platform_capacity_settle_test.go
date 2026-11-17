package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func migrateRefusal(i platformMigrateIntent, dimension string) *journal.CapacityRefusalError {
	id := journal.CapacityIdentityOfHeader(i.command.CommandHeader)
	return &journal.CapacityRefusalError{Receipt: journal.CapacityRefusal{
		Format: "portal.runner.capacity-refusal/1-dev.1", DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch,
		InstallationID: id.InstallationID, OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage,
		EffectNumber: id.EffectNumber, EffectID: id.EffectID, Kind: id.Kind, PayloadSHA256: id.PayloadSHA256,
		Outcome: "capacity_exhausted", Dimension: dimension, Requested: "256", Remaining: "100",
		SettledMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}}
}

// An update refused at its migration ends exactly like a migration that failed for a known reason: the effect is
// reverted (with the typed receipt), the operation waits for recovery, and no discard is ever asked of the runner,
// which holds the effect reverted too. Fails if the effect is left unknown, which a discard can never settle.
func TestMigrateRefusedByCapacitySettlesRevertedAndRecovers(t *testing.T) {
	for _, via := range []string{"execute", "read_back"} {
		t.Run(via, func(t *testing.T) {
			s, lease, endpoint, fake := platformMigrationStageFixture(t, false, true)
			fake.executeMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
				if via == "execute" {
					return nil, migrateRefusal(i, "memory")
				}
				return nil, errors.New("connection lost after the runner refused")
			}
			fake.readMigrate = func(i platformMigrateIntent) (platformMigrateObservation, error) {
				return nil, migrateRefusal(i, "memory")
			}
			if _, err := s.dispatchPlatformMigrateData(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
				t.Fatalf("err = %v", err)
			}
			if operationStage(t, s, lease.id) != "recovery_required" || effectState(t, s, lease.id, "migrate", 2) != "reverted" ||
				scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, lease.installationID) != "recovery_required" {
				t.Fatalf("the refused migration did not wait for recovery: %s %s", operationStage(t, s, lease.id), effectState(t, s, lease.id, "migrate", 2))
			}
			var receipt string
			if err := s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id).Scan(&receipt); err != nil ||
				!strings.Contains(receipt, `"outcome":"capacity_exhausted"`) || !strings.Contains(receipt, `"dimension":"memory"`) {
				t.Fatalf("the typed outcome was not kept: %s %v", receipt, err)
			}
			if scalar[int](t, s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.capacity_refused' AND target_id=?`, lease.id) != 1 {
				t.Fatal("no typed audit entry")
			}
			// Recovery: the settlement restores the checkpoint and asks the runner for no discard of a reverted effect.
			f := &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
			fake.calls = nil
			fake.executeMigrate, fake.readMigrate = nil, nil
			fake.executeRestore = func(i platformRestoreIntent) (platformSettlingRestoreObservation, error) {
				return restoreAnswer(i, endpoint), nil
			}
			if f.settleState(t) != "settling" {
				t.Fatalf("a refused migration is %s", f.settleState(t))
			}
			for range 5 {
				f.step(t)
			}
			if f.settleState(t) != "settled" || fake.count("execute-discard") != 0 || fake.count("read-discard") != 0 || fake.count("read-migrate") != 0 {
				t.Fatalf("the update refused at its migration did not recover: %s %v", f.settleState(t), fake.calls)
			}
		})
	}
}

// refusedEffectFixture claims the install operation and gives it one open effect row, so a capacity refusal of that
// effect can be finished by the scheduler.
type refusedEffectFixture struct {
	s       *Server
	w       *platformInstallScheduler
	op      string
	effect  string
	payload string
}

func newRefusedEffectFixture(t *testing.T) refusedEffectFixture {
	t.Helper()
	s, _, id := platformInstallQueueFixture(t, false)
	w := platformSchedulerFixture(t, s)
	_ = w.tick(context.Background())
	if w.active.id != id {
		t.Fatal("the operation was not claimed")
	}
	f := refusedEffectFixture{s: s, w: w, op: id, effect: platformFixtureID(5001), payload: strings.Repeat("b", 64)}
	runtime := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings LIMIT 1`)
	now := time.Now().UTC().UnixMilli()
	if _, err := s.st.db.Exec(s.st.bind(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'start',1,?,'start-candidate',?,?,?,?,0,'unknown','{}',?,?)`), id, f.effect, f.payload, runtime, w.active.controllerEpoch, w.active.fence, now, now); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f refusedEffectFixture) refusal() journal.CapacityRefusal {
	r := capacityRefusalFixture("cpu")
	r.EffectID, r.PayloadSHA256 = f.effect, f.payload
	return r
}

func (f refusedEffectFixture) effectState(t *testing.T) (state, receipt string) {
	t.Helper()
	if err := f.s.st.queryRow(`SELECT state,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND effect_id=?`, f.op, f.effect).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	return state, receipt
}

func (f refusedEffectFixture) finished(t *testing.T) bool {
	t.Helper()
	return scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL`, f.op) == 1
}

// The refused effect is closed in the same transaction that ends the operation, under its lease, fence and the
// effect's payload digest.
func TestFinishCapacitySettlesTheRefusedEffectAtomicallyWithTheOperation(t *testing.T) {
	f := newRefusedEffectFixture(t)
	if err := f.w.finishCapacity(context.Background(), f.refusal()); err != nil {
		t.Fatal(err)
	}
	state, receipt := f.effectState(t)
	if state != "reverted" || !strings.Contains(receipt, `"dimension":"cpu"`) || !f.finished(t) {
		t.Fatalf("effect %s %s finished=%v", state, receipt, f.finished(t))
	}
}

// An effect of another identity, a payload digest the effect does not carry, and an effect that is already settled
// are not touched; the operation still ends.
func TestFinishCapacityLeavesOtherEffectsAlone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*refusedEffectFixture, *journal.CapacityRefusal)
	}{
		{"other_effect", func(_ *refusedEffectFixture, r *journal.CapacityRefusal) { r.EffectID = platformFixtureID(7777) }},
		{"payload_mismatch", func(_ *refusedEffectFixture, r *journal.CapacityRefusal) { r.PayloadSHA256 = strings.Repeat("c", 64) }},
		{"already_settled", func(f *refusedEffectFixture, _ *journal.CapacityRefusal) {
			if _, err := f.s.st.db.Exec(f.s.st.bind(`UPDATE app_platform_operation_effects SET state='applied',receipt_json='{"a":"b"}' WHERE effect_id=?`), f.effect); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefusedEffectFixture(t)
			r := f.refusal()
			tc.mutate(&f, &r)
			beforeState, beforeReceipt := f.effectState(t)
			if err := f.w.finishCapacity(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if state, receipt := f.effectState(t); state != beforeState || receipt != beforeReceipt {
				t.Fatalf("the effect changed: %s %s", state, receipt)
			}
			if !f.finished(t) {
				t.Fatal("the operation did not end")
			}
		})
	}
}

// A lease that moved on (a different owner, a moved fence) ends nothing and settles nothing.
func TestFinishCapacityWithAStaleLeaseSettlesNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*platformOperationLease)
	}{
		{"moved_fence", func(l *platformOperationLease) { l.fence++ }},
		{"other_owner", func(l *platformOperationLease) { l.owner = platformFixtureID(8888) }},
		{"other_epoch", func(l *platformOperationLease) { l.controllerEpoch = platformFixtureID(8889) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRefusedEffectFixture(t)
			tc.mutate(&f.w.active)
			if err := f.w.finishCapacity(context.Background(), f.refusal()); err == nil {
				t.Fatal("a stale lease ended the operation")
			}
			if state, receipt := f.effectState(t); state != "unknown" || receipt != "{}" {
				t.Fatalf("a stale lease settled the effect: %s %s", state, receipt)
			}
			if f.finished(t) {
				t.Fatal("a stale lease finished the operation")
			}
		})
	}
}

// If ending the operation fails after the effect was closed, the effect is not settled: one transaction.
func TestFinishCapacitySettlesNothingWhenTheOperationCannotEnd(t *testing.T) {
	f := newRefusedEffectFixture(t)
	if _, err := f.s.st.db.Exec(`CREATE TRIGGER refuse_operation_end BEFORE UPDATE ON app_platform_operations WHEN NEW.completed_ms IS NOT NULL BEGIN SELECT RAISE(ABORT, 'operation cannot end'); END`); err != nil {
		t.Fatal(err)
	}
	if err := f.w.finishCapacity(context.Background(), f.refusal()); err == nil {
		t.Fatal("the operation ended")
	}
	if state, receipt := f.effectState(t); state != "unknown" || receipt != "{}" {
		t.Fatalf("the effect was settled although the operation did not end: %s %s", state, receipt)
	}
	if f.finished(t) {
		t.Fatal("the operation ended")
	}
}
