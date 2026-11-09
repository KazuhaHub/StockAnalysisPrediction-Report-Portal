package app

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The acceptance of managed image delivery on a real image (doc 167 section 3, PKG-01 and PKG-08). It runs
// only when CI hands it the Scheduled Digest image exported by the Apps repository's tooling:
//
//	APP_IMAGE_DELIVERY_CANDIDATE  the directory build-app-v2.py --images wrote (descriptor.json, frontend.zip)
//	APP_IMAGE_DELIVERY_BLOBS      the blobs/ directory export-image-oci.py wrote
//	APP_IMAGE_DELIVERY_EVIDENCE   optional path of the evidence file this test writes
//
// It proves the Portal half on real bytes: the strict parser accepts what the real builder produced, offline
// and catalogue install are the same release, and the storage scenarios hold at the real image's size. It does
// not start the image: that is the runner's native proof, which this test does not claim.
func realImageParts(t *testing.T) platformManagedImageParts {
	t.Helper()
	candidate, blobsDir := os.Getenv("APP_IMAGE_DELIVERY_CANDIDATE"), os.Getenv("APP_IMAGE_DELIVERY_BLOBS")
	if candidate == "" || blobsDir == "" {
		t.Skip("no exported image: set APP_IMAGE_DELIVERY_CANDIDATE and APP_IMAGE_DELIVERY_BLOBS")
	}
	raw, err := os.ReadFile(filepath.Join(candidate, "descriptor.json"))
	if err != nil {
		t.Fatal(err)
	}
	frontend, err := os.ReadFile(filepath.Join(candidate, "frontend.zip"))
	if err != nil {
		t.Fatal(err)
	}
	var m appplatform.Manifest
	if json.Unmarshal(raw, &m) != nil || m.ID != "scheduled-digest" || m.Backend == nil || len(m.Backend.Images) != 1 || m.Backend.Image != "" {
		t.Fatal("the candidate is not the managed Scheduled Digest descriptor")
	}
	m.Publisher = "fixture-publisher"
	m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
	m.Compatibility.RunnerProtocol = ">=1.0.0-0 <2.0.0"
	descriptor, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	entries, err := os.ReadDir(blobsDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(blobsDir, e.Name()))
		if err != nil || platformDigest(body) != e.Name() {
			t.Fatalf("exported blob %s does not hash to its name", e.Name())
		}
		blobs[e.Name()] = body
	}
	entry := m.Backend.Images[0]
	plan, err := appplatform.PlanImage(entry, blobs[entry.ManifestDigest], blobs[entry.ConfigDigest])
	if err != nil {
		t.Fatal("the real image does not plan under the Portal's parser:", err)
	}
	return platformManagedImageParts{
		descriptor: descriptor, signature: platformSignTestDescriptor(t, descriptor), frontend: frontend,
		blobs: blobs, entry: entry, plan: plan, version: m.Version, frontendName: m.Frontend.Artifact,
	}
}

