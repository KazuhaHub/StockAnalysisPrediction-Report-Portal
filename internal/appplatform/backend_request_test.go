package appplatform

import (
	"strings"
	"testing"
)

func TestBackendRequestBoundaries(t *testing.T) {
	for _, path := range []string{"/v1/echo", "/v1/items/1", "/v1/items?q=x", "/v1/handshakes", "/v1/%E4%B8%AD", "/v1/echo?q=%E4%B8%AD%20x"} {
		if !ValidBackendRequestRoute("GET", path) {
			t.Fatal("valid route", path)
		}
	}
	for _, path := range []string{"/v1/handshake", "/v1/handshake/x", "/v1/%68andshake", "/v1/%2568andshake", "/v1/%25252568andshake", "/v1/%2525252e%2525252e/x", "/v1/a/../b", "/v1/a%252f..%252fb", "//example.invalid/v1/echo", "https://example.invalid/v1/echo", "/health/ready", "/v1/a\\b", "/v1/a%0db", "/v1/echo#x", "/v1/a%00b", "/v1/%ff", "/v1/中", "/v1/echo?q=#x", "/v1/echo?q=a b", "/v1/echo?q=中"} {
		if ValidBackendRequestRoute("POST", path) {
			t.Fatal("unsafe route", path)
		}
	}
	if ValidBackendRequestRoute("PATCH", "/v1/echo") || ValidBackendRequestData("GET", "/v1/echo", []byte(`{}`), "a.b") || ValidBackendRequestData("POST", "/v1/echo", []byte("invalid"), "a.b") || ValidBackendRequestData("POST", "/v1/echo", make([]byte, 65537), "a.b") || ValidBackendRequestData("POST", "/v1/echo", nil, strings.Repeat("a", 32769)+".b") {
		t.Fatal("request bounds loosened")
	}
	for _, status := range []int{200, 201, 400, 403, 404, 409, 422, 429} {
		if !ValidBackendResponse(status, []byte(`{"ok":true}`)) {
			t.Fatal("safe response rejected")
		}
	}
	for _, status := range []int{101, 301, 302, 401, 418, 500} {
		if ValidBackendResponse(status, []byte(`{}`)) {
			t.Fatal("unsafe status accepted")
		}
	}
	if ValidBackendResponse(200, nil) || ValidBackendResponse(200, []byte("<html>")) || ValidBackendResponse(200, make([]byte, MaxBackendResponseBody+1)) {
		t.Fatal("response bounds loosened")
	}
}
