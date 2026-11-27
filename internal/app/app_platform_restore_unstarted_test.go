package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
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
func testLaterRollbackResolvesAnEarlierOneThatDidNothing(t *testing.T, pg, withImage bool) {
	testLaterRollbackWithEarlierImage(t, pg, withImage, "applied")
}

func testLaterRollbackWithEarlierImage(t *testing.T, pg, withImage bool, imageState string) {
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
	if withImage {
		// The earlier roll back loaded its image and its start was refused: neither is work on the installation.
		mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(effect_id,operation_id,stage,effect_number,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT ?,?,'stage',1,'load_image',payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,?,receipt_json,created_ms,updated_ms
			FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
			platformFixtureID(7413), first, imageState, f.op)
		mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(effect_id,operation_id,stage,effect_number,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			SELECT ?,?,'start',1,'restore-start',payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,'reverted',receipt_json,created_ms,updated_ms
			FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce' AND effect_number=1`,
			platformFixtureID(7414), first, f.op)
	}
	if n, err := platformUnresolvedOperationCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 2 {
		t.Fatalf("fixture: %d unresolved operations: %v", n, err)
	}
	if imageState != "applied" {
		// An image load in doubt is not settled: no later roll back is admitted over it, and the earlier one stays
		// unresolved.
		again := h.post(plans, `{"action":"restore","installation_id":"`+f.lease.installationID+`","recovery_set_id":"`+set+`"}`)
		if again.Code == 200 {
			plan := h.json(again, 200)
			again = h.post(operations, `{"plan_id":"`+h.str(plan, "plan_id")+`","plan_sha256":"`+h.str(plan, "plan_sha256")+`","idempotency_key":"`+platformFixtureID(7415)+`"}`)
		}
		if again.Code == 202 {
			t.Fatalf("a roll back was admitted over a %s image load", imageState)
		}
		if n, err := platformUnresolvedOperationCount(f.s.st.poolExec(), f.lease.installationID); err != nil || n != 2 {
			t.Fatalf("%d unresolved operations with a %s image load, want 2: %v", n, imageState, err)
		}
		return
	}
	rollBackFailedOverHTTP(t, f, h, f.op)
	expectRolledBack(t, f, h)
	if got := scalar[string](t, f.s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, first); got != "recovery_required" {
		t.Fatalf("the first roll back's record changed to %s", got)
	}
}

func TestLaterRollbackResolvesAnEarlierOneThatDidNothingSQLite(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, false, false)
}

func TestLaterRollbackResolvesAnEarlierOneThatOnlyLoadedItsImageSQLite(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, false, true)
}

func TestLaterRollbackResolvesAnEarlierOneThatOnlyLoadedItsImagePostgres(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, true, true)
}

func TestLaterRollbackResolvesAnEarlierOneThatDidNothingPostgres(t *testing.T) {
	t.Parallel()
	testLaterRollbackResolvesAnEarlierOneThatDidNothing(t, true, false)
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
	// Its backend is reachable: the root check of the operation that started it admits the roll back.
	actor := platformManagedFixtureSession(t, s)
	access, err := s.preparePlatformWritableBackendAccess(context.Background(), actor, f.lease.installationID)
	if err != nil {
		t.Fatalf("backend access after the roll back: %v", err)
	}
	defer access.Close()
	restore := scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='restore' AND stage='complete' ORDER BY completed_ms DESC LIMIT 1`, f.lease.installationID)
	verified, err := control.VerifyUpdateStartRegistration(access.intent.raw, access.intent.signature, access.intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil || verified.Start().Command().OperationID != restore {
		t.Fatalf("the backend's signed start does not name the roll back: %v", err)
	}
	// The live runtime read after the gate needs a real runner socket; what matters here is that the gate on the
	// starting operation admits a roll back instead of refusing it as missing evidence.
	if err := s.checkPlatformManagedUpdateBackendRoot(context.Background(), restore, access.intent.start.writer.runtime, access.intent.start.writer.authority, f.endpoint); errors.Is(err, errAppPlatformStageEvidence) {
		t.Fatalf("the backend root check refuses the roll back that started it: %v", err)
	}
	// The apps directory offers it to open again, not only the launch route.
	runtime := scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, f.lease.installationID)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, f.lease.installationID)
	if ok, err := platformWritableLaunchEligible(s.st.poolExec(), f.lease.installationID, runtime, epoch); err != nil || !ok {
		t.Fatalf("the rolled back installation is not eligible to open from the directory: %v", err)
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

func TestLaterRollbackKeepsAnEarlierOneWithAnImageLoadInDoubt(t *testing.T) {
	t.Parallel()
	testLaterRollbackWithEarlierImage(t, false, true, "prepared")
}

// After an ordinary roll back the capability rows are judged against the operation that completed the restored
// generation, not only the install: a roll back past a completed update reads that update's review. The install is
// recast here as such an update, which the install branch no longer matches.
func TestCapabilitySnapshotAfterARollbackReadsTheRestoredOperation(t *testing.T) {
	t.Parallel()
	f := unstartedUpdateFixture(t, false)
	h := newMigrationHTTP(t, f)
	rollBackFailedOverHTTP(t, f, h, f.op)
	s := f.s
	installation := f.lease.installationID
	install := scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install'`, installation)
	epoch := scalar[string](t, s.st, `SELECT controller_epoch FROM app_platform_installations WHERE id=?`, installation)
	release := scalar[string](t, s.st, `SELECT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, install)
	mustExec(t, s.st, `UPDATE app_platform_operations SET action='update' WHERE id=?`, install)
	planID, _, _, action, err := platformCapabilitySnapshotSource(s.st.poolExec(), installation, epoch, release)
	if err != nil || action != "update" || planID != scalar[string](t, s.st, `SELECT review_plan_id FROM app_platform_operations WHERE id=?`, install) {
		t.Fatalf("the snapshot source after the roll back is %q %q: %v", planID, action, err)
	}
	// With no roll back on the current generation an update of an earlier generation is not the source.
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='recovery_required' WHERE action='restore' AND installation_id=? AND stage='complete'`, installation)
	if _, _, _, _, err := platformCapabilitySnapshotSource(s.st.poolExec(), installation, epoch, release); err == nil {
		t.Fatal("an update of an earlier generation was read without a roll back onto the current one")
	}
}

