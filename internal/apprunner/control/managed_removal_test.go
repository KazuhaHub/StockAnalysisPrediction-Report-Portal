package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func managedRemovalFixture(t *testing.T) (ManagedRemovalCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, ed25519.SeedSize))
	authority := Authority{
		DeploymentID:    "10000000-0000-4000-8000-000000000001",
		ControllerEpoch: "10000000-0000-4000-8000-000000000002",
		ControlRevision: 7,
		PublicKey:       key.Public().(ed25519.PublicKey),
		Fingerprint:     containerDigest(key.Public().(ed25519.PublicKey)),
	}
	command := ManagedRemovalCommand{
		Format:            ManagedRemovalFormat,
		DeploymentID:      authority.DeploymentID,
		ControllerEpoch:   authority.ControllerEpoch,
		ControlRevision:   "7",
		InstallationID:    "10000000-0000-4000-8000-000000000003",
		OperationID:       "10000000-0000-4000-8000-000000000004",
		TargetGeneration:  "2",
		Stage:             "stop",
		EffectNumber:      "1",
		EffectID:          "10000000-0000-4000-8000-000000000005",
		Kind:              "stop",
		InstallationFence: "9",
		IssuedMS:          "10000",
		ExpiresMS:         "40000",
		Payload: ManagedRemovalPayload{
			ResourceID:          "10000000-0000-4000-8000-000000000006",
			DataNamespaceID:     "10000000-0000-4000-8000-000000000007",
			ReleaseSHA256:       strings.Repeat("a", 64),
			Platform:            "linux/amd64",
			SourceOperationID:   "10000000-0000-4000-8000-000000000008",
			SourceEffectID:      "10000000-0000-4000-8000-000000000009",
			SourceReceiptSHA256: strings.Repeat("b", 64),
		},
	}
	digest, err := ManagedRemovalLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	return command, key, authority, 20000
}

func containerDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestManagedRemovalCommandRoundTripAndStageBinding(t *testing.T) {
	c, key, authority, now := managedRemovalFixture(t)
	raw, signature, err := SignManagedRemoval(c, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyManagedRemoval(raw, signature, authority, now)
	if err != nil || verified.Command() != c {
		t.Fatal(err)
	}
	changed := c
	changed.Stage = "remove_code"
	changed.Kind = "remove-code"
	digest, err := ManagedRemovalLogicalDigest(changed)
	if err != nil {
		t.Fatal(err)
	}
	changed.PayloadSHA256 = digest
	raw, signature, err = SignManagedRemoval(changed, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyManagedRemoval(raw, signature, authority, now); err != nil || got.Command() != changed {
		t.Fatal(got, err)
	}
	if _, _, err := SignManagedRemoval(ManagedRemovalCommand{}, key, authority, now); err == nil {
		t.Fatal("zero removal command accepted")
	}
}

func TestManagedRemovalCommandRejectsChangedAuthorityAndAction(t *testing.T) {
	c, key, authority, now := managedRemovalFixture(t)
	for name, mutate := range map[string]func(*ManagedRemovalCommand){
		"action":           func(v *ManagedRemovalCommand) { v.Stage, v.Kind = "remove_code", "stop" },
		"resource":         func(v *ManagedRemovalCommand) { v.Payload.ResourceID = "10000000-0000-4000-8000-000000000010" },
		"source operation": func(v *ManagedRemovalCommand) { v.Payload.SourceOperationID = "10000000-0000-4000-8000-000000000011" },
		"receipt":          func(v *ManagedRemovalCommand) { v.Payload.SourceReceiptSHA256 = strings.Repeat("c", 64) },
		"platform":         func(v *ManagedRemovalCommand) { v.Payload.Platform = "linux/386" },
		"generation":       func(v *ManagedRemovalCommand) { v.TargetGeneration = "1" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := c
			mutate(&changed)
			raw, signature, err := SignManagedRemoval(changed, key, authority, now)
			if err == nil {
				if _, err := VerifyManagedRemoval(raw, signature, authority, now); err == nil {
					t.Fatal("changed command accepted")
				}
			}
		})
	}
	raw, signature, err := SignManagedRemoval(c, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	other := authority
	other.ControlRevision = 8
	if _, err := VerifyManagedRemoval(raw, signature, other, now); err == nil {
		t.Fatal("stale control revision accepted")
	}
	if _, err := VerifyManagedRemoval(raw, signature, authority, 40000); err == nil {
		t.Fatal("expired command accepted")
	}
	if _, err := VerifyManagedRemoval(raw, signature, authority, 9999); err == nil {
		t.Fatal("command accepted before issuance")
	}
	changed := []byte(strings.Replace(string(raw), `"stage":"stop"`, `"stage":"remove_code"`, 1))
	if _, err := VerifyManagedRemoval(changed, signature, authority, now); err == nil {
		t.Fatal("changed canonical bytes accepted")
	}
}
