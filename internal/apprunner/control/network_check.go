package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

const NetworkHTTPCheckFormat = "portal.runner.viewer-http-check/1-dev.1"
const networkHTTPCheckPurpose = "portal.app/runner-viewer-http-check/v1"
const MaxNetworkHTTPCheckBytes = 2048

// NetworkHTTPCheck is a short challenge-bound response, not an execution command,
// service delegation or cached grant. Sign only after current SQL authority checks.
type NetworkHTTPCheck struct {
	Format        string `json:"format"`
	RequestSHA256 string `json:"request_sha256"`
	Challenge     string `json:"challenge"`
	IssuedMS      string `json:"issued_ms"`
	ExpiresMS     string `json:"expires_ms"`
}

func networkHTTPCheckBinding(c NetworkHTTPRequestCommand, challenge string, a Authority, nowMS int64) (string, error) {
	if !shaPattern.MatchString(challenge) || challenge == strings.Repeat("0", 64) || validateNetworkHTTPRequest(c, a, nowMS) != nil {
		return "", ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", ErrInvalidCommand
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// The caller, not this cryptographic helper, must resolve the current live
// principal, exact consumed review/grant, installation, restore pause and policy.
func SignNetworkHTTPCheck(c NetworkHTTPRequestCommand, challenge string, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	digest, err := networkHTTPCheckBinding(c, challenge, a, nowMS)
	if err != nil {
		return nil, nil, err
	}
	expires, _ := counter(c.ExpiresMS, false)
	if expires-nowMS > 1000 {
		expires = nowMS + 1000
	}
	proof := NetworkHTTPCheck{Format: NetworkHTTPCheckFormat, RequestSHA256: digest, Challenge: challenge, IssuedMS: strconv.FormatInt(nowMS, 10), ExpiresMS: strconv.FormatInt(expires, 10)}
	raw, err := json.Marshal(proof)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	sig := ed25519.Sign(key, frame(networkHTTPCheckPurpose, raw))
	if VerifyNetworkHTTPCheck(c, challenge, raw, sig, a, nowMS) != nil {
		return nil, nil, ErrInvalidCommand
	}
	return raw, sig, nil
}

func VerifyNetworkHTTPCheck(c NetworkHTTPRequestCommand, challenge string, raw, signature []byte, a Authority, nowMS int64) error {
	if len(raw) == 0 || len(raw) > MaxNetworkHTTPCheckBytes || !utf8.Valid(raw) || len(signature) != ed25519.SignatureSize {
		return ErrInvalidCommand
	}
	digest, err := networkHTTPCheckBinding(c, challenge, a, nowMS)
	if err != nil {
		return err
	}
	var proof NetworkHTTPCheck
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&proof) != nil {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(raw, canonical) || proof.Format != NetworkHTTPCheckFormat || proof.RequestSHA256 != digest || proof.Challenge != challenge {
		return ErrInvalidCommand
	}
	issued, ok := counter(proof.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(proof.ExpiresMS, false)
	requestExpires, _ := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 1000 || issued > nowMS || nowMS >= expires || expires > requestExpires {
		return ErrInvalidCommand
	}
	if !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(networkHTTPCheckPurpose, raw), signature) {
		return ErrInvalidCommand
	}
	return nil
}
