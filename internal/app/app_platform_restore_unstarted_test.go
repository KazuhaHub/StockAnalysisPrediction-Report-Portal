package app

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// unstartedUpdateFixture leaves an update that failed after it stopped and removed the source and took its
// checkpoint, but before it started a target and with no migration: the state an image that could not be loaded
// leaves behind.
func unstartedUpdateFixture(t *testing.T, pg bool) *settleFixture {
	t.Helper()
	platformWritableUpdateFixtures(t)
	s, lease, endpoint, fake := platformMigrationStageFixture(t, pg, false)
	if err := s.finishPlatformManagedUpdateForReview(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if got := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, lease.id); got != "recovery_required" {
		t.Fatalf("fixture: the update ended %s", got)
	}
	if n := scalar[int64](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='start'`, lease.id); n != 0 {
		t.Fatal("fixture: the update has a start effect")
	}
	return &settleFixture{s: s, lease: lease, endpoint: endpoint, fake: fake, op: lease.id}
}

// A roll back of an update that never started a target has nothing to stop or remove: it restores the
// installation from the checkpoint, and the application launches again.
func testRollbackOfAnUnstartedUpdate(t *testing.T, pg bool) {
	f := unstartedUpdateFixture(t, pg)
	h := newMigrationHTTP(t, f)
	rollBackFailedOverHTTP(t, f, h, f.op)
	expectRolledBack(t, f, h)
}

func TestRollbackOfAnUnstartedUpdateSQLite(t *testing.T) {
	t.Parallel()
	testRollbackOfAnUnstartedUpdate(t, false)
}

func TestRollbackOfAnUnstartedUpdatePostgres(t *testing.T) {
	t.Parallel()
	testRollbackOfAnUnstartedUpdate(t, true)
}

// A roll back that ended for review before it did anything does not keep the installation closed once a later
// roll back of the same update completes.
func testLaterRollbackResolvesAnEarlierOneThatDidNothing(t *testing.T, pg bool) {
	f := unstartedUpdateFixture(t, pg)
	h := newMigrationHTTP(t, f)
	ctx := context.Background()
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	set := recoverySetOf(t, f.s, f.op)
	review := h.json(h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`), 200)
	commit := h.json(h.post(operations, `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7410)+`"}`), 202)
	selected, err := f.s.claimNextPlatformManagedRestore(ctx, platformFixtureID(7411))
	if err != nil || selected.lease.id != h.str(commit, "operation_id") {
		t.Fatal("the first roll back was not claimed", selected.lease.id, err)
	}
	if selected.lease.snapshot.Stage != appplatform.StageQuiesce {
		t.Fatalf("the first roll back is at %s", selected.lease.snapshot.Stage)
	}
	if err := f.s.finishPlatformManagedRestoreForReview(ctx, selected.lease); err != nil {
		t.Fatal(err)
	}
	first := selected.lease.id
	if n, err := platformUnresolvedOperationCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 2 {
		t.Fatalf("fixture: %d unresolved operations: %v", n, err)
	}
	rollBackFailedOverHTTP(t, f, h, f.op)
	expectRolledBack(t, f, h)
	if got := scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, first); got != "recovery_required" {
		t.Fatalf("the first roll back's record changed to %s", got)
	}
}

func TestLaterRollbackResolvesAnEarlierOneThatDidNothingSQLite(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, false)
}

func TestLaterRollbackResolvesAnEarlierOneThatDidNothingPostgres(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, true)
}

// An earlier roll back that applied an effect is not resolved by a later one: what it did is not the later
// roll back's evidence.
func TestLaterRollbackDoesNotResolveAnEarlierOneWithEffects(t *testing.T) {
	t.Parallel()
	f := unstartedUpdateFixture(t, false)
	h := newMigrationHTTP(t, f)
	ctx := context.Background()
	const plans, operations = "/api/admin/app-platform/plans", "/api/admin/app-platform/operations"
	set := recoverySetOf(t, f.s, f.op)
	review := h.json(h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`), 200)
	h.json(h.post(operations, `{"plan_id":"`+h.str(review, "plan_id")+`","plan_sha256":"`+h.str(review, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7410)+`"}`), 202)
	selected, err := f.s.claimNextPlatformManagedRestore(ctx, platformFixtureID(7411))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.finishPlatformManagedRestoreForReview(ctx, selected.lease); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(effect_id,operation_id,stage,effect_number,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT ?,?,stage,9,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,'applied',receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
		platformFixtureID(7412), selected.lease.id, f.op)
	rollBackFailedOverHTTP(t, f, h, f.op)
	if n, err := platformUnresolvedOperationCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 1 {
		t.Fatalf("%d unresolved operations, want the earlier roll back: %v", n, err)
	}
}

func expectRolledBack(t *testing.T, f *settleFixture, h migrationHTTP) {
	t.Helper()
	s := f.s
	if n, err := platformUnresolvedOperationCount(s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unresolved operations: %v", n, err)
	}
	if n, err := platformUnresolvedOperationEffectCount(s.st.poolExec(), f.lease.installationID); err != nil || n != 0 {
		t.Fatalf("the roll back left %d unsafe effects: %v", n, err)
	}
	if got := h.launch(); got != 200 {
		t.Fatalf("launch after the roll back answered %d", got)
	}
	if scalar[string](t, s.st, `SELECT phase FROM app_platform_installations WHERE id=?`, f.lease.installationID) != "ready" {
		t.Fatal("the installation is not ready after the roll back")
	}
}

// Only an update whose every effect is settled, whose source removal applied and that started nothing takes the
// start-only roll back: anything in doubt keeps the roll back on the path that stops and removes.
func TestPlatformUpdateNeverStartedRefusesAnythingInDoubt(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]string{
		"removal unknown":    `UPDATE app_platform_operation_effects SET state='unknown' WHERE operation_id=? AND stage='migrate' AND effect_number=1`,
		"removal absent":     `DELETE FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=1`,
		"stop prepared":      `UPDATE app_platform_operation_effects SET state='prepared' WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
		"start reverted":     `UPDATE app_platform_operation_effects SET stage='start',kind='update-start',state='reverted' WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
		"start-bearing kind": `UPDATE app_platform_operation_effects SET kind='restore-start' WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := unstartedUpdateFixture(t, false)
			if ok, err := platformUpdateNeverStarted(f.s.st.poolExec(), f.op); err != nil || !ok {
				t.Fatalf("fixture: the update does not count as never started: %v", err)
			}
			mustExec(t, f.s.st, mutate, f.op)
			if ok, err := platformUpdateNeverStarted(f.s.st.poolExec(), f.op); err != nil || ok {
				t.Fatalf("an update with %s counts as never started: %v", name, err)
			}
		})
	}
}
