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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func candidateReadinessRequestFixture(t *testing.T) (control.Authority, control.CandidateCommand, []byte, []byte, []byte) {
	t.Helper()
	a, c, _, _ := candidateReceiptAuthority(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(descriptor)
	c.Payload.ReleaseSHA256 = hex.EncodeToString(digest[:])
	c.PayloadSHA256, err = control.CandidateLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignCandidate(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	return a, c, raw, sig, descriptor
}

func TestReadOnlyEndpointCannotProbeCandidate(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, _, raw, sig, descriptor := candidateReadinessRequestFixture(t)
	proof, err := probeCandidateReadinessFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor)
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("read-only listener minted readiness")
	}
}

func TestCandidateReadinessFramingAndPrivateProvenance(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "stale", "future", "payload", "schema", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, c, raw, sig, descriptor := candidateReadinessRequestFixture(t)
			dir, err := os.MkdirTemp("/tmp", "rrf-")
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
				request, err := io.ReadAll(io.LimitReader(conn, 150000))
				if err != nil || len(request) < 44 || string(request[:8]) != candidateReadinessQueryMagic {
					return
				}
				r := journal.CandidateReadiness{Format: "portal.runner.candidate-readiness/1-dev.1", PayloadSHA256: c.PayloadSHA256, RunningReceiptSHA256: strings.Repeat("a", 64), NativeID: strings.Repeat("b", 64), Protocol: "portal.backend/1", DataFormat: c.Payload.DataFormat, ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10)}
				switch mode {
				case "stale":
					r.ObservedMS = "0"
				case "future":
					r.ObservedMS = strconv.FormatInt(time.Now().UnixMilli()+10000, 10)
				case "payload":
					r.PayloadSHA256 = strings.Repeat("f", 64)
				case "schema":
					r.DataFormat = "123"
				}
				body, _ := json.Marshal(r)
				if mode == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"extra":true}`)...)
				}
				reply := make([]byte, 44)
				copy(reply, candidateReadinessReplyMagic)
				digest := sha256.Sum256(request)
				copy(reply[8:40], digest[:])
				binary.BigEndian.PutUint32(reply[40:], uint32(len(body)))
				switch mode {
				case "nonce":
					reply[8] ^= 1
				case "magic":
					reply[0] = 'X'
				case "oversize":
					binary.BigEndian.PutUint32(reply[40:], 4097)
				case "truncated":
					body = body[:len(body)/2]
				case "trailing":
					body = append(body, 'x')
				}
				conn.Write(append(reply, body...))
			}()
			proof, err := probeCandidateReadinessFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, a, descriptor)
			<-done
			if mode == "accepted" {
				if err != nil || proof.ReceivedAt().IsZero() || proof.Endpoint().RootController {
					t.Fatal("private frame proof", err)
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("invalid readiness framing accepted", mode)
			}
		})
	}
}
