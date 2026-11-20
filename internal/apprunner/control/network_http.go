package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const NetworkHTTPRequestFormat = "portal.runner.viewer-http-request/1-dev.1"
const networkHTTPRequestPurpose = "portal.app/runner-viewer-http/v1"
const NetworkHTTPStreamRequestFormat = "portal.runner.viewer-http-stream-request/1-dev.1"
const networkHTTPStreamRequestPurpose = "portal.app/runner-viewer-http-stream/v1"

// NetworkHTTPRequestCommand binds one foreground request to a reviewed policy
// and opaque current viewer scope. It is not a service/background delegation or
// proof of current SQL authority. The caller must recheck that authority online.
type NetworkHTTPRequestCommand struct {
	Format          string `json:"format"`
	DeploymentID    string `json:"deployment_id"`
	ControllerEpoch string `json:"controller_epoch"`
	ControlRevision string `json:"control_revision"`
	RequestID       string `json:"request_id"`
	InstallationID  string `json:"installation_id"`
	ReviewPlanID    string `json:"review_plan_id"`
	Generation      string `json:"generation"`
	PolicyRevision  string `json:"policy_revision"`
	ReleaseSHA256   string `json:"release_sha256"`
	ActorScope      string `json:"actor_scope"`
	PolicyRaw       []byte `json:"policy_raw"`
	PolicySHA256    string `json:"policy_sha256"`
	// PrivateNetworksSHA256 is the runner allowlist digest the reviewed policy
	// was checked against. It is present exactly when the policy has a private
	// target; every other command (v1, public-only v2, stream) omits it.
	PrivateNetworksSHA256 string `json:"private_networks_sha256,omitempty"`
	Method                string `json:"method"`
	URL                   string `json:"url"`
	Accept                string `json:"accept"`
	ContentType           string `json:"content_type"`
	BodyBytes             string `json:"body_bytes"`
	BodySHA256            string `json:"body_sha256"`
	CredentialName        string `json:"credential_name,omitempty"`
	CredentialHeader      string `json:"credential_header,omitempty"`
	CredentialValue       []byte `json:"credential_value,omitempty"`
	// SigningScheme and the signed headers replace the credential header and value for a provider
	// that signs each request (doc 177). Portal computed the headers from a key the runner never
	// holds; the runner sets exactly these and no others, so it cannot change what was signed.
	// The headers travel as two parallel scalar arrays (names, then base64 values) because a
	// command is a flat object whose arrays hold only scalars; SignedHeaderList and
	// SetSignedHeaders convert.
	SigningScheme      string   `json:"signing_scheme,omitempty"`
	SignedHeaderNames  []string `json:"signed_header_names,omitempty"`
	SignedHeaderValues [][]byte `json:"signed_header_values,omitempty"`
	IssuedMS           string   `json:"issued_ms"`
	ExpiresMS          string   `json:"expires_ms"`
}

type VerifiedNetworkHTTPRequest struct{ command NetworkHTTPRequestCommand }

func (v VerifiedNetworkHTTPRequest) Command() NetworkHTTPRequestCommand {
	c := v.command
	c.PolicyRaw = bytes.Clone(c.PolicyRaw)
	c.CredentialValue = bytes.Clone(c.CredentialValue)
	c.SignedHeaderNames = slices.Clone(c.SignedHeaderNames)
	if c.SignedHeaderValues != nil {
		values := make([][]byte, len(c.SignedHeaderValues))
		for i, v := range c.SignedHeaderValues {
			values[i] = bytes.Clone(v)
		}
		c.SignedHeaderValues = values
	}
	return c
}

// SetSignedHeaders stores headers (copying nothing: the command aliases the values) as the command's
// signed header arrays.
func (c *NetworkHTTPRequestCommand) SetSignedHeaders(headers []appplatform.SignedHeader) {
	c.SignedHeaderNames, c.SignedHeaderValues = nil, nil
	for _, h := range headers {
		c.SignedHeaderNames = append(c.SignedHeaderNames, h.Name)
		c.SignedHeaderValues = append(c.SignedHeaderValues, h.Value)
	}
}

