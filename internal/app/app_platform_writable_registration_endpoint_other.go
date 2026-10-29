//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunWritableRegistration(context.Context, platformRuntimeEndpoint, platformWritableRegistrationIntent, transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
