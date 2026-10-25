// Package appplatform contains side-effect-free application-platform contracts.
// Parsing a descriptor establishes neither publisher trust nor execution authority.
package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxDescriptorBytes is the provisional parser bound, independent of ZIP limits.
const MaxDescriptorBytes = 128 << 10

var ErrInvalidDescriptor = errors.New("invalid application descriptor")

// ParsedDescriptor binds the decoded declaration to the exact inspected bytes.
// The digest is not a signature verification result.
type ParsedDescriptor struct {
	Manifest Manifest
	SHA256   string
}

type Manifest struct {
	Format        string            `json:"format"`
	ID            string            `json:"id"`
	Version       string            `json:"version"`
	Publisher     string            `json:"publisher"`
	Compatibility Compatibility     `json:"compatibility"`
	Presentation  Presentation      `json:"presentation"`
	Frontend      Artifact          `json:"frontend"`
	Backend       *Backend          `json:"backend,omitempty"`
	Capabilities  Capabilities      `json:"capabilities"`
	Data          DataCompatibility `json:"data"`
}

type Compatibility struct {
	PlatformAPI    string `json:"platform_api"`
	RunnerProtocol string `json:"runner_protocol"`
}

type Presentation struct {
	NameKey        string   `json:"name_key"`
	DescriptionKey string   `json:"description_key"`
	Locales        []string `json:"locales"`
	Routes         []Route  `json:"routes"`
	Entry          string   `json:"entry"`
}

type Route struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Layout string `json:"layout"`
}

