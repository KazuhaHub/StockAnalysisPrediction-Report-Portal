//go:build !linux

package driver

import "context"

func (*CandidateRuntime) DeliverUpdateStartRegistration(context.Context, UpdateStartLaunchRequest, UpdateStartLaunchSnapshot, string, string, []byte) (UpdateStartRegistrationObservation, error) {
	return UpdateStartRegistrationObservation{}, ErrUnsupported
}

func (*CandidateRuntime) ObserveUpdateStartRegistration(context.Context, UpdateStartLaunchRequest, UpdateStartLaunchSnapshot, string, string, []byte) (UpdateStartRegistrationObservation, error) {
	return UpdateStartRegistrationObservation{}, ErrUnsupported
}
