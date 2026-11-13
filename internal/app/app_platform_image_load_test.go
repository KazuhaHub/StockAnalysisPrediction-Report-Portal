package app

import (
	"context"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformFakeImageReply answers as a runner would for one command.
type platformFakeImageReply struct {
	platformFakeObservation
	receipt *journal.LoadImageReceipt
	status  *journal.LoadImageStatus
}

func (r platformFakeImageReply) LoadImageReceipt() (journal.LoadImageReceipt, error) {
	if r.receipt == nil {
		return journal.LoadImageReceipt{}, errFakeNoSuchReply
	}
	return *r.receipt, nil
}

func (r platformFakeImageReply) LoadImageStatus() (journal.LoadImageStatus, error) {
	if r.status == nil {
		return journal.LoadImageStatus{}, errFakeNoSuchReply
	}
	return *r.status, nil
}

// platformFakeImageRunner models the runner's behaviour that matters to the sender: a scratch directory that
// holds uploaded blobs, an effect journal keyed by effect id, and the daemon's image list.
type platformFakeImageRunner struct {
	t        *testing.T
	endpoint platformRuntimeEndpoint
	mu       sync.Mutex
	calls    []string
	effects  map[string]string // effect id -> "unknown" | "applied" | "reverted"
	scratch  map[string]bool
	present  bool
	// pendingFor makes the next n executes answer pending, as when the load lock is held.
	pendingFor int
	// rejectLoad settles a claimed load reverted with the outcome.
	rejectLoad string
	// loseExecuteReplyOnce makes an execute run on the runner and then fail on the wire.
	loseExecuteReplyOnce bool
	// refuseBlob answers an upload with a typed refusal.
	refuseBlob imagestore.Refusal
	uploads    map[string][]int64 // digest -> resume offsets of its sessions
	effectIDs  map[string]bool
	needed     []string
	failUpload error
	// readFails makes every read-back fail on the wire.
	readFails bool
	// refuseCapacity answers every execute and read-back with the runner's capacity refusal for this dimension.
	refuseCapacity string
}

func newPlatformFakeImageRunner(t *testing.T, endpoint platformRuntimeEndpoint, parts platformManagedImageParts) *platformFakeImageRunner {
	f := &platformFakeImageRunner{t: t, endpoint: endpoint, effects: map[string]string{}, scratch: map[string]bool{}, uploads: map[string][]int64{}, effectIDs: map[string]bool{}}
	for _, b := range parts.imageBlobList() {
		f.needed = append(f.needed, b.Digest)
	}
	return f
}

func (f *platformFakeImageRunner) capacityRefusal(call platformLoadImageCall) journal.CapacityRefusal {
	id := journal.CapacityIdentityOfHeader(call.command.CommandHeader)
	return journal.CapacityRefusal{
		Format: "portal.runner.capacity-refusal/1-dev.1", DeploymentID: id.DeploymentID, ControllerEpoch: id.ControllerEpoch,
		InstallationID: id.InstallationID, OperationID: id.OperationID, TargetGeneration: id.TargetGeneration, Stage: id.Stage,
		EffectNumber: id.EffectNumber, EffectID: id.EffectID, Kind: id.Kind, PayloadSHA256: id.PayloadSHA256,
		Outcome: "capacity_exhausted", Dimension: f.refuseCapacity, Requested: "4096", Remaining: "0",
		SettledMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

func (f *platformFakeImageRunner) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (f *platformFakeImageRunner) proof(call platformLoadImageCall) platformFakeObservation {
	return platformFakeProof(call.authority, f.endpoint, call.raw)
}

func (f *platformFakeImageRunner) receipt(call platformLoadImageCall, state, outcome string) *journal.LoadImageReceipt {
	c := call.command
	now := platformFakeFirstObserved(c.EffectID)
	at, _ := strconv.ParseInt(now, 10, 64)
	r := journal.LoadImageReceipt{
		Format: "portal.runner.load-image-receipt/1-dev.1",
		Claim: journal.LoadImageClaim{
			Format: "portal.runner.load-image-claim/1-dev.1", ClaimID: platformDerivedUUID("claim", c.EffectID), EffectID: c.EffectID,
			ReleaseSHA256: c.Payload.ReleaseSHA256, Platform: c.Payload.Platform, ManifestDigest: c.Payload.ManifestDigest,
			ConfigDigest: c.Payload.ConfigDigest, Layers: c.Payload.Layers, AdmittedFence: c.InstallationFence, ClaimedMS: strconv.FormatInt(at-1, 10),
		},
		DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, InstallationID: c.InstallationID, OperationID: c.OperationID,
		TargetGeneration: c.TargetGeneration, Stage: c.Stage, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256,
		ImageID: "sha256:" + c.Payload.ConfigDigest, Store: "classic", State: state, Outcome: outcome, ObservedMS: now, SettledMS: now,
	}
	return &r
}

func (f *platformFakeImageRunner) statusOf(call platformLoadImageCall, state string, missing []string) *journal.LoadImageStatus {
	c := call.command
	return &journal.LoadImageStatus{
		Format: "portal.runner.load-image-status/1-dev.1", DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: c.TargetGeneration, Stage: c.Stage,
		EffectNumber: c.EffectNumber, EffectID: c.EffectID, PayloadSHA256: c.PayloadSHA256, State: state, Missing: missing,
		ObservedMS: strconv.FormatInt(time.Now().UTC().UnixMilli(), 10),
	}
}

func (f *platformFakeImageRunner) missing() []string {
	var out []string
	for _, d := range f.needed {
		if !f.scratch[d] {
			out = append(out, d)
		}
	}
	return out
}

func (f *platformFakeImageRunner) LoadImage(_ context.Context, _ platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "load")
	f.effectIDs[call.command.EffectID] = true
	if f.refuseCapacity != "" {
		return nil, &journal.CapacityRefusalError{Receipt: f.capacityRefusal(call)}
	}
	if _, err := control.VerifyLoadImage(call.raw, call.signature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the command: %v", err)
		return nil, err
	}
	reply := platformFakeImageReply{platformFakeObservation: f.proof(call)}
	if f.pendingFor > 0 {
		f.pendingFor--
		reply.status = f.statusOf(call, "pending", nil)
		return reply, nil
	}
	state := f.effects[call.command.EffectID]
	switch {
	case state == "applied":
		reply.receipt = f.receipt(call, "present", "")
	case f.present:
		f.effects[call.command.EffectID] = "applied"
		reply.receipt = f.receipt(call, "present", "")
	default:
		if state == "" {
			f.effects[call.command.EffectID] = "unknown"
		}
		if miss := f.missing(); len(miss) > 0 {
			reply.status = f.statusOf(call, "blobs_missing", miss)
		} else if f.rejectLoad != "" {
			f.effects[call.command.EffectID] = "reverted"
			reply.receipt = f.receipt(call, "rejected", f.rejectLoad)
		} else {
			f.effects[call.command.EffectID] = "applied"
			f.present = true
			reply.receipt = f.receipt(call, "loaded", "")
		}
	}
	if f.loseExecuteReplyOnce {
		f.loseExecuteReplyOnce = false
		return nil, errors.New("connection lost after the runner ran the command")
	}
	return reply, nil
}

func (f *platformFakeImageRunner) ReadLoadImage(_ context.Context, _ platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read")
	if f.refuseCapacity != "" {
		return nil, &journal.CapacityRefusalError{Receipt: f.capacityRefusal(call)}
	}
	if f.readFails {
		return nil, errors.New("read-back lost on the wire")
	}
	if _, err := control.VerifyLoadImageRead(call.raw, call.readSignature, call.authority, time.Now().UnixMilli()); err != nil {
		f.t.Errorf("the runner would refuse the read: %v", err)
		return nil, err
	}
	reply := platformFakeImageReply{platformFakeObservation: f.proof(call)}
	switch f.effects[call.command.EffectID] {
	case "applied":
		reply.receipt = f.receipt(call, "loaded", "")
	case "reverted":
		reply.receipt = f.receipt(call, "rejected", f.rejectLoad)
	case "unknown":
		reply.status = f.statusOf(call, "orphaned", nil)
	default:
		return nil, errPlatformMigrationAbsent
	}
	return reply, nil
}

func (f *platformFakeImageRunner) SendBlob(_ context.Context, _ platformRuntimeEndpoint, call platformImageBlobCall) (transport.ImageBlobResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	verified, err := control.VerifyImageBlob(call.raw, call.signature, call.authority, time.Now().UnixMilli())
	if err != nil {
		f.t.Errorf("the runner would refuse the blob command: %v", err)
		return transport.ImageBlobResult{}, err
	}
	declaration, _ := verified.Command().Declaration()
	f.calls = append(f.calls, "blob:"+declaration.Blob.Digest)
	f.uploads[declaration.Blob.Digest] = append(f.uploads[declaration.Blob.Digest], declaration.ResumeOffset)
	if f.refuseBlob != "" {
		return transport.ImageBlobResult{Refusal: f.refuseBlob}, transport.ErrImageBlobRefused
	}
	if f.failUpload != nil {
		err := f.failUpload
		f.failUpload = nil
		return transport.ImageBlobResult{}, err
	}
	body, _ := io.ReadAll(call.body)
	if platformDigest(append(make([]byte, declaration.ResumeOffset), body...)) == "" {
		return transport.ImageBlobResult{}, errors.New("unreachable")
	}
	f.scratch[declaration.Blob.Digest] = true
	return transport.ImageBlobResult{Stored: true}, nil
}

// platformImageEnsureFixture is an update operation at its migrate stage whose target release is a managed
// release carrying backend.images, with the blobs stored in the Portal's blob store.
func platformImageEnsureFixture(t *testing.T) (*Server, platformOperationLease, platformRuntimeEndpoint, platformManagedImageParts, *platformFakeImageRunner) {
	t.Helper()
	s, lease, endpoint, _ := platformMigrationStageFixture(t, false, true)
	s.platformImageDir = t.TempDir()
	parts := newPlatformManagedImageParts(t, "9.9.9", "ensure")
	target := scalar[string](t, s.st, `SELECT p.target_release_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, lease.id)
	managed := platformInsertManagedRelease(t, s, target, parts)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET target_release_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, managed, lease.id)
	store, err := s.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	for _, blob := range parts.imageBlobList() {
		if err := store.putBlob(blob, bytesReader(parts.blobs[blob.Digest])); err != nil {
			t.Fatal(err)
		}
	}
	fake := newPlatformFakeImageRunner(t, endpoint, parts)
	s.platformImageRunner = fake
	return s, lease, endpoint, parts, fake
}

func TestEnsureImageLoadsAMissingImageByUploadingExactlyTheBlobsTheRunnerNamedUnderOneEffect(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	if fake.count("blob:") != len(parts.imageBlobList()) || fake.count("load") != 2 || len(fake.effectIDs) != 1 {
		t.Fatalf("calls %v effects %v", fake.calls, fake.effectIDs)
	}
	want := platformLoadImageEffectID(lease.id, platformDigest(parts.descriptor))
	if !fake.effectIDs[want] {
		t.Fatal("the effect id is not the one derived from the operation and release")
	}
	if !fake.present {
		t.Fatal("the image was not loaded")
	}
}

func TestEnsureImageUploadsNothingWhenTheImageIsAlreadyOnTheHost(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.present = true
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	if fake.count("blob:") != 0 || fake.count("load") != 1 {
		t.Fatalf("a present image cost %v", fake.calls)
	}
}

// Fails if a second attempt, a restart or a lease takeover mints a second identity for the load.
func TestEnsureImageSendsTheSameEffectIdOnEveryAttemptAndNeverUploadsAgainOnceApplied(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	for range 3 {
		if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.effectIDs) != 1 {
		t.Fatalf("effect ids %v", fake.effectIDs)
	}
	blobs := fake.count("blob:")
	if blobs != 4 {
		t.Fatalf("blobs uploaded %d times: %v", blobs, fake.calls)
	}
}

// Fails if an unknown execute is sent again under a new identity, or if it is believed failed.
func TestEnsureImageReadsBackAnUncertainExecuteUnderItsOwnIdentityAndTakesItsReceipt(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.present = true
	fake.loseExecuteReplyOnce = true
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	if fake.count("load") != 1 || fake.count("read") != 1 || len(fake.effectIDs) != 1 {
		t.Fatalf("calls %v", fake.calls)
	}
}

// Fails if a load the runner is still doing is treated as a failure that counts against the operation.
func TestEnsureImageReportsABusyRunnerAsBusyNotAsAFailure(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.pendingFor = 1
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageBusy) {
		t.Fatalf("pending: %v", err)
	}
	if fake.count("blob:") != 0 {
		t.Fatal("blobs were uploaded to a runner that is busy")
	}
}

// Fails if a definite rejection is retried as if it were transient, or hidden.
func TestEnsureImageSurfacesARunnerRejectionAsADefiniteFailure(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.rejectLoad = "inspect_mismatch"
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageRejected) || errors.Is(err, errPlatformImageBusy) {
		t.Fatalf("rejected load: %v", err)
	}
}

// Fails if a typed refusal of an upload is retried until the attempts run out, or reported as transient.
func TestEnsureImageStopsOnTheRunnersTypedRefusalOfABlob(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.refuseBlob = imagestore.RefusalDiffIDMismatch
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageRejected) {
		t.Fatalf("refused blob: %v", err)
	}
	if fake.count("blob:") != 1 {
		t.Fatalf("the refused blob was sent again: %v", fake.calls)
	}
	fake.refuseBlob = imagestore.RefusalScratchFull
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageBusy) {
		t.Fatalf("scratch full: %v", err)
	}
}

// Fails if a lost upload connection discards the effect or is reported as a rejection.
func TestEnsureImageResumesALostUploadWithoutANewEffect(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	fake.failUpload = errors.New("connection reset")
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	if len(fake.effectIDs) != 1 || !fake.present {
		t.Fatalf("calls %v", fake.calls)
	}
	first := parts.imageBlobList()[0].Digest
	if len(fake.uploads[first]) != 2 {
		t.Fatalf("the interrupted blob was not retried: %v", fake.uploads)
	}
}

// Fails (sends a command) if any runner effect leaves the Portal for a release whose blobs fail the hash check.
func TestEnsureImageSendsNothingToTheRunnerWhenAStoredBlobFailsItsHashCheck(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	store, _ := s.platformImages()
	layer := parts.plan.Layers[1]
	body := append([]byte(nil), parts.blobs[layer.Digest]...)
	body[len(body)/2] ^= 0xff
	if err := writeFileForTest(store.blobPath(layer.Digest), body); err != nil {
		t.Fatal(err)
	}
	err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint)
	if !errors.Is(err, errPlatformImageBlobs) {
		t.Fatalf("corrupt blob: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("a runner effect was sent: %v", fake.calls)
	}
	// And the corrupt blob is gone, so the release needs its package again.
	if _, err := store.openBlob(layer); !errors.Is(err, errPlatformImageBlobMissing) {
		t.Fatalf("the corrupt blob stayed: %v", err)
	}
	// A missing blob behaves the same way.
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); !errors.Is(err, errPlatformImageBlobs) || len(fake.calls) != 0 {
		t.Fatalf("missing blob: %v %v", err, fake.calls)
	}
}

// Fails if a release with a registry reference (the development form) triggers a load, or a static one does.
func TestEnsureImageDoesNothingForAReleaseThatCarriesNoManagedImage(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	fake.present = false
	// The operation's original target release names a registry reference.
	original := scalar[string](t, s.st, `SELECT sha256 FROM app_platform_releases WHERE sha256<>(SELECT target_release_sha256 FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)) ORDER BY created_ms LIMIT 1`, lease.id)
	mustExec(t, s.st, `UPDATE app_platform_review_plans SET target_release_sha256=? WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, original, lease.id)
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("calls %v", fake.calls)
	}
}

// Fails if the sender believes a reply that answers another command.
func TestEnsureImageDoesNotBelieveAReceiptForAnotherEffect(t *testing.T) {
	s, lease, endpoint, _, fake := platformImageEnsureFixture(t)
	liar := &platformLyingImageRunner{platformFakeImageRunner: fake}
	s.platformImageRunner = liar
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err == nil {
		t.Fatal("a receipt for another effect settled the load")
	}
}

type platformLyingImageRunner struct{ *platformFakeImageRunner }

func (l *platformLyingImageRunner) LoadImage(ctx context.Context, e platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	reply, err := l.platformFakeImageRunner.LoadImage(ctx, e, call)
	if r, ok := reply.(platformFakeImageReply); ok && r.receipt != nil {
		r.receipt.EffectID = platformDerivedUUID("other", call.command.EffectID)
		return r, err
	}
	other := call
	other.command.EffectID = platformDerivedUUID("other", call.command.EffectID)
	return reply, err
}

var _ = appplatform.ImageBlobLayer
