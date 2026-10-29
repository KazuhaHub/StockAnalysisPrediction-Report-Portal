package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const WritableBackendRequestFormat = "portal.runner.writable-backend-request/1-dev.1"
const writableBackendRequestSignaturePurpose = "portal.app/runner-writable-backend-request/v1"

// A per-request control authorization, independently signed from registration.
// Body and backend assertion travel separately and must match both digests.
// This has no durable effect slot and cannot grant browser/viewer authority.
type WritableBackendRequestCommand struct {
	Format                string `json:"format"`
	RequestID             string `json:"request_id"`
	RegistrationRaw       []byte `json:"registration_raw"`
	RegistrationSignature []byte `json:"registration_signature"`
	BootID                string `json:"boot_id"`
	Method                string `json:"method"`
	Path                  string `json:"path"`
	BodySHA256            string `json:"body_sha256"`
	AssertionSHA256       string `json:"assertion_sha256"`
}
type VerifiedWritableBackendRequest struct {
	command      WritableBackendRequestCommand
	registration VerifiedWritableBackendRegistrationCommand
}

func (v VerifiedWritableBackendRequest) Command() WritableBackendRequestCommand {
	c := v.command
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	return c
}
func (v VerifiedWritableBackendRequest) Registration() VerifiedWritableBackendRegistrationCommand {
	return v.registration
}
func VerifyWritableBackendRequest(raw, signature []byte, a Authority, nowMS int64) (VerifiedWritableBackendRequest, error) {
	var zero VerifiedWritableBackendRequest
	var c WritableBackendRequestCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes || len(signature) != ed25519.SignatureSize || !utf8.Valid(raw) {
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
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	registration, err := VerifyWritableBackendRegistration(c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	if c.Format != WritableBackendRequestFormat || !candidateUUID(c.RequestID) || len(c.BootID) == 0 || len(c.BootID) > 128 || !utf8.ValidString(c.BootID) || !appplatform.ValidBackendRequestRoute(c.Method, c.Path) || !shaPattern.MatchString(c.BodySHA256) || !shaPattern.MatchString(c.AssertionSHA256) || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(writableBackendRequestSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedWritableBackendRequest{command: c, registration: registration}, nil
}
func SignWritableBackendRequest(c WritableBackendRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	sig := ed25519.Sign(key, frame(writableBackendRequestSignaturePurpose, raw))
	if _, err := VerifyWritableBackendRequest(raw, sig, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, sig, nil
}
func ValidateWritableBackendRequestData(v VerifiedWritableBackendRequest, body []byte, assertion string) error {
	c := v.command
	if c.Format != WritableBackendRequestFormat {
		return ErrInvalidCommand
	}
	return validateBackendRequestData(c.BootID, c.Method, c.Path, c.BodySHA256, c.AssertionSHA256, body, assertion)
}
