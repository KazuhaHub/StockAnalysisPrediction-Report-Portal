//go:build linux && (amd64 || arm64)

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

// An explicitly signed disposable controller request exercises the native
// transport. Its viewer is synthetic; this does not grant user access or claim
// that pending installations can enter the browser-facing request API.
func testNativeBackendRequestRelay(t *testing.T, endpoint platformRuntimeEndpoint, intent platformBackendDeliveryIntent, boot platformBackendBootObservation) {
	t.Helper()
	ctx := context.Background()
	signer, err := intent.registration.Signer(boot.bootID)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	controlKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	defer clear(controlKey)
	viewer := protocol.Viewer{Subject: strings.Repeat("a", 64), SessionRevision: 1, Role: "user", OUs: []string{strings.Repeat("b", 64)}}
	body := []byte(`{"role":"admin","value":"fixture"}`)
	assertion, err := signer.Sign("POST", "/v1/echo", body, viewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	prepare := func(method, path string, data []byte, token, bootID string) ([]byte, []byte) {
		t.Helper()
		id, err := platformNewUUID()
		if err != nil {
			t.Fatal(err)
		}
		c := control.BackendRequestCommand{Format: control.BackendRequestFormat, RequestID: id, RegistrationRaw: intent.raw, RegistrationSignature: intent.signature, BootID: bootID, Method: method, Path: path, BodySHA256: platformDigest(data), AssertionSHA256: platformDigest([]byte(token))}
		raw, sig, err := control.SignBackendRequest(c, controlKey, intent.candidate.authority, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		return raw, sig
	}
	send := func(raw, sig, data []byte, token string) (transport.BackendRequestObservation, error) {
		return transport.RequestControllerBackend(ctx, endpoint.path, endpoint.controllerUID, endpoint.controllerGID, raw, sig, intent.candidate.authority, intent.candidate.descriptor, data, token)
	}
	raw, sig := prepare("POST", "/v1/echo", body, assertion, boot.bootID)
	proof, err := send(raw, sig, body, assertion)
	receiptBytes, _ := json.Marshal(intent.receipt)
	if err != nil || !proof.Endpoint().RootController || proof.Endpoint().RunnerUID != 0 || proof.Result().NativeID != boot.nativeID || proof.Result().BootID != boot.bootID || proof.Result().DeliveryReceiptSHA256 != platformDigest(receiptBytes) || proof.CommandSHA256() != platformDigest(raw) || proof.Result().Response.Status != 200 {
		t.Fatal("actual backend request relay", err)
	}
	var echoed struct {
		Count  int64           `json:"count"`
		Viewer protocol.Viewer `json:"viewer"`
	}
	if json.Unmarshal(proof.Result().Response.Body, &echoed) != nil || echoed.Count != 1 || echoed.Viewer.Subject != viewer.Subject || echoed.Viewer.Role != "user" || len(echoed.Viewer.OUs) != 1 || echoed.Viewer.OUs[0] != viewer.OUs[0] {
		t.Fatal("viewer came from request body or binding changed")
	}
	replay, err := send(raw, sig, body, assertion)
	if err != nil || replay.Result().Response.Status != 403 {
		t.Fatal("same actual backend assertion replayed", err)
	}
	changedBody := []byte(`{"value":"tampered"}`)
	if proof, err := send(raw, sig, changedBody, assertion); err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("body changed under signed control request")
	}
	// Correct outer control signature cannot upgrade fabricated viewer claims into
	// a valid backend MAC. Keep a structurally coherent altered token with old MAC.
	encoded, mac, _ := strings.Cut(assertion, ".")
	claimsRaw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var claims protocol.Claims
	if json.Unmarshal(claimsRaw, &claims) != nil {
		t.Fatal("fixture assertion unavailable")
	}
	// Use an unseen nonce so refusal must not be satisfied by the replay
	// rejection already exercised above.
	claims.RequestID, err = platformNewUUID()
	if err != nil {
		t.Fatal(err)
	}
	claims.BodyDigest = platformDigest(changedBody)
	claims.Viewer.Role = "admin"
	claimsRaw, _ = json.Marshal(claims)
	forged := base64.RawURLEncoding.EncodeToString(claimsRaw) + "." + mac
	forgedRaw, forgedSig := prepare("POST", "/v1/echo", changedBody, forged, boot.bootID)
	rejected, err := send(forgedRaw, forgedSig, changedBody, forged)
	if err != nil || rejected.Result().Response.Status != 403 {
		t.Fatal("actual backend accepted fabricated viewer MAC", err)
	}
	stale := signer
	stale.BootID = "stale-boot"
	oldToken, err := stale.Sign("POST", "/v1/echo", body, viewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	oldRaw, oldSig := prepare("POST", "/v1/echo", body, oldToken, stale.BootID)
	rejected, err = send(oldRaw, oldSig, body, oldToken)
	if err != nil || rejected.Result().Response.Status != 403 {
		t.Fatal("actual backend accepted stale boot", err)
	}
	queryToken, err := signer.Sign("GET", "/v1/echo", nil, viewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	queryRaw, querySig := prepare("GET", "/v1/echo", nil, queryToken, boot.bootID)
	latest, err := send(queryRaw, querySig, nil, queryToken)
	if err != nil || latest.Result().Response.Status != 200 || json.Unmarshal(latest.Result().Response.Body, &echoed) != nil || echoed.Count != 1 {
		t.Fatal("rejected requests executed application writes", err)
	}
	copied := latest.Result()
	copied.Response.Body[0] ^= 1
	if bytes.Equal(copied.Response.Body, latest.Result().Response.Body) {
		t.Fatal("response getter exposed mutable body")
	}
	if _, err := json.Marshal(latest); err == nil {
		t.Fatal("native request proof serialized")
	}
	t.Log("MANAGED_BACKEND_REQUEST_RELAY_VERIFIED")
}
