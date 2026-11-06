//go:build linux

package app

import (
	"context"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
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

func platformProbeUpdateStart(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (transport.WritableLifecycleObservation, error) {
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartProbe, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
}

// platformReadUpdateStartReceipt reads the running receipt of a start effect back from the runner. Only the
// runner's typed absence reply becomes the definite no-effect answer; every other failure stays an
// ordinary error, which the Portal treats as unknown.
func platformReadUpdateStartReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformUpdateStartIntent) (platformUpdateStartRunningObservation, error) {
	observation, err := platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		transport.UpdateStartReceipt, intent.raw, intent.signature, intent.authority, intent.targetDescriptor, nil)
	if errors.Is(err, journal.ErrNoSuchEffect) {
		return nil, errPlatformStartAbsent
	}
	if err != nil {
		return nil, err
	}
	return observation, nil
}
