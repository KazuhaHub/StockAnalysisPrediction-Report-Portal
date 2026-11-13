package app

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
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

// Interleaving 2: the removal row exists first. The load resolves it inline instead of waiting for an idle tick: a
// receipt settles it, a runner that holds nothing running it abandons it, and only a running removal is waited for.
func TestALoadResolvesAnOpenRemovalOfItsImageInline(t *testing.T) {
	cases := map[string]struct {
		setup func(f *platformFakeReclaimRunner, effect string)
		state string
		busy  bool
		want  string // the Portal row's state after the resolution
	}{
		"the runner applied it":  {func(f *platformFakeReclaimRunner, e string) { f.effects[e] = "applied" }, "", false, "applied"},
		"the runner refused it":  {func(f *platformFakeReclaimRunner, e string) { f.effects[e] = "reverted" }, "", false, "reverted"},
		"orphaned at the runner": {func(f *platformFakeReclaimRunner, e string) { f.effects[e] = "unknown" }, "", false, "reverted"},
		"never claimed":          {func(f *platformFakeReclaimRunner, e string) { f.absentOnRead = true }, "", false, "reverted"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newReclaimFixture(t)
			f.release(t, "a")
			f.load(t, "a")
			f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
			_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
			f.fake.readFails, f.fake.loseExecuteReplyOnce = false, false
			effect := f.fake.sent[0].EffectID
			delete(f.fake.effects, effect)
			tc.setup(f.fake, effect)
			if err := f.s.resolvePlatformRemovalsForLoad(context.Background(), f.endpoint, f.runtimeID, f.config("a")); err != nil {
				t.Fatalf("the load did not get past the open removal: %v", err)
			}
			if got := scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`); got != tc.want {
				t.Fatalf("removal row %q, want %q", got, tc.want)
			}
			if len(f.fake.sent) != 1 {
				t.Fatal("resolving a removal sent it again")
			}
		})
	}
}

// A removal the runner is still doing is a bounded wait, not an abandonment.
func TestALoadWaitsOnlyForARemovalTheRunnerIsStillDoing(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
	_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	f.fake.readFails = false
	f.fake.effects[f.fake.sent[0].EffectID] = "unknown"
	f.fake.readPending = true
	err := f.s.resolvePlatformRemovalsForLoad(context.Background(), f.endpoint, f.runtimeID, f.config("a"))
	if !errors.Is(err, errPlatformImageBusy) {
		t.Fatalf("a running removal: %v", err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "unknown" {
		t.Fatal("a running removal was abandoned")
	}
	// A removal of another image is not waited for.
	if err := f.s.resolvePlatformRemovalsForLoad(context.Background(), f.endpoint, f.runtimeID, platformDigest([]byte("another"))); err != nil {
		t.Fatalf("a removal of another image held the load: %v", err)
	}
}

// Reinstalling the same app while its removal has no verdict completes: the load path resolves the removal and goes
// on, and the image is loaded.
func TestReinstallCompletesWhileItsRemovalIsUnknown(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	runtimeID := scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings WHERE enabled=1 LIMIT 1`)
	installOp := scalar[string](t, s.st, `SELECT id FROM app_platform_operations WHERE installation_id=? AND action='install'`, lease.installationID)
	mustExec(t, s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'stage',9,?,'remove_image',?,?,?,1,0,'unknown',?,1,1)`, installOp, platformFixtureID(9301), platformDigest([]byte("removal")), runtimeID, lease.controllerEpoch,
		`{"intent":{"release_sha256":"`+platformDigest(parts.descriptor)+`","config_digest":"`+parts.entry.ConfigDigest+`"}}`)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatalf("the reinstall did not complete beside an unknown removal: %v", err)
	}
	if !fake.present || fake.count("load") == 0 {
		t.Fatal("the image was not loaded")
	}
	if scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) == "unknown" {
		t.Fatal("the open removal was left open")
	}
}

// An open row whose intent cannot be read is open: it is listed, blocks nothing silently, and is closed.
func TestAnUnreadableOpenRemovalRowFailsClosed(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'stage',9,?,'remove_image',?,?,?,1,0,'unknown','{}',1,1)`, f.hostOp, platformFixtureID(9302), platformDigest([]byte("x")), f.runtimeID, f.epoch)
	open, err := f.s.platformOpenRemovalsFor(context.Background(), f.runtimeID, f.config("a"))
	if err != nil || len(open) != 1 || !open[0].unparsable {
		t.Fatalf("an unreadable open row is not reported open: %+v %v", open, err)
	}
	if err := f.s.resolvePlatformRemovalsForLoad(context.Background(), f.endpoint, f.runtimeID, f.config("a")); err != nil {
		t.Fatal(err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "reverted" {
		t.Fatal("the unreadable row was not closed")
	}
}

// More than the old 64-row window of open removals are all seen.
func TestOpenRemovalsAreNotWindowed(t *testing.T) {
	f := newReclaimFixture(t)
	for i := range 70 {
		mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,?,?,?,'remove_image',?,?,?,1,0,'unknown','{}',1,1)`, f.hostOp, "w"+strconv.Itoa(i/32), i%32+1, platformFixtureID(9500+i), platformDigest([]byte(strconv.Itoa(i))), f.runtimeID, f.epoch)
	}
	open, err := f.s.platformOpenRemovals(context.Background())
	if err != nil || len(open) != 70 {
		t.Fatalf("%d open removals listed: %v", len(open), err)
	}
}

// After the attempts are used the image is reported by the storage status with the last typed reason, and nothing is
// sent again.
func TestReclaimStopsAfterTheAttemptCapAndReportsTheImage(t *testing.T) {
	f := newReclaimFixture(t)
	noBackoff(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.inUse[f.config("a")] = true
	for range platformReclaimMaxAttempts {
		if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
			t.Fatalf("attempt: %d %v", n, err)
		}
	}
	sends := len(f.fake.sent)
	for range 3 {
		if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 0 {
			t.Fatalf("after the cap: %d %v", n, err)
		}
	}
	if len(f.fake.sent) != sends || f.removalRows(t) != platformReclaimMaxAttempts {
		t.Fatalf("the step kept trying: %d sends, %d rows", len(f.fake.sent), f.removalRows(t))
	}
	status, err := f.s.platformImageStorageStatus(context.Background())
	if err != nil || len(status.ReclaimRefused) != 1 {
		t.Fatalf("storage status: %+v %v", status, err)
	}
	got := status.ReclaimRefused[0]
	if got.ConfigDigest != f.config("a") || got.Attempts != platformReclaimMaxAttempts || got.Reason != "image_in_use" {
		t.Fatalf("reported refusal: %+v", got)
	}
}

// An unreadable recovery pause stops the step, and a pause set after the sweep began stops the insert.
func TestReclaimFailsClosedOnTheRecoveryPause(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	target := f.target(t, "a")
	mustExec(t, f.s.st, `INSERT INTO meta(k,v) VALUES('app_platform_restore_paused','1') ON CONFLICT(k) DO UPDATE SET v='1'`)
	if done, err := f.s.reclaimPlatformImage(context.Background(), f.endpoint, target, true); err != nil || done {
		t.Fatalf("reclaim with the pause set after the sweep began: %v %v", done, err)
	}
	if f.removalRows(t) != 0 || len(f.fake.calls) != 0 {
		t.Fatal("a removal row was inserted during the recovery pause")
	}
	mustExec(t, f.s.st, `UPDATE meta SET v='0' WHERE k='app_platform_restore_paused'`)
	mustExec(t, f.s.st, `ALTER TABLE meta RENAME TO meta_gone`)
	n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	mustExec(t, f.s.st, `ALTER TABLE meta_gone RENAME TO meta`)
	if err == nil || n != 0 || len(f.fake.calls) != 0 {
		t.Fatalf("an unreadable pause did not stop the step: %d %v %v", n, err, f.fake.calls)
	}
}

// The settlement closure ends with the transaction that used it: the operation's ending hands later calls a context
// without it.
func TestTheEffectSettlementDoesNotOutliveTheTransactionThatRanIt(t *testing.T) {
	ctx := withPlatformEffectSettlement(context.Background(), func(migExec, platformLeaseFacts) error { return nil })
	if settle, _ := ctx.Value(platformEffectSettlementKey{}).(platformEffectSettlement); settle == nil {
		t.Fatal("setup: the closure is not carried")
	}
	stripped := withPlatformEffectSettlement(ctx, nil)
	if settle, _ := stripped.Value(platformEffectSettlementKey{}).(platformEffectSettlement); settle != nil {
		t.Fatal("the closure survived the strip")
	}
	raw, err := os.ReadFile("app_platform_install_scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	want := "ctx = withPlatformEffectSettlement(ctx, nil)\n\tw.server.sweepPlatformImages(ctx)\n\tw.reclaimImages(ctx)"
	if !strings.Contains(source, want) {
		t.Fatal("finishAs hands its settlement closure to the sweeps that follow it")
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

// Tests run the retry backoff at zero; this pins the production backoff (ten minutes per attempt made, capped at six
// hours) on the constant and the function, without sleeping through it.
func TestTheProductionRetryBackoffIsTenMinutesPerAttempt(t *testing.T) {
	if platformReclaimBackoffDefault != 10*time.Minute || platformReclaimBackoffCap != 6*time.Hour {
		t.Fatal("the documented backoff constants changed")
	}
	old := platformReclaimBackoff
	platformReclaimBackoff = platformReclaimBackoffDefault
	defer func() { platformReclaimBackoff = old }()
	if platformReclaimWait(1) != 10*time.Minute || platformReclaimWait(3) != 30*time.Minute || platformReclaimWait(100) != 6*time.Hour {
		t.Fatalf("backoff by attempt: %s %s %s", platformReclaimWait(1), platformReclaimWait(3), platformReclaimWait(100))
	}
}
