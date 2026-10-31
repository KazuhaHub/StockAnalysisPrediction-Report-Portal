package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func dataWriterFixture(t *testing.T) (DataWriterCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	base, key, a, now := candidateFixture(t)
	ref := Command{Format: Format, DeploymentID: base.DeploymentID, ControllerEpoch: base.ControllerEpoch, ControlRevision: base.ControlRevision, InstallationID: base.InstallationID, OperationID: base.OperationID, TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: base.EffectID, Kind: "prepare-data", InstallationFence: base.InstallationFence, IssuedMS: base.IssuedMS, ExpiresMS: base.ExpiresMS, Payload: PrepareDataPayload{DataNamespaceID: base.Payload.DataNamespaceID, ReleaseSHA256: base.Payload.ReleaseSHA256, DataFormat: "1", QuotaBytes: "8388608", QuotaInodes: "128", WriterFence: "1"}}
	ref.PayloadSHA256, _ = LogicalDigest(ref)
	raw, sig, err := Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	c := DataWriterCommand{Format: DataWriterFormat, EffectID: "80000000-0000-4000-8000-000000000000", DataRaw: raw, DataSignature: sig, DataReceiptSHA256: strings.Repeat("b", 64), ResourceID: base.Payload.ResourceID, Platform: base.Payload.Platform, SandboxProfile: DataWriterSandboxProfile}
	c.PayloadSHA256, err = DataWriterLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	return c, key, a, now
}

func TestDataWriterCommandDomainAndBinding(t *testing.T) {
	c, key, a, now := dataWriterFixture(t)
	if c.PayloadSHA256 != "9317625db31c65f21081e4287512d0c66bf82c6ffc78e8306986c859b95bd308" {
		t.Fatal("independent initial writer logical vector changed")
	}
	raw, sig, err := SignDataWriter(c, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	v, err := VerifyDataWriter(raw, sig, a, now)
	if err != nil || v.Command().PayloadSHA256 != c.PayloadSHA256 || v.Data().Payload.WriterFence != "1" {
		t.Fatal("writer verification", err)
	}
	if _, err := Verify(raw, sig, a, now); err == nil {
		t.Fatal("writer crossed data domain")
	}
	if _, err := VerifyCandidate(raw, sig, a, now); err == nil {
		t.Fatal("writer crossed candidate domain")
	}
	if _, err := VerifyBackendRegistration(raw, sig, a, now); err == nil {
		t.Fatal("writer crossed registration domain")
	}
	wrong := ed25519.Sign(key, frame("portal.app/runner-command/v1", raw))
	if _, err := VerifyDataWriter(raw, wrong, a, now); err == nil {
		t.Fatal("wrong signature purpose accepted")
	}
	copy := v.Command()
	copy.DataRaw[0] ^= 1
	copy.DataSignature[0] ^= 1
	if !bytes.Equal(v.Command().DataRaw, c.DataRaw) || !bytes.Equal(v.Command().DataSignature, c.DataSignature) {
		t.Fatal("verified source aliased mutable getter")
	}
	changed := c
	changed.DataReceiptSHA256 = strings.Repeat("c", 64)
	digest, err := DataWriterLogicalDigest(changed)
	if err != nil || digest == c.PayloadSHA256 {
		t.Fatal("receipt not bound")
	}
	changed = c
	changed.ResourceID = "90000000-0000-4000-8000-000000000000"
	digest, err = DataWriterLogicalDigest(changed)
	if err != nil || digest == c.PayloadSHA256 {
		t.Fatal("future writer resource not bound")
	}
}

func TestDataWriterRenewalKeepsLogicalSource(t *testing.T) {
	c, key, a, now := dataWriterFixture(t)
	var ref Command
	if json.Unmarshal(c.DataRaw, &ref) != nil {
		t.Fatal("fixture source")
	}
	ref.ControlRevision, ref.InstallationFence, ref.IssuedMS, ref.ExpiresMS = "4", "9", "11000", "41000"
	a.ControlRevision = 4
	var err error
	c.DataRaw, c.DataSignature, err = Sign(ref, key, a, now)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := DataWriterLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		t.Fatal("renewal changed logical writer", err)
	}
	if _, _, err := SignDataWriter(c, key, a, now); err != nil {
		t.Fatal(err)
	}
	// Signed renewal is not durable takeover authorization; the journal must
	// still compare the original source fence and current resource floor.
}

