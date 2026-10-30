//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadNetworkRuntime(ctx context.Context, e platformRuntimeEndpoint) (transport.NetworkRuntimeObservation, error) {
	return transport.ObserveControllerNetwork(ctx, e.path, e.controllerUID, e.controllerGID)
}
