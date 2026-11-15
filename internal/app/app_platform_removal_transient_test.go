package app

import (
	"context"
	"errors"
	"testing"
)

// A database error while reading the host operation is not evidence about the operation: it must not be typed as the
// stage-evidence verdict that closes a removal. Only a missing or unsettleable operation is.
func TestHostOperationReadErrorsAreTypedOnlyWhenTheOperationCannotSettle(t *testing.T) {
	eachPlatformDriver(t, testHostOperationReadErrors)
}

func testHostOperationReadErrors(t *testing.T, pg bool) {
	f := newReclaimFixtureFor(t, pg)
	t.Run("a missing operation", func(t *testing.T) {
		if _, err := platformLoadSettlementLease(f.s.st.poolExec(), platformFixtureID(7777)); !errors.Is(err, errAppPlatformStageEvidence) {
			t.Fatalf("a missing operation is typed evidence, got %v", err)
		}
	})
	t.Run("an operation still in flight", func(t *testing.T) {
		mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='stage',completed_ms=NULL WHERE id=?`, f.hostOp)
		_, err := platformLoadSettlementLease(f.s.st.poolExec(), f.hostOp)
		if err == nil || errors.Is(err, errAppPlatformStageEvidence) || !errors.Is(err, errPlatformHostOperationInFlight) {
			t.Fatalf("an operation that has not finished is not settled yet, and says nothing about the removal: %v", err)
		}
	})
	t.Run("a transient read error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := platformLoadSettlementLease(migExec{s: f.s.st, ex: platformContextPool{DB: f.s.st.db, ctx: ctx}}, f.hostOp)
		if err == nil || errors.Is(err, errAppPlatformStageEvidence) || !errors.Is(err, context.Canceled) {
			t.Fatalf("a transient read error must stay untyped, got %v", err)
		}
	})
}

// The guard that runs in the signing transaction follows the same rule: a failed read is neither the lease conflict nor
// the evidence verdict.
func TestSettlementGuardKeepsATransientReadErrorUntyped(t *testing.T) {
	eachPlatformDriver(t, testSettlementGuardTransient)
}

func testSettlementGuardTransient(t *testing.T, pg bool) {
	f := newReclaimFixtureFor(t, pg)
	lease, err := platformLoadSettlementLease(f.s.st.poolExec(), f.hostOp)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = platformLoadSettlementGuard(migExec{s: f.s.st, ex: platformContextPool{DB: f.s.st.db, ctx: ctx}}, lease)
	if err == nil || errors.Is(err, errAppPlatformLeaseConflict) || errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("a transient guard read error was typed as a verdict: %v", err)
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, f.installation)
	if _, err := platformLoadSettlementGuard(f.s.st.poolExec(), lease); !errors.Is(err, errAppPlatformLeaseConflict) {
		t.Fatalf("a moved fence is the lease conflict, got %v", err)
	}
}

// An open removal whose host operation can no longer settle it is closed abandoned; one that merely could not be
// read this time stays open and is tried again.
func TestResolveOpenRemovalAbandonsOnlyOnTypedErrors(t *testing.T) {
	eachPlatformDriver(t, testResolveOpenRemovalTyped)
}

func testResolveOpenRemovalTyped(t *testing.T, pg bool) {
	open := func(t *testing.T) (*reclaimFixture, platformReclaimTarget) {
		f := newReclaimFixtureFor(t, pg)
		f.release(t, "a")
		f.load(t, "a")
		f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
		_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
		f.fake.loseExecuteReplyOnce, f.fake.readFails = false, false
		targets, err := f.s.platformOpenRemovalsFor(context.Background(), f.runtimeID, f.config("a"))
		if err != nil || len(targets) != 1 {
			t.Fatalf("the fixture has no open removal: %v %v", targets, err)
		}
		return f, targets[0]
	}
	state := func(f *reclaimFixture) string {
		return scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`)
	}
	t.Run("a fence that moved past the host operation", func(t *testing.T) {
		f, target := open(t)
		mustExec(t, f.s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, f.installation)
		if err := f.s.resolveOpenRemoval(context.Background(), f.endpoint, target); err != nil {
			t.Fatal(err)
		}
		if state(f) != "reverted" {
			t.Fatalf("a removal its operation cannot settle was left open: %s", state(f))
		}
	})
	t.Run("a host operation that is still in flight", func(t *testing.T) {
		f, target := open(t)
		mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='stage',completed_ms=NULL WHERE id=?`, f.hostOp)
		if err := f.s.resolveOpenRemoval(context.Background(), f.endpoint, target); !errors.Is(err, errPlatformHostOperationInFlight) {
			t.Fatalf("an operation that is not finished must be waited for, got %v", err)
		}
		if state(f) == "reverted" {
			t.Fatal("a removal was abandoned because its host operation had not finished")
		}
	})
	t.Run("a release that is gone", func(t *testing.T) {
		f, target := open(t)
		mustExec(t, f.s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE kind='remove_image'`, `{"intent":{"release_sha256":"`+platformDigest([]byte("gone"))+`","config_digest":"`+f.config("a")+`"}}`)
		target.intent.ReleaseSHA256 = platformDigest([]byte("gone"))
		if err := f.s.resolveOpenRemoval(context.Background(), f.endpoint, target); err != nil {
			t.Fatal(err)
		}
		if state(f) != "reverted" {
			t.Fatalf("a removal of a missing release was left open: %s", state(f))
		}
	})
	t.Run("a read that fails for a reason that is not a verdict", func(t *testing.T) {
		f, target := open(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := f.s.resolveOpenRemoval(ctx, f.endpoint, target); err == nil {
			t.Fatal("a failed read reported success")
		}
		if state(f) == "reverted" {
			t.Fatal("a removal was abandoned on a transient read error")
		}
	})
}
