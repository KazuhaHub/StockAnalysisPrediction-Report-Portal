//go:build linux || darwin

package journal

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
)

func loadSum(raw []byte) string {
	s := sha256.Sum256(raw)
	return hex.EncodeToString(s[:])
}

type loadTestBlob struct {
	blob appplatform.ImageBlob
	body []byte
}

type loadFixtureT struct {
	store      *Store
	scratch    *imagestore.Store
	descriptor []byte
	parsed     appplatform.ParsedDescriptor
	entry      appplatform.BackendImage
	blobs      []loadTestBlob
	command    control.LoadImageCommand
	raw, sig   []byte
	native     *fakeLoadNative
}

type fakeLoadNative struct {
	mu          sync.Mutex
	calls       int
	concurrent  int
	maxParallel int
	onLoad      func(driver.LoadImageRequest)
	err         error
	present     bool
	presentErr  error
	snapshot    func(driver.LoadImageRequest, loadFixtureT) driver.LoadImageSnapshot
	fixture     *loadFixtureT
}

func (n *fakeLoadNative) LoadImage(ctx context.Context, r driver.LoadImageRequest) (driver.LoadImageSnapshot, error) {
	n.mu.Lock()
	n.calls++
	n.concurrent++
	if n.concurrent > n.maxParallel {
		n.maxParallel = n.concurrent
	}
	n.mu.Unlock()
	defer func() { n.mu.Lock(); n.concurrent--; n.mu.Unlock() }()
	if n.onLoad != nil {
		n.onLoad(r)
	}
	if n.err != nil {
		return driver.LoadImageSnapshot{}, n.err
	}
	if n.snapshot != nil {
		return n.snapshot(r, *n.fixture), nil
	}
	return driver.LoadImageSnapshot{
		ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, n.fixture.entry),
		Store: driver.ImageStoreClassic, State: driver.LoadStateLoaded, ObservedMS: time.Now().UnixMilli(),
	}, nil
}

func (n *fakeLoadNative) LoadedImagePresent(context.Context, []byte, string, string) (bool, error) {
	return n.present, n.presentErr
}

func uploadLoadBlob(t *testing.T, scratch *imagestore.Store, operation string, b loadTestBlob) {
	t.Helper()
	u, _, done, refusal, err := scratch.Begin(operation, b.blob, 0)
	if err != nil || refusal != imagestore.RefusalNone {
		t.Fatalf("upload begin: %v %v", refusal, err)
	}
	if done {
		return
	}
	if r := u.Write(b.body); r != imagestore.RefusalNone {
		t.Fatal(r)
	}
	if r := u.Commit(b.blob.Digest, b.blob.Size); r != imagestore.RefusalNone {
		t.Fatal(r)
	}
}

