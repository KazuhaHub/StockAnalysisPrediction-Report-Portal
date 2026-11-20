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

// A viewer assertion is two token parts; a public one has the visible "pub." prefix before them.
var backendRequestAssertion = regexp.MustCompile(`^((pub|ing)\.)?[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

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
		if method != "GET" || len(body) != 0 || space != "public" || strings.Contains(pathOnly, "%") {
			return false
		}
	case IsIngressAssertion(assertion):
		// An ingress assertion (doc 180) is bound to a POST of the ingress route space with a JSON body.
		if method != "POST" || len(body) == 0 || len(body) > maxIngressBodyBytes || space != "ingress" || strings.Contains(path, "?") || strings.Contains(pathOnly, "%") {
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

// backendRequestSpace names the reserved route space ("public", "ingress" or "") a request path falls
// in once decoded the way the relay's own validation decodes it (twice, ending with no "%"), and
// reports whether the path is clean: valid escapes, no empty segment except a trailing one, no "." or
// ".." segment, so that no spelling of the path can hide its space.
func backendRequestSpace(path string) (string, bool) {
	decoded, _, _ := strings.Cut(path, "?")
	for range 2 {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return "", false
		}
		decoded = next
	}
	if !strings.HasPrefix(decoded, "/") {
		return "", false
	}
	segments := strings.Split(decoded[1:], "/")
	for i, segment := range segments {
		if segment == "." || segment == ".." || (segment == "" && i != len(segments)-1) {
			return "", false
		}
	}
	for _, space := range []string{"public", "ingress"} {
		if decoded == "/v1/"+space || strings.HasPrefix(decoded, "/v1/"+space+"/") {
			return space, true
		}
	}
	return "", true
}
