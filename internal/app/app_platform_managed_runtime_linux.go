//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadManagedRuntime(ctx context.Context, e platformRuntimeEndpoint, a control.Authority) (transport.ManagedRuntimeObservation, error) {
	return transport.ObserveControllerManagedRuntime(ctx, e.path, e.controllerUID, e.controllerGID, a)
}
