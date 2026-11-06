package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const ManagedRemovalFormat = "portal.runner.managed-removal-command/1-dev.1"
const managedRemovalSignaturePurpose = "portal.app/runner-managed-removal-command/v1"
const managedRemovalEffectPurpose = "portal.app/runner-managed-removal-effect/v1"

// ManagedRemovalPayload references one completed candidate start. The runner
// independently reloads the original command, data receipt and running receipt;
// this signed reference does not itself establish native authority.
type ManagedRemovalPayload struct {
	ResourceID          string `json:"resource_id"`
	DataNamespaceID     string `json:"data_namespace_id"`
	ReleaseSHA256       string `json:"release_sha256"`
	Platform            string `json:"platform"`
	SourceOperationID   string `json:"source_operation_id"`
	SourceEffectID      string `json:"source_effect_id"`
	SourceReceiptSHA256 string `json:"source_receipt_sha256"`
}

type ManagedRemovalCommand struct {
	Format            string                `json:"format"`
	DeploymentID      string                `json:"deployment_id"`
	ControllerEpoch   string                `json:"controller_epoch"`
	ControlRevision   string                `json:"control_revision"`
	InstallationID    string                `json:"installation_id"`
	OperationID       string                `json:"operation_id"`
	TargetGeneration  string                `json:"target_generation"`
	Stage             string                `json:"stage"`
	EffectNumber      string                `json:"effect_number"`
	EffectID          string                `json:"effect_id"`
	Kind              string                `json:"kind"`
	InstallationFence string                `json:"installation_fence"`
	PayloadSHA256     string                `json:"payload_sha256"`
	IssuedMS          string                `json:"issued_ms"`
	ExpiresMS         string                `json:"expires_ms"`
	Payload           ManagedRemovalPayload `json:"payload"`
}

type VerifiedManagedRemovalCommand struct{ command ManagedRemovalCommand }

func (v VerifiedManagedRemovalCommand) Command() ManagedRemovalCommand { return v.command }

func validManagedRemovalLogical(c ManagedRemovalCommand) bool {
	if c.Format != ManagedRemovalFormat ||
		!((c.Stage == "stop" && c.Kind == "stop") || (c.Stage == "remove_code" && c.Kind == "remove-code")) ||
		(c.Payload.Platform != "linux/amd64" && c.Payload.Platform != "linux/arm64") ||
		!shaPattern.MatchString(c.Payload.ReleaseSHA256) ||
		!shaPattern.MatchString(c.Payload.SourceReceiptSHA256) {
		return false
	}
	for _, id := range []string{
		c.DeploymentID,
		c.ControllerEpoch,
		c.InstallationID,
		c.OperationID,
		c.EffectID,
		c.Payload.ResourceID,
		c.Payload.DataNamespaceID,
		c.Payload.SourceOperationID,
		c.Payload.SourceEffectID,
	} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, number := range []string{c.TargetGeneration, c.EffectNumber} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	slot, _ := counter(c.EffectNumber, true)
	return slot <= 32
}

func ManagedRemovalLogicalDigest(c ManagedRemovalCommand) (string, error) {
	if !validManagedRemovalLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string                `json:"format"`
		DeploymentID     string                `json:"deployment_id"`
		ControllerEpoch  string                `json:"controller_epoch"`
		InstallationID   string                `json:"installation_id"`
		OperationID      string                `json:"operation_id"`
		TargetGeneration string                `json:"target_generation"`
		Stage            string                `json:"stage"`
		EffectNumber     string                `json:"effect_number"`
		Kind             string                `json:"kind"`
		Payload          ManagedRemovalPayload `json:"payload"`
	}{"portal.runner.managed-removal-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(managedRemovalEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseManagedRemoval(raw []byte) (ManagedRemovalCommand, error) {
	var c ManagedRemovalCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) {
		return c, ErrInvalidCommand
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !strictObject(scan, 1) {
		return c, ErrInvalidCommand
	}
	if _, err := scan.Token(); err != io.EOF {
		return c, ErrInvalidCommand
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(&c) != nil {
		return ManagedRemovalCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyManagedRemoval(raw, signature []byte, authority Authority, nowMS int64) (VerifiedManagedRemovalCommand, error) {
	fail := func() (VerifiedManagedRemovalCommand, error) {
		return VerifiedManagedRemovalCommand{}, ErrInvalidCommand
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseManagedRemoval(raw)
	if err != nil {
		return fail()
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != authority.ControlRevision || c.DeploymentID != authority.DeploymentID || c.ControllerEpoch != authority.ControllerEpoch {
		return fail()
	}
	if _, ok := counter(c.InstallationFence, true); !ok {
		return fail()
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return fail()
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || issued > nowMS || nowMS >= expires {
		return fail()
	}
	digest, err := ManagedRemovalLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(managedRemovalSignaturePurpose, raw), signature) {
		return fail()
	}
	return VerifiedManagedRemovalCommand{command: c}, nil
}

func SignManagedRemoval(c ManagedRemovalCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(managedRemovalSignaturePurpose, raw))
	if _, err := VerifyManagedRemoval(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
