package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// The image store probe of the capacity soak (doc 171, PR 4b). The runner's image store budget is the sum of the
// declared uncompressed bytes of the images it has loaded (10 GiB); a descriptor may declare at most 4 GiB for one
// image, so the third image is the one the store refuses. The images the soak loads declare about 4 GiB each, but
// their layers are zero-filled, so the blobs that cross the Portal and the control socket are a few MiB each while
// the daemon holds the real bytes. This file builds those images; the native scenario that loads them is in
// app_platform_capacity_soak_native_linux_test.go.

// soakImageLayerTarBytes is the declared size of a full filler layer: the descriptor limit for one layer. A layer
// holds one file of zeros, so its tar is 512 bytes of header, the file padded to 512 bytes and 1024 bytes of trailer.
const (
	soakImageDeclaredTarget = appplatform.MaxBackendImageUncompressedBytes - 1<<20 // a MiB under the per-image maximum
	soakImageFillerLayers   = 4
)

// soakTarBytes is the size of the tar of one file of fileBytes bytes: the header, the padded file, the trailer.
func soakTarBytes(fileBytes int64) int64 { return 512 + (fileBytes+511)/512*512 + 1024 }

// soakFillerFileBytes is the size of the file of each filler layer so that an image of the soak binary's layer and
// soakImageFillerLayers fillers declares soakImageDeclaredTarget in all, no layer exceeding the descriptor's layer limit.
func soakFillerFileBytes(binaryTarBytes int64) []int64 {
	remaining := int64(soakImageDeclaredTarget) - binaryTarBytes
	// The largest file whose tar fits one layer: 512 bytes of header and 1024 of trailer come out of the layer limit.
	largest := (int64(appplatform.MaxImageLayerUncompressedBytes) - 512 - 1024) / 512 * 512
	files := make([]int64, 0, soakImageFillerLayers)
	for len(files) < soakImageFillerLayers {
		file := largest
		if fits := (remaining - 512 - 1024) / 512 * 512; fits < file {
			file = fits
		}
		if file < 512 {
			break
		}
		files = append(files, file)
		remaining -= soakTarBytes(file)
	}
	return files
}

// soakLayerWriter hashes and counts the tar a layer is made of while it is compressed.
type soakLayerWriter struct {
	gzip *gzip.Writer
	hash interface {
		io.Writer
		Sum([]byte) []byte
	}
	tarBytes int64
}

func (w *soakLayerWriter) Write(p []byte) (int, error) {
	w.tarBytes += int64(len(p))
	w.hash.Write(p)
	return w.gzip.Write(p)
}

// soakZeroLayer is one layer: a gzip-compressed tar with one file of fileBytes bytes, all zeros but a marker that
// names the layer, so two layers of different seed or index never share a digest. It returns the compressed blob, the
// diff id (the digest of the tar) and the tar's size, which is what the descriptor declares.
func soakZeroLayer(seed string, index int, fileBytes int64) (compressed []byte, diffID string, tarBytes int64, err error) {
	var blob bytes.Buffer
	zw, err := gzip.NewWriterLevel(&blob, gzip.BestSpeed)
	if err != nil {
		return nil, "", 0, err
	}
	writer := &soakLayerWriter{gzip: zw, hash: sha256.New()}
	tw := tar.NewWriter(writer)
	if err := tw.WriteHeader(&tar.Header{Name: "soak/filler-" + seed + ".bin", Mode: 0o644, Size: fileBytes, Typeflag: tar.TypeReg}); err != nil {
		return nil, "", 0, err
	}
	chunk := make([]byte, 1<<20)
	marker := sha256.Sum256([]byte(seed + "/" + string(rune('a'+index))))
	copy(chunk, marker[:])
	for written := int64(0); written < fileBytes; {
		n := int64(len(chunk))
		if left := fileBytes - written; left < n {
			n = left
		}
		if _, err := tw.Write(chunk[:n]); err != nil {
			return nil, "", 0, err
		}
		chunk = chunk[:cap(chunk)]
		clear(chunk) // only the first chunk carries the marker
		written += n
	}
	if err := tw.Close(); err != nil {
		return nil, "", 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, "", 0, err
	}
	return blob.Bytes(), hex.EncodeToString(writer.hash.Sum(nil)), writer.tarBytes, nil
}

