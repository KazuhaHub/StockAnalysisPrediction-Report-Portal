package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func candidateFixture(t *testing.T) (CandidateCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, 32))
	public := key.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(public)
	id := func(s string) string { return s + "0000000-0000-4000-8000-000000000000" }
	a := Authority{DeploymentID: id("1"), ControllerEpoch: id("2"), ControlRevision: 3, PublicKey: public, Fingerprint: hex.EncodeToString(sum[:])}
	c := CandidateCommand{Format: CandidateFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch, ControlRevision: "3", InstallationID: id("3"), OperationID: id("4"), TargetGeneration: "1", Stage: "start", EffectNumber: "1", EffectID: id("5"), Kind: "start-candidate", InstallationFence: "4", IssuedMS: "10000", ExpiresMS: "40000", Payload: CandidatePayload{ResourceID: id("6"), DataNamespaceID: id("7"), ReleaseSHA256: strings.Repeat("a", 64), Platform: "linux/amd64", DataFormat: "1", WriterFence: "5", SandboxProfile: CandidateSandboxProfile}}
	digest, err := CandidateLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	return c, key, a, 20000
}
func TestCandidateCommandRoundTripAndDomainSeparation(t *testing.T) {
	c, key, a, now := candidateFixture(t)
	if c.PayloadSHA256 != "d3afb103abf8796bf6b800fe9ac587ca185b36c15a65747022712cf67174ba0b" {
		t.Fatal("candidate logical framing changed")
	}
	raw, sig, err := SignCandidate(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyCandidate(raw, sig, a, now)
	if err != nil || verified.Command() != c {
		t.Fatal("candidate verification failed", err)
	}
	if _, err := Verify(raw, sig, a, now); err == nil {
		t.Fatal("candidate accepted as prepare-data")
	}
	wrong := ed25519.Sign(key, frame("portal.app/runner-command/v1", raw))
	if _, err := VerifyCandidate(raw, wrong, a, now); err == nil {
		t.Fatal("prepare-data signature domain accepted")
	}
	oldDigest := c.PayloadSHA256
	c.InstallationFence = "9"
	c.ControlRevision = "4"
	c.IssuedMS = "11000"
	c.ExpiresMS = "41000"
	digest, err := CandidateLogicalDigest(c)
	if err != nil || digest != oldDigest {
		t.Fatal("transport renewal changed logical effect")
	}
	c.Payload.ResourceID = "90000000-0000-4000-8000-000000000000"
	digest, err = CandidateLogicalDigest(c)
	if err != nil || digest == oldDigest {
		t.Fatal("resource substitution preserved logical effect")
	}
}
func TestCandidateCommandRejectsSignedInvalidInputs(t *testing.T) {
	cases := map[string]func(*CandidateCommand){
		"wrong format":             func(c *CandidateCommand) { c.Format = Format },
		"wrong stage":              func(c *CandidateCommand) { c.Stage = "activate" },
		"wrong kind":               func(c *CandidateCommand) { c.Kind = "prepare-data" },
		"rootless profile":         func(c *CandidateCommand) { c.Payload.SandboxProfile = "rootless" },
		"platform":                 func(c *CandidateCommand) { c.Payload.Platform = "linux/386" },
		"zero resource":            func(c *CandidateCommand) { c.Payload.ResourceID = "00000000-0000-0000-0000-000000000000" },
		"namespace traversal":      func(c *CandidateCommand) { c.Payload.DataNamespaceID = "../other" },
		"generation alias":         func(c *CandidateCommand) { c.TargetGeneration = "01" },
		"expired":                  func(c *CandidateCommand) { c.ExpiresMS = "20000" },
		"future":                   func(c *CandidateCommand) { c.IssuedMS = "20001"; c.ExpiresMS = "40000" },
		"long expiry":              func(c *CandidateCommand) { c.ExpiresMS = "40001" },
		"wrong authority revision": func(c *CandidateCommand) { c.ControlRevision = "4" },
		"wrong deployment":         func(c *CandidateCommand) { c.DeploymentID = "90000000-0000-4000-8000-000000000000" },
		"wrong epoch":              func(c *CandidateCommand) { c.ControllerEpoch = "90000000-0000-4000-8000-000000000000" },
		"zero fence":               func(c *CandidateCommand) { c.InstallationFence = "0" },
		"bad writer":               func(c *CandidateCommand) { c.Payload.WriterFence = "-1" },
		"format limit":             func(c *CandidateCommand) { c.Payload.DataFormat = "1000001" },
		"slot limit":               func(c *CandidateCommand) { c.EffectNumber = "33" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, key, a, now := candidateFixture(t)
			mutate(&c)
			if digest, err := CandidateLogicalDigest(c); err == nil {
				c.PayloadSHA256 = digest
			}
			raw, _ := json.Marshal(c)
			sig := ed25519.Sign(key, frame(candidateSignaturePurpose, raw))
			if _, err := VerifyCandidate(raw, sig, a, now); err == nil {
				t.Fatal("invalid signed candidate accepted")
			}
		})
	}
}
func TestCandidateWireParserRejectsAmbiguity(t *testing.T) {
	c, key, a, now := candidateFixture(t)
	raw, _ := json.Marshal(c)
	cases := [][]byte{
		append(append([]byte{}, raw...), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"stage":"start"`), []byte(`"stage":"start","stage":"start"`), 1),
		bytes.Replace(raw, []byte(`"stage":"start"`), []byte(`"Stage":"start"`), 1),
		bytes.Replace(raw, []byte(`"platform":"linux/amd64"`), []byte(`"platform":"linux/amd64","platform":"linux/amd64"`), 1),
		bytes.Replace(raw, []byte(`"payload":{`), []byte(`"extra":true,"payload":{`), 1),
		[]byte(strings.Repeat(" ", MaxCommandBytes+1)),
	}
	for i, body := range cases {
		sig := ed25519.Sign(key, frame(candidateSignaturePurpose, body))
		if _, err := VerifyCandidate(body, sig, a, now); err == nil {
			t.Fatal("ambiguous candidate accepted", i)
		}
	}
	a.Fingerprint = strings.Repeat("0", 64)
	sig := ed25519.Sign(key, frame(candidateSignaturePurpose, raw))
	if _, err := VerifyCandidate(raw, sig, a, now); err == nil {
		t.Fatal("inconsistent trust snapshot accepted")
	}
}

func TestCandidateLogicalIdentityPinsAllImmutableInputs(t *testing.T) {
	changes := map[string]func(*CandidateCommand){
		"resource":     func(c *CandidateCommand) { c.Payload.ResourceID = "90000000-0000-4000-8000-000000000000" },
		"namespace":    func(c *CandidateCommand) { c.Payload.DataNamespaceID = "90000000-0000-4000-8000-000000000000" },
		"release":      func(c *CandidateCommand) { c.Payload.ReleaseSHA256 = strings.Repeat("b", 64) },
		"platform":     func(c *CandidateCommand) { c.Payload.Platform = "linux/arm64" },
		"data format":  func(c *CandidateCommand) { c.Payload.DataFormat = "2" },
		"writer":       func(c *CandidateCommand) { c.Payload.WriterFence = "6" },
		"generation":   func(c *CandidateCommand) { c.TargetGeneration = "2" },
		"slot":         func(c *CandidateCommand) { c.EffectNumber = "2" },
		"operation":    func(c *CandidateCommand) { c.OperationID = "90000000-0000-4000-8000-000000000000" },
		"installation": func(c *CandidateCommand) { c.InstallationID = "90000000-0000-4000-8000-000000000000" },
		"deployment":   func(c *CandidateCommand) { c.DeploymentID = "90000000-0000-4000-8000-000000000000" },
		"epoch":        func(c *CandidateCommand) { c.ControllerEpoch = "90000000-0000-4000-8000-000000000000" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			c, _, _, _ := candidateFixture(t)
			before := c.PayloadSHA256
			change(&c)
			after, err := CandidateLogicalDigest(c)
			if err != nil || before == after {
				t.Fatal("immutable input escaped effect binding", name, err)
			}
		})
	}
}
