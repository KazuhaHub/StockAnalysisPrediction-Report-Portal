package app

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// The checks of the real-runner image delivery acceptance. They are plain functions of what the Docker API
// reported, so every proof has a unit test that fails in the direction it guards.

type imageDeliveryImage struct {
	ID          string
	RepoTags    []string
	RepoDigests []string
}

type imageDeliveryEvent struct {
	Type, Action, ID, Image string
}

type imageDeliveryContainer struct {
	ID, ImageID, State string
}

type imageDeliveryInspect struct {
	ID, OS, Architecture string
	RepoTags             []string
	RepoDigests          []string
	Layers               []string
}

// imageDeliveryDaemonID is the form the Docker API reports an image or a layer in: "sha256:" and the hex. The
// descriptor writes the same digest as bare hex, so every comparison with the daemon goes through this.
func imageDeliveryDaemonID(digest string) string {
	return "sha256:" + strings.TrimPrefix(digest, "sha256:")
}

// imageDeliveryExpectedID is the ID the daemon gives the loaded image (doc 170, resolution 7), in the daemon's form.
func imageDeliveryExpectedID(containerdStore bool, manifestDigest, configDigest string) string {
	if containerdStore {
		return imageDeliveryDaemonID(manifestDigest)
	}
	return imageDeliveryDaemonID(configDigest)
}

func imageDeliveryCheckAbsent(images []imageDeliveryImage, target string) error {
	for _, image := range images {
		if image.ID == target {
			return fmt.Errorf("the store still holds an image with the package's ID %s", target)
		}
	}
	return nil
}

func imageDeliverySorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// imageDeliveryCheckAfterLoad proves the only change to the store is the target image, with no name of any kind.
func imageDeliveryCheckAfterLoad(before, after []imageDeliveryImage, target string) error {
	prior := map[string]imageDeliveryImage{}
	for _, image := range before {
		prior[image.ID] = image
	}
	found := false
	for _, image := range after {
		if image.ID == target {
			found = true
			if len(image.RepoTags) != 0 || len(image.RepoDigests) != 0 {
				return fmt.Errorf("the loaded image carries names: tags %v digests %v", image.RepoTags, image.RepoDigests)
			}
			continue
		}
		old, ok := prior[image.ID]
		if !ok {
			return fmt.Errorf("an unexpected image appeared: %s", image.ID)
		}
		if !slices.Equal(imageDeliverySorted(old.RepoTags), imageDeliverySorted(image.RepoTags)) ||
			!slices.Equal(imageDeliverySorted(old.RepoDigests), imageDeliverySorted(image.RepoDigests)) {
			return fmt.Errorf("the names of image %s changed", image.ID)
		}
		delete(prior, image.ID)
	}
	if !found {
		return fmt.Errorf("the loaded image %s is not in the store", target)
	}
	if len(prior) != 0 {
		return fmt.Errorf("%d image(s) disappeared during the scenario", len(prior))
	}
	return nil
}

// imageDeliveryCheckEvents requires a live watcher (a container event was seen), exactly one image load, on the
// target, and no pull, tag, untag, push or import of an image while the scenario ran.
func imageDeliveryCheckEvents(events []imageDeliveryEvent, target string) error {
	live, loads := false, 0
	for _, e := range events {
		if e.Type == "container" && e.Action == "create" {
			live = true
		}
		if e.Type != "image" {
			continue
		}
		switch e.Action {
		case "pull", "tag", "untag", "push", "import":
			return fmt.Errorf("image event %q on %s", e.Action, e.ID)
		case "load":
			loads++
			if e.ID != target {
				return fmt.Errorf("image load event on %s, not the package's image %s", e.ID, target)
			}
		}
	}
	if !live {
		return fmt.Errorf("the event watcher saw no container creation, so its silence proves nothing")
	}
	if loads != 1 {
		return fmt.Errorf("%d image load events, expected exactly one", loads)
	}
	return nil
}

