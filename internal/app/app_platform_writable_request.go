package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// Exact mutable viewer/access authority and immutable execution identity must
// survive the entire operation. Short-lived envelope timestamps may renew.
func samePlatformWritableBackendAccess(a, b platformWritableBackendAccess) bool {
	return a.account == b.account && a.policy == b.policy && a.fence == b.fence && a.activationID == b.activationID && a.viewer.Subject == b.viewer.Subject && a.viewer.Role == b.viewer.Role && a.viewer.SessionRevision == b.viewer.SessionRevision && slices.Equal(a.viewer.OUs, b.viewer.OUs) && a.intent.applied && b.intent.applied && samePlatformWritableRegistrationIntent(a.intent, b.intent)
}

// Private host execution path, not an HTTP route. Neither destination nor
// viewer/role/OU/assertion can come from an app. It performs one business request
// with no automatic retry; any error after dispatch may follow an app-side write.
func (s *Server) requestPlatformWritableBackend(ctx context.Context, session platformActorSession, installation, method, path string, body []byte, endpoint platformRuntimeEndpoint) (driver.BackendResponse, error) {
	return s.requestPlatformWritableBackendBinding(ctx, session, installation, nil, method, path, body, endpoint)
}

// Page identity is a precondition, never authority. The current access
// transaction still reconstructs viewer, audience and execution identity.
type platformBackendRequestBinding struct {
	Generation     string `json:"generation"`
	PolicyRevision string `json:"policyRevision"`
	ReleaseSHA256  string `json:"releaseSha256"`
}

func (b platformBackendRequestBinding) valid() bool {
	return platformPositiveCounter(b.Generation) && platformPositiveCounter(b.PolicyRevision) && platformValidSHA(b.ReleaseSHA256)
}

func (b platformBackendRequestBinding) matches(a platformWritableBackendAccess) bool {
	// The completed writable source reader currently admits generation one.
	return b.valid() && b.Generation == "1" && b.PolicyRevision == strconv.FormatInt(a.policy, 10) && b.ReleaseSHA256 == platformDigest(a.intent.start.writer.descriptor)
}

func (s *Server) requestPlatformBoundWritableBackend(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, path string, body []byte, endpoint platformRuntimeEndpoint) (driver.BackendResponse, error) {
	if !binding.valid() {
		return driver.BackendResponse{}, errAppPlatformLaunchUnavailable
	}
	return s.requestPlatformWritableBackendBinding(ctx, session, installation, &binding, method, path, body, endpoint)
}

