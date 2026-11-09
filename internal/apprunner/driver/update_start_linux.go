//go:build linux

package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

func (r *CandidateRuntime) ProvisionUpdateStartSockets(ctx context.Context, request UpdateStartLaunchRequest) error {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validUpdateStartRequest(request) {
		return ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if request.ReplaceSource {
		if err := r.removeStoppedUpdateStartSource(ctx, request); err != nil {
			return err
		}
	}
	binding := request.Binding
	return r.provisionCandidateSocketsProfile(ctx, request.CandidateLaunchRequest, nil, &binding, request.SandboxProfile)
}

func (r *CandidateRuntime) removeStoppedUpdateStartSource(ctx context.Context, request UpdateStartLaunchRequest) error {
	if request.Source == nil {
		return ErrInvalidResponse
	}
	r.mu.Lock()
	var source *candidateLaunchState
	for state := range r.launches {
		state.mu.Lock()
		matches := !state.closed &&
			state.snapshot.Identity.InstallationID == request.Identity.InstallationID &&
			state.snapshot.Identity.ResourceID == request.Source.ResourceID &&
			state.snapshot.Identity.NamespaceID == request.Identity.NamespaceID
		state.mu.Unlock()
		if matches {
			source = state
			break
		}
	}
	r.mu.Unlock()
	image, err := InspectBackendImage(ctx, r.socket, request.Descriptor, request.Source.ReleaseSHA256, r.imagePolicy)
	if err != nil {
		return err
	}
	owner := containerOwner{
		DeploymentID:    request.Identity.DeploymentID,
		ControllerEpoch: request.Identity.ControllerEpoch,
		InstallationID:  request.Identity.InstallationID,
		OperationID:     request.Source.OperationID,
		ResourceID:      request.Source.ResourceID,
		NamespaceID:     request.Identity.NamespaceID,
		ReleaseSHA256:   request.Source.ReleaseSHA256,
		Generation:      request.Source.Generation,
		ControlFence:    request.Source.ControlFence,
		WriterFence:     request.Source.WriterFence,
	}
	plan, err := planUpdateStartContainer(
		request.Descriptor, image, owner, r.roots, request.Source.Binding,
		request.Source.SandboxProfile)
	if err != nil {
		return err
	}
	client, closeClient := r.client()
	defer closeClient()
	if err := removeCandidateContainerHTTP(
		ctx, client, request.Source.NativeID, plan); err != nil {
		return err
	}
	if source != nil {
		launch := UpdateStartLaunch{
			launch: CandidateLaunch{state: source},
		}
		if err := launch.Close(); err != nil {
			return err
		}
	}
	return nil
}

// PrepareUpdateStart pins current native facts without changing data ownership
// or starting a process. The journal claim must persist the resulting snapshot
// before StartUpdateStart can consume it.
func (r *CandidateRuntime) PrepareUpdateStart(ctx context.Context, request UpdateStartLaunchRequest) (UpdateStartLaunch, error) {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validUpdateStartRequest(request) {
		return UpdateStartLaunch{}, ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	launch, err := r.prepareCandidateProfileAccess(ctx, request.CandidateLaunchRequest, nil, &binding, request.SandboxProfile, true)
	if err != nil {
		return UpdateStartLaunch{}, err
	}
	result := UpdateStartLaunch{launch: launch}
	if _, err := result.Snapshot(); err != nil {
		launch.Close()
		return UpdateStartLaunch{}, err
	}
	return result, nil
}

func (r *CandidateRuntime) PrepareUpdateStartRemoval(ctx context.Context, request UpdateStartLaunchRequest) (UpdateStartLaunch, error) {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validUpdateStartRequest(request) {
		return UpdateStartLaunch{}, ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	launch, err := r.prepareCandidateProfileAccess(ctx, request.CandidateLaunchRequest, nil, &binding, request.SandboxProfile, false)
	if err != nil {
		return UpdateStartLaunch{}, err
	}
	result := UpdateStartLaunch{launch: launch}
	if _, err := result.Snapshot(); err != nil {
		launch.Close()
		return UpdateStartLaunch{}, err
	}
	return result, nil
}

func (c UpdateStartLaunch) Snapshot() (UpdateStartLaunchSnapshot, error) {
	var zero UpdateStartLaunchSnapshot
	state := c.launch.state
	if state == nil {
		return zero, fmt.Errorf(
			"%w: update-start launch state", ErrContainerUncertain)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.plan.updateStart == nil || state.plan.writable != nil {
		return zero, fmt.Errorf(
			"%w: update-start launch profile", ErrContainerUncertain)
	}
	snapshot := UpdateStartLaunchSnapshot{
		Launch: state.snapshot, Binding: *state.plan.updateStart,
		SandboxProfile: state.plan.profile,
		SeccompSHA256:  updateStartSeccompSHA(state.plan.profile),
	}
	if reason := updateStartSnapshotFailure(snapshot); reason != "" {
		return zero, fmt.Errorf(
			"%w: update-start snapshot %s",
			ErrContainerUncertain, reason)
	}
	return snapshot, nil
}

func (r *CandidateRuntime) StartUpdateStart(ctx context.Context, launch UpdateStartLaunch) (UpdateStartRunningObservation, error) {
	var zero UpdateStartRunningObservation
	snapshot, err := launch.Snapshot()
	if err != nil {
		return zero, fmt.Errorf(
			"update-start start snapshot: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := snapshot.Binding
	observed, err := r.startCandidateProfileAccess(ctx, launch.launch, nil, &binding)
	if err != nil {
		return zero, fmt.Errorf(
			"update-start native start: %w", err)
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Launch != snapshot.Launch {
		return zero, fmt.Errorf(
			"%w: update-start native identity",
			ErrContainerUncertain)
	}
	result := UpdateStartRunningObservation{
		valid: true,
		snapshot: UpdateStartRunningSnapshot{
			Launch: snapshot, NativeID: actual.NativeID, ObservedMS: actual.ObservedMS,
		},
	}
	if _, err := result.Snapshot(); err != nil {
		return zero, err
	}
	return result, nil
}

func (r *CandidateRuntime) StopUpdateStart(ctx context.Context, launch UpdateStartLaunch, nativeID string) (CandidateRemovalObservation, error) {
	return r.changeCandidateLifecycle(ctx, launch.launch, nativeID, false)
}

func (r *CandidateRuntime) RemoveUpdateStart(ctx context.Context, launch UpdateStartLaunch, nativeID string) (CandidateRemovalObservation, error) {
	observed, err := r.changeCandidateLifecycle(
		ctx, launch.launch, nativeID, true)
	if err != nil {
		return observed, err
	}
	snapshot, err := launch.Snapshot()
	if err != nil {
		return CandidateRemovalObservation{}, err
	}
	identity := snapshot.Launch.Identity
	if err := launch.Close(); err != nil {
		return CandidateRemovalObservation{}, errors.Join(
			ErrContainerUncertain, err)
	}
	releaseCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.ReleaseCandidateHTTPStorage(
		releaseCtx, identity); err != nil {
		return CandidateRemovalObservation{}, err
	}
	return observed, nil
}
