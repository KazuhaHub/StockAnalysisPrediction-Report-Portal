//go:build linux

package transport

import (
	"bytes"
	"context"
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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func TestReadOnlyEndpointCannotHandshakeBackend(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, _, raw, sig, descriptor, _ := registrationRequestFixture(t)
	proof, err := handshakeBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, "a.b")
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("read-only endpoint minted handshake")
	}
}

// Mock same-UID frames exercise framing only. The application MAC and actual
// root/container boundary are independently required by native acceptance.
func TestBackendHandshakeFrameAndFreshness(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "stale", "future", "payload", "challenge", "answer", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, c, raw, sig, descriptor, document := registrationRequestFixture(t)
			dir, err := os.MkdirTemp("/tmp", "bhf-")
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
				if err != nil || len(request) < 44 || string(request[:8]) != backendHandshakeQueryMagic {
					return
				}
				if bytes.Contains(request, document) {
					t.Error("handshake retransmitted private registration")
				}
				sum := sha256.Sum256([]byte("a.b"))
				e := journal.BackendHandshakeEvidence{Format: "portal.runner.backend-handshake/1-dev.1", PayloadSHA256: c.PayloadSHA256, DeliveryReceiptSHA256: strings.Repeat("c", 64), NativeID: strings.Repeat("b", 64), ChallengeSHA256: hex.EncodeToString(sum[:]), Answer: "c.d", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
				switch mode {
				case "stale":
					e.ObservedMS = "0"
				case "future":
					e.ObservedMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
				case "payload":
					e.PayloadSHA256 = strings.Repeat("f", 64)
				case "challenge":
					e.ChallengeSHA256 = strings.Repeat("f", 64)
				case "answer":
					e.Answer = "invalid"
				}
				body, _ := json.Marshal(e)
				if mode == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"extra":true}`)...)
				}
				header := make([]byte, 44)
				copy(header, backendHandshakeReplyMagic)
				digest := sha256.Sum256(request)
				copy(header[8:40], digest[:])
				binary.BigEndian.PutUint32(header[40:44], uint32(len(body)))
				switch mode {
				case "nonce":
					header[8] ^= 1
				case "magic":
					header[0] = 'X'
				case "oversize":
					binary.BigEndian.PutUint32(header[40:44], 8193)
				case "truncated":
					body = body[:len(body)/2]
				case "trailing":
					body = append(body, 'x')
				}
				conn.Write(append(header, body...))
			}()
			proof, err := handshakeBackendFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor, "a.b")
			<-done
			if mode == "accepted" {
				if err != nil || proof.ReceivedAt().IsZero() || proof.Endpoint().RootController {
					t.Fatal("private frame", err)
				}
				if _, err := json.Marshal(proof); err == nil {
					t.Fatal("opaque handshake serialized")
				}
				copied := proof.Controller()
				copied.PublicKey[0] ^= 1
				if bytes.Equal(copied.PublicKey, proof.Controller().PublicKey) {
					t.Fatal("authority getter aliased key")
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("unsafe handshake frame accepted", mode)
			}
		})
	}
}
