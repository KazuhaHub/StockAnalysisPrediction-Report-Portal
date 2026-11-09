package app

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type platformImageHTTP struct {
	t      *testing.T
	s      *Server
	mux    *http.ServeMux
	cookie string
}

func newPlatformImageHTTP(t *testing.T, registerPublisher bool) *platformImageHTTP {
	t.Helper()
	s, actor := platformTrustTestServer(t, false)
	if registerPublisher {
		platformRegisterTestPublisher(t, s, actor)
	}
	s.platformImageDir = t.TempDir()
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	return &platformImageHTTP{t: t, s: s, mux: mux, cookie: s.managedCookie(*s.st.GetUser(actor.username), actor.sessionID, actor.expires.Unix())}
}

func (h *platformImageHTTP) upload(body []byte) (int, string) {
	response := platformOfflineRequest(h.mux, h.cookie, "application/zip", body)
	return response.Code, response.Body.String()
}

func (h *platformImageHTTP) blobNames() []string {
	var names []string
	entries, _ := os.ReadDir(filepath.Join(h.s.platformImageDir, "app-blobs"))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (h *platformImageHTTP) releases() int {
	return scalar[int](h.t, h.s.st, `SELECT COUNT(*) FROM app_platform_releases`)
}

// Fails if the release row is written without its blobs, or if a blob is stored under a name it does not hash to.
func TestOfflineManagedPackageStoresEveryBlobAndLeavesNoPartialFile(t *testing.T) {
	h := newPlatformImageHTTP(t, true)
	parts := newPlatformManagedImageParts(t, "2.0.0", "offline")
	code, body := h.upload(parts.packageBytes(t, nil, ""))
	if code != http.StatusOK || !strings.Contains(body, `"execution":"managed"`) || !strings.Contains(body, `"image_blobs":4`) {
		t.Fatal(code, body)
	}
	store, _ := h.s.platformImages()
	for _, blob := range parts.imageBlobList() {
		file, err := store.openBlob(blob)
		if err != nil {
			t.Fatalf("blob %s: %v", blob.Digest[:8], err)
		}
		file.Close()
	}
	if len(h.blobNames()) != 4 || h.releases() != 1 {
		t.Fatalf("blobs %v releases %d", h.blobNames(), h.releases())
	}
	for _, dir := range []string{"app-staging", "app-blobs"} {
		entries, _ := os.ReadDir(filepath.Join(h.s.platformImageDir, dir))
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".partial") {
				t.Fatalf("%s kept %s", dir, e.Name())
			}
		}
	}
	// The finished package stays in the cache under its own digest, within the cap.
	if _, err := os.Stat(filepath.Join(h.s.platformImageDir, "app-staging", platformDigest(parts.packageBytes(t, nil, "")))); err == nil {
		t.Log("package cached")
	}
}

// Fails if a package over the blob cap writes a release row or a blob.
func TestOfflineManagedPackageOverTheBlobCapLeavesNoReleaseRowAndNoBlob(t *testing.T) {
	h := newPlatformImageHTTP(t, true)
	store, _ := h.s.platformImages()
	store.blobCap = 100
	parts := newPlatformManagedImageParts(t, "2.0.1", "cap")
	code, body := h.upload(parts.packageBytes(t, nil, ""))
	if code != http.StatusInsufficientStorage || !strings.Contains(body, "app_platform_storage_full") {
		t.Fatal(code, body)
	}
	if h.releases() != 0 || len(h.blobNames()) != 0 {
		t.Fatalf("a refused package left %d releases and blobs %v", h.releases(), h.blobNames())
	}
	// The same package fits once the cap allows it: the refusal was about space and nothing else.
	store.blobCap = platformBlobCapBytes
	if code, body := h.upload(parts.packageBytes(t, nil, "")); code != http.StatusOK {
		t.Fatal(code, body)
	}
}

// Fails if a second release is staged while its blobs would push the store over the cap, even when the first
// release's blobs are the ones that fill it: stored blobs must not be evicted to make room.
func TestOfflineManagedPackageCapRefusalNeverEvictsAnotherReleasesBlobs(t *testing.T) {
	h := newPlatformImageHTTP(t, true)
	first := newPlatformManagedImageParts(t, "2.1.0", "first")
	if code, body := h.upload(first.packageBytes(t, nil, "")); code != http.StatusOK {
		t.Fatal(code, body)
	}
	store, _ := h.s.platformImages()
	store.blobCap = store.blobBytes() + 10
	second := newPlatformManagedImageParts(t, "2.1.1", "second")
	if code, _ := h.upload(second.packageBytes(t, nil, "")); code != http.StatusInsufficientStorage {
		t.Fatal("the second package was not refused for space")
	}
	for _, blob := range first.imageBlobList() {
		if file, err := store.openBlob(blob); err != nil {
			t.Fatalf("a blob of the first release was lost: %v", err)
		} else {
			file.Close()
		}
	}
	if h.releases() != 1 {
		t.Fatal("a release row was written for the refused package")
	}
}

