package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const HTTPPolicyFormat = "portal.http-policy/1-dev.1"
const MaxHTTPPolicyBytes = 8192
const MaxHTTPRequestBytes = 65536
const MaxHTTPResponseBytes = 1048576
const MaxHTTPDeadlineMS = 15000
const MaxHTTPConcurrency = 4

var ErrInvalidHTTPPolicy = errors.New("invalid application HTTP policy")

// HTTPPolicy contains only host-reviewed restrictions. It conveys no principal,
// grant, credential, resolver or TLS override. Parsing it grants no authority.
type HTTPPolicy struct {
	Origin        string
	Paths         []string
	Methods       []string
	RequestBytes  int64
	ResponseBytes int64
	Deadline      time.Duration
	Concurrency   int
}

type httpPolicyWire struct {
	Format        string   `json:"format"`
	Origin        string   `json:"origin"`
	Paths         []string `json:"paths"`
	Methods       []string `json:"methods"`
	RequestBytes  int64    `json:"request_bytes"`
	ResponseBytes int64    `json:"response_bytes"`
	DeadlineMS    int64    `json:"deadline_ms"`
	Concurrency   int      `json:"concurrency"`
}

type ParsedHTTPPolicy struct {
	Policy HTTPPolicy
	SHA256 string
}

// NormalizeHTTPPolicy validates and clones the host's selected policy. Paths and
// methods are sets in authority terms, so canonical encoding sorts their copies.
func NormalizeHTTPPolicy(p HTTPPolicy) (HTTPPolicy, error) {
	var zero HTTPPolicy
	if len(p.Origin) > 512 {
		return zero, ErrInvalidHTTPPolicy
	}
	u, err := url.Parse(p.Origin)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !validHTTPPolicyHost(u.Hostname()) || strings.Contains(p.Origin, "#") || p.Origin != "https://"+u.Host {
		return zero, ErrInvalidHTTPPolicy
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || n == 443 {
			return zero, ErrInvalidHTTPPolicy
		}
	} else if u.Host != u.Hostname() {
		return zero, ErrInvalidHTTPPolicy
	}
	if p.RequestBytes < 1 || p.RequestBytes > MaxHTTPRequestBytes || p.ResponseBytes < 1 || p.ResponseBytes > MaxHTTPResponseBytes || p.Deadline < time.Millisecond || p.Deadline > time.Duration(MaxHTTPDeadlineMS)*time.Millisecond || p.Deadline%time.Millisecond != 0 || p.Concurrency < 1 || p.Concurrency > MaxHTTPConcurrency || len(p.Paths) < 1 || len(p.Paths) > 16 || len(p.Methods) < 1 || len(p.Methods) > 7 {
		return zero, ErrInvalidHTTPPolicy
	}
	for i, path := range p.Paths {
		if !validHTTPPolicyPath(path) || (len(path) > 1 && strings.HasSuffix(path, "/")) || slices.Contains(p.Paths[:i], path) {
			return zero, ErrInvalidHTTPPolicy
		}
	}
	for i, method := range p.Methods {
		if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, method) || slices.Contains(p.Methods[:i], method) {
			return zero, ErrInvalidHTTPPolicy
		}
	}
	p.Paths = slices.Clone(p.Paths)
	p.Methods = slices.Clone(p.Methods)
	slices.Sort(p.Paths)
	slices.Sort(p.Methods)
	raw, err := encodeHTTPPolicy(p)
	if err != nil || len(raw) > MaxHTTPPolicyBytes {
		return zero, ErrInvalidHTTPPolicy
	}
	return p, nil
}

func CanonicalHTTPPolicy(p HTTPPolicy) ([]byte, error) {
	p, err := NormalizeHTTPPolicy(p)
	if err != nil {
		return nil, err
	}
	return encodeHTTPPolicy(p)
}

