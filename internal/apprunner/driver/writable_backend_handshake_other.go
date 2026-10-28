//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ExchangeWritableBackendHandshake(context.Context, WritableCandidateLaunchRequest, WritableBackendRegistrationSnapshot, string) (WritableBackendHandshakeObservation, error) {
	return WritableBackendHandshakeObservation{}, ErrUnsupported
}
