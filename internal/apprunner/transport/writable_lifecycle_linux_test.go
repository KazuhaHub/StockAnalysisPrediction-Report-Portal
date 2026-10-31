//go:build linux

package transport

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func TestReadonlyEndpointRefusesAllWritableLifecycleActions(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	a, c, raw, sig, descriptor, _ := writableHandshakeRequestFixture(t)
	v, err := control.VerifyWritableBackendRegistration(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	start := v.Candidate().Command()
	for action := WritableWriterExecute; action <= WritableRegistrationRead; action++ {
		request, signature := raw, sig
		if action == WritableRegistrationDeliver {
			continue
		}
		if action <= WritableWriterReconcile {
			request, signature = start.WriterRaw, start.WriterSignature
		} else if action <= WritableCandidateProbe {
			request, signature = c.CandidateRaw, c.CandidateSignature
		}
		proof, err := writableLifecycleFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), action, request, signature, a, descriptor, nil)
		if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
			t.Fatal("readonly minted lifecycle", action, err)
		}
	}
	checkpointAuthority, _, checkpointRaw, checkpointSig, checkpointDescriptor, _ := checkpointTransportFixture(t)
	for _, action := range []WritableLifecycleAction{CheckpointCreate, CheckpointRead} {
		proof, err := writableLifecycleFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), action, checkpointRaw, checkpointSig, checkpointAuthority, checkpointDescriptor, nil)
		if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
			t.Fatal("readonly minted checkpoint lifecycle", action, err)
		}
	}
	restoreAuthority, _, restoreRaw, restoreSig, restoreDescriptor, _ := restoreTransportFixture(t)
	for _, action := range []WritableLifecycleAction{RestoreExecute, RestoreRead} {
		proof, err := writableLifecycleFrame(context.Background(), path, privateProfile(uint32(os.Geteuid())), action, restoreRaw, restoreSig, restoreAuthority, restoreDescriptor, nil)
		if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
			t.Fatal("readonly minted restore lifecycle", action, err)
		}
	}
}
func TestWritableLifecycleFramingAndHistoricalRead(t *testing.T) {
	for _, mode := range []string{"accepted", "nonce", "magic", "oversize", "truncated", "trailing", "effect", "payload", "source", "document", "future", "revision", "unknown", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, _, raw, sig, descriptor, r := writableHandshakeRequestFixture(t)
			dir, err := os.MkdirTemp("/tmp", "wlf-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "w.sock")
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
				if err != nil || len(request) < 45 || string(request[:8]) != writableLifecycleQueryMagic || WritableLifecycleAction(request[44]) != WritableRegistrationRead {
					return
				}
				// Mutations retain canonical structure but must not match the signed intent.
				switch mode {
				case "effect":
					r.Claim.EffectID = "11111111-1111-4111-8111-111111119990"
				case "payload":
					r.Claim.PayloadSHA256 = strings.Repeat("a", 64)
				case "source":
					r.Claim.SourceSHA256 = strings.Repeat("b", 64)
				case "document":
					r.Claim.DocumentSHA256 = strings.Repeat("c", 64)
				case "revision":
					r.Claim.ControlRevision = "999999"
				case "future":
					r.ObservedMS = "9999999999999"
					r.SettledMS = r.ObservedMS
				}
				marker, _ := json.Marshal(r.Claim)
				binding := sha256.Sum256(marker)
				r.BindingSHA256 = hex.EncodeToString(binding[:])
				body, _ := json.Marshal(r)
				if mode == "unknown" {
					body = append(body[:len(body)-1], []byte(`,"extra":true}`)...)
				}
				reply := make([]byte, 44)
				copy(reply, writableLifecycleReplyMagic)
				digest := sha256.Sum256(request)
				copy(reply[8:40], digest[:])
				binary.BigEndian.PutUint32(reply[40:], uint32(len(body)))
				switch mode {
				case "nonce":
					reply[8] ^= 1
				case "magic":
					reply[0] = 'X'
				case "oversize":
					binary.BigEndian.PutUint32(reply[40:], maxWritableLifecycleReply+1)
				case "truncated":
					body = body[:len(body)/2]
				case "trailing":
					body = append(body, 'x')
				}
				conn.Write(append(reply, body...))
			}()
			ctx, cancel := context.WithCancel(context.Background())
			if mode == "cancel" {
				cancel()
				listener.Close()
			}
			defer cancel()
			proof, err := writableLifecycleFrame(ctx, path, privateProfile(uint32(os.Geteuid())), WritableRegistrationRead, raw, sig, a, descriptor, nil)
			<-done
			if mode == "accepted" {
				if err != nil || proof.ReceivedAt().IsZero() || proof.Endpoint().RootController {
					t.Fatal("private frame", err)
				}
				if _, err := proof.RegistrationReceipt(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("invalid frame", mode, err)
			}
		})
	}
}