// imageDeliveryCheckAfterRemoval requires the daemon to hold exactly the images it held before the load, with their
// names untouched, and not the target: the removal took the delivered image and nothing else.
func imageDeliveryCheckAfterRemoval(before, after []imageDeliveryImage, target string) error {
	for _, image := range after {
		if image.ID == target {
			return fmt.Errorf("the delivered image %s is still in the store", target)
		}
	}
	// The same comparison as after the load, with the target absent from both lists.
	var kept []imageDeliveryImage
	for _, image := range before {
		if image.ID != target {
			kept = append(kept, image)
		}
	}
	return imageDeliveryCheckAfterLoad(kept, append(append([]imageDeliveryImage(nil), after...), imageDeliveryImage{ID: target}), target)
}

// imageDeliveryCheckRemoval requires exactly one image delete event, on the target: the runner's remove-image
// removed the delivered image by ID and nothing else. It is checked only after the uninstall that freed the image.
func imageDeliveryCheckRemoval(events []imageDeliveryEvent, target string) error {
	deletes := 0
	for _, e := range events {
		if e.Type != "image" || e.Action != "delete" {
			continue
		}
		deletes++
		if e.ID != target {
			return fmt.Errorf("image delete event on %s, not the package's image %s", e.ID, target)
		}
	}
	if deletes != 1 {
		return fmt.Errorf("%d image delete events, expected exactly one", deletes)
	}
	return nil
}

// imageDeliveryCheckContainers requires at least one running container whose image ID is the target's, and
// refuses a running container of any other image among those it was asked about.
func imageDeliveryCheckContainers(containers []imageDeliveryContainer, target string) error {
	running := 0
	for _, c := range containers {
		if c.ImageID != target {
			return fmt.Errorf("container %s runs image %s, not %s", c.ID, c.ImageID, target)
		}
		if c.State == "running" {
			running++
		}
	}
	if running == 0 {
		return fmt.Errorf("no running container uses the image %s", target)
	}
	return nil
}

func imageDeliveryCheckInspect(got imageDeliveryInspect, target, platform string, layers []string) error {
	if got.ID != target {
		return fmt.Errorf("stored image ID %s is not %s", got.ID, target)
	}
	if got.OS+"/"+got.Architecture != platform {
		return fmt.Errorf("stored image platform %s/%s is not %s", got.OS, got.Architecture, platform)
	}
	if !slices.Equal(got.Layers, layers) {
		return fmt.Errorf("stored layer chain %v is not the package's %v", got.Layers, layers)
	}
	if len(got.RepoTags) != 0 || len(got.RepoDigests) != 0 {
		return fmt.Errorf("stored image carries names: tags %v digests %v", got.RepoTags, got.RepoDigests)
	}
	return nil
}

var runnerReadyLine = regexp.MustCompile(`app-runner ready profile=(\S+) platform=(\S+) images=(\S+)`)

// imageDeliveryParseRunnerReady reads the profile, platform and image mode the runner announced on startup.
func imageDeliveryParseRunnerReady(output string) (profile, platform, images string, err error) {
	m := runnerReadyLine.FindStringSubmatch(output)
	if m == nil {
		return "", "", "", fmt.Errorf("the runner's ready line is missing from its output")
	}
	return m[1], m[2], m[3], nil
}

// imageDeliveryNativeID finds the first non-empty native_id anywhere in a receipt.
func imageDeliveryNativeID(receipt string) string {
	var v any
	if json.Unmarshal([]byte(receipt), &v) != nil {
		return ""
	}
	var find func(any) string
	find = func(v any) string {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["native_id"].(string); ok && id != "" {
				return id
			}
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if id := find(x[k]); id != "" {
					return id
				}
			}
		case []any:
			for _, e := range x {
				if id := find(e); id != "" {
					return id
				}
			}
		}
		return ""
	}
	return find(v)
}

// imageDeliveryCheckContainerOwner ties a container to the runner's start: its ID is the native ID the start
// receipt recorded, and its owner labels name this installation, operation and release.
func imageDeliveryCheckContainerOwner(containerID string, labels map[string]string, nativeID, installation, operation, release string) error {
	if nativeID == "" || containerID != nativeID {
		return fmt.Errorf("container %s is not the native ID %q of the runner's start receipt", containerID, nativeID)
	}
	for label, want := range map[string]string{"installation": installation, "operation": operation, "release": release} {
		if got := labels["org.portal.app."+label]; got != want || want == "" {
			return fmt.Errorf("container label %s is %q, expected %q", label, got, want)
		}
	}
	return nil
}
