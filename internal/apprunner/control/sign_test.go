package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestSignRequiresExactValidAuthorityAndEnvelope(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, ed25519.SeedSize))
	defer clear(key)
	public := key.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(public)
	id := "00000000-0000-4000-8000-000000000001"
	authority := Authority{DeploymentID: id, ControllerEpoch: id, ControlRevision: 1, PublicKey: public, Fingerprint: hex.EncodeToString(digest[:])}
	c := Command{Format: Format, DeploymentID: id, ControllerEpoch: id, ControlRevision: "1", InstallationID: id, OperationID: id, TargetGeneration: "1", Stage: "prepare_data", EffectNumber: "1", EffectID: id, Kind: "prepare-data", InstallationFence: "1", IssuedMS: "1000", ExpiresMS: "2000", Payload: PrepareDataPayload{DataNamespaceID: id, ReleaseSHA256: hex.EncodeToString(make([]byte, 32)), DataFormat: "1", QuotaBytes: "512", QuotaInodes: "1", WriterFence: "1"}}
	var err error
	c.PayloadSHA256, err = LogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, sig, err := Sign(c, key, authority, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := Verify(raw, sig, authority, 1500); err != nil || verified.Command() != c {
		t.Fatal("signed command did not roundtrip", err)
	}
	for _, kind := range []string{"seed_instead_of_key", "wrong_key", "expired", "wrong_payload", "wrong_authority"} {
		t.Run(kind, func(t *testing.T) {
			command, signing, expected := c, key, authority
			now := int64(1500)
			switch kind {
			case "seed_instead_of_key":
				signing = key[:32]
			case "wrong_key":
				signing = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{31}, 32))
				defer clear(signing)
			case "expired":
				now = 2000
			case "wrong_payload":
				command.PayloadSHA256 = hex.EncodeToString(make([]byte, 32))
			case "wrong_authority":
				expected.ControlRevision = 2
			}
			raw, sig, err := Sign(command, signing, expected, now)
			if err == nil || len(raw) != 0 || len(sig) != 0 {
				t.Fatal("invalid authority emitted command", kind)
			}
		})
	}
}
