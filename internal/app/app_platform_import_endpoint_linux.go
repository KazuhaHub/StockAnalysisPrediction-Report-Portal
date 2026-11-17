//go:build linux

package app

import (
	"context"
	"errors"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformRunLifecycle is the runner round trip; tests substitute it.
var platformRunLifecycle = transport.RunControllerWritableLifecycle

// The runner's import-checkpoint actions (execute and read-receipt) are reached through the writable
// lifecycle transport. The runner verifies the signed command against the release descriptor the
// command names, so an intent without one never leaves the Portal.
func platformExecuteImportCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (transport.WritableLifecycleObservation, error) {
	return runPlatformImportCheckpoint(ctx, endpoint, intent, transport.ImportCheckpointExecute)
}

func platformReadImportCheckpointReceipt(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent) (transport.WritableLifecycleObservation, error) {
	observation, err := runPlatformImportCheckpoint(ctx, endpoint, intent, transport.ImportCheckpointRead)
	if errors.Is(err, journal.ErrNoSuchEffect) {
		// The runner proved it never claimed the effect. Nothing else, a closed connection
		// included, says more than that the answer was lost.
		return transport.WritableLifecycleObservation{}, errPlatformImportAbsent
	}
	return observation, err
}

func runPlatformImportCheckpoint(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformImportIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	if len(intent.descriptor) == 0 {
		return transport.WritableLifecycleObservation{}, errAppPlatformStageEvidence
	}
	// A read-receipt carries the signature made under the read purpose; an execute never does.
	signature := intent.signature
	if action == transport.ImportCheckpointRead {
		signature = intent.readSignature
	}
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		action, intent.raw, signature, intent.authority, intent.descriptor, nil)
}
