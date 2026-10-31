//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReserveUpdateStart(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadUpdateStartReservation(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformExecuteUpdateStart(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadUpdateStartReceipt(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
