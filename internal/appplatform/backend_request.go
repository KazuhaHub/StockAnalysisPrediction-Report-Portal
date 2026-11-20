package appplatform

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
)

// A viewer assertion is two token parts; a public, ingress or machine one has the visible "pub.",
// "ing." or "mch." prefix before them.
var backendRequestAssertion = regexp.MustCompile(`^((pub|ing|mch)\.)?[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

const MaxBackendRequestBody = 65536
const MaxBackendAssertion = 32768
const MaxBackendResponseBody = 1048576

func ValidBackendRequestRoute(method, path string) bool {
	// Keep ParseRequestURI and the HTTP client's URL parser in agreement.
	// Query values and non-ASCII path segments must be percent encoded.
	if strings.Contains(path, "#") || strings.IndexFunc(path, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return false
	}
	policy := protocol.Policy{Routes: []protocol.Route{{Method: method, Prefix: "/v1"}}}
	if !policy.Allows(method, path) {
		return false
	}
	u, err := url.ParseRequestURI(path)
	if err != nil {
		return false
	}
	if u.RequestURI() != path {
		return false
	}
	decoded := u.Path
	for range 2 {
		decoded, err = url.PathUnescape(decoded)
		if err != nil {
			return false
		}
	}
	if !utf8.ValidString(decoded) || strings.Contains(decoded, "%") || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return false
	}
	// The reserved key-possession operation has a separate read-only protocol.
	return decoded != "/v1/handshake" && !strings.HasPrefix(decoded, "/v1/handshake/")
}
func ValidBackendRequestData(method, path string, body []byte, assertion string) bool {
	// The reserved route spaces are judged on the decoded path, never on the spelling: an encoded or dot
	// segment spelling must not put a token of one kind into another's space, nor a viewer token into
	// either reserved one.
	space, clean := backendRequestSpace(path)
	if !clean {
		return false
	}
	pathOnly, _, _ := strings.Cut(path, "?")
	switch {
	case IsPublicAssertion(assertion):
		// A public assertion (doc 179) is read-only and bound to the public route space, in its one
		// canonical spelling; the runner refuses anything else before it picks the public header.
		if method != "GET" || len(body) != 0 || space != "public" || strings.Contains(pathOnly, "%") || !strings.HasPrefix(pathOnly, "/v1/public/") {
			return false
		}
	case IsIngressAssertion(assertion):
		// An ingress assertion (doc 180) is bound to a POST of the ingress route space with a JSON body.
		if method != "POST" || len(body) == 0 || len(body) > maxIngressBodyBytes || space != "ingress" || strings.Contains(path, "?") || strings.Contains(pathOnly, "%") || !strings.HasPrefix(pathOnly, "/v1/ingress/") {
			return false
		}
	case IsMachineAssertion(assertion):
		// A machine assertion (doc 183) is the same for the machine route space.
		if method != "GET" || len(body) != 0 || space != "machine" || strings.Contains(pathOnly, "%") || !strings.HasPrefix(pathOnly, "/v1/machine/") {
			return false
		}
	case space != "":
		// A viewer token never reaches a reserved space.
		return false
	}
	return ValidBackendRequestRoute(method, path) && len(body) <= MaxBackendRequestBody && (len(body) == 0 || (method != "GET" && json.Valid(body))) && len(assertion) > 0 && len(assertion) <= MaxBackendAssertion && utf8.ValidString(assertion) && backendRequestAssertion.MatchString(assertion)
}
func ValidBackendResponse(status int, body []byte) bool {
	if len(body) == 0 || len(body) > MaxBackendResponseBody || !json.Valid(body) {
		return false
	}
	if status >= 200 && status < 300 {
		return true
	}
	switch status {
	case 400, 403, 404, 409, 422, 429:
		return true
	}
	return false
}

// backendRequestSpace names the reserved route space ("public", "ingress", "machine" or "") a request path falls
// in once decoded the way the relay's own validation decodes it (twice, ending with no "%"), and
// reports whether the path is clean: valid escapes, no empty segment except a trailing one, no "." or
// ".." segment, so that no spelling of the path can hide its space.
func backendRequestSpace(path string) (string, bool) {
	space, clean := backendRequestSpaceStrict(path)
	if clean || !pathReachesReservedSpace(path) {
		// An ordinary path keeps every spelling it had before the reserved spaces existed; the
		// strictness applies where a spelling could hide a reserved space.
		return space, true
	}
	return "", false
}

// pathReachesReservedSpace reports whether any backend that normalises the path (decodes it twice,
// ignores case, drops path parameters after ";", empty and "." segments and resolves "..") would
// route it into a reserved space or the handshake. It errs towards yes: it decodes as far as it can.
func pathReachesReservedSpace(path string) bool {
	decoded, _, _ := strings.Cut(path, "?")
	for range 2 {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			break
		}
		decoded = next
	}
	var kept []string
	for _, segment := range strings.Split(strings.ToLower(decoded), "/") {
		segment, _, _ = strings.Cut(segment, ";")
		switch segment {
		case "", ".":
		case "..":
			if len(kept) > 0 {
				kept = kept[:len(kept)-1]
			}
		default:
			kept = append(kept, segment)
		}
	}
	if len(kept) < 2 || kept[0] != "v1" {
		return false
	}
	switch kept[1] {
	case "public", "ingress", "machine", "handshake":
		return true
	}
	return false
}

func backendRequestSpaceStrict(path string) (string, bool) {
	decoded, _, _ := strings.Cut(path, "?")
	for range 2 {
		// A semicolon is a path-parameter separator to a backend that is not Go's, so it is never
		// accepted, in either spelling (it is looked for before and after each decoding).
		if strings.Contains(decoded, ";") {
			return "", false
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		decoded = next
	}
	if !strings.HasPrefix(decoded, "/") || strings.Contains(decoded, ";") {
		return "", false
	}
	// Spaces are compared without regard to case: a backend that is not Go's may route "/v1/MACHINE/x"
	// to its machine handler, or "/v1/INGRESS/x" to its ingress handler. A token of the space's own kind must still use the exact lower-case
	// spelling.
	decoded = strings.ToLower(decoded)
	segments := strings.Split(decoded[1:], "/")
	for i, segment := range segments {
		if segment == "." || segment == ".." || (segment == "" && i != len(segments)-1) {
			return "", false
		}
	}
	for _, space := range []string{"public", "ingress", "machine"} {
		if decoded == "/v1/"+space || strings.HasPrefix(decoded, "/v1/"+space+"/") {
			return space, true
		}
	}
	return "", true
}
