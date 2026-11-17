package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func candidateReceiptVector(t *testing.T) journal.CandidateRunningReceipt {
	t.Helper()
	raw, err := os.ReadFile("testdata/candidate-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := journal.ParseCandidateRunningReceipt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func candidateReceiptAuthority(t *testing.T) (control.Authority, control.CandidateCommand, []byte, []byte) {
	t.Helper()
	r := candidateReceiptVector(t)
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	public := key.Public().(ed25519.PublicKey)
	pin := sha256.Sum256(public)
	a := control.Authority{DeploymentID: r.DeploymentID, ControllerEpoch: r.ControllerEpoch, ControlRevision: 3, PublicKey: public, Fingerprint: hex.EncodeToString(pin[:])}
	now := time.Now().UnixMilli()
	c := control.CandidateCommand{Format: control.CandidateFormat, DeploymentID: r.DeploymentID, ControllerEpoch: r.ControllerEpoch, ControlRevision: r.ControlRevision, InstallationID: r.InstallationID, OperationID: r.OperationID, TargetGeneration: r.Generation, Stage: "start", EffectNumber: "1", EffectID: r.Claim.EffectID, Kind: "start-candidate", InstallationFence: r.Claim.AdmittedFence, IssuedMS: strconv.FormatInt(now-1000, 10), ExpiresMS: strconv.FormatInt(now+29000, 10), PayloadSHA256: r.PayloadSHA256, Payload: control.CandidatePayload{ResourceID: r.Claim.ResourceID, DataNamespaceID: r.DataNamespaceID, ReleaseSHA256: r.ReleaseSHA256, Platform: r.Claim.Preflight.Platform, DataFormat: r.DataFormat, WriterFence: r.WriterFence, SandboxProfile: control.CandidateSandboxProfile}}
	raw, sig, err := control.SignCandidate(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	return a, c, raw, sig
}
func equalCandidateMetadata(a, b journal.CandidateRunningReceipt) bool {
	first, _ := json.Marshal(a)
	second, _ := json.Marshal(b)
	return bytes.Equal(first, second)
}
func TestCandidateReceiptVectorAndOpaqueCopyBoundaries(t *testing.T) {
	r := candidateReceiptVector(t)
	_, c, _, _ := candidateReceiptAuthority(t)
	if !r.MatchesCommand(c) {
		t.Fatal("public candidate logical/ownership vector mismatch")
	}
	encoded, _ := json.Marshal(r)
	proof := CandidateReceiptObservation{receiptJSON: string(encoded), receivedAt: time.Now()}
	copy := proof.Receipt()
	copy.Claim.Preflight.NamespaceInode = "99"
	copy.NativeID = "bad"
	if !equalCandidateMetadata(proof.Receipt(), r) {
		t.Fatal("nested receipt pointer aliases opaque observation")
	}
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("observation serialized as authority")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || !proof.ReceivedAt().IsZero() || proof.Receipt().Format != "" {
		t.Fatal("JSON retained or minted observation")
	}
}
