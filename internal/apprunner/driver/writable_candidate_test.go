package driver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func writableBindingFixture() WritableCandidateBinding {
	return WritableCandidateBinding{EffectID: "70000000-0000-4000-8000-000000000000", ClaimID: "80000000-0000-4000-8000-000000000000", PayloadSHA256: strings.Repeat("c", 64), OwnershipSHA256: strings.Repeat("d", 64), DataReceiptSHA256: strings.Repeat("e", 64), WriterReceiptSHA256: strings.Repeat("f", 64)}
}
func TestWritableContainerPolicyKeepsIsolationAndExactClaim(t *testing.T) {
	if WritableCandidateSandboxProfile != control.WritableCandidateSandboxProfile {
		t.Fatal("control/native profile contract differs")
	}
	raw, digest, reference := managedImageFixture(t)
	owner := containerFixture(t).owner
	owner.WriterFence = "1"
	image := BackendImageInspection{ReleaseSHA256: digest, Reference: reference, ImageID: "sha256:" + strings.Repeat("b", 64), Platform: "linux/amd64"}
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	readonly, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	original := string(readonly.body)
	binding := writableBindingFixture()
	writable, err := planWritableCandidateContainer(raw, image, owner, roots, binding)
	if err != nil {
		t.Fatal(err)
	}
	if !validContainerPlan(writable) || writable.bodySHA == readonly.bodySHA || string(readonly.body) != original {
		t.Fatal("writable policy bypassed identity or changed old builder")
	}
	var old, current containerCreate
	json.Unmarshal(readonly.body, &old)
	json.Unmarshal(writable.body, &current)
	if current.HostConfig.Mounts[0].ReadOnly || current.HostConfig.Mounts[0].Target != "/data" || current.Labels[containerLabelPrefix+"format"] != "writable-candidate/1-dev.1" || current.Labels[containerLabelPrefix+"writer-receipt"] != binding.WriterReceiptSHA256 || current.Labels[containerLabelPrefix+"start-claim"] != binding.ClaimID || len(current.Env) != 2 {
		t.Fatal("unbound writable access")
	}
	// The independently tested read-only baseline supplies every other reviewed
	// resource, network, syscall, identity and mount constraint unchanged.
	current.HostConfig.Mounts[0].ReadOnly = true
	current.Labels = old.Labels
	if !reflect.DeepEqual(current, old) {
		t.Fatal("writable builder weakened another closed constraint")
	}
	changed := binding
	changed.WriterReceiptSHA256 = strings.Repeat("a", 64)
	other, err := planWritableCandidateContainer(raw, image, owner, roots, changed)
	if err != nil || other.bodySHA == writable.bodySHA {
		t.Fatal("writer receipt absent from native plan")
	}
	copy := writable
	copy.writable = &changed
	if validContainerPlan(copy) {
		t.Fatal("opaque binding changed without body policy")
	}
	owner.Generation = "2"
	if _, err := planWritableCandidateContainer(raw, image, owner, roots, binding); err == nil {
		t.Fatal("initial writable path admitted takeover")
	}
}
func TestWritableNativeHandlesAndObservationsRefuseJSON(t *testing.T) {
	var launch WritableCandidateLaunch
	if _, err := launch.Snapshot(); err == nil {
		t.Fatal("zero launch authority")
	}
	if _, err := json.Marshal(launch); err == nil {
		t.Fatal("launch serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &launch); err == nil {
		t.Fatal("launch decoded")
	}
	var observed WritableCandidateRunningObservation
	if _, err := observed.Snapshot(); err == nil {
		t.Fatal("zero process proof")
	}
	if _, err := json.Marshal(observed); err == nil {
		t.Fatal("process proof serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &observed); err == nil {
		t.Fatal("process proof decoded")
	}
	if _, err := observed.Snapshot(); err == nil {
		t.Fatal("decode retained authority")
	}
}

func TestWritableRecoveryObservationCannotBeDecoded(t *testing.T) {
	var observation WritableCandidateRecoveryObservation
	if reflect.TypeOf(observation).ConvertibleTo(reflect.TypeOf(WritableCandidateRunningObservation{})) {
		t.Fatal("recovery proof convertible to live execution authority")
	}
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("zero recovery native authority")
	}
	if _, err := json.Marshal(observation); err == nil {
		t.Fatal("recovery native authority serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &observation); err == nil {
		t.Fatal("recovery native authority decoded")
	}
	if _, err := observation.Snapshot(); err == nil {
		t.Fatal("failed decoding retained recovery authority")
	}
}
