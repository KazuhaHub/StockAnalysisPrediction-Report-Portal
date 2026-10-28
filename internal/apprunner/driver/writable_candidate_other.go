//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ProvisionWritableCandidateSockets(context.Context, WritableCandidateLaunchRequest) error {
	return ErrUnsupported
}
func (*CandidateRuntime) PrepareWritableCandidate(context.Context, WritableCandidateLaunchRequest) (WritableCandidateLaunch, error) {
	return WritableCandidateLaunch{}, ErrUnsupported
}
func (WritableCandidateLaunch) Snapshot() (WritableCandidateLaunchSnapshot, error) {
	return WritableCandidateLaunchSnapshot{}, ErrUnsupported
}
func (*CandidateRuntime) StartWritableCandidate(context.Context, WritableCandidateLaunch) (WritableCandidateRunningObservation, error) {
	return WritableCandidateRunningObservation{}, ErrUnsupported
}
func (*CandidateRuntime) ObserveWritableCandidate(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot) (WritableCandidateRecoveryObservation, error) {
	return WritableCandidateRecoveryObservation{}, ErrUnsupported
}
func (*CandidateRuntime) CheckWritableCandidateReadiness(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot, string, string) (WritableCandidateReadinessObservation, error) {
	return WritableCandidateReadinessObservation{}, ErrUnsupported
}
