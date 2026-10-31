package appplatform

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

var ErrBackendRegistration = errors.New("managed backend registration unavailable")
var backendUUID = regexp.MustCompile(`^[a-f0-9]{8}(-[a-f0-9]{4}){3}-[a-f0-9]{12}$`)

// BackendIdentity binds the existing request protocol's opaque instance to the
// complete managed activation tuple. A declaration alone grants no authority.
type BackendIdentity struct {
	AppID           string `json:"app_id"`
	DeploymentID    string `json:"deployment_id"`
	ControllerEpoch string `json:"controller_epoch"`
	InstallationID  string `json:"installation_id"`
	Generation      string `json:"generation"`
	PolicyRevision  string `json:"policy_revision"`
	ReleaseSHA256   string `json:"release_sha256"`
}

func (i BackendIdentity) instance() (string, error) {
	if !slugPattern.MatchString(i.AppID) || !digestPattern.MatchString(i.ReleaseSHA256) {
		return "", ErrBackendRegistration
	}
	for _, id := range []string{i.DeploymentID, i.ControllerEpoch, i.InstallationID} {
		if !backendUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return "", ErrBackendRegistration
		}
	}
	for _, s := range []string{i.Generation, i.PolicyRevision} {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 || strconv.FormatInt(n, 10) != s {
			return "", ErrBackendRegistration
		}
	}
	raw, _ := json.Marshal(struct {
		Purpose  string          `json:"purpose"`
		Identity BackendIdentity `json:"identity"`
	}{"portal.managed-backend-instance/1-dev.1", i})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

type BackendRegistrationMetadata struct {
	Identity   BackendIdentity `json:"identity"`
	InstanceID string          `json:"instance_id"`
	KeyID      string          `json:"key_id"`
}
type backendRegistrationWire struct {
	Format   string                      `json:"format"`
	Metadata BackendRegistrationMetadata `json:"metadata"`
	Key      []byte                      `json:"signing_key"`
}
type backendRegistrationState struct {
	mu       sync.Mutex
	closed   bool
	metadata BackendRegistrationMetadata
	key      []byte
}

// BackendRegistration holds only the per-generation backend verification key,
// never the installation identity seed or runner control key. Copies share Close.
type BackendRegistration struct{ state *backendRegistrationState }

func (BackendRegistration) MarshalJSON() ([]byte, error) { return nil, ErrBackendRegistration }
func (r *BackendRegistration) UnmarshalJSON([]byte) error {
	r.Close()
	*r = BackendRegistration{}
	return ErrBackendRegistration
}
func (BackendRegistration) String() string     { return "managed backend registration (redacted)" }
func (r BackendRegistration) GoString() string { return r.String() }
func (r BackendRegistration) Close() {
	if r.state == nil {
		return
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	clear(r.state.key)
	r.state.key = nil
	r.state.closed = true
}
func (r BackendRegistration) Metadata() (BackendRegistrationMetadata, error) {
	if r.state == nil {
		return BackendRegistrationMetadata{}, ErrBackendRegistration
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.closed {
		return BackendRegistrationMetadata{}, ErrBackendRegistration
	}
	return r.state.metadata, nil
}

func NewBackendRegistration(identity BackendIdentity, installationSeed []byte) (BackendRegistration, error) {
	instance, err := identity.instance()
	if err != nil || len(installationSeed) != 32 {
		return BackendRegistration{}, ErrBackendRegistration
	}
	frame, _ := json.Marshal([]string{"portal.managed-backend-signing-key/1-dev.1", instance})
	mac := hmac.New(sha256.New, installationSeed)
	mac.Write(frame)
	key := mac.Sum(nil)
	return backendRegistrationFromKey(identity, instance, key), nil
}
func backendRegistrationFromKey(identity BackendIdentity, instance string, key []byte) BackendRegistration {
	digest := sha256.Sum256(key)
	return BackendRegistration{state: &backendRegistrationState{metadata: BackendRegistrationMetadata{Identity: identity, InstanceID: instance, KeyID: hex.EncodeToString(digest[:])}, key: key}}
}

// Document is the explicit privileged export for a protected bootstrap file.
// The returned bytes contain a key and must be cleared after bounded delivery.
func (r BackendRegistration) Document() ([]byte, error) {
	if r.state == nil {
		return nil, ErrBackendRegistration
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.closed {
		return nil, ErrBackendRegistration
	}
	return json.Marshal(backendRegistrationWire{Format: "portal.managed-backend-registration/1-dev.1", Metadata: r.state.metadata, Key: r.state.key})
}

// ParseBackendRegistration is for the app's protected bootstrap reader. Parsing
// authenticates neither its writer nor the installation; filesystem/root-channel
// provenance and current lifecycle authority are separately mandatory.
func ParseBackendRegistration(raw []byte) (BackendRegistration, error) {
	var w backendRegistrationWire
	if len(raw) == 0 || len(raw) > 4096 || json.Unmarshal(raw, &w) != nil {
		clear(w.Key)
		return BackendRegistration{}, ErrBackendRegistration
	}
	defer clear(w.Key)
	canonical, err := json.Marshal(w)
	defer clear(canonical)
	instance, idErr := w.Metadata.Identity.instance()
	digest := sha256.Sum256(w.Key)
	if err != nil || idErr != nil || !bytes.Equal(canonical, raw) || w.Format != "portal.managed-backend-registration/1-dev.1" || len(w.Key) != 32 || instance != w.Metadata.InstanceID || hex.EncodeToString(digest[:]) != w.Metadata.KeyID {
		return BackendRegistration{}, ErrBackendRegistration
	}
	return backendRegistrationFromKey(w.Metadata.Identity, instance, bytes.Clone(w.Key)), nil
}

// Signer reuses ADR 0040's exact request/body/boot binding. The opaque instance
// commits deployment/epoch/installation/generation/policy/release together.
// A returned key copy is private and must be cleared by its caller after use.
func (r BackendRegistration) Signer(bootID string) (protocol.Signer, error) {
	if r.state == nil || len(bootID) > 128 {
		return protocol.Signer{}, ErrBackendRegistration
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.closed {
		return protocol.Signer{}, ErrBackendRegistration
	}
	m := r.state.metadata
	return protocol.Signer{Issuer: "portal.app/" + m.Identity.DeploymentID, Audience: "portal.app/" + m.Identity.InstallationID, AppID: m.Identity.AppID, InstanceID: m.InstanceID, BootID: bootID, KeyID: m.KeyID, Key: bytes.Clone(r.state.key)}, nil
}
