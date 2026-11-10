package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
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
		"missing target":            before,
		"tagged target":             {before[0], {ID: deliveryTargetID, RepoTags: []string{"scheduled-digest:latest"}}},
		"digest ref":                {before[0], {ID: deliveryTargetID, RepoDigests: []string{"r/x@sha256:" + strings.Repeat("c", 64)}}},
		"extra image":               {before[0], {ID: deliveryTargetID}, {ID: "sha256:" + strings.Repeat("d", 64)}},
		"retagged other":            {{ID: deliveryOtherID, RepoTags: []string{"keep:1", "extra:2"}}, {ID: deliveryTargetID}},
		"dropped other":             {{ID: deliveryTargetID}},
		"other gained a digest ref": {{ID: deliveryOtherID, RepoTags: []string{"keep:1"}, RepoDigests: []string{"r/x@sha256:" + strings.Repeat("f", 64)}}, {ID: deliveryTargetID}},
	}
	for name, after := range cases {
		if imageDeliveryCheckAfterLoad(before, after, deliveryTargetID) == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// Fails if the event proof is vacuous (a watcher that saw nothing), lets a pull, a tag or a push through, or
// accepts anything but exactly one image load, on the target.
func TestDeliveryProofRequiresALiveWatcherExactlyOneLoadAndNoPullTagOrPush(t *testing.T) {
	live := imageDeliveryEvent{Type: "container", Action: "create", Image: deliveryTargetID}
	load := imageDeliveryEvent{Type: "image", Action: "load", ID: deliveryTargetID}
	if err := imageDeliveryCheckEvents([]imageDeliveryEvent{load, live}, deliveryTargetID); err != nil {
		t.Fatal(err)
	}
	if imageDeliveryCheckEvents(nil, deliveryTargetID) == nil || imageDeliveryCheckEvents([]imageDeliveryEvent{load}, deliveryTargetID) == nil {
		t.Fatal("a watcher that never saw a container event passed")
	}
	if imageDeliveryCheckEvents([]imageDeliveryEvent{live}, deliveryTargetID) == nil {
		t.Fatal("no load event passed")
	}
	if imageDeliveryCheckEvents([]imageDeliveryEvent{load, load, live}, deliveryTargetID) == nil {
		t.Fatal("two load events passed")
	}
	if imageDeliveryCheckEvents([]imageDeliveryEvent{{Type: "image", Action: "load"}, live}, deliveryTargetID) == nil {
		t.Fatal("a load event with no ID passed")
	}
	other := imageDeliveryEvent{Type: "image", Action: "load", ID: deliveryOtherID}
	if imageDeliveryCheckEvents([]imageDeliveryEvent{other, live}, deliveryTargetID) == nil {
		t.Fatal("a load of another image passed")
	}
	for _, action := range []string{"pull", "tag", "untag", "push", "import"} {
		bad := imageDeliveryEvent{Type: "image", Action: action, ID: "registry.example/x"}
		if imageDeliveryCheckEvents([]imageDeliveryEvent{load, live, bad}, deliveryTargetID) == nil {
			t.Fatalf("image %s passed", action)
		}
	}
	// A container event with a custom action name is not an image event and must not trip the image rules.
	if err := imageDeliveryCheckEvents([]imageDeliveryEvent{load, live, {Type: "container", Action: "pull"}}, deliveryTargetID); err != nil {
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

// Fails if the runner's ready line is not parsed from the captured output, so the profile claim is an observation.
func TestDeliveryRunnerReadyLineIsObservedNotAssumed(t *testing.T) {
	out := "2026/11/09 22:31:26 app-runner ready profile=linux-docker-rootful-projectquota/v1-dev platform=linux/amd64 images=production\n"
	profile, platform, images, err := imageDeliveryParseRunnerReady(out)
	if err != nil || profile != "linux-docker-rootful-projectquota/v1-dev" || platform != "linux/amd64" || images != "production" {
		t.Fatal(profile, platform, images, err)
	}
	for _, bad := range []string{"", "app-runner starting", "app-runner ready profile=p platform=linux/amd64"} {
		if _, _, _, err := imageDeliveryParseRunnerReady(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
	if _, _, images, _ := imageDeliveryParseRunnerReady(strings.Replace(out, "images=production", "images=local", 1)); images != "local" {
		t.Fatal("a development profile was reported as production")
	}
}

// Fails if the native ID of a start receipt is not found wherever the receipt nests it, or is invented.
func TestDeliveryNativeIDIsReadFromTheReceipt(t *testing.T) {
	id := strings.Repeat("a", 64)
	for _, raw := range []string{`{"native_id":"` + id + `"}`, `{"claim":{"x":1},"outer":{"native_id":"` + id + `"}}`, `[{"a":1},{"native_id":"` + id + `"}]`} {
		if got := imageDeliveryNativeID(raw); got != id {
			t.Fatalf("%s -> %q", raw, got)
		}
	}
	for _, raw := range []string{``, `not json`, `{"native_id":""}`, `{"other":"` + id + `"}`} {
		if got := imageDeliveryNativeID(raw); got != "" {
			t.Fatalf("%q -> %q", raw, got)
		}
	}
}

// Fails if a running container is accepted that the runner did not start for this installation, operation and
// release, or whose ID is not the native ID in the start receipt.
func TestDeliveryContainerIsTiedToTheRunnersStartReceipt(t *testing.T) {
	native := strings.Repeat("b", 64)
	labels := map[string]string{
		"org.portal.app.installation": "inst", "org.portal.app.operation": "op", "org.portal.app.release": "rel",
	}
	if err := imageDeliveryCheckContainerOwner(native, labels, native, "inst", "op", "rel"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(map[string]string) (string, map[string]string){
		"native id":    func(l map[string]string) (string, map[string]string) { return strings.Repeat("c", 64), l },
		"empty native": func(l map[string]string) (string, map[string]string) { return native, l },
		"installation": func(l map[string]string) (string, map[string]string) {
			l["org.portal.app.installation"] = "x"
			return native, l
		},
		"operation": func(l map[string]string) (string, map[string]string) {
			l["org.portal.app.operation"] = "x"
			return native, l
		},
		"release": func(l map[string]string) (string, map[string]string) {
			delete(l, "org.portal.app.release")
			return native, l
		},
	} {
		copied := map[string]string{}
		for k, v := range labels {
			copied[k] = v
		}
		container, changed := change(copied)
		receipt := native
		if name == "empty native" {
			receipt = ""
		}
		if imageDeliveryCheckContainerOwner(container, changed, receipt, "inst", "op", "rel") == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// Fails if the staged descriptor may differ from the built one in anything but the runner protocol range.
func TestDeliveryStagedDescriptorDiffersOnlyInTheRunnerProtocolRange(t *testing.T) {
	built := []byte(`{"id":"a","compatibility":{"platform_api":">=2.0.0-0 <3.0.0","runner_protocol":">=1.0.0 <2.0.0"},"backend":{"images":[{"x":1}]}}`)
	good := []byte(`{"id":"a","compatibility":{"platform_api":">=2.0.0-0 <3.0.0","runner_protocol":">=1.0.0-0 <2.0.0"},"backend":{"images":[{"x":1}]}}`)
	if err := imageDeliveryOnlyRunnerRangeDiffers(built, good); err != nil {
		t.Fatal(err)
	}
	for name, staged := range map[string]string{
		"identical":      string(built),
		"publisher":      `{"id":"a","publisher":"p","compatibility":{"platform_api":">=2.0.0-0 <3.0.0","runner_protocol":">=1.0.0-0 <2.0.0"},"backend":{"images":[{"x":1}]}}`,
		"platform range": `{"id":"a","compatibility":{"platform_api":">=2.0.0 <3.0.0","runner_protocol":">=1.0.0-0 <2.0.0"},"backend":{"images":[{"x":1}]}}`,
		"image":          `{"id":"a","compatibility":{"platform_api":">=2.0.0-0 <3.0.0","runner_protocol":">=1.0.0-0 <2.0.0"},"backend":{"images":[{"x":2}]}}`,
		"not json":       `{`,
	} {
		if imageDeliveryOnlyRunnerRangeDiffers(built, []byte(staged)) == nil {
			t.Fatalf("%s passed", name)
		}
	}
}

// The finding that forces the one rewrite: the Apps builder declares runner_protocol ">=1.0.0 <2.0.0", and a
// range without a pre-release tag never matches this runner's protocol 1.0.0-dev.1 (semver pre-release rule).
// Fails if the built range ever becomes compatible (the rewrite is then unnecessary) or the runner's protocol
// stops being a pre-release (the finding no longer holds).
func TestDeliveryBuiltRunnerProtocolRangeExcludesTheRunnersDevProtocol(t *testing.T) {
	parts := newPlatformManagedImageParts(t, "2.0.0", "range")
	var m appplatform.Manifest
	if err := json.Unmarshal(parts.descriptor, &m); err != nil || m.Backend == nil {
		t.Fatal(err)
	}
	target := appplatform.CompatibilityTarget{PlatformAPI: "2.0.0-dev.1", Runner: &appplatform.RunnerCompatibility{
		Protocol: transport.ManagedRunnerProtocol, Platform: m.Backend.Platforms[0], BackendProtocol: m.Backend.Protocol}}
	for rng, want := range map[string]bool{">=1.0.0 <2.0.0": false, ">=1.0.0-0 <2.0.0": true} {
		m.Compatibility.RunnerProtocol = rng
		m.Compatibility.PlatformAPI = ">=2.0.0-0 <3.0.0"
		result, err := appplatform.EvaluateCompatibility(m, target)
		if err != nil {
			t.Fatal(err)
		}
		mismatch := false
		for _, issue := range result.Issues {
			if issue == appplatform.RunnerProtocolMismatch {
				mismatch = true
			}
		}
		if mismatch == want {
			t.Fatalf("range %q: runner protocol mismatch=%v, want %v (issues %v)", rng, mismatch, !want, result.Issues)
		}
	}
}
