//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunManagedRemoval(ctx context.Context, endpoint platformRuntimeEndpoint, stage appplatform.Stage, raw, signature []byte, authority control.Authority, descriptor []byte) (transport.WritableLifecycleObservation, error) {
	action := transport.ManagedRemovalRead
	if stage == appplatform.StageStop {
		action = transport.ManagedRemovalStop
	} else if stage == appplatform.StageRemoveCode {
		action = transport.ManagedRemovalRemoveCode
	}
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, action, raw, signature, authority, descriptor, nil)
}
