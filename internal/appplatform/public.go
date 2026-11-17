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
	"net/url"
	"slices"
	"strings"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// The anonymous public projection (doc 179). Nothing here grants access: these are the pure
// formats the Portal handlers and the runner share.

const PublicPolicyFormat = "portal.public-projection-policy/1-dev.1"
const PublicAssertionPrefix = "pub."
const PublicAssertionHeader = "X-Portal-Public-Assertion"
const PublicAssertionLifetime = 30 * time.Second
const publicAssertionDomain = "rp-extension-public-v1\n"
const (
	MaxPublicQueryValue   = 128
	MaxPublicQueryTotal   = 512
	MaxPublicQueryRaw     = 1024
	DefaultPublicClient   = 60
	MaxPublicClient       = 600
	DefaultPublicBackend  = 5
	MaxPublicBackend      = 20
	maxPublicAssertionLen = 4096
)

var ErrPublicPolicy = errors.New("invalid public projection policy")
var ErrPublicQuery = errors.New("invalid public query")
var ErrPublicAssertion = errors.New("invalid public assertion")

// PublicPolicy is the reviewed content of the public.projection grant row.
type PublicPolicy struct {
	Format           string   `json:"format"`
	Routes           []string `json:"routes"`
	Document         bool     `json:"document"`
	ClientPerMinute  int      `json:"client_per_minute"`
	BackendPerSecond int      `json:"backend_per_second"`
}

// DefaultPublicPolicy is the pre-filled review: every declared route, the document if declared.
func DefaultPublicPolicy(p Public) PublicPolicy {
	routes := make([]string, 0, len(p.Routes))
	for _, r := range p.Routes {
		routes = append(routes, r.ID)
	}
	return PublicPolicy{Format: PublicPolicyFormat, Routes: routes, Document: p.Document != "", ClientPerMinute: DefaultPublicClient, BackendPerSecond: DefaultPublicBackend}
}

func CanonicalPublicPolicy(p PublicPolicy) string {
	raw, _ := json.Marshal(p)
	return string(raw)
}

// ParsePublicPolicy accepts only the canonical bytes of a policy that is a subset of the descriptor's block.
func ParsePublicPolicy(raw string, declared Public) (PublicPolicy, error) {
	var p PublicPolicy
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) == 0 || len(raw) > 4096 || d.Decode(&p) != nil || CanonicalPublicPolicy(p) != raw || p.Format != PublicPolicyFormat ||
		len(p.Routes) < 1 || len(p.Routes) > len(declared.Routes) || p.ClientPerMinute < 1 || p.ClientPerMinute > MaxPublicClient ||
		p.BackendPerSecond < 1 || p.BackendPerSecond > MaxPublicBackend || (p.Document && declared.Document == "") {
		return PublicPolicy{}, ErrPublicPolicy
	}
	for i, id := range p.Routes {
		if (i > 0 && p.Routes[i-1] >= id) || !slices.ContainsFunc(declared.Routes, func(r PublicRoute) bool { return r.ID == id }) {
			return PublicPolicy{}, ErrPublicPolicy
		}
	}
	return p, nil
}

