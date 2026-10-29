//go:build linux && (amd64 || arm64)

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func writableNativeRequestSender(t *testing.T, s *Store, native *driver.CandidateRuntime, raw, sig, descriptor []byte, registration appplatform.BackendRegistration, boot string) func(string) (WritableBackendRequestResult, error) {
	t.Helper()
	return func(method string) (WritableBackendRequestResult, error) {
		t.Helper()
		ctx := context.Background()
		signer, err := registration.Signer(boot)
		if err != nil {
			t.Fatal(err)
		}
		defer clear(signer.Key)
		var body []byte
		if method == "POST" {
			body = []byte(`{"role":"admin"}`)
		}
		token, err := signer.Sign(method, "/v1/echo", body, protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.ControllerDeployment(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a := control.Authority{DeploymentID: d.DeploymentID, ControllerEpoch: d.ControllerEpoch, ControlRevision: d.ControlRevision, PublicKey: d.PublicKey, Fingerprint: d.Fingerprint}
		id, err := dataClaimID()
		if err != nil {
			t.Fatal(err)
		}
		c := control.WritableBackendRequestCommand{Format: control.WritableBackendRequestFormat, RequestID: id, RegistrationRaw: raw, RegistrationSignature: sig, BootID: boot, Method: method, Path: "/v1/echo", BodySHA256: candidateHash(body), AssertionSHA256: candidateHash([]byte(token))}
		key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
		defer clear(key)
		request, signature, err := control.SignWritableBackendRequest(c, key, a, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		return s.ForwardWritableBackendRequest(ctx, request, signature, descriptor, body, token, native)
	}
}
func assertWritableNativeRequestResult(t *testing.T, send func(string) (WritableBackendRequestResult, error), receipt WritableBackendRegistrationReceipt) {
	t.Helper()
	result, err := send("GET")
	if err != nil {
		t.Fatal("writable request after restoring original file", err)
	}
	var echo struct {
		Count  int64           `json:"count"`
		Viewer protocol.Viewer `json:"viewer"`
	}
	if result.Response.Status != 200 || result.NativeID != receipt.Claim.NativeID || json.Unmarshal(result.Response.Body, &echo) != nil || echo.Count != 0 {
		t.Fatal("replacement file request executed a write")
	}
	result, err = send("POST")
	if err != nil || result.Response.Status != 200 || json.Unmarshal(result.Response.Body, &echo) != nil || echo.Count != 1 || echo.Viewer.Role != "user" {
		t.Fatal("actual original writable request", err)
	}
	if receipt.Format == writableRegistrationRecoveredFormat {
		t.Log("NATIVE_WRITABLE_RECOVERED_REQUEST_VERIFIED")
	} else {
		t.Log("NATIVE_WRITABLE_REQUEST_VERIFIED")
	}
}
