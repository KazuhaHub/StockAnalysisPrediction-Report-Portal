//go:build linux

package app

import (
	"context"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunUpdateStartRegistration(ctx context.Context, endpoint platformRuntimeEndpoint, i platformUpdateStartRegistrationIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	var document []byte
	if action == transport.UpdateStartRegistrationDeliver {
		var err error
		document, err = i.registration.Document()
		if err != nil {
			return transport.WritableLifecycleObservation{}, err
		}
		defer clear(document)
	}
	return transport.RunControllerWritableLifecycle(
		ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID,
		action, i.raw, i.signature, i.start.authority,
		i.start.targetDescriptor, document)
}
