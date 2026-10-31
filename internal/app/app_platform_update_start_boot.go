package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformUpdateStartBootObservation struct {
	lease                                                         platformOperationLease
	runtime                                                       platformRuntimeRegistration
	bootID, nativeID, deliverySHA256, documentSHA256, startSHA256 string
	receivedAt                                                    time.Time
}

func (platformUpdateStartBootObservation) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformStageEvidence
}

func (o *platformUpdateStartBootObservation) UnmarshalJSON([]byte) error {
	*o = platformUpdateStartBootObservation{}
	return errAppPlatformStageEvidence
}

type platformUpdateStartHandshakeObservation interface {
	Evidence() journal.UpdateStartBackendHandshakeEvidence
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformUpdateStartHandshakeObservation(proof platformUpdateStartHandshakeObservation, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint, challenge string) bool {
	if !intent.applied || intent.start.running == nil {
		return false
	}
	wire, err := json.Marshal(intent.command)
	if err != nil || !bytes.Equal(wire, intent.raw) {
		return false
	}
	age := time.Since(proof.ReceivedAt())
	a, b := proof.Controller(), intent.start.authority
	peer := proof.Endpoint()
	evidence := proof.Evidence()
	delivery, _ := json.Marshal(intent.receipt)
	return !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second &&
		peer.RootController && peer.RunnerUID == 0 &&
		endpoint.controllerUID > 0 && endpoint.controllerGID > 0 &&
		peer.Path == endpoint.path &&
		peer.ControllerUID == endpoint.controllerUID &&
		peer.ControllerGID == endpoint.controllerGID &&
		a.DeploymentID == b.DeploymentID &&
		a.ControllerEpoch == b.ControllerEpoch &&
		a.ControlRevision == b.ControlRevision &&
		a.Fingerprint == b.Fingerprint &&
		bytes.Equal(a.PublicKey, b.PublicKey) &&
		proof.CommandSHA256() == platformDigest(intent.raw) &&
		evidence.PayloadSHA256 == intent.command.PayloadSHA256 &&
		evidence.DeliveryReceiptSHA256 == platformDigest(delivery) &&
		evidence.NativeID == intent.start.running.NativeID &&
		appplatform.ValidBackendHandshakeToken(challenge) &&
		evidence.ChallengeSHA256 == platformDigest([]byte(challenge))
}

func (s *Server) probePlatformUpdateStartBoot(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartBootObservation, error) {
	var zero platformUpdateStartBootObservation
	intent, err := s.preparePlatformUpdateStartRegistration(ctx, lease)
	if err != nil {
		return zero, err
	}
	defer intent.registration.Close()
	if !intent.applied {
		return zero, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedRuntimeRoot(
		ctx, lease, intent.start.runtime, intent.start.authority, endpoint); err != nil {
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
	proof, err := platformHandshakeUpdateStartBackend(
		ctx, endpoint, intent, challenge)
	if err != nil {
		return zero, err
	}
	return s.finishPlatformUpdateStartBoot(
		ctx, lease, intent, endpoint, challenge, proof)
}

func (s *Server) finishPlatformUpdateStartBoot(ctx context.Context, lease platformOperationLease, intent platformUpdateStartRegistrationIntent, endpoint platformRuntimeEndpoint, challenge string, proof platformUpdateStartHandshakeObservation) (platformUpdateStartBootObservation, error) {
	var zero platformUpdateStartBootObservation
	intent = clonePlatformUpdateStartRegistrationIntent(intent)
	if lease.snapshot.Action != appplatform.ActionUpdate ||
		!lease.snapshot.Managed || lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 ||
		!validPlatformUpdateStartHandshakeObservation(
			proof, intent, endpoint, challenge) {
		return zero, fmt.Errorf("update-start boot precondition: %w", errAppPlatformStageEvidence)
	}
	signer, err := intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	boot, err := signer.VerifyAnswer(
		challenge, proof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, fmt.Errorf("update-start boot answer: %w", appplatform.ErrBackendHandshake)
	}
	current, err := s.preparePlatformUpdateStartRegistration(ctx, lease)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(intent, current) {
		return zero, fmt.Errorf("update-start boot current registration: %w", errAppPlatformStageEvidence)
	}
	if !validPlatformUpdateStartHandshakeObservation(
		proof, intent, endpoint, challenge) {
		return zero, fmt.Errorf("update-start boot current proof: %w", errAppPlatformStageEvidence)
	}
	currentSigner, err := current.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(currentSigner.Key)
	currentBoot, err := currentSigner.VerifyAnswer(
		challenge, proof.Evidence().Answer, time.Now())
	if err != nil || currentBoot != boot {
		return zero, fmt.Errorf("update-start boot current answer: %w", appplatform.ErrBackendHandshake)
	}
	if !validPlatformUpdateStartHandshakeObservation(
		proof, intent, endpoint, challenge) {
		return zero, fmt.Errorf("update-start boot final proof: %w", errAppPlatformStageEvidence)
	}
	delivery, _ := json.Marshal(current.receipt)
	return platformUpdateStartBootObservation{
		lease: lease, runtime: current.start.runtime,
		bootID:         boot,
		nativeID:       proof.Evidence().NativeID,
		deliverySHA256: platformDigest(delivery),
		documentSHA256: current.command.DocumentSHA256,
		startSHA256:    current.command.RunningReceiptSHA256,
		receivedAt:     proof.ReceivedAt(),
	}, nil
}
