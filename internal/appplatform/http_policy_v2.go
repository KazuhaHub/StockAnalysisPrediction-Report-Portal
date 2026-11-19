package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// HTTPPolicyV2Format is the multi-target HTTP policy. Stream policies stay v1,
// and ParseHTTPPolicy keeps refusing this format: only ParseHTTPPolicyAny and
// ParseHTTPPolicyV2 read it.
const HTTPPolicyV2Format = "portal.http-policy/2-dev.1"
const MaxHTTPPolicyV2Bytes = 16384
const MaxHTTPPolicyTargets = 128
const MaxHTTPTargetURLBytes = 4096

// DefaultHTTPTargetID names the single target a v1 policy is read as.
const DefaultHTTPTargetID = "default"

const (
	HTTPNetworkPublic  = "public"
	HTTPNetworkPrivate = "private"
)

var httpTargetIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

// HTTPTarget is one reviewed destination. RequestBytes, ResponseBytes and
// Deadline are optional narrowing overrides: zero means the shared bound, and a
// non-zero value must be strictly lower than it, so a target can only tighten.
type HTTPTarget struct {
	ID            string
	Origin        string
	Paths         []string
	Methods       []string
	Network       string
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
}

// HTTPPolicyV2 holds the shared bounds and every target. Concurrency is per
// installation, not per target.
type HTTPPolicyV2 struct {
	Targets       []HTTPTarget
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
	Concurrency   int
}

// ParsedHTTPPolicyAny is a v1 or v2 policy in one shape. SHA256 is the digest
// of the exact stored bytes, so a v1 policy keeps its v1 digest.
type ParsedHTTPPolicyAny struct {
	Policy  HTTPPolicyV2
	SHA256  string
	Version int
}

// EffectiveHTTPTarget is the outcome of selecting a target for one request,
// with the target's own bounds already applied. Port is the effective port.
// Literal is valid only when the host is an IP literal (no lookup needed).
type EffectiveHTTPTarget struct {
	ID            string
	Origin        string
	Scheme        string
	Host          string
	Port          uint16
	Private       bool
	Literal       netip.Addr
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
	Concurrency   int
	SHA256        string
}

type httpTargetWire struct {
	ID            string   `json:"id"`
	Origin        string   `json:"origin"`
	Paths         []string `json:"paths"`
	Methods       []string `json:"methods"`
	Network       string   `json:"network"`
	RequestBytes  int64    `json:"request_bytes,omitempty"`
	ResponseBytes int64    `json:"response_bytes,omitempty"`
	DeadlineMS    int64    `json:"deadline_ms,omitempty"`
}

type httpPolicyV2Wire struct {
	Format        string            `json:"format"`
	Targets       []json.RawMessage `json:"targets"`
	RequestBytes  int64             `json:"request_bytes"`
	ResponseBytes int64             `json:"response_bytes"`
	DeadlineMS    int64             `json:"deadline_ms"`
	Concurrency   int               `json:"concurrency"`
}

type httpPolicyV2Read struct {
	Format        string           `json:"format"`
	Targets       []httpTargetWire `json:"targets"`
	RequestBytes  int64            `json:"request_bytes"`
	ResponseBytes int64            `json:"response_bytes"`
	DeadlineMS    int64            `json:"deadline_ms"`
	Concurrency   int              `json:"concurrency"`
}

type httpTargetOrigin struct {
	scheme  string
	host    string
	port    uint16
	literal netip.Addr
}

