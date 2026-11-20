//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryNetworkTargets(ctx context.Context, e platformRuntimeEndpoint) (transport.NetworkTargets, error) {
	return transport.QueryControllerNetworkTargets(ctx, e.path, e.controllerUID, e.controllerGID)
}
