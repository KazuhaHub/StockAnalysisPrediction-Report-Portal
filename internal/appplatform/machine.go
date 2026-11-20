package appplatform

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// The authenticated machine read API (doc 183). Nothing here grants access: these are the pure
// formats the Portal handlers and the runner share.

const MachinePolicyFormat = "portal.machine-read-policy/1-dev.1"
const MachineAssertionPrefix = "mch."
const MachineAssertionHeader = "X-Portal-Machine-Assertion"
const MachineAssertionLifetime = 30 * time.Second
const machineAssertionDomain = "rp-extension-machine-v1\n"

const (
	MaxMachineQueryValue          = 256
	MaxMachineQueryTotal          = 1024
	MaxMachineQueryRaw            = 4096
	DefaultMachineInstallation    = 5
	MaxMachineInstallation        = 50
	DefaultMachineConcurrency     = 4
	MaxMachineConcurrency         = 16
	DefaultMachinePerMinute       = 60
	MaxMachinePerMinute           = 600
	MaxMachineTokenLabel          = 64
	MaxMachineTokenCIDRs          = 8
	MaxMachineActiveTokens        = 16
	DefaultMachineExpiryDays      = 90
	MaxMachineExpiryDays          = 365
	MaxMachineRotationGraceDays   = 7
	MachineTokenPrefix            = "pmr_"
	MachineTokenHandleLength      = 12
	MachineTokenSecretLength      = 43
	MachineTokenLength            = len(MachineTokenPrefix) + MachineTokenHandleLength + 1 + MachineTokenSecretLength
	MaxMachineAuthorizationHeader = 128
	maxMachineAssertionLen        = 4096
)

var ErrMachinePolicy = errors.New("invalid machine read policy")
var ErrMachineQuery = errors.New("invalid machine query")
var ErrMachineAssertion = errors.New("invalid machine assertion")
var ErrMachineToken = errors.New("invalid machine token")

// MachinePolicy is the reviewed content of the machine.read grant row.
type MachinePolicy struct {
	Format                  string   `json:"format"`
	Routes                  []string `json:"routes"`
	InstallationPerSecond   int      `json:"installation_per_second"`
	InstallationConcurrency int      `json:"installation_concurrency"`
}

// DefaultMachinePolicy is the pre-filled review: every declared route and the default limits.
func DefaultMachinePolicy(m Machine) MachinePolicy {
	routes := make([]string, 0, len(m.Routes))
	for _, r := range m.Routes {
		routes = append(routes, r.ID)
	}
	return MachinePolicy{Format: MachinePolicyFormat, Routes: routes, InstallationPerSecond: DefaultMachineInstallation, InstallationConcurrency: DefaultMachineConcurrency}
}

func CanonicalMachinePolicy(p MachinePolicy) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}

func decodeMachinePolicy(raw string) (MachinePolicy, bool) {
	var p MachinePolicy
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 4096 || d.Decode(&p) != nil || CanonicalMachinePolicy(p) != raw || p.Format != MachinePolicyFormat ||
		len(p.Routes) < 1 || len(p.Routes) > maxMachineRoutes || p.InstallationPerSecond < 1 || p.InstallationPerSecond > MaxMachineInstallation ||
		p.InstallationConcurrency < 1 || p.InstallationConcurrency > MaxMachineConcurrency {
		return MachinePolicy{}, false
	}
	for i, id := range p.Routes {
		if !machineRouteIDPattern.MatchString(id) || (i > 0 && p.Routes[i-1] >= id) {
			return MachinePolicy{}, false
		}
	}
	return p, true
}

// ValidMachinePolicyShape is the check of a stored grant row that has no descriptor at hand.
func ValidMachinePolicyShape(raw string) bool {
	_, ok := decodeMachinePolicy(raw)
	return ok
}

// ParseMachinePolicy accepts only the canonical bytes of a policy that is a subset of the descriptor's block.
func ParseMachinePolicy(raw string, declared Machine) (MachinePolicy, error) {
	p, ok := decodeMachinePolicy(raw)
	if !ok || len(p.Routes) > len(declared.Routes) {
		return MachinePolicy{}, ErrMachinePolicy
	}
	for _, id := range p.Routes {
		if !slices.ContainsFunc(declared.Routes, func(r MachineRoute) bool { return r.ID == id }) {
			return MachinePolicy{}, ErrMachinePolicy
		}
	}
	return p, nil
}

