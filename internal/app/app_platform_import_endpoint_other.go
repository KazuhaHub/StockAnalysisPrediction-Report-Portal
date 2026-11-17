//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The runner's import-checkpoint actions need the Linux lifecycle transport. Elsewhere the endpoint
// refuses, so a Portal can never report an import it did not perform.
func platformExecuteImportCheckpoint(context.Context, platformRuntimeEndpoint, platformImportIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}

func platformReadImportCheckpointReceipt(context.Context, platformRuntimeEndpoint, platformImportIntent) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{}, errAppPlatformRuntimeConflict
}
