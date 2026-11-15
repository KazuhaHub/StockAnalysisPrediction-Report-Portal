//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ForwardWritableCandidateServiceTask(context.Context, WritableCandidateLaunchRequest, WritableBackendRegistrationSnapshot, []byte, []byte, string) (WritableServiceTaskObservation, error) {
	return WritableServiceTaskObservation{}, ErrUnsupported
}
