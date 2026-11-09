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
	"time"
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

// cloneFailedOperation adds an operation that ended recovery_required, targeting release, as the installation's
// newest operation at createdMS.
func cloneFailedOperation(t *testing.T, s *Server, from, id, release string, createdMS int64) {
	t.Helper()
	plan := platformFixtureID(40000 + int(createdMS%100000))
	mustExec(t, s.st, `INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,app_id,publisher_id,action,source_release_sha256,?,installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms
		FROM app_platform_review_plans WHERE id=(SELECT review_plan_id FROM app_platform_operations WHERE id=?)`, plan, release, from)
	mustExec(t, s.st, `INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
		SELECT ?,installation_id,?,actor_scope,?,?,action,managed,source_generation,target_generation,'recovery_required',state_revision,row_revision,NULL,NULL,fence,controller_epoch,error_code,?,?,?
		FROM app_platform_operations WHERE id=?`, id, plan, platformDigest([]byte(id)), platformDigest([]byte(plan)), createdMS, createdMS, createdMS, from)
}

func markRecovered(t *testing.T, s *Server, operation string, n int) {
	t.Helper()
	addEffectRow(t, s, operation, "recovered", "recovered", 1, platformFixtureID(60000+n))
}

// Fails if pins accumulate: after a successful retry the failed operation's target is sweepable, a stream of failed
// then recovered updates stays bounded to the one unrecovered target, and that one stays pinned while unrecovered.
func TestOnlyTheLatestUnrecoveredFailurePinsItsReleaseAndAStreamOfFailuresStaysBounded(t *testing.T) {
	b, _, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	store.grace = 0
	base := len(blobFiles(t, b.s))
	var targets []platformManagedImageParts
	for i := range 5 {
		p := newPlatformManagedImageParts(t, "7.0."+string(rune('0'+i)), "stream-"+string(rune('a'+i)))
		sha := platformInsertManagedRelease(t, b.s, b.release, p)
		for _, blob := range p.imageBlobList() {
			store.putBlob(blob, bytes.NewReader(p.blobs[blob.Digest]))
		}
		id := platformFixtureID(9100 + i)
		cloneFailedOperation(t, b.s, b.operation, id, sha, time.Now().UnixMilli()+10000+int64(i))
		if i < 4 {
			markRecovered(t, b.s, id, i)
		}
		targets = append(targets, p)
	}
	present := func(p platformManagedImageParts) int {
		n := 0
		for _, blob := range p.imageBlobList() {
			if _, err := os.Stat(store.blobPath(blob.Digest)); err == nil {
				n++
			}
		}
		return n
	}
	b.s.sweepPlatformImages(context.Background())
	for i, p := range targets {
		want := 0
		if i == 4 {
			want = 4 // the latest operation, still unrecovered
		}
		if got := present(p); got != want {
			t.Fatalf("target %d keeps %d blobs, want %d", i, got, want)
		}
	}
	if total := len(blobFiles(t, b.s)); total != base+4 {
		t.Fatalf("a stream of failures left %d blobs, want %d", total, base+4)
	}
	// A successful retry marks the failure recovered: its target is no longer needed and is swept.
	markRecovered(t, b.s, platformFixtureID(9104), 4)
	b.s.sweepPlatformImages(context.Background())
	if present(targets[4]) != 0 || len(blobFiles(t, b.s)) != base {
		t.Fatalf("a recovered failure still pins its target: %d blobs", len(blobFiles(t, b.s)))
	}
}

// Fails if anything stays pinned once the installation is uninstalled.
func TestAfterAnUninstallNothingStaysPinned(t *testing.T) {
	b, _, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	store.grace = 0
	p := newPlatformManagedImageParts(t, "7.1.0", "uninstall")
	sha := platformInsertManagedRelease(t, b.s, b.release, p)
	for _, blob := range p.imageBlobList() {
		store.putBlob(blob, bytes.NewReader(p.blobs[blob.Digest]))
	}
	cloneFailedOperation(t, b.s, b.operation, platformFixtureID(9200), sha, time.Now().UnixMilli()+20000)
	mustExec(t, b.s.st, `UPDATE app_platform_installations SET desired_state='removed',phase='removed' WHERE id=?`, b.installation)
	if err := settlePlatformPortalSideUninstall(b.s.st.poolExec(), b.installation, 3000); err != nil {
		t.Fatal(err)
	}
	b.s.sweepPlatformImages(context.Background())
	if left := blobFiles(t, b.s); len(left) != 0 {
		t.Fatalf("%d blobs stayed pinned after the uninstall", len(left))
	}
}