// Fails if any crafted package writes a release row or a blob.
func TestOfflineManagedPackagesWithCraftedLayoutsAreRefusedAndStoreNothing(t *testing.T) {
	parts := newPlatformManagedImageParts(t, "2.2.0", "crafted")
	someBlob := parts.imageBlobList()[2].Digest
	cases := map[string][]byte{
		"tag annotation in index.json": parts.packageBytes(t, map[string][]byte{"index.json": []byte(`{"manifests":[{"annotations":{"io.containerd.image.name":"portal:latest"}}]}`)}, ""),
		"publisher manifest.json":      parts.packageBytes(t, map[string][]byte{"manifest.json": []byte(`[]`)}, ""),
		"oci-layout":                   parts.packageBytes(t, map[string][]byte{"oci-layout": []byte(`{}`)}, ""),
		"repositories":                 parts.packageBytes(t, map[string][]byte{"repositories": []byte(`{}`)}, ""),
		"extra blob member":            parts.packageBytes(t, map[string][]byte{"blobs/" + strings.Repeat("e", 64): []byte("extra")}, ""),
		"missing layer":                parts.packageBytes(t, nil, someBlob),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			h := newPlatformImageHTTP(t, true)
			code, body := h.upload(raw)
			if code == http.StatusOK {
				t.Fatal("a crafted package was accepted:", body)
			}
			if h.releases() != 0 || len(h.blobNames()) != 0 {
				t.Fatalf("a refused package left %d releases and blobs %v", h.releases(), h.blobNames())
			}
		})
	}
}

// Fails if blobs of a package whose signature or publisher is not trusted stay in the store.
func TestOfflineManagedPackageFromAnUntrustedPublisherLeavesNoBlobs(t *testing.T) {
	h := newPlatformImageHTTP(t, false)
	parts := newPlatformManagedImageParts(t, "2.3.0", "untrusted")
	code, _ := h.upload(parts.packageBytes(t, nil, ""))
	if code == http.StatusOK {
		t.Fatal("an untrusted package was accepted")
	}
	if h.releases() != 0 || len(h.blobNames()) != 0 {
		t.Fatalf("an untrusted package left %d releases and blobs %v", h.releases(), h.blobNames())
	}
}

// Fails if re-uploading a package does not replace a blob that has gone bad, which is how PKG-08 recovers.
func TestOfflineReuploadReplacesACorruptedBlobAndTheReleaseIsReused(t *testing.T) {
	h := newPlatformImageHTTP(t, true)
	parts := newPlatformManagedImageParts(t, "2.4.0", "heal")
	raw := parts.packageBytes(t, nil, "")
	if code, body := h.upload(raw); code != http.StatusOK {
		t.Fatal(code, body)
	}
	store, _ := h.s.platformImages()
	layer := parts.plan.Layers[0]
	bad := append([]byte(nil), parts.blobs[layer.Digest]...)
	bad[10] ^= 1
	os.WriteFile(store.blobPath(layer.Digest), bad, 0o600)
	if _, err := store.openBlob(layer); err == nil {
		t.Fatal("the damaged blob passed")
	}
	// openBlob deleted it; put a damaged copy back to prove the re-upload replaces a present bad blob too.
	os.WriteFile(store.blobPath(layer.Digest), bad, 0o600)
	code, body := h.upload(raw)
	if code != http.StatusOK || !bytes.Contains([]byte(body), []byte(`"reused":true`)) {
		t.Fatal(code, body)
	}
	file, err := store.openBlob(layer)
	if err != nil {
		t.Fatalf("the blob was not replaced: %v", err)
	}
	file.Close()
	if h.releases() != 1 {
		t.Fatal("a second release row appeared")
	}
}

// Fails if the active release's blobs are removed by a sweep, a cache-pressure eviction or a restart, or if a
// release nothing pins keeps its blobs for ever.
func TestPinnedBlobsOfActiveAndRetainedReleasesSurviveEverySweepAndOthersDoNot(t *testing.T) {
	b := platformBundleFixture(t)
	b.s.platformImageDir = t.TempDir()
	store, err := b.s.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	store.grace = 0
	active := newPlatformManagedImageParts(t, "3.0.0", "active")
	held := newPlatformManagedImageParts(t, "3.0.1", "retained")
	stray := newPlatformManagedImageParts(t, "3.0.2", "stray")
	activeSHA := platformInsertManagedRelease(t, b.s, b.release, active)
	heldSHA := platformInsertManagedRelease(t, b.s, b.release, held)
	platformInsertManagedRelease(t, b.s, b.release, stray)
	for _, p := range []platformManagedImageParts{active, held, stray} {
		for _, blob := range p.imageBlobList() {
			if err := store.putBlob(blob, bytes.NewReader(p.blobs[blob.Digest])); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustExec(t, b.s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, activeSHA, b.installation)
	b.retain(t, 1, "verified", heldSHA, []byte("archive"))
	// Cache pressure on the staging area must not touch the blob store.
	store.stagingCap = 1
	_, _ = store.reserveStaging(1<<20, "")
	b.s.platformImageRecover()
	b.s.sweepPlatformImages(t.Context())
	present := func(p platformManagedImageParts) int {
		n := 0
		for _, blob := range p.imageBlobList() {
			if _, err := os.Stat(store.blobPath(blob.Digest)); err == nil {
				n++
			}
		}
		return n
	}
	if present(active) != 4 || present(held) != 4 {
		t.Fatalf("pinned blobs were removed: active %d retained %d", present(active), present(held))
	}
	if present(stray) != 0 {
		t.Fatalf("an unpinned release kept %d blobs", present(stray))
	}
	// Once the recovery set is deleted nothing pins the retained release any more.
	mustExec(t, b.s.st, `UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=2`)
	b.s.sweepPlatformImages(t.Context())
	if present(held) != 0 || present(active) != 4 {
		t.Fatalf("after the set was deleted: active %d retained %d", present(active), present(held))
	}
	_ = time.Now
}
