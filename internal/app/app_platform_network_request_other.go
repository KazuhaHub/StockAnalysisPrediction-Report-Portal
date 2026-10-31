//go:build !linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRequestNetwork(context.Context, platformRuntimeEndpoint, []byte, []byte, []byte, control.Authority, transport.NetworkApproval) (transport.NetworkObservation, error) {
	return transport.NetworkObservation{}, errAppPlatformNetworkUnavailable
}