func (s *Server) requestPlatformWritableBackendBinding(ctx context.Context, session platformActorSession, installation string, binding *platformBackendRequestBinding, method, path string, body []byte, endpoint platformRuntimeEndpoint) (driver.BackendResponse, error) {
	var zero driver.BackendResponse
	if !appplatform.ValidBackendRequestRoute(method, path) || len(body) > appplatform.MaxBackendRequestBody || (len(body) > 0 && (method == "GET" || !json.Valid(body))) {
		return zero, errAppPlatformLaunchUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	initial, err := s.withPlatformWritableBackendAccess(ctx, session, installation, func(a platformWritableBackendAccess, _ []byte) error {
		if binding != nil && !binding.matches(a) {
			return errAppPlatformLaunchUnavailable
		}
		return nil
	})
	if err != nil {
		return zero, err
	}
	defer initial.Close()
	intent := initial.intent
	verified, err := control.VerifyWritableBackendRegistration(intent.raw, intent.signature, intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil {
		return zero, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, verified.Candidate().Index().OperationID, intent.start.writer.runtime, intent.start.writer.authority, endpoint); err != nil {
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
	bootProof, err := platformHandshakeWritableBackend(ctx, endpoint, intent, challenge)
	if err != nil {
		return zero, err
	}
	if !validPlatformWritableHandshakeObservation(bootProof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	boot, err := signer.VerifyAnswer(challenge, bootProof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, err
	}
	var raw, signature []byte
	var assertion string
	var command control.WritableBackendRequestCommand
	current, err := s.withPlatformWritableBackendAccess(ctx, session, installation, func(a platformWritableBackendAccess, seed []byte) error {
		if (binding != nil && !binding.matches(a)) || !samePlatformWritableBackendAccess(initial, a) || !validPlatformWritableHandshakeObservation(bootProof, intent, endpoint, challenge) {
			return errAppPlatformLaunchUnavailable
		}
		var err error
		raw, signature, assertion, command, err = signPlatformWritableBackendRequest(a, seed, method, path, body, boot)
		return err
	})
	if err != nil {
		return zero, err
	}
	defer current.Close()
	defer clear(raw)
	proof, err := platformRequestWritableBackend(ctx, endpoint, current.intent, raw, signature, body, assertion)
	if err != nil {
		return zero, errors.Join(errAppPlatformBackendRequestUncertain, err)
	}
	if !validPlatformWritableBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	return s.completePlatformWritableBackendRequest(ctx, session, installation, current, proof, endpoint, raw, signature, command)
}

// Consume the buffered native result only after current access is revalidated.
func (s *Server) completePlatformWritableBackendRequest(ctx context.Context, session platformActorSession, installation string, current platformWritableBackendAccess, proof transport.WritableBackendRequestObservation, endpoint platformRuntimeEndpoint, raw, signature []byte, command control.WritableBackendRequestCommand) (driver.BackendResponse, error) {
	var zero driver.BackendResponse
	if !validPlatformWritableBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	final, err := s.preparePlatformWritableBackendAccess(ctx, session, installation)
	if err != nil {
		return zero, errors.Join(errAppPlatformBackendRequestUncertain, err)
	}
	defer final.Close()
	if !samePlatformWritableBackendAccess(current, final) || !validPlatformWritableBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	return proof.Result().Response, nil
}

func validPlatformWritableBackendRequestObservation(proof transport.WritableBackendRequestObservation, intent platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint, raw, signature []byte, command control.WritableBackendRequestCommand) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p, r := proof.Controller(), intent.start.writer.authority, proof.Endpoint(), proof.Result()
	receipt, _ := json.Marshal(intent.receipt)
	expected, _ := json.Marshal(command)
	_, err := control.VerifyWritableBackendRequest(raw, signature, b, time.Now().UnixMilli())
	return err == nil && bytes.Equal(expected, raw) && bytes.Equal(command.RegistrationRaw, intent.raw) && bytes.Equal(command.RegistrationSignature, intent.signature) && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(raw) && r.RequestID == command.RequestID && r.RequestSHA256 == platformDigest(raw) && r.DeliveryReceiptSHA256 == platformDigest(receipt) && r.NativeID == intent.start.running.NativeID && r.BootID == command.BootID && appplatform.ValidBackendResponse(r.Response.Status, r.Response.Body)
}

// Called only inside the current access transaction; outputs remain buffered
// until that transaction's final authority/time checks and commit succeed.
func signPlatformWritableBackendRequest(a platformWritableBackendAccess, seed []byte, method, path string, body []byte, boot string) ([]byte, []byte, string, control.WritableBackendRequestCommand, error) {
	var zero control.WritableBackendRequestCommand
	if len(seed) != ed25519.SeedSize {
		return nil, nil, "", zero, errAppPlatformLaunchUnavailable
	}
	signer, err := a.intent.registration.Signer(boot)
	if err != nil {
		return nil, nil, "", zero, err
	}
	defer clear(signer.Key)
	assertion, err := signer.Sign(method, path, body, a.viewer, time.Now())
	if err != nil {
		return nil, nil, "", zero, err
	}
	id, err := platformNewUUID()
	if err != nil {
		return nil, nil, "", zero, err
	}
	command := control.WritableBackendRequestCommand{Format: control.WritableBackendRequestFormat, RequestID: id, RegistrationRaw: a.intent.raw, RegistrationSignature: a.intent.signature, BootID: boot, Method: method, Path: path, BodySHA256: platformDigest(body), AssertionSHA256: platformDigest([]byte(assertion))}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignWritableBackendRequest(command, key, a.intent.start.writer.authority, time.Now().UnixMilli())
	if err != nil {
		return nil, nil, "", zero, err
	}
	return raw, signature, assertion, command, nil
}
