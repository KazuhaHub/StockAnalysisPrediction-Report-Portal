package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// otherInstallation adds a second installation that still exists (n makes ids unique) and returns its id.
func (f *reclaimFixture) otherInstallation(t *testing.T, n int) string {
	t.Helper()
	id := platformFixtureID(9000 + n)
	mustExec(t, f.s.st, `INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms)
		SELECT ?,?,publisher_id,runtime_id,'enabled','ready',generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms FROM app_platform_installations WHERE id=?`, id, "com.example.other"+strconv.Itoa(n), f.installation)
	return id
}

// otherOperation clones the host operation and its review plan for another installation, naming the release the
// plan targets and the state the operation is in.
func (f *reclaimFixture) otherOperation(t *testing.T, n int, installation, release, stage string, completed bool) {
	t.Helper()
	plan, op := platformFixtureID(9100+n), platformFixtureID(9200+n)
	mustExec(t, f.s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,?,app_id,publisher_id,'update',source_release_sha256,?,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,1,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, installation, release, f.hostOp)
	var done any
	if completed {
		done = int64(1)
	}
	mustExec(t, f.s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		SELECT ?,?,?,actor_scope,?,?,'update',managed,source_generation,target_generation,?,state_revision,row_revision,NULL,NULL,fence,controller_epoch,NULL,?,created_ms,updated_ms
		FROM app_platform_operations WHERE id=?`, op, installation, plan, platformDigest([]byte(op)), platformDigest([]byte(plan)), stage, done, f.hostOp)
}

// prepared returns the target for the image of the named release, as the sweep computed it.
func (f *reclaimFixture) target(t *testing.T, name string) platformReclaimTarget {
	t.Helper()
	targets, _, err := f.s.platformReclaimTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.intent.ConfigDigest == f.config(name) {
			return target
		}
	}
	t.Fatalf("the image of %s is not a removal target", name)
	return platformReclaimTarget{}
}

// Interleaving 1: the operation is admitted after the references were computed and before the row is inserted. The
// row is computed again in the inserting transaction, so the removal is not started.
func TestReclaimRecomputesTheReferencesInTheTransactionThatInsertsTheRow(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	target := f.target(t, "a")
	f.setActive(t, "a") // an installation now uses the release
	done, err := f.s.reclaimPlatformImage(context.Background(), f.endpoint, target, true)
	if err != nil || done {
		t.Fatalf("reclaim: %v %v", done, err)
	}
	if f.removalRows(t) != 0 || len(f.fake.calls) != 0 {
		t.Fatalf("a removal of a referenced image was started: rows=%d calls=%v", f.removalRows(t), f.fake.calls)
	}
}

// The same, for a load of the same image applied after the watermark was taken.
func TestReclaimDoesNotInsertARowWhenAnotherLoadWasAppliedAfterTheWatermark(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	target := f.target(t, "a")
	f.load(t, "a") // a second installation's load answered present
	if done, err := f.s.reclaimPlatformImage(context.Background(), f.endpoint, target, true); err != nil || done {
		t.Fatalf("reclaim: %v %v", done, err)
	}
	if f.removalRows(t) != 0 || len(f.fake.calls) != 0 {
		t.Fatal("a removal was started with a stale watermark")
	}
	// With the watermark current the image is removed (both loads are named).
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("reclaim with a current watermark: %d %v", n, err)
	}
	if got := f.fake.sent[0].Payload.KnownLoads; len(got) != 2*36+1 {
		t.Fatalf("the command's watermark is %q, want both loads", got)
	}
}

// Interleaving 2: the removal row exists first. A load of that image waits for it and sends nothing.
func TestALoadOfAnImageWaitsWhileItsRemovalHasNoVerdict(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	runtimeID := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings WHERE enabled=1 LIMIT 1`)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'stage',9,?,'remove_image',?,?,?,1,0,'unknown',?,1,1)`, lease.id, platformFixtureID(9301), platformDigest([]byte("removal")), runtimeID, lease.controllerEpoch,
		`{"intent":{"config_digest":"`+parts.entry.ConfigDigest+`"}}`)
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageBusy) {
		t.Fatalf("a load beside an open removal of its image: %v", err)
	}
	if fake.count("load") != 0 || fake.count("blob:") != 0 {
		t.Fatalf("the load was sent: %v", fake.calls)
	}
	// A removal of another image does not hold it.
	mustExec(t, s.st, `UPDATE app_platform_operation_effects SET receipt_json=? WHERE kind='remove_image'`, `{"intent":{"config_digest":"`+platformDigest([]byte("another image"))+`"}}`)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatalf("a removal of another image held the load: %v", err)
	}
}

// An open removal whose image was loaded again is not completed: the watermark no longer names every load.
func TestReclaimAbandonsAnOpenRemovalWhoseWatermarkIsStale(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
	_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	f.fake.readFails = false
	f.load(t, "a")
	sends := f.fake.count("remove")
	if _, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil {
		t.Fatal(err)
	}
	if f.fake.count("remove") != sends {
		t.Fatal("a removal with a stale watermark was completed")
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "reverted" {
		t.Fatal("the stale removal was not closed")
	}
}

// A refused removal is tried again, as a new effect, after its backoff; an applied one never is.
func TestReclaimRetriesARefusedRemovalAsANewAttemptAfterTheBackoff(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.inUse[f.config("a")] = true
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("first attempt: %d %v", n, err)
	}
	calls := len(f.fake.calls)
	if _, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || len(f.fake.calls) != calls {
		t.Fatal("a refused removal was retried before its backoff")
	}
	noBackoff(t)
	f.fake.conflict = true // the daemon's own conflict is retryable as well
	for attempt := 1; attempt <= 2; attempt++ {
		if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
			t.Fatalf("attempt %d: %d %v", attempt, n, err)
		}
	}
	ids := map[string]bool{}
	for _, c := range f.fake.sent {
		ids[c.EffectID] = true
	}
	if len(ids) != 3 || f.removalRows(t) != 3 {
		t.Fatalf("attempts are not distinct effects: %d ids, %d rows", len(ids), f.removalRows(t))
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='remove_image' AND state='reverted'`) != 3 {
		t.Fatal("the refusals are not recorded as reverted")
	}
	// Once the container is gone the next attempt removes the image, and nothing follows it.
	delete(f.fake.inUse, f.config("a"))
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 || len(f.fake.removed) != 1 {
		t.Fatalf("attempt after the container went: %d %v %v", n, err, f.fake.removed)
	}
	calls = len(f.fake.calls)
	if _, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || len(f.fake.calls) != calls {
		t.Fatal("a removal was sent after one was applied")
	}
}

