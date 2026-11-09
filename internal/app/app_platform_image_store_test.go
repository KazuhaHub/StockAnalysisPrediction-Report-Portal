package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func newTestImageStore(t *testing.T) *platformImageStore {
	t.Helper()
	store, err := newPlatformImageStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testBlob(body string) (appplatform.ImageBlob, []byte) {
	raw := []byte(body)
	return appplatform.ImageBlob{Digest: platformDigest(raw), Kind: appplatform.ImageBlobLayer, Size: int64(len(raw))}, raw
}

func ageFile(t *testing.T, path string, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

// Fails (keeps the file) if a name that is neither a digest nor a partial survives a start.
func TestImageStoreRecoverRemovesEveryNameThatIsNotADigestOrPartial(t *testing.T) {
	store := newTestImageStore(t)
	good := strings.Repeat("a", 64)
	for _, dir := range []string{store.staging, store.blobs} {
		os.WriteFile(filepath.Join(dir, good), []byte("x"), 0o600)
		os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
		os.WriteFile(filepath.Join(dir, strings.Repeat("A", 64)), []byte("x"), 0o600)
		os.WriteFile(filepath.Join(dir, good+".tmp"), []byte("x"), 0o600)
		os.Mkdir(filepath.Join(dir, strings.Repeat("b", 64)), 0o700)
		os.Symlink("/etc/passwd", filepath.Join(dir, strings.Repeat("c", 64)))
	}
	store.recover()
	for _, dir := range []string{store.staging, store.blobs} {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 || entries[0].Name() != good {
			t.Fatalf("%s kept %v", dir, entries)
		}
	}
}

// Fails if a resumable download is dropped inside its window, or an abandoned one is kept past it.
func TestImageStoreRecoverKeepsAFreshPartialAndRemovesAnOldOne(t *testing.T) {
	store := newTestImageStore(t)
	fresh, old := strings.Repeat("1", 64)+".partial", strings.Repeat("2", 64)+".partial"
	os.WriteFile(filepath.Join(store.staging, fresh), []byte("half"), 0o600)
	os.WriteFile(filepath.Join(store.staging, old), []byte("half"), 0o600)
	ageFile(t, filepath.Join(store.staging, old), 25*time.Hour)
	// A blob partial is never resumable: nothing is in flight after a restart.
	os.WriteFile(filepath.Join(store.blobs, strings.Repeat("3", 64)+".partial"), []byte("half"), 0o600)
	store.recover()
	if _, err := os.Stat(filepath.Join(store.staging, fresh)); err != nil {
		t.Fatal("a fresh partial was removed")
	}
	if _, err := os.Stat(filepath.Join(store.staging, old)); err == nil {
		t.Fatal("a partial older than a day was kept")
	}
	if _, err := os.Stat(filepath.Join(store.blobs, strings.Repeat("3", 64)+".partial")); err == nil {
		t.Fatal("a blob partial survived a restart")
	}
}

// Fails (places the blob) if bytes that do not hash to the name ever get that name.
func TestImageStoreBlobThatDoesNotHashToItsNameIsNeverPlaced(t *testing.T) {
	store := newTestImageStore(t)
	blob, _ := testBlob("the real bytes")
	if err := store.putBlob(blob, bytes.NewReader([]byte("the real byteX"))); err == nil {
		t.Fatal("a wrong blob was accepted")
	}
	if err := store.putBlob(blob, bytes.NewReader([]byte("the real bytes and more"))); err == nil {
		t.Fatal("a blob longer than declared was accepted")
	}
	if err := store.putBlob(blob, bytes.NewReader([]byte("the real"))); err == nil {
		t.Fatal("a short blob was accepted")
	}
	entries, _ := os.ReadDir(store.blobs)
	if len(entries) != 0 {
		t.Fatalf("a refused blob left %v", entries)
	}
}

// Fails (returns the file) if a blob that changed on disk is used, or if it stays after being found corrupt.
func TestImageStoreCorruptBlobIsRefusedAndDeletedOnUse(t *testing.T) {
	store := newTestImageStore(t)
	blob, raw := testBlob("a layer of the image")
	if err := store.putBlob(blob, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	file, err := store.openBlob(blob)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	flipped := append([]byte(nil), raw...)
	flipped[3] ^= 1
	os.WriteFile(store.blobPath(blob.Digest), flipped, 0o600)
	if _, err := store.openBlob(blob); err != errPlatformImageBlobCorrupt {
		t.Fatalf("corrupt blob: %v", err)
	}
	if _, err := os.Stat(store.blobPath(blob.Digest)); err == nil {
		t.Fatal("a corrupt blob stayed in the store")
	}
	if _, err := store.openBlob(blob); err != errPlatformImageBlobMissing {
		t.Fatalf("deleted blob: %v", err)
	}
}

// Fails if the cache evicts a package in use, a download in flight, or does not evict the oldest first.
func TestImageStoreStagingEvictsLeastRecentlyUsedAndSparesHeldAndPartials(t *testing.T) {
	store := newTestImageStore(t)
	store.stagingCap = 100
	write := func(name string, size int, age time.Duration) string {
		path := filepath.Join(store.staging, name)
		os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600)
		ageFile(t, path, age)
		return name
	}
	oldest := write(strings.Repeat("1", 64), 30, 3*time.Hour)
	held := write(strings.Repeat("2", 64), 30, 2*time.Hour)
	recent := write(strings.Repeat("3", 64), 30, time.Hour)
	partial := write(strings.Repeat("4", 64)+".partial", 10, 5*time.Hour)
	release := store.hold(held)
	defer release()
	if err := store.reserveStaging(25, ""); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{oldest: false, held: true, recent: true, partial: true} {
		_, err := os.Stat(filepath.Join(store.staging, name))
		if (err == nil) != want {
			t.Fatalf("%s present=%v want %v", name[:4], err == nil, want)
		}
	}
	// Nothing left that may be evicted: the reservation is refused instead of crossing the cap.
	if err := store.reserveStaging(75, ""); err != errPlatformImageStoreFull {
		t.Fatalf("over-cap reservation: %v", err)
	}
}

// Fails if the blob cap lets in a blob set that does not fit, or counts a blob already stored twice.
func TestImageStoreBlobCapRefusesWhatWouldNotFitAndCountsStoredBlobsOnce(t *testing.T) {
	store := newTestImageStore(t)
	a, rawA := testBlob(strings.Repeat("a", 40))
	b, rawB := testBlob(strings.Repeat("b", 40))
	store.blobCap = 100
	if err := store.putBlob(a, bytes.NewReader(rawA)); err != nil {
		t.Fatal(err)
	}
	if !store.fitsBlobs([]appplatform.ImageBlob{a, b}) {
		t.Fatal("a stored blob was counted again")
	}
	if err := store.putBlob(b, bytes.NewReader(rawB)); err != nil {
		t.Fatal(err)
	}
	c, _ := testBlob(strings.Repeat("c", 40))
	if store.fitsBlobs([]appplatform.ImageBlob{a, b, c}) {
		t.Fatal("a set over the cap was admitted")
	}
}

// Fails if a sweep removes a pinned blob, a blob in use, or one younger than the grace period, or keeps an
// old unpinned one.
func TestImageStoreSweepKeepsPinnedHeldAndYoungBlobsOnly(t *testing.T) {
	store := newTestImageStore(t)
	names := map[string]string{}
	for _, role := range []string{"pinned", "held", "young", "stray"} {
		blob, raw := testBlob("blob " + role)
		store.putBlob(blob, bytes.NewReader(raw))
		names[role] = blob.Digest
		if role != "young" {
			ageFile(t, store.blobPath(blob.Digest), 48*time.Hour)
		}
	}
	release := store.hold(names["held"])
	defer release()
	removed := store.sweep(map[string]bool{names["pinned"]: true})
	if len(removed) != 1 || removed[0] != names["stray"] {
		t.Fatalf("removed %v", removed)
	}
	for _, role := range []string{"pinned", "held", "young"} {
		if _, err := os.Stat(store.blobPath(names[role])); err != nil {
			t.Fatalf("%s blob was swept", role)
		}
	}
}

// Fails if an upload is held in memory past its bound, or its partial survives a refusal.
func TestImageStoreUploadStreamsToDiskAndDiscardsWhatExceedsItsBound(t *testing.T) {
	store := newTestImageStore(t)
	file, n, key, err := store.streamUpload(bytes.NewReader(bytes.Repeat([]byte("z"), 50)), 100)
	if err != nil || n != 50 {
		t.Fatal(n, err)
	}
	file.Close()
	if _, err := os.Stat(store.partialPath(key)); err != nil {
		t.Fatal("the upload was not staged on disk")
	}
	if _, _, _, err := store.streamUpload(bytes.NewReader(bytes.Repeat([]byte("z"), 101)), 100); err == nil {
		t.Fatal("an upload over its bound was accepted")
	}
	entries, _ := os.ReadDir(store.staging)
	if len(entries) != 1 {
		t.Fatalf("a refused upload left %v", entries)
	}
}

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func writeFileForTest(path string, body []byte) error { return os.WriteFile(path, body, 0o600) }
