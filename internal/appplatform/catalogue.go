package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const MaxCatalogueBytes = 1 << 20
const MaxCatalogueReleases = 256

var ErrInvalidCatalogue = errors.New("invalid application catalogue")

// Discovery hints only. A catalogue cannot enroll a publisher or grant execution;
// each exact descriptor must independently pass deployment publisher trust.
type CatalogueRelease struct {
	AppID            string `json:"app_id"`
	PublisherID      string `json:"publisher_id"`
	Version          string `json:"version"`
	DescriptorSHA256 string `json:"descriptor_sha256"`
	DescriptorURL    string `json:"descriptor_url"`
	SignatureURL     string `json:"signature_url"`
}

type ParsedCatalogue struct {
	SHA256   string
	Releases []CatalogueRelease
}

// Syntax policy only; network callers must separately refuse private IPs at dial
// time, bound redirects/body/time, and send no deployment authentication data.
func ValidateCatalogueURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) == 0 || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawFragment != "" || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" || strings.ContainsAny(raw, "\\\x00\r\n\t %") || (u.Port() != "" && u.Port() != "443") || u.Path == "" || path.Clean(u.Path) != u.Path || !strings.HasPrefix(u.Path, "/") || u.String() != raw {
		return ErrInvalidCatalogue
	}
	return nil
}

func ParseCatalogue(raw []byte) (ParsedCatalogue, error) {
	var zero ParsedCatalogue
	if len(raw) == 0 || len(raw) > MaxCatalogueBytes || !utf8.Valid(raw) {
		return zero, ErrInvalidCatalogue
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	if token, err := scan.Token(); err != nil || token != json.Delim('{') || !scanObject(scan, 1) {
		return zero, ErrInvalidCatalogue
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidCatalogue
	}
	var doc struct {
		Format   string             `json:"format"`
		Releases []CatalogueRelease `json:"releases"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&doc) != nil || doc.Format != "portal.catalogue/2-dev.1" || doc.Releases == nil || len(doc.Releases) > MaxCatalogueReleases {
		return zero, ErrInvalidCatalogue
	}
	seen := map[string]bool{}
	for _, r := range doc.Releases {
		identity := r.PublisherID + "/" + r.AppID + "/" + r.Version
		if !slugPattern.MatchString(r.AppID) || !slugPattern.MatchString(r.PublisherID) || !validVersion(r.Version) || !digestPattern.MatchString(r.DescriptorSHA256) || seen[identity] || seen[r.DescriptorSHA256] || ValidateCatalogueURL(r.DescriptorURL) != nil || ValidateCatalogueURL(r.SignatureURL) != nil {
			return zero, ErrInvalidCatalogue
		}
		seen[identity], seen[r.DescriptorSHA256] = true, true
	}
	sum := sha256.Sum256(raw)
	return ParsedCatalogue{SHA256: hex.EncodeToString(sum[:]), Releases: doc.Releases}, nil
}
