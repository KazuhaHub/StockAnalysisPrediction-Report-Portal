//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type fakeRemoveNative struct {
	mu         sync.Mutex
	removes    int
	presences  int
	onRemove   func()
	removeErr  error
	presentErr error
	// state is what RemoveImage answers; presence is what ImagePresentByID answers.
	state    string
	presence string
	fixture  *loadFixtureT
}

func (n *fakeRemoveNative) snapshot(state string) driver.RemoveImageSnapshot {
	return driver.RemoveImageSnapshot{
		ReleaseSHA256: n.fixture.parsed.SHA256, Platform: "linux/" + runtime.GOARCH,
		ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, n.fixture.entry), Store: driver.ImageStoreClassic,
		State: state, ObservedMS: time.Now().UnixMilli(),
	}
}

func (n *fakeRemoveNative) RemoveImage(context.Context, driver.RemoveImageRequest) (driver.RemoveImageSnapshot, error) {
	n.mu.Lock()
	n.removes++
	n.mu.Unlock()
	if n.onRemove != nil {
		n.onRemove()
	}
	if n.removeErr != nil {
		return driver.RemoveImageSnapshot{}, n.removeErr
	}
	state := n.state
	if state == "" {
		state = driver.RemoveStateRemoved
	}
	return n.snapshot(state), nil
}

func (n *fakeRemoveNative) ImagePresentByID(context.Context, []byte, string, string) (driver.RemoveImageSnapshot, error) {
	n.mu.Lock()
	n.presences++
	n.mu.Unlock()
	if n.presentErr != nil {
		return driver.RemoveImageSnapshot{}, n.presentErr
	}
	presence := n.presence
	if presence == "" {
		presence = driver.RemoveStatePresent
	}
	return n.snapshot(presence), nil
}

type removeFixtureT struct {
	*loadFixtureT
	command   control.RemoveImageCommand
	raw, sig  []byte
	rnative   *fakeRemoveNative
	removeKey ed25519.PrivateKey
}

// newRemoveFixture builds a removal of the image the load fixture loads, in the same operation (effect 2 beside the
// load's effect 1) and for the same installation.
func newRemoveFixture(t *testing.T) *removeFixtureT {
	t.Helper()
	f := &removeFixtureT{loadFixtureT: newLoadFixture(t), removeKey: ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))}
	f.rnative = &fakeRemoveNative{fixture: f.loadFixtureT}
	f.command, f.raw, f.sig = f.build(t, 1900, 2, 3300)
	return f
}

// extraKnown lets a test name more loads in the watermark of the commands it builds.
var extraKnownLoads []string

func (f *removeFixtureT) knownLoads(first string) []string {
	out := []string{first}
	for _, id := range extraKnownLoads {
		if id != first {
			out = append(out, id)
		}
	}
	return out
}

