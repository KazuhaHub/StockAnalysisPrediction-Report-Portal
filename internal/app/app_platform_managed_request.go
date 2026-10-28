package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

var errAppPlatformBackendRequestUncertain = errors.New("application request outcome is uncertain")

// Exact mutable viewer/access authority and immutable execution identity must
// survive the entire operation. Short-lived envelope timestamps may renew.
func samePlatformManagedBackendAccess(a, b platformManagedBackendAccess) bool {
	c, d := a.intent.candidate.command, b.intent.candidate.command
	c.IssuedMS, c.ExpiresMS = d.IssuedMS, d.ExpiresMS
	return a.account == b.account && a.policy == b.policy && a.fence == b.fence && a.activationID == b.activationID && a.viewer.Subject == b.viewer.Subject && a.viewer.Role == b.viewer.Role && a.viewer.SessionRevision == b.viewer.SessionRevision && slices.Equal(a.viewer.OUs, b.viewer.OUs) && c == d && samePlatformManagedRuntime(a.intent.candidate.runtime, b.intent.candidate.runtime) && a.intent.applied && b.intent.applied && a.intent.receipt == b.intent.receipt && a.intent.command.PayloadSHA256 == b.intent.command.PayloadSHA256 && a.intent.command.DocumentSHA256 == b.intent.command.DocumentSHA256 && bytes.Equal(a.intent.candidate.descriptor, b.intent.candidate.descriptor)
}

// Private host execution path, not an HTTP route. Neither destination nor
// viewer/role/OU/assertion can come from an app. It performs one business request
// with no automatic retry; any error after dispatch may follow an app-side write.
func (s *Server) requestPlatformManagedBackend(ctx context.Context, session platformActorSession, installation, method, path string, body []byte, endpoint platformRuntimeEndpoint) (driver.BackendResponse, error) {
	var zero driver.BackendResponse
	if !appplatform.ValidBackendRequestRoute(method, path) || len(body) > appplatform.MaxBackendRequestBody || (len(body) > 0 && (method == "GET" || !json.Valid(body))) {
		return zero, errAppPlatformLaunchUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	initial, err := s.preparePlatformManagedBackendAccess(ctx, session, installation)
	if err != nil {
		return zero, err
	}
	defer initial.Close()
	intent := initial.intent
	if err := s.checkPlatformManagedOperationRoot(ctx, intent.candidate.command.OperationID, intent.candidate.runtime, intent.candidate.authority, endpoint); err != nil {
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
	bootProof, err := platformHandshakeBackend(ctx, endpoint, intent, challenge)
	if err != nil {
		return zero, err
	}
	if !validPlatformBackendHandshakeObservation(bootProof, intent, endpoint, challenge) {
		return zero, errAppPlatformStageEvidence
	}
	boot, err := signer.VerifyAnswer(challenge, bootProof.Evidence().Answer, time.Now())
	if err != nil {
		return zero, err
	}
	var raw, signature []byte
	var assertion string
	var command control.BackendRequestCommand
	current, err := s.withPlatformManagedBackendAccess(ctx, session, installation, func(a platformManagedBackendAccess, seed []byte) error {
		if !samePlatformManagedBackendAccess(initial, a) || !validPlatformBackendHandshakeObservation(bootProof, intent, endpoint, challenge) {
			return errAppPlatformLaunchUnavailable
		}
		var err error
		raw, signature, assertion, command, err = signPlatformManagedBackendRequest(a, seed, method, path, body, boot)
		return err
	})
	if err != nil {
		return zero, err
	}
	defer current.Close()
	defer clear(raw)
	proof, err := platformRequestBackend(ctx, endpoint, current.intent, raw, signature, body, assertion)
	if err != nil {
		return zero, errors.Join(errAppPlatformBackendRequestUncertain, err)
	}
	if !validPlatformManagedBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	return s.completePlatformManagedBackendRequest(ctx, session, installation, current, proof, endpoint, raw, signature, command)
}

// Consume the buffered native result only after current access is revalidated.
func (s *Server) completePlatformManagedBackendRequest(ctx context.Context, session platformActorSession, installation string, current platformManagedBackendAccess, proof transport.BackendRequestObservation, endpoint platformRuntimeEndpoint, raw, signature []byte, command control.BackendRequestCommand) (driver.BackendResponse, error) {
	var zero driver.BackendResponse
	if !validPlatformManagedBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	final, err := s.preparePlatformManagedBackendAccess(ctx, session, installation)
	if err != nil {
		return zero, errors.Join(errAppPlatformBackendRequestUncertain, err)
	}
	defer final.Close()
	if !samePlatformManagedBackendAccess(current, final) || !validPlatformManagedBackendRequestObservation(proof, current.intent, endpoint, raw, signature, command) {
		return zero, errAppPlatformBackendRequestUncertain
	}
	return proof.Result().Response, nil
}

func validPlatformManagedBackendRequestObservation(proof transport.BackendRequestObservation, intent platformBackendDeliveryIntent, endpoint platformRuntimeEndpoint, raw, signature []byte, command control.BackendRequestCommand) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p, r := proof.Controller(), intent.candidate.authority, proof.Endpoint(), proof.Result()
	receipt, _ := json.Marshal(intent.receipt)
	expected, _ := json.Marshal(command)
	_, err := control.VerifyBackendRequest(raw, signature, b, time.Now().UnixMilli())
	return err == nil && bytes.Equal(expected, raw) && bytes.Equal(command.RegistrationRaw, intent.raw) && bytes.Equal(command.RegistrationSignature, intent.signature) && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(raw) && r.RequestID == command.RequestID && r.RequestSHA256 == platformDigest(raw) && r.DeliveryReceiptSHA256 == platformDigest(receipt) && r.NativeID == intent.candidate.running.NativeID && r.BootID == command.BootID && appplatform.ValidBackendResponse(r.Response.Status, r.Response.Body)
}

// Called only inside the current access transaction; outputs remain buffered
// until that transaction's final authority/time checks and commit succeed.
func signPlatformManagedBackendRequest(a platformManagedBackendAccess, seed []byte, method, path string, body []byte, boot string) ([]byte, []byte, string, control.BackendRequestCommand, error) {
	var zero control.BackendRequestCommand
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
	command := control.BackendRequestCommand{Format: control.BackendRequestFormat, RequestID: id, RegistrationRaw: a.intent.raw, RegistrationSignature: a.intent.signature, BootID: boot, Method: method, Path: path, BodySHA256: platformDigest(body), AssertionSHA256: platformDigest([]byte(assertion))}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	raw, signature, err := control.SignBackendRequest(command, key, a.intent.candidate.authority, time.Now().UnixMilli())
	if err != nil {
		return nil, nil, "", zero, err
	}
	return raw, signature, assertion, command, nil
}
