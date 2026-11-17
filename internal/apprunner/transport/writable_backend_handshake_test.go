package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func writableHandshakeRequestFixture(t *testing.T) (control.Authority, control.WritableBackendRegistrationCommand, []byte, []byte, []byte, journal.WritableBackendRegistrationReceipt) {
	t.Helper()
	a, c, _, _ := candidateReceiptAuthority(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(descriptor)
	c.Payload.ReleaseSHA256 = hex.EncodeToString(digest[:])
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
	now := time.Now().UnixMilli()
	prep := control.Command{Format: control.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch, ControlRevision: c.ControlRevision, InstallationID: c.InstallationID, OperationID: c.OperationID, TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: c.EffectID, Kind: "prepare-data", InstallationFence: c.InstallationFence, IssuedMS: c.IssuedMS, ExpiresMS: c.ExpiresMS, Payload: control.PrepareDataPayload{DataNamespaceID: c.Payload.DataNamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256, DataFormat: c.Payload.DataFormat, QuotaBytes: "8388608", QuotaInodes: "128", WriterFence: "1"}}
	prep.PayloadSHA256, err = control.LogicalDigest(prep)
	if err != nil {
		t.Fatal(err)
	}
	pr, ps, err := control.Sign(prep, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	w := control.DataWriterCommand{Format: control.DataWriterFormat, EffectID: "11111111-1111-4111-8111-111111119902", DataRaw: pr, DataSignature: ps, DataReceiptSHA256: strings.Repeat("a", 64), ResourceID: c.Payload.ResourceID, Platform: c.Payload.Platform, SandboxProfile: control.DataWriterSandboxProfile}
	w.PayloadSHA256, err = control.DataWriterLogicalDigest(w)
	if err != nil {
		t.Fatal(err)
	}
	wr, ws, err := control.SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	start := control.WritableCandidateCommand{Format: control.WritableCandidateFormat, EffectID: "11111111-1111-4111-8111-111111119903", WriterRaw: wr, WriterSignature: ws, WriterReceiptSHA256: strings.Repeat("b", 64), SandboxProfile: control.WritableCandidateSandboxProfile}
	start.PayloadSHA256, err = control.WritableCandidateLogicalDigest(start)
	if err != nil {
		t.Fatal(err)
	}
	sr, ss, err := control.SignWritableCandidate(start, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	reg := control.WritableBackendRegistrationCommand{Format: control.WritableBackendRegistrationFormat, EffectID: "11111111-1111-4111-8111-111111119904", CandidateRaw: sr, CandidateSignature: ss, RunningReceiptSHA256: strings.Repeat("c", 64), DocumentSHA256: strings.Repeat("d", 64)}
	reg.PayloadSHA256, err = control.WritableBackendRegistrationLogicalDigest(reg)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := control.SignWritableBackendRegistration(reg, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	m := struct {
		Format          string `json:"format"`
		ClaimID         string `json:"claim_id"`
		EffectID        string `json:"effect_id"`
		PayloadSHA256   string `json:"payload_sha256"`
		SourceSHA256    string `json:"source_sha256"`
		NativeID        string `json:"native_id"`
		DocumentSHA256  string `json:"document_sha256"`
		ControlRevision string `json:"control_revision"`
		ClaimedMS       string `json:"claimed_ms"`
		ExpiresMS       string `json:"expires_ms"`
	}{"portal.runner.writable-backend-registration-claim/1-dev.1", "11111111-1111-4111-8111-111111119905", reg.EffectID, reg.PayloadSHA256, reg.RunningReceiptSHA256, strings.Repeat("e", 64), reg.DocumentSHA256, c.ControlRevision, c.IssuedMS, c.ExpiresMS}
	mb, _ := json.Marshal(m)
	binding := sha256.Sum256(mb)
	encoded, _ := json.Marshal(map[string]any{"format": "portal.runner.writable-backend-registration-delivery/1-dev.1", "claim": m, "binding_sha256": hex.EncodeToString(binding[:]), "device": "1", "inode": "2", "observed_ms": c.IssuedMS, "settled_ms": c.IssuedMS})
	var receipt journal.WritableBackendRegistrationReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(receipt)
	receipt, err = journal.ParseWritableBackendRegistrationReceipt(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return a, reg, raw, sig, descriptor, receipt
}

func TestWritableHandshakeFixtureAndOpaqueProof(t *testing.T) {
	a, c, raw, sig, _, expected := writableHandshakeRequestFixture(t)
	v, err := control.VerifyWritableBackendRegistration(raw, sig, a, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != c.PayloadSHA256 {
		t.Fatal("fixture authority", err)
	}
	encoded, _ := json.Marshal(expected)
	if _, err := journal.ParseWritableBackendRegistrationReceipt(encoded); err != nil {
		t.Fatal("fixture receipt", err)
	}
	var proof WritableBackendHandshakeObservation
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("serialized native provenance")
	}
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || !proof.ReceivedAt().IsZero() || proof.Evidence().Format != "" {
		t.Fatal("decoded native provenance")
	}
}
