//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// The runner's import-checkpoint actions (execute and read-receipt) are reached through the writable
// lifecycle transport. The runner verifies the signed command against the release descriptor the
// command names, so an intent without one never leaves the Portal.
func platformExecuteImportCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (transport.WritableLifecycleObservation, error) {
	return runPlatformImportCheckpoint(ctx, endpoint, intent, transport.ImportCheckpointExecute)
}

func platformReadImportCheckpointReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (transport.WritableLifecycleObservation, error) {
	return runPlatformImportCheckpoint(ctx, endpoint, intent, transport.ImportCheckpointRead)
}

func runPlatformImportCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	if len(intent.descriptor) == 0 {
		return transport.WritableLifecycleObservation{}, errAppPlatformStageEvidence
	}
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		action, intent.raw, intent.signature, intent.authority, intent.descriptor, nil)
}
