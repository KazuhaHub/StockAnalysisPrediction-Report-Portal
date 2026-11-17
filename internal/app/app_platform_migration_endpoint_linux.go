//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// platformRunMigrationLifecycle is the runner round trip of a migration lifecycle action. The runner verifies
// the signed command against the release descriptor the command names, so one without a descriptor never leaves
// the Portal.
func platformRunMigrationLifecycle(ctx context.Context, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction, raw, signature []byte, authority control.Authority, descriptor []byte) (transport.WritableLifecycleObservation, error) {
	if len(descriptor) == 0 {
		return transport.WritableLifecycleObservation{}, errAppPlatformStageEvidence
	}
	return platformRunLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, action, raw, signature, authority, descriptor, nil)
}
