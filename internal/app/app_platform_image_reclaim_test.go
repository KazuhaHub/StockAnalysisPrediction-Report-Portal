package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// platformFakeRemoveReply answers as a runner would for one remove-image command.
type platformFakeRemoveReply struct {
	platformFakeObservation
	receipt *journal.RemoveImageReceipt
	status  *journal.RemoveImageStatus
}

func (r platformFakeRemoveReply) RemoveImageReceipt() (journal.RemoveImageReceipt, error) {
	if r.receipt == nil {
		return journal.RemoveImageReceipt{}, errFakeNoSuchReply
	}
	return *r.receipt, nil
}

func (r platformFakeRemoveReply) RemoveImageStatus() (journal.RemoveImageStatus, error) {
	if r.status == nil {
		return journal.RemoveImageStatus{}, errFakeNoSuchReply
	}
	return *r.status, nil
}

// platformFakeReclaimRunner models the runner's removal behaviour that matters to the sender: an effect journal keyed
// by effect id and the daemon's image list. Loads are never expected.
type platformFakeReclaimRunner struct {
	platformImageRunner
	t        *testing.T
	endpoint platformRuntimeEndpoint
	mu       sync.Mutex
	calls    []string
	effects  map[string]string // effect id -> "unknown" | "applied" | "reverted"
	removed  []string          // config digests removed, in order
	sent     []control.RemoveImageCommand
	// inUse makes the runner refuse every removal because a container uses the image.
	inUse map[string]bool
	// loseExecuteReplyOnce makes an execute run on the runner and then fail on the wire.
	loseExecuteReplyOnce bool
	// readFails makes every read-back fail on the wire.
	readFails bool
	// absentOnRead answers a read-back with the typed absence, as a runner that never claimed the effect.
	absentOnRead bool
	pendingFor   int
	// readPending answers every read-back of an unknown effect with pending, as a runner whose gate is held.
	readPending bool
	// conflict makes a refusal the daemon's own conflict rather than a container using the image.
	conflict bool
}

func newPlatformFakeReclaimRunner(t *testing.T, endpoint platformRuntimeEndpoint) *platformFakeReclaimRunner {
	return &platformFakeReclaimRunner{t: t, endpoint: endpoint, effects: map[string]string{}, inUse: map[string]bool{}}
}

func (f *platformFakeReclaimRunner) count(name string) int {
	n := 0
	for _, c := range f.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (f *platformFakeReclaimRunner) receipt(call platformRemoveImageCall, state, outcome string) *journal.RemoveImageReceipt {
	c := call.command
	now := platformFakeFirstObserved(c.EffectID)
	at, _ := strconv.ParseInt(now, 10, 64)
	return &journal.RemoveImageReceipt{
		Format: "portal.runner.remove-image-receipt/1-dev.1",
		Claim: journal.RemoveImageClaim{
			Format: "portal.runner.remove-image-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", c.EffectID), EffectID: c.EffectID,
			ReleaseSHA256: c.Payload.ReleaseSHA256, Platform: c.Payload.Platform, ManifestDigest: c.Payload.ManifestDigest,
			ConfigDigest: c.Payload.ConfigDigest, Layers: c.Payload.Layers, AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(at-1, 10),
		},
		DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
		TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		ImageID: "sha256:" + c.Payload.ConfigDigest, Store: "classic", State: state, Outcome: outcome, ObservedMS: now, SettledMS: now,
	}
}

