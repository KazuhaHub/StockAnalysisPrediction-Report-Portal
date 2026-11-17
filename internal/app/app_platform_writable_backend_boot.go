package app

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// A live host-verified boot is separate from delivery and from readonly boot.
// Later activation must independently recheck its exact source and freshness.
type platformWritableBootObservation struct {
	lease                                                         platformOperationLease
	runtime                                                       platformRuntimeRegistration
	bootID, nativeID, deliverySHA256, documentSHA256, startSHA256 string
	receivedAt                                                    time.Time
}

func (platformWritableBootObservation) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformStageEvidence
}
func (o *platformWritableBootObservation) UnmarshalJSON([]byte) error {
	*o = platformWritableBootObservation{}
	return errAppPlatformStageEvidence
}

func validPlatformWritableHandshakeObservation(proof transport.WritableBackendHandshakeObservation, i platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint, challenge string) bool {
	if !i.applied || !validPlatformWritableRegistrationWire(i) {
		return false
	}
	age := time.Since(proof.ReceivedAt())
	a, b, p, e := proof.Controller(), i.start.writer.authority, proof.Endpoint(), proof.Evidence()
	delivery, _ := json.Marshal(i.receipt)
	return !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(i.raw) && e.PayloadSHA256 == i.command.PayloadSHA256 && e.DeliveryReceiptSHA256 == platformDigest(delivery) && e.NativeID == i.start.running.NativeID && appplatform.ValidBackendHandshakeToken(challenge) && e.ChallengeSHA256 == platformDigest([]byte(challenge))
}
func (s *Server) probePlatformWritableBackendBoot(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformWritableBootObservation, error) {
	var zero platformWritableBootObservation
	i, err := s.preparePlatformWritableRegistration(ctx, lease)
	if err != nil {
		return zero, err
	}
	defer i.registration.Close()
	if !i.applied {
		return zero, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, i.start.writer.runtime, i.start.writer.authority, endpoint); err != nil {
		return zero, err
	}
	signer, err := i.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return zero, err
	}
	proof, err := platformHandshakeWritableBackend(ctx, endpoint, i, challenge)
	if err != nil {
		return zero, err
	}
	return s.finishPlatformWritableBackendBoot(ctx, lease, i, endpoint, challenge, proof)
}
func (s *Server) finishPlatformWritableBackendBoot(ctx context.Context, lease platformOperationLease, intent platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint, challenge string, proof transport.WritableBackendHandshakeObservation) (platformWritableBootObservation, error) {
	var zero platformWritableBootObservation
	intent = clonePlatformWritableRegistrationIntent(intent)
	if !validPlatformWritableActivateStage(lease) || !validPlatformWritableHandshakeObservation(proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	signer, err := intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	boot, err := signer.VerifyAnswer(challenge, proof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, appplatform.ErrBackendHandshake
	}
	current, err := s.preparePlatformWritableRegistration(ctx, lease)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformWritableRegistrationIntent(intent, current) || !validPlatformWritableHandshakeObservation(proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	// Re-derive the verifier from current encrypted identity instead of trusting a
	// runner-provided success flag or the original handle alone.
	currentSigner, err := current.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(currentSigner.Key)
	currentBoot, err := currentSigner.VerifyAnswer(challenge, proof.Evidence().Answer, time.Now())
	if err != nil || currentBoot != boot {
		return zero, appplatform.ErrBackendHandshake
	}
	if !validPlatformWritableHandshakeObservation(proof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	delivery, _ := json.Marshal(current.receipt)
	return platformWritableBootObservation{lease: lease, runtime: current.start.writer.runtime, bootID: boot, nativeID: proof.Evidence().NativeID, deliverySHA256: platformDigest(delivery), documentSHA256: current.command.DocumentSHA256, startSHA256: current.command.RunningReceiptSHA256, receivedAt: proof.ReceivedAt()}, nil
}
