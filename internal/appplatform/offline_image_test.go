package appplatform

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"testing"
)

// imageTestLayer is a gzip-compressed layer holding one file.
type imageTestLayer struct {
	compressed []byte
	diffID     string
	size       int
}

func newImageTestLayer(t testing.TB, name, body string) imageTestLayer {
	t.Helper()
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(tarball.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return imageTestLayer{compressed: gz.Bytes(), diffID: platformTestDigest(tarball.Bytes()), size: tarball.Len()}
}

// managedOfflineParts is a coherent version 2 package that each test may
// damage in exactly one way before archiving.
type managedOfflineParts struct {
	descriptor, signature, frontend []byte
	frontendBlob                    string
	blobs                           map[string][]byte
	entry                           BackendImage
	manifest                        []byte
	config                          []byte
	layers                          []imageTestLayer
}

func imageTestMetadata(t testing.TB, platform, layerMediaType string, layers []imageTestLayer) (manifest, config []byte) {
	t.Helper()
	var diffIDs []string
	var descriptors []map[string]any
	for _, layer := range layers {
		diffIDs = append(diffIDs, "sha256:"+layer.diffID)
		descriptors = append(descriptors, map[string]any{"mediaType": layerMediaType, "digest": "sha256:" + platformTestDigest(layer.compressed), "size": len(layer.compressed)})
	}
	config, err := json.Marshal(map[string]any{
		"architecture": platformArchitecture(platform), "os": "linux",
		"config": map[string]any{"Labels": map[string]string{"a": "1", "A": "2"}},
		"rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": MediaTypeOCIConfig, "digest": "sha256:" + platformTestDigest(config), "size": len(config)},
		"layers": descriptors,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, config
}

func newManagedOfflineParts(t *testing.T) managedOfflineParts {
	t.Helper()
	layers := []imageTestLayer{newImageTestLayer(t, "etc/one", "first"), newImageTestLayer(t, "etc/two", "second")}
	manifest, config := imageTestMetadata(t, "linux/amd64", "application/vnd.oci.image.layer.v1.tar+gzip", layers)
	entry := BackendImage{Platform: "linux/amd64", ManifestDigest: platformTestDigest(manifest), ConfigDigest: platformTestDigest(config)}
	blobs := map[string][]byte{"blobs/" + entry.ManifestDigest: manifest, "blobs/" + entry.ConfigDigest: config}
	for _, layer := range layers {
		entry.Layers = append(entry.Layers, ImageLayer{DiffID: layer.diffID, UncompressedBytes: layer.size})
		blobs["blobs/"+platformTestDigest(layer.compressed)] = layer.compressed
	}
	frontend := frontendTestArchive(t, frontendTestEntry{name: "index.html", body: "<!doctype html><title>Managed</title>"})
	var d map[string]any
	if err := json.Unmarshal(descriptorFixture(t, true), &d); err != nil {
		t.Fatal(err)
	}
	backend := d["backend"].(map[string]any)
	delete(backend, "image")
	backend["platforms"] = []any{"linux/amd64"}
	rawEntry, _ := json.Marshal(entry)
	var generic any
	_ = json.Unmarshal(rawEntry, &generic)
	backend["images"] = []any{generic}
	d["frontend"].(map[string]any)["sha256"] = platformTestDigest(frontend)
	descriptor, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return managedOfflineParts{
		descriptor: descriptor, signature: offlineSignatureFixture(t, descriptor), frontend: frontend,
		frontendBlob: "blobs/" + platformTestDigest(frontend) + "/ui.zip",
		blobs:        blobs, entry: entry, manifest: manifest, config: config, layers: layers,
	}
}

func (p managedOfflineParts) archive(t testing.TB, extra ...offlineTestEntry) []byte {
	t.Helper()
	entries := []offlineTestEntry{
		{name: "descriptor.json", body: p.descriptor, mode: 0644},
		{name: "descriptor.sig", body: p.signature, mode: 0644},
		{name: p.frontendBlob, body: p.frontend, mode: 0644},
	}
	for name, body := range p.blobs {
		entries = append(entries, offlineTestEntry{name: name, body: body, mode: 0644, store: true})
	}
	return offlineTestArchive(t, append(entries, extra...)...)
}

func TestOfflineEnvelopeV2BindsImageContentToDescriptor(t *testing.T) {
	parts := newManagedOfflineParts(t)
	envelope, err := InspectOfflineEnvelope(parts.archive(t))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Inspection.Format != "portal.offline-envelope/2-dev.1" || envelope.Inspection.Execution != "managed" ||
		envelope.Inspection.ImageBlobs != 4 || len(envelope.Images) != 1 || len(envelope.Images[0].Layers) != 2 {
		t.Fatalf("unexpected inspection: %+v", envelope.Inspection)
	}
	for _, layer := range envelope.Images[0].Layers {
		stream, err := envelope.OpenBlob(layer.Digest)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(stream)
		stream.Close()
		if platformTestDigest(body) != layer.Digest {
			t.Fatal("blob stream does not match its digest")
		}
	}
	if _, err := envelope.OpenBlob(hexDigest("9")); err == nil {
		t.Fatal("blob outside the plan was readable")
	}
}

// Every case damages one rule of resolution 5 or 6 and must be refused with
// no partial bytes returned.
func TestOfflineEnvelopeV2RefusesCraftedPackages(t *testing.T) {
	cases := map[string]func(t *testing.T, p *managedOfflineParts) []byte{
		"extra blob member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			return p.archive(t, offlineTestEntry{name: "blobs/" + hexDigest("7"), body: []byte("extra"), mode: 0644, store: true})
		},
		"missing layer member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			delete(p.blobs, "blobs/"+platformTestDigest(p.layers[1].compressed))
			return p.archive(t)
		},
		"missing config member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			delete(p.blobs, "blobs/"+p.entry.ConfigDigest)
			return p.archive(t)
		},
		"deflated layer member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			name := "blobs/" + platformTestDigest(p.layers[0].compressed)
			body := p.blobs[name]
			delete(p.blobs, name)
			return p.archive(t, offlineTestEntry{name: name, body: body, mode: 0644})
		},
		"layer bytes changed refused": func(t *testing.T, p *managedOfflineParts) []byte {
			name := "blobs/" + platformTestDigest(p.layers[0].compressed)
			p.blobs[name] = append(append([]byte(nil), p.blobs[name]...), 0)
			return p.archive(t)
		},
		"layer member renamed refused": func(t *testing.T, p *managedOfflineParts) []byte {
			name := "blobs/" + platformTestDigest(p.layers[0].compressed)
			body := p.blobs[name]
			delete(p.blobs, name)
			p.blobs["blobs/"+hexDigest("8")] = body
			return p.archive(t)
		},
		"diff id differs from config refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.entry.Layers[0].DiffID = hexDigest("5")
			p.rewriteDescriptor(t)
			return p.archive(t)
		},
		"layer order swapped against descriptor refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.entry.Layers[0], p.entry.Layers[1] = p.entry.Layers[1], p.entry.Layers[0]
			p.rewriteDescriptor(t)
			return p.archive(t)
		},
		"descriptor config digest differs refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.entry.ConfigDigest = hexDigest("6")
			p.rewriteDescriptor(t)
			return p.archive(t)
		},
		"architecture differs from platform refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.useMetadata(t, "linux/arm64", "application/vnd.oci.image.layer.v1.tar+gzip")
			return p.archive(t)
		},
		"zstd layer refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.useMetadata(t, "linux/amd64", "application/vnd.oci.image.layer.v1.tar+zstd")
			return p.archive(t)
		},
		"foreign layer refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.useMetadata(t, "linux/amd64", "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip")
			return p.archive(t)
		},
		"image index as platform manifest refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.mutateManifest(t, func(m map[string]any) { m["mediaType"] = "application/vnd.oci.image.index.v1+json" })
			return p.archive(t)
		},
		"docker manifest list refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.mutateManifest(t, func(m map[string]any) { m["mediaType"] = "application/vnd.docker.distribution.manifest.list.v2+json" })
			return p.archive(t)
		},
		"layer url field refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.mutateManifest(t, func(m map[string]any) {
				m["layers"].([]any)[0].(map[string]any)["urls"] = []string{"https://example.org/l"}
			})
			return p.archive(t)
		},
		"case aliased manifest key refused": func(t *testing.T, p *managedOfflineParts) []byte {
			p.mutateManifest(t, func(m map[string]any) { m["Mediatype"] = MediaTypeOCIManifest })
			return p.archive(t)
		},
		"tag annotation in a layout refused": func(t *testing.T, p *managedOfflineParts) []byte {
			index := []byte(`{"schemaVersion":2,"manifests":[{"annotations":{"io.containerd.image.name":"portal:latest"}}]}`)
			return p.archive(t, offlineTestEntry{name: "index.json", body: index, mode: 0644})
		},
		"nested manifest json refused": func(t *testing.T, p *managedOfflineParts) []byte {
			return p.archive(t, offlineTestEntry{name: "blobs/x/manifest.json", body: []byte("[]"), mode: 0644})
		},
		"repositories member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			return p.archive(t, offlineTestEntry{name: "repositories", body: []byte("{}"), mode: 0644})
		},
		"oci layout member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			return p.archive(t, offlineTestEntry{name: "oci-layout", body: []byte(`{"imageLayoutVersion":"1.0.0"}`), mode: 0644})
		},
		"cased layout member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			return p.archive(t, offlineTestEntry{name: "OCI-Layout", body: []byte("{}"), mode: 0644})
		},
		"too many entries refused": func(t *testing.T, p *managedOfflineParts) []byte {
			var extra []offlineTestEntry
			for i := 0; i < MaxManagedOfflineEntries; i++ {
				extra = append(extra, offlineTestEntry{name: "blobs/" + platformTestDigest([]byte{byte(i), 1}), body: []byte("x"), mode: 0644, store: true})
			}
			return p.archive(t, extra...)
		},
		"oversize metadata member refused": func(t *testing.T, p *managedOfflineParts) []byte {
			name := "blobs/" + p.entry.ConfigDigest
			p.blobs[name] = bytes.Repeat([]byte(" "), MaxImageMetadataBytes+1)
			return p.archive(t)
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			parts := newManagedOfflineParts(t)
			result, err := InspectOfflineEnvelope(build(t, &parts))
			if !errors.Is(err, ErrInvalidOfflineEnvelope) || len(result.Descriptor) != 0 || len(result.Images) != 0 {
				t.Fatalf("crafted package accepted or returned partial result: %v", err)
			}
		})
	}
}