// The global storage status names every pinned release whose manifest cannot be read, while the sweep stays
// fail-closed for exactly those. Fails if the status misses the release, or if the sweep does not resume once the
// package is uploaded again.
func TestGlobalStorageStatusListsAnUnreadablePinnedManifestAndTheSweepResumesAfterAReupload(t *testing.T) {
	b, active, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	store.grace = 0
	stray := newPlatformManagedImageParts(t, "7.2.0", "global-stray")
	for _, blob := range stray.imageBlobList() {
		store.putBlob(blob, bytes.NewReader(stray.blobs[blob.Digest]))
	}
	empty, err := b.s.platformImageStorageStatus(context.Background())
	if err != nil || len(empty.Unreadable) != 0 {
		t.Fatalf("a healthy store reported %+v %v", empty, err)
	}
	// Corrupt the manifest of the active release.
	path := store.blobPath(active.entry.ManifestDigest)
	original, _ := os.ReadFile(path)
	os.WriteFile(path, append(original[:len(original)-2:len(original)-2], '}', ' '), 0o600)
	status, err := b.s.platformImageStorageStatus(context.Background())
	sha := platformDigest(active.descriptor)
	if err != nil || len(status.Unreadable) != 1 || status.Unreadable[0].ReleaseSHA256 != sha || status.Unreadable[0].Reason != "manifest_unreadable" {
		t.Fatalf("status: %+v %v", status, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("the status read removed the file")
	}
	b.s.sweepPlatformImages(context.Background())
	for _, blob := range stray.imageBlobList() {
		if _, err := os.Stat(store.blobPath(blob.Digest)); err != nil {
			t.Fatal("the sweep ran with an unreadable pinned manifest")
		}
	}
	// The package is uploaded again: the manifest is back, the status clears and the sweep resumes.
	os.Remove(path)
	if err := store.putBlob(active.plan.Manifest, bytes.NewReader(active.blobs[active.entry.ManifestDigest])); err != nil {
		t.Fatal(err)
	}
	if after, _ := b.s.platformImageStorageStatus(context.Background()); len(after.Unreadable) != 0 {
		t.Fatalf("the status did not clear: %+v", after)
	}
	b.s.sweepPlatformImages(context.Background())
	for _, blob := range stray.imageBlobList() {
		if _, err := os.Stat(store.blobPath(blob.Digest)); err == nil {
			t.Fatal("the sweep did not resume after the re-upload")
		}
	}
}

// Fails if a status read removes any file, or if a removal ignores that the file was replaced after it was examined.
func TestStatusReadsNeverRemoveAndAProvenWrongRemovalSparesAReplacedFile(t *testing.T) {
	b, active, _ := bundleWithManagedReleases(t)
	store, _ := b.s.platformImages()
	layer := active.plan.Layers[0]
	bad := append([]byte(nil), active.blobs[layer.Digest]...)
	bad[3] ^= 1
	manifest := store.blobPath(active.entry.ManifestDigest)
	os.WriteFile(manifest, []byte("not the manifest"), 0o600)
	os.WriteFile(store.blobPath(layer.Digest), bad, 0o600)
	before := blobFiles(t, b.s)
	for range 2 {
		b.s.platformImageStorageStatus(context.Background())
		b.s.platformImageDeliveryProjection(b.s.st.poolExec(), b.installation, platformDigest(active.descriptor))
	}
	if got := blobFiles(t, b.s); len(got) != len(before) {
		t.Fatalf("a status read removed files: %d -> %d", len(before), len(got))
	}
	// A file replaced after it was examined is not removed on the strength of the old examination.
	seen, _ := os.Lstat(manifest)
	os.Remove(manifest)
	os.WriteFile(manifest, active.blobs[active.entry.ManifestDigest], 0o600)
	store.removeProven(active.entry.ManifestDigest, seen)
	if _, err := os.Stat(manifest); err != nil {
		t.Fatal("a replaced file was removed on a stale examination")
	}
	// The write path removes a blob it proves wrong.
	if _, err := store.openBlob(layer); err == nil {
		t.Fatal("a wrong blob passed")
	}
	if _, err := os.Stat(store.blobPath(layer.Digest)); err == nil {
		t.Fatal("the write path did not remove a blob it proved wrong")
	}
}
