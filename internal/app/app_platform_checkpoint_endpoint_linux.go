//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformCreateCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformCheckpointIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.CheckpointCreate, intent.raw, intent.signature, intent.authority, intent.source.descriptor, nil)
}

func platformReadCheckpointReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformCheckpointIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.CheckpointRead, intent.raw, intent.signature, intent.authority, intent.source.descriptor, nil)
}