func encodeHTTPPolicy(p HTTPPolicy) ([]byte, error) {
	raw, err := json.Marshal(httpPolicyWire{Format: HTTPPolicyFormat, Origin: p.Origin, Paths: p.Paths, Methods: p.Methods, RequestBytes: p.RequestBytes, ResponseBytes: p.ResponseBytes, DeadlineMS: p.Deadline.Milliseconds(), Concurrency: p.Concurrency})
	if err != nil || len(raw) > MaxHTTPPolicyBytes {
		return nil, ErrInvalidHTTPPolicy
	}
	return raw, nil
}

// ParseHTTPPolicy accepts only the exact canonical bytes, not a JSON-equivalent
// alternative. Use this entry point on complete stored/transmitted policy bytes.
func ParseHTTPPolicy(raw []byte) (ParsedHTTPPolicy, error) {
	var zero ParsedHTTPPolicy
	if len(raw) == 0 || len(raw) > MaxHTTPPolicyBytes || !utf8.Valid(raw) {
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
	var w httpPolicyWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || w.Format != HTTPPolicyFormat || w.DeadlineMS < 1 || w.DeadlineMS > MaxHTTPDeadlineMS {
		return zero, ErrInvalidHTTPPolicy
	}
	p, err := NormalizeHTTPPolicy(HTTPPolicy{Origin: w.Origin, Paths: w.Paths, Methods: w.Methods, RequestBytes: w.RequestBytes, ResponseBytes: w.ResponseBytes, Deadline: time.Duration(w.DeadlineMS) * time.Millisecond, Concurrency: w.Concurrency})
	if err != nil {
		return zero, err
	}
	canonical, err := CanonicalHTTPPolicy(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidHTTPPolicy
	}
	sum := sha256.Sum256(raw)
	return ParsedHTTPPolicy{Policy: p, SHA256: hex.EncodeToString(sum[:])}, nil
}

func (p HTTPPolicy) MarshalJSON() ([]byte, error) { return CanonicalHTTPPolicy(p) }

// A containing review still has to validate its own complete canonical bytes;
// encoding/json may discard whitespace outside this nested policy object.
func (p *HTTPPolicy) UnmarshalJSON(raw []byte) error {
	parsed, err := ParseHTTPPolicy(raw)
	if err != nil {
		return err
	}
	*p = parsed.Policy
	return nil
}

// ValidateHTTPPolicyTarget uses the same rules as review encoding and actual
// dispatch. It performs no DNS, IO or authorization and cannot grant a request.
func ValidateHTTPPolicyTarget(p HTTPPolicy, method, raw string) error {
	p, err := NormalizeHTTPPolicy(p)
	if err != nil || len(raw) > 4096 || !slices.Contains(p.Methods, method) {
		return ErrInvalidHTTPPolicy
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || "https://"+u.Host != p.Origin || u.User != nil || u.Fragment != "" || strings.Contains(raw, "#") || u.Opaque != "" || u.RawPath != "" || !validHTTPPolicyPath(u.Path) {
		return ErrInvalidHTTPPolicy
	}
	for _, prefix := range p.Paths {
		if prefix == "/" || u.Path == prefix || strings.HasPrefix(u.Path, prefix+"/") {
			return nil
		}
	}
	return ErrInvalidHTTPPolicy
}

func validHTTPPolicyHost(host string) bool {
	if len(host) < 3 || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, b := range []byte(label) {
			if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-') {
				return false
			}
		}
	}
	for _, b := range []byte(labels[len(labels)-1]) {
		if b >= 'a' && b <= 'z' {
			return true
		}
	}
	return false
}
func validHTTPPolicyPath(path string) bool {
	if len(path) < 1 || len(path) > 1024 || path[0] != '/' || strings.Contains(path, "//") {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
		for _, b := range []byte(segment) {
			if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || strings.ContainsRune("._~-", rune(b))) {
				return false
			}
		}
	}
	return true
}

// ValidHTTPPolicyHeaderValue bounds the two supported request header values;
// it cannot permit arbitrary header names or inject additional header lines.
func ValidHTTPPolicyHeaderValue(s string) bool {
	if len(s) > 256 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 32 || b > 126 {
			return false
		}
	}
	return true
}