func (f *removeFixtureT) build(t *testing.T, base, slot, effect int) (control.RemoveImageCommand, []byte, []byte) {
	t.Helper()
	authority := importAuthority()
	c := control.RemoveImageCommand{CommandHeader: control.CommandHeader{
		Format: control.RemoveImageFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: fixtureID(base), OperationID: fixtureID(base + 1), TargetGeneration: "2", Stage: "stage", EffectNumber: itoa(int64(slot)),
		EffectID: fixtureID(effect), Kind: "remove-image", InstallationFence: "2",
	}, Payload: control.RemoveImagePayload{
		ReleaseSHA256: f.parsed.SHA256, Platform: "linux/" + runtime.GOARCH, ManifestDigest: f.entry.ManifestDigest,
		ConfigDigest: f.entry.ConfigDigest, Layers: control.FormatLoadImageLayers(f.entry.Layers),
		KnownLoads: control.FormatKnownLoads(f.knownLoads(f.loadFixtureT.command.EffectID)),
	}}
	digest, err := control.RemoveImageLogicalDigest(c)
	if err != nil {
		t.Fatalf("digest: %v header=%+v payload=%+v", err, c.CommandHeader, c.Payload)
	}
	c.PayloadSHA256 = digest
	now := time.Now().UTC().UnixMilli()
	c.IssuedMS, c.ExpiresMS = itoa(now-1000), itoa(now+25000)
	raw, sig, err := control.SignRemoveImage(c, f.removeKey, importAuthority(), now)
	if err != nil {
		t.Fatalf("sign: %v header=%+v payload=%+v", err, c.CommandHeader, c.Payload)
	}
	verified, err := control.VerifyRemoveImage(raw, sig, importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return verified.Command(), raw, sig
}

func (f *removeFixtureT) readSig(t *testing.T) []byte {
	t.Helper()
	sig, err := control.SignRemoveImageRead(f.raw, f.removeKey, importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func (f *removeFixtureT) dispatch() (any, error) {
	return f.store.DispatchRemoveImage(context.Background(), f.raw, f.sig, f.descriptor, f.rnative)
}

func (f *removeFixtureT) read(t *testing.T) (any, error) {
	return f.store.ReadRemoveImageOrStatus(context.Background(), f.raw, f.readSig(t))
}

func (f *removeFixtureT) rowState(t *testing.T) (string, string) {
	t.Helper()
	var state, receipt string
	if err := f.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, f.command.EffectID).Scan(&state, &receipt); err != nil {
		return "", ""
	}
	return state, receipt
}

// holdImage loads the image through the real load path so its reservation is held, as a runner that loaded it would.
func (f *removeFixtureT) holdImage(t *testing.T) {
	t.Helper()
	if err := f.store.SetCapacityBudgets(roomyBudgets()); err != nil {
		t.Fatal(err)
	}
	f.uploadAll(t)
	if _, err := f.loadFixtureT.dispatch(); err != nil {
		t.Fatal(err)
	}
	if reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatal("the load did not hold the image reservation")
	}
}

func TestRemoveImageClaimsUnknownBeforeAnyNativeCall(t *testing.T) {
	f := newRemoveFixture(t)
	var stateAtCall, receiptAtCall string
	f.rnative.onRemove = func() { stateAtCall, receiptAtCall = f.rowState(t) }
	got, err := f.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	if stateAtCall != "unknown" {
		t.Fatalf("effect was %q when the native call began", stateAtCall)
	}
	var claim removeImageClaim
	if !canonicalDataJSON(receiptAtCall, &claim) || !validRemoveClaim(claim) || !removeClaimMatchesCommand(claim, f.command) {
		t.Fatalf("claim marker is not canonical and bound to the command: %s", receiptAtCall)
	}
	receipt, ok := got.(RemoveImageReceipt)
	if !ok || receipt.State != driver.RemoveStateRemoved || receipt.Claim.ClaimID != claim.ClaimID || !RemoveImageReceiptMatchesCommand(receipt, f.command) {
		t.Fatalf("receipt: %+v", got)
	}
	if state, _ := f.rowState(t); state != "applied" {
		t.Fatalf("settled row %q", state)
	}
}

// The reservation is released by the settlement that records the image absent, and by nothing before it: while the
// native removal runs it is still held.
func TestRemoveImageAppliedReleasesExactlyThatReservation(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	// A second, unrelated image reservation must not be touched.
	other := capacityPlan{kind: reservationImage, resourceID: imageResourceID("9999999999999999999999999999999999999999999999999999999999999999"), installationID: fixtureID(1), imageBytes: 10}
	if refusal := admitPlan(t, f.store, other); refusal != nil {
		t.Fatal(refusal)
	}
	var heldDuring string
	f.rnative.onRemove = func() { heldDuring = reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) }
	if _, err := f.dispatch(); err != nil {
		t.Fatal(err)
	}
	if heldDuring != "held" {
		t.Fatalf("the reservation was %q while the native removal ran", heldDuring)
	}
	if reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "released" {
		t.Fatal("an applied removal kept its reservation")
	}
	if reservationState(t, f.store, imageResourceID("9999999999999999999999999999999999999999999999999999999999999999")) != "held" {
		t.Fatal("another image's reservation was released")
	}
}

func TestRemoveImageAbsentImageSettlesAppliedAndReleases(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.state = driver.RemoveStateAbsent
	got, err := f.dispatch()
	receipt, ok := got.(RemoveImageReceipt)
	if err != nil || !ok || receipt.State != driver.RemoveStateAbsent || receipt.Outcome != "" {
		t.Fatalf("absent image: %+v %v", got, err)
	}
	if state, _ := f.rowState(t); state != "applied" || reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "released" {
		t.Fatalf("absent: row %q", state)
	}
}

