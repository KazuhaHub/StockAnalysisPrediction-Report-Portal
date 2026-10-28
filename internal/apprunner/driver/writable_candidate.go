package driver

import "encoding/json"

const WritableCandidateSandboxProfile = "linux-docker-rootful-projectquota-writable/v1-dev"

// Binding comes only from current durably claimed journal authority. Native
// shape validation is not signature verification or admission by itself.
type WritableCandidateBinding struct {
	EffectID, ClaimID, PayloadSHA256, OwnershipSHA256, DataReceiptSHA256, WriterReceiptSHA256 string
}
type WritableCandidateLaunchRequest struct {
	CandidateLaunchRequest
	Binding WritableCandidateBinding
}
type WritableCandidateLaunchSnapshot struct {
	Launch         CandidateLaunchSnapshot  `json:"launch"`
	Binding        WritableCandidateBinding `json:"binding"`
	SandboxProfile string                   `json:"sandbox_profile"`
	SeccompSHA256  string                   `json:"seccomp_sha256"`
}
type WritableCandidateRunningSnapshot struct {
	Launch     WritableCandidateLaunchSnapshot `json:"launch"`
	NativeID   string                          `json:"native_id"`
	ObservedMS int64                           `json:"observed_ms"`
}

type WritableCandidateLaunch struct{ launch CandidateLaunch }

func (WritableCandidateLaunch) MarshalJSON() ([]byte, error) { return nil, ErrContainerUncertain }
func (c *WritableCandidateLaunch) UnmarshalJSON([]byte) error {
	c.launch = CandidateLaunch{}
	return ErrContainerUncertain
}
func (c WritableCandidateLaunch) Close() error { return c.launch.Close() }

type WritableCandidateRunningObservation struct {
	snapshot WritableCandidateRunningSnapshot
	valid    bool
}

func (WritableCandidateRunningObservation) MarshalJSON() ([]byte, error) {
	return nil, ErrContainerUncertain
}
func (o *WritableCandidateRunningObservation) UnmarshalJSON([]byte) error {
	*o = WritableCandidateRunningObservation{}
	return ErrContainerUncertain
}
func (o WritableCandidateRunningObservation) Snapshot() (WritableCandidateRunningSnapshot, error) {
	s := o.snapshot
	if !o.valid || !validWritableCandidateSnapshot(s.Launch) || !containerSHA.MatchString(s.NativeID) || s.ObservedMS < 0 {
		return WritableCandidateRunningSnapshot{}, ErrContainerUncertain
	}
	return s, nil
}
func validWritableCandidateBinding(b WritableCandidateBinding) bool {
	for _, id := range []string{b.EffectID, b.ClaimID} {
		if !containerUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, sha := range []string{b.PayloadSHA256, b.OwnershipSHA256, b.DataReceiptSHA256, b.WriterReceiptSHA256} {
		if !containerSHA.MatchString(sha) {
			return false
		}
	}
	return true
}
func validWritableCandidateSnapshot(s WritableCandidateLaunchSnapshot) bool {
	base := s.Launch
	d := base.Data
	return validWritableCandidateBinding(s.Binding) && validContainerOwner(candidateOwner(base.Identity)) && base.Identity.Generation == "1" && base.Identity.WriterFence == "1" && nativeImageID.MatchString(base.ImageID) && (base.Platform == "linux/amd64" || base.Platform == "linux/arm64") && containerSHA.MatchString(base.PolicySHA256) && containerSHA.MatchString(base.MountsSHA256) && s.SandboxProfile == WritableCandidateSandboxProfile && candidateSeccompError == nil && s.SeccompSHA256 == DataWriterPolicySHA256() && d.Root.Inode > 0 && containerUUID.MatchString(d.Root.FilesystemUUID) && d.Root.FilesystemUUID != "00000000-0000-0000-0000-000000000000" && d.ProjectID > 0 && d.NamespaceInode > 0 && d.QuotaBytes > 0 && d.QuotaBytes%512 == 0 && d.QuotaInodes > 0
}
func writableCandidateLabels(owner containerOwner, b WritableCandidateBinding) map[string]string {
	labels := ownerContainerLabels(owner)
	labels[containerLabelPrefix+"format"] = "writable-candidate/1-dev.1"
	labels[containerLabelPrefix+"sandbox-profile"] = WritableCandidateSandboxProfile
	labels[containerLabelPrefix+"start-effect"] = b.EffectID
	labels[containerLabelPrefix+"start-claim"] = b.ClaimID
	labels[containerLabelPrefix+"start-payload"] = b.PayloadSHA256
	labels[containerLabelPrefix+"start-ownership"] = b.OwnershipSHA256
	labels[containerLabelPrefix+"data-receipt"] = b.DataReceiptSHA256
	labels[containerLabelPrefix+"writer-receipt"] = b.WriterReceiptSHA256
	return labels
}

// A separate closed builder changes only reviewed data access and exact claim
// labels. Callers cannot supply an environment, mount, UID or Docker option.
func planWritableCandidateContainer(raw []byte, image BackendImageInspection, owner containerOwner, roots containerRoots, binding WritableCandidateBinding) (containerPlan, error) {
	if !validWritableCandidateBinding(binding) || owner.Generation != "1" || owner.WriterFence != "1" || binding.EffectID == owner.ResourceID || binding.EffectID == owner.NamespaceID {
		return containerPlan{}, ErrInvalidResponse
	}
	plan, err := planCandidateContainer(raw, image, owner, roots)
	if err != nil {
		return containerPlan{}, err
	}
	var request containerCreate
	if json.Unmarshal(plan.body, &request) != nil || len(request.HostConfig.Mounts) != 3 || request.HostConfig.Mounts[0].Target != "/data" || !request.HostConfig.Mounts[0].ReadOnly {
		return containerPlan{}, ErrInvalidResponse
	}
	request.HostConfig.Mounts[0].ReadOnly = false
	request.Labels = writableCandidateLabels(owner, binding)
	policy, _ := json.Marshal(request)
	request.Labels[containerLabelPrefix+"policy-sha256"] = containerHash(policy)
	plan.body, err = json.Marshal(request)
	if err != nil {
		return containerPlan{}, ErrInvalidResponse
	}
	plan.bodySHA = containerHash(plan.body)
	copied := binding
	plan.writable = &copied
	if !validContainerPlan(plan) {
		return containerPlan{}, ErrInvalidResponse
	}
	return plan, nil
}
