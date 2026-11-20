package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// Transformed forms of a credential are looked for only for values between these
// bounds. A few characters transformed would withhold ordinary responses; real
// credentials are far longer. A value over the upper bound is matched as it is and
// case-folded only, which keeps the longest form (and so the work and the memory a
// stream scan needs) proportional to the value, not a multiple of it.
const (
	echoTransformMinBytes = 8
	echoTransformMaxBytes = 4096
)

// echoMatcher recognises a response that gives a credential back. It looks for
// the value (and, for a Bearer header, the token without its prefix) as it is,
// and in the forms an upstream commonly echoes it in: any letter case, base64
// (standard and URL alphabets, padded or not, at each of the three byte
// alignments), hex, URL-encoded and JSON-escaped. It is a recognition of those
// forms, not a guarantee against every possible encoding.
type echoMatcher struct {
	exact  [][]byte // matched case-sensitively
	folded [][]byte // ASCII lower-cased, matched against an ASCII lower-cased response
	span   int      // the longest form
	// First bytes of the forms, so a stream scan looks closely only where a form
	// could begin.
	firstExact, firstFolded [256]bool
}

// asciiLower lower-cases A-Z only. Unlike bytes.ToLower it never changes the
// length, so offsets in the lowered copy are offsets in the original.
func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

func newEchoMatcher(header string, value []byte) *echoMatcher {
	if len(value) == 0 {
		return &echoMatcher{}
	}
	secrets := [][]byte{value}
	if header == "Authorization" && bytes.HasPrefix(value, []byte("Bearer ")) && len(value) > len("Bearer ") {
		secrets = append(secrets, value[len("Bearer "):])
	}
	return newEchoMatcherFor(secrets)
}

// newRequestEchoMatcher matches what a request carried that a response must not give back: its
// credential header value, or, for a signed request, the Authorization value, the signature in it
// and the security token (the AccessKey secret itself never reaches the runner).
func newRequestEchoMatcher(input HTTPRequest) *echoMatcher {
	return newEchoMatcherFor(echoSecretsOf(input.HeaderName, input.HeaderValue, input.SignedHeaders))
}

func echoSecretsOf(header string, value []byte, signed []appplatform.SignedHeader) [][]byte {
	if len(signed) == 0 {
		if len(value) == 0 {
			return nil
		}
		secrets := [][]byte{value}
		if header == "Authorization" && bytes.HasPrefix(value, []byte("Bearer ")) && len(value) > len("Bearer ") {
			secrets = append(secrets, value[len("Bearer "):])
		}
		return secrets
	}
	var secrets [][]byte
	for _, h := range signed {
		if !appplatform.ACS3SecretHeader(h.Name) || len(h.Value) == 0 {
			continue
		}
		secrets = append(secrets, h.Value)
		if h.Name == "authorization" {
			if i := bytes.LastIndex(h.Value, []byte("Signature=")); i >= 0 {
				secrets = append(secrets, h.Value[i+len("Signature="):])
			}
		}
	}
	return secrets
}

func newEchoMatcherFor(secrets [][]byte) *echoMatcher {
	m := &echoMatcher{}
	if len(secrets) == 0 {
		return m
	}
	seenExact, seenFolded := map[string]bool{}, map[string]bool{}
	addExact := func(f []byte) {
		if len(f) > 0 && !seenExact[string(f)] {
			seenExact[string(f)] = true
			m.exact = append(m.exact, bytes.Clone(f))
		}
	}
	addFolded := func(f []byte) {
		if f = asciiLower(f); len(f) > 0 && !seenFolded[string(f)] {
			seenFolded[string(f)] = true
			m.folded = append(m.folded, f)
		}
	}
	for _, s := range secrets {
		addExact(s)
		if len(s) < echoTransformMinBytes {
			continue
		}
		addFolded(s)
		if len(s) > echoTransformMaxBytes {
			continue
		}
		addFolded([]byte(hex.EncodeToString(s)))
		addFolded([]byte(url.QueryEscape(string(s))))
		addFolded([]byte(url.PathEscape(string(s))))
		addFolded(jsonEscaped(s, true))
		addFolded(jsonEscaped(s, false))
		for _, f := range base64Forms(s) {
			addExact(f)
		}
	}
	for _, f := range m.exact {
		m.span = max(m.span, len(f))
		m.firstExact[f[0]] = true
	}
	for _, f := range m.folded {
		m.span = max(m.span, len(f))
		m.firstFolded[f[0]] = true
	}
	return m
}