// CanonicalPublicQuery validates a raw query against a route's declared names and returns the one
// sorted, re-encoded form that keys the cache and is forwarded. Anything unusual is refused rather
// than normalised: undeclared or repeated names, empty names, controls, non-ASCII, oversize values.
func CanonicalPublicQuery(declared []string, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if len(raw) > MaxPublicQueryRaw || strings.IndexFunc(raw, func(r rune) bool { return r < 0x21 || r > 0x7e || r == '#' }) >= 0 {
		return "", ErrPublicQuery
	}
	type pair struct{ name, value string }
	var pairs []pair
	total := 0
	for _, part := range strings.Split(raw, "&") {
		name, value, ok := strings.Cut(part, "=")
		if !ok || name == "" || !slices.Contains(declared, name) || slices.ContainsFunc(pairs, func(p pair) bool { return p.name == name }) {
			return "", ErrPublicQuery
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil || len(decoded) > MaxPublicQueryValue || strings.IndexFunc(decoded, func(r rune) bool { return r < 0x20 || r > 0x7e }) >= 0 {
			return "", ErrPublicQuery
		}
		total += len(name) + len(decoded)
		pairs = append(pairs, pair{name, decoded})
	}
	if total > MaxPublicQueryTotal {
		return "", ErrPublicQuery
	}
	slices.SortFunc(pairs, func(a, b pair) int { return strings.Compare(a.name, b.name) })
	var out strings.Builder
	for i, p := range pairs {
		if i > 0 {
			out.WriteByte('&')
		}
		out.WriteString(p.name + "=" + url.QueryEscape(p.value))
	}
	return out.String(), nil
}

// PublicRequestTarget joins a route path and its canonical query.
func PublicRequestTarget(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

func IsPublicAssertion(token string) bool { return strings.HasPrefix(token, PublicAssertionPrefix) }

// PublicClaims carry no subject, role or OU: the backend learns which reviewed route is being
// read, for which installation generation, and nothing about the reader.
type PublicClaims struct {
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
	Path         string `json:"path"`
	QueryDigest  string `json:"query_sha256"`
}

func publicMAC(key []byte, encoded string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(publicAssertionDomain))
	mac.Write([]byte(encoded))
	return mac.Sum(nil)
}

func publicQueryDigest(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

// SignPublicAssertion signs with the registration key of the viewer assertion under a separate MAC
// domain and a visible prefix, so neither kind verifies as the other. query is the canonical query.
func SignPublicAssertion(s protocol.Signer, generation int64, routeID, path, query string, now time.Time) (string, error) {
	if len(s.Key) < 32 || s.KeyID == "" || s.Issuer == "" || !strings.HasPrefix(s.Audience, "portal.app/") || s.AppID == "" || s.InstanceID == "" || s.BootID == "" ||
		generation < 1 || !publicRouteIDPattern.MatchString(routeID) || !publicPathPattern.MatchString(path) || !ValidBackendRequestRoute("GET", path) || len(query) > MaxPublicQueryRaw {
		return "", ErrPublicAssertion
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	claims := PublicClaims{Version: 1, KeyID: s.KeyID, Issuer: s.Issuer, Audience: s.Audience, AppID: s.AppID, InstanceID: s.InstanceID, BootID: s.BootID,
		RequestID: base64.RawURLEncoding.EncodeToString(nonce), IssuedAt: now.Unix(), ExpiresAt: now.Add(PublicAssertionLifetime).Unix(), Method: "GET",
		Installation: strings.TrimPrefix(s.Audience, "portal.app/"), Generation: generation, Route: routeID, Path: path, QueryDigest: publicQueryDigest(query)}
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	token := PublicAssertionPrefix + encoded + "." + base64.RawURLEncoding.EncodeToString(publicMAC(s.Key, encoded))
	if len(token) > maxPublicAssertionLen {
		return "", ErrPublicAssertion
	}
	return token, nil
}

// VerifyPublicAssertion is the reference check an app backend performs; the Portal uses it in
// tests. It does not consume the nonce: replay protection is the app's.
func VerifyPublicAssertion(s protocol.Signer, token, target string, now time.Time) (PublicClaims, error) {
	var zero PublicClaims
	body, ok := strings.CutPrefix(token, PublicAssertionPrefix)
	if !ok || len(token) > maxPublicAssertionLen || len(s.Key) < 32 {
		return zero, ErrPublicAssertion
	}
	encoded, sig, ok := strings.Cut(body, ".")
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || subtle.ConstantTimeCompare(mac, publicMAC(s.Key, encoded)) != 1 {
		return zero, ErrPublicAssertion
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return zero, ErrPublicAssertion
	}
	var c PublicClaims
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	path, query, _ := strings.Cut(target, "?")
	if d.Decode(&c) != nil || c.Version != 1 || c.KeyID != s.KeyID || c.Issuer != s.Issuer || c.Audience != s.Audience || c.AppID != s.AppID || c.InstanceID != s.InstanceID || c.BootID != s.BootID ||
		c.Method != "GET" || c.Path != path || c.QueryDigest != publicQueryDigest(query) || c.Generation < 1 || c.ExpiresAt-c.IssuedAt != int64(PublicAssertionLifetime/time.Second) ||
		now.Unix() < c.IssuedAt-5 || now.Unix() >= c.ExpiresAt {
		return zero, ErrPublicAssertion
	}
	return c, nil
}
