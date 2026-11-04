package driver

import (
	"encoding/json"
)

const (
	UpdateStartReadOnlySandboxProfile = Profile
	UpdateStartWritableSandboxProfile = WritableCandidateSandboxProfile
)

// UpdateStartBinding is reconstructed only from an exact applied checkpoint
// source and a current reserved update-start command. It is metadata, not
// native execution authority.
type UpdateStartBinding struct {
	EffectID, ClaimID, PayloadSHA256, OwnershipSHA256, CheckpointReceiptSHA256 string
	PolicyRevision                                                             string
}

type UpdateStartSource struct {
	OperationID, ResourceID, NativeID                    string
	ReleaseSHA256, Generation, ControlFence, WriterFence string
	Binding                                              UpdateStartBinding
	SandboxProfile                                       string
}

type UpdateStartLaunchRequest struct {
	CandidateLaunchRequest
	Binding        UpdateStartBinding
	SandboxProfile string
	ReplaceSource  bool
	Source         *UpdateStartSource
}

type UpdateStartLaunchSnapshot struct {
	Launch         CandidateLaunchSnapshot `json:"launch"`
	Binding        UpdateStartBinding      `json:"binding"`
	SandboxProfile string                  `json:"sandbox_profile"`
	SeccompSHA256  string                  `json:"seccomp_sha256"`
}

type UpdateStartRunningSnapshot struct {
	Launch     UpdateStartLaunchSnapshot `json:"launch"`
	NativeID   string                    `json:"native_id"`
	ObservedMS int64                     `json:"observed_ms"`
}

type UpdateStartLaunch struct{ launch CandidateLaunch }

func (UpdateStartLaunch) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (c *UpdateStartLaunch) UnmarshalJSON([]byte) error {
	c.launch = CandidateLaunch{}
	return ErrContainerUncertain
}
func (c UpdateStartLaunch) Close() error { return c.launch.Close() }

type UpdateStartRunningObservation struct {
	snapshot UpdateStartRunningSnapshot
	valid    bool
}