func TestOfflineEnvelopeV2AcceptsDockerSpellingsAndSharedBlobs(t *testing.T) {
	parts := newManagedOfflineParts(t)
	parts.mutateManifest(t, func(m map[string]any) {
		m["mediaType"] = MediaTypeDockerManifest
		m["config"].(map[string]any)["mediaType"] = MediaTypeDockerConfig
		for _, layer := range m["layers"].([]any) {
			layer.(map[string]any)["mediaType"] = "application/vnd.docker.image.rootfs.diff.tar.gzip"
		}
	})
	if _, err := InspectOfflineEnvelope(parts.archive(t)); err != nil {
		t.Fatalf("docker schema 2 spelling refused: %v", err)
	}
}

func TestOfflineEnvelopeV1RefusesImageMembersWithoutDeclaration(t *testing.T) {
	descriptor, signature, frontend, blob := offlineStaticFixture(t)
	raw := offlineTestArchive(t,
		offlineTestEntry{name: "descriptor.json", body: descriptor, mode: 0644},
		offlineTestEntry{name: "descriptor.sig", body: signature, mode: 0644},
		offlineTestEntry{name: blob, body: frontend, mode: 0644},
		offlineTestEntry{name: "blobs/" + hexDigest("7"), body: []byte("x"), mode: 0644, store: true})
	if _, err := InspectOfflineEnvelope(raw); !errors.Is(err, ErrInvalidOfflineEnvelope) {
		t.Fatalf("static envelope carrying image blobs accepted: %v", err)
	}
}