// SignedHeaderList is the command's signed headers as pairs, aliasing the values; ok is false when
// the two arrays differ in length.
func (c NetworkHTTPRequestCommand) SignedHeaderList() (headers []appplatform.SignedHeader, ok bool) {
	if len(c.SignedHeaderNames) != len(c.SignedHeaderValues) {
		return nil, false
	}
	for i, name := range c.SignedHeaderNames {
		headers = append(headers, appplatform.SignedHeader{Name: name, Value: c.SignedHeaderValues[i]})
	}
	return headers, true
}

// ClearSignedHeaders wipes the signed header values the command holds.
func (c *NetworkHTTPRequestCommand) ClearSignedHeaders() {
	for _, v := range c.SignedHeaderValues {
		clear(v)
	}
	c.SignedHeaderNames, c.SignedHeaderValues = nil, nil
}

// Clear releases the decrypted credential copy held by a verified command.
func (v *VerifiedNetworkHTTPRequest) Clear() {
	if v == nil {
		return
	}
	clear(v.command.CredentialValue)
	v.command.CredentialValue = nil
	v.command.ClearSignedHeaders()
}

func VerifyNetworkHTTPRequest(raw, signature []byte, a Authority, nowMS int64) (VerifiedNetworkHTTPRequest, error) {
	return verifyNetworkHTTPRequest(raw, signature, a, nowMS, NetworkHTTPRequestFormat, networkHTTPRequestPurpose, validateNetworkHTTPRequest)
}

func VerifyNetworkHTTPStreamRequest(raw, signature []byte, a Authority, nowMS int64) (VerifiedNetworkHTTPRequest, error) {
	return verifyNetworkHTTPRequest(raw, signature, a, nowMS, NetworkHTTPStreamRequestFormat, networkHTTPStreamRequestPurpose, validateNetworkHTTPStreamRequest)
}

func verifyNetworkHTTPRequest(raw, signature []byte, a Authority, nowMS int64, format, purpose string, validate func(NetworkHTTPRequestCommand, Authority, int64) error) (VerifiedNetworkHTTPRequest, error) {
	var zero VerifiedNetworkHTTPRequest
	if len(raw) == 0 || len(raw) > MaxCommandBytes || !utf8.Valid(raw) || len(signature) != ed25519.SignatureSize || len(a.PublicKey) != ed25519.PublicKeySize || a.ControlRevision < 1 || nowMS < 0 {
		return zero, ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
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
	var c NetworkHTTPRequestCommand
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, raw) || c.Format != format {
		return zero, ErrInvalidCommand
	}
	if validate(c, a, nowMS) != nil {
		return zero, ErrInvalidCommand
	}
	if !ed25519.Verify(ed25519.PublicKey(a.PublicKey), frame(purpose, raw), signature) {
		return zero, ErrInvalidCommand
	}
	return VerifiedNetworkHTTPRequest{command: c}, nil
}

func SignNetworkHTTPRequest(c NetworkHTTPRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	return signNetworkHTTPRequest(c, key, a, nowMS, networkHTTPRequestPurpose, VerifyNetworkHTTPRequest)
}

func SignNetworkHTTPStreamRequest(c NetworkHTTPRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64) ([]byte, []byte, error) {
	return signNetworkHTTPRequest(c, key, a, nowMS, networkHTTPStreamRequestPurpose, VerifyNetworkHTTPStreamRequest)
}

func signNetworkHTTPRequest(c NetworkHTTPRequestCommand, key ed25519.PrivateKey, a Authority, nowMS int64, purpose string, verify func([]byte, []byte, Authority, int64) (VerifiedNetworkHTTPRequest, error)) ([]byte, []byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, nil, ErrInvalidCommand
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, nil, ErrInvalidCommand
	}
	sig := ed25519.Sign(key, frame(purpose, raw))
	if _, err := verify(raw, sig, a, nowMS); err != nil {
		return nil, nil, err
	}
	return raw, sig, nil
}

