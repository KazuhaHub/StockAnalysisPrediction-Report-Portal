package app

import (
	"strings"
	"testing"
)

const (
	deliveryTargetID = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	deliveryOtherID  = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

var deliveryLayers = []string{
	"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
}

// Fails if the "image absent before the load" proof passes while a copy of the target is still in the store,
// which would turn the whole scenario into a start of a pre-existing image.
func TestDeliveryProofRefusesATargetThatIsStillPresentBeforeTheLoad(t *testing.T) {
	if err := imageDeliveryCheckAbsent([]imageDeliveryImage{{ID: deliveryOtherID}}, deliveryTargetID); err != nil {
		t.Fatal(err)
	}
	for _, present := range [][]imageDeliveryImage{
		{{ID: deliveryTargetID}},
		{{ID: deliveryOtherID}, {ID: deliveryTargetID, RepoTags: []string{"x:y"}}},
	} {
		if imageDeliveryCheckAbsent(present, deliveryTargetID) == nil {
			t.Fatal("a store that still holds the target passed the absence proof")
		}
	}
}

// Fails if the load may add a tag, a digest reference or a second image, or may alter another image.
func TestDeliveryProofAcceptsOnlyTheUntaggedTargetAsTheDifference(t *testing.T) {
	before := []imageDeliveryImage{{ID: deliveryOtherID, RepoTags: []string{"keep:1"}}}
	good := append(append([]imageDeliveryImage{}, before...), imageDeliveryImage{ID: deliveryTargetID})
	if err := imageDeliveryCheckAfterLoad(before, good, deliveryTargetID); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]imageDeliveryImage{
		"missing target": before,
		"tagged target":  {before[0], {ID: deliveryTargetID, RepoTags: []string{"scheduled-digest:latest"}}},
		"digest ref":     {before[0], {ID: deliveryTargetID, RepoDigests: []string{"r/x@sha256:" + strings.Repeat("c", 64)}}},
		"extra image":    {before[0], {ID: deliveryTargetID}, {ID: "sha256:" + strings.Repeat("d", 64)}},
		"retagged other": {{ID: deliveryOtherID, RepoTags: []string{"keep:1", "extra:2"}}, {ID: deliveryTargetID}},
		"dropped other":  {{ID: deliveryTargetID}},
	}
	for name, after := range cases {
		if imageDeliveryCheckAfterLoad(before, after, deliveryTargetID) == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// Fails if the event proof is vacuous (a watcher that saw nothing) or lets a pull, a tag or a push through.
func TestDeliveryProofRequiresALiveWatcherAndNoPullTagOrPush(t *testing.T) {
	live := imageDeliveryEvent{Type: "container", Action: "create", Image: deliveryTargetID}
	load := imageDeliveryEvent{Type: "image", Action: "load", ID: deliveryTargetID}
	if err := imageDeliveryCheckEvents([]imageDeliveryEvent{load, live}); err != nil {
		t.Fatal(err)
	}
	if imageDeliveryCheckEvents(nil) == nil || imageDeliveryCheckEvents([]imageDeliveryEvent{load}) == nil {
		t.Fatal("a watcher that never saw a container event passed")
	}
	for _, action := range []string{"pull", "tag", "untag", "push", "import"} {
		bad := imageDeliveryEvent{Type: "image", Action: action, ID: "registry.example/x"}
		if imageDeliveryCheckEvents([]imageDeliveryEvent{load, live, bad}) == nil {
			t.Fatalf("image %s passed", action)
		}
	}
	// A container event with a custom action name is not an image event and must not trip the image rules.
	if err := imageDeliveryCheckEvents([]imageDeliveryEvent{live, {Type: "container", Action: "pull"}}); err != nil {
		t.Fatal(err)
	}
}

// Fails if the running container is accepted although its image is not exactly the package's image.
func TestDeliveryProofBindsTheRunningContainerToTheExactImage(t *testing.T) {
	running := []imageDeliveryContainer{{ID: "c1", ImageID: deliveryTargetID, State: "running"}}
	if err := imageDeliveryCheckContainers(running, deliveryTargetID); err != nil {
		t.Fatal(err)
	}
	for name, cs := range map[string][]imageDeliveryContainer{
		"none":          nil,
		"other image":   {{ID: "c1", ImageID: deliveryOtherID, State: "running"}},
		"not running":   {{ID: "c1", ImageID: deliveryTargetID, State: "exited"}},
		"mixed running": {{ID: "c1", ImageID: deliveryTargetID, State: "running"}, {ID: "c2", ImageID: deliveryTargetID, State: "created"}},
	} {
		// "mixed running" is accepted: a helper container of the same image may exist; only a running one is required.
		err := imageDeliveryCheckContainers(cs, deliveryTargetID)
		if name == "mixed running" {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// Fails if the stored image passes with another layer chain, another platform or a name.
func TestDeliveryProofChecksTheStoredImageAgainstThePackage(t *testing.T) {
	ok := imageDeliveryInspect{ID: deliveryTargetID, OS: "linux", Architecture: "amd64", Layers: deliveryLayers}
	if err := imageDeliveryCheckInspect(ok, deliveryTargetID, "linux/amd64", deliveryLayers); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]imageDeliveryInspect{
		"id":           {ID: deliveryOtherID, OS: "linux", Architecture: "amd64", Layers: deliveryLayers},
		"layer order":  {ID: deliveryTargetID, OS: "linux", Architecture: "amd64", Layers: []string{deliveryLayers[1], deliveryLayers[0]}},
		"fewer layers": {ID: deliveryTargetID, OS: "linux", Architecture: "amd64", Layers: deliveryLayers[:1]},
		"arch":         {ID: deliveryTargetID, OS: "linux", Architecture: "arm64", Layers: deliveryLayers},
		"tag":          {ID: deliveryTargetID, OS: "linux", Architecture: "amd64", Layers: deliveryLayers, RepoTags: []string{"a:b"}},
		"repo digest":  {ID: deliveryTargetID, OS: "linux", Architecture: "amd64", Layers: deliveryLayers, RepoDigests: []string{"a@sha256:" + strings.Repeat("e", 64)}},
	} {
		if imageDeliveryCheckInspect(bad, deliveryTargetID, "linux/amd64", deliveryLayers) == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// Fails if the expected ID follows the wrong store: the classic store names an image by its config digest, the
// containerd store by its manifest digest.
func TestDeliveryExpectedImageIDFollowsTheStore(t *testing.T) {
	if imageDeliveryExpectedID(false, "sha256:m", "sha256:c") != "sha256:c" || imageDeliveryExpectedID(true, "sha256:m", "sha256:c") != "sha256:m" {
		t.Fatal("expected image id")
	}
}

// Fails if a descriptor digest (bare hex) is compared with the daemon's form ("sha256:" and the hex) without
// normalising it, which would make "the image is absent from the store" pass on a store that still holds it.
func TestDeliveryDigestsAreComparedInTheDaemonsForm(t *testing.T) {
	hex := strings.Repeat("9", 64)
	if imageDeliveryDaemonID(hex) != "sha256:"+hex || imageDeliveryDaemonID("sha256:"+hex) != "sha256:"+hex {
		t.Fatal("daemon id form")
	}
	if imageDeliveryExpectedID(false, strings.Repeat("8", 64), hex) != "sha256:"+hex {
		t.Fatal("expected id from a bare config digest")
	}
	if imageDeliveryCheckAbsent([]imageDeliveryImage{{ID: "sha256:" + hex}}, imageDeliveryExpectedID(false, "", hex)) == nil {
		t.Fatal("a bare descriptor digest hid a present image")
	}
}
