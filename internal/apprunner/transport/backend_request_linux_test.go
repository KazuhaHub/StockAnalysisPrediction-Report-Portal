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

func backendRequestFrameFixture(t *testing.T) (control.Authority, control.BackendRequestCommand, []byte, []byte, []byte, []byte, string) {
	t.Helper()
	a, _, registrationRaw, registrationSig, descriptor, document := registrationRequestFixture(t)
	material, err := appplatform.ParseBackendRegistration(document)
	if err != nil {
		t.Fatal(err)
	}
	defer material.Close()
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
	c := control.BackendRequestCommand{Format: control.BackendRequestFormat, RequestID: "11111111-1111-4111-8111-111111119911", RegistrationRaw: registrationRaw, RegistrationSignature: registrationSig, BootID: "boot-test", Method: "POST", Path: "/v1/echo", BodySHA256: hex.EncodeToString(bodySHA[:]), AssertionSHA256: hex.EncodeToString(tokenSHA[:])}
	raw, sig, err := control.SignBackendRequest(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return a, c, raw, sig, descriptor, body, token
}
func TestReadOnlyEndpointCannotRelayBackendRequest(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, _, raw, sig, descriptor, body, token := backendRequestFrameFixture(t)
	proof, err := requestBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, body, token)
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("read-only endpoint minted response")
	}
}
func TestBackendRequestFramingAndProvenance(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "stale", "future", "request_id", "request_hash", "boot", "status", "body", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, c, raw, sig, descriptor, body, token := backendRequestFrameFixture(t)
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
				if err != nil || len(request) < 44 || string(request[:8]) != backendRequestQueryMagic {
					return
				}
				sum := sha256.Sum256(raw)
				r := journal.BackendRequestResult{Format: "portal.runner.backend-request-result/1-dev.1", RequestID: c.RequestID, RequestSHA256: hex.EncodeToString(sum[:]), DeliveryReceiptSHA256: strings.Repeat("c", 64), NativeID: strings.Repeat("b", 64), BootID: c.BootID, Response: driver.BackendResponse{Status: 200, Body: []byte(`{"ok":true}`)}, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
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
				copy(header, backendRequestReplyMagic)
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
			proof, err := requestBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, body, token)
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
