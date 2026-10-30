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

func backendRegistrationFixture(t *testing.T) (BackendRegistrationCommand, ed25519.PrivateKey, Authority, int64, []byte, []byte) {
	t.Helper()
	ref, key, a, now := candidateFixture(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	ref.Payload.ReleaseSHA256 = parsed.SHA256
	ref.PayloadSHA256, err = CandidateLogicalDigest(ref)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := SignCandidate(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := appplatform.NewBackendRegistration(appplatform.BackendIdentity{AppID: parsed.Manifest.ID, DeploymentID: ref.DeploymentID, ControllerEpoch: ref.ControllerEpoch, InstallationID: ref.InstallationID, Generation: "1", PolicyRevision: "1", ReleaseSHA256: parsed.SHA256}, bytes.Repeat([]byte{29}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	document, err := registration.Document()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	c := BackendRegistrationCommand{Format: BackendRegistrationFormat, EffectID: "80000000-0000-4000-8000-000000000000", CandidateRaw: raw, CandidateSignature: sig, DocumentSHA256: hex.EncodeToString(digest[:])}
	c.PayloadSHA256, err = BackendRegistrationLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, key, a, now, descriptor, document
}

func TestBackendRegistrationCommandAndDocumentBinding(t *testing.T) {
	c, key, a, now, descriptor, document := backendRegistrationFixture(t)
	defer clear(document)
	raw, sig, err := SignBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyBackendRegistration(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := ValidateBackendRegistrationDocument(verified, descriptor, document)
	if err != nil {
		t.Fatal(err)
	}
	registration.Close()
	if _, err := VerifyCandidate(raw, sig, a, now); err == nil {
		t.Fatal("registration crossed candidate domain")
	}
	if _, err := Verify(raw, sig, a, now); err == nil {
		t.Fatal("registration crossed data domain")
	}
	wrong := ed25519.Sign(key, frame(candidateSignaturePurpose, raw))
	if _, err := VerifyBackendRegistration(raw, wrong, a, now); err == nil {
		t.Fatal("wrong signature purpose accepted")
	}
	copy := verified.Command()
	copy.CandidateRaw[0] ^= 1
	copy.CandidateSignature[0] ^= 1
	if !bytes.Equal(verified.Command().CandidateRaw, c.CandidateRaw) || !bytes.Equal(verified.Command().CandidateSignature, c.CandidateSignature) {
		t.Fatal("verified reference aliased mutable getter")
	}
	for _, bad := range [][]byte{nil, append(bytes.Clone(document), ' '), []byte(`{}`)} {
		if accepted, err := ValidateBackendRegistrationDocument(verified, descriptor, bad); err == nil {
			accepted.Close()
			t.Fatal("different private document accepted")
		}
		clear(bad)
	}
	if accepted, err := ValidateBackendRegistrationDocument(verified, []byte(`{}`), document); err == nil {
		accepted.Close()
		t.Fatal("unbound descriptor accepted")
	}
	if accepted, err := ValidateBackendRegistrationDocument(VerifiedBackendRegistrationCommand{}, descriptor, document); err == nil {
		accepted.Close()
		t.Fatal("zero verified command accepted")
	}
	if strings.Contains(string(raw), "signing_key") || bytes.Contains(raw, document) {
		t.Fatal("command included registration secret body")
	}
}

func TestBackendRegistrationRenewalAndStrictRefusal(t *testing.T) {
	c, key, a, now, _, document := backendRegistrationFixture(t)
	defer clear(document)
	var ref CandidateCommand
	if err := json.Unmarshal(c.CandidateRaw, &ref); err != nil {
		t.Fatal(err)
	}
	ref.IssuedMS, ref.ExpiresMS, ref.InstallationFence = "11000", "41000", "5"
	var err error
	c.CandidateRaw, c.CandidateSignature, err = SignCandidate(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := BackendRegistrationLogicalDigest(c); err != nil || digest != c.PayloadSHA256 {
		t.Fatal("reference renewal changed logical delivery identity", err)
	}
	raw, sig, err := SignBackendRegistration(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"expired", "signature", "reference", "digest", "unknown", "duplicate", "trailing", "oversize", "authority"} {
		bad, badSig, authority, when := bytes.Clone(raw), bytes.Clone(sig), a, now
		switch mode {
		case "expired":
			when = 41000
		case "signature":
			badSig[0] ^= 1
		case "reference":
			copy := c
			copy.CandidateSignature = bytes.Clone(c.CandidateSignature)
			copy.CandidateSignature[0] ^= 1
			bad, _ = json.Marshal(copy)
			badSig = ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, bad))
		case "digest":
			copy := c
			copy.DocumentSHA256 = strings.Repeat("f", 64)
			bad, _ = json.Marshal(copy)
			badSig = ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, bad))
		case "unknown":
			bad = append(bad[:len(bad)-1], []byte(`,"other":true}`)...)
			badSig = ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, bad))
		case "duplicate":
			bad = append(bad[:len(bad)-1], []byte(`,"format":"other"}`)...)
			badSig = ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, bad))
		case "trailing":
			bad = append(bad, []byte(`{}`)...)
			badSig = ed25519.Sign(key, frame(backendRegistrationSignaturePurpose, bad))
		case "oversize":
			bad = bytes.Repeat([]byte{'x'}, MaxCommandBytes+1)
		case "authority":
			authority.PublicKey = authority.PublicKey[:31]
		}
		if _, err := VerifyBackendRegistration(bad, badSig, authority, when); err == nil {
			t.Fatal("invalid registration authorization accepted", mode)
		}
	}
}

func TestBackendRegistrationDocumentMustMatchCandidateTuple(t *testing.T) {
	for _, mode := range []string{"app", "deployment", "epoch", "installation", "generation", "policy", "release"} {
		t.Run(mode, func(t *testing.T) {
			c, key, a, now, descriptor, original := backendRegistrationFixture(t)
			defer clear(original)
			registration, err := appplatform.ParseBackendRegistration(original)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := registration.Metadata()
			registration.Close()
			if err != nil {
				t.Fatal(err)
			}
			i := metadata.Identity
			switch mode {
			case "app":
				i.AppID = "other-app"
			case "deployment":
				i.DeploymentID = "90000000-0000-4000-8000-000000000000"
			case "epoch":
				i.ControllerEpoch = "90000000-0000-4000-8000-000000000000"
			case "installation":
				i.InstallationID = "90000000-0000-4000-8000-000000000000"
			case "generation":
				i.Generation = "2"
			case "policy":
				i.PolicyRevision = "2"
			case "release":
				i.ReleaseSHA256 = strings.Repeat("f", 64)
			}
			changed, err := appplatform.NewBackendRegistration(i, bytes.Repeat([]byte{29}, 32))
			if err != nil {
				t.Fatal(err)
			}
			defer changed.Close()
			document, err := changed.Document()
			if err != nil {
				t.Fatal(err)
			}
			defer clear(document)
			digest := sha256.Sum256(document)
			c.DocumentSHA256 = hex.EncodeToString(digest[:])
			c.PayloadSHA256, err = BackendRegistrationLogicalDigest(c)
			if err != nil {
				t.Fatal(err)
			}
			raw, sig, err := SignBackendRegistration(c, key, a, now)
			if err != nil {
				t.Fatal(err)
			}
			verified, err := VerifyBackendRegistration(raw, sig, a, now)
			if err != nil {
				t.Fatal(err)
			}
			if accepted, err := ValidateBackendRegistrationDocument(verified, descriptor, document); err == nil {
				accepted.Close()
				t.Fatal("signed but mismatched registration document accepted", mode)
			}
		})
	}
}
