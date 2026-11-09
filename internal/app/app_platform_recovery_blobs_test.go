package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// bundleWithManagedReleases returns a bundle source whose active release is a managed release with image
// content and whose retained recovery set pins a second one.
func bundleWithManagedReleases(t *testing.T) (*platformBundleSource, platformManagedImageParts, platformManagedImageParts) {
	t.Helper()
	b := platformBundleFixture(t)
	b.s.platformImageDir = t.TempDir()
	store, err := b.s.platformImages()
	if err != nil {
		t.Fatal(err)
	}
	active := newPlatformManagedImageParts(t, "5.0.0", "bundle-active")
	pinned := newPlatformManagedImageParts(t, "5.0.1", "bundle-pinned")
	activeSHA := platformInsertManagedRelease(t, b.s, b.release, active)
	pinnedSHA := platformInsertManagedRelease(t, b.s, b.release, pinned)
	for _, p := range []platformManagedImageParts{active, pinned} {
		for _, blob := range p.imageBlobList() {
			if err := store.putBlob(blob, bytes.NewReader(p.blobs[blob.Digest])); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustExec(t, b.s.st, `UPDATE app_platform_activations SET release_sha256=? WHERE installation_id=? AND state='active'`, activeSHA, b.installation)
	b.retain(t, 1, "verified", pinnedSHA, []byte("archive of the pinned release"))
	return b, active, pinned
}

func sortedDigests(p platformManagedImageParts) []string {
	var out []string
	for _, b := range p.imageBlobList() {
		out = append(out, b.Digest)
	}
	sort.Strings(out)
	return out
}

// Fails if the bundle leaves out the blobs of an active or a pinned managed release, or lists one it did not write.
func TestRecoveryBundleCarriesTheBlobsOfEveryActiveAndPinnedManagedRelease(t *testing.T) {
	b, active, pinned := bundleWithManagedReleases(t)
	dir := t.TempDir()
	manifest, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives})
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.ImageBlobs) != 8 {
		t.Fatalf("%d blobs in the manifest", len(manifest.ImageBlobs))
	}
	byRelease := map[string]platformReleaseBlob{}
	for _, r := range manifest.Releases {
		byRelease[r.ReleaseSHA256] = r
	}
	for _, p := range []platformManagedImageParts{active, pinned} {
		r := byRelease[platformDigest(p.descriptor)]
		if r.Backend != platformBlobInBundle || !reflect.DeepEqual(r.ImageBlobs, sortedDigests(p)) {
			t.Fatalf("release %s: %+v", p.version, r)
		}
	}
	for _, f := range manifest.ImageBlobs {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || platformDigest(raw) != f.SHA256 || f.Path != "app-blobs/"+f.SHA256 {
			t.Fatalf("bundle blob %s: %v", f.Path, err)
		}
	}
	if _, err := verifyPlatformRecoveryBundle(dir); err != nil {
		t.Fatal(err)
	}
}

// Fails if a bundle is written that could not restore a managed release.
func TestRecoveryBundleIsRefusedAndRemovedWhenAPinnedManagedReleaseHasLostABlob(t *testing.T) {
	b, active, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	layer := active.plan.Layers[0]
	flipped := append([]byte(nil), active.blobs[layer.Digest]...)
	flipped[7] ^= 1
	os.WriteFile(store.blobPath(layer.Digest), flipped, 0o600)
	dir := t.TempDir()
	if _, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives}); err == nil {
		t.Fatal("a bundle was written with a corrupt blob")
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("a refused bundle left %d entries", len(left))
	}
	// A missing blob is refused the same way.
	os.Remove(store.blobPath(active.plan.Config.Digest))
	if _, err := b.s.writePlatformRecoveryBundle(t.TempDir(), platformRecoveryBundleInput{Archives: b.archives}); err == nil {
		t.Fatal("a bundle was written with a missing blob")
	}
}

