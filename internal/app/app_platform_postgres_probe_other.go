//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryPostgresProbe(context.Context, platformRuntimeEndpoint) (transport.PostgresProbeProfile, error) {
	return transport.PostgresProbeProfile{}, transport.ErrPostgresProbeUnsupported
}