// parseHTTPTargetOrigin validates an origin for a network class and returns its
// parts. The origin must be exactly the canonical spelling it parses to.
func parseHTTPTargetOrigin(origin, network string) (httpTargetOrigin, bool) {
	var zero httpTargetOrigin
	if len(origin) < 1 || len(origin) > 512 || strings.Contains(origin, "#") {
		return zero, false
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return zero, false
	}
	scheme := u.Scheme
	defaultPort := 443
	switch {
	case scheme == "https":
	case scheme == "http" && network == HTTPNetworkPrivate:
		defaultPort = 80
	default:
		return zero, false
	}
	host := u.Hostname()
	var literal netip.Addr
	switch network {
	case HTTPNetworkPublic:
		if !validHTTPPolicyHost(host) {
			return zero, false
		}
	case HTTPNetworkPrivate:
		if addr, err := netip.ParseAddr(host); err == nil {
			if addr.Zone() != "" || addr.String() != host || addr.Is4In6() || AlwaysDenied(addr) {
				return zero, false
			}
			literal = addr
		} else if !validHTTPPolicyHost(host) {
			return zero, false
		}
	default:
		return zero, false
	}
	spelled := host
	if literal.Is6() {
		spelled = "[" + host + "]"
	}
	port := defaultPort
	if text := u.Port(); text != "" {
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != text || n == defaultPort {
			return zero, false
		}
		port = n
		spelled += ":" + text
	}
	if origin != scheme+"://"+spelled {
		return zero, false
	}
	return httpTargetOrigin{scheme: scheme, host: host, port: uint16(port), literal: literal}, true
}

func validHTTPTargetPaths(paths []string) bool {
	if len(paths) < 1 || len(paths) > 16 {
		return false
	}
	for i, path := range paths {
		if !validHTTPPolicyPath(path) || (len(path) > 1 && strings.HasSuffix(path, "/")) || slices.Contains(paths[:i], path) {
			return false
		}
	}
	return true
}

func validHTTPTargetMethods(methods []string) bool {
	if len(methods) < 1 || len(methods) > 7 {
		return false
	}
	for i, method := range methods {
		if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, method) || slices.Contains(methods[:i], method) {
			return false
		}
	}
	return true
}

// normalizeHTTPTarget validates one target on its own and returns a copy with
// sorted paths and methods. Whether an override is lower than the shared bound
// is checked at policy level.
func normalizeHTTPTarget(t HTTPTarget) (HTTPTarget, error) {
	var zero HTTPTarget
	if !httpTargetIDPattern.MatchString(t.ID) || !validHTTPTargetPaths(t.Paths) || !validHTTPTargetMethods(t.Methods) {
		return zero, ErrInvalidHTTPPolicy
	}
	if _, ok := parseHTTPTargetOrigin(t.Origin, t.Network); !ok {
		return zero, ErrInvalidHTTPPolicy
	}
	if t.RequestBytes < 0 || t.RequestBytes > MaxHTTPRequestBytes || t.ResponseBytes < 0 || t.ResponseBytes > MaxHTTPResponseBytes ||
		t.Deadline < 0 || t.Deadline > time.Duration(MaxHTTPDeadlineMS)*time.Millisecond || t.Deadline%time.Millisecond != 0 {
		return zero, ErrInvalidHTTPPolicy
	}
	t.Paths = slices.Clone(t.Paths)
	t.Methods = slices.Clone(t.Methods)
	slices.Sort(t.Paths)
	slices.Sort(t.Methods)
	return t, nil
}

func encodeHTTPTarget(t HTTPTarget) ([]byte, error) {
	raw, err := json.Marshal(httpTargetWire{ID: t.ID, Origin: t.Origin, Paths: t.Paths, Methods: t.Methods, Network: t.Network, RequestBytes: t.RequestBytes, ResponseBytes: t.ResponseBytes, DeadlineMS: t.Deadline.Milliseconds()})
	if err != nil {
		return nil, ErrInvalidHTTPPolicy
	}
	return raw, nil
}

// CanonicalHTTPTarget returns the canonical bytes the target digest covers: keys
// in the order id, origin, paths, methods, network, then any overrides.
func CanonicalHTTPTarget(t HTTPTarget) ([]byte, error) {
	t, err := normalizeHTTPTarget(t)
	if err != nil {
		return nil, err
	}
	return encodeHTTPTarget(t)
}