// Fails if the receiving host accepts a blob that was damaged in transit, renamed, or a release left pending.
func TestRecoveryBundleVerificationRefusesDamagedRenamedAndUnresolvedBlobs(t *testing.T) {
	b, _, _ := bundleWithManagedReleases(t)
	dir := t.TempDir()
	manifest, err := b.s.writePlatformRecoveryBundle(dir, platformRecoveryBundleInput{Archives: b.archives})
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, filepath.FromSlash(manifest.ImageBlobs[0].Path))
	original, _ := os.ReadFile(first)
	damaged := append([]byte(nil), original...)
	damaged[0] ^= 1
	os.WriteFile(first, damaged, 0o600)
	if _, err := verifyPlatformRecoveryBundle(dir); err == nil {
		t.Fatal("a damaged blob verified")
	}
	os.WriteFile(first, original, 0o600)
	if _, err := verifyPlatformRecoveryBundle(dir); err != nil {
		t.Fatal(err)
	}
	// A blob listed under a name other than its digest is refused even when the bytes hash as recorded.
	renamed := manifest
	renamed.ImageBlobs = append([]platformBundleFile(nil), manifest.ImageBlobs...)
	renamed.ImageBlobs[0].Path = "app-blobs/" + platformDigest([]byte("another name"))
	if err := writeManifestForTest(dir, renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPlatformRecoveryBundle(dir); err == nil {
		t.Fatal("a blob under another name verified")
	}
	pending := manifest
	pending.Releases = append([]platformReleaseBlob(nil), manifest.Releases...)
	pending.Releases[0].Backend = platformBlobPending
	if err := writeManifestForTest(dir, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPlatformRecoveryBundle(dir); err == nil {
		t.Fatal("a bundle with an unresolved managed release verified")
	}
}

// Fails (places the blob) if a blob that does not hash to its name reaches the store of the new host.
func TestRestoreOfBundleBlobsPlacesOnlyBlobsThatHashToTheirNames(t *testing.T) {
	parts := newPlatformManagedImageParts(t, "5.1.0", "restore")
	dir := t.TempDir()
	manifest := platformRecoveryBundleManifest{}
	for _, blob := range parts.imageBlobList() {
		path := filepath.Join(dir, "app-blobs", blob.Digest)
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, parts.blobs[blob.Digest], 0o600)
		manifest.ImageBlobs = append(manifest.ImageBlobs, platformBundleFile{Path: "app-blobs/" + blob.Digest, SHA256: blob.Digest, Bytes: blob.Size})
	}
	bad := parts.plan.Layers[1]
	tampered := append([]byte(nil), parts.blobs[bad.Digest]...)
	tampered[3] ^= 1
	os.WriteFile(filepath.Join(dir, "app-blobs", bad.Digest), tampered, 0o600)
	s := &Server{platformImageDir: t.TempDir()}
	if err := s.restorePlatformBundleBlobs(dir, manifest); err == nil {
		t.Fatal("a tampered blob was restored")
	}
	store, _ := s.platformImages()
	if _, err := os.Stat(store.blobPath(bad.Digest)); err == nil {
		t.Fatal("the tampered blob took its name in the store")
	}
	// The repaired bundle restores completely.
	os.WriteFile(filepath.Join(dir, "app-blobs", bad.Digest), parts.blobs[bad.Digest], 0o600)
	if err := s.restorePlatformBundleBlobs(dir, manifest); err != nil {
		t.Fatal(err)
	}
	for _, blob := range parts.imageBlobList() {
		f, err := store.openBlob(blob)
		if err != nil {
			t.Fatalf("restored blob %s: %v", blob.Digest[:8], err)
		}
		f.Close()
	}
}

// Fails if a restored managed release cannot be started because its image is never loaded: after the blobs are
// placed on a host that has an empty store, the ordinary load-image effect finds them and loads the image.
func TestRestoredManagedReleaseIsLoadedFromThePlacedBlobsBeforeItsStart(t *testing.T) {
	s, lease, endpoint, parts, fake := platformImageEnsureFixture(t)
	store, _ := s.platformImages()
	dir := t.TempDir()
	manifest := platformRecoveryBundleManifest{}
	for _, blob := range parts.imageBlobList() {
		path := filepath.Join(dir, "app-blobs", blob.Digest)
		os.MkdirAll(filepath.Dir(path), 0o700)
		os.WriteFile(path, parts.blobs[blob.Digest], 0o600)
		manifest.ImageBlobs = append(manifest.ImageBlobs, platformBundleFile{Path: "app-blobs/" + blob.Digest, SHA256: blob.Digest, Bytes: blob.Size})
		os.Remove(store.blobPath(blob.Digest)) // the new host starts empty
	}
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err == nil || len(fake.calls) != 0 {
		t.Fatalf("an empty store reached the runner: %v %v", err, fake.calls)
	}
	if err := s.restorePlatformBundleBlobs(dir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := s.ensurePlatformReleaseImage(context.Background(), lease, endpoint); err != nil || !fake.present {
		t.Fatalf("restored blobs did not load: %v %v", err, fake.calls)
	}
}

func writeManifestForTest(dir string, m platformRecoveryBundleManifest) error {
	raw, err := jsonMarshalIndent(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, platformBundleManifestName), raw, 0o600)
}

