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
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type receiptFixtureBackend struct {
	recorder
	receipt journal.DataPreparationReceipt
	reads   atomic.Int32
}

func (r *receiptFixtureBackend) ReadDataPreparationReceipt(context.Context, []byte, []byte) (journal.DataPreparationReceipt, error) {
	r.reads.Add(1)
	return r.receipt, nil
}
func receiptAuthority(t *testing.T) (control.Authority, control.Command, []byte, []byte) {
	t.Helper()
	e, c, raw, sig := signedIntentFixture(t)
	return control.Authority{DeploymentID: e.DeploymentID, ControllerEpoch: e.ControllerEpoch, ControlRevision: e.ControlRevision, PublicKey: e.PublicKey, Fingerprint: e.Fingerprint}, c, raw, sig
}
func TestReceiptChannelPrivateProof(t *testing.T) {
	// This fixed public vector tests framing/provenance, not native execution.
	authority, c, raw, sig := receiptAuthority(t)
	backend := &receiptFixtureBackend{receipt: receiptVector(t)}
	if !backend.receipt.MatchesCommand(c) {
		t.Fatal("shared logical/ownership vector changed")
	}
	dir, err := os.MkdirTemp("/tmp", "receipt-channel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "receipt.sock")
	server, err := Listen(path, uint32(os.Geteuid()), backend)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	proof, err := ReadDataReceipt(ctx, path, uint32(os.Geteuid()), raw, sig, authority)
	if err != nil || proof.Receipt() != backend.receipt || proof.ReceivedAt().IsZero() || time.Since(proof.ReceivedAt()) > MaxDataReceiptAge {
		t.Fatal("authenticated receipt missing", err)
	}
	digest := sha256.Sum256(raw)
	if proof.CommandSHA256() != hex.EncodeToString(digest[:]) || len(proof.RequestSHA256()) != 64 {
		t.Fatal("request binding missing")
	}
	again, err := ReadDataReceipt(ctx, path, uint32(os.Geteuid()), raw, sig, authority)
	if err != nil || again.RequestSHA256() == proof.RequestSHA256() || again.CommandSHA256() != proof.CommandSHA256() {
		t.Fatal("fresh nonce missing", err)
	}
	if backend.reads.Load() != 2 || proof.Endpoint().RootController || proof.Endpoint().Path != path || proof.Endpoint().RunnerUID != uint32(os.Geteuid()) {
		t.Fatal("private profile masqueraded as controller proof")
	}
	copy := proof.Controller()
	copy.PublicKey[0] ^= 1
	if proof.Controller().PublicKey[0] != authority.PublicKey[0] {
		t.Fatal("controller public key aliases caller")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("proof serialized as bearer authority")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || !proof.ReceivedAt().IsZero() {
		t.Fatal("JSON retained or minted proof")
	}
}
func TestReceiptChannelRejectsMalformedOrCrossCommandReply(t *testing.T) {
	for _, mode := range []string{"nonce", "oversize", "truncated", "trailing", "invalid", "cross_command", "wrong_magic", "admission_ack", "future_observation"} {
		t.Run(mode, func(t *testing.T) {
			authority, _, raw, sig := receiptAuthority(t)
			dir, err := os.MkdirTemp("/tmp", "receipt-bad-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "receipt.sock")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			receipt := receiptVector(t)
			if mode == "cross_command" {
				receipt.InstallationID = "00000000-0000-0000-0000-000000000499"
			}
			if mode == "future_observation" {
				now := time.Now().UnixMilli()
				receipt.Claim.ClaimedMS = strconv.FormatInt(now+10000, 10)
				receipt.Claim.ExpiresMS = strconv.FormatInt(now+11000, 10)
				receipt.ObservedMS = strconv.FormatInt(now+10500, 10)
			}
			body, _ := json.Marshal(receipt)
			if mode == "invalid" {
				body = []byte(`{}`)
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
				frame, err := io.ReadAll(io.LimitReader(conn, control.MaxCommandBytes+109))
				if err != nil {
					return
				}
				header := make([]byte, 44)
				copy(header, dataReceiptReplyMagic)
				digest := sha256.Sum256(frame)
				copy(header[8:40], digest[:])
				binary.BigEndian.PutUint32(header[40:44], uint32(len(body)))
				switch mode {
				case "nonce":
					header[8] ^= 1
				case "oversize":
					binary.BigEndian.PutUint32(header[40:44], maxDataReceiptBytes+1)
				case "wrong_magic":
					header[0] ^= 1
				case "truncated":
					body = body[:len(body)-1]
				case "trailing":
					body = append(body, 0)
				}
				if mode == "admission_ack" {
					conn.Write(append(append([]byte(replyMagic), digest[:]...), 1))
					return
				}
				conn.Write(append(header, body...))
			}()
			proof, err := ReadDataReceipt(context.Background(), path, uint32(os.Geteuid()), raw, sig, authority)
			if err == nil || !proof.ReceivedAt().IsZero() {
				t.Fatal("bad reply minted proof", mode, err)
			}
			<-done
		})
	}
}
