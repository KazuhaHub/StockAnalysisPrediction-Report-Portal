//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunWritableStart(ctx context.Context, endpoint platformRuntimeEndpoint, i platformWritableStartIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, action, i.raw, i.signature, i.writer.authority, i.writer.descriptor, nil)
}
