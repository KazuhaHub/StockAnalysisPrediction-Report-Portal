package journal

import (
	"context"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/imagestore"
)

// refuseTheManifestUpload sends bytes that do not hash to the manifest digest, under the manifest role.
func (f *loadFixtureT) refuseTheManifestUpload(t *testing.T) {
	t.Helper()
	body := append([]byte(nil), f.blobs[0].body...)
	body[len(body)-1] ^= 1
	u, _, _, refusal, err := f.scratch.Begin(f.command.OperationID, f.blobs[0].blob, 0)
	if err != nil || refusal != imagestore.RefusalNone {
		t.Fatalf("begin: %v %v", refusal, err)
	}
	if r := u.Write(body); r != imagestore.RefusalNone {
		t.Fatalf("write: %q", r)
	}
	if r := u.Commit(f.blobs[0].blob.Digest, f.blobs[0].blob.Size); r != imagestore.RefusalDigestMismatch {
		t.Fatalf("commit: %q", r)
	}
}

// A refused manifest ends the load as reverted with blob_refused instead of answering blobs_missing for ever, and
// Docker is never asked.
func TestLoadImageExecuteAfterARefusedManifestSettlesBlobRefusedWithoutDocker(t *testing.T) {
	f := newLoadFixture(t)
	f.native.err = driver.ErrImageBlobUnavailable
	if got, err := f.dispatch(); err != nil || got.(LoadImageStatus).State != "blobs_missing" {
		t.Fatalf("claim: %+v %v", got, err)
	}
	calls := f.native.calls
	f.refuseTheManifestUpload(t)
	got, err := f.dispatch()
	receipt, ok := got.(LoadImageReceipt)
	if err != nil || !ok || receipt.State != driver.LoadStateRejected || receipt.Outcome != driver.LoadOutcomeBlobRefused || receipt.ImageID != "" {
		t.Fatalf("settlement: %+v %v", got, err)
	}
	if f.native.calls != calls {
		t.Fatal("a refused manifest reached Docker")
	}
	if state, _ := f.rowState(t); state != "reverted" {
		t.Fatalf("row %q", state)
	}
}

func TestLoadImageReadBackAfterARefusedManifestAnswersBlobRefusedAndWritesNothing(t *testing.T) {
	f := newLoadFixture(t)
	f.native.err = driver.ErrImageBlobUnavailable
	_, _ = f.dispatch()
	calls := f.native.calls
	f.refuseTheManifestUpload(t)
	got, err := f.store.ReadLoadImageOrStatus(context.Background(), f.raw, f.readSig(t, f.raw), f.descriptor, f.native, f.scratch)
	status, ok := got.(LoadImageStatus)
	if err != nil || !ok || status.State != "blob_refused" || f.native.calls != calls {
		t.Fatalf("read-back: %+v %v calls=%d", got, err, f.native.calls)
	}
	if state, _ := f.rowState(t); state != "unknown" {
		t.Fatalf("a read-back settled the row: %q", state)
	}
}
