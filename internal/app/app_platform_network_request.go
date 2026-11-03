package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/broker"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

const platformNetworkRequestLifetime = 30 * time.Second
const platformNetworkStreamRequestLifetime = 40 * time.Second
const platformNetworkStreamExecutionTimeout = 35 * time.Second

var errAppPlatformNetworkUnavailable = errors.New("application network request unavailable or denied")
var errAppPlatformNetworkUncertain = errors.New("application network request outcome uncertain")

type platformNetworkGrant struct {
	policy  string
	review  string
	profile transport.NetworkRuntimeProfile
	stream  bool
}
type platformNetworkRequestIntent struct {
	access         platformWritableBackendAccess
	grant          platformNetworkGrant
	stream         bool
	secretBinding  platformSecretBindingUse
	command        control.NetworkHTTPRequestCommand
	raw, signature []byte
}

func (i *platformNetworkRequestIntent) Close() {
	i.access.Close()
	clear(i.raw)
	clear(i.signature)
	clear(i.command.PolicyRaw)
	clear(i.command.CredentialValue)
	*i = platformNetworkRequestIntent{}
}
func (platformNetworkRequestIntent) MarshalJSON() ([]byte, error) {
	return nil, errAppPlatformNetworkUnavailable
}
func (i *platformNetworkRequestIntent) UnmarshalJSON([]byte) error {
	i.Close()
	return errAppPlatformNetworkUnavailable
}
func (platformNetworkRequestIntent) String() string     { return "network request intent (redacted)" }
func (i platformNetworkRequestIntent) GoString() string { return i.String() }

