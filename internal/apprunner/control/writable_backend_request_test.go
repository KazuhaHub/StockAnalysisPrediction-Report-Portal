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

func writableBackendRequestFixture(t *testing.T) (WritableBackendRequestCommand, ed25519.PrivateKey, Authority, int64, []byte, string) {
	t.Helper()
	reg, key, a, now := writableRegistrationFixture(t)
	v, err := VerifyWritableCandidate(reg.CandidateRaw, reg.CandidateSignature, a, now)
	if err != nil {
		t.Fatal(err)
	}
	index := v.Index()
	material, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: "notes-demo", DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: index.Payload.ReleaseSHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
	doc, err := material.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(doc)
	digest := sha256.Sum256(doc)
	reg.DocumentSHA256 = hex.EncodeToString(digest[:])
	reg.PayloadSHA256, err = WritableBackendRegistrationLogicalDigest(reg)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := SignWritableBackendRegistration(reg, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
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
	c := WritableBackendRequestCommand{Format: WritableBackendRequestFormat, RequestID: "90000000-0000-4000-8000-000000000001", RegistrationRaw: raw, RegistrationSignature: sig, BootID: "boot-1", Method: "POST", Path: "/v1/echo", BodySHA256: hex.EncodeToString(bodySHA[:]), AssertionSHA256: hex.EncodeToString(assertionSHA[:])}
	return c, key, a, now, body, assertion
}
func TestWritableBackendRequestSignaturesAndBindings(t *testing.T) {
	c, key, a, now, body, assertion := writableBackendRequestFixture(t)
	raw, sig, err := SignWritableBackendRequest(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyWritableBackendRequest(raw, sig, a, now)
	if err != nil || ValidateWritableBackendRequestData(v, body, assertion) != nil {
		t.Fatal("valid request", err)
	}
	if _, err := VerifyBackendRequest(raw, sig, a, now); err == nil {
		t.Fatal("writable request crossed readonly domain")
	}
	if _, err := VerifyBackendRegistration(raw, sig, a, now); err == nil {
		t.Fatal("request crossed registration domain")
	}
	wrong := ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, raw))
	if _, err := VerifyWritableBackendRequest(raw, wrong, a, now); err == nil {
		t.Fatal("registration signature authorized request")
	}
	if ValidateWritableBackendRequestData(v, []byte(`{"changed":true}`), assertion) == nil || ValidateWritableBackendRequestData(v, body, assertion+"x") == nil || ValidateWritableBackendRequestData(VerifiedWritableBackendRequest{}, body, assertion) == nil {
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
			r, s, err := SignWritableBackendRequest(changed, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := VerifyWritableBackendRequest(r, s, a, now)
			if err != nil || ValidateWritableBackendRequestData(parsed, body, assertion) == nil {
				t.Fatal("assertion/control divergence accepted", mode, err)
			}
		})
	}
}
func TestWritableBackendRequestRejectsMalformedSignedCommands(t *testing.T) {
	for _, mode := range []string{"format", "id", "zero", "boot", "method", "reserved", "digest", "nested", "expired", "unknown", "duplicate", "trailing", "oversize", "signature"} {
		t.Run(mode, func(t *testing.T) {
			c, key, a, now, _, _ := writableBackendRequestFixture(t)
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
			sig := ed25519.Sign(key, frame(writableBackendRequestSignaturePurpose, raw))
			if mode == "signature" {
				sig[0] ^= 1
			}
			if _, err := VerifyWritableBackendRequest(raw, sig, a, now); err == nil {
				t.Fatal("invalid signed request accepted", mode)
			}
		})
	}
}
