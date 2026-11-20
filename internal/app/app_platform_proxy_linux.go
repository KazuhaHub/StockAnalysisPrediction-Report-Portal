//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryProxy(ctx context.Context, e platformRuntimeEndpoint) (transport.ProxyProfile, error) {
	return transport.QueryControllerProxy(ctx, e.path, e.controllerUID, e.controllerGID)
}
