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

const WritableBackendRegistrationFormat = "portal.runner.writable-backend-registration-command/1-dev.1"
const writableBackendRegistrationSignaturePurpose = "portal.app/runner-writable-backend-registration/v1"

// Digests only: private registration bytes never enter the signed journal
// command. The nested current reference does not authorize another start.
type WritableBackendRegistrationCommand struct {
	Format               string `json:"format"`
	EffectID             string `json:"effect_id"`
	PayloadSHA256        string `json:"payload_sha256"`
	CandidateRaw         []byte `json:"candidate_raw"`
	CandidateSignature   []byte `json:"candidate_signature"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
	DocumentSHA256       string `json:"document_sha256"`
}
type VerifiedWritableBackendRegistrationCommand struct {
	command   WritableBackendRegistrationCommand
	candidate VerifiedWritableCandidateCommand
}

func (v VerifiedWritableBackendRegistrationCommand) Command() WritableBackendRegistrationCommand {
	c := v.command
	c.CandidateRaw, c.CandidateSignature = bytes.Clone(c.CandidateRaw), bytes.Clone(c.CandidateSignature)
	return c
}
func (v VerifiedWritableBackendRegistrationCommand) Candidate() VerifiedWritableCandidateCommand {
	return VerifiedWritableCandidateCommand{command: v.candidate.Command(), writer: v.candidate.Writer()}
}
func WritableBackendRegistrationLogicalDigest(c WritableBackendRegistrationCommand) (string, error) {
	ref, err := parseWritableCandidate(c.CandidateRaw)
	if err != nil || c.Format != WritableBackendRegistrationFormat || !candidateUUID(c.EffectID) || len(c.CandidateSignature) != ed25519.SignatureSize || !shaPattern.MatchString(c.RunningReceiptSHA256) || !shaPattern.MatchString(c.DocumentSHA256) {
		return "", ErrInvalidCommand
	}
	digest, err := WritableCandidateLogicalDigest(ref)
	if err != nil || digest != ref.PayloadSHA256 {
		return "", ErrInvalidCommand
	}
	w, err := parseDataWriter(ref.WriterRaw)
	if err != nil {
		return "", ErrInvalidCommand
	}
	source, err := parse(w.DataRaw)
	if err != nil {
		return "", ErrInvalidCommand
	}
	for _, id := range []string{ref.EffectID, w.EffectID, w.ResourceID, source.EffectID, source.Payload.DataNamespaceID} {
		if c.EffectID == id {
			return "", ErrInvalidCommand
		}
	}
	logical := struct {
		Format                 string `json:"format"`
		CandidateEffectID      string `json:"candidate_effect_id"`
		CandidatePayloadSHA256 string `json:"candidate_payload_sha256"`
		RunningReceiptSHA256   string `json:"running_receipt_sha256"`
		DocumentSHA256         string `json:"document_sha256"`
	}{"portal.runner.writable-backend-registration-effect/1-dev.1", ref.EffectID, ref.PayloadSHA256, c.RunningReceiptSHA256, c.DocumentSHA256}
	raw, _ := json.Marshal(logical)
	sum := sha256.Sum256(frame("portal.app/runner-writable-backend-registration-effect/v1", raw))
	return hex.EncodeToString(sum[:]), nil
}
func parseWritableBackendRegistration(raw []byte) (WritableBackendRegistrationCommand, error) {
	var c WritableBackendRegistrationCommand
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return WritableBackendRegistrationCommand{}, ErrInvalidCommand
	}
	return c, nil
}
func VerifyWritableBackendRegistration(raw, signature []byte, a Authority, nowMS int64) (VerifiedWritableBackendRegistrationCommand, error) {
	var zero VerifiedWritableBackendRegistrationCommand
	if len(signature) != ed25519.SignatureSize {
		return zero, ErrInvalidCommand
	}
	c, err := parseWritableBackendRegistration(raw)
	if err != nil {
		return zero, err
	}
	ref, err := VerifyWritableCandidate(c.CandidateRaw, c.CandidateSignature, a, nowMS)
	if err != nil {
		return zero, err
	}
	digest, err := WritableBackendRegistrationLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(writableBackendRegistrationSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedWritableBackendRegistrationCommand{command: c, candidate: ref}, nil
}
func SignWritableBackendRegistration(c WritableBackendRegistrationCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(key, frame(writableBackendRegistrationSignaturePurpose, raw))
	if _, err := VerifyWritableBackendRegistration(raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

// Secret document validation remains separate from admission. Parsing does not
// prove delivery, readiness, registration consumption or a completed handshake.
func ValidateWritableBackendRegistrationDocument(v VerifiedWritableBackendRegistrationCommand, descriptor, document []byte) (appplatform.BackendRegistration, error) {
	if v.command.Format != WritableBackendRegistrationFormat {
		return appplatform.BackendRegistration{}, ErrInvalidCommand
	}
	return validateBackendRegistrationDocument(v.candidate.Index(), v.command.DocumentSHA256, descriptor, document)
}
