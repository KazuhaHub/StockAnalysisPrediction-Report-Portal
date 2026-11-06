package appplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const HTTPStreamPolicyFormat = "portal.http-stream-policy/1-dev.1"
const MaxHTTPStreamPolicyBytes = 8192
const MaxHTTPStreamBytes = 4 << 20
const MaxHTTPStreamFrameBytes = 32 << 10
const MaxHTTPStreamDurationMS = 30000
const MaxHTTPStreamIdleMS = 15000
const MaxHTTPStreamConcurrency = 4
const MaxHTTPStreamChecks = 160

var httpStreamMediaTypes = []string{
	"application/json-seq",
	"application/x-ndjson",
	"text/event-stream",
}

// HTTPStreamPolicy is a host-reviewed restriction for ordinary HTTPS response
// streaming. It never authorizes protocol upgrades, arbitrary tunnels or a
// caller-selected destination.
type HTTPStreamPolicy struct {
	Origin       string
	Paths        []string
	Methods      []string
	RequestBytes int64
	StreamBytes  int64
	Duration     time.Duration
	IdleTimeout  time.Duration
	Concurrency  int
	MediaTypes   []string
}

type httpStreamPolicyWire struct {
	Format        string   `json:"format"`
	Origin        string   `json:"origin"`
	Paths         []string `json:"paths"`
	Methods       []string `json:"methods"`
	RequestBytes  int64    `json:"request_bytes"`
	StreamBytes   int64    `json:"stream_bytes"`
	DurationMS    int64    `json:"duration_ms"`
	IdleTimeoutMS int64    `json:"idle_timeout_ms"`
	Concurrency   int      `json:"concurrency"`
	MediaTypes    []string `json:"media_types"`
}

type ParsedHTTPStreamPolicy struct {
	Policy HTTPStreamPolicy
	SHA256 string
}

// NormalizeHTTPStreamPolicy validates and clones the selected restriction.
// Paths, methods and media types are sets in authority terms.
func NormalizeHTTPStreamPolicy(p HTTPStreamPolicy) (HTTPStreamPolicy, error) {
	var zero HTTPStreamPolicy
	base := HTTPPolicy{
		Origin:       p.Origin,
		Paths:        p.Paths,
		Methods:      p.Methods,
		RequestBytes: p.RequestBytes,
		// The shared validator requires a buffered response bound. It is not used
		// as authority here; the stream limit below remains the effective bound.
		ResponseBytes: 1,
		Deadline:      time.Second,
		Concurrency:   p.Concurrency,
	}
	normalized, err := NormalizeHTTPPolicy(base)
	if err != nil ||
		p.StreamBytes < 1 || p.StreamBytes > MaxHTTPStreamBytes ||
		p.Duration < time.Millisecond || p.Duration > time.Duration(MaxHTTPStreamDurationMS)*time.Millisecond ||
		p.Duration%time.Millisecond != 0 ||
		p.IdleTimeout < time.Millisecond || p.IdleTimeout > time.Duration(MaxHTTPStreamIdleMS)*time.Millisecond ||
		p.IdleTimeout%time.Millisecond != 0 || p.IdleTimeout > p.Duration ||
		len(p.MediaTypes) < 1 || len(p.MediaTypes) > len(httpStreamMediaTypes) {
		return zero, ErrInvalidHTTPPolicy
	}
	for i, method := range normalized.Methods {
		if method != "GET" && method != "POST" || slices.Contains(normalized.Methods[:i], method) {
			return zero, ErrInvalidHTTPPolicy
		}
	}
	for i, mediaType := range p.MediaTypes {
		if !slices.Contains(httpStreamMediaTypes, mediaType) || slices.Contains(p.MediaTypes[:i], mediaType) {
			return zero, ErrInvalidHTTPPolicy
		}
	}
	p.Origin = normalized.Origin
	p.Paths = slices.Clone(normalized.Paths)
	p.Methods = slices.Clone(normalized.Methods)
	p.MediaTypes = slices.Clone(p.MediaTypes)
	slices.Sort(p.Paths)
	slices.Sort(p.Methods)
	slices.Sort(p.MediaTypes)
	raw, err := encodeHTTPStreamPolicy(p)
	if err != nil || len(raw) > MaxHTTPStreamPolicyBytes {
		return zero, ErrInvalidHTTPPolicy
	}
	return p, nil
}

