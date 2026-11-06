//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformDeleteApplicationData(
	context.Context, platformRuntimeEndpoint, platformDeleteDataIntent,
) (transport.WritableLifecycleObservation, error) {
	return transport.WritableLifecycleObservation{},
		errAppPlatformRuntimeConflict
}
