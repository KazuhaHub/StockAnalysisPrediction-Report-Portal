//go:build linux || darwin

package journal

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// A backend reply is carried base64 inside its result record, so a JSON body of a few hundred kilobytes (an app's
// dashboard summary) makes a record far over the 16 KiB that bounds the journal's small receipts. The record's own
// bound is MaxBackendRequestResultBytes; the canonical check must use it, or every such reply reads as uncertain.
func largeBackendBody(t *testing.T, size int) []byte {
	t.Helper()
	body := []byte(`{"rows":"` + strings.Repeat("x", size) + `"}`)
	if !json.Valid(body) {
		t.Fatal("fixture body is not JSON")
	}
	return body
}

func TestLargeBackendRepliesParseUpToTheResultBound(t *testing.T) {
	body := largeBackendBody(t, 200_000)
	response := driver.BackendResponse{Status: 200, Body: body}
	sha := func(c string) string { return strings.Repeat(c, 64) }

	writable, _ := json.Marshal(WritableBackendRequestResult{Format: writableBackendRequestResultFormat, RequestID: fixtureID(1910), RequestSHA256: sha("a"), DeliveryReceiptSHA256: sha("b"), NativeID: sha("c"), BootID: "boot-1", Response: response, ObservedMS: "1"})
	if len(writable) <= 16384 {
		t.Fatal("fixture record is not over the small-receipt bound")
	}
	if got, err := ParseWritableBackendRequestResult(writable); err != nil || !bytes.Equal(got.Response.Body, body) {
		t.Fatal("a 200 KB writable reply was refused", err)
	}
	readonly, _ := json.Marshal(BackendRequestResult{Format: "portal.runner.backend-request-result/1-dev.1", RequestID: fixtureID(1911), RequestSHA256: sha("a"), DeliveryReceiptSHA256: sha("b"), NativeID: sha("c"), BootID: "boot-1", Response: response, ObservedMS: "1"})
	if got, err := ParseBackendRequestResult(readonly); err != nil || !bytes.Equal(got.Response.Body, body) {
		t.Fatal("a 200 KB read-only reply was refused", err)
	}
	// Past the record's bound it is still refused, and a non-canonical record of any size is refused.
	huge, _ := json.Marshal(WritableBackendRequestResult{Format: writableBackendRequestResultFormat, RequestID: fixtureID(1910), RequestSHA256: sha("a"), DeliveryReceiptSHA256: sha("b"), NativeID: sha("c"), BootID: "boot-1", Response: driver.BackendResponse{Status: 200, Body: largeBackendBody(t, MaxBackendRequestResultBytes)}, ObservedMS: "1"})
	if _, err := ParseWritableBackendRequestResult(huge); err == nil {
		t.Fatal("a record over MaxBackendRequestResultBytes was accepted")
	}
	if _, err := ParseWritableBackendRequestResult(append(writable, ' ')); err == nil {
		t.Fatal("a non-canonical large record was accepted")
	}
}

// The small-receipt default still bounds callers that pass no limit of their own.
func TestCanonicalDataJSONKeepsItsDefaultBound(t *testing.T) {
	var v map[string]string
	raw := `{"a":"` + strings.Repeat("x", 16384) + `"}`
	if canonicalDataJSON(raw, &v) {
		t.Fatal("the default canonical check accepted more than 16 KiB")
	}
	if !canonicalDataJSONWithin(raw, len(raw), &v) {
		t.Fatal("an explicit bound did not admit a canonical record")
	}
}