// An unknown outcome is never started again as a new attempt: it is read back under its own effect.
func TestReclaimNeverStartsANewAttemptWhileARemovalHasNoVerdict(t *testing.T) {
	f := newReclaimFixture(t)
	noBackoff(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
	_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	f.fake.readFails, f.fake.absentOnRead = true, false
	for range 3 {
		_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	}
	if f.removalRows(t) != 1 {
		t.Fatalf("%d removal rows: a second attempt began beside an open one", f.removalRows(t))
	}
}

// While the Portal is paused for recovery the step sends nothing.
func TestReclaimWaitsForTheRecoveryPauseToEnd(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	mustExec(t, f.s.st, `INSERT INTO meta(k,v) VALUES('app_platform_restore_paused','1') ON CONFLICT(k) DO UPDATE SET v='1'`)
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 0 || len(f.fake.calls) != 0 {
		t.Fatalf("reclaim during the recovery pause: %d %v %v", n, err, f.fake.calls)
	}
	mustExec(t, f.s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("reclaim after the pause: %d %v", n, err)
	}
}

// Each test below needs an image nothing references (the baseline target) and one reason to keep it. Removing the
// pin query that reason rests on makes its test fail.
func pinFixture(t *testing.T) *reclaimFixture {
	f := newReclaimFixture(t)
	f.release(t, "x")
	f.load(t, "x")
	if !f.targets(t)[f.config("x")] {
		t.Fatal("the baseline image is not a target")
	}
	return f
}

func TestReclaimKeepsTheImageOfAnUnfinishedOperationsRelease(t *testing.T) {
	for _, stage := range []string{"queued", "migrate"} {
		f := pinFixture(t)
		f.otherOperation(t, 1, f.otherInstallation(t, 1), f.shas["x"], stage, false)
		if f.targets(t)[f.config("x")] {
			t.Fatalf("an operation at %s does not keep its release's image", stage)
		}
	}
}

func TestReclaimKeepsTheImageOfAnUnrecoveredFailure(t *testing.T) {
	f := pinFixture(t)
	f.otherOperation(t, 2, f.otherInstallation(t, 2), f.shas["x"], "recovery_required", true)
	if f.targets(t)[f.config("x")] {
		t.Fatal("an operation that ended recovery_required does not keep its release's image")
	}
}

func TestReclaimKeepsTheImageOfAnOpenReviewPlan(t *testing.T) {
	f := pinFixture(t)
	mustExec(t, f.s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,'update',source_release_sha256,?,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,?,NULL,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`,
		platformFixtureID(9150), f.shas["x"], time.Now().Add(time.Hour).UnixMilli(), f.hostOp)
	if f.targets(t)[f.config("x")] {
		t.Fatal("an open review plan does not keep its release's image")
	}
}

// Two releases carry the same image: the one still needed keeps it for the one that is not.
func TestReclaimKeepsAnImageASecondReleaseStillCarries(t *testing.T) {
	f := newReclaimFixture(t)
	f.releaseSeed(t, "old", "shared-image")
	f.releaseSeed(t, "new", "shared-image")
	if f.config("old") != f.config("new") {
		t.Fatal("the fixture's releases do not share an image")
	}
	f.load(t, "old")
	if !f.targets(t)[f.config("old")] {
		t.Fatal("the baseline image is not a target")
	}
	f.setActive(t, "new")
	if f.targets(t)[f.config("old")] {
		t.Fatal("an image a pinned release also carries was removed")
	}
}

// An image loaded on another runtime is never removed through this runtime's endpoint.
func TestReclaimNeverSendsAnImageOfAnotherRuntimeToThisOne(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	other := platformFixtureID(9400)
	mustExec(t, f.s.st, `INSERT INTO app_platform_runtime_bindings(id,deployment_id,controller_epoch,control_revision,enabled,verification_key_fingerprint,sealed_control_key,revision,created_ms,updated_ms)
		SELECT ?,?,controller_epoch,control_revision,enabled,verification_key_fingerprint,sealed_control_key,revision,created_ms,updated_ms FROM app_platform_runtime_bindings WHERE id=?`, other, platformFixtureID(9401), f.runtimeID)
	f.runtimeID = other
	f.load(t, "a")
	if len(f.targets(t)) != 1 {
		t.Fatal("the baseline image is not a target")
	}
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 0 || len(f.fake.calls) != 0 || f.removalRows(t) != 0 {
		t.Fatalf("another runtime's image was sent to this runtime: %d %v %v", n, err, f.fake.calls)
	}
}

func TestRemoveImageWatermarkFitsTheJournalBound(t *testing.T) {
	if control.MaxKnownLoads < 8 {
		t.Fatal("the watermark bound is too small to be useful")
	}
}
