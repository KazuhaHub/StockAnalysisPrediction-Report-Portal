//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformRunWritableRegistration(ctx context.Context, endpoint platformRuntimeEndpoint, i platformWritableRegistrationIntent, action transport.WritableLifecycleAction) (transport.WritableLifecycleObservation, error) {
	var document []byte
	if action == transport.WritableRegistrationDeliver {
		var err error
		document, err = i.registration.Document()
		if err != nil {
			return transport.WritableLifecycleObservation{}, err
		}
		defer clear(document)
	}
	return transport.RunControllerWritableLifecycle(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, action, i.raw, i.signature, i.start.writer.authority, i.start.writer.descriptor, document)
}

func platformHandshakeWritableBackend(ctx context.Context, endpoint platformRuntimeEndpoint, i platformWritableRegistrationIntent, challenge string) (transport.WritableBackendHandshakeObservation, error) {
	return transport.HandshakeWritableControllerBackend(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, i.raw, i.signature, i.start.writer.authority, i.start.writer.descriptor, i.receipt, challenge)
}

func platformRequestWritableBackend(ctx context.Context, endpoint platformRuntimeEndpoint, i platformWritableRegistrationIntent, raw, signature, body []byte, assertion string) (transport.WritableBackendRequestObservation, error) {
	return transport.RequestWritableControllerBackend(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, signature, i.start.writer.authority, i.start.writer.descriptor, i.receipt, body, assertion)
}
