package appplatform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDevelopmentCatalogueContract(t *testing.T) {
	valid := `{"format":"portal.catalogue/2-dev.1","releases":[{"app_id":"notes-sample","publisher_id":"example-publisher","version":"1.0.0-beta.1","descriptor_sha256":"` + strings.Repeat("a", 64) + `","descriptor_url":"https://example.org/releases/app.json","signature_url":"https://example.org/releases/app.sig"}]}`
	parsed, err := ParseCatalogue([]byte(valid))
	if err != nil || len(parsed.Releases) != 1 || parsed.SHA256 == "" {
		t.Fatal("valid development catalogue refused", err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"format":`, `"Format":`, 1),
		strings.Replace(valid, `"format":"portal.catalogue/2-dev.1"`, `"format":"portal.catalogue/2-dev.1","format":"portal.catalogue/2-dev.1"`, 1),
		strings.Replace(valid, `"version":"1.0.0-beta.1"`, `"version":"01.0.0"`, 1),
		strings.Replace(valid, `https://example.org/releases/app.json`, `http://example.org/app.json`, 1),
		strings.Replace(valid, `https://example.org/releases/app.json`, `https://user:secret@example.org/app.json`, 1),
		strings.Replace(valid, `https://example.org/releases/app.json`, `https://example.org/app.json?token=secret`, 1),
		strings.Replace(valid, `https://example.org/releases/app.json`, `https://example.org:444/app.json`, 1),
		strings.Replace(valid, `"app_id":"notes-sample"`, `"app_id":"notes-sample","extra":true`, 1),
		`{"format":"portal.catalogue/2-dev.1","releases":null}`, valid + `{}`,
	} {
		if _, err := ParseCatalogue([]byte(raw)); err == nil {
			t.Fatal("invalid catalogue accepted", raw)
		}
	}
	for _, raw := range []string{"https://example.org/a/../app.json", "https://example.org//app.json", "https://example.org/%2e%2e/app.json", "https://example.org/app.json#fragment", "https://example.org/app.json?", "https://example.org/\\app.json"} {
		if ValidateCatalogueURL(raw) == nil {
			t.Fatal("ambiguous URL accepted", raw)
		}
	}
	if _, err := ParseCatalogue([]byte(strings.Repeat(" ", MaxCatalogueBytes+1))); err == nil {
		t.Fatal("oversized catalogue accepted")
	}
	entry := parsed.Releases[0]
	duplicate, _ := json.Marshal(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": []CatalogueRelease{entry, entry}})
	if _, err := ParseCatalogue(duplicate); err == nil {
		t.Fatal("duplicate release accepted")
	}
	oversized, _ := json.Marshal(map[string]any{"format": "portal.catalogue/2-dev.1", "releases": make([]CatalogueRelease, MaxCatalogueReleases+1)})
	if _, err := ParseCatalogue(oversized); err == nil {
		t.Fatal("too many catalogue releases accepted")
	}
}

func catalogueEntryJSON(extra string) string {
	return `{"app_id":"notes-sample","publisher_id":"example-publisher","version":"1.0.0-beta.1","descriptor_sha256":"` + strings.Repeat("a", 64) + `","descriptor_url":"https://example.org/releases/app.json","signature_url":"https://example.org/releases/app.sig"` + extra + `}`
}

func TestCatalogueV2Dev2CarriesPackageURL(t *testing.T) {
	managed := `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON(`,"package_url":"https://example.org/releases/app.portalapp"`) + `]}`
	parsed, err := ParseCatalogue([]byte(managed))
	if err != nil || parsed.Releases[0].PackageURL != "https://example.org/releases/app.portalapp" {
		t.Fatal("managed catalogue refused", err)
	}
	staticOnly := `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON("") + `]}`
	if _, err := ParseCatalogue([]byte(staticOnly)); err != nil {
		t.Fatal("frontend-only release without a package refused", err)
	}
}

