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

// Whatever stage the runner refused, the Portal's effect row of that very effect is closed as reverted with the
// typed receipt, so nothing waits on an effect the runner holds reverted. An effect of another identity, or one
// already settled, is left alone.
func TestSettleRefusedEffectRevertsOnlyTheNamedOpenEffect(t *testing.T) {
	f := settleAfterEnd(t, false, orphanedScript)
	if effectState(t, f.s, f.op, "migrate", 2) != "unknown" {
		t.Fatalf("fixture effect is %s", effectState(t, f.s, f.op, "migrate", 2))
	}
	var effectID string
	if err := f.s.st.queryRow(`SELECT effect_id FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, f.op).Scan(&effectID); err != nil {
		t.Fatal(err)
	}
	other := capacityRefusalFixture("cpu")
	other.EffectID = platformFixtureID(7777)
	if err := f.s.settleRefusedEffect(context.Background(), f.op, other); err != nil {
		t.Fatal(err)
	}
	if effectState(t, f.s, f.op, "migrate", 2) != "unknown" {
		t.Fatal("another effect's refusal closed this effect")
	}
	named := capacityRefusalFixture("cpu")
	named.EffectID = effectID
	if err := f.s.settleRefusedEffect(context.Background(), f.op, named); err != nil {
		t.Fatal(err)
	}
	var receipt string
	if err := f.s.st.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, f.op).Scan(&receipt); err != nil ||
		effectState(t, f.s, f.op, "migrate", 2) != "reverted" || !strings.Contains(receipt, `"dimension":"cpu"`) {
		t.Fatalf("effect after its refusal: %s %s %v", effectState(t, f.s, f.op, "migrate", 2), receipt, err)
	}
}