func (p *managedOfflineParts) rewriteDescriptor(t testing.TB) {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(p.descriptor, &d); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p.entry)
	var generic any
	_ = json.Unmarshal(raw, &generic)
	d["backend"].(map[string]any)["images"] = []any{generic}
	var err error
	if p.descriptor, err = json.Marshal(d); err != nil {
		t.Fatal(err)
	}
	p.signature = offlineSignatureFixture(t, p.descriptor)
}

// useMetadata rebuilds manifest and config for another platform or layer type
// and rebinds the descriptor, keeping the layer blobs.
func (p *managedOfflineParts) useMetadata(t testing.TB, platform, layerMediaType string) {
	t.Helper()
	p.replaceMetadata(t, func() ([]byte, []byte) { return imageTestMetadata(t, platform, layerMediaType, p.layers) })
}

func (p *managedOfflineParts) mutateManifest(t testing.TB, fn func(map[string]any)) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(p.manifest, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	p.replaceMetadata(t, func() ([]byte, []byte) {
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw, p.config
	})
}

func (p *managedOfflineParts) replaceMetadata(t testing.TB, build func() ([]byte, []byte)) {
	t.Helper()
	delete(p.blobs, "blobs/"+p.entry.ManifestDigest)
	delete(p.blobs, "blobs/"+p.entry.ConfigDigest)
	p.manifest, p.config = build()
	p.entry.ManifestDigest, p.entry.ConfigDigest = platformTestDigest(p.manifest), platformTestDigest(p.config)
	p.blobs["blobs/"+p.entry.ManifestDigest], p.blobs["blobs/"+p.entry.ConfigDigest] = p.manifest, p.config
	p.rewriteDescriptor(t)
}

// The parser decompresses every layer, so a package whose layer the runner would refuse is never staged: the
// descriptor and the config agree with each other and the blob hashes, but the tar inside does not match.
func TestOfflineEnvelopeV2RefusesALayerWhoseContentBreaksItsDeclaration(t *testing.T) {
	for name, damage := range map[string]func(p *managedOfflineParts){
		"diff id that is not the tar's": func(p *managedOfflineParts) {
			p.layers[1].diffID = platformTestDigest([]byte("another tar"))
			p.entry.Layers[1].DiffID = p.layers[1].diffID
		},
		"expands beyond the declared size": func(p *managedOfflineParts) {
			p.layers[1].size--
			p.entry.Layers[1].UncompressedBytes = p.layers[1].size
		},
		"declared larger than the tar": func(p *managedOfflineParts) {
			p.layers[1].size++
			p.entry.Layers[1].UncompressedBytes = p.layers[1].size
		},
	} {
		t.Run(name, func(t *testing.T) {
			parts := newManagedOfflineParts(t)
			damage(&parts)
			parts.useMetadata(t, "linux/amd64", "application/vnd.oci.image.layer.v1.tar+gzip")
			if _, err := InspectOfflineEnvelope(parts.archive(t)); !errors.Is(err, ErrInvalidOfflineEnvelope) {
				t.Fatalf("a layer that breaks its declaration was staged: %v", err)
			}
		})
	}
}
