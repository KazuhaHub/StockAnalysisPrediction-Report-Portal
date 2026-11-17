//go:build !linux

package driver

import "context"

func (*CandidateRuntime) BindServiceBroker(context.Context, CandidateLaunch, BackendRegistrationSnapshot, bool) (ServiceBroker, error) {
	return ServiceBroker{}, ErrUnsupported
}

func (*CandidateRuntime) BindServiceBrokerUpdateStart(context.Context, UpdateStartLaunchRequest, UpdateStartLaunchSnapshot, UpdateStartRegistrationSnapshot, bool) (ServiceBroker, error) {
	return ServiceBroker{}, ErrUnsupported
}

func (*CandidateRuntime) BindServiceBrokerWritable(context.Context, WritableCandidateLaunchRequest, WritableBackendRegistrationSnapshot, bool) (ServiceBroker, error) {
	return ServiceBroker{}, ErrUnsupported
}
