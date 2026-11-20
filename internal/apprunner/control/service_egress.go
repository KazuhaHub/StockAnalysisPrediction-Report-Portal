package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// ServiceEgressSessionFormat is the signed command that opens a background
// egress session: a bounded period during which the runner may execute
// Portal-approved network and notification calls for one installation, without
// a signed-in viewer. It carries no request and grants nothing by itself; every
// call inside the session still needs its own signed NetworkHTTPRequestCommand.
const ServiceEgressSessionFormat = "portal.service-egress-session/1-dev.1"
const serviceEgressSessionPurpose = "portal.app/runner-service-egress-session/v1"

// Platform-fixed budgets. They are in the signed command so a runner never
// invents them, and they are compared exactly so a future change is a format
// revision, not a field edit.
const (
	ServiceEgressRatePerMinute = "120"
	ServiceEgressBurst         = "20"
	// ServiceEgressMinLifetimeMS and ServiceEgressMaxLifetimeMS bound
	// expires_ms - issued_ms: at least the renewal overlap, at most one H07
	// assertion lifetime.
	ServiceEgressMinLifetimeMS = 60000
	ServiceEgressMaxLifetimeMS = 300000
)

// ServiceEgressResources are the only resources a session may carry.
// secrets.use (doc 176) is only meaningful beside network.http: it lets a call
// name a reviewed credential binding, which Portal places into that call.
var ServiceEgressResources = []string{"network.http", "notifications.publish", "secrets.use"}

// ServiceEgressSessionCommand binds one egress session to the installation
// tuple the runner already trusts. Like ServiceBackendTaskCommand it carries the
// registration proof of the generation it is for, so the runner compares the
// tuple against a controller-signed document rather than against Portal's word.
type ServiceEgressSessionCommand struct {
	Format                string   `json:"format"`
	DeploymentID          string   `json:"deployment_id"`
	ControllerEpoch       string   `json:"controller_epoch"`
	ControlRevision       string   `json:"control_revision"`
	SessionID             string   `json:"session_id"`
	InstallationID        string   `json:"installation_id"`
	Generation            string   `json:"generation"`
	PolicyRevision        string   `json:"policy_revision"`
	ReleaseSHA256         string   `json:"release_sha256"`
	ServiceIdentitySHA256 string   `json:"service_identity_sha256"`
	Resources             []string `json:"resources"`
	Concurrency           string   `json:"concurrency"`
	RatePerMinute         string   `json:"rate_per_minute"`
	Burst                 string   `json:"burst"`
	RegistrationRaw       []byte   `json:"registration_raw"`
	RegistrationSignature []byte   `json:"registration_signature"`
	IssuedMS              string   `json:"issued_ms"`
	ExpiresMS             string   `json:"expires_ms"`
}

// VerifiedServiceEgressSession is a command that passed every check below. It
// proves the controller's signature and the registration tuple only; it is not
// current SQL authority, which Portal re-checks on every call.
type VerifiedServiceEgressSession struct {
	command      ServiceEgressSessionCommand
	registration serviceRegistration
}

func (v VerifiedServiceEgressSession) Command() ServiceEgressSessionCommand {
	c := v.command
	c.Resources = slices.Clone(c.Resources)
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	return c
}

// RegistrationIdentity is the tuple of the verified registration; it equals the
// command's own tuple (checked at verification).
func (v VerifiedServiceEgressSession) RegistrationIdentity() ServiceBackendTaskRegistrationIdentity {
	return v.registration.identity
}

func (v VerifiedServiceEgressSession) InitialRegistration() (VerifiedBackendRegistrationCommand, bool) {
	if v.registration.initial == nil {
		return VerifiedBackendRegistrationCommand{}, false
	}
	return *v.registration.initial, true
}

func (v VerifiedServiceEgressSession) UpdateStartRegistration() (VerifiedUpdateStartRegistrationCommand, bool) {
	if v.registration.update == nil {
		return VerifiedUpdateStartRegistrationCommand{}, false
	}
	return *v.registration.update, true
}

func (v VerifiedServiceEgressSession) WritableRegistration() (VerifiedWritableBackendRegistrationCommand, bool) {
	if v.registration.writable == nil {
		return VerifiedWritableBackendRegistrationCommand{}, false
	}
	return *v.registration.writable, true
}

