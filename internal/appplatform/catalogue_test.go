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
