package appplatform

import (
	"strings"
	"testing"
)

const testLayerDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func ociManifestJSON(replace ...string) string {
	raw := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","artifactType":"application/vnd.portal.app.package.v2","config":{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2,"data":"e30="},"layers":[{"mediaType":"application/vnd.portal.app.package.v2+zip","digest":"` + testLayerDigest + `","size":1234,"annotations":{"org.opencontainers.image.title":"app.portalapp"}}],"annotations":{"org.opencontainers.image.created":"2026-11-21T00:00:00Z"}}`
	for i := 0; i+1 < len(replace); i += 2 {
		raw = strings.Replace(raw, replace[i], replace[i+1], 1)
	}
	return raw
}

func TestOCIPackageManifestAcceptsTheOrasArtifactShape(t *testing.T) {
	layer, err := ParseOCIPackageManifest([]byte(ociManifestJSON()))
	if err != nil || layer.Digest != testLayerDigest || layer.Size != 1234 {
		t.Fatal(layer, err)
	}
	if _, err := ParseOCIPackageManifest([]byte(ociManifestJSON(`,"data":"e30="`, ``, `,"annotations":{"org.opencontainers.image.created":"2026-11-21T00:00:00Z"}`, ``))); err != nil {
		t.Fatal("manifest without optional fields refused", err)
	}
}

// Each case fails closed: anything but one zip layer of the Portal package type under an empty config is
// not a Portal package.
func TestOCIPackageManifestRefusesEverythingElse(t *testing.T) {
	layer := `{"mediaType":"application/vnd.portal.app.package.v2+zip","digest":"` + testLayerDigest + `","size":1234}`
	for name, raw := range map[string]string{
		"schema version 1":    ociManifestJSON(`"schemaVersion":2`, `"schemaVersion":1`),
		"docker manifest":     ociManifestJSON(`application/vnd.oci.image.manifest.v1+json`, `application/vnd.docker.distribution.manifest.v2+json`),
		"index":               ociManifestJSON(`application/vnd.oci.image.manifest.v1+json`, `application/vnd.oci.image.index.v1+json`),
		"no media type":       ociManifestJSON(`"mediaType":"application/vnd.oci.image.manifest.v1+json",`, ``),
		"wrong artifact type": ociManifestJSON(`application/vnd.portal.app.package.v2"`, `application/vnd.example.other"`),
		"no artifact type":    ociManifestJSON(`"artifactType":"application/vnd.portal.app.package.v2",`, ``),
		"image config":        ociManifestJSON(`"mediaType":"application/vnd.oci.empty.v1+json"`, `"mediaType":"application/vnd.oci.image.config.v1+json"`),
		"config digest":       ociManifestJSON(`44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a`, strings.Repeat("d", 64)),
		"config size":         ociManifestJSON(`"size":2`, `"size":3`),
		"config data":         ociManifestJSON(`"data":"e30="`, `"data":"e30K"`),
		"wrong layer type":    ociManifestJSON(`application/vnd.portal.app.package.v2+zip`, `application/vnd.oci.image.layer.v1.tar+gzip`),
		"two layers":          ociManifestJSON(`"layers":[`, `"layers":[`+layer+`,`),
		"no layers":           ociManifestJSON(`"layers":[{"mediaType":"application/vnd.portal.app.package.v2+zip","digest":"`+testLayerDigest+`","size":1234,"annotations":{"org.opencontainers.image.title":"app.portalapp"}}]`, `"layers":[]`),
		"layer digest":        ociManifestJSON(testLayerDigest, "sha256:abc"),
		"layer sha512":        ociManifestJSON(testLayerDigest, "sha512:"+strings.Repeat("c", 128)),
		"zero size layer":     ociManifestJSON(`"size":1234`, `"size":0`),
		"oversized layer":     ociManifestJSON(`"size":1234`, `"size":402653185`),
		"layer urls":          ociManifestJSON(`"size":1234`, `"size":1234,"urls":["https://example.org/x"]`),
		"subject":             ociManifestJSON(`"layers":`, `"subject":`+layer+`,"layers":`),
		"duplicate key":       ociManifestJSON(`"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`),
		"trailing data":       ociManifestJSON() + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseOCIPackageManifest([]byte(raw)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestOCIRepositorySplitsHostAndPath(t *testing.T) {
	host, path, err := SplitOCIRepository("ghcr.io:443/kazuhahub/apps/market-monitor")
	if err != nil || host != "ghcr.io" || path != "kazuhahub/apps/market-monitor" {
		t.Fatal(host, path, err)
	}
}