// soakBinaryLayer is the layer that holds the soak backend's executable.
func soakBinaryLayer(binary []byte) (compressed []byte, diffID string, tarBytes int64, err error) {
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	if err := tw.WriteHeader(&tar.Header{Name: "soak-app", Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		return nil, "", 0, err
	}
	if _, err := tw.Write(binary); err != nil {
		return nil, "", 0, err
	}
	if err := tw.Close(); err != nil {
		return nil, "", 0, err
	}
	var blob bytes.Buffer
	zw := gzip.NewWriter(&blob)
	if _, err := zw.Write(tarball.Bytes()); err != nil {
		return nil, "", 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, "", 0, err
	}
	sum := sha256.Sum256(tarball.Bytes())
	return blob.Bytes(), hex.EncodeToString(sum[:]), int64(tarball.Len()), nil
}

// soakImageRelease builds a managed release of the soak backend whose image is the soak binary plus zero-filled
// layers of the given file sizes. The image starts the soak backend, so an install of it runs through the ordinary
// worker; its declared bytes are what the runner's image store budget counts.
func soakImageRelease(t *testing.T, app, seed, platform string, binary []byte, fillers []int64, resources appplatform.Resources) platformManagedImageParts {
	t.Helper()
	raw, frontend := platformManagedReleaseFixture(t, platform)
	var m appplatform.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var entry appplatform.BackendImage
	blobs := map[string][]byte{}
	var layerDescriptors []map[string]any
	var diffIDs []string
	addLayer := func(compressed []byte, diffID string, tarBytes int64, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		digest := platformDigest(compressed)
		blobs[digest] = compressed
		diffIDs = append(diffIDs, "sha256:"+diffID)
		layerDescriptors = append(layerDescriptors, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + digest, "size": len(compressed)})
		entry.Layers = append(entry.Layers, appplatform.ImageLayer{DiffID: diffID, UncompressedBytes: int(tarBytes)})
	}
	addLayer(soakBinaryLayer(binary))
	for i, file := range fillers {
		addLayer(soakZeroLayer(seed, i, file))
	}
	config, _ := json.Marshal(map[string]any{
		"architecture": "amd64", "os": "linux",
		"config": map[string]any{"User": "65532:65532", "Entrypoint": []string{"/soak-app"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	})
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": appplatform.MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": appplatform.MediaTypeOCIConfig, "digest": "sha256:" + platformDigest(config), "size": len(config)},
		"layers": layerDescriptors,
	})
	entry.Platform, entry.ManifestDigest, entry.ConfigDigest = platform, platformDigest(manifest), platformDigest(config)
	blobs[entry.ManifestDigest], blobs[entry.ConfigDigest] = manifest, config

	m.ID = app
	m.Capabilities.Required = []string{}
	m.Backend.Image, m.Backend.Images = "", []appplatform.BackendImage{entry}
	m.Backend.Resources = resources
	descriptor, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal("soak image descriptor does not parse:", err)
	}
	plan, err := appplatform.PlanImage(parsed.Manifest.Backend.Images[0], manifest, config)
	if err != nil {
		t.Fatal(err)
	}
	return platformManagedImageParts{descriptor: descriptor, signature: platformSignTestDescriptor(t, descriptor), frontend: frontend,
		blobs: blobs, entry: entry, plan: plan, version: m.Version}
}

