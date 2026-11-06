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

const WritableCandidateFormat = "portal.runner.writable-candidate-command/1-dev.1"
const WritableCandidateSandboxProfile = "linux-docker-rootful-projectquota-writable/v1-dev"
const writableCandidateSignaturePurpose = "portal.app/runner-writable-candidate/v1"

// WritableCandidateCommand binds start intent to one completed writer receipt.
// It selects no path, permission mode, executable, environment or Docker option.
// The embedded writer is a current authenticated reference, not a new attempt.
type WritableCandidateCommand struct {
	Format              string `json:"format"`
	EffectID            string `json:"effect_id"`
	PayloadSHA256       string `json:"payload_sha256"`
	WriterRaw           []byte `json:"writer_raw"`
	WriterSignature     []byte `json:"writer_signature"`
	WriterReceiptSHA256 string `json:"writer_receipt_sha256"`
	SandboxProfile      string `json:"sandbox_profile"`
}

type VerifiedWritableCandidateCommand struct {
	command WritableCandidateCommand
	writer  VerifiedDataWriterCommand
}

func (v VerifiedWritableCandidateCommand) Command() WritableCandidateCommand {
	c := v.command
	c.WriterRaw, c.WriterSignature = bytes.Clone(c.WriterRaw), bytes.Clone(c.WriterSignature)
	return c
}
func (v VerifiedWritableCandidateCommand) Writer() VerifiedDataWriterCommand {
	return VerifiedDataWriterCommand{command: v.writer.Command(), data: v.writer.Data()}
}

// Index is a projection for durable indexing, never a signed legacy candidate
// command or executable handle. Legacy VerifyCandidate rejects this format.
func (v VerifiedWritableCandidateCommand) Index() CandidateCommand {
	c, w, ref := v.command, v.writer.Command(), v.writer.Data()
	return CandidateCommand{Format: WritableCandidateFormat, DeploymentID: ref.DeploymentID, ControllerEpoch: ref.ControllerEpoch, ControlRevision: ref.ControlRevision, InstallationID: ref.InstallationID, OperationID: ref.OperationID, TargetGeneration: ref.TargetGeneration, Stage: "start", EffectNumber: "1", EffectID: c.EffectID, Kind: "start-candidate", InstallationFence: ref.InstallationFence, PayloadSHA256: c.PayloadSHA256, IssuedMS: ref.IssuedMS, ExpiresMS: ref.ExpiresMS, Payload: CandidatePayload{ResourceID: w.ResourceID, DataNamespaceID: ref.Payload.DataNamespaceID, ReleaseSHA256: ref.Payload.ReleaseSHA256, Platform: w.Platform, DataFormat: ref.Payload.DataFormat, WriterFence: ref.Payload.WriterFence, SandboxProfile: c.SandboxProfile}}
}

func WritableCandidateLogicalDigest(c WritableCandidateCommand) (string, error) {
	w, err := parseDataWriter(c.WriterRaw)
	if err != nil || c.Format != WritableCandidateFormat || !candidateUUID(c.EffectID) || len(c.WriterSignature) != ed25519.SignatureSize || !shaPattern.MatchString(c.WriterReceiptSHA256) || c.SandboxProfile != WritableCandidateSandboxProfile {
		return "", ErrInvalidCommand
	}
	digest, err := DataWriterLogicalDigest(w)
	if err != nil || digest != w.PayloadSHA256 {
		return "", ErrInvalidCommand
	}
	ref, err := parse(w.DataRaw)
	if err != nil {
		return "", ErrInvalidCommand
	}
	for _, id := range []string{w.EffectID, ref.EffectID, w.ResourceID, ref.Payload.DataNamespaceID} {
		if c.EffectID == id {
			return "", ErrInvalidCommand
		}
	}
	logical := struct {
		Format              string `json:"format"`
		WriterEffectID      string `json:"writer_effect_id"`
		WriterPayloadSHA256 string `json:"writer_payload_sha256"`
		WriterReceiptSHA256 string `json:"writer_receipt_sha256"`
		SandboxProfile      string `json:"sandbox_profile"`
	}{"portal.runner.writable-candidate-effect/1-dev.1", w.EffectID, w.PayloadSHA256, c.WriterReceiptSHA256, c.SandboxProfile}
	raw, _ := json.Marshal(logical)
	sum := sha256.Sum256(frame("portal.app/runner-writable-candidate-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}

func parseWritableCandidate(raw []byte) (WritableCandidateCommand, error) {
	var c WritableCandidateCommand
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
		return WritableCandidateCommand{}, ErrInvalidCommand
	}
	return c, nil
}

func VerifyWritableCandidate(raw, signature []byte, a Authority, nowMS int64) (VerifiedWritableCandidateCommand, error) {
	var zero VerifiedWritableCandidateCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	c, err := parseWritableCandidate(raw)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	w, err := VerifyDataWriter(c.WriterRaw, c.WriterSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	digest, err := WritableCandidateLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(writableCandidateSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedWritableCandidateCommand{command: c, writer: w}, nil
}

func SignWritableCandidate(c WritableCandidateCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(writableCandidateSignaturePurpose, raw))
	if _, err := VerifyWritableCandidate(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}
