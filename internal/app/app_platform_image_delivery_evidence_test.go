package app

import (
	"fmt"
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
		if !slices.Equal(imageDeliverySorted(old.RepoTags), imageDeliverySorted(image.RepoTags)) {
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

// imageDeliveryCheckEvents requires a live watcher (a container event was seen) and no pull, tag, untag, push or
// import of an image while the scenario ran.
func imageDeliveryCheckEvents(events []imageDeliveryEvent) error {
	live := false
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
		}
	}
	if !live {
		return fmt.Errorf("the event watcher saw no container creation, so its silence proves nothing")
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
