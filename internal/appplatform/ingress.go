package appplatform

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// Authenticated inbound ingress (doc 180). Nothing here grants access: these are the pure formats
// the Portal handler, the runner and a reference backend verifier share.

const (
	IngressTimestampHeader   = "X-Portal-Ingress-Timestamp"
	IngressNonceHeader       = "X-Portal-Ingress-Nonce"
	IngressSignatureHeader   = "X-Portal-Ingress-Signature"
	IngressAssertionHeader   = "X-Portal-Ingress-Assertion"
	IngressAssertionPrefix   = "ing."
	IngressSigningDomain     = "portal-ingress/v1"
	IngressSignaturePrefix   = "v1="
	IngressAssertionLifetime = 30 * time.Second
	// The accepted window of a sender timestamp around Portal time, and how long a nonce is held.
	IngressTimestampPast   = 30 * time.Second
	IngressTimestampFuture = 5 * time.Second
	IngressNonceWindow     = 60 * time.Second
	MinIngressSecret       = 32
	MaxIngressSecret       = 256

	ingressAssertionDomain = "rp-extension-ingress-v1\n"
	maxIngressAssertionLen = 4096
)

var (
	ingressNonceShape     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	ingressSignatureShape = regexp.MustCompile(`^v1=[0-9a-f]{64}$`)
	ingressTimestamp      = regexp.MustCompile(`^[0-9]{1,12}$`)
	ingressEndpointShape  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

var ErrIngressAssertion = errors.New("invalid ingress assertion")

// ValidIngressRouteID and ValidIngressEndpoint are the shapes the URL carries.
func ValidIngressRouteID(id string) bool { return ingressRouteIDPattern.MatchString(id) }
func ValidIngressEndpoint(name string) bool {
	return ingressEndpointShape.MatchString(name)
}

// ValidIngressNonce and ValidIngressSignatureHeader are the static shapes of the headers.
func ValidIngressNonce(nonce string) bool { return ingressNonceShape.MatchString(nonce) }
func ValidIngressSignatureHeader(value string) bool {
	return ingressSignatureShape.MatchString(value)
}

// IngressTimestampWithin reports whether a header timestamp (decimal seconds) is inside the window
// around now, and returns it.
func IngressTimestampWithin(header string, now time.Time) (int64, bool) {
	if !ingressTimestamp.MatchString(header) {
		return 0, false
	}
	seconds, err := strconv.ParseInt(header, 10, 64)
	if err != nil {
		return 0, false
	}
	at := time.Unix(seconds, 0)
	return seconds, !at.Before(now.Add(-IngressTimestampPast)) && !at.After(now.Add(IngressTimestampFuture))
}

// IngressSigningString is the string the sender signs (doc 180 section 3.3).
func IngressSigningString(installation, route, endpoint, timestamp, nonce string, body []byte) string {
	digest := sha256.Sum256(body)
	return IngressSigningDomain + "\n" + installation + "\n" + route + "\n" + endpoint + "\n" + timestamp + "\n" + nonce + "\n" + hex.EncodeToString(digest[:])
}

// IngressSign is the sender's signature header value: "v1=" and the hex HMAC-SHA256 of the signing string.
func IngressSign(secret []byte, installation, route, endpoint, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(IngressSigningString(installation, route, endpoint, timestamp, nonce, body)))
	return IngressSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// IngressVerify compares a signature header with the expected one in constant time.
func IngressVerify(secret []byte, installation, route, endpoint, timestamp, nonce string, body []byte, header string) bool {
	expected := IngressSign(secret, installation, route, endpoint, timestamp, nonce, body)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(header)) == 1
}

// IngressDigest is the lower-case hex SHA-256 the assertion binds for a nonce or a body.
func IngressDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func IsIngressAssertion(token string) bool { return strings.HasPrefix(token, IngressAssertionPrefix) }

