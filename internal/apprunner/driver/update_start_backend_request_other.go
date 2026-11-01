//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ForwardUpdateStartCandidateRequest(context.Context, UpdateStartLaunchRequest, UpdateStartLaunchSnapshot, UpdateStartRegistrationSnapshot, []byte, string, string, []byte, string) (UpdateStartBackendRequestObservation, error) {
	return UpdateStartBackendRequestObservation{}, ErrUnsupported
}