// VerifyServiceEgressSession accepts only the exact canonical encoding of a
// valid command, signed by the enrolled controller key, whose tuple equals the
// one its registration proof binds, and which is inside its lifetime at nowMS.
func VerifyServiceEgressSession(raw, signature []byte, a Authority, nowMS int64) (VerifiedServiceEgressSession, error) {
	var zero VerifiedServiceEgressSession
	if len(raw) == 0 || len(raw) > MaxCommandBytes || len(signature) != ed25519.SignatureSize || !utf8.Valid(raw) ||
		len(a.PublicKey) != ed25519.PublicKeySize || a.ControlRevision < 1 || nowMS < 0 {
		return zero, ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return zero, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return zero, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidCommand
	}
	var c ServiceEgressSessionCommand
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, raw) || validateServiceEgressSession(c, a, nowMS) != nil {
		return zero, ErrInvalidCommand
	}
	if !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(serviceEgressSessionPurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	// The registration is verified after the signature so an unauthenticated
	// command never makes the runner parse registration documents.
	bound, err := verifyServiceRegistration(c.RegistrationRaw, c.RegistrationSignature, c.PolicyRevision, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	id := bound.identity
	if c.InstallationID != id.InstallationID || c.Generation != id.Generation || c.PolicyRevision != id.PolicyRevision ||
		c.ControllerEpoch != id.ControllerEpoch || c.ReleaseSHA256 != id.ReleaseSHA256 {
		return zero, ErrInvalidCommand
	}
	return VerifiedServiceEgressSession{command: c, registration: bound}, nil
}

func validateServiceEgressSession(c ServiceEgressSessionCommand, a Authority, nowMS int64) error {
	if c.Format != ServiceEgressSessionFormat {
		return ErrInvalidCommand
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.SessionID, c.InstallationID} {
		if !candidateUUID(id) {
			return ErrInvalidCommand
		}
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != a.ControlRevision || c.DeploymentID != a.DeploymentID || c.ControllerEpoch != a.ControllerEpoch {
		return ErrInvalidCommand
	}
	for _, number := range []string{c.Generation, c.PolicyRevision} {
		if _, ok := counter(number, true); !ok {
			return ErrInvalidCommand
		}
	}
	for _, digest := range []string{c.ReleaseSHA256, c.ServiceIdentitySHA256} {
		if !shaPattern.MatchString(digest) || digest == strings.Repeat("0", 64) {
			return ErrInvalidCommand
		}
	}
	if len(c.Resources) < 1 || len(c.Resources) > len(ServiceEgressResources) {
		return ErrInvalidCommand
	}
	previous := ""
	for _, resource := range c.Resources {
		if previous >= resource || !slices.Contains(ServiceEgressResources, resource) {
			return ErrInvalidCommand
		}
		previous = resource
	}
	if slices.Contains(c.Resources, "secrets.use") && !slices.Contains(c.Resources, "network.http") {
		return ErrInvalidCommand
	}
	if concurrency, ok := counter(c.Concurrency, true); !ok || concurrency > 4 {
		return ErrInvalidCommand
	}
	if c.RatePerMinute != ServiceEgressRatePerMinute || c.Burst != ServiceEgressBurst {
		return ErrInvalidCommand
	}
	if len(c.RegistrationRaw) < 1 || len(c.RegistrationSignature) != ed25519.SignatureSize {
		return ErrInvalidCommand
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires-issued < ServiceEgressMinLifetimeMS || expires-issued > ServiceEgressMaxLifetimeMS || issued > nowMS || nowMS >= expires {
		return ErrInvalidCommand
	}
	if canonical, err := json.Marshal(c); err != nil || len(canonical) > MaxCommandBytes {
		return ErrInvalidCommand
	}
	return nil
}

// SignServiceEgressSession signs c and proves the result verifies, so a caller
// can never emit a command a runner would refuse for a reason knowable here.
func SignServiceEgressSession(c ServiceEgressSessionCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(serviceEgressSessionPurpose, raw))
	if _, err := VerifyServiceEgressSession(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
