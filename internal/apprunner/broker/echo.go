package broker

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
)

// echoTransformMinBytes is the shortest credential whose transformed forms are
// looked for. A few characters transformed would withhold ordinary responses;
// real credentials are far longer. Shorter ones are matched as they are.
const echoTransformMinBytes = 8

// echoMatcher recognises a response that gives a credential back. It looks for
// the value (and, for a Bearer header, the token without its prefix) as it is,
// and in the forms an upstream commonly echoes it in: any letter case, base64
// (standard and URL alphabets, padded or not, at each of the three byte
// alignments), hex, URL-encoded and JSON-escaped. It is a recognition of those
// forms, not a guarantee against every possible encoding.
type echoMatcher struct {
	exact  [][]byte // matched case-sensitively
	folded [][]byte // lower-cased, matched against a lower-cased response
	span   int      // the longest form: how much of a stream must be kept
}

func newEchoMatcher(header string, value []byte) *echoMatcher {
	m := &echoMatcher{}
	if len(value) == 0 {
		return m
	}
	secrets := [][]byte{value}
	if header == "Authorization" && bytes.HasPrefix(value, []byte("Bearer ")) && len(value) > len("Bearer ") {
		secrets = append(secrets, value[len("Bearer "):])
	}
	for _, s := range secrets {
		m.exact = append(m.exact, bytes.Clone(s))
		if len(s) < echoTransformMinBytes {
			continue
		}
		m.folded = append(m.folded, bytes.ToLower(s))
		m.folded = append(m.folded, bytes.ToLower([]byte(hex.EncodeToString(s))))
		m.folded = append(m.folded, bytes.ToLower([]byte(url.QueryEscape(string(s)))))
		m.folded = append(m.folded, bytes.ToLower([]byte(url.PathEscape(string(s)))))
		m.folded = append(m.folded, bytes.ToLower(jsonEscaped(s, true)))
		m.folded = append(m.folded, bytes.ToLower(jsonEscaped(s, false)))
		m.exact = append(m.exact, base64Forms(s)...)
	}
	for _, f := range append(append([][]byte{}, m.exact...), m.folded...) {
		m.span = max(m.span, len(f))
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
	lower := bytes.ToLower(data)
	defer clear(lower)
	for _, f := range m.folded {
		if len(f) > 0 && bytes.Contains(lower, f) {
			return true
		}
	}
	return false
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
