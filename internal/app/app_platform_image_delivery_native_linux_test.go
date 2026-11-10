//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/service"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Real-runner acceptance of managed image delivery (doc 170, PR 3c): the exact Scheduled Digest image, installed
// from a managed package, is loaded by a real root runner from the Portal's blobs and started, with the runner in
// the production profile (no local reference images) and the image absent from the Docker store before the load.
//
// The parent runs as root, owns the Docker store and the runner; the child is the Portal as an unprivileged user,
// which stages the package through the offline route and drives the ordinary install worker. They meet at two
// files in the child's directory: the child writes imageDeliveryReadyFile when the app is installed and has
// answered, the parent inspects the running container and writes imageDeliveryDoneFile.
const (
	imageDeliveryReadyFile = "ready-for-inspection"
	imageDeliveryDoneFile  = "inspection-done"
	imageDeliveryFormat    = "portal.managed-image-delivery.real-runner/1-dev.1"
)

type imageDeliveryExpected struct {
	ReleaseSHA256  string   `json:"release_sha256"`
	ManifestDigest string   `json:"manifest_digest"`
	ConfigDigest   string   `json:"config_digest"`
	Platform       string   `json:"platform"`
	ImageID        string   `json:"expected_image_id"`
	Store          string   `json:"store"`
	Layers         []string `json:"layers"`
	Blobs          int      `json:"blobs"`
	Publisher      string   `json:"publisher"`
}

type imageDeliveryChildEvidence struct {
	OperationID    string          `json:"operation_id"`
	InstallationID string          `json:"installation_id"`
	StageHTTP      int             `json:"offline_stage_http_status"`
	LoadEffect     json.RawMessage `json:"load_effect"`
	LoadEffectRows int             `json:"load_effect_rows"`
	BlobSends      int             `json:"blob_sends"`
	BlobsStored    int             `json:"blobs_stored"`
	LoadExecutes   int             `json:"load_executes"`
	Receipt        json.RawMessage `json:"runner_receipt"`
	ReceiptState   string          `json:"runner_receipt_state"`
	StartNativeID  string          `json:"start_receipt_native_id"`
	ReadinessSeen  bool            `json:"readiness_evidence_recorded"`
	AppAnswered    bool            `json:"app_answered"`
	Uninstalled    bool            `json:"uninstalled"`
}

// --- a minimal Docker API client; the parent is root and owns the daemon of this disposable worker ---

type imageDeliveryDocker struct {
	client *http.Client
}

func newImageDeliveryDocker(socket string) *imageDeliveryDocker {
	return &imageDeliveryDocker{client: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}}
}

func (d *imageDeliveryDocker) call(ctx context.Context, method, path string, out any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, nil)
	if err != nil {
		return 0, err
	}
	response, err := d.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if out != nil && response.StatusCode/100 == 2 {
		if err := json.Unmarshal(body, out); err != nil {
			return response.StatusCode, fmt.Errorf("%s %s: %w", method, path, err)
		}
	}
	return response.StatusCode, nil
}

func (d *imageDeliveryDocker) containerdStore(ctx context.Context) (bool, error) {
	var info struct {
		DriverStatus [][]string
	}
	if status, err := d.call(ctx, http.MethodGet, "/info", &info); err != nil || status != 200 {
		return false, fmt.Errorf("docker info: %d %v", status, err)
	}
	for _, pair := range info.DriverStatus {
		if len(pair) == 2 && pair[0] == "driver-type" && pair[1] == "io.containerd.snapshotter.v1" {
			return true, nil
		}
	}
	return false, nil
}

func (d *imageDeliveryDocker) images(ctx context.Context) ([]imageDeliveryImage, error) {
	var raw []struct {
		ID          string `json:"Id"`
		RepoTags    []string
		RepoDigests []string
	}
	if status, err := d.call(ctx, http.MethodGet, "/images/json?all=1", &raw); err != nil || status != 200 {
		return nil, fmt.Errorf("list images: %d %v", status, err)
	}
	out := make([]imageDeliveryImage, 0, len(raw))
	for _, image := range raw {
		out = append(out, imageDeliveryImage{ID: image.ID, RepoTags: image.RepoTags, RepoDigests: image.RepoDigests})
	}
	return out, nil
}

func (d *imageDeliveryDocker) containers(ctx context.Context) ([]imageDeliveryContainer, error) {
	var raw []struct {
		ID      string `json:"Id"`
		ImageID string
		State   string
	}
	if status, err := d.call(ctx, http.MethodGet, "/containers/json?all=1", &raw); err != nil || status != 200 {
		return nil, fmt.Errorf("list containers: %d %v", status, err)
	}
	out := make([]imageDeliveryContainer, 0, len(raw))
	for _, c := range raw {
		out = append(out, imageDeliveryContainer{ID: c.ID, ImageID: c.ImageID, State: c.State})
	}
	return out, nil
}

