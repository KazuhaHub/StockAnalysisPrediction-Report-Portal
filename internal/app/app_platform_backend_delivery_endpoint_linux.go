//go:build linux

package app

import (
	"context"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func platformDeliverBackendRegistration(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformBackendDeliveryIntent) (transport.BackendRegistrationObservation, error) {
	document, err := intent.registration.Document()
	if err != nil {
		return transport.BackendRegistrationObservation{}, err
	}
	defer clear(document)
	return transport.DeliverControllerRegistration(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.candidate.authority, intent.candidate.descriptor, document)
}

func platformHandshakeBackend(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformBackendDeliveryIntent, challenge string) (transport.BackendHandshakeObservation, error) {
	return transport.HandshakeControllerBackend(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, intent.raw, intent.signature, intent.candidate.authority, intent.candidate.descriptor, challenge)
}

func platformRequestBackend(ctx context.Context, endpoint platformRuntimeEndpoint, intent platformBackendDeliveryIntent, raw, signature, body []byte, assertion string) (transport.BackendRequestObservation, error) {
	return transport.RequestControllerBackend(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, signature, intent.candidate.authority, intent.candidate.descriptor, body, assertion)
}

func platformHandshakeServiceBackend(
	ctx context.Context,
	endpoint platformRuntimeEndpoint,
	history *platformServiceBackendHistory,
	challenge string,
) (string, error) {
	if history == nil {
		return "", errAppPlatformStageEvidence
	}
	switch history.format {
	case control.BackendRegistrationFormat:
		if history.initial == nil {
			return "", errAppPlatformStageEvidence
		}
		proof, err := platformHandshakeBackend(
			ctx, endpoint, *history.initial, challenge)
		if err != nil || !validPlatformBackendHandshakeObservation(
			proof, *history.initial, endpoint, challenge) {
			return "", errAppPlatformStageEvidence
		}
		return proof.Evidence().Answer, nil
	case control.UpdateStartRegistrationFormat:
		if history.update == nil || history.update.update == nil {
			return "", errAppPlatformStageEvidence
		}
		proof, err := platformHandshakeUpdateStartBackend(
			ctx, endpoint, *history.update.update, challenge)
		if err != nil || !validPlatformUpdateStartHandshakeObservation(
			proof, *history.update.update, endpoint, challenge) {
			return "", errAppPlatformStageEvidence
		}
		return proof.Evidence().Answer, nil
	default:
		return "", errAppPlatformStageEvidence
	}
}
