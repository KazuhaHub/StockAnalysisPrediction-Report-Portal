//go:build linux

package driver

import (
	"bytes"
	"context"
	"path/filepath"
	"time"
)

func validWritableCandidateRequest(r WritableCandidateLaunchRequest) bool {
	return candidateRequestValid(r.CandidateLaunchRequest) && validWritableCandidateBinding(r.Binding) && r.Identity.Generation == "1" && r.Identity.WriterFence == "1" && r.Binding.EffectID != r.Identity.ResourceID && r.Binding.EffectID != r.Identity.NamespaceID
}
func (r *CandidateRuntime) checkCandidateDataProfile(ctx context.Context, i CandidateIdentity, d CandidateDataEvidence, writable bool) error {
	if !writable {
		return r.checkData(ctx, i, d)
	}
	if d.Root != r.rootIdentity {
		return ErrUnsafeSocket
	}
	observed, err := r.root.InspectWriteAccess(ctx, d.Root, i.NamespaceID, d.ProjectID)
	if err != nil {
		return err
	}
	n := observed.Namespace
	if n.NamespaceID != i.NamespaceID || n.ProjectID != d.ProjectID || n.Device != d.Device || n.Inode != d.NamespaceInode || n.ByteLimit != d.QuotaBytes || n.InodeLimit != d.QuotaInodes || !n.Inherited || n.UsedBytes < 0 || n.UsedBytes > n.ByteLimit || n.UsedInodes < 0 || n.UsedInodes > n.InodeLimit {
		return ErrUnsafeSocket
	}
	return ctx.Err()
}

// Caller holds r.mu. The current handle can be exempt only at its own first
// start; every actual Docker mount reference (including stopped ones) conflicts.
func (r *CandidateRuntime) requireWritableCandidateExclusion(ctx context.Context, request CandidateLaunchRequest, own *candidateLaunchState) error {
	for other := range r.launches {
		if other != own && other.snapshot.Identity.NamespaceID == request.Identity.NamespaceID {
			return ErrContainerConflict
		}
	}
	client, closeClient := r.client()
	defer closeClient()
	return requireNoContainerMountReferences(ctx, client, filepath.Join(r.roots.Data, request.Identity.NamespaceID))
}

func (r *CandidateRuntime) ProvisionWritableCandidateSockets(ctx context.Context, request WritableCandidateLaunchRequest) error {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validWritableCandidateRequest(request) {
		return ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	return r.provisionCandidateSocketsProfile(ctx, request.CandidateLaunchRequest, &binding)
}

// PrepareWritableCandidate only pins native facts. Caller must persist its
// exact snapshot under the current one-shot journal claim before Start.
func (r *CandidateRuntime) PrepareWritableCandidate(ctx context.Context, request WritableCandidateLaunchRequest) (WritableCandidateLaunch, error) {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validWritableCandidateRequest(request) {
		return WritableCandidateLaunch{}, ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	launch, err := r.prepareCandidateProfile(ctx, request.CandidateLaunchRequest, &binding)
	if err != nil {
		return WritableCandidateLaunch{}, err
	}
	result := WritableCandidateLaunch{launch: launch}
	if _, err := result.Snapshot(); err != nil {
		launch.Close()
		return WritableCandidateLaunch{}, err
	}
	return result, nil
}
func (r *CandidateRuntime) PrepareWritableCandidateRemoval(ctx context.Context, request WritableCandidateLaunchRequest) (WritableCandidateLaunch, error) {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validWritableCandidateRequest(request) {
		return WritableCandidateLaunch{}, ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	launch, err := r.prepareCandidateProfileMode(ctx, request.CandidateLaunchRequest, &binding, false)
	if err != nil {
		return WritableCandidateLaunch{}, err
	}
	result := WritableCandidateLaunch{launch: launch}
	if _, err := result.Snapshot(); err != nil {
		launch.Close()
		return WritableCandidateLaunch{}, err
	}
	return result, nil
}
func (c WritableCandidateLaunch) Snapshot() (WritableCandidateLaunchSnapshot, error) {
	var zero WritableCandidateLaunchSnapshot
	state := c.launch.state
	if state == nil {
		return zero, ErrContainerUncertain
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.plan.writable == nil {
		return zero, ErrContainerUncertain
	}
	snapshot := WritableCandidateLaunchSnapshot{Launch: state.snapshot, Binding: *state.plan.writable, SandboxProfile: WritableCandidateSandboxProfile, SeccompSHA256: DataWriterPolicySHA256()}
	if !validWritableCandidateSnapshot(snapshot) {
		return zero, ErrContainerUncertain
	}
	return snapshot, nil
}

func (r *CandidateRuntime) StopWritableCandidate(ctx context.Context, launch WritableCandidateLaunch, nativeID string) (CandidateRemovalObservation, error) {
	return r.changeCandidateLifecycle(ctx, launch.launch, nativeID, false)
}

func (r *CandidateRuntime) RemoveWritableCandidate(ctx context.Context, launch WritableCandidateLaunch, nativeID string) (CandidateRemovalObservation, error) {
	return r.changeCandidateLifecycle(ctx, launch.launch, nativeID, true)
}

// StartWritableCandidate never retries, adopts a container or changes data
// permissions. It consumes only this runtime's live pinned writable handle.
// Every possibly effectful failure stays uncertain. A running observation is
// not application readiness, registration, activation or writer revocation.
func (r *CandidateRuntime) StartWritableCandidate(ctx context.Context, launch WritableCandidateLaunch) (WritableCandidateRunningObservation, error) {
	var zero WritableCandidateRunningObservation
	snapshot, err := launch.Snapshot()
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := snapshot.Binding
	observed, err := r.startCandidateProfile(ctx, launch.launch, &binding)
	if err != nil {
		return zero, err
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Launch != snapshot.Launch {
		return zero, ErrContainerUncertain
	}
	result := WritableCandidateRunningObservation{valid: true, snapshot: WritableCandidateRunningSnapshot{Launch: snapshot, NativeID: actual.NativeID, ObservedMS: actual.ObservedMS}}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}

// Owned disposable cleanup only, not a public lifecycle/stop implementation.
func (r *CandidateRuntime) cleanupWritableCandidate(ctx context.Context, launch WritableCandidateLaunch, observation WritableCandidateRunningObservation) error {
	actual, err := observation.Snapshot()
	if err != nil {
		return err
	}
	snapshot, err := launch.Snapshot()
	if err != nil || snapshot != actual.Launch {
		return ErrContainerConflict
	}
	return r.cleanupCandidate(ctx, launch.launch, CandidateRunningObservation{valid: true, snapshot: CandidateRunningSnapshot{Launch: actual.Launch.Launch, NativeID: actual.NativeID, ObservedMS: actual.ObservedMS}})
}