func (f *platformFakeReclaimRunner) statusOf(call platformRemoveImageCall, state string) *journal.RemoveImageStatus {
	c := call.command
	return &journal.RemoveImageStatus{
		Format: "portal.runner.remove-image-status/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

func (f *platformFakeReclaimRunner) answer(call platformRemoveImageCall) platformFakeRemoveReply {
	reply := platformFakeRemoveReply{platformFakeObservation: platformFakeProof(call.authority, f.endpoint, call.raw)}
	switch f.effects[call.command.EffectID] {
	case "applied":
		reply.receipt = f.receipt(call, "removed", "")
	case "reverted":
		if f.conflict {
			reply.receipt = f.receipt(call, "conflict", "image_conflict")
		} else {
			reply.receipt = f.receipt(call, "in_use", "image_in_use")
		}
	}
	return reply
}

func (f *platformFakeReclaimRunner) RemoveImage(_ context.Context, _ platformRuntimeEndpoint, call platformRemoveImageCall) (platformRemoveImageReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "remove")
	f.sent = append(f.sent, call.command)
	if _, err := control.VerifyRemoveImage(call.raw, call.signature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the command: %v", err)
		return nil, err
	}
	if f.pendingFor > 0 {
		f.pendingFor--
		reply := platformFakeRemoveReply{platformFakeObservation: platformFakeProof(call.authority, f.endpoint, call.raw)}
		reply.status = f.statusOf(call, "pending")
		return reply, nil
	}
	if f.effects[call.command.EffectID] == "" {
		f.effects[call.command.EffectID] = "unknown"
	}
	if f.effects[call.command.EffectID] == "unknown" {
		if f.inUse[call.command.Payload.ConfigDigest] {
			f.effects[call.command.EffectID] = "reverted"
		} else {
			f.effects[call.command.EffectID] = "applied"
			f.removed = append(f.removed, call.command.Payload.ConfigDigest)
		}
	}
	reply := f.answer(call)
	if f.loseExecuteReplyOnce {
		f.loseExecuteReplyOnce = false
		return nil, errors.New("connection lost after the runner ran the command")
	}
	return reply, nil
}

func (f *platformFakeReclaimRunner) ReadRemoveImage(_ context.Context, _ platformRuntimeEndpoint, call platformRemoveImageCall) (platformRemoveImageReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read")
	if f.readFails {
		return nil, errors.New("read-back lost on the wire")
	}
	if _, err := control.VerifyRemoveImageRead(call.raw, call.readSignature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the read: %v", err)
		return nil, err
	}
	if f.absentOnRead || f.effects[call.command.EffectID] == "" {
		return nil, errPlatformMigrationAbsent
	}
	reply := f.answer(call)
	if reply.receipt != nil {
		return reply, nil
	}
	if f.readPending {
		reply.status = f.statusOf(call, "pending")
		return reply, nil
	}
	reply.status = f.statusOf(call, "orphaned")
	return reply, nil
}

// reclaimFixture is a managed installation whose operation is finished, a runtime that can be sent commands, and
// helpers to say which releases exist, which have images loaded and which are referenced.
type reclaimFixture struct {
	s            *Server
	endpoint     platformRuntimeEndpoint
	fake         *platformFakeReclaimRunner
	hostOp       string
	installation string
	runtimeID    string
	epoch        string
	releases     map[string]platformManagedImageParts
	shas         map[string]string
	loads        int
	clock        int64
}

func newReclaimFixture(t *testing.T) *reclaimFixture {
	t.Helper()
	s, lease, endpoint, _, _ := platformImageEnsureFixture(t)
	mustExec(t, s.st, `UPDATE app_platform_operations SET stage='complete',completed_ms=9999999999999,lease_owner=NULL,lease_until_ms=NULL WHERE id=?`, lease.id)
	f := &reclaimFixture{
		s: s, endpoint: endpoint, hostOp: lease.id, installation: lease.installationID, epoch: lease.controllerEpoch,
		releases: map[string]platformManagedImageParts{}, shas: map[string]string{}, clock: 5000,
	}
	f.runtimeID = scalar[string](t, s.st, `SELECT id FROM app_platform_runtime_bindings WHERE enabled=1 LIMIT 1`)
	f.fake = newPlatformFakeReclaimRunner(t, endpoint)
	s.platformImageRunner = f.fake
	// Nothing the fixture inherited is loaded or pinned beyond what a test says.
	mustExec(t, s.st, `DELETE FROM app_platform_operation_effects WHERE kind='load_image'`)
	return f
}

// release adds a managed release with images of its own and returns its sha.
func (f *reclaimFixture) release(t *testing.T, name string) string {
	return f.releaseSeed(t, name, "reclaim-"+name)
}

// releaseSeed is release with the image seed named, so two releases can share an image.
func (f *reclaimFixture) releaseSeed(t *testing.T, name, seed string) string {
	t.Helper()
	parts := newPlatformManagedImageParts(t, "7."+strconv.Itoa(len(f.releases)+1)+".0", seed)
	like := scalar[string](t, f.s.st, `SELECT release_sha256 FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation)
	sha := platformInsertManagedRelease(t, f.s, like, parts)
	f.releases[name], f.shas[name] = parts, sha
	return sha
}

func (f *reclaimFixture) config(name string) string { return f.releases[name].entry.ConfigDigest }

// load records an applied load of the release's image on the runtime, as the load effect would have left it.
func (f *reclaimFixture) load(t *testing.T, name string) string {
	t.Helper()
	f.loads++
	f.clock += 10
	parts := f.releases[name]
	effect := platformFixtureID(8800 + f.loads)
	receipt := journal.LoadImageReceipt{
		Format: "portal.runner.load-image-receipt/1-dev.1",
		Claim: journal.LoadImageClaim{
			Format: "portal.runner.load-image-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", effect), EffectID: effect,
			ReleaseSHA256: f.shas[name], Platform: parts.entry.Platform, ManifestDigest: parts.entry.ManifestDigest, ConfigDigest: parts.entry.ConfigDigest,
			Layers: control.FormatLoadImageLayers(parts.entry.Layers), AdmittedFence: "1", ClaimedMS: "100",
		},
		DeploymentID: platformFixtureID(8700), ControllerEpoch: f.epoch, InstallationID: f.installation, OperationID: f.hostOp,
		TargetGeneration: "1", Stage: "stage", EffectID: effect, PayloadSHA256: platformDigest([]byte(effect)),
		ImageID: "sha256:" + parts.entry.ConfigDigest, Store: "classic", State: "loaded", ObservedMS: "200", SettledMS: "300",
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ParseLoadImageReceipt(raw); err != nil {
		t.Fatal("the fixture load receipt is not a valid receipt:", err)
	}
	number := scalar[int](t, f.s.st, `SELECT COALESCE(MAX(effect_number),0)+1 FROM app_platform_operation_effects WHERE operation_id=? AND stage='stage'`, f.hostOp)
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'stage',?,?,'load_image',?,?,?,1,1,'applied',?,?,?)`, f.hostOp, number, effect, receipt.PayloadSHA256, f.runtimeID, f.epoch, string(raw), f.clock, f.clock)
	return effect
}

// setActive makes the release the installation's active one.
func (f *reclaimFixture) setActive(t *testing.T, name string) {
	t.Helper()
	mustExec(t, f.s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, f.shas[name], f.installation)
}

func (f *reclaimFixture) targets(t *testing.T) map[string]bool {
	t.Helper()
	targets, _, err := f.s.platformReclaimTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, target := range targets {
		out[target.intent.ConfigDigest] = true
	}
	return out
}

func (f *reclaimFixture) removalRows(t *testing.T) int {
	return scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='remove_image'`)
}

// noBackoff lets a refused removal be retried at once for the length of the test.
func noBackoff(t *testing.T) {
	t.Helper()
	old := platformReclaimBackoff
	platformReclaimBackoff = 0
	t.Cleanup(func() { platformReclaimBackoff = old })
}

func TestReclaimComputesOnlyTheImagesNothingReferences(t *testing.T) {
	f := newReclaimFixture(t)
	for _, name := range []string{"old", "active", "previous", "restorable", "shared", "free"} {
		f.release(t, name)
		f.load(t, name)
	}
	f.setActive(t, "active")
	// The release a completed update moved the installation away from is kept for rollback.
	mustExec(t, f.s.st, `UPDATE app_platform_review_plans SET action='update',source_release_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, f.shas["previous"], f.hostOp)
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET action='update' WHERE id=?`, f.hostOp)
	// A retained recovery checkpoint could restore this release.
	namespace := scalar[string](t, f.s.st, `SELECT data_namespace_id FROM app_platform_activations WHERE installation_id=? AND state='active'`, f.installation)
	platformTestInsert(t, f.s.st.poolExec(), "recovery_sets", map[string]any{
		"id": platformFixtureID(8901), "installation_id": f.installation, "data_namespace_id": namespace, "operation_id": f.hostOp,
		"release_sha256": f.shas["restorable"], "data_format": 1, "checkpoint_id": platformFixtureID(8902), "manifest_sha256": platformDigest([]byte("x")),
		"bytes": 1, "state": "verified", "controller_epoch": f.epoch, "writer_fence": 1, "verified_ms": int64(1), "deleted_ms": nil, "created_ms": 1, "updated_ms": 1,
	})
	// Another installation that still exists keeps its release, whatever the first one moved on to.
	other := platformFixtureID(8910)
	mustExec(t, f.s.st, `INSERT INTO app_platform_installations(id,app_id,publisher_id,runtime_id,desired_state,phase,generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms)
		SELECT ?,?,publisher_id,runtime_id,'enabled','ready',generation,revision,policy_revision,control_fence,controller_epoch,sealed_identity_seed,created_ms,updated_ms FROM app_platform_installations WHERE id=?`, other, "com.example.other", f.installation)
	mustExec(t, f.s.st, `INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
		SELECT ?,?,?,generation,policy_revision,NULL,data_format,'active',readiness_evidence,observed_ms,created_ms,updated_ms FROM app_platform_activations WHERE installation_id=? AND state='active'`, platformFixtureID(8911), other, f.shas["shared"], f.installation)

	got := f.targets(t)
	want := map[string]bool{f.config("old"): true, f.config("free"): true}
	if len(got) != len(want) || !got[f.config("old")] || !got[f.config("free")] {
		t.Fatalf("unreferenced images: got %v want %v (active=%s previous=%s restorable=%s shared=%s)", got, want, f.config("active"), f.config("previous"), f.config("restorable"), f.config("shared"))
	}
	for _, kept := range []string{"active", "previous", "restorable", "shared"} {
		if got[f.config(kept)] {
			t.Fatalf("the image of the %s release would be removed", kept)
		}
	}
	// The installation that held the shared release goes away: its image is now referenced by nobody else, but the
	// other installation was the holder, so it stays until that one is removed too.
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, other)
	if !f.targets(t)[f.config("shared")] {
		t.Fatal("the image of a removed installation's release is still kept")
	}
}

// The removed installation itself references nothing: removing an installation frees its releases, which is the
// uninstall case of the sweep.
func TestReclaimAfterAnUninstallFreesTheRemovedInstallationsImages(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "app")
	f.load(t, "app")
	f.setActive(t, "app")
	if f.targets(t)[f.config("app")] {
		t.Fatal("the image of a live installation's active release is removed")
	}
	mustExec(t, f.s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, f.installation)
	if !f.targets(t)[f.config("app")] {
		t.Fatal("the image of a removed installation is not reclaimed")
	}
}

// Fails if a release whose descriptor cannot be read lets anything be removed: with the references in doubt the
// image stays.
func TestReclaimFailsClosedWhenAReferencedReleaseCannotBeRead(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.release(t, "b")
	f.setActive(t, "b")
	mustExec(t, f.s.st, `UPDATE app_platform_releases SET descriptor=? WHERE sha256=?`, []byte("not a descriptor"), f.shas["b"])
	if _, _, err := f.s.platformReclaimTargets(context.Background()); err == nil {
		t.Fatal("the computation answered with a pinned release it could not read")
	}
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err == nil || n != 0 || len(f.fake.calls) != 0 {
		t.Fatalf("a removal was sent with the references unknown: %d %v %v", n, err, f.fake.calls)
	}
}

// A load with no verdict may be introducing any image: nothing on that runtime is removed meanwhile.
func TestReclaimWaitsForALoadWithNoVerdict(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.release(t, "b")
	f.setActive(t, "b")
	if !f.targets(t)[f.config("a")] {
		t.Fatal("setup: the image is not a target")
	}
	mustExec(t, f.s.st, `INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,'stage',30,?,'load_image',?,?,?,1,0,'unknown','{}',9000,9000)`, f.hostOp, platformFixtureID(8990), platformDigest([]byte("pending")), f.runtimeID, f.epoch)
	if len(f.targets(t)) != 0 {
		t.Fatal("an image is removed while a load has no verdict")
	}
}

func TestReclaimSendsOneSignedRemovalPerImageAndNeverAgainOnceApplied(t *testing.T) {
	f := newReclaimFixture(t)
	for _, name := range []string{"a", "b"} {
		f.release(t, name)
		f.load(t, name)
	}
	f.release(t, "live")
	f.load(t, "live")
	f.setActive(t, "live")
	n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	if err != nil || n != 2 {
		t.Fatalf("reclaim settled %d: %v", n, err)
	}
	if len(f.fake.removed) != 2 || (f.fake.removed[0] != f.config("a") && f.fake.removed[1] != f.config("a")) {
		t.Fatalf("removed %v", f.fake.removed)
	}
	for _, c := range f.fake.sent {
		if c.Kind != "remove-image" || c.Stage != "stage" || mustInt64(c.EffectNumber) < 2 || c.OperationID != f.hostOp || c.InstallationID != f.installation {
			t.Fatalf("a removal command is not shaped as designed: %+v", c.CommandHeader)
		}
	}
	if f.fake.sent[0].EffectNumber == f.fake.sent[1].EffectNumber || f.fake.sent[0].EffectID == f.fake.sent[1].EffectID {
		t.Fatal("two removals share an effect number or id")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='remove_image' AND state='applied'`) != 2 {
		t.Fatal("the removals are not settled as applied")
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM audit_log WHERE action='app_platform.image_removed'`) != 2 {
		t.Fatal("the removals are not audited")
	}
	// The image of the active release was not touched, and a second pass sends nothing.
	before := len(f.fake.calls)
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 0 || len(f.fake.calls) != before {
		t.Fatalf("a second pass sent more: %d %v %v", n, err, f.fake.calls[before:])
	}
	// The effect id is the one derived from the host operation and the load it undoes.
	loadEffect := scalar[string](t, f.s.st, `SELECT effect_id FROM app_platform_operation_effects WHERE kind='load_image' AND receipt_json LIKE ?`, "%"+f.config("a")+"%")
	want := platformRemoveImageEffectID(f.hostOp, loadEffect, 0)
	found := false
	for _, c := range f.fake.sent {
		found = found || c.EffectID == want
	}
	if !found {
		t.Fatal("the effect id is not derived from the host operation and the load effect")
	}
}

// An image loaded again after it was removed is a target again; one removed and not reloaded is not.
func TestReclaimTreatsAReloadAfterARemovalAsLoadedAgain(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("first reclaim: %d %v", n, err)
	}
	if len(f.targets(t)) != 0 {
		t.Fatal("a removed image is a target again")
	}
	f.clock = time.Now().UnixMilli() + 60000
	f.load(t, "a")
	if !f.targets(t)[f.config("a")] {
		t.Fatal("an image loaded again after its removal is not a target")
	}
}

// A container using the image is the runner's refusal: the row ends reverted, and the same host does not resend.
func TestReclaimRecordsTheRunnersInUseRefusalAndDoesNotResend(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.inUse[f.config("a")] = true
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("reclaim: %d %v", n, err)
	}
	if scalar[int](t, f.s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE kind='remove_image' AND state='reverted'`) != 1 {
		t.Fatal("the refusal is not recorded as reverted")
	}
	calls := len(f.fake.calls)
	if _, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || len(f.fake.calls) != calls {
		t.Fatalf("a refused removal was sent again by the same host: %v", f.fake.calls[calls:])
	}
}

