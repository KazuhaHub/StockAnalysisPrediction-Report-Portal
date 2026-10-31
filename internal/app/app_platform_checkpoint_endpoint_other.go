//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformCreateCheckpoint(context.Context, platformRuntimeEndpoint, platformCheckpointIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadCheckpointReceipt(context.Context, platformRuntimeEndpoint, platformCheckpointIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