// The outer current-viewer transaction has verified complete signed execution
// history and exact active grant coverage. This read adds the selected policy
// and profile; an app cannot select a review, actor scope or runtime identity.
func loadPlatformNetworkGrant(e migExec, installation string, a platformWritableBackendAccess, capability string) (platformNetworkGrant, error) {
	var g platformNetworkGrant
	var raw, digest, action string
	release := platformDigest(a.intent.start.writer.descriptor)
	if err := e.queryRow(`SELECT g.policy_json,g.review_plan_id,p.plan_json,p.plan_sha256,o.action
		FROM app_platform_capability_grants g
		JOIN app_platform_review_plans p ON p.id=g.review_plan_id
		JOIN app_platform_operations o ON o.review_plan_id=p.id
		WHERE g.installation_id=? AND g.capability=?
		AND g.granted=1 AND g.policy_revision=?
		AND p.consumed_ms IS NOT NULL
		AND p.target_release_sha256=?
		AND o.installation_id=g.installation_id
		AND o.stage='complete' AND o.completed_ms IS NOT NULL
		AND o.controller_epoch=p.controller_epoch`,
		installation, capability, a.policy, release).
		Scan(&g.policy, &g.review, &raw, &digest, &action); err != nil {
		return g, errAppPlatformNetworkUnavailable
	}
	if len(raw) > 65536 || !platformValidUUID(g.review) || platformDigest([]byte(raw)) != digest {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	var target *platformManagedInstallTarget
	switch action {
	case "install":
		var plan platformInstallProjection
		if json.Unmarshal([]byte(raw), &plan) != nil ||
			plan.Format != platformWritableInstallFormat ||
			plan.Runtime == nil || plan.ReleaseSHA256 != release ||
			plan.ControllerEpoch != a.intent.start.writer.runtime.controllerEpoch {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
		canonical, err := json.Marshal(plan)
		if err != nil || !bytes.Equal(canonical, []byte(raw)) {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
		target = plan.Runtime
	case "update":
		var plan platformManagedUpdatePlan
		if json.Unmarshal([]byte(raw), &plan) != nil ||
			plan.Format != platformManagedUpdateFormat ||
			plan.TargetReleaseSHA256 != release ||
			plan.TargetGeneration != a.intent.generation ||
			plan.ControllerEpoch != a.intent.start.writer.runtime.controllerEpoch {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
		canonical, err := json.Marshal(plan)
		if err != nil || !bytes.Equal(canonical, []byte(raw)) {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
		target = plan.TargetRuntime
	default:
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	if target == nil ||
		target.RuntimeID != a.intent.start.writer.runtime.id ||
		target.DeploymentID != a.intent.start.writer.runtime.deploymentID ||
		target.ControllerEpoch != a.intent.start.writer.runtime.controllerEpoch ||
		target.ControlRevision != a.intent.start.writer.runtime.controlRevision ||
		target.Fingerprint != a.intent.start.writer.runtime.fingerprint {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	httpPolicy, streamPolicy, err := platformNetworkTargetPolicies(target)
	if err != nil {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	switch capability {
	case "network.http":
		if httpPolicy != g.policy {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
	case "network.stream":
		if streamPolicy != g.policy || target.NetworkProfile.StreamFormat != appplatform.HTTPStreamPolicyFormat {
			return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
		}
		g.stream = true
	default:
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	g.profile = target.NetworkProfile
	return g, nil
}

func (s *Server) preparePlatformNetworkRequest(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind, credential string, body []byte) (platformNetworkRequestIntent, error) {
	return s.preparePlatformNetworkIntent(ctx, session, installation, binding, method, target, accept, kind, credential, body, false)
}

func (s *Server) preparePlatformNetworkStreamRequest(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind, credential string, body []byte) (platformNetworkRequestIntent, error) {
	return s.preparePlatformNetworkIntent(ctx, session, installation, binding, method, target, accept, kind, credential, body, true)
}

func (s *Server) preparePlatformNetworkIntent(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind, credential string, body []byte, stream bool) (platformNetworkRequestIntent, error) {
	var result platformNetworkRequestIntent
	if !platformValidUUID(installation) || !binding.valid() || len(body) > appplatform.MaxHTTPRequestBytes || len(method) > 16 || len(target) > 4096 || !appplatform.ValidHTTPPolicyHeaderValue(accept) || !appplatform.ValidHTTPPolicyHeaderValue(kind) || (credential != "" && !platformSecretBindingNamePattern.MatchString(credential)) {
		return result, errAppPlatformNetworkUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	access, err := withPlatformBackendRead(s, ctx, session, installation, s.loadPlatformWritableBackendHistory, func(e migExec, a platformWritableBackendAccess, seed []byte) error {
		if !binding.matches(a) || len(seed) != ed25519.SeedSize {
			return errAppPlatformNetworkUnavailable
		}
		capability := "network.http"
		if stream {
			capability = "network.stream"
		}
		grant, err := loadPlatformNetworkGrant(e, installation, a, capability)
		if err != nil {
			return err
		}
		var policySHA string
		var duration time.Duration
		if stream {
			policy, err := appplatform.ParseHTTPStreamPolicy([]byte(grant.policy))
			if err != nil || int64(len(body)) > policy.Policy.RequestBytes {
				return errAppPlatformNetworkUnavailable
			}
			policySHA = policy.SHA256
			duration = policy.Policy.Duration
		} else {
			policy, err := appplatform.ParseHTTPPolicy([]byte(grant.policy))
			if err != nil || int64(len(body)) > policy.Policy.RequestBytes {
				return errAppPlatformNetworkUnavailable
			}
			policySHA = policy.SHA256
			duration = policy.Policy.Deadline
		}
		var credentialHeader string
		var credentialValue []byte
		if credential != "" {
			use, header, value, err := s.resolvePlatformSecretBindingCommand(e, installation, credential)
			if err != nil {
				clear(value)
				return errAppPlatformNetworkUnavailable
			}
			credentialHeader, credentialValue = header, value
			result.secretBinding = use
		}
		viewer, err := json.Marshal(a.viewer)
		if err != nil {
			clear(credentialValue)
			return errAppPlatformNetworkUnavailable
		}
		id, err := platformNewUUID()
		if err != nil {
			clear(credentialValue)
			return err
		}
		now := time.Now().UnixMilli()
		r := a.intent.start.writer.runtime
		result.grant = grant
		format := control.NetworkHTTPRequestFormat
		lifetime := platformNetworkRequestLifetime
		if stream {
			format = control.NetworkHTTPStreamRequestFormat
			lifetime = platformNetworkStreamRequestLifetime
		}
		if duration <= 0 || duration > lifetime {
			return errAppPlatformNetworkUnavailable
		}
		result.stream = stream
		result.command = control.NetworkHTTPRequestCommand{Format: format, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, ControlRevision: strconv.FormatInt(r.controlRevision, 10), RequestID: id, InstallationID: installation, ReviewPlanID: grant.review, Generation: strconv.FormatInt(a.intent.generation, 10), PolicyRevision: strconv.FormatInt(a.policy, 10), ReleaseSHA256: platformDigest(a.intent.start.writer.descriptor), ActorScope: platformDigest(append([]byte("portal-network-viewer/v1:"), viewer...)), PolicyRaw: []byte(grant.policy), PolicySHA256: policySHA, Method: method, URL: target, Accept: accept, ContentType: kind, BodyBytes: strconv.Itoa(len(body)), BodySHA256: platformDigest(body), CredentialName: credential, CredentialHeader: credentialHeader, CredentialValue: credentialValue, IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+lifetime.Milliseconds(), 10)}
		key := ed25519.NewKeyFromSeed(seed)
		defer clear(key)
		if stream {
			result.raw, result.signature, err = control.SignNetworkHTTPStreamRequest(result.command, key, a.intent.start.writer.authority, now)
		} else {
			result.raw, result.signature, err = control.SignNetworkHTTPRequest(result.command, key, a.intent.start.writer.authority, now)
		}
		clear(credentialValue)
		return err
	})
	if err != nil {
		clear(result.raw)
		clear(result.command.CredentialValue)
		clear(result.command.PolicyRaw)
		return platformNetworkRequestIntent{}, errAppPlatformNetworkUnavailable
	}
	result.access = access
	if err := verifyPlatformNetworkIntent(result, result.access.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		result.Close()
		return platformNetworkRequestIntent{}, errAppPlatformNetworkUnavailable
	}
	return result, nil
}

func verifyPlatformNetworkIntent(i platformNetworkRequestIntent, a control.Authority, now int64) error {
	if i.stream {
		if _, err := control.VerifyNetworkHTTPStreamRequest(i.raw, i.signature, a, now); err != nil {
			return err
		}
		return nil
	}
	_, err := control.VerifyNetworkHTTPRequest(i.raw, i.signature, a, now)
	return err
}

// No cached permission or original administrator identity is used here. Every
// online challenge and final result release reconstructs current viewer facts.
func (s *Server) withPlatformNetworkRequestCurrent(ctx context.Context, session platformActorSession, installation string, i platformNetworkRequestIntent, fn func(platformWritableBackendAccess, []byte) error) error {
	if installation != i.command.InstallationID {
		return errAppPlatformNetworkUnavailable
	}
	if err := verifyPlatformNetworkIntent(i, i.access.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return errAppPlatformNetworkUnavailable
	}
	current, err := withPlatformBackendRead(s, ctx, session, installation, s.loadPlatformWritableBackendHistory, func(e migExec, a platformWritableBackendAccess, seed []byte) error {
		capability := "network.http"
		if i.stream {
			capability = "network.stream"
		}
		grant, err := loadPlatformNetworkGrant(e, installation, a, capability)
		if err != nil || grant != i.grant || !samePlatformWritableBackendAccess(i.access, a) {
			return errAppPlatformNetworkUnavailable
		}
		if i.command.CredentialName != "" {
			facts, err := loadPlatformSecretBindingFacts(e, installation, false)
			if err != nil {
				return errAppPlatformNetworkUnavailable
			}
			use, _, err := loadPlatformSecretBindingUse(e, installation, i.command.CredentialName, facts)
			if err != nil || use != i.secretBinding {
				return errAppPlatformNetworkUnavailable
			}
		}
		if fn != nil {
			return fn(a, seed)
		}
		return nil
	})
	if err != nil {
		return errAppPlatformNetworkUnavailable
	}
	defer current.Close()
	if err := verifyPlatformNetworkIntent(i, current.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return errAppPlatformNetworkUnavailable
	}
	return nil
}
func (s *Server) approvePlatformNetworkRequest(ctx context.Context, session platformActorSession, installation string, i platformNetworkRequestIntent, command control.NetworkHTTPRequestCommand, challenge string) ([]byte, []byte, error) {
	if i.stream {
		if control.ValidateNetworkHTTPStreamRequestMetadata(command, i.access.intent.start.writer.authority, time.Now().UnixMilli()) != nil {
			return nil, nil, errAppPlatformNetworkUnavailable
		}
	} else {
		if control.ValidateNetworkHTTPRequestMetadata(command, i.access.intent.start.writer.authority, time.Now().UnixMilli()) != nil {
			return nil, nil, errAppPlatformNetworkUnavailable
		}
	}
	raw, err := json.Marshal(command)
	if err != nil || !bytes.Equal(raw, i.raw) {
		return nil, nil, errAppPlatformNetworkUnavailable
	}
	var proof, signature []byte
	err = s.withPlatformNetworkRequestCurrent(ctx, session, installation, i, func(a platformWritableBackendAccess, seed []byte) error {
		if len(seed) != ed25519.SeedSize {
			return errAppPlatformNetworkUnavailable
		}
		key := ed25519.NewKeyFromSeed(seed)
		defer clear(key)
		var err error
		if i.stream {
			proof, signature, err = control.SignNetworkHTTPStreamCheck(command, challenge, key, a.intent.start.writer.authority, time.Now().UnixMilli())
		} else {
			proof, signature, err = control.SignNetworkHTTPCheck(command, challenge, key, a.intent.start.writer.authority, time.Now().UnixMilli())
		}
		return err
	})
	if err != nil {
		return nil, nil, errAppPlatformNetworkUnavailable
	}
	if i.stream {
		err = control.VerifyNetworkHTTPStreamCheck(command, challenge, proof, signature, i.access.intent.start.writer.authority, time.Now().UnixMilli())
	} else {
		err = control.VerifyNetworkHTTPCheck(command, challenge, proof, signature, i.access.intent.start.writer.authority, time.Now().UnixMilli())
	}
	if err != nil {
		return nil, nil, errAppPlatformNetworkUnavailable
	}
	return proof, signature, nil
}

func validPlatformNetworkResult(o transport.NetworkObservation, i platformNetworkRequestIntent, e platformRuntimeEndpoint) bool {
	age := time.Since(o.ReceivedAt())
	peer := o.Endpoint()
	a := o.Controller()
	r := i.access.intent.start.writer.runtime
	return !o.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && peer.RootController && peer.RunnerUID == 0 && peer.Path == e.path && e.controllerUID > 0 && e.controllerGID > 0 && peer.ControllerUID == e.controllerUID && peer.ControllerGID == e.controllerGID && a.DeploymentID == r.deploymentID && a.ControllerEpoch == r.controllerEpoch && a.ControlRevision == r.controlRevision && a.Fingerprint == r.fingerprint && bytes.Equal(a.PublicKey, r.publicKey) && o.RequestSHA256() == platformDigest(i.raw)
}
func (s *Server) completePlatformNetworkRequest(ctx context.Context, session platformActorSession, installation string, i platformNetworkRequestIntent, o transport.NetworkObservation, e platformRuntimeEndpoint) (broker.HTTPResponse, error) {
	if !validPlatformNetworkResult(o, i, e) {
		return broker.HTTPResponse{}, errAppPlatformNetworkUncertain
	}
	if err := s.withPlatformNetworkRequestCurrent(ctx, session, installation, i, nil); err != nil || !validPlatformNetworkResult(o, i, e) {
		return broker.HTTPResponse{}, errAppPlatformNetworkUncertain
	}
	return o.Response(), nil
}

// Private foreground execution, one attempt only. Public route/SDK admission
// must also provide current session, origin/CSRF/enrollment and page binding.
func (s *Server) requestPlatformNetwork(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind, credential string, body []byte, e platformRuntimeEndpoint) (broker.HTTPResponse, error) {
	if len(body) > appplatform.MaxHTTPRequestBytes {
		return broker.HTTPResponse{}, errAppPlatformNetworkUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	i, err := s.preparePlatformNetworkRequest(ctx, session, installation, binding, method, target, accept, kind, credential, body)
	if err != nil {
		return broker.HTTPResponse{}, errAppPlatformNetworkUnavailable
	}
	defer i.Close()
	observed, err := platformReadNetworkRuntime(ctx, e)
	if err != nil || !validPlatformNetworkRuntimeObservation(observed, i.access.intent.start.writer.runtime, e) || observed.Profile() != i.grant.profile {
		return broker.HTTPResponse{}, errAppPlatformNetworkUnavailable
	}
	proof, err := platformRequestNetwork(ctx, e, i.raw, i.signature, body, i.access.intent.start.writer.authority, func(checkCtx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
		return s.approvePlatformNetworkRequest(checkCtx, session, installation, i, c, nonce)
	})
	if err != nil {
		if errors.Is(err, broker.ErrUncertain) {
			return broker.HTTPResponse{}, errAppPlatformNetworkUncertain
		}
		return broker.HTTPResponse{}, errAppPlatformNetworkUnavailable
	}
	return s.completePlatformNetworkRequest(ctx, session, installation, i, proof, e)
}

func validPlatformNetworkStreamResult(o transport.NetworkStreamObservation, i platformNetworkRequestIntent, e platformRuntimeEndpoint) bool {
	age := time.Since(o.ReceivedAt())
	peer := o.Endpoint()
	controller := o.Controller()
	r := i.access.intent.start.writer.runtime
	result := o.Result()
	return !o.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second &&
		peer.RootController && peer.RunnerUID == 0 && peer.Path == e.path &&
		e.controllerUID > 0 && e.controllerGID > 0 &&
		peer.ControllerUID == e.controllerUID && peer.ControllerGID == e.controllerGID &&
		controller.DeploymentID == r.deploymentID &&
		controller.ControllerEpoch == r.controllerEpoch &&
		controller.ControlRevision == r.controlRevision &&
		controller.Fingerprint == r.fingerprint &&
		bytes.Equal(controller.PublicKey, r.publicKey) &&
		o.RequestSHA256() == platformDigest(i.raw) &&
		result.Status >= 200 && result.Status < 300 &&
		result.Bytes >= 0 && result.Bytes <= appplatform.MaxHTTPStreamBytes &&
		result.Frames >= 0 && result.Frames <= appplatform.MaxHTTPStreamChecks
}

// Private foreground stream execution, one attempt only. Data callbacks may run
// before this function returns; every one is preceded by a fresh current check.
func (s *Server) requestPlatformNetworkStream(
	ctx context.Context,
	session platformActorSession,
	installation string,
	binding platformBackendRequestBinding,
	method, target, accept, kind, credential string,
	body []byte,
	e *platformRuntimeEndpoint,
	callbacks broker.HTTPStreamCallbacks,
) (broker.HTTPStreamResult, error) {
	if len(body) > appplatform.MaxHTTPRequestBytes || callbacks.Start == nil {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, platformNetworkStreamExecutionTimeout)
	defer cancel()
	i, err := s.preparePlatformNetworkStreamRequest(ctx, session, installation, binding, method, target, accept, kind, credential, body)
	if err != nil {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUnavailable
	}
	defer i.Close()
	if e == nil {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUnavailable
	}
	observed, err := platformReadNetworkRuntime(ctx, *e)
	if err != nil || !validPlatformNetworkRuntimeObservation(observed, i.access.intent.start.writer.runtime, *e) || observed.Profile() != i.grant.profile {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUnavailable
	}
	proof, err := platformRequestNetworkStream(ctx, *e, i.raw, i.signature, body, i.access.intent.start.writer.authority, func(checkCtx context.Context, c control.NetworkHTTPRequestCommand, nonce string) ([]byte, []byte, error) {
		return s.approvePlatformNetworkRequest(checkCtx, session, installation, i, c, nonce)
	}, callbacks)
	if err != nil {
		if errors.Is(err, broker.ErrUncertain) {
			return broker.HTTPStreamResult{}, errAppPlatformNetworkUncertain
		}
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUnavailable
	}
	if !validPlatformNetworkStreamResult(proof, i, *e) {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUncertain
	}
	if err := s.withPlatformNetworkRequestCurrent(ctx, session, installation, i, nil); err != nil {
		return broker.HTTPStreamResult{}, errAppPlatformNetworkUncertain
	}
	return proof.Result(), nil
}