func CanonicalHTTPStreamPolicy(p HTTPStreamPolicy) ([]byte, error) {
	p, err := NormalizeHTTPStreamPolicy(p)
	if err != nil {
		return nil, err
	}
	return encodeHTTPStreamPolicy(p)
}

func encodeHTTPStreamPolicy(p HTTPStreamPolicy) ([]byte, error) {
	raw, err := json.Marshal(httpStreamPolicyWire{
		Format:        HTTPStreamPolicyFormat,
		Origin:        p.Origin,
		Paths:         p.Paths,
		Methods:       p.Methods,
		RequestBytes:  p.RequestBytes,
		StreamBytes:   p.StreamBytes,
		DurationMS:    p.Duration.Milliseconds(),
		IdleTimeoutMS: p.IdleTimeout.Milliseconds(),
		Concurrency:   p.Concurrency,
		MediaTypes:    p.MediaTypes,
	})
	if err != nil || len(raw) > MaxHTTPStreamPolicyBytes {
		return nil, ErrInvalidHTTPPolicy
	}
	return raw, nil
}

// ParseHTTPStreamPolicy accepts only exact canonical bytes. Callers must still
// bind the result to a current grant and viewer authority before dispatch.
func ParseHTTPStreamPolicy(raw []byte) (ParsedHTTPStreamPolicy, error) {
	var zero ParsedHTTPStreamPolicy
	if len(raw) == 0 || len(raw) > MaxHTTPStreamPolicyBytes || !utf8.Valid(raw) {
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
	var w httpStreamPolicyWire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&w) != nil || w.Format != HTTPStreamPolicyFormat {
		return zero, ErrInvalidHTTPPolicy
	}
	p, err := NormalizeHTTPStreamPolicy(HTTPStreamPolicy{
		Origin:       w.Origin,
		Paths:        w.Paths,
		Methods:      w.Methods,
		RequestBytes: w.RequestBytes,
		StreamBytes:  w.StreamBytes,
		Duration:     time.Duration(w.DurationMS) * time.Millisecond,
		IdleTimeout:  time.Duration(w.IdleTimeoutMS) * time.Millisecond,
		Concurrency:  w.Concurrency,
		MediaTypes:   w.MediaTypes,
	})
	if err != nil {
		return zero, err
	}
	canonical, err := CanonicalHTTPStreamPolicy(p)
	if err != nil || !bytes.Equal(canonical, raw) {
		return zero, ErrInvalidHTTPPolicy
	}
	sum := sha256.Sum256(raw)
	return ParsedHTTPStreamPolicy{Policy: p, SHA256: hex.EncodeToString(sum[:])}, nil
}

func (p HTTPStreamPolicy) MarshalJSON() ([]byte, error) {
	return CanonicalHTTPStreamPolicy(p)
}

func (p *HTTPStreamPolicy) UnmarshalJSON(raw []byte) error {
	parsed, err := ParseHTTPStreamPolicy(raw)
	if err != nil {
		return err
	}
	*p = parsed.Policy
	return nil
}

func ValidateHTTPStreamPolicyTarget(p HTTPStreamPolicy, method, raw string) error {
	p, err := NormalizeHTTPStreamPolicy(p)
	if err != nil {
		return ErrInvalidHTTPPolicy
	}
	base := HTTPPolicy{Origin: p.Origin, Paths: p.Paths, Methods: p.Methods, RequestBytes: p.RequestBytes, ResponseBytes: 1, Deadline: time.Second, Concurrency: p.Concurrency}
	return ValidateHTTPPolicyTarget(base, method, raw)
}

// ValidateHTTPStreamContentType returns the bounded response media type only
// when the reviewed stream policy explicitly allows its base type.
func ValidateHTTPStreamContentType(p HTTPStreamPolicy, value string) (string, bool) {
	p, err := NormalizeHTTPStreamPolicy(p)
	if err != nil || !ValidHTTPPolicyHeaderValue(value) {
		return "", false
	}
	base, parameters, err := mime.ParseMediaType(value)
	if err != nil || !slices.Contains(p.MediaTypes, strings.ToLower(base)) {
		return "", false
	}
	if len(parameters) > 1 {
		return "", false
	}
	if len(parameters) == 1 {
		charset, ok := parameters["charset"]
		if !ok || !strings.EqualFold(charset, "utf-8") {
			return "", false
		}
	}
	return strings.ToLower(base), true
}
