package appplatform

import (
	"encoding/base64"
	"errors"
	"slices"
)

// HTTPRequestMethods is the closed set of methods an application may name in a
// network request, foreground or background. A reviewed policy narrows it; it
// never widens it. CONNECT, TRACE and extension methods are never accepted.
var HTTPRequestMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// MaxHTTPTargetBytes bounds the request URL an application may name.
const MaxHTTPTargetBytes = 4096

// ValidHTTPRequestMethod reports whether method is in HTTPRequestMethods.
func ValidHTTPRequestMethod(method string) bool {
	return slices.Contains(HTTPRequestMethods, method)
}

var errInvalidHTTPRequestBody = errors.New("invalid http request body encoding")

// DecodeHTTPRequestBody decodes the canonical standard base64 an application
// sends as a request body. It refuses an over-long text before allocating,
// anything not canonical (whitespace, missing or non-zero padding bits, URL
// alphabet) and a decoded body over MaxHTTPRequestBytes. The caller owns the
// returned bytes and should clear them after use.
func DecodeHTTPRequestBody(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(MaxHTTPRequestBytes) {
		return nil, errInvalidHTTPRequestBody
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) > MaxHTTPRequestBytes || base64.StdEncoding.EncodeToString(decoded) != encoded {
		clear(decoded)
		return nil, errInvalidHTTPRequestBody
	}
	return decoded, nil
}