// A container of any state refusing the removal is a typed, definite refusal: the effect is reverted, the image and
// its reservation stay, and a replay returns the same refusal without touching Docker.
func TestRemoveImageRefusedWhileAContainerUsesIt(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.state = driver.RemoveStateInUse
	got, err := f.dispatch()
	receipt, ok := got.(RemoveImageReceipt)
	if err != nil || !ok || receipt.State != driver.RemoveStateInUse || receipt.Outcome != RemoveOutcomeImageInUse {
		t.Fatalf("in-use refusal: %+v %v", got, err)
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row %q, want reverted (a refusal is not an uncertain effect)", state)
	}
	if reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatal("a refused removal released the reservation")
	}
	again, err := f.dispatch()
	if err != nil || f.rnative.removes != 1 || again.(RemoveImageReceipt) != receipt {
		t.Fatalf("replay of a refusal: removes=%d %v", f.rnative.removes, err)
	}
}

func TestRemoveImageReplaysASettledReceiptWithoutDocker(t *testing.T) {
	f := newRemoveFixture(t)
	first, err := f.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.dispatch()
	if err != nil || f.rnative.removes != 1 || f.rnative.presences != 0 || first.(RemoveImageReceipt) != again.(RemoveImageReceipt) {
		t.Fatalf("replay: removes=%d presences=%d err=%v", f.rnative.removes, f.rnative.presences, err)
	}
}

