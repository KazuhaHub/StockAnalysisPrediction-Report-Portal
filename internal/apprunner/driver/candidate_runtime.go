package driver

import (
	"encoding/json"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

// CandidateIdentity is trusted infrastructure input from a verified journal
// claim, not an application or browser-selected container configuration.
type CandidateIdentity struct {
	DeploymentID, ControllerEpoch, InstallationID, OperationID, ResourceID, NamespaceID string
	ReleaseSHA256, Generation, ControlFence, WriterFence                                string
}
type CandidateDataEvidence struct {
	Root                    data.RootIdentity
	ProjectID               uint32
	Device, NamespaceInode  uint64
	QuotaBytes, QuotaInodes int64
}
type CandidateLaunchRequest struct {
	Identity   CandidateIdentity
	Descriptor []byte
	Platform   string
	Data       CandidateDataEvidence
}

// CandidateLaunchSnapshot excludes host paths, image command/environment and
// control secrets. Its structure alone proves no physical effect or authority.
type CandidateLaunchSnapshot struct {
	Identity     CandidateIdentity     `json:"identity"`
	ImageID      string                `json:"image_id"`
	Platform     string                `json:"platform"`
	PolicySHA256 string                `json:"policy_sha256"`
	MountsSHA256 string                `json:"mounts_sha256"`
	Data         CandidateDataEvidence `json:"data"`
}
type CandidateRunningSnapshot struct {
	Launch     CandidateLaunchSnapshot `json:"launch"`
	NativeID   string                  `json:"native_id"`
	ObservedMS int64                   `json:"observed_ms"`
}

// CandidateLaunch owns live private native pins. Neither it nor the running
// observation may be serialized/deserialized into executable authority.
type CandidateLaunch struct{ state *candidateLaunchState }

func (CandidateLaunch) MarshalJSON() ([]byte, error)  { return nil, ErrContainerUncertain }
func (c *CandidateLaunch) UnmarshalJSON([]byte) error { c.state = nil; return ErrContainerUncertain }

type CandidateRunningObservation struct {
	snapshot CandidateRunningSnapshot
	valid    bool
}

func (CandidateRunningObservation) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (o *CandidateRunningObservation) UnmarshalJSON([]byte) error {
	*o = CandidateRunningObservation{}
	return ErrContainerUncertain
}
func (o CandidateRunningObservation) Snapshot() (CandidateRunningSnapshot, error) {
	if !o.valid || !containerSHA.MatchString(o.snapshot.NativeID) || !nativeImageID.MatchString(o.snapshot.Launch.ImageID) || !containerSHA.MatchString(o.snapshot.Launch.PolicySHA256) || !containerSHA.MatchString(o.snapshot.Launch.MountsSHA256) || o.snapshot.ObservedMS < 0 {
		return CandidateRunningSnapshot{}, ErrContainerUncertain
	}
	return o.snapshot, nil
}

// CandidateRemovalObservation is current native lifecycle evidence for one
// exact previously-running candidate. It is not a journal receipt or proof
// that the application stopped cleanly at an earlier point.
type CandidateRemovalObservation struct {
	snapshot CandidateRemovalSnapshot
	valid    bool
}

type CandidateRemovalSnapshot struct {
	Launch     CandidateLaunchSnapshot `json:"launch"`
	NativeID   string                  `json:"native_id"`
	State      string                  `json:"state"`
	ObservedMS int64                   `json:"observed_ms"`
}

func (CandidateRemovalObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *CandidateRemovalObservation) UnmarshalJSON([]byte) error {
	*o = CandidateRemovalObservation{}
	return ErrContainerUncertain
}
func (o CandidateRemovalObservation) Snapshot() (CandidateRemovalSnapshot, error) {
	if !o.valid || !containerSHA.MatchString(o.snapshot.NativeID) || (o.snapshot.State != "exited" && o.snapshot.State != "absent") || o.snapshot.ObservedMS < 0 {
		return CandidateRemovalSnapshot{}, ErrContainerUncertain
	}
	if !validCandidateLaunchSnapshot(o.snapshot.Launch) {
		return CandidateRemovalSnapshot{}, ErrContainerUncertain
	}
	return o.snapshot, nil
}
func validCandidateLaunchSnapshot(s CandidateLaunchSnapshot) bool {
	d := s.Data
	return validContainerOwner(candidateOwner(s.Identity)) &&
		(s.Platform == "linux/amd64" || s.Platform == "linux/arm64") &&
		nativeImageID.MatchString(s.ImageID) &&
		containerSHA.MatchString(s.PolicySHA256) &&
		containerSHA.MatchString(s.MountsSHA256) &&
		d.ProjectID > 0 &&
		d.NamespaceInode > 0 &&
		d.QuotaBytes > 0 &&
		d.QuotaInodes > 0 &&
		d.QuotaBytes%512 == 0 &&
		d.Root.Inode > 0 &&
		containerUUID.MatchString(d.Root.FilesystemUUID) &&
		d.Root.FilesystemUUID != "00000000-0000-0000-0000-000000000000"
}
func candidateOwner(i CandidateIdentity) containerOwner { return containerOwner(i) }

func candidateRequestValid(request CandidateLaunchRequest) bool {
	d := request.Data
	parsed, err := appplatform.ParseDescriptor(request.Descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != request.Identity.ReleaseSHA256 {
		return false
	}
	return validContainerOwner(candidateOwner(request.Identity)) && (request.Platform == "linux/amd64" || request.Platform == "linux/arm64") && d.ProjectID > 0 && d.NamespaceInode > 0 && d.QuotaBytes > 0 && d.QuotaInodes > 0 && d.QuotaBytes%512 == 0 && d.Root.Inode > 0 && containerUUID.MatchString(d.Root.FilesystemUUID) && d.Root.FilesystemUUID != "00000000-0000-0000-0000-000000000000"
}
func candidateSnapshotHash(value any) string {
	raw, _ := json.Marshal(value)
	return containerHash(raw)
}

// RuntimeConfiguration is current infrastructure metadata, not execution or
// application-readiness authority. Only concrete configuration inspection fills it.
type RuntimeConfiguration struct {
	Platform, SandboxProfile string
	Root                     data.RootIdentity
}
