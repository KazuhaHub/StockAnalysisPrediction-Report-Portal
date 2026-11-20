//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryPostgresProbe(ctx context.Context, e platformRuntimeEndpoint) (transport.PostgresProbeProfile, error) {
	return transport.QueryControllerPostgresProbe(ctx, e.path, e.controllerUID, e.controllerGID)
}
