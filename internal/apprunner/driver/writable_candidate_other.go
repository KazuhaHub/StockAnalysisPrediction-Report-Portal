//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ProvisionWritableCandidateSockets(context.Context, WritableCandidateLaunchRequest) error {
	return ErrUnsupported
}
func (*CandidateRuntime) PrepareWritableCandidate(context.Context, WritableCandidateLaunchRequest) (WritableCandidateLaunch, error) {
	return WritableCandidateLaunch{}, ErrUnsupported
}
func (*CandidateRuntime) PrepareWritableCandidateRemoval(context.Context, WritableCandidateLaunchRequest) (WritableCandidateLaunch, error) {
	return WritableCandidateLaunch{}, ErrUnsupported
}
func (WritableCandidateLaunch) Snapshot() (WritableCandidateLaunchSnapshot, error) {
	return WritableCandidateLaunchSnapshot{}, ErrUnsupported
}
func (*CandidateRuntime) StartWritableCandidate(context.Context, WritableCandidateLaunch) (WritableCandidateRunningObservation, error) {
	return WritableCandidateRunningObservation{}, ErrUnsupported
}
func (*CandidateRuntime) StopWritableCandidate(context.Context, WritableCandidateLaunch, string) (CandidateRemovalObservation, error) {
	return CandidateRemovalObservation{}, ErrUnsupported
}
func (*CandidateRuntime) RemoveWritableCandidate(context.Context, WritableCandidateLaunch, string) (CandidateRemovalObservation, error) {
	return CandidateRemovalObservation{}, ErrUnsupported
}
func (*CandidateRuntime) ObserveWritableCandidate(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot) (WritableCandidateRecoveryObservation, error) {
	return WritableCandidateRecoveryObservation{}, ErrUnsupported
}
func (*CandidateRuntime) CheckWritableCandidateReadiness(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot, string, string) (WritableCandidateReadinessObservation, error) {
	return WritableCandidateReadinessObservation{}, ErrUnsupported
}

func (*CandidateRuntime) ProvisionUpdateStartSockets(context.Context, UpdateStartLaunchRequest) error {
	return ErrUnsupported
}
func (*CandidateRuntime) PrepareUpdateStart(context.Context, UpdateStartLaunchRequest) (UpdateStartLaunch, error) {
	return UpdateStartLaunch{}, ErrUnsupported
}
func (*CandidateRuntime) PrepareUpdateStartRemoval(context.Context, UpdateStartLaunchRequest) (UpdateStartLaunch, error) {
	return UpdateStartLaunch{}, ErrUnsupported
}
func (UpdateStartLaunch) Snapshot() (UpdateStartLaunchSnapshot, error) {
	return UpdateStartLaunchSnapshot{}, ErrUnsupported
}
func (*CandidateRuntime) StartUpdateStart(context.Context, UpdateStartLaunch) (UpdateStartRunningObservation, error) {
	return UpdateStartRunningObservation{}, ErrUnsupported
}
func (*CandidateRuntime) StopUpdateStart(context.Context, UpdateStartLaunch, string) (CandidateRemovalObservation, error) {
	return CandidateRemovalObservation{}, ErrUnsupported
}
func (*CandidateRuntime) RemoveUpdateStart(context.Context, UpdateStartLaunch, string) (CandidateRemovalObservation, error) {
	return CandidateRemovalObservation{}, ErrUnsupported
}
