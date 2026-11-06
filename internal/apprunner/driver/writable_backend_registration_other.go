//go:build !linux

package driver

import "context"

func (*CandidateRuntime) DeliverWritableBackendRegistration(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot, string, string, []byte) (WritableBackendRegistrationObservation, error) {
	return WritableBackendRegistrationObservation{}, ErrUnsupported
}
func (*CandidateRuntime) ObserveWritableBackendRegistration(context.Context, WritableCandidateLaunchRequest, WritableCandidateLaunchSnapshot, string, string, []byte) (WritableBackendRegistrationObservation, error) {
	return WritableBackendRegistrationObservation{}, ErrUnsupported
}
