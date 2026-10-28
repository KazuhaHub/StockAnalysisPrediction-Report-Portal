package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const BackendRegistrationFormat = "portal.runner.backend-registration-command/1-dev.1"
const backendRegistrationSignaturePurpose = "portal.app/runner-backend-registration/v1"

// Contains only a digest of private registration material. CandidateRaw and its
// signature are a separately authenticated, renewable reference to the original
// start effect. No filename, URL or executable option can be supplied.
type BackendRegistrationCommand struct {
	Format             string `json:"format"`
	EffectID           string `json:"effect_id"`
	PayloadSHA256      string `json:"payload_sha256"`
	CandidateRaw       []byte `json:"candidate_raw"`
	CandidateSignature []byte `json:"candidate_signature"`
	DocumentSHA256     string `json:"document_sha256"`
}

type VerifiedBackendRegistrationCommand struct {
	command   BackendRegistrationCommand
	candidate CandidateCommand
}

func (v VerifiedBackendRegistrationCommand) Command() BackendRegistrationCommand {
	c := v.command
	c.CandidateRaw, c.CandidateSignature = bytes.Clone(c.CandidateRaw), bytes.Clone(c.CandidateSignature)
	return c
}
func (v VerifiedBackendRegistrationCommand) Candidate() CandidateCommand { return v.candidate }

func BackendRegistrationLogicalDigest(c BackendRegistrationCommand) (string, error) {
	ref, err := parseCandidate(c.CandidateRaw)
	if err != nil || c.Format != BackendRegistrationFormat || !candidateUUID(c.EffectID) || c.EffectID == ref.EffectID || len(c.CandidateSignature) != 64 || !shaPattern.MatchString(c.DocumentSHA256) {
		return "", ErrInvalidCommand
	}
	refDigest, err := CandidateLogicalDigest(ref)
	if err != nil || refDigest != ref.PayloadSHA256 {
		return "", ErrInvalidCommand
	}
	logical := struct {
		Format                 string `json:"format"`
		DeploymentID           string `json:"deployment_id"`
		ControllerEpoch        string `json:"controller_epoch"`
		InstallationID         string `json:"installation_id"`
		OperationID            string `json:"operation_id"`
		Generation             string `json:"generation"`
		ResourceID             string `json:"resource_id"`
		CandidateEffectID      string `json:"candidate_effect_id"`
		CandidatePayloadSHA256 string `json:"candidate_payload_sha256"`
		DocumentSHA256         string `json:"document_sha256"`
	}{"portal.runner.backend-registration-effect/1-dev.1", ref.DeploymentID, ref.ControllerEpoch, ref.InstallationID, ref.OperationID, ref.TargetGeneration, ref.Payload.ResourceID, ref.EffectID, ref.PayloadSHA256, c.DocumentSHA256}
	raw, _ := json.Marshal(logical)
	sum := sha256.Sum256(frame("portal.app/runner-backend-registration-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseBackendRegistration(raw []byte) (BackendRegistrationCommand, error) {
	var c BackendRegistrationCommand
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
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return BackendRegistrationCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyBackendRegistration(raw, signature []byte, a Authority, nowMS int64) (VerifiedBackendRegistrationCommand, error) {
	var zero VerifiedBackendRegistrationCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	c, err := parseBackendRegistration(raw)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	ref, err := VerifyCandidate(c.CandidateRaw, c.CandidateSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	digest, err := BackendRegistrationLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(backendRegistrationSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedBackendRegistrationCommand{command: c, candidate: ref.Command()}, nil
}

func SignBackendRegistration(c BackendRegistrationCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, raw))
	if _, err := VerifyBackendRegistration(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

// Validates secret bytes separately from the journal/wire command. A successful
// parse is not native file delivery, current health or a registration receipt.
func ValidateBackendRegistrationDocument(v VerifiedBackendRegistrationCommand, descriptor, document []byte) (appplatform.BackendRegistration, error) {
	var zero appplatform.BackendRegistration
	c := v.candidate
	if v.command.Format != BackendRegistrationFormat || len(document) == 0 || len(document) > 4096 {
		return zero, ErrInvalidCommand
	}
	sum := sha256.Sum256(document)
	if hex.EncodeToString(sum[:]) != v.command.DocumentSHA256 {
		return zero, ErrInvalidCommand
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil || parsed.SHA256 != c.Payload.ReleaseSHA256 {
		return zero, ErrInvalidCommand
	}
	registration, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	m, err := registration.Metadata()
	i := m.Identity
	// Only the current initial-install policy exists in this experimental path.
	if err != nil || i.AppID != parsed.Manifest.ID || i.DeploymentID != c.DeploymentID || i.ControllerEpoch != c.ControllerEpoch || i.InstallationID != c.InstallationID || i.Generation != c.TargetGeneration || i.Generation != "1" || i.PolicyRevision != "1" || i.ReleaseSHA256 != c.Payload.ReleaseSHA256 {
		registration.Close()
		return zero, ErrInvalidCommand
	}
	return registration, nil
}