func newLoadFixture(t *testing.T) *loadFixtureT {
	t.Helper()
	store, _ := enrolledCommandStore(t)
	dir, err := os.MkdirTemp("", "journal-images-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, _ = filepath.EvalSymlinks(dir)
	root := filepath.Join(dir, "images")
	_ = os.Mkdir(root, 0700)
	scratch, err := imagestore.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &loadFixtureT{store: store, scratch: scratch}
	var diffIDs []string
	var descriptors []map[string]any
	var entryLayers []map[string]any
	for _, name := range []string{"etc/one", "etc/two"} {
		var tarball bytes.Buffer
		tw := tar.NewWriter(&tarball)
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("abc"))
		_ = tw.Close()
		var gz bytes.Buffer
		zw := gzip.NewWriter(&gz)
		_, _ = zw.Write(tarball.Bytes())
		_ = zw.Close()
		digest := loadSum(gz.Bytes())
		f.blobs = append(f.blobs, loadTestBlob{blob: appplatform.ImageBlob{
			Digest: digest, Kind: appplatform.ImageBlobLayer, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
			Size: int64(gz.Len()), DiffID: loadSum(tarball.Bytes()), UncompressedBytes: int64(tarball.Len()),
		}, body: gz.Bytes()})
		diffIDs = append(diffIDs, "sha256:"+loadSum(tarball.Bytes()))
		descriptors = append(descriptors, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + digest, "size": gz.Len()})
		entryLayers = append(entryLayers, map[string]any{"diff_id": loadSum(tarball.Bytes()), "uncompressed_bytes": tarball.Len()})
	}
	config, _ := json.Marshal(map[string]any{"architecture": runtime.GOARCH, "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs}})
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": appplatform.MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": appplatform.MediaTypeOCIConfig, "digest": "sha256:" + loadSum(config), "size": len(config)},
		"layers": descriptors,
	})
	f.blobs = append([]loadTestBlob{
		{blob: appplatform.ImageBlob{Digest: loadSum(manifest), Kind: appplatform.ImageBlobManifest, MediaType: appplatform.MediaTypeOCIManifest, Size: int64(len(manifest))}, body: manifest},
		{blob: appplatform.ImageBlob{Digest: loadSum(config), Kind: appplatform.ImageBlobConfig, MediaType: appplatform.MediaTypeOCIConfig, Size: int64(len(config))}, body: config},
	}, f.blobs...)
	raw, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	backend := d["backend"].(map[string]any)
	delete(backend, "image")
	platform := "linux/" + runtime.GOARCH
	backend["platforms"] = []any{platform}
	backend["images"] = []any{map[string]any{"platform": platform, "manifest_digest": loadSum(manifest), "config_digest": loadSum(config), "layers": entryLayers}}
	f.descriptor, _ = json.Marshal(d)
	f.parsed, err = appplatform.ParseDescriptor(f.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.entry = f.parsed.Manifest.Backend.Images[0]
	f.native = &fakeLoadNative{fixture: f}
	f.command = f.buildCommand(t, 1900)
	f.raw, f.sig = f.sign(t, f.command)
	// The signed form carries the logical digest and the signing window, as the runner sees it.
	verified, err := control.VerifyLoadImage(f.raw, f.sig, importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	f.command = verified.Command()
	return f
}

func (f *loadFixtureT) buildCommand(t *testing.T, base int) control.LoadImageCommand {
	t.Helper()
	authority := importAuthority()
	c := control.LoadImageCommand{CommandHeader: control.CommandHeader{
		Format: control.LoadImageFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: fixtureID(base), OperationID: fixtureID(base + 1), TargetGeneration: "2", Stage: "stage", EffectNumber: "1",
		EffectID: fixtureID(base + 2), Kind: "load-image", InstallationFence: "2",
	}, Payload: control.LoadImagePayload{
		ReleaseSHA256: f.parsed.SHA256, Platform: "linux/" + runtime.GOARCH, ManifestDigest: f.entry.ManifestDigest,
		ConfigDigest: f.entry.ConfigDigest, Layers: control.FormatLoadImageLayers(f.entry.Layers),
	}}
	return c
}

func (f *loadFixtureT) sign(t *testing.T, c control.LoadImageCommand) ([]byte, []byte) {
	t.Helper()
	digest, err := control.LoadImageLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	now := time.Now().UTC().UnixMilli()
	c.IssuedMS, c.ExpiresMS = "1", "2"
	c.IssuedMS, c.ExpiresMS = itoa(now-1000), itoa(now+25000)
	raw, sig, err := control.SignLoadImage(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}

func (f *loadFixtureT) readSig(t *testing.T, raw []byte) []byte {
	t.Helper()
	sig, err := control.SignLoadImageRead(raw, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func (f *loadFixtureT) uploadAll(t *testing.T) {
	t.Helper()
	for _, b := range f.blobs {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
}

func (f *loadFixtureT) dispatch() (any, error) {
	return f.store.DispatchLoadImage(context.Background(), f.raw, f.sig, f.descriptor, f.native, f.scratch)
}

func (f *loadFixtureT) rowState(t *testing.T) (string, string) {
	t.Helper()
	var state, receipt string
	err := f.store.db.QueryRow(`SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, f.command.EffectID).Scan(&state, &receipt)
	if err != nil {
		return "", ""
	}
	return state, receipt
}

func itoa(n int64) string {
	return strings.TrimSpace(strings.Replace(string(mustJSON(n)), "\"", "", -1))
}
func mustJSON(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

// The claim is committed as unknown before any native call: nothing can reach Docker under an effect the journal
// has not recorded.
func TestLoadImageClaimsUnknownBeforeAnyNativeCall(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	var stateAtCall, receiptAtCall string
	f.native.onLoad = func(driver.LoadImageRequest) { stateAtCall, receiptAtCall = f.rowState(t) }
	got, err := f.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	if stateAtCall != "unknown" {
		t.Fatalf("effect was %q when the native call began", stateAtCall)
	}
	var claim loadImageClaim
	if !canonicalDataJSON(receiptAtCall, &claim) || !validLoadClaim(claim) || !loadClaimMatchesCommand(claim, f.command) {
		t.Fatalf("claim marker is not canonical and bound to the command: %s", receiptAtCall)
	}
	receipt, ok := got.(LoadImageReceipt)
	if !ok || receipt.State != driver.LoadStateLoaded || receipt.Claim.ClaimID != claim.ClaimID || !LoadImageReceiptMatchesCommand(receipt, f.command) {
		t.Fatalf("receipt: %+v", got)
	}
	if state, _ := f.rowState(t); state != "applied" {
		t.Fatalf("settled row %q", state)
	}
}

func TestLoadImageReplaysASettledReceiptWithoutDocker(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	first, err := f.dispatch()
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.dispatch()
	if err != nil || f.native.calls != 1 {
		t.Fatalf("replay: calls=%d err=%v", f.native.calls, err)
	}
	if first.(LoadImageReceipt) != again.(LoadImageReceipt) {
		t.Fatal("replay differs from the settled receipt")
	}
	if f.scratch.Has(f.command.OperationID, f.entry.ManifestDigest) {
		t.Fatal("scratch outlived the settled load")
	}
}

func TestLoadImageRejectionSettlesRevertedWithATypedOutcome(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	f.native.snapshot = func(r driver.LoadImageRequest, f loadFixtureT) driver.LoadImageSnapshot {
		return driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, f.entry),
			Store: driver.ImageStoreClassic, State: driver.LoadStateRejected, Outcome: driver.LoadOutcomeRetagDetected, Removed: true, ObservedMS: time.Now().UnixMilli()}
	}
	got, err := f.dispatch()
	receipt, ok := got.(LoadImageReceipt)
	if err != nil || !ok || receipt.State != driver.LoadStateRejected || receipt.Outcome != driver.LoadOutcomeRetagDetected || !receipt.Removed {
		t.Fatalf("rejection: %+v %v", got, err)
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row %q", state)
	}
	// A reverted load is never rerun: the replay is the same receipt.
	if again, err := f.dispatch(); err != nil || f.native.calls != 1 || again.(LoadImageReceipt) != receipt {
		t.Fatalf("replay of a rejection: calls=%d %v", f.native.calls, err)
	}
}

// An unknown effect is completed under the SAME claim and effect id, never claimed again.
func TestLoadImageUnknownIsCompletedUnderTheSameClaim(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	f.native.err = driver.ErrContainerUncertain
	if _, err := f.dispatch(); !errors.Is(err, driver.ErrContainerUncertain) {
		t.Fatalf("uncertain load: %v", err)
	}
	state, claimRaw := f.rowState(t)
	if state != "unknown" {
		t.Fatalf("an uncertain load settled as %q", state)
	}
	var first loadImageClaim
	if !canonicalDataJSON(claimRaw, &first) {
		t.Fatal("claim not canonical")
	}
	f.native.err = nil
	got, err := f.dispatch()
	receipt, ok := got.(LoadImageReceipt)
	if err != nil || !ok || receipt.State != driver.LoadStateLoaded {
		t.Fatalf("completion: %+v %v", got, err)
	}
	if receipt.Claim.ClaimID != first.ClaimID || receipt.Claim.ClaimedMS != first.ClaimedMS || receipt.EffectID != f.command.EffectID {
		t.Fatal("the unknown load was claimed again instead of completed")
	}
	if n := scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='load-image'`); n != 1 {
		t.Fatalf("%d load rows", n)
	}
}

func TestLoadImageMissingBlobsKeepTheClaimAndNameTheDigests(t *testing.T) {
	f := newLoadFixture(t)
	// Only the manifest and config arrived.
	uploadLoadBlob(t, f.scratch, f.command.OperationID, f.blobs[0])
	uploadLoadBlob(t, f.scratch, f.command.OperationID, f.blobs[1])
	f.native.err = driver.ErrImageBlobUnavailable
	got, err := f.dispatch()
	status, ok := got.(LoadImageStatus)
	if err != nil || !ok || status.State != "blobs_missing" || len(status.Missing) != 2 ||
		status.Missing[0] != f.blobs[2].blob.Digest || status.Missing[1] != f.blobs[3].blob.Digest || !LoadImageStatusMatchesCommand(status, f.command) {
		t.Fatalf("status: %+v %v", got, err)
	}
	if state, _ := f.rowState(t); state != "unknown" {
		t.Fatalf("blobs_missing settled the effect as %q", state)
	}
	// The sender re-uploads to the same operation and resends the same command: the claim completes.
	f.uploadAll(t)
	f.native.err = nil
	got, err = f.dispatch()
	if receipt, ok := got.(LoadImageReceipt); err != nil || !ok || receipt.State != driver.LoadStateLoaded {
		t.Fatalf("resend: %+v %v", got, err)
	}
	if n := scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE kind='load-image'`); n != 1 {
		t.Fatalf("%d load rows", n)
	}
}

// Every refusal happens before the claim: no row, no native call.
func TestLoadImageRefusesBeforeAnyClaim(t *testing.T) {
	cases := map[string]func(f *loadFixtureT){
		"descriptor of another release": func(f *loadFixtureT) {
			f.descriptor = append([]byte(" "), f.descriptor...)
		},
		"platform that is not the host": func(f *loadFixtureT) {
			other := "linux/amd64"
			if runtime.GOARCH == "amd64" {
				other = "linux/arm64"
			}
			f.command.Payload.Platform = other
			f.raw, f.sig = f.sign(t0(f), f.command)
		},
		"image entry that differs from the descriptor": func(f *loadFixtureT) {
			f.command.Payload.Layers = strings.Replace(f.command.Payload.Layers, ":", ":9", 1)
			f.raw, f.sig = f.sign(t0(f), f.command)
		},
		"manifest digest that differs from the descriptor": func(f *loadFixtureT) {
			f.command.Payload.ManifestDigest = strings.Repeat("7", 64)
			f.raw, f.sig = f.sign(t0(f), f.command)
		},
		"descriptor with a registry reference": func(f *loadFixtureT) {
			raw, _ := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
			parsed, _ := appplatform.ParseDescriptor(raw)
			f.descriptor = raw
			f.command.Payload.ReleaseSHA256 = parsed.SHA256
			f.raw, f.sig = f.sign(t0(f), f.command)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newLoadFixture(t)
			f.uploadAll(t)
			testT = t
			mutate(f)
			if _, err := f.dispatch(); err == nil {
				t.Fatal("dispatch succeeded")
			}
			if f.native.calls != 0 || scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
				t.Fatalf("a refusal left a claim or reached Docker: calls=%d", f.native.calls)
			}
		})
	}
}

var testT *testing.T

func t0(*loadFixtureT) *testing.T { return testT }

func TestLoadImageRefusesStaleAndConflictingIdentities(t *testing.T) {
	t.Run("expired command", func(t *testing.T) {
		f := newLoadFixture(t)
		c := f.command
		digest, _ := control.LoadImageLogicalDigest(c)
		c.PayloadSHA256, c.IssuedMS, c.ExpiresMS = digest, "1000", "2000"
		if _, _, err := control.SignLoadImage(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), importAuthority(), time.Now().UTC().UnixMilli()); err == nil {
			t.Fatal("expired command signed")
		}
	})
	t.Run("effect number already used by another effect", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		if _, err := f.dispatch(); err != nil {
			t.Fatal(err)
		}
		other := f.command
		other.EffectID = fixtureID(1990)
		raw, sig := f.sign(t, other)
		if _, err := f.store.DispatchLoadImage(context.Background(), raw, sig, f.descriptor, f.native, f.scratch); !errors.Is(err, ErrCommandConflict) {
			t.Fatalf("second effect in one slot: %v", err)
		}
	})
	t.Run("same effect id with another payload", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		if _, err := f.dispatch(); err != nil {
			t.Fatal(err)
		}
		other := f.command
		other.Payload.Layers = strings.Replace(other.Payload.Layers, ":", ":8", 1)
		raw, sig := f.sign(t, other)
		if _, err := f.store.DispatchLoadImage(context.Background(), raw, sig, f.descriptor, f.native, f.scratch); !errors.Is(err, ErrCommandConflict) {
			t.Fatalf("same identity, other payload: %v", err)
		}
	})
	t.Run("a fence below the installation floor", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		now := time.Now().UnixMilli()
		_, err := f.store.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,'stage',1,'stage',?,2,?,9,'applied','{}',NULL,?,?)`, fixtureID(1991), fixtureID(1992), f.command.InstallationID, strings.Repeat("a", 64), f.command.ControllerEpoch, now, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.dispatch(); !errors.Is(err, ErrCommandConflict) || f.native.calls != 0 {
			t.Fatalf("stale fence: %v calls=%d", err, f.native.calls)
		}
	})
}

// A native observation that does not describe the claimed load is never settled.
func TestLoadImageRefusesASnapshotThatDoesNotBindTheClaim(t *testing.T) {
	for name, mutate := range map[string]func(*driver.LoadImageSnapshot){
		"another image id":            func(s *driver.LoadImageSnapshot) { s.ImageID = "sha256:" + strings.Repeat("9", 64) },
		"another release":             func(s *driver.LoadImageSnapshot) { s.ReleaseSHA256 = strings.Repeat("9", 64) },
		"another platform":            func(s *driver.LoadImageSnapshot) { s.Platform = "linux/riscv64" },
		"an unknown state":            func(s *driver.LoadImageSnapshot) { s.State = "maybe" },
		"rejected without an outcome": func(s *driver.LoadImageSnapshot) { s.State = driver.LoadStateRejected },
		"loaded with an outcome":      func(s *driver.LoadImageSnapshot) { s.Outcome = driver.LoadOutcomeLoadRejected },
	} {
		t.Run(name, func(t *testing.T) {
			f := newLoadFixture(t)
			f.uploadAll(t)
			f.native.snapshot = func(r driver.LoadImageRequest, fx loadFixtureT) driver.LoadImageSnapshot {
				s := driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, fx.entry),
					Store: driver.ImageStoreClassic, State: driver.LoadStateLoaded, ObservedMS: time.Now().UnixMilli()}
				mutate(&s)
				return s
			}
			if _, err := f.dispatch(); err == nil {
				t.Fatal("a snapshot that does not bind the claim was settled")
			}
			if state, _ := f.rowState(t); state != "unknown" {
				t.Fatalf("row %q", state)
			}
		})
	}
}

func TestLoadImageSerializesLoads(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	second := f.buildCommand(t, 1950)
	secondRaw, secondSig := f.sign(t, second)
	for _, b := range f.blobs {
		uploadLoadBlob(t, f.scratch, second.OperationID, b)
	}
	f.native.onLoad = func(driver.LoadImageRequest) { time.Sleep(50 * time.Millisecond) }
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); f.dispatch() }()
	go func() {
		defer wg.Done()
		f.store.DispatchLoadImage(context.Background(), secondRaw, secondSig, f.descriptor, f.native, f.scratch)
	}()
	wg.Wait()
	if f.native.calls != 2 || f.native.maxParallel != 1 {
		t.Fatalf("calls=%d parallel=%d", f.native.calls, f.native.maxParallel)
	}
}

