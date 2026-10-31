package driver

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func updateStartBindingFixture() UpdateStartBinding {
	return UpdateStartBinding{
		EffectID:                "70000000-0000-4000-8000-000000000100",
		ClaimID:                 "80000000-0000-4000-8000-000000000101",
		PayloadSHA256:           strings.Repeat("a", 64),
		OwnershipSHA256:         strings.Repeat("b", 64),
		CheckpointReceiptSHA256: strings.Repeat("c", 64),
	}
}

func TestUpdateStartContainerPolicyPreservesIsolation(t *testing.T) {
	raw, digest, reference := managedImageFixture(t)
	owner := containerFixture(t).owner
	owner.Generation, owner.WriterFence = "2", "2"
	image := BackendImageInspection{
		ReleaseSHA256: digest, Reference: reference,
		ImageID: "sha256:" + strings.Repeat("d", 64), Platform: "linux/amd64",
	}
	roots := containerRoots{Data: "/srv/portal-data", Sockets: "/run/portal-apps"}
	readonly, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		t.Fatal(err)
	}
	original := string(readonly.body)
	binding := updateStartBindingFixture()
	update, err := planUpdateStartContainer(raw, image, owner, roots, binding, UpdateStartWritableSandboxProfile)
	if err != nil {
		t.Fatal(err)
	}
	if !validContainerPlan(update) || update.bodySHA == readonly.bodySHA || string(readonly.body) != original {
		t.Fatal("update-start policy bypassed identity or changed the read-only builder")
	}
	var old, current containerCreate
	if json.Unmarshal(readonly.body, &old) != nil || json.Unmarshal(update.body, &current) != nil {
		t.Fatal("decode native plans")
	}
	if current.HostConfig.Mounts[0].ReadOnly || current.HostConfig.Mounts[0].Target != "/data" ||
		current.Labels[containerLabelPrefix+"format"] != "update-start/1-dev.1" ||
		current.Labels[containerLabelPrefix+"generation"] != "2" ||
		current.Labels[containerLabelPrefix+"writer-fence"] != "2" ||
		current.Labels[containerLabelPrefix+"checkpoint-receipt"] != binding.CheckpointReceiptSHA256 ||
		current.Labels[containerLabelPrefix+"start-claim"] != binding.ClaimID ||
		len(current.Env) != 2 {
		t.Fatal("update-start native identity is incomplete")
	}
	current.HostConfig.Mounts[0].ReadOnly = true
	current.Labels = old.Labels
	if !reflect.DeepEqual(current, old) {
		t.Fatal("update-start builder weakened another closed runtime constraint")
	}
	readonlyUpdate, err := planUpdateStartContainer(raw, image, owner, roots, binding, UpdateStartReadOnlySandboxProfile)
	if err != nil || !validContainerPlan(readonlyUpdate) {
		t.Fatal("read-only generation update plan", err)
	}
	var readonlyGeneration containerCreate
	if json.Unmarshal(readonlyUpdate.body, &readonlyGeneration) != nil ||
		!readonlyGeneration.HostConfig.Mounts[0].ReadOnly {
		t.Fatal("read-only generation update gained data write access")
	}
	owner.Generation, owner.WriterFence = "1", "2"
	if _, err := planUpdateStartContainer(raw, image, owner, roots, binding, UpdateStartWritableSandboxProfile); err == nil {
		t.Fatal("update-start builder admitted an initial generation")
	}
	owner.Generation, owner.WriterFence = "2", "1"
	if _, err := planUpdateStartContainer(raw, image, owner, roots, binding, UpdateStartWritableSandboxProfile); err == nil {
		t.Fatal("update-start builder admitted an initial writer fence")
	}
}

func TestUpdateStartNativeHandlesRefuseJSON(t *testing.T) {
	var launch UpdateStartLaunch
	if _, err := launch.Snapshot(); err == nil {
		t.Fatal("zero launch authority")
	}
	if _, err := json.Marshal(launch); err == nil {
		t.Fatal("launch serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &launch); err == nil {
		t.Fatal("launch decoded")
	}
	var observed UpdateStartRunningObservation
	if _, err := observed.Snapshot(); err == nil {
		t.Fatal("zero process proof")
	}
	if _, err := json.Marshal(observed); err == nil {
		t.Fatal("process proof serialized")
	}
	if err := json.Unmarshal([]byte(`{"valid":true}`), &observed); err == nil {
		t.Fatal("process proof decoded")
	}
}
