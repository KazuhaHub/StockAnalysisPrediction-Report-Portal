//go:build linux || darwin

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// candidateIndexFor reads the identity out of a signed candidate the test built itself.
func candidateIndexFor(t *testing.T, raw, _ []byte) control.Command {
	t.Helper()
	var c control.CandidateCommand
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return candidateIndex(c)
}

func fixtureResources() appplatform.Resources {
	return appplatform.Resources{MemoryMiB: 256, CPUMillis: 500, PIDs: 64}
}

func roomyBudgets() CapacityBudgets {
	return CapacityBudgets{MemoryMiB: 4096, CPUMillis: 2000, DiskBytes: 8 << 30, ImageStoreBytes: 10 << 30, RunningBackends: 8}
}

func TestCandidateReservationHoldsTheDeclaredResources(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	reserved, err := s.ReserveCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	var memory, cpu, disk int64
	var kind, state string
	if err := s.db.QueryRow(`SELECT kind,state,memory_mib,cpu_millis,disk_bytes FROM runner_reservations WHERE resource_id=?`, reserved.ResourceID).Scan(&kind, &state, &memory, &cpu, &disk); err != nil {
		t.Fatalf("no reservation: %v", err)
	}
	if kind != "backend" || state != "held" || memory != 256 || cpu != 500 || disk != 0 {
		t.Fatalf("reservation = %s %s %d %d %d", kind, state, memory, cpu, disk)
	}
	// The resource and its reservation commit together; a replay reserves nothing more.
	if _, err := s.ReserveCandidate(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 1 {
		t.Fatal("replay added a reservation")
	}
	if _, err := s.ClaimCandidate(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateReservationRefusedLeavesNoRowAndReplays(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	budgets := roomyBudgets()
	budgets.MemoryMiB = 255 // one MiB short of the declared 256
	if err := s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	_, err := s.ReserveCandidate(ctx, raw, sig)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a capacity refusal", err)
	}
	r := refusal.Receipt
	if r.Outcome != CapacityExhaustedOutcome || r.Dimension != CapacityMemory || r.Requested != "256" || r.Remaining != "255" {
		t.Fatalf("receipt = %+v", r)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("a refused candidate left a resource or reservation row")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='reverted' AND kind='start-candidate'`) != 1 {
		t.Fatal("the refused command is not reverted")
	}
	// The claim cannot start what was refused.
	if _, err := s.ClaimCandidate(ctx, raw, sig); err == nil {
		t.Fatal("claimed a refused candidate")
	}
	// A replay of the dispatch returns the same receipt.
	again := s.capacityRefusalFor(context.Background(), CapacityIdentityOfCommand(candidateIndexFor(t, raw, sig)))
	var replay *CapacityRefusalError
	if !errors.As(again, &replay) || replay.Receipt != r {
		t.Fatalf("replay = %v", again)
	}
}

func TestCandidateReservationWithoutDeclaredResourcesFailsClosed(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); !errors.Is(err, ErrCapacityPlanMissing) {
		t.Fatalf("err = %v", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 {
		t.Fatal("reserved without declared resources")
	}
}

func TestCandidateClaimRequiresAHeldReservation(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	// Reserved while no budgets were configured: the resource exists, its reservation does not.
	if _, err := s.ReserveCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("claimed without a held reservation")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 0 {
		t.Fatal("a refused claim marked the effect unknown")
	}
}

func TestUnknownCandidateKeepsItsReservation(t *testing.T) {
	s, raw, sig := readyCandidateFixture(t)
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	if _, err := s.ReserveCandidate(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	// The provider fails after the claim: the effect stays unknown, so the reservation stays held.
	_, _ = s.RunCandidateOnce(ctx, claim, nil)
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE state='unknown'`) != 1 || heldCount(t, s) != 1 {
		t.Fatal("an unknown start released or lost its reservation")
	}
}

func admittedWritableFixture(t *testing.T) (*Store, []byte, []byte) {
	t.Helper()
	s, wraw, wsig, receipt := syntheticAppliedWriterFixture(t)
	raw, sig := signWritableIntent(t, writableIntentFixture(t, wraw, wsig, receipt))
	if _, err := s.AdmitWritableCandidateCommand(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	return s, raw, sig
}

func TestWritableReservationHoldsAdmitsAndRefuses(t *testing.T) {
	s, raw, sig := admittedWritableFixture(t)
	budgets := roomyBudgets()
	budgets.CPUMillis = 499 // one millicore short of the declared 500
	if err := s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	_, err := s.ReserveWritableCandidate(ctx, raw, sig)
	var refusal *CapacityRefusalError
	if !errors.As(err, &refusal) || refusal.Receipt.Dimension != CapacityCPU || refusal.Receipt.Requested != "500" || refusal.Receipt.Remaining != "499" {
		t.Fatalf("err = %v", err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container'`) != 0 || scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations`) != 0 {
		t.Fatal("refused writable candidate left a row")
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='start-candidate' AND state='reverted'`) != 1 {
		t.Fatal("command not reverted")
	}
}

func TestWritableReservationAdmitted(t *testing.T) {
	s, raw, sig := admittedWritableFixture(t)
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	r, err := s.ReserveWritableCandidate(ctx, raw, sig)
	if err != nil {
		t.Fatal(err)
	}
	if scalar(t, s.db, `SELECT COUNT(*) FROM runner_reservations WHERE state='held' AND kind='backend' AND memory_mib=256 AND cpu_millis=500 AND resource_id='`+r.ResourceID+`'`) != 1 {
		t.Fatal("writable reservation not held at the declared amounts")
	}
	if _, err := s.ClaimWritableCandidate(ctx, raw, sig); err != nil {
		t.Fatal(err)
	}
}

func TestWritableClaimRequiresAHeldReservation(t *testing.T) {
	s, raw, sig := admittedWritableFixture(t)
	if _, err := s.ReserveWritableCandidate(context.Background(), raw, sig); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimWritableCandidate(context.Background(), raw, sig); err == nil {
		t.Fatal("claimed without a held reservation")
	}
}

func TestRefusedStartIsReadBackAsTheSameReceipt(t *testing.T) {
	s, raw, sig := admittedWritableFixture(t)
	budgets := roomyBudgets()
	budgets.MemoryMiB = 1
	if err := s.SetCapacityBudgets(budgets); err != nil {
		t.Fatal(err)
	}
	ctx := withBackendResources(context.Background(), fixtureResources())
	_, err := s.ReserveWritableCandidate(ctx, raw, sig)
	var first *CapacityRefusalError
	if !errors.As(err, &first) {
		t.Fatalf("err = %v", err)
	}
	_, err = s.ReadWritableCandidateRunningReceipt(context.Background(), raw, sig)
	var again *CapacityRefusalError
	if !errors.As(err, &again) || again.Receipt != first.Receipt {
		t.Fatalf("read-back = %v", err)
	}
	// A signature that does not verify gets no receipt.
	bad := append([]byte(nil), sig...)
	bad[0] ^= 1
	if _, err := s.ReadWritableCandidateRunningReceipt(context.Background(), raw, bad); errors.As(err, &again) {
		t.Fatal("a forged signature read a refusal")
	}
}