// A removal claim refuses while a load of the same digest has no verdict, typed, and writes nothing.
func TestRemoveImageClaimRefusedWhileALoadOfTheDigestIsUnknown(t *testing.T) {
	f := newRemoveFixture(t)
	f.uploadAll(t)
	f.native.err = driver.ErrContainerUncertain
	if _, err := f.loadFixtureT.dispatch(); !errors.Is(err, driver.ErrContainerUncertain) {
		t.Fatalf("uncertain load: %v", err)
	}
	if state, _ := f.loadFixtureT.rowState(t); state != "unknown" {
		t.Fatalf("load row %q", state)
	}
	_, err := f.dispatch()
	if !errors.Is(err, ErrRemoveImageLoadPending) {
		t.Fatalf("remove while a load is unknown: %v", err)
	}
	if f.rnative.removes != 0 || f.rnative.presences != 0 {
		t.Fatal("the native side was reached while a load was unknown")
	}
	if state, _ := f.rowState(t); state != "" {
		t.Fatalf("a refused claim wrote a row: %q", state)
	}
	// Once the load has its verdict the claim is admitted.
	f.native.err = nil
	if _, err := f.loadFixtureT.dispatch(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatch(); err != nil {
		t.Fatalf("remove after the load settled: %v", err)
	}
}

// A load of a different digest does not block the removal.
func TestRemoveImageIgnoresAnUnknownLoadOfAnotherImage(t *testing.T) {
	f := newRemoveFixture(t)
	f.uploadAll(t)
	f.native.err = driver.ErrContainerUncertain
	_, _ = f.loadFixtureT.dispatch()
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET receipt_json=replace(receipt_json,?,?) WHERE kind='load-image'`, f.entry.ConfigDigest, "7777777777777777777777777777777777777777777777777777777777777777"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatch(); err != nil {
		t.Fatalf("remove blocked by a load of another image: %v", err)
	}
}

// The removal and a load of the same digest both run under the load gate, so they never run at once.
func TestRemoveImageRunsUnderTheLoadGate(t *testing.T) {
	f := newRemoveFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.rnative.onRemove = func() { close(entered); <-release }
	done := make(chan error, 1)
	go func() { _, err := f.dispatch(); done <- err }()
	<-entered
	// With the gate held by the removal, a read answers pending and a load waits.
	got, err := f.read(t)
	status, ok := got.(RemoveImageStatus)
	if err != nil || !ok || status.State != "pending" || !RemoveImageStatusMatchesCommand(status, f.command) {
		t.Fatalf("read during the removal: %+v %v", got, err)
	}
	f.uploadAll(t)
	loaded := make(chan struct{})
	go func() { _, _ = f.loadFixtureT.dispatch(); close(loaded) }()
	select {
	case <-loaded:
		t.Fatal("a load ran while the removal held the gate")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-loaded:
	case <-time.After(5 * time.Second):
		t.Fatal("the load never ran after the removal")
	}
}

// An unknown removal is resolved by looking by ID, never by resending blind: an image that is already gone settles
// as absent without any removal call.
func TestRemoveImageUnknownResolvedByProvingAbsence(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.removeErr = driver.ErrContainerUncertain
	if _, err := f.dispatch(); !errors.Is(err, driver.ErrContainerUncertain) {
		t.Fatalf("uncertain removal: %v", err)
	}
	if state, _ := f.rowState(t); state != "unknown" || reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatalf("an uncertain removal must stay unknown with its reservation: %q", state)
	}
	// The daemon did remove it before the connection broke.
	f.rnative.removeErr = nil
	f.rnative.presence = driver.RemoveStateAbsent
	removesBefore := f.rnative.removes
	got, err := f.dispatch()
	receipt, ok := got.(RemoveImageReceipt)
	if err != nil || !ok || receipt.State != driver.RemoveStateAbsent {
		t.Fatalf("resolution by absence: %+v %v", got, err)
	}
	if f.rnative.removes != removesBefore {
		t.Fatal("an unknown removal whose image was proven absent was sent again")
	}
	if state, _ := f.rowState(t); state != "applied" || reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "released" {
		t.Fatalf("resolved row %q", state)
	}
}

func TestRemoveImageUnknownResolvedByProvingPresenceRunsTheGuardAgain(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.removeErr = driver.ErrContainerUncertain
	_, _ = f.dispatch()
	f.rnative.removeErr = nil
	f.rnative.presence = driver.RemoveStatePresent
	f.rnative.state = driver.RemoveStateInUse
	got, err := f.dispatch()
	receipt, ok := got.(RemoveImageReceipt)
	if err != nil || !ok || receipt.State != driver.RemoveStateInUse {
		t.Fatalf("resolution by presence: %+v %v", got, err)
	}
	if f.rnative.removes != 2 || f.rnative.presences != 1 {
		t.Fatalf("removes=%d presences=%d: the presence was not proven before the guard ran again", f.rnative.removes, f.rnative.presences)
	}
	if state, _ := f.rowState(t); state != "reverted" || reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatalf("row %q", state)
	}
}

// A presence check that cannot be answered settles nothing and removes nothing.
func TestRemoveImageUnknownStaysUnknownWhenPresenceCannotBeProven(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.removeErr = driver.ErrContainerUncertain
	_, _ = f.dispatch()
	f.rnative.removeErr = nil
	f.rnative.presentErr = driver.ErrUnavailable
	removes := f.rnative.removes
	if _, err := f.dispatch(); !errors.Is(err, driver.ErrUnavailable) {
		t.Fatalf("unanswered presence: %v", err)
	}
	if f.rnative.removes != removes {
		t.Fatal("a removal was sent without proving presence")
	}
	if state, _ := f.rowState(t); state != "unknown" {
		t.Fatalf("row %q", state)
	}
}

func TestRemoveImageReadBackReceiptPendingOrphanedAndAbsence(t *testing.T) {
	f := newRemoveFixture(t)
	// No row: typed absence, given under the gate.
	got, err := f.read(t)
	if status, ok := got.(RemoveImageStatus); err != nil || !ok || status.State != "absent" {
		t.Fatalf("never claimed: %+v %v", got, err)
	}
	// An unknown effect with the gate free is orphaned.
	f.rnative.removeErr = driver.ErrContainerUncertain
	_, _ = f.dispatch()
	got, err = f.read(t)
	if status, ok := got.(RemoveImageStatus); err != nil || !ok || status.State != "orphaned" {
		t.Fatalf("unknown effect: %+v %v", got, err)
	}
	// A settled effect answers its receipt.
	f.rnative.removeErr = nil
	f.rnative.presence = driver.RemoveStateAbsent
	want, err := f.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	got, err = f.read(t)
	if err != nil || got.(RemoveImageReceipt) != want.(RemoveImageReceipt) {
		t.Fatalf("read of a settled effect: %+v %v", got, err)
	}
	if f.rnative.removes != 1 {
		t.Fatal("a read reached the native side")
	}
}

func TestRemoveImageRefusesADescriptorThatDoesNotBindBeforeAnyClaim(t *testing.T) {
	f := newRemoveFixture(t)
	other := append([]byte(" "), f.descriptor...)
	if _, err := f.store.DispatchRemoveImage(context.Background(), f.raw, f.sig, other, f.rnative); err == nil {
		t.Fatal("a descriptor that is not the signed release's was accepted")
	}
	if state, _ := f.rowState(t); state != "" || f.rnative.removes != 0 {
		t.Fatal("a refused descriptor claimed an effect")
	}
}

func TestRemoveImageRefusesATakenEffectNumber(t *testing.T) {
	f := newRemoveFixture(t)
	// The slot is taken by another effect of the operation.
	if _, err := f.store.db.Exec(importKindCommand, fixtureID(3401), f.command.OperationID, f.command.InstallationID, "stage", loadSum([]byte("x")), fixtureID(2)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET effect_number=2,stage='stage',controller_epoch=? WHERE effect_id=?`, f.command.ControllerEpoch, fixtureID(3401)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatch(); err == nil || f.rnative.removes != 0 {
		t.Fatalf("a taken effect number admitted a removal: %v", err)
	}
}

// A load of the image the Portal had not seen (applied after its watermark) refuses the claim, typed and with no
// row written, even though no container uses the image yet: this is the remove-versus-install race.
func TestRemoveImageClaimRefusedWhenALoadAppliedAfterTheWatermark(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t) // the load the watermark names
	// A second installation loads the same image: present, applied, and no container yet.
	second := f.loadFixtureT.buildCommand(t, 2100)
	f.loadFixtureT.command = second
	f.loadFixtureT.raw, f.loadFixtureT.sig = f.loadFixtureT.sign(t, second)
	f.uploadAll(t)
	if _, err := f.loadFixtureT.dispatch(); err != nil {
		t.Fatal(err)
	}
	_, err := f.dispatch()
	if !errors.Is(err, ErrRemoveImageReferencedAgain) {
		t.Fatalf("removal after an unseen load: %v", err)
	}
	if f.rnative.removes != 0 || f.rnative.presences != 0 {
		t.Fatal("the native side was reached")
	}
	if state, _ := f.rowState(t); state != "" {
		t.Fatalf("a refused claim wrote a row: %q", state)
	}
	if reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatal("the reservation was released")
	}
	// With the second load named in the watermark the removal is admitted.
	extraKnownLoads = []string{fixtureID(1902), second.EffectID}
	defer func() { extraKnownLoads = nil }()
	f.command, f.raw, f.sig = f.build(t, 1900, 2, 3301)
	if _, err := f.dispatch(); err != nil {
		t.Fatalf("removal naming every load: %v (known %q)", err, f.command.Payload.KnownLoads)
	}
}