func (UpdateStartRunningObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *UpdateStartRunningObservation) UnmarshalJSON([]byte) error {
	*o = UpdateStartRunningObservation{}
	return ErrContainerUncertain
}
func (o UpdateStartRunningObservation) Snapshot() (UpdateStartRunningSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validUpdateStartSnapshot(s.Launch) || !containerSHA.MatchString(s.NativeID) || s.ObservedMS < 0 {
		return UpdateStartRunningSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}

func validUpdateStartBinding(b UpdateStartBinding) bool {
	for _, id := range []string{b.EffectID, b.ClaimID} {
		if !containerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	if !positiveContainerCounter(b.PolicyRevision) {
		return false
	}
	for _, sha := range []string{b.PayloadSHA256, b.OwnershipSHA256, b.CheckpointReceiptSHA256} {
		if !containerSHA.MatchString(sha) {
			return false
		}
	}
	return true
}

func validUpdateStartSnapshot(s UpdateStartLaunchSnapshot) bool {
	return updateStartSnapshotFailure(s) == ""
}

func updateStartSnapshotFailure(s UpdateStartLaunchSnapshot) string {
	base := s.Launch
	d := base.Data
	switch {
	case !validUpdateStartBinding(s.Binding):
		return "binding"
	case !validContainerOwner(candidateOwner(base.Identity)):
		return "owner"
	case !positiveContainerCounter(base.Identity.Generation) ||
		base.Identity.Generation == "1":
		return "generation"
	case !positiveContainerCounter(base.Identity.WriterFence) ||
		base.Identity.WriterFence == "1":
		return "writer fence"
	case !nativeImageID.MatchString(base.ImageID):
		return "image"
	case base.Platform != "linux/amd64" &&
		base.Platform != "linux/arm64":
		return "platform"
	case !containerSHA.MatchString(base.PolicySHA256) ||
		!containerSHA.MatchString(base.MountsSHA256):
		return "plan digest"
	case s.SandboxProfile != UpdateStartReadOnlySandboxProfile &&
		s.SandboxProfile != UpdateStartWritableSandboxProfile:
		return "sandbox profile"
	case candidateSeccompError != nil ||
		s.SeccompSHA256 != updateStartSeccompSHA(s.SandboxProfile):
		return "seccomp"
	case d.Root.Inode == 0 ||
		!containerUUID.MatchString(d.Root.FilesystemUUID) ||
		d.Root.FilesystemUUID == "00000000-0000-0000-0000-000000000000":
		return "data root"
	case d.ProjectID == 0 || d.NamespaceInode == 0 ||
		d.QuotaBytes <= 0 || d.QuotaBytes%512 != 0 ||
		d.QuotaInodes <= 0:
		return "data quota"
	default:
		return ""
	}
}

func updateStartRequestFailure(r UpdateStartLaunchRequest) string {
	validSource := !r.ReplaceSource && r.Source == nil ||
		r.ReplaceSource && r.Source != nil &&
			validUpdateStartSource(*r.Source, r)
	switch {
	case !validSource:
		return "source"
	case !candidateRequestValid(r.CandidateLaunchRequest):
		return "candidate"
	case !validUpdateStartBinding(r.Binding):
		return "binding"
	case !positiveContainerCounter(r.Identity.Generation) ||
		r.Identity.Generation == "1":
		return "generation"
	case !positiveContainerCounter(r.Identity.WriterFence) ||
		r.Identity.WriterFence == "1":
		return "writer_fence"
	case r.SandboxProfile != UpdateStartReadOnlySandboxProfile &&
		r.SandboxProfile != UpdateStartWritableSandboxProfile:
		return "sandbox_profile"
	case r.Binding.EffectID == r.Identity.ResourceID ||
		r.Binding.EffectID == r.Identity.NamespaceID:
		return "effect_identity"
	default:
		return ""
	}
}

func validUpdateStartRequest(r UpdateStartLaunchRequest) bool {
	return updateStartRequestFailure(r) == ""
}

func validUpdateStartSource(s UpdateStartSource, r UpdateStartLaunchRequest) bool {
	validID := func(id string) bool {
		return containerUUID.MatchString(id) &&
			id != "00000000-0000-0000-0000-000000000000"
	}
	return validID(s.OperationID) && validID(s.ResourceID) &&
		s.ResourceID != r.Identity.ResourceID &&
		containerSHA.MatchString(s.NativeID) &&
		containerSHA.MatchString(s.ReleaseSHA256) &&
		s.ReleaseSHA256 == r.Identity.ReleaseSHA256 &&
		positiveContainerCounter(s.Generation) &&
		s.Generation != "1" &&
		positiveContainerCounter(s.ControlFence) &&
		positiveContainerCounter(s.WriterFence) &&
		s.WriterFence != "1" &&
		validUpdateStartBinding(s.Binding) &&
		(s.SandboxProfile == UpdateStartReadOnlySandboxProfile ||
			s.SandboxProfile == UpdateStartWritableSandboxProfile)
}

func updateStartSeccompSHA(profile string) string {
	if profile == UpdateStartWritableSandboxProfile {
		return DataWriterPolicySHA256()
	}
	return containerHash([]byte(candidateSeccompOption))
}

func UpdateStartSeccompSHA256(profile string) string {
	return updateStartSeccompSHA(profile)
}

func updateStartLabels(owner containerOwner, b UpdateStartBinding, profile string) map[string]string {
	labels := ownerContainerLabels(owner)
	labels[containerLabelPrefix+"format"] = "update-start/1-dev.1"
	labels[containerLabelPrefix+"sandbox-profile"] = profile
	labels[containerLabelPrefix+"start-effect"] = b.EffectID
	labels[containerLabelPrefix+"start-claim"] = b.ClaimID
	labels[containerLabelPrefix+"start-payload"] = b.PayloadSHA256
	labels[containerLabelPrefix+"start-ownership"] = b.OwnershipSHA256
	labels[containerLabelPrefix+"checkpoint-receipt"] = b.CheckpointReceiptSHA256
	labels[containerLabelPrefix+"policy-revision"] = b.PolicyRevision
	return labels
}

// planUpdateStartContainer changes only the reviewed data mount, dynamic
// generation identity, and exact update-start claim labels. Every network,
// resource, syscall, socket, environment and read-only-root constraint from the
// ordinary candidate builder remains unchanged.
func planUpdateStartContainer(raw []byte, image BackendImageInspection, owner containerOwner, roots containerRoots, binding UpdateStartBinding, profile string) (containerPlan, error) {
	if !validUpdateStartBinding(binding) || !positiveContainerCounter(owner.Generation) || owner.Generation == "1" ||
		!positiveContainerCounter(owner.WriterFence) || owner.WriterFence == "1" ||
		(profile != UpdateStartReadOnlySandboxProfile && profile != UpdateStartWritableSandboxProfile) ||
		binding.EffectID == owner.ResourceID || binding.EffectID == owner.NamespaceID {
		return containerPlan{}, ErrInvalidResponse
	}
	plan, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		return containerPlan{}, err
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || len(request.HostConfig.Mounts) != 3 ||
		request.HostConfig.Mounts[0].Target != "/data" || !request.HostConfig.Mounts[0].ReadOnly {
		return containerPlan{}, ErrInvalidResponse
	}
	request.HostConfig.Mounts[0].ReadOnly = profile != UpdateStartWritableSandboxProfile
	request.Labels = updateStartLabels(owner, binding, profile)
	policy, _ := json.Marshal(request)
	request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
	plan.body, err = json.Marshal(request)
	if err != nil {
		return containerPlan{}, ErrInvalidResponse
	}
	plan.bodySHA = containerHash(plan.body)
	copied := binding
	plan.updateStart = &copied
	plan.profile = profile
	if !validContainerPlan(plan) {
		return containerPlan{}, ErrInvalidResponse
	}
	return plan, nil
}
