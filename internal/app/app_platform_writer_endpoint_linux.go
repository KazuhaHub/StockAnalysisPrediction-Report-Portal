//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunWriter(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformWriterIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, action, intent.raw, intent.signature, intent.authority, intent.descriptor, nil)
}
