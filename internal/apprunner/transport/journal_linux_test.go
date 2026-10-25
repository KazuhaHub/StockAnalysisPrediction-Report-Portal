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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func TestSocketToEnrolledJournal(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "runner-integrated-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	data := filepath.Join(dir, "journal")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Public development fixture only; never enroll this seed in a deployment.
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	pub := key.Public().(ed25519.PublicKey)
	pin := sha256.Sum256(pub)
	deployment := "00000000-0000-0000-0000-000000000400"
	epoch := "00000000-0000-0000-0000-000000000401"
	if _, _, err := store.EnrollController(ctx, journal.ControllerEnrollment{DeploymentID: deployment, ControllerEpoch: epoch, ControlRevision: 3, PublicKey: pub, Fingerprint: hex.EncodeToString(pin[:])}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.sock")
	server, err := Listen(path, uint32(os.Geteuid()), store)
	if err != nil {
		t.Fatal(err)
	}
	serving, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serving) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	now := time.Now().UnixMilli()
	command := control.Command{Format: control.Format, DeploymentID: deployment, ControllerEpoch: epoch, ControlRevision: "3", InstallationID: "00000000-0000-0000-0000-000000000410", OperationID: "00000000-0000-0000-0000-000000000411", TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: "00000000-0000-0000-0000-000000000412", Kind: "prepare-data", InstallationFence: "1", IssuedMS: strconv.FormatInt(now-1000, 10), ExpiresMS: strconv.FormatInt(now+29000, 10), Payload: control.PrepareDataPayload{DataNamespaceID: "00000000-0000-0000-0000-000000000413", ReleaseSHA256: strings.Repeat("a", 64), DataFormat: "1", QuotaBytes: "268435456", QuotaInodes: "65536", WriterFence: "1"}}
	command.PayloadSHA256, err = control.LogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	frame := append([]byte{}, []byte("portal.app/runner-command/v1\x00")...)
	length := make([]byte, 8)
	binary.BigEndian.PutUint64(length, uint64(len(raw)))
	frame = append(frame, length...)
	frame = append(frame, raw...)
	sig := ed25519.Sign(key, frame)
	invalid := command
	invalid.EffectID = "00000000-0000-0000-0000-000000000499"
	invalidRaw, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := Exchange(ctx, path, uint32(os.Geteuid()), invalidRaw, make([]byte, 64)); err == nil {
		t.Fatal("unsigned command accepted")
	}
	if err := Exchange(ctx, path, uint32(os.Geteuid()), raw, sig); err != nil {
		t.Fatal(err)
	}
	result, err := store.AdmitControlCommand(ctx, raw, sig)
	if err != nil || !result.Replayed || result.State != "prepared" || result.AdmittedFence != 1 {
		t.Fatalf("socket did not persist prepared identity: %+v %v", result, err)
	}
	// A duplicate is still just an intent acknowledgement, never permission to run.
	if err := Exchange(ctx, path, uint32(os.Geteuid()), raw, sig); err != nil {
		t.Fatal(err)
	}
}