func TestSoakFillerLayersMakeAnImageDeclareTheMaximumLessAMiB(t *testing.T) {
	// The soak backend's own binary is some MiB of tar; the fillers bring the image to the descriptor limit.
	for _, binaryTar := range []int64{2 << 20, 6 << 20, 12 << 20} {
		files := soakFillerFileBytes(binaryTar)
		declared, layers := binaryTar, []int64{binaryTar}
		for _, file := range files {
			tarSize := soakTarBytes(file)
			if tarSize > appplatform.MaxImageLayerUncompressedBytes {
				t.Fatalf("a layer of %d bytes is over the descriptor limit of %d", tarSize, appplatform.MaxImageLayerUncompressedBytes)
			}
			declared += tarSize
			layers = append(layers, tarSize)
		}
		if declared > appplatform.MaxBackendImageUncompressedBytes || declared < soakImageDeclaredTarget-1<<20 {
			t.Fatalf("binary of %d bytes: the image declares %d (limit %d)", binaryTar, declared, appplatform.MaxBackendImageUncompressedBytes)
		}
		// Two such images fit the runner's 10 GiB image store; the third does not. The test fails if the declared size
		// ever drifts to where the store would refuse the second or accept the third.
		const store = 10 << 30
		if 2*declared > store || 3*declared <= store {
			t.Fatalf("two images of %d bytes against a store of %d", declared, store)
		}
		if len(layers) > 8 {
			t.Fatalf("%d layers", len(layers))
		}
	}
}

func TestSoakImageDeclaresWhatItsLayersHoldAndNoTwoImagesShareALayer(t *testing.T) {
	binary := bytes.Repeat([]byte{0x7f}, 4096)
	resources := appplatform.Resources{MemoryMiB: 64, CPUMillis: 50, PIDs: 64}
	a := soakImageRelease(t, "notes-soak-i-a", "a", "linux/amd64", binary, []int64{1 << 20, 1 << 20}, resources)
	b := soakImageRelease(t, "notes-soak-i-b", "b", "linux/amd64", binary, []int64{1 << 20, 1 << 20}, resources)
	if len(a.entry.Layers) != 3 {
		t.Fatalf("%d layers", len(a.entry.Layers))
	}
	seen := map[string]bool{}
	for _, image := range []platformManagedImageParts{a, b} {
		for i, layer := range image.entry.Layers {
			if i == 0 {
				continue // the soak binary layer is the same file in every image
			}
			if seen[layer.DiffID] {
				t.Fatalf("layer %s is shared between two images", layer.DiffID)
			}
			seen[layer.DiffID] = true
		}
	}
	// Every blob is what the descriptor says it is: the compressed blob inflates to a tar of exactly the declared size and digest.
	for i, layer := range a.entry.Layers {
		var compressed []byte
		for _, blob := range a.plan.Blobs() {
			if blob.Kind == appplatform.ImageBlobLayer && blob.DiffID == layer.DiffID {
				compressed = a.blobs[blob.Digest]
			}
		}
		if compressed == nil {
			t.Fatalf("layer %d has no blob", i)
		}
		zr, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.New()
		n, err := io.Copy(hash, zr)
		if err != nil || n != int64(layer.UncompressedBytes) || hex.EncodeToString(hash.Sum(nil)) != layer.DiffID {
			t.Fatalf("layer %d inflates to %d bytes (declared %d), err %v", i, n, layer.UncompressedBytes, err)
		}
	}
	// The package a release is imported from is the ordinary offline envelope; the zero-filled layers make it small.
	if size := len(a.packageBytes(t, nil, "")); size > 1<<20 {
		t.Fatalf("the package is %d bytes", size)
	}
}

func TestSoakZeroLayerIsTheDeclaredSizeAndCompressesToAlmostNothing(t *testing.T) {
	const file = 32 << 20
	compressed, diffID, tarBytes, err := soakZeroLayer("seed", 0, file)
	if err != nil {
		t.Fatal(err)
	}
	if tarBytes != soakTarBytes(file) {
		t.Fatalf("tar bytes %d, want %d", tarBytes, soakTarBytes(file))
	}
	// A 1 GiB layer must cross the Portal and the control socket as a few MiB: at least a 100 to 1 ratio on zeros.
	if ratio := float64(tarBytes) / float64(len(compressed)); ratio < 100 {
		t.Fatalf("compression ratio %.1f: a 1 GiB layer would be %d MiB", ratio, len(compressed)*32/(1<<20))
	}
	again, againID, _, _ := soakZeroLayer("seed", 0, file)
	other, otherID, _, _ := soakZeroLayer("seed", 1, file)
	if diffID != againID || !bytes.Equal(compressed, again) || diffID == otherID || bytes.Equal(compressed, other) {
		t.Fatal("a layer is deterministic by seed and index, and distinct across them")
	}
}
