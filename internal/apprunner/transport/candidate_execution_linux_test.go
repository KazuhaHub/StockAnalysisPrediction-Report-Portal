//go:build linux

package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

func TestReadOnlyEndpointCannotDispatchCandidate(t *testing.T) {
	backend := &recorder{}
	path, _ := fixture(t, uint32(os.Geteuid()), backend)
	authority, c, _, _ := candidateReceiptAuthority(t)
	descriptor, readErr := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	digest := sha256.Sum256(descriptor)
	c.Payload.ReleaseSHA256 = hex.EncodeToString(digest[:])
	c.PayloadSHA256, readErr = control.CandidateLogicalDigest(c)
	if readErr != nil {
		t.Fatal(readErr)
	}
	raw, sig, readErr := control.SignCandidate(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, time.Now().UnixMilli())
	if readErr != nil {
		t.Fatal(readErr)
	}
	proof, err := readCandidateResponseMode(context.Background(), path, privateProfile(uint32(os.Geteuid())), raw, sig, authority, descriptor, true)
	if err == nil || !proof.ReceivedAt().IsZero() || backend.calls.Load() != 0 {
		t.Fatal("invalid execution reached admission or minted proof")
	}
}
func TestCandidateControllerRequiresConfiguredNativeProvider(t *testing.T) {
	for _, native := range []*driver.CandidateRuntime{nil, {}} {
		path := t.TempDir() + "/control.sock"
		server, err := ListenCandidateController(context.Background(), path, 41001, 41001, nil, native)
		if err == nil {
			server.Close()
			t.Fatal("unconfigured candidate provider exposed listener")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("failed configuration created socket", err)
		}
		if err := native.CheckConfiguration(context.Background()); err == nil {
			t.Fatal("zero native runtime reports configured")
		}
	}
}
func TestCandidateDescriptorFramingRejectsOversizeAndStatic(t *testing.T) {
	_, _, raw, _ := candidateReceiptAuthority(t)
	for _, descriptor := range [][]byte{nil, []byte(`{}`), make([]byte, 131073)} {
		if validCandidateDescriptor(raw, descriptor) {
			t.Fatal("invalid descriptor admitted")
		}
	}
	if validCandidateDescriptor(make([]byte, control.MaxCommandBytes+1), []byte(`{}`)) {
		t.Fatal("invalid command/descriptor admitted")
	}
}