func TestLoadImageReadBackStates(t *testing.T) {
	read := func(f *loadFixtureT) (any, error) {
		return f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.readSig(t, f.raw), f.descriptor, f.native, f.scratch)
	}
	t.Run("no row is absent, and a read writes nothing", func(t *testing.T) {
		f := newLoadFixture(t)
		got, err := read(f)
		status, ok := got.(LoadImageStatus)
		if err != nil || !ok || status.State != "absent" || !LoadImageStatusMatchesCommand(status, f.command) {
			t.Fatalf("absent: %+v %v", got, err)
		}
		if scalar(t, f.store.db, `SELECT COUNT(*) FROM runner_commands`) != 0 {
			t.Fatal("a read wrote a row")
		}
	})
	t.Run("a held load lock is pending, not absent", func(t *testing.T) {
		f := newLoadFixture(t)
		if err := f.store.acquireLoadGate(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer f.store.releaseLoadGate()
		got, err := read(f)
		if status, ok := got.(LoadImageStatus); err != nil || !ok || status.State != "pending" {
			t.Fatalf("pending: %+v %v", got, err)
		}
	})
	t.Run("unknown with every blob in scratch is orphaned", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		f.native.err = driver.ErrContainerUncertain
		_, _ = f.dispatch()
		got, err := read(f)
		if status, ok := got.(LoadImageStatus); err != nil || !ok || status.State != "orphaned" {
			t.Fatalf("orphaned: %+v %v", got, err)
		}
	})
	t.Run("unknown with blobs lost names them", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		f.native.err = driver.ErrContainerUncertain
		_, _ = f.dispatch()
		_ = f.scratch.RemoveOperation(f.command.OperationID)
		got, err := read(f)
		status, ok := got.(LoadImageStatus)
		if err != nil || !ok || status.State != "blobs_missing" || len(status.Missing) != 2 {
			t.Fatalf("blobs_missing: %+v %v", got, err)
		}
	})
	t.Run("an image already in the daemon needs no blobs", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		f.native.err = driver.ErrContainerUncertain
		_, _ = f.dispatch()
		_ = f.scratch.RemoveOperation(f.command.OperationID)
		f.native.present = true
		got, err := read(f)
		if status, ok := got.(LoadImageStatus); err != nil || !ok || status.State != "orphaned" {
			t.Fatalf("present image: %+v %v", got, err)
		}
		// Resending completes the claim as present with no upload.
		f.native.err = nil
		f.native.snapshot = func(r driver.LoadImageRequest, fx loadFixtureT) driver.LoadImageSnapshot {
			return driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, fx.entry),
				Store: driver.ImageStoreClassic, State: driver.LoadStatePresent, ObservedMS: time.Now().UnixMilli()}
		}
		done, err := f.dispatch()
		if receipt, ok := done.(LoadImageReceipt); err != nil || !ok || receipt.State != driver.LoadStatePresent {
			t.Fatalf("present completion: %+v %v", done, err)
		}
	})
	t.Run("a settled load reads back its receipt", func(t *testing.T) {
		f := newLoadFixture(t)
		f.uploadAll(t)
		want, _ := f.dispatch()
		got, err := read(f)
		if err != nil || got.(LoadImageReceipt) != want.(LoadImageReceipt) {
			t.Fatalf("receipt read-back: %+v %v", got, err)
		}
	})
	t.Run("an execute signature is not a read", func(t *testing.T) {
		f := newLoadFixture(t)
		if _, err := f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.sig, f.descriptor, f.native, f.scratch); err == nil {
			t.Fatal("an execute signature was accepted as a read")
		}
	})
}

