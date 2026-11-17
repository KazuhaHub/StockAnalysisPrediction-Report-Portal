package appplatform

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestHTTPRequestBodyDecoderIsStrictAndBounded(t *testing.T) {
	for _, m := range HTTPRequestMethods {
		if !ValidHTTPRequestMethod(m) {
			t.Fatal(m)
		}
	}
	for _, m := range []string{"", "get", "CONNECT", "TRACE", "GET ", "PROPFIND"} {
		if ValidHTTPRequestMethod(m) {
			t.Fatalf("method %q accepted", m)
		}
	}
	got, err := DecodeHTTPRequestBody("")
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	full := bytes.Repeat([]byte{0, 255, 128, 10}, MaxHTTPRequestBytes/4)
	got, err = DecodeHTTPRequestBody(base64.StdEncoding.EncodeToString(full))
	if err != nil || !bytes.Equal(got, full) {
		t.Fatal("maximum body changed", err)
	}
	over := base64.StdEncoding.EncodeToString(append(bytes.Clone(full), 1))
	for _, bad := range []string{"YQ", "YQ==\n", "YR==", "!!!!", "YQ==YQ==", "_-__", over} {
		if got, err := DecodeHTTPRequestBody(bad); err == nil || len(got) != 0 {
			t.Fatalf("%q accepted", bad)
		}
	}
}
