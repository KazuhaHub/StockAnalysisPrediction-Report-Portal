//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRequestNetwork(ctx context.Context, e platformRuntimeEndpoint, raw, signature, body []byte, a control.Authority, approve transport.NetworkApproval) (transport.NetworkObservation, error) {
	return transport.RequestControllerNetwork(ctx, e.path, e.controllerUID, e.controllerGID, raw, signature, body, a, approve)
}
