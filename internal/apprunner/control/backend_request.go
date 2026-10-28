package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const BackendRequestFormat = "portal.runner.backend-request/1-dev.1"
const backendRequestSignaturePurpose = "portal.app/runner-backend-request/v1"

// A per-request control authorization, independently signed from registration.
// Body and backend assertion travel separately and must match both digests.
// This has no durable effect slot and cannot grant browser/viewer authority.
type BackendRequestCommand struct {
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
type VerifiedBackendRequest struct {
	command      BackendRequestCommand
	registration VerifiedBackendRegistrationCommand
}

func (v VerifiedBackendRequest) Command() BackendRequestCommand {
	c := v.command
	c.RegistrationRaw = bytes.Clone(c.RegistrationRaw)
	c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
	return c
}
func (v VerifiedBackendRequest) Registration() VerifiedBackendRegistrationCommand {
	return v.registration
}
func VerifyBackendRequest(raw, signature []byte, a Authority, nowMS int64) (VerifiedBackendRequest, error) {
	var zero VerifiedBackendRequest
	var c BackendRequestCommand
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
	registration, err := VerifyBackendRegistration(c.RegistrationRaw, c.RegistrationSignature, a, nowMS)
	if err != nil {
		return zero, ErrInvalidCommand
	}
	if c.Format != BackendRequestFormat || !candidateUUID(c.RequestID) || len(c.BootID) == 0 || len(c.BootID) > 128 || !utf8.ValidString(c.BootID) || !appplatform.ValidBackendRequestRoute(c.Method, c.Path) || !shaPattern.MatchString(c.BodySHA256) || !shaPattern.MatchString(c.AssertionSHA256) || !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(backendRequestSignaturePurpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedBackendRequest{command: c, registration: registration}, nil
}
func SignBackendRequest(c BackendRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	sig := ed25519.Sign(key, frame(backendRequestSignaturePurpose, raw))
	if _, err := VerifyBackendRequest(raw, sig, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, sig, nil
}
func ValidateBackendRequestData(v VerifiedBackendRequest, body []byte, assertion string) error {
	c := v.command
	if c.Format != BackendRequestFormat || !appplatform.ValidBackendRequestData(c.Method, c.Path, body, assertion) {
		return ErrInvalidCommand
	}
	encoded, _, ok := strings.Cut(assertion, ".")
	if !ok {
		return ErrInvalidCommand
	}
	claimsRaw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return ErrInvalidCommand
	}
	var claims protocol.Claims
	decoder := json.NewDecoder(bytes.NewReader(claimsRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&claims) != nil {
		return ErrInvalidCommand
	}
	canonical, _ := json.Marshal(claims)
	// Structural consistency is not MAC/viewer authentication. The application
	// verifier remains mandatory before executing its route.
	if !bytes.Equal(canonical, claimsRaw) || claims.Version != 1 || claims.BootID != c.BootID || claims.Method != c.Method || claims.Path != c.Path || claims.BodyDigest != c.BodySHA256 {
		return ErrInvalidCommand
	}
	bodySHA, assertionSHA := sha256.Sum256(body), sha256.Sum256([]byte(assertion))
	if hex.EncodeToString(bodySHA[:]) != c.BodySHA256 || hex.EncodeToString(assertionSHA[:]) != c.AssertionSHA256 {
		return ErrInvalidCommand
	}
	return nil
}