// HTTPTargetSHA256 is the lowercase hex SHA-256 of CanonicalHTTPTarget. Renaming
// a target changes it, so a rename is a remove plus an add.
func HTTPTargetSHA256(t HTTPTarget) (string, error) {
	raw, err := CanonicalHTTPTarget(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizeHTTPPolicyV2 validates and clones a policy. Targets, paths and
// methods are sets in authority terms, so the copy is sorted.
func NormalizeHTTPPolicyV2(p HTTPPolicyV2) (HTTPPolicyV2, error) {
	var zero HTTPPolicyV2
	if len(p.Targets) < 1 || len(p.Targets) > MaxHTTPPolicyTargets ||
		p.RequestBytes < 1 || p.RequestBytes > MaxHTTPRequestBytes ||
		p.ResponseBytes < 1 || p.ResponseBytes > MaxHTTPResponseBytes ||
		p.Deadline < time.Millisecond || p.Deadline > time.Duration(MaxHTTPDeadlineMS)*time.Millisecond || p.Deadline%time.Millisecond != 0 ||
		p.Concurrency < 1 || p.Concurrency > MaxHTTPConcurrency {
		return zero, ErrInvalidHTTPPolicy
	}
	targets := make([]HTTPTarget, 0, len(p.Targets))
	origins := make(map[string]bool, len(p.Targets))
	for _, t := range p.Targets {
		n, err := normalizeHTTPTarget(t)
		if err != nil || origins[n.Origin] {
			return zero, ErrInvalidHTTPPolicy
		}
		if n.RequestBytes != 0 && n.RequestBytes >= p.RequestBytes || n.ResponseBytes != 0 && n.ResponseBytes >= p.ResponseBytes || n.Deadline != 0 && n.Deadline >= p.Deadline {
			return zero, ErrInvalidHTTPPolicy
		}
		origins[n.Origin] = true
		targets = append(targets, n)
	}
	slices.SortFunc(targets, func(a, b HTTPTarget) int { return strings.Compare(a.ID, b.ID) })
	for i := 1; i < len(targets); i++ {
		if targets[i].ID == targets[i-1].ID {
			return zero, ErrInvalidHTTPPolicy
		}
	}
	p.Targets = targets
	if _, err := encodeHTTPPolicyV2(p); err != nil {
		return zero, err
	}
	return p, nil
}

func encodeHTTPPolicyV2(p HTTPPolicyV2) ([]byte, error) {
	raws := make([]json.RawMessage, 0, len(p.Targets))
	for _, t := range p.Targets {
		raw, err := encodeHTTPTarget(t)
		if err != nil {
			return nil, err
		}
		raws = append(raws, raw)
	}
	raw, err := json.Marshal(httpPolicyV2Wire{Format: HTTPPolicyV2Format, Targets: raws, RequestBytes: p.RequestBytes, ResponseBytes: p.ResponseBytes, DeadlineMS: p.Deadline.Milliseconds(), Concurrency: p.Concurrency})
	if err != nil || len(raw) > MaxHTTPPolicyV2Bytes {
		return nil, ErrInvalidHTTPPolicy
	}
	return raw, nil
}

// CanonicalHTTPPolicyV2 returns the canonical v2 bytes of a valid policy.
func CanonicalHTTPPolicyV2(p HTTPPolicyV2) ([]byte, error) {
	p, err := NormalizeHTTPPolicyV2(p)
	if err != nil {
		return nil, err
	}
	return encodeHTTPPolicyV2(p)
}

// ParseHTTPPolicyV2 accepts only the exact canonical v2 bytes.
func ParseHTTPPolicyV2(raw []byte) (ParsedHTTPPolicyAny, error) {
	var zero ParsedHTTPPolicyAny
	if len(raw) == 0 || len(raw) > MaxHTTPPolicyV2Bytes || !utf8.Valid(raw) {
		return zero, ErrInvalidHTTPPolicy
	}
	scan := json.NewDecoder(bytes.NewReader(raw))
	scan.UseNumber()
	first, err := scan.Token()
	if err != nil || first != json.Delim('{') || !scanObject(scan, 1) {
		return zero, ErrInvalidHTTPPolicy
	}
	if _, err := scan.Token(); err != io.EOF {
		return zero, ErrInvalidHTTPPolicy
	}
	var w httpPolicyV2Read
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || w.Format != HTTPPolicyV2Format || w.DeadlineMS < 1 || w.DeadlineMS > MaxHTTPDeadlineMS || len(w.Targets) > MaxHTTPPolicyTargets {
		return zero, ErrInvalidHTTPPolicy
	}
	policy := HTTPPolicyV2{RequestBytes: w.RequestBytes, ResponseBytes: w.ResponseBytes, Deadline: time.Duration(w.DeadlineMS) * time.Millisecond, Concurrency: w.Concurrency}
	for _, t := range w.Targets {
		policy.Targets = append(policy.Targets, HTTPTarget{ID: t.ID, Origin: t.Origin, Paths: t.Paths, Methods: t.Methods, Network: t.Network, RequestBytes: t.RequestBytes, ResponseBytes: t.ResponseBytes, Deadline: time.Duration(t.DeadlineMS) * time.Millisecond})
	}
	p, err := NormalizeHTTPPolicyV2(policy)
	if err != nil {
		return zero, err
	}
	canonical, err := encodeHTTPPolicyV2(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidHTTPPolicy
	}
	sum := sha256.Sum256(raw)
	return ParsedHTTPPolicyAny{Policy: p, SHA256: hex.EncodeToString(sum[:]), Version: 2}, nil
}

// HTTPPolicyFromV1 reads a v1 policy as one public target named "default".
func HTTPPolicyFromV1(p HTTPPolicy) HTTPPolicyV2 {
	return HTTPPolicyV2{
		Targets:       []HTTPTarget{{ID: DefaultHTTPTargetID, Origin: p.Origin, Paths: slices.Clone(p.Paths), Methods: slices.Clone(p.Methods), Network: HTTPNetworkPublic}},
		RequestBytes:  p.RequestBytes,
		ResponseBytes: p.ResponseBytes,
		Deadline:      p.Deadline,
		Concurrency:   p.Concurrency,
	}
}

// ParseHTTPPolicyAny reads exact canonical v1 or v2 bytes. A v1 policy becomes
// one public target named "default" and keeps its own digest.
func ParseHTTPPolicyAny(raw []byte) (ParsedHTTPPolicyAny, error) {
	if v1, err := ParseHTTPPolicy(raw); err == nil {
		return ParsedHTTPPolicyAny{Policy: HTTPPolicyFromV1(v1.Policy), SHA256: v1.SHA256, Version: 1}, nil
	}
	return ParseHTTPPolicyV2(raw)
}

// HasPrivate reports whether any target is a private-network target.
func (p HTTPPolicyV2) HasPrivate() bool {
	return slices.ContainsFunc(p.Targets, func(t HTTPTarget) bool { return t.Network == HTTPNetworkPrivate })
}

// Select picks the one target whose origin equals the request URL's origin and
// applies its methods and path prefixes and its effective bounds. It performs no
// DNS, IO or authorization and cannot grant a request; anything else is denied
// before any lookup. Redirects are never part of a selection.
func (p HTTPPolicyV2) Select(method, raw string) (EffectiveHTTPTarget, error) {
	var zero EffectiveHTTPTarget
	p, err := NormalizeHTTPPolicyV2(p)
	if err != nil || len(raw) > MaxHTTPTargetURLBytes {
		return zero, ErrInvalidHTTPPolicy
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" || u.RawPath != "" || !validHTTPPolicyPath(u.Path) {
		return zero, ErrInvalidHTTPPolicy
	}
	origin := u.Scheme + "://" + u.Host
	for _, t := range p.Targets {
		if t.Origin != origin {
			continue
		}
		if !slices.Contains(t.Methods, method) || !slices.ContainsFunc(t.Paths, func(prefix string) bool {
			return prefix == "/" || u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/")
		}) {
			return zero, ErrInvalidHTTPPolicy
		}
		parts, ok := parseHTTPTargetOrigin(t.Origin, t.Network)
		digest, err := HTTPTargetSHA256(t)
		if !ok || err != nil {
			return zero, ErrInvalidHTTPPolicy
		}
		e := EffectiveHTTPTarget{ID: t.ID, Origin: t.Origin, Scheme: parts.scheme, Host: parts.host, Port: parts.port, Private: t.Network == HTTPNetworkPrivate, Literal: parts.literal, RequestBytes: p.RequestBytes, ResponseBytes: p.ResponseBytes, Deadline: p.Deadline, Concurrency: p.Concurrency, SHA256: digest}
		if t.RequestBytes != 0 {
			e.RequestBytes = t.RequestBytes
		}
		if t.ResponseBytes != 0 {
			e.ResponseBytes = t.ResponseBytes
		}
		if t.Deadline != 0 {
			e.Deadline = t.Deadline
		}
		return e, nil
	}
	return zero, ErrInvalidHTTPPolicy
}
