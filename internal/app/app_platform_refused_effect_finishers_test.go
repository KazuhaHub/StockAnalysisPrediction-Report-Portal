package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// refusedFinisherCase is an in-flight operation of one action with the finisher that ends it for review.
type refusedFinisherCase struct {
	name   string
	build  func(t *testing.T) (*Server, platformOperationLease)
	finish func(*Server, context.Context, platformOperationLease) error
}

func refusedFinisherCases() []refusedFinisherCase {
	return []refusedFinisherCase{
		{"update", func(t *testing.T) (*Server, platformOperationLease) {
			s, lease, _, _, _ := platformImageEnsureFixture(t)
			return s, lease
		}, (*Server).finishPlatformUpdateForReview},
		{"enable", func(t *testing.T) (*Server, platformOperationLease) {
			s, snapshot := platformManagedEnableSnapshotFixture(t, false)
			checkpoint, err := s.preparePlatformCheckpoint(context.Background(), snapshot)
			if err != nil {
				t.Fatal(err)
			}
			endpoint := platformCheckpointEndpointFixture()
			lease, err := s.commitPlatformCheckpoint(context.Background(), snapshot, checkpoint, endpoint, platformCheckpointObservationFixture(checkpoint, endpoint))
			if err != nil {
				t.Fatal(err)
			}
			return s, lease
		}, (*Server).finishPlatformManagedEnableForReview},
		{"restore", func(t *testing.T) (*Server, platformOperationLease) {
			s, lease, _ := platformUpdateRecoveryRestoreFixture(t, false)
			return s, lease
		}, (*Server).finishPlatformManagedRestoreForReview},
	}
}

const refusedFinisherEffect = 9

// openRefusedEffect gives the operation one open effect row and returns the refusal that names it.
func openRefusedEffect(t *testing.T, s *Server, lease platformOperationLease) journal.CapacityRefusal {
	t.Helper()
	runtime := scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID)
	effect, payload := platformFixtureID(6001), strings.Repeat("d", 64)
	now := time.Now().UTC().UnixMilli()
	if _, err := s.st.db.Exec(s.st.bind(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'start',?,?,'start-candidate',?,?,?,?,0,'unknown','{}',?,?)`), lease.id, refusedFinisherEffect, effect, payload, runtime, lease.controllerEpoch, lease.fence, now, now); err != nil {
		t.Fatal(err)
	}
	r := capacityRefusalFixture("memory")
	r.OperationID, r.InstallationID, r.EffectID, r.PayloadSHA256 = lease.id, lease.installationID, effect, payload
	return r
}

func refusedEffectRow(t *testing.T, s *Server, lease platformOperationLease) (state, receipt string) {
	t.Helper()
	if err := s.st.queryRow(`SELECT state,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='start' AND effect_number=?`, lease.id, refusedFinisherEffect).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	return state, receipt
}

func operationCompleted(t *testing.T, s *Server, lease platformOperationLease) bool {
	t.Helper()
	return scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL`, lease.id) == 1
}

// The refused effect's row is closed in the same transaction that ends the operation, for every action's finisher.
// Fails if a finisher ends the operation without settling the row, or settles it without the operation ending.
func TestFinishersSettleTheRefusedEffectWithTheOperation(t *testing.T) {
	for _, c := range refusedFinisherCases() {
		t.Run(c.name, func(t *testing.T) {
			s, lease := c.build(t)
			r := openRefusedEffect(t, s, lease)
			if err := c.finish(s, withPlatformRefusedEffect(context.Background(), r), lease); err != nil {
				t.Fatal(err)
			}
			state, receipt := refusedEffectRow(t, s, lease)
			if state != "reverted" || !strings.Contains(receipt, `"dimension":"memory"`) || !operationCompleted(t, s, lease) {
				t.Fatalf("%s: row %s %s completed=%v", c.name, state, receipt, operationCompleted(t, s, lease))
			}
		})
	}
}

// Without the request nothing is settled, and when the operation cannot end the row stays as it was: one
// transaction, never a settlement on its own.
func TestFinishersSettleTheRefusedEffectOnlyWhenAskedAndOnlyWithTheOperation(t *testing.T) {
	for _, c := range refusedFinisherCases() {
		t.Run(c.name+"/not_asked", func(t *testing.T) {
			s, lease := c.build(t)
			openRefusedEffect(t, s, lease)
			if err := c.finish(s, context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			if state, receipt := refusedEffectRow(t, s, lease); state != "unknown" || receipt != "{}" || !operationCompleted(t, s, lease) {
				t.Fatalf("%s: an unasked finish touched the row: %s %s", c.name, state, receipt)
			}
		})
		t.Run(c.name+"/operation_cannot_end", func(t *testing.T) {
			s, lease := c.build(t)
			r := openRefusedEffect(t, s, lease)
			if _, err := s.st.db.Exec(`CREATE TRIGGER refuse_operation_end BEFORE UPDATE ON app_platform_operations WHEN NEW.completed_ms IS NOT NULL BEGIN SELECT RAISE(ABORT, 'operation cannot end'); END`); err != nil {
				t.Fatal(err)
			}
			if err := c.finish(s, withPlatformRefusedEffect(context.Background(), r), lease); err == nil {
				t.Fatal("the operation ended")
			}
			if state, receipt := refusedEffectRow(t, s, lease); state != "unknown" || receipt != "{}" || operationCompleted(t, s, lease) {
				t.Fatalf("%s: partial settlement %s %s", c.name, state, receipt)
			}
		})
		t.Run(c.name+"/stale_lease", func(t *testing.T) {
			s, lease := c.build(t)
			r := openRefusedEffect(t, s, lease)
			stale := lease
			stale.fence++
			if err := c.finish(s, withPlatformRefusedEffect(context.Background(), r), stale); err == nil {
				t.Fatal(fmt.Sprint(c.name, ": a stale lease ended the operation"))
			}
			if state, receipt := refusedEffectRow(t, s, lease); state != "unknown" || receipt != "{}" || operationCompleted(t, s, lease) {
				t.Fatalf("%s: a stale lease settled the row %s %s", c.name, state, receipt)
			}
		})
	}
}
