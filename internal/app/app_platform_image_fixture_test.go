package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// platformManagedImageParts is a coherent managed release: a signed descriptor whose backend carries
// backend.images, the frontend, and the blobs of a tiny two-layer image.
type platformManagedImageParts struct {
	descriptor, signature, frontend []byte
	blobs                           map[string][]byte // by digest
	entry                           appplatform.BackendImage
	plan                            appplatform.ImagePlan
	version                         string
	// frontendName is the member name of the frontend archive; empty means "ui.zip".
	frontendName string
}

func platformImageLayer(t testing.TB, name, body string) (compressed []byte, diffID string, size int) {
	t.Helper()
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write([]byte(body))
	tw.Close()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(tarball.Bytes())
	zw.Close()
	return gz.Bytes(), platformDigest(tarball.Bytes()), tarball.Len()
}

// newPlatformManagedImageParts builds a managed release for the fixture publisher. Layers differ by seed, so two
// releases never share a blob unless a test asks them to.
func newPlatformManagedImageParts(t *testing.T, version, seed string) platformManagedImageParts {
	t.Helper()
	_, _, frontend := platformSignedReleaseFixture(t, version)
	var layerDescriptors []map[string]any
	var diffIDs []string
	blobs := map[string][]byte{}
	var entry appplatform.BackendImage
	for i, body := range []string{seed + "-one", seed + "-two"} {
		compressed, diffID, size := platformImageLayer(t, "etc/layer"+string(rune('a'+i)), body)
		digest := platformDigest(compressed)
		blobs[digest] = compressed
		diffIDs = append(diffIDs, "sha256:"+diffID)
		layerDescriptors = append(layerDescriptors, map[string]any{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + digest, "size": len(compressed)})
		entry.Layers = append(entry.Layers, appplatform.ImageLayer{DiffID: diffID, UncompressedBytes: size})
	}
	config, _ := json.Marshal(map[string]any{
		"architecture": "amd64", "os": "linux", "config": map[string]any{},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	})
	manifest, _ := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": appplatform.MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": appplatform.MediaTypeOCIConfig, "digest": "sha256:" + platformDigest(config), "size": len(config)},
		"layers": layerDescriptors,
	})
	entry.Platform, entry.ManifestDigest, entry.ConfigDigest = "linux/amd64", platformDigest(manifest), platformDigest(config)
	blobs[entry.ManifestDigest], blobs[entry.ConfigDigest] = manifest, config

	raw, err := os.ReadFile("../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("example-publisher"), []byte("fixture-publisher"))
	raw = bytes.ReplaceAll(raw, []byte("1.0.0-beta.1"), []byte(version))
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	backend := d["backend"].(map[string]any)
	delete(backend, "image")
	backend["platforms"] = []any{"linux/amd64"}
	var generic any
	encoded, _ := json.Marshal(entry)
	_ = json.Unmarshal(encoded, &generic)
	backend["images"] = []any{generic}
	d["compatibility"] = map[string]any{"platform_api": ">=2.0.0-0 <3.0.0", "runner_protocol": ">=1.0.0-0 <2.0.0"}
	d["capabilities"] = map[string]any{"required": []any{}, "optional": []any{}}
	d["frontend"].(map[string]any)["sha256"] = platformDigest(frontend)
	d["frontend"].(map[string]any)["artifact"] = "ui.zip"
	descriptor, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal("managed fixture descriptor does not parse:", err)
	}
	plan, err := appplatform.PlanImage(parsed.Manifest.Backend.Images[0], manifest, config)
	if err != nil {
		t.Fatal(err)
	}
	return platformManagedImageParts{
		descriptor: descriptor, signature: platformSignTestDescriptor(t, descriptor), frontend: frontend,
		blobs: blobs, entry: entry, plan: plan, version: version,
	}
}

// packageBytes is the version 2 envelope: stored image blobs, flat under blobs/.
func (p platformManagedImageParts) packageBytes(t testing.TB, extra map[string][]byte, omit string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	write := func(name string, body []byte, store bool) {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if store {
			header.Method = zip.Store
		}
		header.SetMode(0o644)
		f, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(body)
	}
	write("descriptor.json", p.descriptor, false)
	write("descriptor.sig", p.signature, false)
	name := p.frontendName
	if name == "" {
		name = "ui.zip"
	}
	write("blobs/"+platformDigest(p.frontend)+"/"+name, p.frontend, false)
	for digest, body := range p.blobs {
		if digest != omit {
			write("blobs/"+digest, body, true)
		}
	}
	for name, body := range extra {
		write(name, body, strings.HasPrefix(name, "blobs/"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (p platformManagedImageParts) imageBlobList() []appplatform.ImageBlob { return p.plan.Blobs() }

// platformInsertManagedRelease puts the release row directly, as a staged release would be, using the
// publisher and key of an existing release. It is for tests of the stores, not of trust.
func platformInsertManagedRelease(t *testing.T, s *Server, like string, p platformManagedImageParts) string {
	t.Helper()
	sha := platformDigest(p.descriptor)
	mustExec(t, s.st, `INSERT INTO app_platform_releases(sha256,app_id,publisher_id,version,descriptor,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,managed,created_ms)
		SELECT ?,app_id,publisher_id,?,?,signature,verification_key_id,trust_revision,verified_ms,frontend_sha256,1,created_ms FROM app_platform_releases WHERE sha256=?`,
		sha, p.version, p.descriptor, like)
	return sha
}
