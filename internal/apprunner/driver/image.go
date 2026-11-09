package driver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

var nativeImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

var (
	// ErrImageMissing is the typed reason a start is refused, before any claim, when the loaded image a descriptor
	// binds is not in the daemon (pruned after its load, or never loaded on this host). It is never repaired here:
	// nothing is pulled.
	ErrImageMissing = errors.New("application image is missing from the runtime (image_missing)")
	// ErrReferenceImageRefused is the production profile's refusal of a registry reference (reference_image_refused).
	ErrReferenceImageRefused = errors.New("registry reference images are refused by the production profile (reference_image_refused)")
)

// ImagePolicy selects the runner profile. The zero value is production: only managed image content is accepted.
// AllowLocalReference is the development profile: a descriptor's registry reference is inspected in the local
// daemon for an image that is already present, exactly as before managed delivery.
type ImagePolicy struct{ AllowLocalReference bool }

// ImageStore is the daemon's image store as detected from its info, which decides what image ID a loaded image has.
type ImageStore string

const (
	ImageStoreClassic    ImageStore = "classic"
	ImageStoreContainerd ImageStore = "containerd"
)

const containerdSnapshotterDriverType = "io.containerd.snapshotter.v1"

// imageStoreFromInfo reads the daemon's driver status: the containerd image store reports a driver-type of
// io.containerd.snapshotter.v1; anything else is the classic store.
func imageStoreFromInfo(info infoResponse) ImageStore {
	for _, pair := range info.DriverStatus {
		if len(pair) == 2 && pair[0] == "driver-type" && pair[1] == containerdSnapshotterDriverType {
			return ImageStoreContainerd
		}
	}
	return ImageStoreClassic
}

// ExpectedImageID is the ID a loaded image has: its config digest in the classic store, and the digest of its
// manifest (the index entry the runner writes) in the containerd store.
func ExpectedImageID(store ImageStore, entry appplatform.BackendImage) string {
	if store == ImageStoreContainerd {
		return "sha256:" + entry.ManifestDigest
	}
	return "sha256:" + entry.ConfigDigest
}

// backendImageEntry returns the descriptor's managed image entry for a platform.
func backendImageEntry(backend *appplatform.Backend, platform string) (appplatform.BackendImage, bool) {
	for _, entry := range backend.Images {
		if entry.Platform == platform {
			return entry, true
		}
	}
	return appplatform.BackendImage{}, false
}

// backendImageReference is the identity a descriptor binds for one platform: its registry reference, or the
// manifest digest of its managed content. Container planning compares it with the inspected image.
func backendImageReference(backend *appplatform.Backend, platform string) (string, bool) {
	if backend.Image != "" {
		return backend.Image, true
	}
	entry, ok := backendImageEntry(backend, platform)
	return "sha256:" + entry.ManifestDigest, ok
}

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
func InspectBackendImage(ctx context.Context, socket string, raw []byte, releaseSHA256 string, policy ImagePolicy) (BackendImageInspection, error) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return BackendImageInspection{}, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: probeTimeout, DialContext: dockerDialer(socket)}
	defer transport.CloseIdleConnections()
	return inspectBackendImageHTTP(ctx, daemonClient(transport), raw, releaseSHA256, runtime.GOARCH, policy)
}

func inspectBackendImageHTTP(ctx context.Context, client *http.Client, raw []byte, releaseSHA256, arch string, policy ImagePolicy) (BackendImageInspection, error) {
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
	if backend.Image == "" {
		return inspectManagedImageHTTP(ctx, client, parsed, platform, arch)
	}
	if !policy.AllowLocalReference {
		return empty, ErrReferenceImageRefused
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

// inspectManagedImageHTTP inspects a loaded image by the ID its descriptor entry implies for the daemon's image
// store. It never needs RepoDigests or a tag: the loaded image is untagged, and the ID is content-derived. A
// missing image is the typed ErrImageMissing, never a pull.
func inspectManagedImageHTTP(ctx context.Context, client *http.Client, parsed appplatform.ParsedDescriptor, platform, arch string) (BackendImageInspection, error) {
	empty := BackendImageInspection{}
	entry, ok := backendImageEntry(parsed.Manifest.Backend, platform)
	if !ok {
		return empty, ErrUnsupported
	}
	var info infoResponse
	if err := readJSON(ctx, client, "/v"+APIVersion+"/info", &info); err != nil {
		return empty, err
	}
	expected := ExpectedImageID(imageStoreFromInfo(info), entry)
	var response imageResponse
	if err := readJSONOrMissing(ctx, client, "/v"+APIVersion+"/images/"+expected+"/json", &response, ErrImageMissing); err != nil {
		return empty, err
	}
	if response.ID != expected || response.OS != "linux" || response.Architecture != arch || (response.Variant != "" && !(arch == "arm64" && response.Variant == "v8")) || !validProjection(response.Config, &imageConfig{}) {
		return empty, ErrInvalidResponse
	}
	var config imageConfig
	if json.Unmarshal(response.Config, &config) != nil || len(config.Volumes) != 0 {
		return empty, ErrInvalidResponse
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	reference, _ := backendImageReference(parsed.Manifest.Backend, platform)
	return BackendImageInspection{ReleaseSHA256: parsed.SHA256, Reference: reference, ImageID: response.ID, Platform: platform}, nil
}
