package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func checkpointFixture(t *testing.T) (CheckpointCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{41}, ed25519.SeedSize))
	public := key.Public().(ed25519.PublicKey)
	fingerprint := sha256Hex(public)
	authority := Authority{
		DeploymentID: "10000000-0000-4000-8000-000000000001", ControllerEpoch: "20000000-0000-4000-8000-000000000002",
		ControlRevision: 3, PublicKey: public, Fingerprint: fingerprint,
	}
	now := int64(30000)
	command := CheckpointCommand{
		Format: CheckpointFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch,
		ControlRevision: "3", InstallationID: "30000000-0000-4000-8000-000000000003",
		OperationID: "40000000-0000-4000-8000-000000000004", TargetGeneration: "2",
		Stage: "snapshot", EffectNumber: "1", EffectID: "50000000-0000-4000-8000-000000000005",
		Kind: "snapshot", InstallationFence: "2",
		Payload: CheckpointPayload{
			CheckpointID: "60000000-0000-4000-8000-000000000006", NamespaceID: "70000000-0000-4000-8000-000000000007",
			ResourceID:    "71000000-0000-4000-8000-000000000071",
			ReleaseSHA256: strings.Repeat("a", 64), SourceOperationID: "80000000-0000-4000-8000-000000000008",
			SourceStartEffectID: "90000000-0000-4000-8000-000000000009", SourceReceiptSHA256: strings.Repeat("b", 64),
			SourceDataEffectID: "a0000000-0000-4000-8000-00000000000a", SourceDataReceiptSHA256: strings.Repeat("c", 64),
			ProjectID: "4101", NamespaceInode: "10", Device: "11", DataFormat: "1", WriterFence: "1",
		},
	}
	digest, err := CheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestCheckpointCommandRoundTripAndBinding(t *testing.T) {
	command, key, authority, now := checkpointFixture(t)
	raw, signature, err := SignCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyCheckpoint(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("checkpoint command round trip", err)
	}
	for name, mutate := range map[string]func(*CheckpointCommand){
		"stage":               func(c *CheckpointCommand) { c.Stage = "stop" },
		"kind":                func(c *CheckpointCommand) { c.Kind = "stop" },
		"checkpoint":          func(c *CheckpointCommand) { c.Payload.CheckpointID = "60000000-0000-4000-8000-000000000016" },
		"namespace":           func(c *CheckpointCommand) { c.Payload.NamespaceID = "70000000-0000-4000-8000-000000000017" },
		"resource":            func(c *CheckpointCommand) { c.Payload.ResourceID = "71000000-0000-4000-8000-000000000072" },
		"source operation":    func(c *CheckpointCommand) { c.Payload.SourceOperationID = "80000000-0000-4000-8000-000000000018" },
		"source start":        func(c *CheckpointCommand) { c.Payload.SourceStartEffectID = "90000000-0000-4000-8000-000000000019" },
		"source receipt":      func(c *CheckpointCommand) { c.Payload.SourceReceiptSHA256 = strings.Repeat("d", 64) },
		"source data":         func(c *CheckpointCommand) { c.Payload.SourceDataEffectID = "a0000000-0000-4000-8000-00000000001a" },
		"source data receipt": func(c *CheckpointCommand) { c.Payload.SourceDataReceiptSHA256 = strings.Repeat("e", 64) },
		"project":             func(c *CheckpointCommand) { c.Payload.ProjectID = "4102" },
		"inode":               func(c *CheckpointCommand) { c.Payload.NamespaceInode = "12" },
		"device":              func(c *CheckpointCommand) { c.Payload.Device = "12" },
		"data format":         func(c *CheckpointCommand) { c.Payload.DataFormat = "2" },
		"writer fence":        func(c *CheckpointCommand) { c.Payload.WriterFence = "2" },
		"generation":          func(c *CheckpointCommand) { c.TargetGeneration = "3" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			digest, err := CheckpointLogicalDigest(changed)
			if name == "stage" || name == "kind" {
				if err == nil {
					t.Fatal("invalid checkpoint action admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			changed.PayloadSHA256 = digest
			raw, signature, err := SignCheckpoint(changed, key, authority, now)
			if err != nil || len(raw) == 0 || len(signature) != ed25519.SignatureSize {
				t.Fatal("changed checkpoint could not be signed", err)
			}
		})
	}
}

func TestCheckpointCommandRejectsAuthorityAndTime(t *testing.T) {
	command, key, authority, now := checkpointFixture(t)
	raw, signature, err := SignCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	other := authority
	other.ControlRevision++
	if _, err := VerifyCheckpoint(raw, signature, other, now); err == nil {
		t.Fatal("changed authority accepted")
	}
	if _, err := VerifyCheckpoint(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired checkpoint accepted")
	}
	changed := append([]byte(nil), raw...)
	changed[len(changed)/2] ^= 1
	if _, err := VerifyCheckpoint(changed, signature, authority, now); err == nil {
		t.Fatal("changed checkpoint bytes accepted")
	}
}
