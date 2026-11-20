package appplatform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Alibaba Cloud ACS3-HMAC-SHA256 request signing (doc 177).
//
// This file is pure: it computes the signed request headers from a request and a
// key, and checks that a set of headers is exactly the shape Portal produces for a
// request. Portal signs (it alone holds the AccessKey secret); the runner only
// checks the shape, which binds every signed field to the request it sends: it
// holds no key and cannot produce or alter a signature, and a header, path or
// query it changed would make the provider refuse the request.

// ACS3Scheme is the signing scheme name carried by a signed command and a request.
const ACS3Scheme = "acs3"

// ACS3Algorithm is the algorithm string of the string to sign and the Authorization
// header.
const ACS3Algorithm = "ACS3-HMAC-SHA256"

const (
	// ACS3MaxSignedHeaders is the most pairs a command carries: host, content-type,
	// x-acs-action, x-acs-version, x-acs-date, x-acs-signature-nonce,
	// x-acs-content-sha256, x-acs-security-token and authorization.
	ACS3MaxSignedHeaders = 9
	// ACS3MaxSignedHeaderBytes bounds the names and values together.
	ACS3MaxSignedHeaderBytes = 16 << 10

	ACS3MaxAccessKeyIDBytes     = 128
	ACS3MaxAccessKeySecretBytes = 256
	ACS3MaxSecurityTokenBytes   = 4096
)

