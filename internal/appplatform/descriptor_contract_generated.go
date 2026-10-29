// Derived from the Portal descriptor schema; edit the source contract instead.
package appplatform

import "regexp"

const descriptorContractSHA256 = "e956d5b4a12ad4752892a1c6a426c8d3ba1f55770536e40eeaabbc4f9de935f8"

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
	NameKey        string      `json:"name_key"`
	DescriptionKey string      `json:"description_key"`
	Locales        []string    `json:"locales"`
	Routes         []Route     `json:"routes"`
	Entry          string      `json:"entry"`
	Display        *AppDisplay `json:"display,omitempty"`
}
type Route struct {
	ID     string       `json:"id"`
	Path   string       `json:"path"`
	Layout string       `json:"layout"`
	Query  []RouteQuery `json:"query,omitempty"`
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
type AppDisplay struct {
	Icon  string           `json:"icon"`
	Texts []AppDisplayText `json:"texts"`
}
type AppDisplayText struct {
	Locale      string `json:"locale"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type RouteQuery struct {
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	MaxBytes *int     `json:"max_bytes,omitempty"`
	Min      *int     `json:"min,omitempty"`
	Max      *int     `json:"max,omitempty"`
	Values   []string `json:"values,omitempty"`
}

const MaxDescriptorBytes = 131072
const MaxAppNameCharacters = 80
const MaxAppDescriptionCharacters = 320
const maxDescriptorDepth = 24
const maxRouteQueryFields = 16
const maxRouteQueryTextBytes = 256
const maxRouteQueryInteger = 1000000000
const maxRouteQueryValues = 16
const maxDescriptorRoutes = 32
const maxDescriptorReadableSchemas = 32
const minDescriptorDataSchema = 1
const maxDescriptorDataSchema = 1000000
const maxDescriptorPathBytes = 256
const maxBackendImageBytes = 512
const maxDescriptorVersionBytes = 128
const minBackendMemoryMiB = 16
const maxBackendMemoryMiB = 4096
const minBackendCPUMillis = 25
const maxBackendCPUMillis = 4000
const minBackendPIDs = 16
const maxBackendPIDs = 1024
const descriptorFormat = "portal.app/v2"
const descriptorBackendKind = "oci"
const descriptorBackendProtocol = "portal.backend/1"

var queryNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
var queryEnumPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var copyKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,95}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
var rangePattern = regexp.MustCompile(`^[0-9A-Za-z.*+<>=~^| -]{1,96}$`)
var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(:[0-9]{1,5})?/[a-z0-9][a-z0-9._/-]*@sha256:[a-f0-9]{64}$`)
var pathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var descriptorKnownCapabilities = map[string]bool{"reports.read": true, "storage.files": true, "tasks.schedule": true, "events.subscribe": true, "notifications.publish": true, "network.http": true, "network.stream": true, "secrets.use": true, "secrets.read": true, "background.run": true, "files.export": true}
var descriptorLocaleNames = map[string]bool{"en-US": true, "zh-CN": true, "zh-TW": true}
var descriptorDisplayIcons = map[string]bool{"appstore": true, "file-text": true, "dashboard": true, "calendar": true, "bar-chart": true, "cloud": true, "tool": true, "experiment": true}
var descriptorPlatformNames = map[string]bool{"linux/amd64": true, "linux/arm64": true}
var descriptorLayoutNames = map[string]bool{"document": true, "viewport": true}
