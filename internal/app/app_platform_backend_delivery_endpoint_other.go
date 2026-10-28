//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformDeliverBackendRegistration(context.Context, platformRuntimeEndpoint, platformBackendDeliveryIntent) (transport.BackendRegistrationObservation, error) {
	return transport.BackendRegistrationObservation{}, errAppPlatformRuntimeConflict
}

func platformHandshakeBackend(context.Context, platformRuntimeEndpoint, platformBackendDeliveryIntent, string) (transport.BackendHandshakeObservation, error) {
	return transport.BackendHandshakeObservation{}, errAppPlatformRuntimeConflict
}