type Artifact struct {
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

type Backend struct {
	Kind      string    `json:"kind"`
	Image     string    `json:"image"`
	Platforms []string  `json:"platforms"`
	Protocol  string    `json:"protocol"`
	Resources Resources `json:"resources"`
}

type Resources struct {
	MemoryMiB int `json:"memory_mib"`
	CPUMillis int `json:"cpu_millis"`
	PIDs      int `json:"pids"`
}

type Capabilities struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

type DataCompatibility struct {
	Schema          int   `json:"schema"`
	ReadableSchemas []int `json:"readable_schemas"`
	WritesOnStartup bool  `json:"writes_on_startup"`
}

var (
	slugPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	keyPattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	copyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,95}$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	rangePattern   = regexp.MustCompile(`^[0-9A-Za-z.*+<>=~^| -]{1,96}$`)
	imagePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$`)
)

// ParseDescriptor validates provisional v2 declarations without downloading,
// verifying signatures, comparing compatibility ranges or granting capabilities.
// Those independent admission checks must precede any future execution.
func ParseDescriptor(raw []byte) (ParsedDescriptor, error) {
	if len(raw) == 0 || len(raw) > MaxDescriptorBytes || !utf8.Valid(raw) {
		return ParsedDescriptor{}, ErrInvalidDescriptor
	}
	// encoding/json accepts duplicate and case-insensitive field aliases. Inspect
	// every object before typed decoding so a signed review has one interpretation.
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !scanObject(scan, 1) {
		return ParsedDescriptor{}, ErrInvalidDescriptor
	}
	if _, err := scan.Token(); err != io.EOF {
		return ParsedDescriptor{}, ErrInvalidDescriptor
	}
	var m Manifest
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(&m) != nil || !validManifest(m) {
		return ParsedDescriptor{}, ErrInvalidDescriptor
	}
	sum := sha256.Sum256(raw)
	return ParsedDescriptor{Manifest: m, SHA256: hex.EncodeToString(sum[:])}, nil
}

func scanObject(d *json.Decoder, depth int) bool {
	if depth > 24 {
		return false
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !keyPattern.MatchString(key) || seen[key] {
			return false
		}
		seen[key] = true
		if !scanValue(d, depth+1) {
			return false
		}
	}
	token, err := d.Token()
	return err == nil && token == json.Delim('}')
}

func scanValue(d *json.Decoder, depth int) bool {
	if depth > 24 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		return scanObject(d, depth)
	case json.Delim('['):
		for d.More() {
			if !scanValue(d, depth+1) {
				return false
			}
		}
		token, err = d.Token()
		return err == nil && token == json.Delim(']')
	}
	_, delimiter := token.(json.Delim)
	return !delimiter
}

func validManifest(m Manifest) bool {
	if m.Format != "portal.app/v2" || !slugPattern.MatchString(m.ID) || !slugPattern.MatchString(m.Publisher) || !validVersion(m.Version) {
		return false
	}
	// Range strings are bounded declarations here; the future compatibility
	// evaluator must parse and compare them before an install plan can be admitted.
	if !rangePattern.MatchString(m.Compatibility.PlatformAPI) || !rangePattern.MatchString(m.Compatibility.RunnerProtocol) ||
		strings.TrimSpace(m.Compatibility.PlatformAPI) == "" || strings.TrimSpace(m.Compatibility.RunnerProtocol) == "" {
		return false
	}
	p := m.Presentation
	if !copyKeyPattern.MatchString(p.NameKey) || !copyKeyPattern.MatchString(p.DescriptionKey) || !safeRelativePath(p.Entry) ||
		!uniqueMembers(p.Locales, map[string]bool{"en-US": true, "zh-CN": true, "zh-TW": true}) || len(p.Locales) != 3 || len(p.Routes) == 0 || len(p.Routes) > 32 {
		return false
	}
	routeIDs, routePaths := map[string]bool{}, map[string]bool{}
	for _, route := range p.Routes {
		if !slugPattern.MatchString(route.ID) || !validRoutePath(route.Path) || routeIDs[route.ID] || routePaths[route.Path] ||
			(route.Layout != "document" && route.Layout != "viewport") {
			return false
		}
		routeIDs[route.ID], routePaths[route.Path] = true, true
	}
	if !routePaths["/"] || !safeRelativePath(m.Frontend.Artifact) || !digestPattern.MatchString(m.Frontend.SHA256) || !validCapabilities(m.Capabilities, m.Backend != nil) {
		return false
	}
	if m.Backend != nil && !validBackend(*m.Backend) {
		return false
	}
	if m.Data.Schema < 1 || m.Data.Schema > 1_000_000 || m.Data.WritesOnStartup || len(m.Data.ReadableSchemas) == 0 || len(m.Data.ReadableSchemas) > 32 {
		return false
	}
	schemas := map[int]bool{}
	for _, schema := range m.Data.ReadableSchemas {
		if schema < 1 || schema > 1_000_000 || schemas[schema] {
			return false
		}
		schemas[schema] = true
	}
	return schemas[m.Data.Schema]
}

func safeRelativePath(value string) bool {
	if value == "" || len(value) > 256 || value == "." || strings.HasPrefix(value, "/") || path.Clean(value) != value || strings.Contains(value, "..") {
		return false
	}
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-/", c)) {
			return false
		}
	}
	return true
}

func validRoutePath(value string) bool {
	return value == "/" || (strings.HasPrefix(value, "/") && safeRelativePath(strings.TrimPrefix(value, "/")))
}

func validVersion(value string) bool {
	if len(value) > 128 || !versionPattern.MatchString(value) {
		return false
	}
	withoutBuild, _, _ := strings.Cut(value, "+")
	_, prerelease, hasPrerelease := strings.Cut(withoutBuild, "-")
	if hasPrerelease {
		for _, part := range strings.Split(prerelease, ".") {
			if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

func uniqueMembers(values []string, allowed map[string]bool) bool {
	if len(values) == 0 || len(values) > len(allowed) {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !allowed[value] || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func validBackend(b Backend) bool {
	return b.Kind == "oci" && b.Protocol == "portal.backend/1" && len(b.Image) <= 512 && imagePattern.MatchString(b.Image) &&
		!strings.Contains(b.Image, "..") && !strings.Contains(b.Image, "//") &&
		uniqueMembers(b.Platforms, map[string]bool{"linux/amd64": true, "linux/arm64": true}) &&
		b.Resources.MemoryMiB >= 16 && b.Resources.MemoryMiB <= 4096 && b.Resources.CPUMillis >= 25 && b.Resources.CPUMillis <= 4000 && b.Resources.PIDs >= 16 && b.Resources.PIDs <= 1024
}

func validCapabilities(c Capabilities, hasBackend bool) bool {
	known := map[string]bool{"reports.read": true, "storage.files": true, "tasks.schedule": true, "events.subscribe": true, "notifications.publish": true,
		"network.http": true, "network.stream": true, "secrets.use": true, "secrets.read": true, "background.run": true, "files.export": true}
	seen, required := map[string]bool{}, map[string]bool{}
	for _, value := range c.Required {
		if !known[value] || seen[value] {
			return false
		}
		seen[value], required[value] = true, true
	}
	for _, value := range c.Optional {
		if !known[value] || seen[value] {
			return false
		}
		seen[value] = true
	}
	for _, value := range []string{"tasks.schedule", "events.subscribe", "background.run"} {
		if seen[value] && !hasBackend {
			return false
		}
	}
	for _, value := range []string{"tasks.schedule", "events.subscribe"} {
		if seen[value] && !seen["background.run"] {
			return false
		}
		if required[value] && !required["background.run"] {
			return false
		}
	}
	return true
}
