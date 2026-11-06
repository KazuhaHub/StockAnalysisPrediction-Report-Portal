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

func platformProbeUpdateStart(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

// The runner's start read-receipt needs the Linux lifecycle transport. Elsewhere the endpoint refuses, so a
// Portal can never settle a start it did not read.
func platformReadUpdateStartReceipt(context.Context, platformRuntimeEndpoint, platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
	return nil, errAppPlatformRuntimeConflict
}
