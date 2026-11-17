package app

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Fresh boot evidence remains a live private value. It neither writes activation
// state nor grants viewer/broker/writer authority. Later consumers must recheck
// this exact operation/runtime/delivery and enforce the short freshness bound.
type platformBackendBootObservation struct {
	lease                                            platformOperationLease
	runtime                                          platformRuntimeRegistration
	bootID, nativeID, deliverySHA256, documentSHA256 string
	receivedAt                                       time.Time
}

func (platformBackendBootObservation) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformStageEvidence
}
func (o *platformBackendBootObservation) UnmarshalJSON([]byte) error {
	*o = platformBackendBootObservation{}
	return errAppPlatformStageEvidence
}
func validPlatformBackendHandshakeObservation(proof transport.BackendHandshakeObservation, intent platformBackendDeliveryIntent, endpoint platformRuntimeEndpoint, challenge string) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p, e := proof.Controller(), intent.candidate.authority, proof.Endpoint(), proof.Evidence()
	delivery, _ := json.Marshal(intent.receipt)
	return intent.applied && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(intent.raw) && e.PayloadSHA256 == intent.command.PayloadSHA256 && e.DeliveryReceiptSHA256 == platformDigest(delivery) && e.NativeID == intent.candidate.running.NativeID && e.ChallengeSHA256 == platformDigest([]byte(challenge))
}
func (s *Server) probePlatformBackendBoot(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformBackendBootObservation, error) {
	var zero platformBackendBootObservation
	intent, err := s.preparePlatformBackendDelivery(ctx, expected)
	if err != nil {
		return zero, err
	}
	defer intent.registration.Close()
	if !intent.applied {
		return zero, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, expected, intent.candidate.runtime, intent.candidate.authority, endpoint); err != nil {
		return zero, err
	}
	signer, err := intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return zero, err
	}
	proof, err := platformHandshakeBackend(ctx, endpoint, intent, challenge)
	if err != nil {
		return zero, err
	}
	if !validPlatformBackendHandshakeObservation(proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	boot, err := signer.VerifyAnswer(challenge, proof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, appplatform.ErrBackendHandshake
	}
	// Re-derive material and recheck current authority/history after native IO.
	current, err := s.preparePlatformBackendDelivery(ctx, expected)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !current.applied || current.receipt != intent.receipt || current.command.PayloadSHA256 != intent.command.PayloadSHA256 || current.command.DocumentSHA256 != intent.command.DocumentSHA256 || !samePlatformManagedRuntime(current.candidate.runtime, intent.candidate.runtime) || !validPlatformBackendHandshakeObservation(proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := signer.VerifyAnswer(challenge, proof.Evidence().Answer, time.Now()); err != nil {
		return zero, appplatform.ErrBackendHandshake
	}
	delivery, _ := json.Marshal(intent.receipt)
	return platformBackendBootObservation{lease: expected, runtime: intent.candidate.runtime, bootID: boot, nativeID: proof.Evidence().NativeID, deliverySHA256: platformDigest(delivery), documentSHA256: intent.command.DocumentSHA256, receivedAt: proof.ReceivedAt()}, nil
}
