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
		"unknown format refused":                 `{"format":"portal.catalogue/2-dev.3","releases":[` + catalogueEntryJSON("") + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseCatalogue([]byte(raw)); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