func jsonMarshalIndent(v any) ([]byte, error) { return json.MarshalIndent(v, "", "  ") }

// A retired activation of a disabled installation is still the installation's last activation: enable and repair
// need its blobs and the recovery bundle must carry them. Fails if a sweep with no grace removes them, or if the
// pinned set is smaller than the bundle's own rule.
func TestSweepKeepsTheBlobsOfARetiredActivationOfADisabledInstallationAndTheBundleStillWrites(t *testing.T) {
	b, active, pinned := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	store.grace = 0
	mustExec(t, b.s.st, `UPDATE app_platform_activations SET state='retired' WHERE installation_id=?`, b.installation)
	mustExec(t, b.s.st, `UPDATE app_platform_installations SET desired_state='disabled',phase='disabled' WHERE id=?`, b.installation)
	b.s.sweepPlatformImages(context.Background())
	for _, p := range []platformManagedImageParts{active, pinned} {
		for _, blob := range p.imageBlobList() {
			if _, err := os.Stat(store.blobPath(blob.Digest)); err != nil {
				t.Fatalf("a sweep removed blob %s of a release the bundle needs", blob.Digest[:8])
			}
		}
	}
	if _, err := b.s.writePlatformRecoveryBundle(t.TempDir(), platformRecoveryBundleInput{Archives: b.archives}); err != nil {
		t.Fatal("the bundle no longer writes:", err)
	}
	// The sweep's pins are a superset of the bundle's.
	set, err := b.s.platformPinnedBlobSet(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	required, err := platformRequiredReleaseBlobs(b.s.st.poolExec())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range required {
		var descriptor []byte
		if err := b.s.st.db.QueryRow(b.s.st.bind(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`), r.ReleaseSHA256).Scan(&descriptor); err != nil {
			t.Fatal(err)
		}
		digests, err := store.releaseBlobs(descriptor)
		if err != nil {
			continue
		}
		for _, d := range digests {
			if !set[d] {
				t.Fatalf("the bundle needs blob %s that the sweep does not pin", d[:8])
			}
		}
	}
}

// Fails if any error while computing the pinned set, a cancelled context included, lets the sweep run on what it
// had so far.
func TestSweepRemovesNothingWhenThePinnedSetCannotBeComputed(t *testing.T) {
	b, active, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	store.grace = 0
	stray := newPlatformManagedImageParts(t, "5.9.9", "stray-failclosed")
	for _, blob := range stray.imageBlobList() {
		store.putBlob(blob, bytes.NewReader(stray.blobs[blob.Digest]))
	}
	strays := func() int {
		n := 0
		for _, blob := range stray.imageBlobList() {
			if _, err := os.Stat(store.blobPath(blob.Digest)); err == nil {
				n++
			}
		}
		return n
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	b.s.sweepPlatformImages(cancelled)
	if strays() != 4 {
		t.Fatal("a cancelled sweep removed blobs")
	}
	// A pinned release whose manifest can no longer be read: the layers cannot be named, so nothing is removed.
	os.Remove(store.blobPath(active.entry.ManifestDigest))
	b.s.sweepPlatformImages(context.Background())
	if strays() != 4 {
		t.Fatal("a sweep ran with a pinned release it could not expand")
	}
	// A pinned release whose descriptor row is gone: the query fails, nothing is removed.
	mustExec(t, b.s.st, `PRAGMA foreign_keys=OFF`)
	mustExec(t, b.s.st, `DELETE FROM app_platform_releases WHERE sha256=?`, platformDigest(active.descriptor))
	b.s.sweepPlatformImages(context.Background())
	if strays() != 4 {
		t.Fatal("a sweep ran after a pinned release's descriptor query failed")
	}
}
