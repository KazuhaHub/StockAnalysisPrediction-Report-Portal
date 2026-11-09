package imagestore

import (
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type imageFixture struct {
	entry                      appplatform.BackendImage
	manifest, config           appplatform.ImageBlob
	manifestBytes, configBytes []byte
	layers                     []testLayer
}

func newImageFixture(t *testing.T) imageFixture {
	t.Helper()
	f := imageFixture{layers: []testLayer{newLayer(t, "one"), newLayer(t, "two")}}
	var diffIDs []string
	var descriptors []map[string]any
	for _, l := range f.layers {
		diffIDs = append(diffIDs, "sha256:"+l.blob.DiffID)
		descriptors = append(descriptors, map[string]any{"mediaType": l.blob.MediaType, "digest": "sha256:" + l.blob.Digest, "size": l.blob.Size})
		f.entry.Layers = append(f.entry.Layers, appplatform.ImageLayer{DiffID: l.blob.DiffID, UncompressedBytes: int(l.blob.UncompressedBytes)})
	}
	f.configBytes, _ = json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": diffIDs}})
	f.manifestBytes, _ = json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": appplatform.MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": appplatform.MediaTypeOCIConfig, "digest": "sha256:" + sum(f.configBytes), "size": len(f.configBytes)},
		"layers": descriptors,
	})
	f.entry.Platform, f.entry.ManifestDigest, f.entry.ConfigDigest = "linux/amd64", sum(f.manifestBytes), sum(f.configBytes)
	f.manifest = appplatform.ImageBlob{Digest: f.entry.ManifestDigest, Kind: appplatform.ImageBlobManifest, MediaType: appplatform.MediaTypeOCIManifest, Size: int64(len(f.manifestBytes))}
	f.config = appplatform.ImageBlob{Digest: f.entry.ConfigDigest, Kind: appplatform.ImageBlobConfig, MediaType: appplatform.MediaTypeOCIConfig, Size: int64(len(f.configBytes))}
	return f
}
