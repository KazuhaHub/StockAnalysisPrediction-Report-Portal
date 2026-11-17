//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ForwardWritableCandidateRequest(context.Context, WritableCandidateLaunchRequest, WritableBackendRegistrationSnapshot, string, string, []byte, string) (WritableBackendRequestObservation, error) {
	return WritableBackendRequestObservation{}, ErrUnsupported
}
