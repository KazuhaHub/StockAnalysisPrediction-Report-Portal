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

const CandidateFormat = "portal.runner.candidate-command/1-dev.1"
const CandidateSandboxProfile = "linux-docker-rootful-projectquota/v1-dev"
const candidateSignaturePurpose = "portal.app/runner-candidate-command/v1"
const candidateEffectPurpose = "portal.app/runner-candidate-effect/v1"

// CandidatePayload selects platform-owned identities and an exact release, not
// a host path, Docker option, arbitrary executable or credential. Release bytes
// independently determine the reviewed image/resources at physical dispatch.
type CandidatePayload struct {
	ResourceID      string `json:"resource_id"`
	DataNamespaceID string `json:"data_namespace_id"`
	ReleaseSHA256   string `json:"release_sha256"`
	Platform        string `json:"platform"`
	DataFormat      string `json:"data_format"`
	WriterFence     string `json:"writer_fence"`
	SandboxProfile  string `json:"sandbox_profile"`
}
type CandidateCommand struct {
	Format            string           `json:"format"`
	DeploymentID      string           `json:"deployment_id"`
	ControllerEpoch   string           `json:"controller_epoch"`
	ControlRevision   string           `json:"control_revision"`
	InstallationID    string           `json:"installation_id"`
	OperationID       string           `json:"operation_id"`
	TargetGeneration  string           `json:"target_generation"`
	Stage             string           `json:"stage"`
	EffectNumber      string           `json:"effect_number"`
	EffectID          string           `json:"effect_id"`
	Kind              string           `json:"kind"`
	InstallationFence string           `json:"installation_fence"`
	PayloadSHA256     string           `json:"payload_sha256"`
	IssuedMS          string           `json:"issued_ms"`
	ExpiresMS         string           `json:"expires_ms"`
	Payload           CandidatePayload `json:"payload"`
}
type VerifiedCandidateCommand struct{ command CandidateCommand }

func (v VerifiedCandidateCommand) Command() CandidateCommand { return v.command }
func candidateUUID(value string) bool {
	return uuidPattern.MatchString(value) && value != "00000000-0000-0000-0000-000000000000"
}
func validCandidateLogical(c CandidateCommand) bool {
	if c.Format != CandidateFormat || c.Stage != "start" || c.Kind != "start-candidate" || c.Payload.SandboxProfile != CandidateSandboxProfile || (c.Payload.Platform != "linux/amd64" && c.Payload.Platform != "linux/arm64") || !shaPattern.MatchString(c.Payload.ReleaseSHA256) {
		return false
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.OperationID, c.Payload.ResourceID, c.Payload.DataNamespaceID} {
		if !candidateUUID(id) {
			return false
		}
	}
	for _, number := range []string{c.TargetGeneration, c.EffectNumber, c.Payload.DataFormat, c.Payload.WriterFence} {
		if _, ok := counter(number, true); !ok {
			return false
		}
	}
	slot, _ := counter(c.EffectNumber, true)
	format, _ := counter(c.Payload.DataFormat, true)
	return slot <= 32 && format <= 1000000
}

// CandidateLogicalDigest keeps transport renewal/fence revisions outside stable
// effect identity. Resource, release, namespace, generation and writer identity
// remain immutable. Durable floors/replay admission must still enforce renewal.
func CandidateLogicalDigest(c CandidateCommand) (string, error) {
	if !validCandidateLogical(c) {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format           string           `json:"format"`
		DeploymentID     string           `json:"deployment_id"`
		ControllerEpoch  string           `json:"controller_epoch"`
		OperationID      string           `json:"operation_id"`
		InstallationID   string           `json:"installation_id"`
		TargetGeneration string           `json:"target_generation"`
		Stage            string           `json:"stage"`
		EffectNumber     string           `json:"effect_number"`
		Kind             string           `json:"kind"`
		Payload          CandidatePayload `json:"payload"`
	}{"portal.runner.candidate-effect/1-dev.1", c.DeploymentID, c.ControllerEpoch, c.OperationID, c.InstallationID, c.TargetGeneration, c.Stage, c.EffectNumber, c.Kind, c.Payload}
	raw, err := json.Marshal(logical)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(frame(candidateEffectPurpose, raw))
	return hex.EncodeToString(sum[:]), nil
}
func parseCandidate(raw []byte) (CandidateCommand, error) {
	var c CandidateCommand
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
		return CandidateCommand{}, ErrInvalidCommand
	}
	return c, nil
}

// VerifyCandidate authenticates one bounded candidate intent under an already
// trusted controller snapshot. It does not admit a journal effect, prove native
// paths/quotas, create/start a container or grant active data-write authority.
func VerifyCandidate(raw, signature []byte, authority Authority, nowMS int64) (VerifiedCandidateCommand, error) {
	fail := func() (VerifiedCandidateCommand, error) { return VerifiedCandidateCommand{}, ErrInvalidCommand }
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.ControlRevision < 1 || nowMS < 0 || len(signature) != ed25519.SignatureSize {
		return fail()
	}
	keySHA := sha256.Sum256(authority.PublicKey)
	if authority.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return fail()
	}
	c, err := parseCandidate(raw)
	if err != nil {
		return fail()
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != authority.ControlRevision || c.DeploymentID != authority.DeploymentID || c.ControllerEpoch != authority.ControllerEpoch || !candidateUUID(c.EffectID) {
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
	digest, err := CandidateLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return fail()
	}
	if !ed25519.Verify(ed25519.PublicKey(authority.PublicKey), frame(candidateSignaturePurpose, raw), signature) {
		return fail()
	}
	return VerifiedCandidateCommand{command: c}, nil
}
func SignCandidate(c CandidateCommand, key ed25519.PrivateKey, authority Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(candidateSignaturePurpose, raw))
	if _, err := VerifyCandidate(raw, signature, authority, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