// Each case fails closed: the package URL is fetched and trusted as an exact
// byte source, so it follows the same URL policy as the descriptor.
func TestCatalogueV2Dev2RefusesUnsafePackageURLAndOldFormatPackage(t *testing.T) {
	for name, raw := range map[string]string{
		"2-dev.1 carrying a package url refused": `{"format":"portal.catalogue/2-dev.1","releases":[` + catalogueEntryJSON(`,"package_url":"https://example.org/a.portalapp"`) + `]}`,
		"plain http package url refused":         `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON(`,"package_url":"http://example.org/a.portalapp"`) + `]}`,
		"credentialed package url refused":       `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON(`,"package_url":"https://u:p@example.org/a.portalapp"`) + `]}`,
		"query package url refused":              `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON(`,"package_url":"https://example.org/a.portalapp?x=1"`) + `]}`,
		"empty package url refused":              `{"format":"portal.catalogue/2-dev.2","releases":[` + catalogueEntryJSON(`,"package_url":""`) + `]}`,
		"unknown format refused":                 `{"format":"portal.catalogue/2-dev.9","releases":[` + catalogueEntryJSON("") + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalogue([]byte(raw)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func ociEntry(repository, digest string) string {
	return `,"package_oci":{"repository":"` + repository + `","digest":"` + digest + `"}`
}

func TestCatalogueV2Dev3CarriesAnOCIPackage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	raw := `{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON(ociEntry("ghcr.io/kazuhahub/portal-apps/market-monitor", digest)) + `]}`
	parsed, err := ParseCatalogue([]byte(raw))
	if err != nil || parsed.Releases[0].PackageOCI == nil || parsed.Releases[0].PackageOCI.Repository != "ghcr.io/kazuhahub/portal-apps/market-monitor" || parsed.Releases[0].PackageOCI.Digest != digest || parsed.Releases[0].PackageURL != "" {
		t.Fatal("OCI catalogue refused", err)
	}
	for _, accepted := range []string{
		`{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON(`,"package_url":"https://example.org/a.portalapp"`) + `]}`,
		`{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON("") + `]}`,
		`{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON(ociEntry("ghcr.io:443/a/b_c__d-e.f", digest)) + `]}`,
		`{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON(ociEntry("registry.example.org/x", digest)) + `]}`,
	} {
		if _, err := ParseCatalogue([]byte(accepted)); err != nil {
			t.Fatal("refused", accepted, err)
		}
	}
}

// Each case fails closed: the repository and digest are the only authority for which bytes are fetched.
func TestCatalogueRefusesUnsafeOrAmbiguousOCIPackages(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	wrap := func(format, extra string) string {
		return `{"format":"` + format + `","releases":[` + catalogueEntryJSON(extra) + `]}`
	}
	for name, raw := range map[string]string{
		"2-dev.2 with package_oci": wrap("portal.catalogue/2-dev.2", ociEntry("ghcr.io/a/b", digest)),
		"both url and oci":         wrap("portal.catalogue/2-dev.3", `,"package_url":"https://example.org/a.portalapp"`+ociEntry("ghcr.io/a/b", digest)),
		"null oci":                 wrap("portal.catalogue/2-dev.3", `,"package_oci":null`),
		"empty oci":                wrap("portal.catalogue/2-dev.3", `,"package_oci":{}`),
		"extra oci field":          wrap("portal.catalogue/2-dev.3", `,"package_oci":{"repository":"ghcr.io/a/b","digest":"`+digest+`","tag":"latest"}`),
		"tag instead of digest":    wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b", "latest")),
		"upper-case digest":        wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b", "sha256:"+strings.Repeat("B", 64))),
		"sha512 digest":            wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b", "sha512:"+strings.Repeat("b", 128))),
		"short digest":             wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b", "sha256:"+strings.Repeat("b", 63))),
		"repository with tag":      wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b:latest", digest)),
		"repository with digest":   wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b@"+digest, digest)),
		"upper-case path":          wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/A/b", digest)),
		"upper-case host":          wrap("portal.catalogue/2-dev.3", ociEntry("GHCR.io/a/b", digest)),
		"no path":                  wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io", digest)),
		"scheme":                   wrap("portal.catalogue/2-dev.3", ociEntry("https://ghcr.io/a/b", digest)),
		"userinfo":                 wrap("portal.catalogue/2-dev.3", ociEntry("u:p@ghcr.io/a/b", digest)),
		"query":                    wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b?x=1", digest)),
		"other port":               wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io:5000/a/b", digest)),
		"empty segment":            wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a//b", digest)),
		"dot segment":              wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/../b", digest)),
		"trailing slash":           wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b/", digest)),
		"bad separator":            wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/b___c", digest)),
		"leading separator":        wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/-b", digest)),
		"localhost":                wrap("portal.catalogue/2-dev.3", ociEntry("localhost/a/b", digest)),
		"ipv6 literal":             wrap("portal.catalogue/2-dev.3", ociEntry("[::1]/a/b", digest)),
		"percent":                  wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/a/%62", digest)),
		"overlong":                 wrap("portal.catalogue/2-dev.3", ociEntry("ghcr.io/"+strings.Repeat("a", 250), digest)),
		"unknown future format":    wrap("portal.catalogue/2-dev.4", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalogue([]byte(raw)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
