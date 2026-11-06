package driver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var nativeImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// BackendImageInspection is a read-only local daemon observation. It proves
// neither publisher trust, runtime enforcement, writer admission nor permission
// to create or start a container. Native ImageID pins the later create request
// to the inspected local configuration rather than resolving a name again.
type BackendImageInspection struct {
	ReleaseSHA256 string `json:"release_sha256"`
	Reference     string `json:"reference"`
	ImageID       string `json:"image_id"`
	Platform      string `json:"platform"`
}

type imageResponse struct {
	ID           string          `json:"Id"`
	RepoDigests  []string        `json:"RepoDigests"`
	OS           string          `json:"Os"`
	Architecture string          `json:"Architecture"`
	Variant      string          `json:"Variant"`
	Config       json.RawMessage `json:"Config"`
}
type imageConfig struct {
	Volumes map[string]json.RawMessage `json:"Volumes"`
}

func matchesImageReference(observed, reviewed string) bool {
	if observed == reviewed {
		return true
	}
	// Docker reports Docker Hub repositories in familiar form (for example
	// busybox@sha256:... instead of docker.io/library/busybox@sha256:...).
	// Only strip these exact default-registry prefixes, never another host,
	// repository or digest. The later create still uses the native image ID.
	if strings.HasPrefix(reviewed, "docker.io/") && observed == strings.TrimPrefix(reviewed, "docker.io/") {
		return true
	}
	return strings.HasPrefix(reviewed, "docker.io/library/") && observed == strings.TrimPrefix(reviewed, "docker.io/library/")
}

// InspectBackendImage inspects an already-present digest reference through the
// configured Linux Docker Unix socket. It never pulls an image, resolves a tag,
// accesses a registry or starts a process. The descriptor digest must be the
// exact independently reviewed release; signature/actor admission remains the
// controller's separate responsibility.
func InspectBackendImage(ctx context.Context, socket string, raw []byte, releaseSHA256 string) (BackendImageInspection, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return BackendImageInspection{}, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: probeTimeout, DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	return inspectBackendImageHTTP(ctx, daemonClient(transport), raw, releaseSHA256, runtime.GOARCH)
}

func inspectBackendImageHTTP(ctx context.Context, client *http.Client, raw []byte, releaseSHA256, arch string) (BackendImageInspection, error) {
	empty := BackendImageInspection{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	parsed, err := appplatform.ParseDescriptor(raw)
	if err != nil || parsed.SHA256 != releaseSHA256 || parsed.Manifest.Backend == nil {
		return empty, ErrInvalidResponse
	}
	backend := parsed.Manifest.Backend
	if arch != "amd64" && arch != "arm64" {
		return empty, ErrUnsupported
	}
	platform := "linux/" + arch
	declared := false
	for _, p := range backend.Platforms {
		if p == platform {
			declared = true
		}
	}
	if !declared {
		return empty, ErrUnsupported
	}
	var response imageResponse
	if err := readJSON(ctx, client, "/v"+APIVersion+"/images/"+url.PathEscape(backend.Image)+"/json", &response); err != nil {
		return empty, err
	}
	// Unlike daemon-info aliases, OCI architecture names must be canonical. A
	// mismatched native image or unadmitted variant cannot silently use emulation.
	if !nativeImageID.MatchString(response.ID) || response.OS != "linux" || response.Architecture != arch || (response.Variant != "" && !(arch == "arm64" && response.Variant == "v8")) || len(response.RepoDigests) == 0 || len(response.RepoDigests) > 256 || !validProjection(response.Config, &imageConfig{}) {
		return empty, ErrInvalidResponse
	}
	matched := false
	seen := map[string]bool{}
	for _, digest := range response.RepoDigests {
		if len(digest) > 512 || strings.ContainsAny(digest, "\x00\r\n") || seen[digest] {
			return empty, ErrInvalidResponse
		}
		seen[digest] = true
		if matchesImageReference(digest, backend.Image) {
			matched = true
		}
	}
	var config imageConfig
	// Docker creates image-declared anonymous volumes even with an otherwise
	// constrained create request. Refuse them instead of inventing an unreviewed
	// storage mount or pretending an empty Volumes override removes inheritance.
	if !matched || json.Unmarshal(response.Config, &config) != nil || len(config.Volumes) != 0 {
		return empty, ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	return BackendImageInspection{ReleaseSHA256: parsed.SHA256, Reference: backend.Image, ImageID: response.ID, Platform: platform}, nil
}
