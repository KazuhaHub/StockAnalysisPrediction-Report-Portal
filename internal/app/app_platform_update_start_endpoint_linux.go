//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformReserveUpdateStart(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartReserve, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
}

func platformReadUpdateStartReservation(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartRead, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
}

func platformExecuteUpdateStart(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartExecute, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
}

func platformReadUpdateStartReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartReceipt, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
}
