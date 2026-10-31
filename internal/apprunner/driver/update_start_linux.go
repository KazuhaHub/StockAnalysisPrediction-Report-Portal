//go:build linux

package driver

import (
	"bytes"
	"context"
	"time"
)

func (r *CandidateRuntime) ProvisionUpdateStartSockets(ctx context.Context, request UpdateStartLaunchRequest) error {
	request.Descriptor = bytes.Clone(request.Descriptor)
	if !validUpdateStartRequest(request) {
		return ErrInvalidResponse
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := request.Binding
	return r.provisionCandidateSocketsProfile(ctx, request.CandidateLaunchRequest, nil, &binding, request.SandboxProfile)
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
		return zero, ErrContainerUncertain
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.plan.updateStart == nil || state.plan.writable != nil {
		return zero, ErrContainerUncertain
	}
	snapshot := UpdateStartLaunchSnapshot{
		Launch: state.snapshot, Binding: *state.plan.updateStart,
		SandboxProfile: state.plan.profile,
		SeccompSHA256:  updateStartSeccompSHA(state.plan.profile),
	}
	if !validUpdateStartSnapshot(snapshot) {
		return zero, ErrContainerUncertain
	}
	return snapshot, nil
}

func (r *CandidateRuntime) StartUpdateStart(ctx context.Context, launch UpdateStartLaunch) (UpdateStartRunningObservation, error) {
	var zero UpdateStartRunningObservation
	snapshot, err := launch.Snapshot()
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	binding := snapshot.Binding
	observed, err := r.startCandidateProfileAccess(ctx, launch.launch, nil, &binding)
	if err != nil {
		return zero, err
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Launch != snapshot.Launch {
		return zero, ErrContainerUncertain
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
	return r.changeCandidateLifecycle(ctx, launch.launch, nativeID, true)
}
