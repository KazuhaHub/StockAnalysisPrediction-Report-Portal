//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadNetworkRuntime(context.Context, platformRuntimeEndpoint) (transport.NetworkRuntimeObservation, error) {
	return transport.NetworkRuntimeObservation{}, errAppPlatformRuntimeConflict
}