// seedRollbackOnto records an ordinary roll back that restored the generation the completed update reached and landed
// on the next one, running the update's release again under its own fence: the shape a roll back past a completed
// update leaves.
func seedRollbackOnto(t *testing.T, s *Server, update string) (installation, restore string) {
	t.Helper()
	installation = scalar[string](t, s.st, `SELECT installation_id FROM app_platform_operations WHERE id=?`, update)
	plan, restore := platformFixtureID(7501), platformFixtureID(7502)
	raw := `{"format":"` + platformManagedRestoreFormat + `","installation_id":"` + installation + `"}`
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'restore',target_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,?,?,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, platformDigest([]byte(raw)), raw, update)
	mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,?,actor_scope,?,?,'restore',managed,target_generation,target_generation+1,'complete',state_revision,row_revision,NULL,NULL,fence+1,controller_epoch,NULL,completed_ms+1,created_ms+1,updated_ms+1
		FROM app_platform_operations WHERE id=?`, restore, plan, platformDigest([]byte(restore)), platformDigest([]byte(plan)), update)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(effect_id,operation_id,stage,effect_number,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		SELECT ?,?,stage,effect_number,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence+1,observed_fence+1,state,receipt_json,created_ms,updated_ms
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='activate' AND state='applied'`, platformFixtureID(7503), restore, update)
	mustExec(t, s.st, `UPDATE app_platform_installations SET generation=generation+1,control_fence=control_fence+1 WHERE id=?`, installation)
	mustExec(t, s.st, `UPDATE app_platform_activations SET generation=generation+1 WHERE installation_id=? AND state='active'`, installation)
	return installation, restore
}

// The apps directory opens an installation rolled back past a completed update: the update's reviewed target runs
// again on the roll back's generation. It refuses when the roll back is not the installation's fence or another
// release is active.
func TestDirectoryOpensAnInstallationRolledBackPastACompletedUpdate(t *testing.T) {
	t.Parallel()
	for _, refusal := range []string{"", "fence", "release", "not activated"} {
		t.Run("refusal="+refusal, func(t *testing.T) {
			t.Parallel()
			s, lease, intent, endpoint, obs := platformWritableUpdateStartRegistrationCommitFixture(t, false)
			next, err := s.commitPlatformUpdateStartRegistration(context.Background(), lease, intent, endpoint, obs)
			if err != nil {
				t.Fatal(err)
			}
			applied, boot := platformUpdateStartBootFixture(t, s, next, endpoint)
			defer applied.registration.Close()
			if _, err := s.commitPlatformUpdateStartActivation(context.Background(), next, applied, boot); err != nil {
				t.Fatal(err)
			}
			runtime := scalar[string](t, s.st, `SELECT runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID)
			if ok, err := platformWritableLaunchEligible(s.st.poolExec(), lease.installationID, runtime, lease.controllerEpoch); err != nil || !ok {
				t.Fatalf("fixture: the updated installation is not eligible: %v", err)
			}
			installation, restore := seedRollbackOnto(t, s, lease.id)
			switch refusal {
			case "fence":
				mustExec(t, s.st, `UPDATE app_platform_installations SET control_fence=control_fence+1 WHERE id=?`, installation)
			case "release":
				other := scalar[string](t, s.st, `SELECT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.installation_id=? AND o.action='install'`, installation)
				mustExec(t, s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, other, installation)
			case "not activated":
				mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE operation_id=?`, restore)
			}
			ok, err := platformWritableLaunchEligible(s.st.poolExec(), installation, runtime, lease.controllerEpoch)
			if err != nil || ok != (refusal == "") {
				t.Fatalf("eligible=%v, want %v: %v", ok, refusal == "", err)
			}
		})
	}
}
