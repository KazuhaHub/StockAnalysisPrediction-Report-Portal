package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func backendRequestFixture(t *testing.T) (BackendRequestCommand, ed25519.PrivateKey, Authority, int64, []byte, string) {
	t.Helper()
	registration, key, a, now, _, document := backendRegistrationFixture(t)
	defer clear(document)
	raw, sig, err := SignBackendRegistration(registration, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	material, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	signer, err := material.Signer("boot-1")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	body := []byte(`{"value":"fixture"}`)
	assertion, err := signer.Sign("POST", "/v1/echo", body, protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"}, time.UnixMilli(now))
	if err != nil {
		t.Fatal(err)
	}
	bodySHA, assertionSHA := sha256.Sum256(body), sha256.Sum256([]byte(assertion))
	c := BackendRequestCommand{Format: BackendRequestFormat, RequestID: "90000000-0000-4000-8000-000000000001", RegistrationRaw: raw, RegistrationSignature: sig, BootID: "boot-1", Method: "POST", Path: "/v1/echo", BodySHA256: hex.EncodeToString(bodySHA[:]), AssertionSHA256: hex.EncodeToString(assertionSHA[:])}
	return c, key, a, now, body, assertion
}
func TestBackendRequestSignaturesAndBindings(t *testing.T) {
	c, key, a, now, body, assertion := backendRequestFixture(t)
	raw, sig, err := SignBackendRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyBackendRequest(raw, sig, a, now)
	if err != nil || ValidateBackendRequestData(v, body, assertion) != nil {
		t.Fatal("valid request", err)
	}
	if _, err := VerifyBackendRegistration(raw, sig, a, now); err == nil {
		t.Fatal("request crossed registration domain")
	}
	wrong := ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, raw))
	if _, err := VerifyBackendRequest(raw, wrong, a, now); err == nil {
		t.Fatal("registration signature authorized request")
	}
	if ValidateBackendRequestData(v, []byte(`{"changed":true}`), assertion) == nil || ValidateBackendRequestData(v, body, assertion+"x") == nil || ValidateBackendRequestData(VerifiedBackendRequest{}, body, assertion) == nil {
		t.Fatal("changed data accepted")
	}
	copied := v.Command()
	copied.RegistrationRaw[0] ^= 1
	copied.RegistrationSignature[0] ^= 1
	if bytes.Equal(copied.RegistrationRaw, v.Command().RegistrationRaw) || bytes.Equal(copied.RegistrationSignature, v.Command().RegistrationSignature) {
		t.Fatal("getter aliased bytes")
	}
	for _, mode := range []string{"boot", "method", "path", "body"} {
		t.Run(mode, func(t *testing.T) {
			changed := c
			switch mode {
			case "boot":
				changed.BootID = "other"
			case "method":
				changed.Method = "PUT"
			case "path":
				changed.Path = "/v1/other"
			case "body":
				changed.BodySHA256 = strings.Repeat("f", 64)
			}
			r, s, err := SignBackendRequest(changed, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := VerifyBackendRequest(r, s, a, now)
			if err != nil || ValidateBackendRequestData(parsed, body, assertion) == nil {
				t.Fatal("assertion/control divergence accepted", mode, err)
			}
		})
	}
}
func TestBackendRequestRejectsMalformedSignedCommands(t *testing.T) {
	for _, mode := range []string{"format", "id", "zero", "boot", "method", "reserved", "digest", "nested", "expired", "unknown", "duplicate", "trailing", "oversize", "signature"} {
		t.Run(mode, func(t *testing.T) {
			c, key, a, now, _, _ := backendRequestFixture(t)
			switch mode {
			case "format":
				c.Format = BackendRegistrationFormat
			case "id":
				c.RequestID = "bad"
			case "zero":
				c.RequestID = "00000000-0000-0000-0000-000000000000"
			case "boot":
				c.BootID = ""
			case "method":
				c.Method = "PATCH"
			case "reserved":
				c.Path = "/v1/%2568andshake"
			case "digest":
				c.BodySHA256 = "bad"
			case "nested":
				c.RegistrationSignature = bytes.Clone(c.RegistrationSignature)
				c.RegistrationSignature[0] ^= 1
			case "expired":
				now += 30000
			}
			raw, _ := json.Marshal(c)
			switch mode {
			case "unknown":
				raw = append(raw[:len(raw)-1], []byte(`,"extra":true}`)...)
			case "duplicate":
				raw = append(raw[:len(raw)-1], []byte(`,"method":"POST"}`)...)
			case "trailing":
				raw = append(raw, []byte(" {}")...)
			case "oversize":
				raw = make([]byte, MaxCommandBytes+1)
			}
			sig := ed25519.Sign(key, frame(backendRequestSignaturePurpose, raw))
			if mode == "signature" {
				sig[0] ^= 1
			}
			if _, err := VerifyBackendRequest(raw, sig, a, now); err == nil {
				t.Fatal("invalid signed request accepted", mode)
			}
		})
	}
}
