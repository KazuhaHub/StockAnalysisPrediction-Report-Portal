//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformObserveRuntimeEndpoint(ctx context.Context, endpoint platformRuntimeEndpoint) (platformRuntimeObservation, error) {
	proof, err := transport.ObserveControllerEnrollment(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID)
	if err != nil {
		return platformRuntimeObservation{}, err
	}
	return platformRuntimeObservation{controller: proof.Controller(), receivedAt: proof.ReceivedAt()}, nil
}
