//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformHandshakeUpdateStartBackend(context.Context, platformRuntimeEndpoint, platformUpdateStartRegistrationIntent, string) (transport.UpdateStartBackendHandshakeObservation, error) {
	return transport.UpdateStartBackendHandshakeObservation{}, errAppPlatformRuntimeConflict
}
