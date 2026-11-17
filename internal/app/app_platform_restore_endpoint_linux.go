//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformExecuteRestore(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (transport.WritableLifecycleObservation, error) {
	descriptor, err := platformRestoreDescriptor(intent)
	if err != nil {
		return transport.WritableLifecycleObservation{}, err
	}
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.RestoreExecute, intent.raw, intent.signature, intent.authority, descriptor, nil)
}

func platformReadRestoreReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformRestoreIntent) (transport.WritableLifecycleObservation, error) {
	descriptor, err := platformRestoreDescriptor(intent)
	if err != nil {
		return transport.WritableLifecycleObservation{}, err
	}
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.RestoreRead, intent.raw, intent.readSignature, intent.authority, descriptor, nil)
}

func platformRestoreDescriptor(intent platformRestoreIntent) ([]byte, error) {
	if len(intent.source.descriptor) == 0 {
		return nil, errAppPlatformStageEvidence
	}
	return intent.source.descriptor, nil
}
