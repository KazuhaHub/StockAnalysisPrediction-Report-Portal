//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformQueryNetworkTargets(context.Context, platformRuntimeEndpoint) (transport.NetworkTargets, error) {
	return transport.NetworkTargets{}, transport.ErrNetworkTargetsUnsupported
}
