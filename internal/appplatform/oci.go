package appplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"regexp"
	"strings"
)

// The OCI shape of a catalogue package (doc 188): one zip layer holding the .portalapp, under the OCI empty
// config. Anything else is not a Portal package.
const (
	OCIManifestMediaType     = "application/vnd.oci.image.manifest.v1+json"
	OCIPackageArtifactType   = "application/vnd.portal.app.package.v2"
	OCIPackageLayerMediaType = "application/vnd.portal.app.package.v2+zip"
	ociEmptyMediaType        = "application/vnd.oci.empty.v1+json"
	ociEmptyDigest           = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	// MaxOCIManifestBytes bounds the manifest read; a Portal package manifest is well under 1 KiB.
	MaxOCIManifestBytes = 256 << 10
)

var ErrInvalidOCIPackage = errors.New("invalid OCI application package")

var (
	ociPathPattern   = regexp.MustCompile(`^[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:\.|_|__|-+)[a-z0-9]+)*)*$`)
	ociHostPattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
	ociDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// CataloguePackageOCI names a package by registry repository and manifest digest. There is no tag: the reference
// is immutable.
type CataloguePackageOCI struct {
	Repository string `json:"repository"`
	Digest     string `json:"digest"`
}

// OCILayer is the one layer of a package manifest.
type OCILayer struct {
	Digest string
	Size   int64
}

// ValidOCIDigest reports whether digest is a lower-case sha256 OCI digest.
func ValidOCIDigest(digest string) bool { return ociDigestPattern.MatchString(digest) }

// SplitOCIRepository validates host[:443]/path and returns the host without the port and the repository path.
// Syntax only: callers must still refuse private addresses at dial time.
func SplitOCIRepository(repository string) (host, path string, err error) {
	if len(repository) == 0 || len(repository) > 255 {
		return "", "", ErrInvalidOCIPackage
	}
	authority, path, found := strings.Cut(repository, "/")
	if !found {
		return "", "", ErrInvalidOCIPackage
	}
	host = authority
	if h, port, ok := strings.Cut(authority, ":"); ok {
		if port != "443" {
			return "", "", ErrInvalidOCIPackage
		}
		host = h
	}
	if !ociHostPattern.MatchString(host) || host == "localhost" || !ociPathPattern.MatchString(path) {
		return "", "", ErrInvalidOCIPackage
	}
	// A dotted all-digit host is an IPv4 literal; it must be a well-formed one.
	if strings.Trim(host, "0123456789.") == "" && net.ParseIP(host).To4() == nil {
		return "", "", ErrInvalidOCIPackage
	}
	return host, path, nil
}

func validCataloguePackageOCI(p *CataloguePackageOCI) bool {
	if p == nil || !ValidOCIDigest(p.Digest) {
		return false
	}
	_, _, err := SplitOCIRepository(p.Repository)
	return err == nil
}

type ociPackageDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Data        *string           `json:"data,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ParseOCIPackageManifest checks a manifest is exactly a Portal package artifact and returns its layer. The
// caller has already checked the manifest bytes against the catalogue's digest.
func ParseOCIPackageManifest(raw []byte) (OCILayer, error) {
	var zero OCILayer
	if len(raw) == 0 || len(raw) > MaxOCIManifestBytes {
		return zero, ErrInvalidOCIPackage
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	if token, err := scan.Token(); err != nil || token != json.Delim('{') || !ociScanObject(scan, 1) {
		return zero, ErrInvalidOCIPackage
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidOCIPackage
	}
	var m struct {
		SchemaVersion int                    `json:"schemaVersion"`
		MediaType     string                 `json:"mediaType"`
		ArtifactType  string                 `json:"artifactType"`
		Config        ociPackageDescriptor   `json:"config"`
		Layers        []ociPackageDescriptor `json:"layers"`
		Annotations   map[string]string      `json:"annotations,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || m.SchemaVersion != 2 || m.MediaType != OCIManifestMediaType || m.ArtifactType != OCIPackageArtifactType {
		return zero, ErrInvalidOCIPackage
	}
	c := m.Config
	if c.MediaType != ociEmptyMediaType || c.Digest != ociEmptyDigest || c.Size != 2 || (c.Data != nil && *c.Data != "e30=") {
		return zero, ErrInvalidOCIPackage
	}
	if len(m.Layers) != 1 {
		return zero, ErrInvalidOCIPackage
	}
	l := m.Layers[0]
	if l.MediaType != OCIPackageLayerMediaType || !ValidOCIDigest(l.Digest) || l.Size <= 0 || l.Size > MaxManagedOfflineEnvelopeBytes || l.Data != nil {
		return zero, ErrInvalidOCIPackage
	}
	return OCILayer{Digest: l.Digest, Size: l.Size}, nil
}

// ociScanObject refuses duplicate keys and deep nesting. OCI keys are camelCase and dotted annotation names, so
// the descriptor key pattern does not apply.
func ociScanObject(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return false
		}
		seen[key] = true
		if !ociScanValue(d, depth+1) {
			return false
		}
	}
	token, err := d.Token()
	return err == nil && token == json.Delim('}')
}

func ociScanValue(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		return ociScanObject(d, depth)
	case json.Delim('['):
		for d.More() {
			if !ociScanValue(d, depth+1) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim(']')
	}
	_, delimiter := token.(json.Delim)
	return !delimiter
}
