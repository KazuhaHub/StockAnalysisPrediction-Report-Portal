//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformHandshakeUpdateStartBackend(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartRegistrationIntent, challenge string) (transport.UpdateStartBackendHandshakeObservation, error) {
	return transport.HandshakeUpdateStartControllerBackend(
		ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		intent.raw, intent.signature, intent.start.authority,
		intent.start.targetDescriptor, intent.receipt, challenge)
}
