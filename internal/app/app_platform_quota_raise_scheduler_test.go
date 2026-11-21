package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The scheduler's tick settles through settlePlatformMigrations, so that is the function a raise's convergence must
// be reachable from; a convergence only the tests call would never run.
func TestSchedulerSettlementReachesTheQuotaRaiseConvergence(t *testing.T) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, false, false)
	raise.execute = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	raise.read = func(platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return nil, errors.New("lost")
	}
	_, _ = s.runPlatformManagedUpdate(context.Background(), lease, endpoint)
	if err := s.finishPlatformUpdateForReview(context.Background(), platformLeaseOf(t, s, lease.id)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE operation_id=? AND stage='migrate' AND effect_number=?`,
		platformImportPendingReceipt(time.Now().UnixMilli()+int64(time.Hour/time.Millisecond)), lease.id, platformUpdateRaiseEffectNumber)
	raise.read = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.receipt(i), nil
	}
	progressed, err := s.settlePlatformMigrations(context.Background(), endpoint)
	if err != nil || progressed != 1 || effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "applied" {
		t.Fatalf("the scheduler's settlement did not converge the raise: %d %v", progressed, err)
	}
}