// CanonicalMachineQuery validates a raw query against a route's declared names and returns the one
// sorted, re-encoded form that is forwarded. Anything unusual is refused rather than normalised.
func CanonicalMachineQuery(declared []string, raw string) (string, error) {
	out, err := canonicalQuery(declared, raw, MaxMachineQueryRaw, MaxMachineQueryValue, MaxMachineQueryTotal)
	if err != nil {
		return "", ErrMachineQuery
	}
	return out, nil
}

// MachineRequestTarget joins a route path and its canonical query.
func MachineRequestTarget(path, query string) string { return PublicRequestTarget(path, query) }

func IsMachineAssertion(token string) bool { return strings.HasPrefix(token, MachineAssertionPrefix) }

// MachineClaims carry no subject, role or OU: the backend learns which reviewed route is being
// read, for which installation generation and which named machine client, and nothing else.
type MachineClaims struct {
	Version      int    `json:"v"`
	KeyID        string `json:"kid"`
	Issuer       string `json:"iss"`
	Audience     string `json:"aud"`
	AppID        string `json:"app"`
	InstanceID   string `json:"instance"`
	BootID       string `json:"boot"`
	RequestID    string `json:"rid"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
	Method       string `json:"method"`
	Installation string `json:"installation"`
	Generation   int64  `json:"generation"`
	Route        string `json:"route"`
	Path         string `json:"path"`
	QueryDigest  string `json:"query_sha256"`
	ClientID     string `json:"client_id"`
	ClientLabel  string `json:"client_label"`
}

func machineMAC(key []byte, encoded string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(machineAssertionDomain))
	mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

// MachineQueryDigest is the digest of a canonical query that a machine assertion binds.
func MachineQueryDigest(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

var machineHandleChars = base32.StdEncoding.WithPadding(base32.NoPadding)

func validMachineHandle(handle string) bool {
	if len(handle) != MachineTokenHandleLength {
		return false
	}
	for _, c := range handle {
		if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return false
		}
	}
	return true
}

// ValidMachineLabel is the administrator's label: plain single-line text of at most 64 bytes. It is
// sent to the app and shown in audit rows.
func ValidMachineLabel(label string) bool {
	return label != "" && len(label) <= MaxMachineTokenLabel && utf8.ValidString(label) && strings.TrimSpace(label) == label &&
		strings.IndexFunc(label, unicode.IsControl) < 0
}

// SignMachineAssertion signs with the registration key of the viewer assertion under a separate MAC
// domain and a visible prefix, so no kind verifies as another. query is the canonical query.
func SignMachineAssertion(s protocol.Signer, generation int64, routeID, path, query, clientID, clientLabel string, now time.Time) (string, error) {
	if len(s.Key) < 32 || s.KeyID == "" || s.Issuer == "" || !strings.HasPrefix(s.Audience, "portal.app/") || s.AppID == "" || s.InstanceID == "" || s.BootID == "" ||
		generation < 1 || !machineRouteIDPattern.MatchString(routeID) || !machinePathPattern.MatchString(path) || !ValidBackendRequestRoute("GET", path) || len(query) > MaxMachineQueryRaw ||
		!validMachineHandle(clientID) || !ValidMachineLabel(clientLabel) {
		return "", ErrMachineAssertion
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	claims := MachineClaims{Version: 1, KeyID: s.KeyID, Issuer: s.Issuer, Audience: s.Audience, AppID: s.AppID, InstanceID: s.InstanceID, BootID: s.BootID,
		RequestID: base64.RawURLEncoding.EncodeToString(nonce), IssuedAt: now.Unix(), ExpiresAt: now.Add(MachineAssertionLifetime).Unix(), Method: "GET",
		Installation: strings.TrimPrefix(s.Audience, "portal.app/"), Generation: generation, Route: routeID, Path: path, QueryDigest: MachineQueryDigest(query),
		ClientID: clientID, ClientLabel: clientLabel}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	token := MachineAssertionPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(machineMAC(s.Key, encoded))
	if len(token) > maxMachineAssertionLen {
		return "", ErrMachineAssertion
	}
	return token, nil
}

// ParseMachineClaims decodes the claims of a machine token and checks only their structure: strict
// fields, canonical encoding, version. It does not authenticate the MAC, which only the app can;
// the runner uses it to check that the token is consistent with the request it was signed for.
func ParseMachineClaims(token string) (MachineClaims, error) {
	var zero MachineClaims
	body, ok := strings.CutPrefix(token, MachineAssertionPrefix)
	if !ok || len(token) > maxMachineAssertionLen {
		return zero, ErrMachineAssertion
	}
	encoded, _, ok := strings.Cut(body, ".")
	if !ok {
		return zero, ErrMachineAssertion
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return zero, ErrMachineAssertion
	}
	var c MachineClaims
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrMachineAssertion
	}
	again, _ := json.Marshal(c)
	if !bytes.Equal(again, raw) || c.Version != 1 || c.Method != "GET" || c.Generation < 1 || !validMachineHandle(c.ClientID) || !ValidMachineLabel(c.ClientLabel) {
		return zero, ErrMachineAssertion
	}
	return c, nil
}

// VerifyMachineAssertion is the reference check an app backend performs; the Portal uses it in
// tests. It does not consume the nonce: replay protection is the app's.
func VerifyMachineAssertion(s protocol.Signer, token, target string, now time.Time) (MachineClaims, error) {
	var zero MachineClaims
	body, ok := strings.CutPrefix(token, MachineAssertionPrefix)
	if !ok || len(token) > maxMachineAssertionLen || len(s.Key) < 32 {
		return zero, ErrMachineAssertion
	}
	encoded, sig, ok := strings.Cut(body, ".")
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || subtle.ConstantTimeCompare(mac, machineMAC(s.Key, encoded)) != 1 {
		return zero, ErrMachineAssertion
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return zero, ErrMachineAssertion
	}
	var c MachineClaims
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	path, query, _ := strings.Cut(target, "?")
	if d.Decode(&c) != nil || c.Version != 1 || c.KeyID != s.KeyID || c.Issuer != s.Issuer || c.Audience != s.Audience || c.AppID != s.AppID || c.InstanceID != s.InstanceID || c.BootID != s.BootID ||
		c.Method != "GET" || c.Path != path || c.QueryDigest != MachineQueryDigest(query) || c.Generation < 1 || c.ExpiresAt-c.IssuedAt != int64(MachineAssertionLifetime/time.Second) ||
		now.Unix() < c.IssuedAt-5 || now.Unix() >= c.ExpiresAt || !validMachineHandle(c.ClientID) || !ValidMachineLabel(c.ClientLabel) {
		return zero, ErrMachineAssertion
	}
	return c, nil
}

// NewMachineToken makes a token: "pmr_" + a 12 character lowercase base32 handle + "_" + the base64url
// of 32 random bytes (43 characters), 60 characters in all. The caller shows the token once and
// stores only the handle and the digest of the secret part.
func NewMachineToken() (token, handle, secretSHA256 string, err error) {
	raw := make([]byte, 32+8)
	if _, err = rand.Read(raw); err != nil {
		return "", "", "", err
	}
	handle = strings.ToLower(machineHandleChars.EncodeToString(raw[32:]))[:MachineTokenHandleLength]
	secret := base64.RawURLEncoding.EncodeToString(raw[:32])
	clear(raw)
	return MachineTokenPrefix + handle + "_" + secret, handle, MachineSecretDigest(secret), nil
}

// MachineSecretDigest is what the database holds of a token's secret part.
func MachineSecretDigest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ParseMachineAuthorization takes exactly "Bearer pmr_<handle>_<secret>" and returns its two parts.
// The handle is a lookup key and the secret never leaves the request handler.
func ParseMachineAuthorization(header string) (handle, secret string, err error) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(header) > MaxMachineAuthorizationHeader || len(token) != MachineTokenLength || !strings.HasPrefix(token, MachineTokenPrefix) {
		return "", "", ErrMachineToken
	}
	body := token[len(MachineTokenPrefix):]
	handle, secret = body[:MachineTokenHandleLength], body[MachineTokenHandleLength+1:]
	if body[MachineTokenHandleLength] != '_' || !validMachineHandle(handle) {
		return "", "", ErrMachineToken
	}
	for _, c := range secret {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return "", "", ErrMachineToken
		}
	}
	return handle, secret, nil
}

// ValidMachineHandle reports whether text has the shape of a token handle.
func ValidMachineHandle(handle string) bool { return validMachineHandle(handle) }
