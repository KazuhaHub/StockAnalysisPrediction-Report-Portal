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

func updateStartHandshakeRequestFixture(t *testing.T) (control.Authority, control.UpdateStartCommand, []byte, []byte, []byte, journal.UpdateStartRegistrationReceipt, string) {
	t.Helper()
	authority, start, startRaw, startSignature, descriptor, _ :=
		updateStartTransportFixture(t)
	now := time.Now()
	registration := control.UpdateStartRegistrationCommand{
		Format:   control.UpdateStartRegistrationFormat,
		EffectID: "11111111-1111-4111-8111-111111119980",
		StartRaw: startRaw, StartSignature: startSignature,
		RunningReceiptSHA256: strings.Repeat("a", 64),
		DocumentSHA256:       strings.Repeat("b", 64),
	}
	registration.PayloadSHA256, _ =
		control.UpdateStartRegistrationLogicalDigest(registration)
	raw, signature, err := control.SignUpdateStartRegistration(
		registration,
		ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)),
		authority, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var receipt journal.UpdateStartRegistrationReceipt
	if err := json.Unmarshal([]byte(`{"claim":{}}`), &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Format = "portal.runner.update-start-registration-delivery/1-dev.1"
	receipt.Claim.Format = "portal.runner.update-start-registration-claim/1-dev.1"
	receipt.Claim.ClaimID = "11111111-1111-4111-8111-111111119981"
	receipt.Claim.EffectID = registration.EffectID
	receipt.Claim.PayloadSHA256 = registration.PayloadSHA256
	receipt.Claim.SourceSHA256 = registration.RunningReceiptSHA256
	receipt.Claim.DocumentSHA256 = registration.DocumentSHA256
	receipt.Claim.NativeID = strings.Repeat("c", 64)
	receipt.Claim.ControlRevision = start.ControlRevision
	receipt.Claim.ClaimedMS = strconv.FormatInt(now.UnixMilli(), 10)
	receipt.Claim.ExpiresMS = start.ExpiresMS
	claimRaw, _ := json.Marshal(receipt.Claim)
	claimSHA := sha256.Sum256(claimRaw)
	receipt.BindingSHA256 = hex.EncodeToString(claimSHA[:])
	receipt.Device = "9"
	receipt.Inode = "10"
	receipt.ObservedMS = strconv.FormatInt(now.UnixMilli(), 10)
	receipt.SettledMS = receipt.ObservedMS
	return authority, start, raw, signature, descriptor, receipt, "a.b"
}

func TestUpdateStartBackendHandshakeFrame(t *testing.T) {
	authority, start, raw, signature, descriptor, expected, challenge :=
		updateStartHandshakeRequestFixture(t)
	dir, err := os.MkdirTemp("/tmp", "ushf-")
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
		if err != nil || len(request) < 44 ||
			string(request[:8]) != updateStartBackendHandshakeQueryMagic {
			return
		}
		expectedRaw, _ := json.Marshal(expected)
		expectedSHA := sha256.Sum256(expectedRaw)
		challengeSHA := sha256.Sum256([]byte(challenge))
		evidence := journal.UpdateStartBackendHandshakeEvidence{
			Format:                "portal.runner.update-start-backend-handshake/1-dev.1",
			PayloadSHA256:         start.PayloadSHA256,
			DeliveryReceiptSHA256: hex.EncodeToString(expectedSHA[:]),
			NativeID:              expected.Claim.NativeID,
			ChallengeSHA256:       hex.EncodeToString(challengeSHA[:]),
			Answer:                "c.d",
			ObservedMS:            strconv.FormatInt(time.Now().UnixMilli(), 10),
		}
		body, _ := json.Marshal(evidence)
		header := make([]byte, 44)
		copy(header, updateStartBackendHandshakeReplyMagic)
		digest := sha256.Sum256(request)
		copy(header[8:40], digest[:])
		binary.BigEndian.PutUint32(header[40:44], uint32(len(body)))
		conn.Write(append(header, body...))
	}()
	proof, err := handshakeUpdateStartBackendFrame(
		context.Background(), path, privateProfile(uint32(os.Geteuid())),
		raw, signature, authority, descriptor, expected, challenge)
	<-done
	if err != nil || proof.ReceivedAt().IsZero() ||
		proof.Evidence().PayloadSHA256 != start.PayloadSHA256 {
		t.Fatal("update-start handshake frame", proof, err)
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("opaque update-start handshake serialized")
	}
}