// containersOf lists the containers created from one image ID.
func (d *imageDeliveryDocker) containersOf(ctx context.Context, imageID string) ([]imageDeliveryContainer, error) {
	all, err := d.containers(ctx)
	if err != nil {
		return nil, err
	}
	var out []imageDeliveryContainer
	for _, c := range all {
		if c.ImageID == imageID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (d *imageDeliveryDocker) inspectImage(ctx context.Context, id string) (imageDeliveryInspect, int, error) {
	var raw struct {
		ID           string `json:"Id"`
		OS           string `json:"Os"`
		Architecture string
		RepoTags     []string
		RepoDigests  []string
		RootFS       struct{ Layers []string }
	}
	status, err := d.call(ctx, http.MethodGet, "/images/"+url.PathEscape(id)+"/json", &raw)
	return imageDeliveryInspect{ID: raw.ID, OS: raw.OS, Architecture: raw.Architecture, RepoTags: raw.RepoTags,
		RepoDigests: raw.RepoDigests, Layers: raw.RootFS.Layers}, status, err
}

type imageDeliveryContainerDetail struct {
	ID, Image, ConfigImage string
	Running                bool
	Labels                 map[string]string
}

// containerDetail is what the daemon recorded for a container: the image ID it was created from and its labels.
func (d *imageDeliveryDocker) containerDetail(ctx context.Context, id string) (imageDeliveryContainerDetail, error) {
	var raw struct {
		ID     string `json:"Id"`
		Image  string
		Config struct {
			Image  string
			Labels map[string]string
		}
		State struct{ Running bool }
	}
	status, err := d.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", &raw)
	if err != nil || status != 200 {
		return imageDeliveryContainerDetail{}, fmt.Errorf("inspect container %s: %d %v", id, status, err)
	}
	return imageDeliveryContainerDetail{ID: raw.ID, Image: raw.Image, ConfigImage: raw.Config.Image, Running: raw.State.Running, Labels: raw.Config.Labels}, nil
}

// removeTarget removes the containers created from the target image and then every name and copy of the image,
// by ID. It returns what it removed. Only an image the job built (the package's own image) is passed in.
func (d *imageDeliveryDocker) removeTarget(ctx context.Context, id string) (containers int, removedImage bool, err error) {
	owned, err := d.containersOf(ctx, id)
	if err != nil {
		return 0, false, err
	}
	for _, c := range owned {
		if status, err := d.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(c.ID)+"?force=1&v=1", nil); err != nil || status/100 != 2 {
			return containers, false, fmt.Errorf("remove container %s: %d %v", c.ID, status, err)
		}
		containers++
	}
	status, err := d.call(ctx, http.MethodDelete, "/images/"+url.PathEscape(id)+"?force=1", nil)
	if err != nil {
		return containers, false, err
	}
	switch status {
	case 200:
		return containers, true, nil
	case 404:
		return containers, false, nil
	}
	return containers, false, fmt.Errorf("remove image %s: status %d", id, status)
}

// imageDeliveryWatcher collects image and container events from the moment it is created.
type imageDeliveryWatcher struct {
	mu     sync.Mutex
	events []imageDeliveryEvent
	cancel context.CancelFunc
	done   chan struct{}
}

func (d *imageDeliveryDocker) watch(ctx context.Context) (*imageDeliveryWatcher, error) {
	ctx, cancel := context.WithCancel(ctx)
	filters := url.QueryEscape(`{"type":["image","container"]}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/events?filters="+filters, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	response, err := d.client.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	if response.StatusCode != 200 {
		response.Body.Close()
		cancel()
		return nil, fmt.Errorf("events: status %d", response.StatusCode)
	}
	w := &imageDeliveryWatcher{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		defer response.Body.Close()
		decoder := json.NewDecoder(response.Body)
		for {
			var raw struct {
				Type, Action string
				Actor        struct {
					ID         string
					Attributes map[string]string
				}
			}
			if decoder.Decode(&raw) != nil {
				return
			}
			w.mu.Lock()
			w.events = append(w.events, imageDeliveryEvent{Type: raw.Type, Action: raw.Action, ID: raw.Actor.ID, Image: raw.Actor.Attributes["image"]})
			w.mu.Unlock()
		}
	}()
	return w, nil
}

func (w *imageDeliveryWatcher) stop() []imageDeliveryEvent {
	// Events are delivered asynchronously; let the last ones arrive before the stream is closed.
	time.Sleep(500 * time.Millisecond)
	w.cancel()
	<-w.done
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]imageDeliveryEvent(nil), w.events...)
}

// --- the parent ---

func TestPlatformManagedImageDeliveryAcceptance(t *testing.T) {
	if os.Getenv("APP_IMAGE_DELIVERY_CANDIDATE") == "" {
		t.Skip("explicit managed image delivery acceptance on a real runner")
	}
	fixture, socket := os.Getenv("APP_DATA_PORTAL_FIXTURE"), os.Getenv("APP_CANDIDATE_DOCKER_SOCKET")
	if os.Geteuid() != 0 || os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || !filepath.IsAbs(fixture) || !filepath.IsAbs(socket) {
		t.Fatal("requires disposable Linux root and the owned Docker socket")
	}
	ctx := context.Background()
	parts := realImageParts(t)
	entry := parts.entry
	docker := newImageDeliveryDocker(socket)

	// BEFORE the load: no image with the package's ID exists in the Docker store. The job built this very image
	// (and pushed a copy to its disposable registry), so remove it, by ID, together with the containers made
	// from it. Nothing else is touched.
	containerd, err := docker.containerdStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	storeName := "classic"
	if containerd {
		storeName = "containerd"
	}
	target := imageDeliveryExpectedID(containerd, entry.ManifestDigest, entry.ConfigDigest)
	// "Exact" must not depend on a cache hit: the package was saved from the very image the job built and the
	// exact-image step verified, so its ID must be that image's ID.
	builtImageID := os.Getenv("APP_IMAGE_DELIVERY_BUILT_IMAGE_ID")
	if builtImageID == "" || builtImageID != target {
		t.Fatalf("the delivered image %s is not the image the job built and verified (%q)", target, builtImageID)
	}
	removedContainers, removedImage, err := docker.removeTarget(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	before, err := docker.images(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := imageDeliveryCheckAbsent(before, target); err != nil {
		t.Fatal(err)
	}
	if _, status, err := docker.inspectImage(ctx, target); err != nil || status != http.StatusNotFound {
		t.Fatalf("the package's image is still inspectable before the load: %d %v", status, err)
	}
	t.Logf("IMAGE_DELIVERY_REAL_RUNNER_IMAGE_ABSENT_BEFORE_LOAD id=%s removed_image=%v removed_containers=%d images=%d",
		target, removedImage, removedContainers, len(before))

	layers := make([]string, 0, len(entry.Layers))
	for _, layer := range entry.Layers {
		layers = append(layers, imageDeliveryDaemonID(layer.DiffID))
	}
	expected := imageDeliveryExpected{
		ReleaseSHA256: platformDigest(parts.descriptor), ManifestDigest: entry.ManifestDigest, ConfigDigest: entry.ConfigDigest,
		Platform: entry.Platform, ImageID: target, Store: storeName, Layers: layers, Blobs: len(parts.imageBlobList()), Publisher: parts.trustPublisher(),
	}

	store, err := journal.Open(os.Getenv("APP_DATA_JOURNAL_CONTROL"))
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := store.ControllerDeployment(ctx)
	if closeErr := store.Close(); err != nil || closeErr != nil {
		t.Fatal("read the enrolled deployment", err, closeErr)
	}

	dir, err := os.MkdirTemp(fixture, "image-delivery-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dir, "portal.test")
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, binary, 0555); err != nil {
		t.Fatal(err)
	}
	childData := filepath.Join(dir, "child")
	if err := os.Mkdir(childData, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(childData, 41001, 41001); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"package.portalapp": parts.packageBytes(t, nil, ""), "expected.json": imageDeliveryJSON(t, expected)} {
		if err := os.WriteFile(filepath.Join(childData, name), body, 0444); err != nil {
			t.Fatal(err)
		}
	}

	// The production profile: no local reference images, a real image root (the fixture creates it).
	allowLocalImages := true
	runner := startPlatformServiceFixture(t, binaryPath, dir, deployment, func(c *service.Config) {
		c.AllowLocalReferenceImages = false
		allowLocalImages = c.AllowLocalReferenceImages
	})
	defer func() {
		runner.stop(t)
		removePlatformServiceFixtureRoot(t, runner.root)
	}()

	watcher, err := docker.watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var events []imageDeliveryEvent
	stopWatcher := func() {
		if watcher != nil {
			events = watcher.stop()
			watcher = nil
		}
	}
	defer stopWatcher()

	enrollment, _ := json.Marshal(deployment.ControllerEnrollment)
	child := exec.Command(binaryPath, "-test.run=^TestPlatformManagedImageDeliveryChild$", "-test.v", "-test.timeout=500s")
	child.Dir = dir
	child.Env = []string{
		"GOMAXPROCS=2", "PORTAL_IMAGE_DELIVERY_CHILD=1", "PORTAL_IMAGE_DELIVERY_DIR=" + childData,
		"PORTAL_DATA_SOCKET=" + runner.socket,
		"PORTAL_DATA_ENROLLMENT=" + base64.RawStdEncoding.EncodeToString(enrollment),
		"TMPDIR=" + childData, "HOME=" + childData,
	}
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 41001, Gid: 41001, Groups: []uint32{41001}}}
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	evidenceWritten := false
	defer func() {
		// A failed run still leaves a record: what the scenario had established and the child's output tail.
		if path := os.Getenv("APP_IMAGE_DELIVERY_EVIDENCE"); t.Failed() && !evidenceWritten && path != "" {
			tail := output.String()
			if len(tail) > 16384 {
				tail = tail[len(tail)-16384:]
			}
			failure := map[string]any{"format": imageDeliveryFormat, "passed": false, "expected_image_id": target, "child_output_tail": tail}
			if raw, err := json.MarshalIndent(failure, "", "  "); err == nil {
				_ = os.WriteFile(path, append(raw, '\n'), 0644)
			}
		}
	}()
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			<-waited
		}
		if t.Failed() {
			t.Logf("Portal child output:\n%s", output.String())
		}
	}()

	// Wait for the child to report the installed, answering app, inspect the daemon, and release the child.
	var inspection imageDeliveryInspection
	var inspectErr error
	var waitErr error
	inspected := false
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
wait:
	for {
		select {
		case waitErr = <-waited:
			break wait
		case <-tick.C:
			if inspected {
				continue
			}
			if _, err := os.Stat(filepath.Join(childData, imageDeliveryReadyFile)); err != nil {
				continue
			}
			inspected = true
			var reported imageDeliveryChildEvidence
			if raw, err := os.ReadFile(filepath.Join(childData, "child-evidence.json")); err != nil || json.Unmarshal(raw, &reported) != nil {
				inspectErr = fmt.Errorf("child evidence unavailable: %v", err)
			} else {
				inspection, inspectErr = inspectImageDeliveryRun(ctx, docker, expected, before, reported)
			}
			if err := os.WriteFile(filepath.Join(childData, imageDeliveryDoneFile), []byte("done\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if waitErr != nil {
		t.Fatalf("Portal child failed: %v\n%s", waitErr, output.String())
	}
	if !inspected {
		t.Fatalf("the child ended without reporting a running app\n%s", output.String())
	}
	if inspectErr != nil {
		t.Fatal(inspectErr)
	}
	for _, marker := range []string{"IMAGE_DELIVERY_CHILD_STAGED_OFFLINE", "IMAGE_DELIVERY_CHILD_LOAD_RECEIPT", "IMAGE_DELIVERY_CHILD_APP_ANSWERED", "IMAGE_DELIVERY_CHILD_UNINSTALLED"} {
		if !bytes.Contains(output.Bytes(), []byte(marker)) {
			t.Fatal("child assertion missing: "+marker, output.String())
		}
		t.Log(marker)
	}
	stopWatcher()
	if err := imageDeliveryCheckEvents(events, target); err != nil {
		t.Fatal(err)
	}
	// What the runner announced about itself is an observation, not an assumption: stop it (the child has
	// finished) and read its captured output.
	runner.stop(t)
	runnerProfile, runnerPlatform, runnerImages, err := imageDeliveryParseRunnerReady(runner.output.String())
	if err != nil {
		t.Fatal(err)
	}
	if runnerImages != "production" || allowLocalImages || runnerPlatform != entry.Platform {
		t.Fatalf("runner profile %s platform %s images %s (allow local reference images: %v), expected production on %s", runnerProfile, runnerPlatform, runnerImages, allowLocalImages, entry.Platform)
	}
	// The uninstall stopped and removed the container; leave no container of this image behind.
	left, err := docker.containersOf(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		if _, _, err := docker.removeTarget(ctx, target); err != nil {
			t.Log(err)
		}
		t.Errorf("%d container(s) of the delivered image remained after the uninstall", len(left))
	}

	var childEvidence imageDeliveryChildEvidence
	raw, err := os.ReadFile(filepath.Join(childData, "child-evidence.json"))
	if err != nil || json.Unmarshal(raw, &childEvidence) != nil {
		t.Fatal("child evidence unavailable", err)
	}
	pulls, loads, creates := 0, 0, 0
	var imageEvents []imageDeliveryEvent
	for _, e := range events {
		switch {
		case e.Type == "image":
			imageEvents = append(imageEvents, e)
			if e.Action == "pull" {
				pulls++
			}
			if e.Action == "load" {
				loads++
			}
		case e.Type == "container" && e.Action == "create":
			creates++
		}
	}
	var runnerReceipt journal.LoadImageReceipt
	receiptBinds := json.Unmarshal(childEvidence.Receipt, &runnerReceipt) == nil &&
		strings.TrimPrefix(runnerReceipt.Claim.ManifestDigest, "sha256:") == strings.TrimPrefix(entry.ManifestDigest, "sha256:") &&
		strings.TrimPrefix(runnerReceipt.Claim.ConfigDigest, "sha256:") == strings.TrimPrefix(entry.ConfigDigest, "sha256:") &&
		runnerReceipt.ImageID == target
	report := map[string]any{
		"format":                       imageDeliveryFormat,
		"image_absent_before_load":     imageDeliveryCheckAbsent(before, target) == nil,
		"production_profile":           runnerImages == "production",
		"allow_local_reference_images": allowLocalImages,
		"runner": map[string]any{
			"profile": runnerProfile, "platform": runnerPlatform, "images": runnerImages,
		},
		"built_image_id":                 builtImageID,
		"delivered_image_id":             target,
		"built_image_is_delivered_image": builtImageID == target,
		"publisher":                      expected.Publisher,
		"staged_descriptor_change":       "compatibility.runner_protocol only: >=1.0.0 <2.0.0 to >=1.0.0-0 <2.0.0",
		"image_store":                    storeName,
		"platform":                       entry.Platform,
		"release_sha256":                 expected.ReleaseSHA256,
		"manifest_digest":                entry.ManifestDigest,
		"config_digest":                  entry.ConfigDigest,
		"expected_image_id":              target,
		"layer_diff_ids":                 layers,
		"before_load": map[string]any{
			"removed_image": removedImage, "removed_containers": removedContainers, "images_in_store": len(before),
		},
		"portal": childEvidence,
		"daemon": map[string]any{
			"stored_image":  inspection.Image,
			"containers":    inspection.Containers,
			"image_events":  imageEvents,
			"load_events":   loads,
			"pull_events":   pulls,
			"create_events": creates,
		},
		"blobs_uploaded_over_control_socket":          childEvidence.BlobsStored == expected.Blobs,
		"load_receipt_loaded":                         childEvidence.ReceiptState == "loaded",
		"runner_started_container_uses_package_image": inspection.ContainersUsePackageImage,
		"container_tied_to_start_receipt":             inspection.ContainerTiedToStartReceipt,
		"manifest_digest_bound_by_receipt":            receiptBinds,
		"layers_match":                                slices.Equal(inspection.Image.Layers, expected.Layers),
		"readiness_reached":                           childEvidence.ReadinessSeen,
		"app_answered":                                childEvidence.AppAnswered,
		"no_pull_events":                              pulls == 0,
		"no_names_added":                              len(inspection.Image.RepoTags) == 0 && len(inspection.Image.RepoDigests) == 0,
		"not_proved": []string{
			"the manifest digest is not retained by the classic store, so it is bound through the runner's signed claim and receipt, not read back from the daemon",
			"the PKG-08 corrupt-blob scenario is proved by Portal-side tests only, not against a real runner",
		},
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("APP_IMAGE_DELIVERY_EVIDENCE"); path != "" {
		if err := os.WriteFile(path, append(encoded, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
		evidenceWritten = true
	}
	t.Log("IMAGE_DELIVERY_REAL_RUNNER_LOADED_FROM_BLOBS")
	t.Log("IMAGE_DELIVERY_REAL_RUNNER_CONTAINER_IMAGE_IS_PACKAGE_IMAGE")
	t.Log("IMAGE_DELIVERY_REAL_RUNNER_NO_PULL_NO_NAMES")
	t.Log("IMAGE_DELIVERY_REAL_RUNNER_APP_ANSWERED")
}

func imageDeliveryJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type imageDeliveryInspection struct {
	Image                       imageDeliveryInspect
	Containers                  []map[string]any
	ContainersUsePackageImage   bool
	ContainerTiedToStartReceipt bool
}

// inspectImageDeliveryRun is the daemon-side proof, taken while the app runs: the stored image is the package's
// (ID, platform, layer chain, no name), the store changed by exactly that image, every container created from it
// was created from the image ID with one running, and that container is the one the runner's start receipt named,
// carrying this installation, operation and release as its owner labels.
func inspectImageDeliveryRun(ctx context.Context, docker *imageDeliveryDocker, expected imageDeliveryExpected, before []imageDeliveryImage, child imageDeliveryChildEvidence) (imageDeliveryInspection, error) {
	var out imageDeliveryInspection
	stored, status, err := docker.inspectImage(ctx, expected.ImageID)
	if err != nil || status != 200 {
		return out, fmt.Errorf("inspect the loaded image: %d %v", status, err)
	}
	out.Image = stored
	if err := imageDeliveryCheckInspect(stored, expected.ImageID, expected.Platform, expected.Layers); err != nil {
		return out, err
	}
	after, err := docker.images(ctx)
	if err != nil {
		return out, err
	}
	if err := imageDeliveryCheckAfterLoad(before, after, expected.ImageID); err != nil {
		return out, err
	}
	containers, err := docker.containersOf(ctx, expected.ImageID)
	if err != nil {
		return out, err
	}
	if err := imageDeliveryCheckContainers(containers, expected.ImageID); err != nil {
		return out, err
	}
	var ownerErr error
	for _, c := range containers {
		detail, err := docker.containerDetail(ctx, c.ID)
		if err != nil {
			return out, err
		}
		if detail.Image != expected.ImageID {
			return out, fmt.Errorf("container %s was created from %s, not the package's image %s", c.ID, detail.Image, expected.ImageID)
		}
		out.Containers = append(out.Containers, map[string]any{"id": detail.ID, "image_id": detail.Image, "configured_image": detail.ConfigImage, "running": detail.Running, "labels": detail.Labels})
		if detail.ID == child.StartNativeID {
			ownerErr = imageDeliveryCheckContainerOwner(detail.ID, detail.Labels, child.StartNativeID, child.InstallationID, child.OperationID, expected.ReleaseSHA256)
			out.ContainerTiedToStartReceipt = ownerErr == nil && detail.Running
		}
	}
	out.ContainersUsePackageImage = true
	if !out.ContainerTiedToStartReceipt {
		return out, fmt.Errorf("no running container is the one the runner's start receipt named (%q): %v", child.StartNativeID, ownerErr)
	}
	return out, nil
}

// --- the Portal child ---

// imageDeliveryRunner records what crosses the real control socket and delegates every call to it.
type imageDeliveryRunner struct {
	inner                platformImageRunner
	mu                   sync.Mutex
	sends, stored, loads int
	receipt              *journal.LoadImageReceipt
}

func (r *imageDeliveryRunner) SendBlob(ctx context.Context, endpoint platformRuntimeEndpoint, call platformImageBlobCall) (transport.ImageBlobResult, error) {
	result, err := r.inner.SendBlob(ctx, endpoint, call)
	r.mu.Lock()
	r.sends++
	if err == nil && result.Stored {
		r.stored++
	}
	r.mu.Unlock()
	return result, err
}

func (r *imageDeliveryRunner) note(reply platformImageReply) {
	if reply == nil {
		return
	}
	if receipt, err := reply.LoadImageReceipt(); err == nil {
		r.mu.Lock()
		r.receipt = &receipt
		r.mu.Unlock()
	}
}

func (r *imageDeliveryRunner) LoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	reply, err := r.inner.LoadImage(ctx, endpoint, call)
	r.mu.Lock()
	r.loads++
	r.mu.Unlock()
	if err == nil {
		r.note(reply)
	}
	return reply, err
}

func (r *imageDeliveryRunner) ReadLoadImage(ctx context.Context, endpoint platformRuntimeEndpoint, call platformLoadImageCall) (platformImageReply, error) {
	reply, err := r.inner.ReadLoadImage(ctx, endpoint, call)
	if err == nil {
		r.note(reply)
	}
	return reply, err
}

func TestPlatformManagedImageDeliveryChild(t *testing.T) {
	if os.Getenv("PORTAL_IMAGE_DELIVERY_CHILD") != "1" {
		t.Skip("isolated Portal child of the managed image delivery acceptance only")
	}
	if os.Geteuid() != 41001 {
		t.Fatal("Portal child did not drop root")
	}
	ctx := context.Background()
	dir := os.Getenv("PORTAL_IMAGE_DELIVERY_DIR")
	var expected imageDeliveryExpected
	rawExpected, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil || json.Unmarshal(rawExpected, &expected) != nil {
		t.Fatal("expected image facts unavailable", err)
	}
	pkg, err := os.ReadFile(filepath.Join(dir, "package.portalapp"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := base64.RawStdEncoding.DecodeString(os.Getenv("PORTAL_DATA_ENROLLMENT"))
	if err != nil {
		t.Fatal(err)
	}
	var enrollment journal.ControllerEnrollment
	if json.Unmarshal(encoded, &enrollment) != nil {
		t.Fatal("missing public enrollment")
	}

	s, session := platformTrustTestServer(t, false)
	platformRegisterTestPublisherAs(t, s, session, expected.Publisher)
	s.platformImageDir = t.TempDir()
	bindPlatformFixtureRuntime(t, s, session, enrollment)
	endpoint := platformRuntimeEndpoint{path: os.Getenv("PORTAL_DATA_SOCKET"), controllerUID: 41001, controllerGID: 41001}
	s.platformBackendEndpoint.Store(&endpoint)
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		t.Fatal("authenticated runtime profile", err)
	}
	if profile := facts.observation.Profile(); profile.Platform != expected.Platform {
		t.Fatalf("runner platform %q is not the package's %q", profile.Platform, expected.Platform)
	}
	counting := &imageDeliveryRunner{inner: platformImageTransport{}}
	s.platformImageRunner = counting
	evidence := imageDeliveryChildEvidence{}

	// The real offline import route, as an administrator reaches it.
	mux := http.NewServeMux()
	s.wireRoutes(mux)
	cookie := s.managedCookie(*s.st.GetUser(session.username), session.sessionID, session.expires.Unix())
	response := platformOfflineRequest(mux, cookie, "application/zip", pkg)
	evidence.StageHTTP = response.Code
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected.ReleaseSHA256) || !strings.Contains(response.Body.String(), `"execution":"managed"`) {
		t.Fatal("offline import:", response.Code, response.Body.String())
	}
	t.Log("IMAGE_DELIVERY_CHILD_STAGED_OFFLINE")
	trust := scalar[int64](t, s.st, `SELECT trust_revision FROM app_platform_releases WHERE sha256=?`, expected.ReleaseSHA256)

	// Review, consumption and the ordinary install worker: stages through load_image, start, probe, registration
	// and activation, with no synthetic native observation and no direct runner journal write.
	review, err := s.createPlatformWritableInstallReview(ctx, session, expected.ReleaseSHA256, trust,
		[]platformAudienceTarget{{kind: "user", username: session.username}}, endpoint)
	if err != nil {
		t.Fatal("managed review", err)
	}
	committed, err := s.consumePlatformWritableInstallReview(ctx, session, review.ID, review.SHA256, platformFixtureID(901), endpoint)
	if err != nil {
		t.Fatal("managed admission", err)
	}
	evidence.OperationID, evidence.InstallationID = committed.OperationID, committed.InstallationID
	worker, err := newPlatformWritableInstallScheduler(s, endpoint, platformFixtureID(902), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	installCtx, stopInstall := context.WithTimeout(ctx, 330*time.Second)
	defer stopInstall()
	deadline := time.Now().Add(340 * time.Second)
	for {
		tickErr := worker.tick(installCtx)
		stage := scalar[string](t, s.st, `SELECT stage FROM app_platform_operations WHERE id=?`, committed.OperationID)
		if stage == "complete" {
			break
		}
		status := worker.currentStatus()
		if tickErr != nil {
			t.Logf("install tick: stage=%s status=%+v err=%v", stage, status, tickErr)
		}
		if status.code == "review_required" || !time.Now().Before(deadline) || installCtx.Err() != nil {
			t.Fatalf("install incomplete: stage=%s status=%+v err=%v", stage, status, tickErr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The load effect: one row, applied, and the runner's own receipt says it loaded the image (it was absent).
	evidence.LoadEffectRows = scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='load_image'`, committed.OperationID)
	loadState := scalar[string](t, s.st, `SELECT state FROM app_platform_operation_effects WHERE operation_id=? AND kind='load_image'`, committed.OperationID)
	loadReceipt := scalar[string](t, s.st, `SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND kind='load_image'`, committed.OperationID)
	if evidence.LoadEffectRows != 1 || loadState != "applied" {
		t.Fatalf("load_image effect: rows=%d state=%q", evidence.LoadEffectRows, loadState)
	}
	if json.Valid([]byte(loadReceipt)) {
		evidence.LoadEffect = json.RawMessage(loadReceipt)
	}
	counting.mu.Lock()
	evidence.BlobSends, evidence.BlobsStored, evidence.LoadExecutes = counting.sends, counting.stored, counting.loads
	receipt := counting.receipt
	counting.mu.Unlock()
	if receipt == nil {
		t.Fatal("the runner never returned a load receipt on this Portal's control socket")
	}
	evidence.Receipt, _ = json.Marshal(receipt)
	evidence.ReceiptState = receipt.State
	if receipt.State != "loaded" || receipt.Outcome != "" || receipt.Removed {
		t.Fatalf("load receipt %q outcome %q removed %v: the image was not loaded fresh", receipt.State, receipt.Outcome, receipt.Removed)
	}
	if receipt.ImageID != expected.ImageID || receipt.Store != expected.Store {
		t.Fatalf("load receipt image %s store %s, expected %s on the %s store", receipt.ImageID, receipt.Store, expected.ImageID, expected.Store)
	}
	claim := receipt.Claim
	bare := func(digest string) string { return strings.TrimPrefix(digest, "sha256:") }
	if bare(claim.ReleaseSHA256) != bare(expected.ReleaseSHA256) || bare(claim.ManifestDigest) != bare(expected.ManifestDigest) || bare(claim.ConfigDigest) != bare(expected.ConfigDigest) || claim.Platform != expected.Platform {
		t.Fatalf("the runner's claim does not bind the package's image: %+v", claim)
	}
	if evidence.BlobsStored != expected.Blobs || evidence.LoadExecutes < 2 {
		t.Fatalf("blob upload over the control socket: %d stored of %d, %d load execute(s)", evidence.BlobsStored, expected.Blobs, evidence.LoadExecutes)
	}
	t.Logf("IMAGE_DELIVERY_CHILD_LOAD_RECEIPT state=%s image=%s store=%s blobs_stored=%d", receipt.State, receipt.ImageID, receipt.Store, evidence.BlobsStored)

	// The runner's own start receipt names the container it started; the parent ties the daemon's container to it.
	rows, err := s.st.query(`SELECT stage,receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND kind<>'load_image' ORDER BY stage`, committed.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var stage, raw string
		if err := rows.Scan(&stage, &raw); err != nil {
			t.Fatal(err)
		}
		if id := imageDeliveryNativeID(raw); stage == "start" && id != "" && evidence.StartNativeID == "" {
			evidence.StartNativeID = id
		}
	}
	rows.Close()
	if evidence.StartNativeID == "" {
		t.Fatal("no start effect of the operation carries a native ID")
	}
	// Readiness was reached (the backend answered its probe) and the app answers through the platform path.
	evidence.ReadinessSeen = scalar[int](t, s.st, `SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active' AND readiness_evidence<>'{}' AND observed_ms IS NOT NULL`, committed.InstallationID) == 1
	if !evidence.ReadinessSeen {
		t.Fatal("the activation carries no readiness evidence")
	}
	// A worker-installed generation-1 app has no service-task history (that is tied to the managed-install review
	// format), so the answer is a signed session request through the platform to the running backend, the path the
	// enable and repair acceptances use: the Scheduled Digest service serves its current digest.
	configureNativeScheduledDigestSetup(t, s, session, committed.InstallationID)
	answer, err := s.requestPlatformWritableBackend(ctx, session, committed.InstallationID, "GET", "/v1/digest", nil, endpoint)
	var snapshot map[string]any
	if err != nil || answer.Status != http.StatusOK || json.Unmarshal(answer.Body, &snapshot) != nil || len(snapshot) == 0 {
		t.Fatalf("the installed Scheduled Digest did not answer through the platform: status %d err %v body %q", answer.Status, err, answer.Body)
	}
	evidence.AppAnswered = true
	t.Log("IMAGE_DELIVERY_CHILD_APP_ANSWERED")

	// Hand over to the parent while the container is running.
	writeEvidence := func() {
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "child-evidence.json"), raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	writeEvidence()
	if err := os.WriteFile(filepath.Join(dir, imageDeliveryReadyFile), []byte("ready\n"), 0644); err != nil {
		t.Fatal(err)
	}
	released := false
	for until := time.Now().Add(120 * time.Second); time.Now().Before(until); time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(dir, imageDeliveryDoneFile)); err == nil {
			released = true
			break
		}
	}
	if !released {
		t.Fatal("the parent never released the child after inspecting the daemon")
	}

	// Leave nothing running: the ordinary managed uninstall stops and removes the container and its data.
	removal, err := s.createPlatformRemovalReview(session, committed.InstallationID, appplatform.ActionUninstall)
	if err != nil {
		t.Fatal("uninstall review", err)
	}
	removalCommit, err := s.consumePlatformRemovalReview(session, removal.ID, removal.SHA256, platformFixtureID(903))
	if err != nil {
		t.Fatal("uninstall admission", err)
	}
	removalLease, err := s.claimNextPlatformManagedUninstall(ctx, platformFixtureID(904))
	if err != nil || removalLease.lease.id != removalCommit.OperationID {
		t.Fatal("uninstall claim", removalLease, err)
	}
	removalRun, err := s.runPlatformManagedUninstall(ctx, removalLease.lease, endpoint)
	if err != nil || removalRun.lease.snapshot.Stage != appplatform.StageComplete {
		t.Fatal("uninstall execution", removalRun, err)
	}
	evidence.Uninstalled = true
	writeEvidence()
	t.Log("IMAGE_DELIVERY_CHILD_UNINSTALLED")
}
