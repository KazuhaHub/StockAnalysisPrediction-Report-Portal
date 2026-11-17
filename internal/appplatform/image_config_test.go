package appplatform

import (
	"encoding/json"
	"errors"
	"testing"
)

// planWithConfig builds a coherent image whose config document is edited by mutate before the manifest is
// re-bound to it.
func planWithConfig(t *testing.T, mutate func(map[string]any)) error {
	t.Helper()
	layers := []imageTestLayer{newImageTestLayer(t, "etc/one", "first")}
	_, configRaw := imageTestMetadata(t, "linux/amd64", "application/vnd.oci.image.layer.v1.tar+gzip", layers)
	var config map[string]any
	if err := json.Unmarshal(configRaw, &config); err != nil {
		t.Fatal(err)
	}
	mutate(config)
	configRaw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	manifestRaw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": MediaTypeOCIManifest,
		"config": map[string]any{"mediaType": MediaTypeOCIConfig, "digest": "sha256:" + platformTestDigest(configRaw), "size": len(configRaw)},
		"layers": []map[string]any{{"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip", "digest": "sha256:" + platformTestDigest(layers[0].compressed), "size": len(layers[0].compressed)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := BackendImage{
		Platform: "linux/amd64", ManifestDigest: platformTestDigest(manifestRaw), ConfigDigest: platformTestDigest(configRaw),
		Layers: []ImageLayer{{DiffID: layers[0].diffID, UncompressedBytes: layers[0].size}},
	}
	_, err = PlanImage(entry, manifestRaw, configRaw)
	return err
}

func TestPlanImageRefusesAConfigWithoutAConfigSection(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"absent": func(c map[string]any) { delete(c, "config") },
		"null":   func(c map[string]any) { c["config"] = nil },
	} {
		err := planWithConfig(t, mutate)
		if !errors.Is(err, ErrImageConfigSectionMissing) {
			t.Errorf("%s: got %v, want ErrImageConfigSectionMissing", name, err)
		}
		if !errors.Is(err, ErrInvalidImage) {
			t.Errorf("%s: the typed error must still be an ErrInvalidImage: %v", name, err)
		}
	}
}

func TestPlanImageAcceptsAnEmptyConfigSection(t *testing.T) {
	if err := planWithConfig(t, func(c map[string]any) { c["config"] = map[string]any{} }); err != nil {
		t.Fatalf("an empty config object is a valid config: %v", err)
	}
}
