package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const UpdateStartBackendRequestFormat = "portal.runner.update-start-backend-request/1-dev.1"
const updateStartBackendRequestSignaturePurpose = "portal.app/runner-update-start-backend-request/v1"

// A per-request authorization for the generation selected by an update-start
// registration. It cannot start, register, activate or grant viewer authority.
type UpdateStartBackendRequestCommand struct {
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

type VerifiedUpdateStartBackendRequest struct {
	command      UpdateStartBackendRequestCommand
	registration VerifiedUpdateStartRegistrationCommand
}

func (v VerifiedUpdateStartBackendRequest) Command() UpdateStartBackendRequestCommand {
	c := v.command
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	return c
}

func (v VerifiedUpdateStartBackendRequest) Registration() VerifiedUpdateStartRegistrationCommand {
	return v.registration
}

func VerifyUpdateStartBackendRequest(raw, signature []byte, a Authority, nowMS int64) (VerifiedUpdateStartBackendRequest, error) {
	var zero VerifiedUpdateStartBackendRequest
	var c UpdateStartBackendRequestCommand
	if len(raw) == 0 || len(raw) > MaxCommandBytes ||
		len(signature) != ed25519.SignatureSize || !utf8.Valid(raw) {
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
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	registration, err := VerifyUpdateStartRegistration(
		c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	if c.Format != UpdateStartBackendRequestFormat ||
		!candidateUUID(c.RequestID) || len(c.BootID) == 0 ||
		len(c.BootID) > 128 || !utf8.ValidString(c.BootID) ||
		!appplatform.ValidBackendRequestRoute(c.Method, c.Path) ||
		!shaPattern.MatchString(c.BodySHA256) ||
		!shaPattern.MatchString(c.AssertionSHA256) ||
		!ed25519.Verify(
			ed25519.PublicKey(a.PublicKey),
			frame(updateStartBackendRequestSignaturePurpose, raw),
			signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedUpdateStartBackendRequest{
		command: c, registration: registration,
	}, nil
}

func SignUpdateStartBackendRequest(c UpdateStartBackendRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	signature := ed25519.Sign(
		key, frame(updateStartBackendRequestSignaturePurpose, raw))
	if _, err := VerifyUpdateStartBackendRequest(
		raw, signature, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, signature, nil
}

func ValidateUpdateStartBackendRequestData(v VerifiedUpdateStartBackendRequest, body []byte, assertion string) error {
	c := v.command
	if c.Format != UpdateStartBackendRequestFormat {
		return ErrInvalidCommand
	}
	return validateBackendRequestData(
		c.BootID, c.Method, c.Path, c.BodySHA256,
		c.AssertionSHA256, body, assertion)
}
