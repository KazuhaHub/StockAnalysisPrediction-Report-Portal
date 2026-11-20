//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryMachineSupport(ctx context.Context, e platformRuntimeEndpoint) (transport.MachineSupport, error) {
	return transport.QueryControllerMachineSupport(ctx, e.path, e.controllerUID, e.controllerGID)
}