// A reverted load never counts: only applied and unknown loads can reference the image.
func TestRemoveImageWatermarkIgnoresARevertedLoad(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	if _, err := f.store.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
		SELECT ?,operation_id,installation_id,'stage',9,'load-image',payload_sha256,target_generation,controller_epoch,admitted_fence,'reverted',receipt_json,NULL,created_ms,updated_ms FROM runner_commands WHERE kind='load-image' LIMIT 1`, fixtureID(3999)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dispatch(); err != nil {
		t.Fatalf("a reverted load blocked the removal: %v", err)
	}
}

// The daemon's own 409 after the guard passed is a typed, retryable refusal: reverted, reservation kept.
func TestRemoveImageDaemonConflictSettlesRevertedWithItsOwnOutcome(t *testing.T) {
	f := newRemoveFixture(t)
	f.holdImage(t)
	f.rnative.state = driver.RemoveStateConflict
	got, err := f.dispatch()
	receipt, ok := got.(RemoveImageReceipt)
	if err != nil || !ok || receipt.State != driver.RemoveStateConflict || receipt.Outcome != RemoveOutcomeImageConflict {
		t.Fatalf("conflict: %+v %v", got, err)
	}
	if state, _ := f.rowState(t); state != "reverted" || reservationState(t, f.store, imageResourceID(f.entry.ConfigDigest)) != "held" {
		t.Fatalf("row %q", state)
	}
}
