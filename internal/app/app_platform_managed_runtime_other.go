//go:build !linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReadManagedRuntime(context.Context, platformRuntimeEndpoint, control.Authority) (transport.ManagedRuntimeObservation, error) {
	return transport.ManagedRuntimeObservation{}, errAppPlatformRuntimeConflict
}