func ValidateNetworkHTTPBody(v VerifiedNetworkHTTPRequest, body []byte) error {
	c := v.command
	if c.Format != NetworkHTTPRequestFormat {
		return ErrInvalidCommand
	}
	length, ok := counter(c.BodyBytes, false)
	if !ok || int64(len(body)) != length || length > 65536 {
		return ErrInvalidCommand
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != c.BodySHA256 {
		return ErrInvalidCommand
	}
	return nil
}

func ValidateNetworkHTTPStreamBody(v VerifiedNetworkHTTPRequest, body []byte) error {
	c := v.command
	if c.Format != NetworkHTTPStreamRequestFormat {
		return ErrInvalidCommand
	}
	length, ok := counter(c.BodyBytes, false)
	if !ok || int64(len(body)) != length || length > 65536 {
		return ErrInvalidCommand
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != c.BodySHA256 {
		return ErrInvalidCommand
	}
	return nil
}

func validateNetworkHTTPRequest(c NetworkHTTPRequestCommand, a Authority, nowMS int64) error {
	if c.Format != NetworkHTTPRequestFormat || nowMS < 0 || a.ControlRevision < 1 || len(a.PublicKey) != ed25519.PublicKeySize {
		return ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return ErrInvalidCommand
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.ReviewPlanID, c.RequestID} {
		if !candidateUUID(id) {
			return ErrInvalidCommand
		}
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != a.ControlRevision || c.DeploymentID != a.DeploymentID || c.ControllerEpoch != a.ControllerEpoch {
		return ErrInvalidCommand
	}
	for _, number := range []string{c.Generation, c.PolicyRevision} {
		if _, ok := counter(number, true); !ok {
			return ErrInvalidCommand
		}
	}
	for _, digest := range []string{c.ReleaseSHA256, c.ActorScope, c.BodySHA256} {
		if !shaPattern.MatchString(digest) || digest == strings.Repeat("0", 64) {
			return ErrInvalidCommand
		}
	}
	parsed, err := appplatform.ParseHTTPPolicyAny(c.PolicyRaw)
	if err != nil || parsed.SHA256 != c.PolicySHA256 || !appplatform.ValidHTTPPolicyHeaderValue(c.Accept) || !appplatform.ValidHTTPPolicyHeaderValue(c.ContentType) {
		return ErrInvalidCommand
	}
	target, err := parsed.Policy.Select(c.Method, c.URL)
	if err != nil {
		return ErrInvalidCommand
	}
	if !validNetworkCredential(c) || c.CredentialName != "" && target.Scheme != "https" {
		return ErrInvalidCommand
	}
	if parsed.Policy.HasPrivate() {
		if !shaPattern.MatchString(c.PrivateNetworksSHA256) || c.PrivateNetworksSHA256 == strings.Repeat("0", 64) {
			return ErrInvalidCommand
		}
	} else if c.PrivateNetworksSHA256 != "" {
		return ErrInvalidCommand
	}
	length, ok := counter(c.BodyBytes, false)
	if !ok || length > target.RequestBytes {
		return ErrInvalidCommand
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 30000 || expires-issued < target.Deadline.Milliseconds() || issued > nowMS || nowMS >= expires {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || len(canonical) > MaxCommandBytes {
		return ErrInvalidCommand
	}
	return nil
}

func validateNetworkHTTPStreamRequest(c NetworkHTTPRequestCommand, a Authority, nowMS int64) error {
	if c.Format != NetworkHTTPStreamRequestFormat || nowMS < 0 || a.ControlRevision < 1 || len(a.PublicKey) != ed25519.PublicKeySize {
		return ErrInvalidCommand
	}
	keySHA := sha256.Sum256(a.PublicKey)
	if a.Fingerprint != hex.EncodeToString(keySHA[:]) {
		return ErrInvalidCommand
	}
	for _, id := range []string{c.DeploymentID, c.ControllerEpoch, c.InstallationID, c.ReviewPlanID, c.RequestID} {
		if !candidateUUID(id) {
			return ErrInvalidCommand
		}
	}
	revision, ok := counter(c.ControlRevision, true)
	if !ok || revision != a.ControlRevision || c.DeploymentID != a.DeploymentID || c.ControllerEpoch != a.ControllerEpoch {
		return ErrInvalidCommand
	}
	for _, number := range []string{c.Generation, c.PolicyRevision} {
		if _, ok := counter(number, true); !ok {
			return ErrInvalidCommand
		}
	}
	for _, digest := range []string{c.ReleaseSHA256, c.ActorScope, c.BodySHA256} {
		if !shaPattern.MatchString(digest) || digest == strings.Repeat("0", 64) {
			return ErrInvalidCommand
		}
	}
	parsed, err := appplatform.ParseHTTPStreamPolicy(c.PolicyRaw)
	if c.PrivateNetworksSHA256 != "" {
		return ErrInvalidCommand
	}
	if err != nil ||
		parsed.SHA256 != c.PolicySHA256 ||
		appplatform.ValidateHTTPStreamPolicyTarget(parsed.Policy, c.Method, c.URL) != nil ||
		!appplatform.ValidHTTPPolicyHeaderValue(c.Accept) ||
		!appplatform.ValidHTTPPolicyHeaderValue(c.ContentType) {
		return ErrInvalidCommand
	}
	if !validNetworkCredential(c) || c.SigningScheme != "" || len(c.SignedHeaderNames) != 0 || len(c.SignedHeaderValues) != 0 {
		return ErrInvalidCommand
	}
	length, ok := counter(c.BodyBytes, false)
	if !ok || length > parsed.Policy.RequestBytes {
		return ErrInvalidCommand
	}
	issued, ok := counter(c.IssuedMS, false)
	if !ok {
		return ErrInvalidCommand
	}
	expires, ok := counter(c.ExpiresMS, false)
	if !ok || expires <= issued || expires-issued > 40000 || expires-issued < parsed.Policy.Duration.Milliseconds() || issued > nowMS || nowMS >= expires {
		return ErrInvalidCommand
	}
	canonical, err := json.Marshal(c)
	if err != nil || len(canonical) > MaxCommandBytes {
		return ErrInvalidCommand
	}
	return nil
}

// ValidateNetworkHTTPRequestMetadata checks bounded logical metadata against
// current enrollment. It verifies neither a request signature nor SQL authority.
// Use only alongside VerifyNetworkHTTPRequest and current authorization.
func ValidateNetworkHTTPRequestMetadata(c NetworkHTTPRequestCommand, a Authority, nowMS int64) error {
	return validateNetworkHTTPRequest(c, a, nowMS)
}

func ValidateNetworkHTTPStreamRequestMetadata(c NetworkHTTPRequestCommand, a Authority, nowMS int64) error {
	return validateNetworkHTTPStreamRequest(c, a, nowMS)
}

func validNetworkCredentialName(name string) bool {
	if len(name) < 1 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, b := range []byte(name) {
		if !(b >= 'a' && b <= 'z') && !(b >= '0' && b <= '9') && b != '-' {
			return false
		}
	}
	return true
}

// validNetworkSigning is the shape of a signed call: the scheme, the binding's name, the closed
// header set Portal computed (checked against this very request) and no credential header or value.
func validNetworkSigning(c NetworkHTTPRequestCommand) bool {
	headers, ok := c.SignedHeaderList()
	return ok && c.SigningScheme == appplatform.ACS3Scheme && validNetworkCredentialName(c.CredentialName) &&
		c.CredentialHeader == "" && len(c.CredentialValue) == 0 &&
		appplatform.CheckACS3SignedHeaders(c.Method, c.URL, c.ContentType, c.BodySHA256, headers) == nil
}

func validNetworkCredential(c NetworkHTTPRequestCommand) bool {
	if c.SigningScheme != "" || len(c.SignedHeaderNames) != 0 || len(c.SignedHeaderValues) != 0 {
		return validNetworkSigning(c)
	}
	if c.CredentialName == "" && c.CredentialHeader == "" && len(c.CredentialValue) == 0 {
		return true
	}
	if !validNetworkCredentialName(c.CredentialName) {
		return false
	}
	if c.CredentialHeader != "Authorization" && c.CredentialHeader != "X-API-Key" {
		return false
	}
	if len(c.CredentialValue) < 1 || len(c.CredentialValue) > 16400 {
		return false
	}
	for _, b := range c.CredentialValue {
		if b < 32 || b > 126 {
			return false
		}
	}
	if c.CredentialHeader == "Authorization" {
		const prefix = "Bearer "
		if len(c.CredentialValue) <= len(prefix) || string(c.CredentialValue[:len(prefix)]) != prefix {
			return false
		}
	}
	return true
}
