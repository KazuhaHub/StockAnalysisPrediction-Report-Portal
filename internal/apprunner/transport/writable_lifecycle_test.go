package transport

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

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
	copy := proof.Controller()
	copy.PublicKey[0] ^= 1
	if proof.Controller().PublicKey[0] == copy.PublicKey[0] {
		t.Fatal("authority alias")
	}
}