// IngressClaims carry what the Portal verified and nothing about the sender: the backend learns
// which endpoint of which installation generation was heard, when, and the digests it needs to
// deduplicate durably. They carry no secret and no source address.
type IngressClaims struct {
	Version      int    `json:"v"`
	KeyID        string `json:"kid"`
	Issuer       string `json:"iss"`
	Audience     string `json:"aud"`
	AppID        string `json:"app"`
	InstanceID   string `json:"instance"`
	BootID       string `json:"boot"`
	RequestID    string `json:"jti"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
	Method       string `json:"method"`
	Installation string `json:"installation"`
	Generation   int64  `json:"generation"`
	Route        string `json:"route"`
	Endpoint     string `json:"endpoint"`
	Path         string `json:"path"`
	ReceivedMS   int64  `json:"received_ms"`
	NonceDigest  string `json:"nonce_sha256"`
	BodyDigest   string `json:"body_sha256"`
}

func ingressMAC(key []byte, encoded string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(ingressAssertionDomain))
	mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

// SignIngressAssertion signs with the registration key of the viewer assertion under a separate MAC
// domain and a visible prefix, so no two kinds verify as each other. nonce is the sender's nonce
// string and body the request body; the claims carry only their digests.
func SignIngressAssertion(s protocol.Signer, generation int64, routeID, endpoint, path string, received time.Time, nonce string, body []byte, now time.Time) (string, error) {
	if len(s.Key) < 32 || s.KeyID == "" || s.Issuer == "" || !strings.HasPrefix(s.Audience, "portal.app/") || s.AppID == "" || s.InstanceID == "" || s.BootID == "" ||
		generation < 1 || !ingressRouteIDPattern.MatchString(routeID) || !ingressEndpointShape.MatchString(endpoint) || !ingressPathPattern.MatchString(path) ||
		!ValidBackendRequestRoute("POST", path) || !ingressNonceShape.MatchString(nonce) || len(body) > maxIngressBodyBytes {
		return "", ErrIngressAssertion
	}
	jti := make([]byte, 24)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	claims := IngressClaims{Version: 1, KeyID: s.KeyID, Issuer: s.Issuer, Audience: s.Audience, AppID: s.AppID, InstanceID: s.InstanceID, BootID: s.BootID,
		RequestID: base64.RawURLEncoding.EncodeToString(jti), IssuedAt: now.Unix(), ExpiresAt: now.Add(IngressAssertionLifetime).Unix(), Method: "POST",
		Installation: strings.TrimPrefix(s.Audience, "portal.app/"), Generation: generation, Route: routeID, Endpoint: endpoint, Path: path,
		ReceivedMS: received.UnixMilli(), NonceDigest: IngressDigest([]byte(nonce)), BodyDigest: IngressDigest(body)}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	token := IngressAssertionPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(ingressMAC(s.Key, encoded))
	if len(token) > maxIngressAssertionLen {
		return "", ErrIngressAssertion
	}
	return token, nil
}

// ParseIngressClaims decodes the claims of an ingress token and checks only their structure: strict
// fields, canonical encoding, version. It does not authenticate the MAC, which only the app can; the
// runner uses it to check that the token is consistent with the request it was signed for.
func ParseIngressClaims(token string) (IngressClaims, error) {
	var zero IngressClaims
	body, ok := strings.CutPrefix(token, IngressAssertionPrefix)
	if !ok || len(token) > maxIngressAssertionLen {
		return zero, ErrIngressAssertion
	}
	encoded, _, ok := strings.Cut(body, ".")
	if !ok {
		return zero, ErrIngressAssertion
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return zero, ErrIngressAssertion
	}
	var c IngressClaims
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return zero, ErrIngressAssertion
	}
	again, _ := json.Marshal(c)
	if !bytes.Equal(again, raw) || c.Version != 1 || c.Method != "POST" || c.Generation < 1 || !ingressEndpointShape.MatchString(c.Endpoint) {
		return zero, ErrIngressAssertion
	}
	return c, nil
}

// VerifyIngressAssertion is the reference check an app backend performs; Portal uses it in tests. It
// does not consume the nonce digest: durable deduplication is the app's.
func VerifyIngressAssertion(s protocol.Signer, token, path string, body []byte, now time.Time) (IngressClaims, error) {
	var zero IngressClaims
	rest, ok := strings.CutPrefix(token, IngressAssertionPrefix)
	if !ok || len(token) > maxIngressAssertionLen || len(s.Key) < 32 {
		return zero, ErrIngressAssertion
	}
	encoded, sig, ok := strings.Cut(rest, ".")
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || subtle.ConstantTimeCompare(mac, ingressMAC(s.Key, encoded)) != 1 {
		return zero, ErrIngressAssertion
	}
	c, err := ParseIngressClaims(token)
	if err != nil || c.KeyID != s.KeyID || c.Issuer != s.Issuer || c.Audience != s.Audience || c.AppID != s.AppID || c.InstanceID != s.InstanceID || c.BootID != s.BootID ||
		c.Path != path || c.BodyDigest != IngressDigest(body) || c.ExpiresAt-c.IssuedAt != int64(IngressAssertionLifetime/time.Second) ||
		now.Unix() < c.IssuedAt-5 || now.Unix() >= c.ExpiresAt {
		return zero, ErrIngressAssertion
	}
	return c, nil
}
