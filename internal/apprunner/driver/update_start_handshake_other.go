//go:build !linux

package driver

import "context"

func (*CandidateRuntime) ExchangeUpdateStartBackendHandshake(context.Context, UpdateStartLaunchRequest, UpdateStartRegistrationSnapshot, string) (UpdateStartBackendHandshakeObservation, error) {
	return UpdateStartBackendHandshakeObservation{}, ErrUnsupported
}