func TestLoadImageReceiptAndStatusParsersRefuseNonCanonicalOrMismatchedBytes(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	got, _ := f.dispatch()
	raw, _ := json.Marshal(got.(LoadImageReceipt))
	if _, err := ParseLoadImageReceipt(raw); err != nil {
		t.Fatal(err)
	}
	for name, variant := range map[string][]byte{
		"whitespace":    bytes.Replace(raw, []byte(`"state":`), []byte(`"state": `), 1),
		"unknown field": bytes.Replace(raw, []byte(`"removed":false`), []byte(`"removed":false,"x":"y"`), 1),
		"wrong state":   bytes.Replace(raw, []byte(`"state":"loaded"`), []byte(`"state":"loaded2"`), 1),
	} {
		if _, err := ParseLoadImageReceipt(variant); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	status := loadStatusFor(f.command, "blobs_missing", []string{f.blobs[0].blob.Digest}, time.Now().UnixMilli())
	encoded, _ := json.Marshal(status)
	if _, err := ParseLoadImageStatus(encoded); err != nil {
		t.Fatal(err)
	}
	bad := status
	bad.Missing = nil
	badRaw, _ := json.Marshal(bad)
	if _, err := ParseLoadImageStatus(badRaw); err == nil {
		t.Fatal("blobs_missing with no digests accepted")
	}
	bad = status
	bad.State, bad.Missing = "absent", []string{f.blobs[0].blob.Digest}
	badRaw, _ = json.Marshal(bad)
	if _, err := ParseLoadImageStatus(badRaw); err == nil {
		t.Fatal("absent with digests accepted")
	}
}

// A managed image that was pruned after its load refuses the start before any claim: no unknown effect is left
// behind, no job is created, and the reason is the typed image_missing.
func TestMigrateDataRefusesBeforeItsClaimWhenTheTargetImageIsMissing(t *testing.T) {
	m := migrateDataJournalFixture(t)
	m.native.imageErr = driver.ErrImageMissing
	before := scalar(t, m.f.store.db, `SELECT COUNT(*) FROM runner_commands`)
	if _, err := m.dispatch(t); !errors.Is(err, driver.ErrImageMissing) {
		t.Fatalf("missing image: %v", err)
	}
	if m.native.runs != 0 || m.native.imageChecks != 1 {
		t.Fatalf("native runs=%d checks=%d", m.native.runs, m.native.imageChecks)
	}
	if scalar(t, m.f.store.db, `SELECT COUNT(*) FROM runner_commands`) != before || scalar(t, m.f.store.db, `SELECT COUNT(*) FROM runner_resources WHERE kind='container' AND native_id LIKE 'migration-job/%'`) != 0 {
		t.Fatal("a refused start left a claim or a reserved job behind")
	}
	// The gate passes once the image is back, and the same signed command then runs.
	m.native.imageErr = nil
	if receipt, err := m.dispatch(t); err != nil || receipt.State != "migrated" {
		t.Fatalf("after the image returned: %+v %v", receipt, err)
	}
}

// refuseALayerUpload sends bytes that do not hash to the second layer's digest, under the plan's own declaration:
// a definite verification refusal for a blob this image needs, in the layer role.
func (f *loadFixtureT) refuseALayerUpload(t *testing.T) {
	t.Helper()
	body := append([]byte(nil), f.blobs[3].body...)
	body[len(body)-1] ^= 1
	u, _, _, refusal, err := f.scratch.Begin(f.command.OperationID, f.blobs[3].blob, 0)
	if err != nil || refusal != imagestore.RefusalNone {
		t.Fatalf("begin: %v %v", refusal, err)
	}
	if r := u.Write(body); r != imagestore.RefusalNone {
		return
	}
	if r := u.Commit(f.blobs[3].blob.Digest, f.blobs[3].blob.Size); r != imagestore.RefusalDigestMismatch {
		t.Fatalf("commit: %q", r)
	}
}

// A load waiting for a blob that can never pass ends, with no Docker call, as reverted with blob_refused.
func TestLoadImageExecuteAfterARefusedUploadSettlesBlobRefusedWithoutDocker(t *testing.T) {
	f := newLoadFixture(t)
	for _, b := range f.blobs[:3] {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
	f.native.err = driver.ErrImageBlobUnavailable
	if got, err := f.dispatch(); err != nil || got.(LoadImageStatus).State != "blobs_missing" {
		t.Fatalf("claim: %+v %v", got, err)
	}
	calls := f.native.calls
	f.refuseALayerUpload(t)
	got, err := f.dispatch()
	receipt, ok := got.(LoadImageReceipt)
	if err != nil || !ok || receipt.State != driver.LoadStateRejected || receipt.Outcome != driver.LoadOutcomeBlobRefused || receipt.ImageID != "" {
		t.Fatalf("settlement: %+v %v", got, err)
	}
	if f.native.calls != calls {
		t.Fatal("a refused blob reached Docker")
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row %q", state)
	}
	if !LoadImageReceiptMatchesCommand(receipt, f.command) {
		t.Fatal("receipt does not answer the command")
	}
	if again, err := f.dispatch(); err != nil || again.(LoadImageReceipt).Outcome != driver.LoadOutcomeBlobRefused {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

// A read-back never writes a row: it answers the typed blob_refused state, and the execute under the same effect id
// then settles the effect reverted.
func TestLoadImageReadBackAfterARefusedUploadAnswersBlobRefusedAndWritesNothing(t *testing.T) {
	f := newLoadFixture(t)
	for _, b := range f.blobs[:3] {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
	f.native.err = driver.ErrImageBlobUnavailable
	_, _ = f.dispatch()
	calls := f.native.calls
	f.refuseALayerUpload(t)
	got, err := f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.readSig(t, f.raw), f.descriptor, f.native, f.scratch)
	status, ok := got.(LoadImageStatus)
	if err != nil || !ok || status.State != "blob_refused" || f.native.calls != calls {
		t.Fatalf("read-back: %+v %v calls=%d", got, err, f.native.calls)
	}
	if state, _ := f.rowState(t); state != "unknown" {
		t.Fatalf("a read-back settled the row: %q", state)
	}
	done, err := f.dispatch()
	if receipt, ok := done.(LoadImageReceipt); err != nil || !ok || receipt.Outcome != driver.LoadOutcomeBlobRefused || f.native.calls != calls {
		t.Fatalf("execute after the read: %+v %v", done, err)
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row %q", state)
	}
}

// A refusal made under a wrong declaration never poisons the role: valid bytes uploaded again under the right one are
// stored and the load applies.
func TestLoadImageAppliesAfterAWrongDeclarationWasRefusedAndTheUploadWasCorrected(t *testing.T) {
	f := newLoadFixture(t)
	for _, b := range f.blobs[:3] {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
	f.native.err = driver.ErrImageBlobUnavailable
	_, _ = f.dispatch()
	bad := f.blobs[3].blob
	bad.DiffID = strings.Repeat("a", 64)
	u, _, _, _, _ := f.scratch.Begin(f.command.OperationID, bad, 0)
	_ = u.Write(f.blobs[3].body)
	if r := u.Commit(bad.Digest, bad.Size); r != imagestore.RefusalDiffIDMismatch {
		t.Fatalf("wrong declaration: %q", r)
	}
	f.uploadAll(t)
	f.native.err = nil
	got, err := f.dispatch()
	if receipt, ok := got.(LoadImageReceipt); err != nil || !ok || receipt.State != driver.LoadStateLoaded {
		t.Fatalf("load after correction: %+v %v", got, err)
	}
}

// An image already in the daemon settles as applied even if a blob was once refused.
func TestLoadImagePresentBeatsARefusal(t *testing.T) {
	f := newLoadFixture(t)
	for _, b := range f.blobs[:3] {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
	f.native.err = driver.ErrImageBlobUnavailable
	_, _ = f.dispatch()
	f.refuseALayerUpload(t)
	f.native.err = nil
	f.native.present = true
	f.native.snapshot = func(r driver.LoadImageRequest, fx loadFixtureT) driver.LoadImageSnapshot {
		return driver.LoadImageSnapshot{ReleaseSHA256: r.ReleaseSHA256, Platform: r.Platform, ImageID: driver.ExpectedImageID(driver.ImageStoreClassic, fx.entry),
			Store: driver.ImageStoreClassic, State: driver.LoadStatePresent, ObservedMS: time.Now().UnixMilli()}
	}
	got, err := f.dispatch()
	if receipt, ok := got.(LoadImageReceipt); err != nil || !ok || receipt.State != driver.LoadStatePresent {
		t.Fatalf("present image: %+v %v", got, err)
	}
	// And a read-back answers orphaned, not blob_refused, while the image is there.
	g2 := newLoadFixture(t)
	for _, b := range g2.blobs[:3] {
		uploadLoadBlob(t, g2.scratch, g2.command.OperationID, b)
	}
	g2.native.err = driver.ErrImageBlobUnavailable
	_, _ = g2.dispatch()
	g2.refuseALayerUpload(t)
	g2.native.present = true
	read, err := g2.store.ReadLoadImageOrStatus(context.Background(), g2.raw, g2.readSig(t, g2.raw), g2.descriptor, g2.native, g2.scratch)
	if status, ok := read.(LoadImageStatus); err != nil || !ok || status.State != "orphaned" {
		t.Fatalf("read with the image present: %+v %v", read, err)
	}
}

// A bomb uploaded under the wrong kind never reaches Docker: the load asks for the layer in the layer role and is
// told it is missing.
func TestLoadImageNeverSendsABlobStoredUnderAnotherKind(t *testing.T) {
	f := newLoadFixture(t)
	for _, b := range f.blobs[:3] {
		uploadLoadBlob(t, f.scratch, f.command.OperationID, b)
	}
	asConfig := loadTestBlob{blob: appplatform.ImageBlob{Digest: f.blobs[3].blob.Digest, Kind: appplatform.ImageBlobConfig, MediaType: appplatform.MediaTypeOCIConfig, Size: f.blobs[3].blob.Size}, body: f.blobs[3].body}
	uploadLoadBlob(t, f.scratch, f.command.OperationID, asConfig)
	missing := f.scratch.Missing(f.command.OperationID, f.entry)
	if len(missing) != 1 || missing[0] != f.blobs[3].blob.Digest {
		t.Fatalf("missing %v", missing)
	}
	if f.scratch.Source(f.command.OperationID).Verified(f.blobs[3].blob) {
		t.Fatal("the driver would accept a config-kind blob as a layer")
	}
}

func TestLoadImageReadBackRefusesADescriptorThatDoesNotBindTheCommand(t *testing.T) {
	f := newLoadFixture(t)
	f.uploadAll(t)
	f.native.err = driver.ErrContainerUncertain
	_, _ = f.dispatch()
	_, err := f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.readSig(t, f.raw), append([]byte(" "), f.descriptor...), f.native, f.scratch)
	if !errors.Is(err, ErrLoadImageRefused) {
		t.Fatalf("an unbound descriptor was answered with %v", err)
	}
}