var (
	ErrInvalidACS3 = errors.New("invalid ACS3 request")

	acs3ActionPattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,63}$`)
	acs3VersionPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	acs3NoncePattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	acs3SHAPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ACS3 header names, lower case. The set is closed: a command carries these and
// no other header.
const (
	acs3Authorization = "authorization"
	acs3ContentType   = "content-type"
	acs3Host          = "host"
	acs3Action        = "x-acs-action"
	acs3ContentSHA    = "x-acs-content-sha256"
	acs3Date          = "x-acs-date"
	acs3Token         = "x-acs-security-token"
	acs3Nonce         = "x-acs-signature-nonce"
	acs3Version       = "x-acs-version"
)

// SignedHeader is one header a signed command carries. Value is a byte slice so
// that the holder can wipe it: the Authorization value derives from the key and
// the security token is a secret.
type SignedHeader struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// ValidACS3Action reports whether s is an API action name.
func ValidACS3Action(s string) bool { return acs3ActionPattern.MatchString(s) }

// ValidACS3Version reports whether s is an API version (YYYY-MM-DD).
func ValidACS3Version(s string) bool { return acs3VersionPattern.MatchString(s) }

// ACS3Input is everything a signature covers.
type ACS3Input struct {
	Method string
	// Target is the absolute https URL the request is sent to, exactly as the
	// runner will send it. Its query must already be in canonical form (see
	// ACS3CanonicalQuery): the bytes signed are then the bytes sent.
	Target string
	// ContentType is the request's Content-Type, or empty for none.
	ContentType string
	// BodySHA256 is the lower-case hex SHA-256 of the exact body.
	BodySHA256 string
	Action     string
	Version    string
	// Date is the signing time (Portal's clock); it is rendered in UTC.
	Date time.Time
	// Nonce is 32 lower-case hex characters, fresh for every request.
	Nonce         string
	AccessKeyID   string
	SecurityToken []byte
}

func acs3Unreserved(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~'
}

// acs3Escape is RFC 3986 percent-encoding: unreserved characters stay, every
// other byte is %XX in upper-case hex.
func acs3Escape(s string, keepSlash bool) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if acs3Unreserved(c) || keepSlash && c == '/' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upper[c>>4])
		b.WriteByte(upper[c&15])
	}
	return b.String()
}

type acs3Pair struct{ name, value string }

// acs3ParseQuery splits a raw query into decoded pairs, strictly: no empty pair, no
// empty name, no '+', ';' or malformed escape, so the pairs mean one thing to every
// reader.
func acs3ParseQuery(raw string) ([]acs3Pair, error) {
	if raw == "" {
		return nil, nil
	}
	if strings.ContainsAny(raw, "+;#") {
		return nil, ErrInvalidACS3
	}
	var pairs []acs3Pair
	for _, part := range strings.Split(raw, "&") {
		name, value, _ := strings.Cut(part, "=")
		if name == "" {
			return nil, ErrInvalidACS3
		}
		n, err1 := url.PathUnescape(name)
		v, err2 := url.PathUnescape(value)
		if err1 != nil || err2 != nil {
			return nil, ErrInvalidACS3
		}
		pairs = append(pairs, acs3Pair{n, v})
	}
	return pairs, nil
}

// ACS3CanonicalQuery is the canonical query string of a raw query: the pairs
// sorted by name (then value), each name and value RFC 3986 encoded, joined as
// name=value (an empty value is "name=") with '&'.
func ACS3CanonicalQuery(raw string) (string, error) {
	pairs, err := acs3ParseQuery(raw)
	if err != nil {
		return "", err
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		a, b := acs3Escape(pairs[i].name, false), acs3Escape(pairs[j].name, false)
		if a != b {
			return a < b
		}
		return acs3Escape(pairs[i].value, false) < acs3Escape(pairs[j].value, false)
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = acs3Escape(p.name, false) + "=" + acs3Escape(p.value, false)
	}
	return strings.Join(parts, "&"), nil
}

// acs3Target parses the request target: absolute https, a host, no user info and
// no fragment. It returns the host as the request will carry it, the canonical
// path and the (already canonical) query.
func acs3Target(target string) (host, path, query string, err error) {
	if len(target) == 0 || len(target) > MaxHTTPTargetBytes || strings.Contains(target, "#") {
		return "", "", "", ErrInvalidACS3
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.Fragment != "" || u.Host != strings.ToLower(u.Host) {
		return "", "", "", ErrInvalidACS3
	}
	canonicalQuery, err := ACS3CanonicalQuery(u.RawQuery)
	if err != nil || canonicalQuery != u.RawQuery {
		return "", "", "", ErrInvalidACS3
	}
	path = u.EscapedPath()
	if path == "" {
		path = "/"
	}
	// The path the runner sends is u.RequestURI's: refuse a path whose encoding is
	// not already the canonical one, so the signed bytes are the sent bytes.
	if acs3Escape(u.Path, true) != path {
		return "", "", "", ErrInvalidACS3
	}
	return u.Host, path, u.RawQuery, nil
}

// ValidACS3Target reports whether target can be signed exactly as it is sent.
func ValidACS3Target(target string) bool {
	_, _, _, err := acs3Target(target)
	return err == nil
}

func acs3PrintableASCII(b []byte) bool {
	for _, c := range b {
		if c < 32 || c > 126 {
			return false
		}
	}
	return true
}

// ValidACS3AccessKeyID, ValidACS3AccessKeySecret and ValidACS3SecurityToken are
// the credential value rules (doc 177 section 3.1). The identifier also appears
// unescaped in the Authorization header, so ',' and '=' are refused in it.
func ValidACS3AccessKeyID(id []byte) bool {
	return len(id) >= 1 && len(id) <= ACS3MaxAccessKeyIDBytes && acs3PrintableASCII(id) && !strings.ContainsAny(string(id), ",= ")
}
func ValidACS3AccessKeySecret(secret []byte) bool {
	return len(secret) >= 1 && len(secret) <= ACS3MaxAccessKeySecretBytes && acs3PrintableASCII(secret)
}
func ValidACS3SecurityToken(token []byte) bool {
	return len(token) <= ACS3MaxSecurityTokenBytes && acs3PrintableASCII(token)
}

func acs3Hex(sum [32]byte) string { return hex.EncodeToString(sum[:]) }

// ACS3Sign returns the headers of the signed request, sorted by name:
// authorization, content-type (when the request has one), host, x-acs-action,
// x-acs-content-sha256, x-acs-date, x-acs-security-token (when given),
// x-acs-signature-nonce and x-acs-version. secret is used as the HMAC key and is
// not copied.
func ACS3Sign(in ACS3Input, secret []byte) ([]SignedHeader, error) {
	host, path, query, err := acs3Target(in.Target)
	if err != nil {
		return nil, err
	}
	if !ValidHTTPRequestMethod(in.Method) || !ValidHTTPPolicyHeaderValue(in.ContentType) || !acs3SHAPattern.MatchString(in.BodySHA256) ||
		!ValidACS3Action(in.Action) || !ValidACS3Version(in.Version) || !acs3NoncePattern.MatchString(in.Nonce) ||
		!ValidACS3AccessKeyID([]byte(in.AccessKeyID)) || !ValidACS3AccessKeySecret(secret) || !ValidACS3SecurityToken(in.SecurityToken) || in.Date.IsZero() {
		return nil, ErrInvalidACS3
	}
	date := in.Date.UTC().Format("2006-01-02T15:04:05Z")
	headers := map[string][]byte{
		acs3Host: []byte(host), acs3Action: []byte(in.Action), acs3Version: []byte(in.Version),
		acs3Date: []byte(date), acs3Nonce: []byte(in.Nonce), acs3ContentSHA: []byte(in.BodySHA256),
	}
	if len(in.SecurityToken) > 0 {
		headers[acs3Token] = slices.Clone(in.SecurityToken)
	}
	if in.ContentType != "" {
		headers[acs3ContentType] = []byte(in.ContentType)
	}
	names := make([]string, 0, len(headers)+1)
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonical strings.Builder
	canonical.WriteString(in.Method + "\n" + path + "\n" + query + "\n")
	for _, name := range names {
		canonical.WriteString(name + ":" + strings.TrimSpace(string(headers[name])) + "\n")
	}
	signedNames := strings.Join(names, ";")
	canonical.WriteString("\n" + signedNames + "\n" + in.BodySHA256)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ACS3Algorithm + "\n" + acs3Hex(sha256.Sum256([]byte(canonical.String())))))
	signature := hex.EncodeToString(mac.Sum(nil))
	headers[acs3Authorization] = []byte(ACS3Algorithm + " Credential=" + in.AccessKeyID + ",SignedHeaders=" + signedNames + ",Signature=" + signature)
	all := append(names, acs3Authorization)
	sort.Strings(all)
	out := make([]SignedHeader, 0, len(all))
	for _, name := range all {
		out = append(out, SignedHeader{Name: name, Value: headers[name]})
	}
	return out, nil
}

// ACS3HeaderValue is the value of the named header, or nil.
func ACS3HeaderValue(headers []SignedHeader, name string) []byte {
	for _, h := range headers {
		if h.Name == name {
			return h.Value
		}
	}
	return nil
}

// ACS3ActionOf is the action and version a signed header set names.
func ACS3ActionOf(headers []SignedHeader) (action, version string) {
	return string(ACS3HeaderValue(headers, acs3Action)), string(ACS3HeaderValue(headers, acs3Version))
}

// ACS3SecretHeaders are the names of the headers whose values are secret or
// derive from the secret: the Authorization value (it carries the signature) and
// the security token.
func ACS3SecretHeader(name string) bool { return name == acs3Authorization || name == acs3Token }

// ACS3StandInHeaders is a copy of headers with the secret values replaced by
// stand-ins of the right shape, for a holder that must validate the shape of a
// command it no longer holds the secrets of.
func ACS3StandInHeaders(headers []SignedHeader) []SignedHeader {
	out := make([]SignedHeader, len(headers))
	var names []string
	for _, h := range headers {
		if h.Name != acs3Authorization {
			names = append(names, h.Name)
		}
	}
	for i, h := range headers {
		out[i] = SignedHeader{Name: h.Name, Value: slices.Clone(h.Value)}
		switch h.Name {
		case acs3Authorization:
			out[i].Value = []byte(ACS3Algorithm + " Credential=x,SignedHeaders=" + strings.Join(names, ";") + ",Signature=" + strings.Repeat("0", 64))
		case acs3Token:
			out[i].Value = []byte("x")
		}
	}
	return out
}

// CheckACS3SignedHeaders checks that headers are exactly what ACS3Sign produces
// for this request, as far as a holder without the key can: the closed name set,
// sorted and unique, at most ACS3MaxSignedHeaders and ACS3MaxSignedHeaderBytes;
// host, content-type, x-acs-content-sha256 equal to the request's own; a canonical
// query; well-formed action, version, date and nonce; and an Authorization whose
// SignedHeaders list is exactly the other names. It cannot verify the signature
// itself, which needs the secret, so a header the holder changes is caught here
// when it stops matching the request and by the provider otherwise.
func CheckACS3SignedHeaders(method, target, contentType, bodySHA256 string, headers []SignedHeader) error {
	host, _, _, err := acs3Target(target)
	if err != nil || !ValidHTTPRequestMethod(method) || !ValidHTTPPolicyHeaderValue(contentType) || !acs3SHAPattern.MatchString(bodySHA256) {
		return ErrInvalidACS3
	}
	if len(headers) < 7 || len(headers) > ACS3MaxSignedHeaders {
		return ErrInvalidACS3
	}
	total := 0
	var names []string
	byName := map[string][]byte{}
	for i, h := range headers {
		total += len(h.Name) + len(h.Value)
		if i > 0 && headers[i-1].Name >= h.Name {
			return ErrInvalidACS3
		}
		switch h.Name {
		case acs3Authorization, acs3ContentType, acs3Host, acs3Action, acs3ContentSHA, acs3Date, acs3Token, acs3Nonce, acs3Version:
		default:
			return ErrInvalidACS3
		}
		if len(h.Value) == 0 || !acs3PrintableASCII(h.Value) || h.Value[0] == ' ' || h.Value[len(h.Value)-1] == ' ' {
			return ErrInvalidACS3
		}
		byName[h.Name] = h.Value
		if h.Name != acs3Authorization {
			names = append(names, h.Name)
		}
	}
	if total > ACS3MaxSignedHeaderBytes {
		return ErrInvalidACS3
	}
	for _, required := range []string{acs3Authorization, acs3Host, acs3Action, acs3Version, acs3Date, acs3Nonce, acs3ContentSHA} {
		if _, ok := byName[required]; !ok {
			return ErrInvalidACS3
		}
	}
	if string(byName[acs3Host]) != host || string(byName[acs3ContentSHA]) != bodySHA256 {
		return ErrInvalidACS3
	}
	if got, ok := byName[acs3ContentType]; ok != (contentType != "") || ok && string(got) != contentType {
		return ErrInvalidACS3
	}
	if !ValidACS3Action(string(byName[acs3Action])) || !ValidACS3Version(string(byName[acs3Version])) || !acs3NoncePattern.Match(byName[acs3Nonce]) {
		return ErrInvalidACS3
	}
	if when, err := time.Parse("2006-01-02T15:04:05Z", string(byName[acs3Date])); err != nil || when.Format("2006-01-02T15:04:05Z") != string(byName[acs3Date]) {
		return ErrInvalidACS3
	}
	if token, ok := byName[acs3Token]; ok && (len(token) > ACS3MaxSecurityTokenBytes) {
		return ErrInvalidACS3
	}
	authorization := string(byName[acs3Authorization])
	rest, ok := strings.CutPrefix(authorization, ACS3Algorithm+" Credential=")
	if !ok {
		return ErrInvalidACS3
	}
	id, rest, ok := strings.Cut(rest, ",SignedHeaders=")
	if !ok || !ValidACS3AccessKeyID([]byte(id)) {
		return ErrInvalidACS3
	}
	signed, signature, ok := strings.Cut(rest, ",Signature=")
	if !ok || signed != strings.Join(names, ";") || !acs3SHAPattern.MatchString(signature) {
		return ErrInvalidACS3
	}
	return nil
}
