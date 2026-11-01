//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformDeleteApplicationData(
	ctx context.Context,
	endpoint platformRuntimeEndpoint,
	intent platformDeleteDataIntent,
) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(
		ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.ManagedDataDelete, intent.raw, intent.signature,
		intent.authority, intent.descriptor, nil)
}

func platformReadDeleteApplicationData(
	ctx context.Context,
	endpoint platformRuntimeEndpoint,
	intent platformDeleteDataIntent,
) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(
		ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.ManagedDataRead, intent.raw, intent.signature,
		intent.authority, intent.descriptor, nil)
}
