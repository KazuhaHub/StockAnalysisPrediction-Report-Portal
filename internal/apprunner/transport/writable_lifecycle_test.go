package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func managedRemovalTransportFixture(t *testing.T) (control.Authority, control.ManagedRemovalCommand, []byte, []byte, []byte, journal.ManagedRemovalReceipt) {
	t.Helper()
	a, source, _, _ := candidateReceiptAuthority(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	release := sha256.Sum256(descriptor)
	a.ControlRevision = 3
	now := time.Now().UnixMilli()
	command := control.ManagedRemovalCommand{
		Format: control.ManagedRemovalFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch,
		ControlRevision: strconv.FormatInt(a.ControlRevision, 10), InstallationID: source.InstallationID,
		OperationID: "11111111-1111-4111-8111-111111119911", TargetGeneration: "2",
		Stage: "stop", EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111119912", Kind: "stop",
		InstallationFence: "2", IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		Payload: control.ManagedRemovalPayload{
			ResourceID: source.Payload.ResourceID, DataNamespaceID: source.Payload.DataNamespaceID,
			ReleaseSHA256: hex.EncodeToString(release[:]), Platform: source.Payload.Platform,
			SourceOperationID: source.OperationID, SourceEffectID: source.EffectID,
			SourceReceiptSHA256: strings.Repeat("a", 64),
		},
	}
	command.PayloadSHA256, err = control.ManagedRemovalLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignManagedRemoval(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt := journal.ManagedRemovalReceipt{
		Format: "portal.runner.managed-removal-receipt/1-dev.1", DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID, OperationID: command.OperationID,
		SourceOperationID: command.Payload.SourceOperationID, SourceEffectID: command.Payload.SourceEffectID,
		TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, SourceReceiptSHA256: command.Payload.SourceReceiptSHA256,
		ResourceID: command.Payload.ResourceID, DataNamespaceID: command.Payload.DataNamespaceID,
		ReleaseSHA256: command.Payload.ReleaseSHA256, NativeID: strings.Repeat("b", 64), State: "stopped",
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestWritableLifecycleIndependentActionReferences(t *testing.T) {
	a, c, raw, sig, descriptor, _ := writableHandshakeRequestFixture(t)
	v, err := control.VerifyWritableBackendRegistration(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	start := v.Candidate().Command()
	writer := v.Candidate().Writer().Command()
	for action := WritableWriterExecute; action <= WritableRegistrationRead; action++ {
		request, signature := raw, sig
		if action <= WritableWriterReconcile {
			request, signature = start.WriterRaw, start.WriterSignature
		} else if action <= WritableCandidateProbe {
			request, signature = c.CandidateRaw, c.CandidateSignature
		}
		ref, err := verifyWritableLifecycle(action, request, signature, descriptor, a, time.Now().UnixMilli())
		if err != nil || ref.writer.EffectID != writer.EffectID {
			t.Fatal(action, err)
		}
		badSig := append([]byte(nil), signature...)
		badSig[0] ^= 1
		if _, err := verifyWritableLifecycle(action, request, badSig, descriptor, a, time.Now().UnixMilli()); err == nil {
			t.Fatal("bad signature accepted", action)
		}
		if _, err := verifyWritableLifecycle(action, request, signature, []byte(`{}`), a, time.Now().UnixMilli()); err == nil {
			t.Fatal("bad descriptor accepted", action)
		}
	}
	if _, err := verifyWritableLifecycle(0, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("zero action")
	}
	if _, err := verifyWritableLifecycle(255, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("foreign action")
	}
	if _, err := verifyWritableLifecycle(WritableWriterExecute, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("cross-domain request")
	}
}

func TestWritableLifecycleManagedRemovalReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := managedRemovalTransportFixture(t)
	now := time.Now().UnixMilli()
	ref, err := verifyWritableLifecycle(ManagedRemovalStop, raw, sig, descriptor, a, now)
	if err != nil || ref.removal.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
		t.Fatal("managed removal reference", err)
	}
	body, _ := json.Marshal(receipt)
	started, received := time.Now().Add(-time.Second), time.Now()
	if !validWritableLifecycleReply(ManagedRemovalStop, body, ref, started, received) {
		t.Fatal("exact managed stop reply refused")
	}
	for name, mutate := range map[string]func(*journal.ManagedRemovalReceipt){
		"deployment":       func(v *journal.ManagedRemovalReceipt) { v.DeploymentID = "11111111-1111-4111-8111-111111119920" },
		"installation":     func(v *journal.ManagedRemovalReceipt) { v.InstallationID = "11111111-1111-4111-8111-111111119921" },
		"operation":        func(v *journal.ManagedRemovalReceipt) { v.OperationID = "11111111-1111-4111-8111-111111119922" },
		"source operation": func(v *journal.ManagedRemovalReceipt) { v.SourceOperationID = "11111111-1111-4111-8111-111111119923" },
		"source effect":    func(v *journal.ManagedRemovalReceipt) { v.SourceEffectID = "11111111-1111-4111-8111-111111119924" },
		"target":           func(v *journal.ManagedRemovalReceipt) { v.TargetGeneration = "3" },
		"effect":           func(v *journal.ManagedRemovalReceipt) { v.EffectID = "11111111-1111-4111-8111-111111119925" },
		"payload":          func(v *journal.ManagedRemovalReceipt) { v.PayloadSHA256 = strings.Repeat("c", 64) },
		"source receipt":   func(v *journal.ManagedRemovalReceipt) { v.SourceReceiptSHA256 = strings.Repeat("d", 64) },
		"resource":         func(v *journal.ManagedRemovalReceipt) { v.ResourceID = "11111111-1111-4111-8111-111111119926" },
		"namespace":        func(v *journal.ManagedRemovalReceipt) { v.DataNamespaceID = "11111111-1111-4111-8111-111111119927" },
		"release":          func(v *journal.ManagedRemovalReceipt) { v.ReleaseSHA256 = strings.Repeat("e", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(ManagedRemovalStop, encoded, ref, started, received) {
				t.Fatal("changed managed receipt accepted")
			}
		})
	}
	if _, err := verifyWritableLifecycle(ManagedRemovalStop, raw, sig, []byte(`{}`), a, now); err == nil {
		t.Fatal("managed removal accepted changed descriptor")
	}
	badSignature := append([]byte(nil), sig...)
	badSignature[0] ^= 1
	if _, err := verifyWritableLifecycle(ManagedRemovalStop, raw, badSignature, descriptor, a, now); err == nil {
		t.Fatal("managed removal accepted changed signature")
	}
}

func TestWritableLifecycleOpaqueTypedEvidence(t *testing.T) {
	var proof WritableLifecycleObservation
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("serialized proof")
	}
	proof.action = WritableRegistrationRead
	proof.body = `{}`
	proof.commandSHA = "old"
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || proof.action != 0 || proof.commandSHA != "" {
		t.Fatal("decoded proof")
	}
	if _, err := proof.WriterReceipt(); err == nil {
		t.Fatal("zero writer")
	}
	if _, err := proof.RunningReceipt(); err == nil {
		t.Fatal("zero running")
	}
	if _, err := proof.Readiness(); err == nil {
		t.Fatal("zero readiness")
	}
	if _, err := proof.RegistrationReceipt(); err == nil {
		t.Fatal("zero registration")
	}
	a, _, _, _, _, r := writableHandshakeRequestFixture(t)
	body, _ := json.Marshal(r)
	proof = WritableLifecycleObservation{action: WritableRegistrationRead, body: string(body), authority: a}
	if _, err := proof.RegistrationReceipt(); err != nil {
		t.Fatal(err)
	}
	if _, err := proof.RunningReceipt(); err == nil {
		t.Fatal("delivery used as running")
	}
	_, _, _, _, _, removal := managedRemovalTransportFixture(t)
	removalBody, _ := json.Marshal(removal)
	proof = WritableLifecycleObservation{action: ManagedRemovalRead, body: string(removalBody), authority: a}
	if got, err := proof.ManagedRemovalReceipt(); err != nil || got != removal {
		t.Fatal("managed removal evidence", got, err)
	}
	proof.action = WritableRegistrationRead
	if _, err := proof.ManagedRemovalReceipt(); err == nil {
		t.Fatal("registration used as managed removal")
	}
	copy := proof.Controller()
	copy.PublicKey[0] ^= 1
	if proof.Controller().PublicKey[0] == copy.PublicKey[0] {
		t.Fatal("authority alias")
	}
}
