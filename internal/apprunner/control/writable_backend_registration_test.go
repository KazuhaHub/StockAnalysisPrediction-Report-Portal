package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func writableRegistrationFixture(t *testing.T) (WritableBackendRegistrationCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	ref, key, a, now := writableCandidateFixture(t)
	raw, sig, err := SignWritableCandidate(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c := WritableBackendRegistrationCommand{Format: WritableBackendRegistrationFormat, EffectID: "b0000000-0000-4000-8000-000000000000", CandidateRaw: raw, CandidateSignature: sig, RunningReceiptSHA256: strings.Repeat("d", 64), DocumentSHA256: strings.Repeat("e", 64)}
	c.PayloadSHA256, err = WritableBackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, key, a, now
}
func TestWritableRegistrationDomainAndExactBindings(t *testing.T) {
	c, key, a, now := writableRegistrationFixture(t)
	raw, sig, err := SignWritableBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyWritableBackendRegistration(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.PayloadSHA256 != "310f8af108a4c51907b1d30a319069ca8d6d75be95c4be2d5d9cbc40bee41aa8" {
		t.Fatal("independent registration vector changed", c.PayloadSHA256)
	}
	if v.Candidate().Index().Format != WritableCandidateFormat || !bytes.Equal(v.Command().CandidateRaw, c.CandidateRaw) {
		t.Fatal("lost writable reference")
	}
	copy := v.Command()
	copy.CandidateRaw[0] ^= 1
	copy.CandidateSignature[0] ^= 1
	inner := v.Candidate().Command()
	inner.WriterRaw[0] ^= 1
	if _, err := VerifyWritableCandidate(v.Command().CandidateRaw, v.Command().CandidateSignature, a, now); err != nil {
		t.Fatal("getter aliased source", err)
	}
	for _, purpose := range []string{backendRegistrationSignaturePurpose, writableCandidateSignaturePurpose, candidateSignaturePurpose} {
		bad := ed25519.Sign(key, frame(purpose, raw))
		if _, err := VerifyWritableBackendRegistration(raw, bad, a, now); err == nil {
			t.Fatal("foreign signature purpose accepted")
		}
	}
	if _, err := VerifyBackendRegistration(raw, sig, a, now); err == nil {
		t.Fatal("writable crossed readonly registration")
	}
	if _, err := VerifyWritableCandidate(raw, sig, a, now); err == nil {
		t.Fatal("registration crossed start authority")
	}
	for _, mode := range []string{"running", "document", "alias", "unknown", "duplicate", "bad_source", "bad_signature"} {
		changed := c
		switch mode {
		case "running":
			changed.RunningReceiptSHA256 = strings.Repeat("f", 64)
		case "document":
			changed.DocumentSHA256 = strings.Repeat("f", 64)
		case "alias":
			changed.EffectID = v.Candidate().Index().Payload.ResourceID
		case "bad_source":
			changed.CandidateRaw = bytes.Clone(c.CandidateRaw)
			changed.CandidateRaw[0] ^= 1
		case "bad_signature":
			changed.CandidateSignature = bytes.Repeat([]byte{0}, 64)
		}
		encoded, _ := json.Marshal(changed)
		if mode == "unknown" {
			encoded = append(encoded[:len(encoded)-1], []byte(`,"secret":"forbidden"}`)...)
		}
		if mode == "duplicate" {
			encoded = append(encoded[:len(encoded)-1], []byte(`,"effect_id":"`+c.EffectID+`"}`)...)
		}
		signed := ed25519.Sign(key, frame(writableBackendRegistrationSignaturePurpose, encoded))
		if _, err := VerifyWritableBackendRegistration(encoded, signed, a, now); err == nil {
			t.Fatal("changed registration accepted", mode)
		}
	}
}
func TestWritableRegistrationRenewalPreservesLogicalIntent(t *testing.T) {
	c, key, a, now := writableRegistrationFixture(t)
	original := c.PayloadSHA256
	var ref WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &ref)
	var w DataWriterCommand
	json.Unmarshal(ref.WriterRaw, &w)
	var source Command
	json.Unmarshal(w.DataRaw, &source)
	source.ControlRevision, source.InstallationFence, source.IssuedMS, source.ExpiresMS = "4", "9", "11000", "41000"
	a.ControlRevision = 4
	var err error
	w.DataRaw, w.DataSignature, err = Sign(source, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	ref.WriterRaw, ref.WriterSignature, err = SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.CandidateRaw, c.CandidateSignature, err = SignWritableCandidate(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := WritableBackendRegistrationLogicalDigest(c); err != nil || digest != original {
		t.Fatal("renewal changed logical effect", err)
	}
	if _, _, err := SignWritableBackendRegistration(c, key, a, now); err != nil {
		t.Fatal(err)
	}
}
func TestWritableRegistrationPrivateDocumentValidation(t *testing.T) {
	c, key, a, now := writableRegistrationFixture(t)
	var ref WritableCandidateCommand
	json.Unmarshal(c.CandidateRaw, &ref)
	var w DataWriterCommand
	json.Unmarshal(ref.WriterRaw, &w)
	var source Command
	json.Unmarshal(w.DataRaw, &source)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	source.Payload.ReleaseSHA256 = parsed.SHA256
	source.PayloadSHA256, err = LogicalDigest(source)
	if err != nil {
		t.Fatal(err)
	}
	w.DataRaw, w.DataSignature, err = Sign(source, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	w.PayloadSHA256, err = DataWriterLogicalDigest(w)
	if err != nil {
		t.Fatal(err)
	}
	ref.WriterRaw, ref.WriterSignature, err = SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	ref.PayloadSHA256, err = WritableCandidateLogicalDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	c.CandidateRaw, c.CandidateSignature, err = SignWritableCandidate(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	reg, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: source.DeploymentID, ControllerEpoch: source.ControllerEpoch, InstallationID: source.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: parsed.SHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	doc, err := reg.Document()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(doc)
	sum := sha256.Sum256(doc)
	c.DocumentSHA256 = hex.EncodeToString(sum[:])
	c.PayloadSHA256, err = WritableBackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := SignWritableBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyWritableBackendRegistration(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := ValidateWritableBackendRegistrationDocument(v, descriptor, doc)
	if err != nil {
		t.Fatal(err)
	}
	validated.Close()
	if _, err := ValidateWritableBackendRegistrationDocument(v, descriptor, append(bytes.Clone(doc), ' ')); err == nil {
		t.Fatal("changed secret bytes accepted")
	}
	if _, err := ValidateWritableBackendRegistrationDocument(VerifiedWritableBackendRegistrationCommand{}, descriptor, doc); err == nil {
		t.Fatal("zero command validated secret")
	}
}
