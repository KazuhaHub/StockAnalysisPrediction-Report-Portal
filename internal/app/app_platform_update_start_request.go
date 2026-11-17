package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func (s *Server) requestPlatformUpdateStartBackend(ctx context.Context, session platformActorSession, installation string, binding *platformBackendRequestBinding, method, path string, body []byte, endpoint platformRuntimeEndpoint, initial platformWritableBackendAccess) (driver.BackendResponse, error) {
	var zero driver.BackendResponse
	if initial.intent.update == nil ||
		initial.intent.generation < 2 ||
		(binding != nil && !binding.matches(initial)) {
		return zero, errAppPlatformLaunchUnavailable
	}
	verified, err := control.VerifyUpdateStartRegistration(
		initial.intent.raw, initial.intent.signature,
		initial.intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	start := verified.Start().Command()
	if err := s.checkPlatformManagedUpdateBackendRoot(
		ctx, start.OperationID, initial.intent.start.writer.runtime,
		initial.intent.start.writer.authority, endpoint); err != nil {
		return zero, err
	}
	signer, err := initial.intent.registration.Signer("")
	if err != nil {
		return zero, err
	}
	defer clear(signer.Key)
	challenge, err := signer.NewChallenge(time.Now())
	if err != nil {
		return zero, err
	}
	proof, err := platformHandshakeUpdateStartBackend(
		ctx, endpoint, *initial.intent.update, challenge)
	if err != nil {
		return zero, err
	}
	if !validPlatformUpdateStartHandshakeObservation(
		proof, *initial.intent.update, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	boot, err := signer.VerifyAnswer(
		challenge, proof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, err
	}
	var raw, signature []byte
	var assertion string
	var command control.UpdateStartBackendRequestCommand
	current, err := s.withPlatformWritableBackendAccess(
		ctx, session, installation,
		func(a platformWritableBackendAccess, seed []byte) error {
			if (binding != nil && !binding.matches(a)) ||
				!samePlatformWritableBackendAccess(initial, a) ||
				a.intent.update == nil ||
				!validPlatformUpdateStartHandshakeObservation(
					proof, *initial.intent.update, endpoint,
					challenge) {
				return errAppPlatformLaunchUnavailable
			}
			var err error
			raw, signature, assertion, command, err =
				signPlatformUpdateStartBackendRequest(
					a, seed, method, path, body, boot)
			return err
		})
	if err != nil {
		return zero, err
	}
	defer current.Close()
	defer clear(raw)
	proofResult, err := platformRequestUpdateStartBackend(
		ctx, endpoint, current.intent, raw, signature, body, assertion)
	if err != nil {
		return zero, errors.Join(
			errAppPlatformBackendRequestUncertain, err)
	}
	if !validPlatformUpdateStartBackendRequestObservation(
		proofResult, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	final, err := s.preparePlatformWritableBackendAccess(
		ctx, session, installation)
	if err != nil {
		return zero, errors.Join(
			errAppPlatformBackendRequestUncertain, err)
	}
	defer final.Close()
	if !samePlatformWritableBackendAccess(current, final) ||
		!validPlatformUpdateStartBackendRequestObservation(
			proofResult, current.intent, endpoint, raw, signature,
			command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	return proofResult.Result().Response, nil
}

func signPlatformUpdateStartBackendRequest(a platformWritableBackendAccess, seed []byte, method, path string, body []byte, boot string) ([]byte, []byte, string, control.UpdateStartBackendRequestCommand, error) {
	var zero control.UpdateStartBackendRequestCommand
	if len(seed) != ed25519.SeedSize || a.intent.update == nil {
		return nil, nil, "", zero, errAppPlatformLaunchUnavailable
	}
	signer, err := a.intent.registration.Signer(boot)
	if err != nil {
		return nil, nil, "", zero, err
	}
	defer clear(signer.Key)
	assertion, err := signer.Sign(
		method, path, body, a.viewer, time.Now())
	if err != nil {
		return nil, nil, "", zero, err
	}
	id, err := platformNewUUID()
	if err != nil {
		return nil, nil, "", zero, err
	}
	command := control.UpdateStartBackendRequestCommand{
		Format:    control.UpdateStartBackendRequestFormat,
		RequestID: id, RegistrationRaw: bytes.Clone(a.intent.raw),
		RegistrationSignature: bytes.Clone(a.intent.signature),
		BootID:                boot, Method: method, Path: path,
		BodySHA256:      platformDigest(body),
		AssertionSHA256: platformDigest([]byte(assertion)),
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignUpdateStartBackendRequest(
		command, key, a.intent.start.writer.authority,
		time.Now().UnixMilli())
	if err != nil {
		return nil, nil, "", zero, err
	}
	return raw, signature, assertion, command, nil
}

func validPlatformUpdateStartBackendRequestObservation(proof transport.UpdateStartBackendRequestObservation, intent platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint, raw, signature []byte, command control.UpdateStartBackendRequestCommand) bool {
	age := time.Since(proof.ReceivedAt())
	a, b := proof.Controller(), intent.start.writer.authority
	peer := proof.Endpoint()
	result := proof.Result()
	expected, _ := json.Marshal(command)
	delivery, _ := json.Marshal(intent.updateReceipt)
	_, err := control.VerifyUpdateStartBackendRequest(
		raw, signature, b, time.Now().UnixMilli())
	return err == nil && bytes.Equal(expected, raw) &&
		bytes.Equal(command.RegistrationRaw, intent.raw) &&
		bytes.Equal(command.RegistrationSignature, intent.signature) &&
		!proof.ReceivedAt().IsZero() &&
		age >= 0 && age <= 3*time.Second &&
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
		proof.CommandSHA256() == platformDigest(raw) &&
		result.RequestID == command.RequestID &&
		result.RequestSHA256 == platformDigest(raw) &&
		result.DeliveryReceiptSHA256 == platformDigest(delivery) &&
		result.NativeID == intent.start.running.NativeID &&
		result.BootID == command.BootID &&
		appplatform.ValidBackendResponse(
			result.Response.Status, result.Response.Body)
}
