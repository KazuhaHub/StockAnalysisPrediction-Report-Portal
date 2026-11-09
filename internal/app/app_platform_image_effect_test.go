package app

import (
	"context"
	"errors"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func imageRow(t *testing.T, s *Server, operation string) (id, kind, state, receipt string, found bool) {
	t.Helper()
	rows, err := s.st.db.Query(s.st.bind(`SELECT effect_id,kind,state,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='stage' AND effect_number=1`), operation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&id, &kind, &state, &receipt); err != nil {
			t.Fatal(err)
		}
		found = true
	}
	return
}

// Fails if the load leaves no Portal row, uses another kind or stage, or keeps asking the runner once applied.
func TestEnsureImageWritesALoadImageRowThatEndsAppliedWithTheReceiptAndIsNotSentAgain(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	id, kind, state, receipt, found := imageRow(t, s, lease.id)
	if !found || kind != "load_image" || state != "applied" || id != platformLoadImageEffectID(lease.id, platformDigest(parts.descriptor)) {
		t.Fatalf("row %v %s %s %s", found, kind, state, id)
	}
	if r, err := journal.ParseLoadImageReceipt([]byte(receipt)); err != nil || r.EffectID != id {
		t.Fatalf("the row does not hold the runner's receipt: %v", err)
	}
	calls := len(fake.calls)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil || len(fake.calls) != calls {
		t.Fatalf("an applied load was asked for again: %v %v", err, fake.calls[calls:])
	}
}

// Fails if a load with no verdict leaves the row prepared or applied, which would hide it from the settlement
// driver or claim a load that was never seen.
func TestEnsureImageLeavesTheRowUnknownWhenTheOutcomeIsNotKnown(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.loseExecuteReplyOnce, fake.readFails = true, true
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageUncertain) {
		t.Fatal(err)
	}
	if _, _, state, _, found := imageRow(t, s, lease.id); !found || state != "unknown" {
		t.Fatalf("row state %q", state)
	}
}

// Fails if a rejection is not recorded, or is asked of the runner again.
func TestEnsureImageRecordsARejectionAsRevertedAndRefusesWithoutAskingAgain(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.rejectLoad = "inspect_mismatch"
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageRejected) {
		t.Fatal(err)
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "reverted" {
		t.Fatalf("row state %q", state)
	}
	calls := len(fake.calls)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageRejected) || len(fake.calls) != calls {
		t.Fatalf("a recorded rejection was sent again: %v", err)
	}
}

// The unknown load of an operation the gate gave up on: the operation cannot be settled while the load has no
// verdict, a later driver tick reads it back, sends it again under the same id and settles it, and the
// operation then leaves recovery_required. Fails if the unknown load is invisible, if the driver mints a second
// identity, or if the installation stays blocked after the load settled.
func TestAnUnknownLoadKeepsTheOperationRecoveryRequiredUntilTheDriverSettlesItUnderTheSameId(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	fake.loseExecuteReplyOnce, fake.readFails = true, true
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err == nil {
		t.Fatal("the gate did not give up")
	}
	if err := s.finishPlatformUpdateForReview(context.Background(), lease); err != nil {
		t.Fatal("the operation did not end for review:", err)
	}
	if stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id); stage != "recovery_required" {
		t.Fatalf("operation stage %q", stage)
	}
	// The recovery choices (retry, roll back, restore) are offered only past this gate.
	settleOperation := func() error { return platformRequireSettledMigration(s.st.poolExec(), lease.id) }
	if err := settleOperation(); err == nil {
		t.Fatal("an operation with an unknown load was settled")
	}
	fake.readFails = false
	n, err := s.settlePlatformLoadImages(context.Background(), endpoint)
	if err != nil || n != 1 {
		t.Fatalf("driver tick: %d %v", n, err)
	}
	if _, _, state, _, _ := imageRow(t, s, lease.id); state != "applied" {
		t.Fatalf("row state %q after the driver", state)
	}
	if len(fake.effectIDs) != 1 || !fake.effectIDs[platformLoadImageEffectID(lease.id, platformDigest(parts.descriptor))] {
		t.Fatalf("the driver used another identity: %v", fake.effectIDs)
	}
	if err := settleOperation(); err != nil {
		t.Fatal("the installation is still blocked after the load settled:", err)
	}
	if n, _ := s.settlePlatformLoadImages(context.Background(), endpoint); n != 0 {
		t.Fatal("a settled load was settled again")
	}
}
