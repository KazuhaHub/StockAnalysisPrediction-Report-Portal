//go:build !linux

package driver

import "context"

type ServiceTaskSnapshot struct {
	Binding               BackendRegistrationSnapshot
	BodySHA256            string
	ServiceIdentitySHA256 string
	Response              BackendResponse
	ObservedMS            int64
}

type ServiceTaskObservation struct{}

func (ServiceTaskObservation) Snapshot() (ServiceTaskSnapshot, error) {
	return ServiceTaskSnapshot{}, ErrUnsupported
}

func (*CandidateRuntime) ForwardServiceTask(
	context.Context,
	CandidateLaunch,
	BackendRegistrationSnapshot,
	[]byte,
	[]byte,
	string,
) (ServiceTaskObservation, error) {
	return ServiceTaskObservation{}, ErrUnsupported
}
