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

var errAppPlatformNetworkUnavailable = errors.New("application network request unavailable or denied")
var errAppPlatformNetworkUncertain = errors.New("application network request outcome uncertain")

type platformNetworkGrant struct {
	policy  string
	review  string
	profile transport.NetworkRuntimeProfile
}
type platformNetworkRequestIntent struct {
	access         platformWritableBackendAccess
	grant          platformNetworkGrant
	command        control.NetworkHTTPRequestCommand
	raw, signature []byte
}

func (i *platformNetworkRequestIntent) Close() {
	i.access.Close()
	clear(i.raw)
	clear(i.signature)
	clear(i.command.PolicyRaw)
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
func loadPlatformNetworkGrant(e migExec, installation string, a platformWritableBackendAccess) (platformNetworkGrant, error) {
	var g platformNetworkGrant
	var raw, digest string
	if err := e.queryRow(`SELECT g.policy_json,g.review_plan_id,p.plan_json,p.plan_sha256 FROM app_platform_capability_grants g JOIN app_platform_review_plans p ON p.id=g.review_plan_id WHERE g.installation_id=? AND g.capability='network.http' AND g.granted=1 AND g.policy_revision=? AND p.consumed_ms IS NOT NULL AND p.action='install'`, installation, a.policy).Scan(&g.policy, &g.review, &raw, &digest); err != nil {
		return g, errAppPlatformNetworkUnavailable
	}
	if len(raw) > 65536 || !platformValidUUID(g.review) || platformDigest([]byte(raw)) != digest {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	var p platformInstallProjection
	if json.Unmarshal([]byte(raw), &p) != nil || p.Format != platformWritableInstallFormat || p.Runtime == nil || p.Runtime.NetworkPolicy == "" || p.Runtime.NetworkPolicy != g.policy || p.ReleaseSHA256 != platformDigest(a.intent.start.writer.descriptor) || p.ControllerEpoch != a.intent.start.writer.runtime.controllerEpoch || p.Runtime.RuntimeID != a.intent.start.writer.runtime.id {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	canonical, err := json.Marshal(p)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	if _, err := platformNetworkTargetPolicy(p.Runtime); err != nil {
		return platformNetworkGrant{}, errAppPlatformNetworkUnavailable
	}
	g.profile = p.Runtime.NetworkProfile
	return g, nil
}

func (s *Server) preparePlatformNetworkRequest(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind string, body []byte) (platformNetworkRequestIntent, error) {
	var result platformNetworkRequestIntent
	if !platformValidUUID(installation) || !binding.valid() || len(body) > appplatform.MaxHTTPRequestBytes || len(method) > 16 || len(target) > 4096 || !appplatform.ValidHTTPPolicyHeaderValue(accept) || !appplatform.ValidHTTPPolicyHeaderValue(kind) {
		return result, errAppPlatformNetworkUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	access, err := withPlatformBackendRead(s, ctx, session, installation, s.loadPlatformWritableBackendHistory, func(e migExec, a platformWritableBackendAccess, seed []byte) error {
		if !binding.matches(a) || len(seed) != ed25519.SeedSize {
			return errAppPlatformNetworkUnavailable
		}
		grant, err := loadPlatformNetworkGrant(e, installation, a)
		if err != nil {
			return err
		}
		policy, err := appplatform.ParseHTTPPolicy([]byte(grant.policy))
		if err != nil || int64(len(body)) > policy.Policy.RequestBytes {
			return errAppPlatformNetworkUnavailable
		}
		viewer, err := json.Marshal(a.viewer)
		if err != nil {
			return errAppPlatformNetworkUnavailable
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		r := a.intent.start.writer.runtime
		result.grant = grant
		result.command = control.NetworkHTTPRequestCommand{Format: control.NetworkHTTPRequestFormat, DeploymentID: r.deploymentID, ControllerEpoch: r.controllerEpoch, ControlRevision: strconv.FormatInt(r.controlRevision, 10), RequestID: id, InstallationID: installation, ReviewPlanID: grant.review, Generation: "1", PolicyRevision: strconv.FormatInt(a.policy, 10), ReleaseSHA256: platformDigest(a.intent.start.writer.descriptor), ActorScope: platformDigest(append([]byte("portal-network-viewer/v1:"), viewer...)), PolicyRaw: []byte(grant.policy), PolicySHA256: policy.SHA256, Method: method, URL: target, Accept: accept, ContentType: kind, BodyBytes: strconv.Itoa(len(body)), BodySHA256: platformDigest(body), IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+platformNetworkRequestLifetime.Milliseconds(), 10)}
		key := ed25519.NewKeyFromSeed(seed)
		defer clear(key)
		result.raw, result.signature, err = control.SignNetworkHTTPRequest(result.command, key, a.intent.start.writer.authority, now)
		return err
	})
	if err != nil {
		clear(result.raw)
		clear(result.command.PolicyRaw)
		return platformNetworkRequestIntent{}, errAppPlatformNetworkUnavailable
	}
	result.access = access
	if _, err := control.VerifyNetworkHTTPRequest(result.raw, result.signature, result.access.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		result.Close()
		return platformNetworkRequestIntent{}, errAppPlatformNetworkUnavailable
	}
	return result, nil
}

// No cached permission or original administrator identity is used here. Every
// online challenge and final result release reconstructs current viewer facts.
func (s *Server) withPlatformNetworkRequestCurrent(ctx context.Context, session platformActorSession, installation string, i platformNetworkRequestIntent, fn func(platformWritableBackendAccess, []byte) error) error {
	if installation != i.command.InstallationID {
		return errAppPlatformNetworkUnavailable
	}
	if _, err := control.VerifyNetworkHTTPRequest(i.raw, i.signature, i.access.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return errAppPlatformNetworkUnavailable
	}
	current, err := withPlatformBackendRead(s, ctx, session, installation, s.loadPlatformWritableBackendHistory, func(e migExec, a platformWritableBackendAccess, seed []byte) error {
		grant, err := loadPlatformNetworkGrant(e, installation, a)
		if err != nil || grant != i.grant || !samePlatformWritableBackendAccess(i.access, a) {
			return errAppPlatformNetworkUnavailable
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
	if _, err := control.VerifyNetworkHTTPRequest(i.raw, i.signature, current.intent.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return errAppPlatformNetworkUnavailable
	}
	return nil
}
func (s *Server) approvePlatformNetworkRequest(ctx context.Context, session platformActorSession, installation string, i platformNetworkRequestIntent, command control.NetworkHTTPRequestCommand, challenge string) ([]byte, []byte, error) {
	if control.ValidateNetworkHTTPRequestMetadata(command, i.access.intent.start.writer.authority, time.Now().UnixMilli()) != nil {
		return nil, nil, errAppPlatformNetworkUnavailable
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
		proof, signature, err = control.SignNetworkHTTPCheck(command, challenge, key, a.intent.start.writer.authority, time.Now().UnixMilli())
		return err
	})
	if err != nil || control.VerifyNetworkHTTPCheck(command, challenge, proof, signature, i.access.intent.start.writer.authority, time.Now().UnixMilli()) != nil {
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
func (s *Server) requestPlatformNetwork(ctx context.Context, session platformActorSession, installation string, binding platformBackendRequestBinding, method, target, accept, kind string, body []byte, e platformRuntimeEndpoint) (broker.HTTPResponse, error) {
	if len(body) > appplatform.MaxHTTPRequestBytes {
		return broker.HTTPResponse{}, errAppPlatformNetworkUnavailable
	}
	body = bytes.Clone(body)
	defer clear(body)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	i, err := s.preparePlatformNetworkRequest(ctx, session, installation, binding, method, target, accept, kind, body)
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
