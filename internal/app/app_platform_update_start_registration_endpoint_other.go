//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunUpdateStartRegistration(context.Context, platformRuntimeEndpoint, platformUpdateStartRegistrationIntent, transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