// A lost reply is read back under the same identity, and an unknown removal is never sent again before the read.
func TestReclaimReadsAnUncertainExecuteBackUnderItsOwnIdentity(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce = true
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("reclaim: %d %v", n, err)
	}
	if f.fake.count("remove") != 1 || f.fake.count("read") != 1 || len(f.fake.removed) != 1 {
		t.Fatalf("calls %v", f.fake.calls)
	}
}

func TestReclaimResolvesAnUnknownRemovalOnALaterTickWithoutAWrongIdentity(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); n != 0 {
		t.Fatalf("an unresolved removal counted as settled: %d %v", n, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "unknown" {
		t.Fatal("the removal with no verdict is not unknown")
	}
	f.fake.readFails = false
	first := f.fake.sent[0].EffectID
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("the driver tick: %d %v", n, err)
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "applied" {
		t.Fatal("the unknown removal did not settle")
	}
	for _, c := range f.fake.sent {
		if c.EffectID != first {
			t.Fatal("an unknown removal was sent under a new identity")
		}
	}
	if len(f.fake.removed) != 1 {
		t.Fatalf("removed %v", f.fake.removed)
	}
}

// An unknown removal whose image is referenced again is not completed: the Portal closes its row and sends nothing.
func TestReclaimDoesNotCompleteAnUnknownRemovalOfAnImageReferencedAgain(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.readFails = true, true
	_, _ = f.s.reclaimPlatformImages(context.Background(), f.endpoint)
	f.fake.readFails = false
	f.setActive(t, "a")
	sends := f.fake.count("remove")
	if _, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil {
		t.Fatal(err)
	}
	if f.fake.count("remove") != sends {
		t.Fatal("a removal of a referenced image was completed")
	}
	if scalar[string](t, f.s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE kind='remove_image'`) != `{"outcome":"referenced_again","config_digest":"`+f.config("a")+`"}` ||
		scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "reverted" {
		t.Fatal("the row of a removal that no longer applies is not closed with its reason")
	}
}

// A runner that has no claim (a load of the image has no verdict there, say) leaves the row prepared: it is not
// settled and not resent blind, and the next tick tries the removal again.
func TestReclaimLeavesAnUnclaimedRemovalForALaterTick(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.loseExecuteReplyOnce, f.fake.absentOnRead = true, true
	if n, _ := f.s.reclaimPlatformImages(context.Background(), f.endpoint); n != 0 {
		t.Fatal("an unclaimed removal settled")
	}
	if scalar[string](t, f.s.st, `SELECT state FROM app_platform_operation_effects WHERE kind='remove_image'`) != "prepared" {
		t.Fatal("an unclaimed removal is not left prepared")
	}
}

func TestReclaimReportsABusyRunnerAsBusy(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	f.fake.pendingFor = 1
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 0 {
		t.Fatalf("a busy runner: %d %v", n, err)
	}
	if n, err := f.s.reclaimPlatformImages(context.Background(), f.endpoint); err != nil || n != 1 {
		t.Fatalf("after the runner freed: %d %v", n, err)
	}
}

// An installation with a running operation hosts no removal: its fence and generation belong to that operation.
func TestReclaimWaitsWhileTheInstallationHasARunningOperation(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "a")
	f.load(t, "a")
	// The installation's operation is running again.
	mustExec(t, f.s.st, `UPDATE app_platform_operations SET stage='queued',completed_ms=NULL WHERE id=?`, f.hostOp)
	if len(f.targets(t)) != 0 {
		t.Fatal("a removal was planned while the installation's operation was running")
	}
}

// An idle tick reaches the step: an installation whose operation finished and whose image nothing references is
// reclaimed by the scheduler with no other trigger.
func TestSchedulerIdleTickReclaimsTheImagesNothingReferences(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "old")
	f.load(t, "old")
	w, err := newPlatformInstallScheduler(f.s, f.endpoint, platformFixtureID(931), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.tick(context.Background())
	if len(f.fake.removed) != 1 || f.fake.removed[0] != f.config("old") {
		t.Fatalf("the idle tick did not reclaim: removed=%v calls=%v", f.fake.removed, f.fake.calls)
	}
}

// The scheduler's own hook runs the step with its endpoint, and tolerates a failure: it never fails a tick.
func TestSchedulerReclaimHookSendsTheRemovalsAnOperationFreed(t *testing.T) {
	f := newReclaimFixture(t)
	f.release(t, "old")
	f.load(t, "old")
	w, err := newPlatformInstallScheduler(f.s, f.endpoint, platformFixtureID(932), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	w.reclaimImages(context.Background())
	if len(f.fake.removed) != 1 {
		t.Fatalf("the hook did not send the removal: %v", f.fake.calls)
	}
	f.release(t, "other")
	f.load(t, "other")
	f.fake.readFails, f.fake.loseExecuteReplyOnce = true, true
	w.reclaimImages(context.Background())
}

// Both places the scheduler ends an operation (it completed, or it was finished for review) sweep the image blobs
// and must then reclaim the images: a new ending that forgot the step would leak images. The check reads the source
// of the scheduler, as the SQL guard tests do.
func TestEveryOperationEndingSweepsBlobsAndThenReclaimsImages(t *testing.T) {
	raw, err := os.ReadFile("app_platform_install_scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if strings.Count(source, "w.server.sweepPlatformImages(ctx)") != 2 {
		t.Fatalf("the scheduler sweeps blobs at %d places, the test expects the two operation endings", strings.Count(source, "w.server.sweepPlatformImages(ctx)"))
	}
	if strings.Count(source, "w.server.sweepPlatformImages(ctx)\n\t\tw.reclaimImages(ctx)")+strings.Count(source, "w.server.sweepPlatformImages(ctx)\n\tw.reclaimImages(ctx)") != 2 {
		t.Fatal("an operation ending sweeps blobs but does not reclaim images")
	}
	if !strings.Contains(source, "w.server.reclaimPlatformImages(bulkCtx, w.endpoint)") {
		t.Fatal("the idle tick does not reclaim images")
	}
}

func TestRemoveImageKindIsInTheEffectAllowList(t *testing.T) {
	if !platformEffectKindAllowed("stage", platformRemoveImageKind) {
		t.Fatal("remove_image is not in the effect kind allow-list")
	}
	if platformEffectKindAllowed("start", platformRemoveImageKind) || platformEffectKindAllowed("stage", "remove-everything") {
		t.Fatal("the allow-list accepts a kind it should not")
	}
}