func TestDataWriterRejectsSignedInvalidFields(t *testing.T) {
	for name, change := range map[string]func(*DataWriterCommand){
		"format": func(c *DataWriterCommand) { c.Format = BackendRegistrationFormat },
		"effect alias": func(c *DataWriterCommand) {
			var ref Command
			json.Unmarshal(c.DataRaw, &ref)
			c.EffectID = ref.EffectID
		},
		"resource alias": func(c *DataWriterCommand) {
			var ref Command
			json.Unmarshal(c.DataRaw, &ref)
			c.ResourceID = ref.Payload.DataNamespaceID
		},
		"effect resource alias": func(c *DataWriterCommand) { c.ResourceID = c.EffectID },
		"zero effect":           func(c *DataWriterCommand) { c.EffectID = "00000000-0000-0000-0000-000000000000" },
		"resource path":         func(c *DataWriterCommand) { c.ResourceID = "/data/other" },
		"receipt":               func(c *DataWriterCommand) { c.DataReceiptSHA256 = strings.Repeat("B", 64) },
		"platform":              func(c *DataWriterCommand) { c.Platform = "linux/386" },
		"profile":               func(c *DataWriterCommand) { c.SandboxProfile = CandidateSandboxProfile },
		"nested signature":      func(c *DataWriterCommand) { c.DataSignature[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c, key, a, now := dataWriterFixture(t)
			change(&c)
			if digest, err := DataWriterLogicalDigest(c); err == nil {
				c.PayloadSHA256 = digest
			}
			raw, _ := json.Marshal(c)
			sig := ed25519.Sign(key, frame(dataWriterSignaturePurpose, raw))
			if _, err := VerifyDataWriter(raw, sig, a, now); err == nil {
				t.Fatal("invalid signed writer accepted")
			}
		})
	}
	for name, change := range map[string]func(*Command){
		"generation":   func(c *Command) { c.TargetGeneration = "2" },
		"writer fence": func(c *Command) { c.Payload.WriterFence = "2" },
		"source slot":  func(c *Command) { c.EffectNumber = "2" },
		"expired":      func(c *Command) { c.ExpiresMS = "20000" },
		"future":       func(c *Command) { c.IssuedMS = "20001" },
		"namespace":    func(c *Command) { c.Payload.DataNamespaceID = "00000000-0000-0000-0000-000000000000" },
	} {
		t.Run(name, func(t *testing.T) {
			c, key, a, now := dataWriterFixture(t)
			var ref Command
			json.Unmarshal(c.DataRaw, &ref)
			change(&ref)
			if digest, err := LogicalDigest(ref); err == nil {
				ref.PayloadSHA256 = digest
			}
			c.DataRaw, _ = json.Marshal(ref)
			c.DataSignature = ed25519.Sign(key, frame("portal.app/runner-command/v1", c.DataRaw))
			if digest, err := DataWriterLogicalDigest(c); err == nil {
				c.PayloadSHA256 = digest
			}
			raw, _ := json.Marshal(c)
			sig := ed25519.Sign(key, frame(dataWriterSignaturePurpose, raw))
			if _, err := VerifyDataWriter(raw, sig, a, now); err == nil {
				t.Fatal("invalid signed source accepted")
			}
		})
	}
}

func TestDataWriterWireRejectsAmbiguity(t *testing.T) {
	c, key, a, now := dataWriterFixture(t)
	raw, _ := json.Marshal(c)
	for _, bad := range [][]byte{
		nil, []byte(`null`), []byte(`[]`), append(bytes.Clone(raw), []byte(` {}`)...),
		bytes.Replace(raw, []byte(`"format":`), []byte(`"Format":`), 1),
		bytes.Replace(raw, []byte(`"effect_id":`), []byte(`"effect_id":"90000000-0000-4000-8000-000000000000","effect_id":`), 1),
		append([]byte(`{"host_path":"/data",`), raw[1:]...),
		bytes.Repeat([]byte{'x'}, MaxCommandBytes+1),
	} {
		sig := ed25519.Sign(key, frame(dataWriterSignaturePurpose, bad))
		if _, err := VerifyDataWriter(bad, sig, a, now); err == nil {
			t.Fatal("ambiguous writer wire accepted")
		}
	}
}
