package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func writableCandidateFixture(t *testing.T) (WritableCandidateCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	w, key, a, now := dataWriterFixture(t)
	raw, sig, err := SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c := WritableCandidateCommand{Format: WritableCandidateFormat, EffectID: "a0000000-0000-4000-8000-000000000000", WriterRaw: raw, WriterSignature: sig, WriterReceiptSHA256: strings.Repeat("c", 64), SandboxProfile: WritableCandidateSandboxProfile}
	c.PayloadSHA256, err = WritableCandidateLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, key, a, now
}
func TestWritableCandidateDomainAndImmutableSource(t *testing.T) {
	c, key, a, now := writableCandidateFixture(t)
	raw, sig, err := SignWritableCandidate(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyWritableCandidate(raw, sig, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if c.PayloadSHA256 != "e99174d74155ae5fd6f2fbeefbf6afc3c3e9b863a34999b07f35abd080b8fffe" {
		t.Fatal("independent writable candidate vector changed", c.PayloadSHA256)
	}
	if v.Index().Format != WritableCandidateFormat || v.Index().Stage != "start" || v.Index().EffectNumber != "1" || v.Index().Payload.ResourceID != v.Writer().Command().ResourceID || v.Index().Payload.SandboxProfile != WritableCandidateSandboxProfile {
		t.Fatal("projection lost exact future writer identity")
	}
	if _, err := VerifyCandidate(raw, sig, a, now); err == nil {
		t.Fatal("writable request crossed readonly start domain")
	}
	if _, err := VerifyDataWriter(raw, sig, a, now); err == nil {
		t.Fatal("writable request crossed writer mutation domain")
	}
	if _, err := Verify(raw, sig, a, now); err == nil {
		t.Fatal("writable request crossed preparation domain")
	}
	if _, err := VerifyBackendRegistration(raw, sig, a, now); err == nil {
		t.Fatal("writable request crossed registration domain")
	}
	wrong := ed25519.Sign(key, frame(candidateSignaturePurpose, raw))
	if _, err := VerifyWritableCandidate(raw, wrong, a, now); err == nil {
		t.Fatal("legacy start signature authorized writable request")
	}
	copy := v.Command()
	copy.WriterRaw[0] ^= 1
	copy.WriterSignature[0] ^= 1
	nested := v.Writer().Command()
	nested.DataRaw[0] ^= 1
	nested.DataSignature[0] ^= 1
	if !bytes.Equal(v.Command().WriterRaw, c.WriterRaw) {
		t.Fatal("source getter aliased verified input")
	}
	if _, err := VerifyDataWriter(v.Command().WriterRaw, v.Command().WriterSignature, a, now); err != nil {
		t.Fatal("nested getter mutated verified writer", err)
	}
	changed := c
	changed.WriterReceiptSHA256 = strings.Repeat("d", 64)
	if digest, err := WritableCandidateLogicalDigest(changed); err != nil || digest == c.PayloadSHA256 {
		t.Fatal("writer receipt not bound")
	}
}
func TestWritableCandidateRenewalPreservesLogicalIntent(t *testing.T) {
	c, key, a, now := writableCandidateFixture(t)
	var w DataWriterCommand
	json.Unmarshal(c.WriterRaw, &w)
	var ref Command
	json.Unmarshal(w.DataRaw, &ref)
	ref.ControlRevision, ref.InstallationFence, ref.IssuedMS, ref.ExpiresMS = "4", "9", "11000", "41000"
	a.ControlRevision = 4
	var err error
	w.DataRaw, w.DataSignature, err = Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c.WriterRaw, c.WriterSignature, err = SignDataWriter(w, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	if digest, err := WritableCandidateLogicalDigest(c); err != nil || digest != c.PayloadSHA256 {
		t.Fatal("renewal changed immutable start", err)
	}
	if _, _, err := SignWritableCandidate(c, key, a, now); err != nil {
		t.Fatal(err)
	}
}
func TestWritableCandidateRefusesSignedInvalidFields(t *testing.T) {
	for name, change := range map[string]func(*WritableCandidateCommand){
		"format":      func(c *WritableCandidateCommand) { c.Format = CandidateFormat },
		"zero effect": func(c *WritableCandidateCommand) { c.EffectID = "00000000-0000-0000-0000-000000000000" },
		"alias writer": func(c *WritableCandidateCommand) {
			var w DataWriterCommand
			json.Unmarshal(c.WriterRaw, &w)
			c.EffectID = w.EffectID
		},
		"alias resource": func(c *WritableCandidateCommand) {
			var w DataWriterCommand
			json.Unmarshal(c.WriterRaw, &w)
			c.EffectID = w.ResourceID
		},
		"receipt":          func(c *WritableCandidateCommand) { c.WriterReceiptSHA256 = strings.Repeat("C", 64) },
		"readonly profile": func(c *WritableCandidateCommand) { c.SandboxProfile = CandidateSandboxProfile },
		"writer signature": func(c *WritableCandidateCommand) { c.WriterSignature[0] ^= 1 },
		"missing source":   func(c *WritableCandidateCommand) { c.WriterRaw = nil },
	} {
		t.Run(name, func(t *testing.T) {
			c, key, a, now := writableCandidateFixture(t)
			change(&c)
			if digest, err := WritableCandidateLogicalDigest(c); err == nil {
				c.PayloadSHA256 = digest
			}
			raw, _ := json.Marshal(c)
			sig := ed25519.Sign(key, frame(writableCandidateSignaturePurpose, raw))
			if _, err := VerifyWritableCandidate(raw, sig, a, now); err == nil {
				t.Fatal("signed invalid start accepted")
			}
		})
	}
}
func TestWritableCandidateStrictWire(t *testing.T) {
	c, key, a, now := writableCandidateFixture(t)
	raw, _ := json.Marshal(c)
	for _, bad := range [][]byte{nil, append(append([]byte{}, raw...), []byte(` {}`)...), append([]byte(`{"effect_id":"duplicate",`), raw[1:]...), append([]byte(`{"docker_options":{},`), raw[1:]...), bytes.Repeat([]byte{' '}, MaxCommandBytes+1)} {
		sig := ed25519.Sign(key, frame(writableCandidateSignaturePurpose, bad))
		if _, err := VerifyWritableCandidate(bad, sig, a, now); err == nil {
			t.Fatal("invalid wire admitted")
		}
	}
}