func TestRealImagePackageDeliveryOfflineCatalogueAndStorageScenarios(t *testing.T) {
	parts := realImageParts(t)
	pkg := parts.packageBytes(t, nil, "")
	var total int64
	for _, blob := range parts.imageBlobList() {
		total += blob.Size
	}
	evidence := map[string]any{
		"release_sha256": platformDigest(parts.descriptor), "manifest_digest": parts.entry.ManifestDigest, "config_digest": parts.entry.ConfigDigest,
		"layers": len(parts.entry.Layers), "blobs": len(parts.imageBlobList()), "blob_bytes": total, "package_bytes": len(pkg),
	}

	// PKG-01, offline: the package stages, every blob is in the store and hashes to its name.
	offline := newPlatformImageHTTP(t, true)
	if code, body := offline.upload(pkg); code != http.StatusOK || !strings.Contains(body, platformDigest(parts.descriptor)) {
		t.Fatal("offline install:", code, body)
	}
	store, _ := offline.s.platformImages()
	for _, blob := range parts.imageBlobList() {
		file, err := store.openBlob(blob)
		if err != nil {
			t.Fatalf("offline blob %s: %v", blob.Digest[:8], err)
		}
		file.Close()
	}

	// PKG-01, catalogue: the same package arrives through the catalogue and yields the same release and blobs.
	fixture := &platformCatalogueFixture{parts: parts}
	fixture.s, fixture.actor = platformTrustTestServer(t, false)
	platformRegisterTestPublisher(t, fixture.s, fixture.actor)
	fixture.s.platformImageDir = t.TempDir()
	fixture.remote = newPlatformPackageRemote(t, parts, pkg)
	staged, err := fixture.install(context.Background())
	if err != nil || staged.sha256 != platformDigest(parts.descriptor) {
		t.Fatal("catalogue install:", err)
	}
	if got := len(fixture.blobDigests()); got != len(parts.imageBlobList()) {
		t.Fatalf("catalogue stored %d blobs, offline %d", got, len(parts.imageBlobList()))
	}

	// PKG-08: a corrupted cached blob is found on use, deleted, and healed by downloading the package again.
	layer := parts.plan.Layers[len(parts.plan.Layers)-1]
	cstore, _ := fixture.s.platformImages()
	damaged := append([]byte(nil), parts.blobs[layer.Digest]...)
	damaged[len(damaged)/2] ^= 0x5a
	if err := os.WriteFile(cstore.blobPath(layer.Digest), damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cstore.openBlob(layer); err != errPlatformImageBlobCorrupt {
		t.Fatalf("a corrupted blob was used: %v", err)
	}
	if _, err := fixture.install(context.Background()); err != nil {
		t.Fatal("re-download after corruption:", err)
	}
	if file, err := cstore.openBlob(layer); err != nil {
		t.Fatal("the blob was not restored by the re-download:", err)
	} else {
		file.Close()
	}

	// PKG-08: each cap refuses with no partial release, on the real size.
	capped := newPlatformImageHTTP(t, true)
	capStore, _ := capped.s.platformImages()
	capStore.blobCap = total - 1
	if code, body := capped.upload(pkg); code != http.StatusInsufficientStorage {
		t.Fatal("blob cap:", code, body)
	}
	if capped.releases() != 0 || len(capped.blobNames()) != 0 {
		t.Fatal("the blob cap left a release or blobs")
	}
	evidence["blob_cap_refused_without_release"] = true

	// PKG-08: the staging cap refuses a package it cannot hold before a byte is downloaded, and the blobs of the
	// installed release survive that pressure.
	fixture.remote.mu.Lock()
	before := fixture.remote.packets
	fixture.remote.mu.Unlock()
	cstore.stagingCap = 1 << 20
	if _, err := fixture.install(context.Background()); err == nil {
		t.Fatal("the staging cap admitted a package it cannot hold")
	}
	fixture.remote.mu.Lock()
	after := fixture.remote.packets
	fixture.remote.mu.Unlock()
	if after != before {
		t.Fatal("bytes were downloaded for a package the staging cap refused")
	}
	for _, blob := range parts.imageBlobList() {
		file, err := cstore.openBlob(blob)
		if err != nil {
			t.Fatalf("cache pressure removed blob %s of the installed release: %v", blob.Digest[:8], err)
		}
		file.Close()
	}
	evidence["package_over_staging_cap_refused_before_download"] = true
	evidence["installed_release_blobs_survive_cache_pressure"] = true
	evidence["corrupt_blob_deleted_and_redownloaded"] = true
	evidence["offline_and_catalogue_stage_the_same_release_and_blobs"] = true

	if path := os.Getenv("APP_IMAGE_DELIVERY_EVIDENCE"); path != "" {
		raw, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log("IMAGE_DELIVERY_REAL_PACKAGE_VERIFIED blobs=" + strconv.Itoa(len(parts.imageBlobList())))
}
