//go:build linux || darwin

package journal

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func admittedDataFixture(t *testing.T) (*Store, control.Command, []byte, []byte) {
	t.Helper()
	s, _ := enrolledCommandStore(t)
	c := runnerCommandFixture(t)
	raw, sig := signRunnerCommand(t, c)
	if _, err := s.AdmitControlCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return s, c, raw, sig
}

func TestNamespaceReservationHoldsTheDiskItNeeds(t *testing.T) {
	s, c, raw, sig := admittedDataFixture(t)
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	var kind string
	var disk, memory int64
	if err := s.db.QueryRow(`SELECT kind,disk_bytes,memory_mib FROM runner_reservations WHERE resource_id=?`, c.Payload.DataNamespaceID).Scan(&kind, &disk, &memory); err != nil {
		t.Fatalf("no namespace reservation: %v", err)
	}
	if kind != "namespace" || disk != NamespaceQuotaBytes || memory != 0 {
		t.Fatalf("namespace reservation = %s %d %d", kind, disk, memory)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 2 {
		t.Fatal("replay reserved twice")
	}
}

func TestNamespaceReservationRefusedLeavesNoRow(t *testing.T) {
	s, _, raw, sig := admittedDataFixture(t)
	budgets := roomyBudgets()
	budgets.DiskBytes = defaultNamespaceDisk - 1
	if err := s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReservePrepareData(context.Background(), raw, sig)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityDataQuota || refusal.Receipt.Remaining != "1342177279" {
		t.Fatalf("err = %v", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("refused namespace left a row")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='prepare-data' AND state='reverted'`) != 1 {
		t.Fatal("refused namespace not reverted")
	}
	// The data claim cannot proceed from a reverted effect, and the refusal reads back.
	if _, err := s.ClaimPrepareData(context.Background(), raw, sig); err == nil {
		t.Fatal("claimed a refused namespace")
	}
	_, err = s.ReadDataPreparationReceipt(context.Background(), raw, sig)
	var again *CapacityRefusalError
	if !errors.As(err, &again) || again.Receipt != refusal.Receipt {
		t.Fatalf("read-back = %v", err)
	}
}

// defaultNamespaceDisk is what a default namespace reserves: 256 MiB of data and two checkpoints at the 512 MiB
// archive cap. The data row holds the first, a second row the rest.
const defaultNamespaceDisk = int64(1280) << 20

func TestNamespaceReservationBoundaryIsExact(t *testing.T) {
	s, _, raw, sig := admittedDataFixture(t)
	budgets := roomyBudgets()
	budgets.DiskBytes = defaultNamespaceDisk
	if err := s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReservePrepareData(context.Background(), raw, sig); err != nil {
		t.Fatalf("exact fit refused: %v", err)
	}
}

func TestNamespaceQuotaConstantMatchesTheCommandsPortalSends(t *testing.T) {
	c := runnerCommandFixture(t)
	if c.Payload.QuotaBytes != "268435456" {
		t.Skip("fixture does not use the standard quota")
	}
	if NamespaceQuotaBytes != 268435456 {
		t.Fatal("namespace quota constant drifted")
	}
}
