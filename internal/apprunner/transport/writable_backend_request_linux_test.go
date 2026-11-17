//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	protocol "github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal-Apps/packages/extensionprotocol"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Canonical simulated metadata validates framing only, not a native file/process.
func writableBackendRequestFrameFixture(t *testing.T) (control.Authority, control.WritableBackendRequestCommand, []byte, []byte, []byte, journal.WritableBackendRegistrationReceipt, []byte, string) {
	t.Helper()
	a, reg, _, _, descriptor, expected := writableHandshakeRequestFixture(t)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	v, err := control.VerifyWritableCandidate(reg.CandidateRaw, reg.CandidateSignature, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	index := v.Index()
	material, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, InstallationID: index.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: parsed.SHA256}, bytes.Repeat([]byte{29}, 32))
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
	reg.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(reg)
	if err != nil {
		t.Fatal(err)
	}
	registrationRaw, registrationSig, err := control.SignWritableBackendRegistration(reg, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	expected.Claim.DocumentSHA256, expected.Claim.PayloadSHA256 = reg.DocumentSHA256, reg.PayloadSHA256
	claim, _ := json.Marshal(expected.Claim)
	binding := sha256.Sum256(claim)
	expected.BindingSHA256 = hex.EncodeToString(binding[:])
	signer, err := material.Signer("boot-test")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(signer.Key)
	body := []byte(`{"value":1}`)
	token, err := signer.Sign("POST", "/v1/echo", body, protocol.Viewer{Subject: strings.Repeat("a", 64), Role: "user"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	bodySHA, tokenSHA := sha256.Sum256(body), sha256.Sum256([]byte(token))
	c := control.WritableBackendRequestCommand{Format: control.WritableBackendRequestFormat, RequestID: "11111111-1111-4111-8111-111111119911", RegistrationRaw: registrationRaw, RegistrationSignature: registrationSig, BootID: "boot-test", Method: "POST", Path: "/v1/echo", BodySHA256: hex.EncodeToString(bodySHA[:]), AssertionSHA256: hex.EncodeToString(tokenSHA[:])}
	raw, sig, err := control.SignWritableBackendRequest(c, key, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return a, c, raw, sig, descriptor, expected, body, token
}
func TestReadOnlyEndpointCannotRelayWritableBackendRequest(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, _, raw, sig, descriptor, expected, body, token := writableBackendRequestFrameFixture(t)
	proof, err := requestWritableBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, expected, body, token)
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("read-only endpoint minted response")
	}
}
func TestWritableBackendRequestFramingAndProvenance(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "stale", "future", "request_id", "request_hash", "boot", "delivery", "native", "readonly", "status", "body", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, c, raw, sig, descriptor, expected, body, token := writableBackendRequestFrameFixture(t)
			dir, err := os.MkdirTemp("/tmp", "breq-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "runner.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.AcceptUnix()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				request, err := io.ReadAll(io.LimitReader(conn, 250000))
				defer clear(request)
				if err != nil || len(request) < 44 || string(request[:8]) != writableBackendRequestQueryMagic {
					return
				}
				sum := sha256.Sum256(raw)
				r := journal.WritableBackendRequestResult{Format: "portal.runner.writable-backend-request-result/1-dev.1", RequestID: c.RequestID, RequestSHA256: hex.EncodeToString(sum[:]), DeliveryReceiptSHA256: requestFixtureDigest(expected), NativeID: expected.Claim.NativeID, BootID: c.BootID, Response: driver.BackendResponse{Status: 200, Body: []byte(`{"ok":true}`)}, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
				switch mode {
				case "stale":
					r.ObservedMS = "0"
				case "future":
					r.ObservedMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
				case "request_id":
					r.RequestID = "11111111-1111-4111-8111-111111119912"
				case "request_hash":
					r.RequestSHA256 = strings.Repeat("f", 64)
				case "boot":
					r.BootID = "other"
				case "delivery":
					r.DeliveryReceiptSHA256 = strings.Repeat("f", 64)
				case "native":
					r.NativeID = strings.Repeat("f", 64)
				case "readonly":
					r.Format = "portal.runner.backend-request-result/1-dev.1"
				case "status":
					r.Response.Status = 302
				case "body":
					r.Response.Body = []byte("<html>")
				}
				encoded, _ := json.Marshal(r)
				if mode == "unknown" {
					encoded = append(encoded[:len(encoded)-1], []byte(`,"extra":true}`)...)
				}
				header := make([]byte, 44)
				copy(header, writableBackendRequestReplyMagic)
				digest := sha256.Sum256(request)
				copy(header[8:40], digest[:])
				binary.BigEndian.PutUint32(header[40:44], uint32(len(encoded)))
				switch mode {
				case "nonce":
					header[8] ^= 1
				case "magic":
					header[0] = 'X'
				case "oversize":
					binary.BigEndian.PutUint32(header[40:44], journal.MaxBackendRequestResultBytes+1)
				case "truncated":
					encoded = encoded[:len(encoded)/2]
				case "trailing":
					encoded = append(encoded, 'x')
				}
				conn.Write(append(header, encoded...))
			}()
			proof, err := requestWritableBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, expected, body, token)
			<-done
			if mode == "accepted" {
				if err != nil || proof.ReceivedAt().IsZero() || proof.Endpoint().RootController {
					t.Fatal("private response", err)
				}
				copied := proof.Result()
				copied.Response.Body[0] ^= 1
				if bytes.Equal(copied.Response.Body, proof.Result().Response.Body) {
					t.Fatal("mutable response getter")
				}
				authority := proof.Controller()
				authority.PublicKey[0] ^= 1
				if bytes.Equal(authority.PublicKey, proof.Controller().PublicKey) {
					t.Fatal("mutable authority getter")
				}
				if _, err := json.Marshal(proof); err == nil {
					t.Fatal("response serialized into provenance")
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("unsafe response accepted", mode)
			}
		})
	}
}

func requestFixtureDigest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