// jsonEscaped is s as the inside of a JSON string, with or without the HTML
// escapes (< for <) encoding/json adds by default.
func jsonEscaped(s []byte, escapeHTML bool) []byte {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(escapeHTML)
	if e.Encode(string(s)) != nil {
		return nil
	}
	out := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(out) < 2 {
		return nil
	}
	return bytes.Clone(out[1 : len(out)-1])
}

// base64Forms are the substrings of the base64 of any text that contains s,
// whatever precedes it: the characters determined by s alone, at each of the
// three alignments of s within a 3-byte group, in both alphabets without padding.
func base64Forms(s []byte) [][]byte {
	var out [][]byte
	for _, enc := range []*base64.Encoding{base64.RawStdEncoding, base64.RawURLEncoding} {
		for pad := 0; pad < 3; pad++ {
			buf := append(make([]byte, pad), s...)
			text := []byte(enc.EncodeToString(buf))
			if pad > 0 {
				// The first group mixes the preceding bytes in.
				text = text[4:]
			}
			if rest := len(buf) % 3; rest != 0 {
				// The last partial group depends on what follows s.
				text = text[:len(text)-(rest+1)]
			}
			if len(text) >= echoTransformMinBytes {
				out = append(out, text)
			}
		}
	}
	return out
}

// Match reports whether data contains the credential in a recognised form.
func (m *echoMatcher) Match(data []byte) bool {
	for _, f := range m.exact {
		if len(f) > 0 && bytes.Contains(data, f) {
			return true
		}
	}
	if len(m.folded) == 0 {
		return false
	}
	lower := asciiLower(data)
	defer clear(lower)
	for _, f := range m.folded {
		if len(f) > 0 && bytes.Contains(lower, f) {
			return true
		}
	}
	return false
}

// pending is how many bytes at the end of window must be held back: the longest
// suffix that is a proper prefix of a recognised form, that is a match a later
// chunk could still complete. Everything before it can be released: a match that
// started there would already have been found by Match or would be a pending
// suffix. Only the last span-1 bytes can begin an uncompleted match, so that is
// all that is looked at, and only where a form's first byte occurs.
func (m *echoMatcher) pending(window []byte) int {
	var lower []byte
	defer func() { clear(lower) }()
	for i := max(0, len(window)-(m.span-1)); i < len(window); i++ {
		rest := window[i:]
		if m.firstExact[rest[0]] {
			for _, f := range m.exact {
				if len(f) > len(rest) && bytes.HasPrefix(f, rest) {
					return len(rest)
				}
			}
		}
		c := rest[0]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if m.firstFolded[c] {
			if lower == nil {
				lower = asciiLower(window)
			}
			for _, f := range m.folded {
				if len(f) > len(rest) && bytes.HasPrefix(f, lower[i:]) {
					return len(rest)
				}
			}
		}
	}
	return 0
}

// Clear wipes the matcher's copies of the credential and its forms.
func (m *echoMatcher) Clear() {
	for _, f := range m.exact {
		clear(f)
	}
	for _, f := range m.folded {
		clear(f)
	}
}

func credentialResponseEcho(body []byte, header string, value []byte) bool {
	m := newEchoMatcher(header, value)
	defer m.Clear()
	return m.Match(body)
}
