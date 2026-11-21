package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// applyInRolledBackTx applies a receipt to a raise row inside a transaction that is always rolled back, and returns
// what the application answered.
func applyInRolledBackTx(t *testing.T, s *Server, ref platformRaiseCommitRef, receipt journal.SetDataQuotaReceipt, encoded string, again bool) error {
	t.Helper()
	tx, err := s.st.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: tx}
	if err := applyPlatformRaise(e, ref, receipt, encoded, 2); err != nil || !again {
		return err
	}
	return applyPlatformRaise(e, ref, receipt, encoded, 3)
}

// A raise row is written once to its verdict. A late receipt cannot resurrect a reverted row, and a receipt cannot be
// applied twice: the quota chain is derived from applied rows, so a rewritten one would move the chain under every
// loader.
func testRaiseRowTakesItsVerdictOnce(t *testing.T, pg bool) {
	s, lease, endpoint, raise, _ := platformUpdateRaiseFixture(t, pg, false)
	namespace := platformNamespaceOf(t, s, lease.installationID)
	bytes0 := scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace)
	raise.execute = func(i platformSetDataQuotaIntent) (platformSetDataQuotaObservation, error) {
		return raise.status(i, "failed", "quota_raise_unsafe"), nil
	}
	if _, err := s.runPlatformManagedUpdate(context.Background(), lease, endpoint); !errors.Is(err, errPlatformMigrationEnded) {
		t.Fatal(err)
	}
	intent := raise.sent[0]
	receipt := platformQuotaObservation(intent, endpoint).receipt
	encoded, err := platformEncodeRaiseReceipt(receipt)
	if err != nil {
		t.Fatal(err)
	}
	ref := platformRaiseCommitRef{
		operation: lease.id, installation: lease.installationID, namespace: namespace, slot: platformUpdateRaiseSlot,
		effectID: intent.effectID, digest: intent.command.PayloadSHA256, runtimeID: intent.runtime.id, epoch: lease.controllerEpoch,
		admittedFence: intent.admittedFence, observedFence: lease.fence,
	}
	if err := applyInRolledBackTx(t, s, ref, receipt, encoded, false); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("a late receipt was applied over a reverted row: %v", err)
	}
	if effectState(t, s, lease.id, "migrate", platformUpdateRaiseEffectNumber) != "reverted" ||
		scalar[int64](t, s.st, `SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=?`, namespace) != bytes0 {
		t.Fatal("a refused late receipt changed the row or the namespace")
	}

	// An applied row takes no second receipt either.
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET state='unknown',receipt_json='{}' WHERE operation_id=? AND stage='migrate' AND effect_number=?`, lease.id, platformUpdateRaiseEffectNumber)
	if err := applyInRolledBackTx(t, s, ref, receipt, encoded, true); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("a receipt was applied twice: %v", err)
	}
	if err := applyInRolledBackTx(t, s, ref, receipt, encoded, false); err != nil {
		t.Fatalf("the first application of the receipt was refused: %v", err)
	}
}

func TestRaiseRowTakesItsVerdictOnceSQLite(t *testing.T)   { testRaiseRowTakesItsVerdictOnce(t, false) }
func TestRaiseRowTakesItsVerdictOncePostgres(t *testing.T) { testRaiseRowTakesItsVerdictOnce(t, true) }
