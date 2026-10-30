package driver

import (
	"encoding/json"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

func TestCandidateNativeHandlesCannotBeForgedByJSON(t *testing.T) {
	for _, value := range []any{CandidateLaunch{}, CandidateRunningObservation{}} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("native handle serialized")
		}
	}
	var launch CandidateLaunch
	var observed CandidateRunningObservation
	if json.Unmarshal([]byte(`{}`), &launch) == nil || json.Unmarshal([]byte(`{"valid":true}`), &observed) == nil {
		t.Fatal("native handle reconstructed from JSON")
	}
	if _, err := launch.Snapshot(); err == nil {
		t.Fatal("zero launch fabricated native preflight")
	}
	if _, err := observed.Snapshot(); err == nil {
		t.Fatal("zero observation fabricated running evidence")
	}
}
func TestCandidateNativeRequestBindsDescriptorAndDataEvidence(t *testing.T) {
	raw, digest, _ := managedImageFixture(t)
	owner := containerFixture(t).owner
	identity := CandidateIdentity{DeploymentID: owner.DeploymentID, ControllerEpoch: owner.ControllerEpoch, InstallationID: owner.InstallationID, OperationID: owner.OperationID, ResourceID: owner.ResourceID, NamespaceID: owner.NamespaceID, ReleaseSHA256: digest, Generation: owner.Generation, ControlFence: owner.ControlFence, WriterFence: owner.WriterFence}
	valid := CandidateLaunchRequest{Descriptor: raw, Identity: identity, Platform: "linux/amd64", Data: CandidateDataEvidence{Root: data.RootIdentity{FilesystemUUID: owner.DeploymentID, Inode: 10}, ProjectID: 1, Device: 7, NamespaceInode: 12, QuotaBytes: 1 << 20, QuotaInodes: 128}}
	if !candidateRequestValid(valid) {
		t.Fatal("valid native request refused")
	}
	for _, mutate := range []func(*CandidateLaunchRequest){func(r *CandidateLaunchRequest) { r.Descriptor = []byte(`{}`) }, func(r *CandidateLaunchRequest) { r.Identity.Generation = "0" }, func(r *CandidateLaunchRequest) { r.Data.NamespaceInode = 0 }, func(r *CandidateLaunchRequest) { r.Data.ProjectID = 0 }, func(r *CandidateLaunchRequest) { r.Data.QuotaBytes = 1 }, func(r *CandidateLaunchRequest) { r.Platform = "linux/386" }} {
		request := valid
		mutate(&request)
		if candidateRequestValid(request) {
			t.Fatal("invalid native request accepted")
		}
	}
}
