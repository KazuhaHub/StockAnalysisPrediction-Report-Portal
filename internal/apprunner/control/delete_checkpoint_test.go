package control

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func deleteCheckpointFixture(t *testing.T) (DeleteCheckpointCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	load, key, authority, now := loadImageFixture(t)
	command := DeleteCheckpointCommand{CommandHeader: load.CommandHeader, Payload: DeleteCheckpointPayload{
		RecoverySetID: "c0000000-0000-4000-8000-0000000000d1", CheckpointID: "c0000000-0000-4000-8000-0000000000d2",
		NamespaceID: "c0000000-0000-4000-8000-0000000000d3", SourceOperationID: "c0000000-0000-4000-8000-0000000000d4",
		ReleaseSHA256: strings.Repeat("9", 64), ManifestSHA256: strings.Repeat("5", 64), ArchiveBytes: "4096", DataFormat: "1", WriterFence: "3",
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber = DeleteCheckpointFormat, DeleteCheckpointKind, DeleteCheckpointStage, "2"
	command.EffectID = "c0000000-0000-4000-8000-0000000000b2"
	digest, err := DeleteCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestDeleteCheckpointRoundTripsAndRefusesNonCanonicalBytes(t *testing.T) {
	command, key, authority, now := deleteCheckpointFixture(t)
	raw, signature, err := SignDeleteCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyDeleteCheckpoint(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	for name, variant := range map[string][]byte{
		"trailing":  append(append([]byte(nil), raw...), '\n'),
		"duplicate": []byte(strings.Replace(string(raw), `"stage":"remove_data"`, `"stage":"remove_data","stage":"remove_data"`, 1)),
		"escaped":   []byte(strings.Replace(string(raw), `"delete-checkpoint"`, "\"delete\\u002dcheckpoint\"", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			sig := ed25519.Sign(key, frame(deleteCheckpointSignaturePurpose, variant))
			if _, err := VerifyDeleteCheckpoint(variant, sig, authority, now); err == nil {
				t.Fatal("non-canonical command accepted")
			}
		})
	}
}

// Execute, read and settlement purposes are disjoint from each other and from every other command that shares the
// stage, so a signed deletion can never be replayed as another command or the reverse.
func TestDeleteCheckpointPurposesAreDisjoint(t *testing.T) {
	command, key, authority, now := deleteCheckpointFixture(t)
	raw, execute, err := SignDeleteCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	read, err := SignDeleteCheckpointRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) == string(execute) {
		t.Fatal("read signature equals execute signature")
	}
	if _, err := VerifyDeleteCheckpointRead(raw, read, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	for name, check := range map[string]func() error{
		"execute signature as read":    func() error { _, err := VerifyDeleteCheckpointRead(raw, execute, authority, now); return err },
		"read signature as execute":    func() error { _, err := VerifyDeleteCheckpoint(raw, read, authority, now); return err },
		"read signature as settlement": func() error { _, err := VerifyDeleteCheckpointSettlement(raw, read, authority, now); return err },
		"delete-data purpose": func() error {
			_, err := VerifyDeleteCheckpoint(raw, ed25519.Sign(key, frame(deleteDataSignaturePurpose, raw)), authority, now)
			return err
		},
		"remove-image purpose": func() error {
			_, err := VerifyDeleteCheckpoint(raw, ed25519.Sign(key, frame(removeImageSignaturePurpose, raw)), authority, now)
			return err
		},
		"as a remove-image command": func() error {
			_, err := VerifyRemoveImage(raw, ed25519.Sign(key, frame(removeImageSignaturePurpose, raw)), authority, now)
			return err
		},
		"as a delete-data command": func() error {
			_, err := VerifyDeleteData(raw, ed25519.Sign(key, frame(deleteDataSignaturePurpose, raw)), authority, now)
			return err
		},
	} {
		if check() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := VerifyDeleteCheckpoint(raw, execute, authority, 54000); err == nil {
		t.Fatal("expired execute accepted")
	}
	if _, err := VerifyDeleteCheckpointSettlement(raw, execute, authority, 54000); err != nil {
		t.Fatal("settlement is not bound by the window:", err)
	}
}

func TestDeleteCheckpointRefusesMalformedHeaderAndPayload(t *testing.T) {
	cases := map[string]func(*DeleteCheckpointCommand){
		"remove-image format":     func(c *DeleteCheckpointCommand) { c.Format = RemoveImageFormat },
		"delete-data kind":        func(c *DeleteCheckpointCommand) { c.Kind = "delete-data" },
		"wrong stage":             func(c *DeleteCheckpointCommand) { c.Stage = "stage" },
		"slot one is delete-data": func(c *DeleteCheckpointCommand) { c.EffectNumber = "1" },
		"zero slot":               func(c *DeleteCheckpointCommand) { c.EffectNumber = "0" },
		"slot above thirty two":   func(c *DeleteCheckpointCommand) { c.EffectNumber = "33" },
		"bad set id":              func(c *DeleteCheckpointCommand) { c.Payload.RecoverySetID = "x" },
		"bad checkpoint id":       func(c *DeleteCheckpointCommand) { c.Payload.CheckpointID = "x" },
		"bad namespace id":        func(c *DeleteCheckpointCommand) { c.Payload.NamespaceID = "" },
		"bad source operation":    func(c *DeleteCheckpointCommand) { c.Payload.SourceOperationID = "x" },
		"short release":           func(c *DeleteCheckpointCommand) { c.Payload.ReleaseSHA256 = "abc" },
		"upper manifest":          func(c *DeleteCheckpointCommand) { c.Payload.ManifestSHA256 = strings.Repeat("A", 64) },
		"zero bytes":              func(c *DeleteCheckpointCommand) { c.Payload.ArchiveBytes = "0" },
		"bytes over the limit":    func(c *DeleteCheckpointCommand) { c.Payload.ArchiveBytes = "18782978049" },
		"leading zero bytes":      func(c *DeleteCheckpointCommand) { c.Payload.ArchiveBytes = "04096" },
		"zero format":             func(c *DeleteCheckpointCommand) { c.Payload.DataFormat = "0" },
		"zero fence":              func(c *DeleteCheckpointCommand) { c.Payload.WriterFence = "0" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			command, _, _, _ := deleteCheckpointFixture(t)
			mutate(&command)
			if _, err := DeleteCheckpointLogicalDigest(command); err == nil {
				t.Fatal("malformed delete-checkpoint command produced a digest")
			}
		})
	}
	for _, slot := range []string{"2", "17", "32"} {
		command, _, _, _ := deleteCheckpointFixture(t)
		command.EffectNumber = slot
		if _, err := DeleteCheckpointLogicalDigest(command); err != nil {
			t.Fatalf("slot %s refused: %v", slot, err)
		}
	}
	command, _, _, _ := deleteCheckpointFixture(t)
	command.Payload.ArchiveBytes = "18782978048"
	if _, err := DeleteCheckpointLogicalDigest(command); err != nil {
		t.Fatal("the largest checkpoint archive was refused:", err)
	}
}

// The logical digest binds every payload field, so a signed deletion of one checkpoint cannot be carried to another.
func TestDeleteCheckpointDigestBindsEveryPayloadField(t *testing.T) {
	command, _, _, _ := deleteCheckpointFixture(t)
	base, _ := DeleteCheckpointLogicalDigest(command)
	for name, mutate := range map[string]func(*DeleteCheckpointPayload){
		"set":        func(p *DeleteCheckpointPayload) { p.RecoverySetID = "c0000000-0000-4000-8000-0000000000e1" },
		"checkpoint": func(p *DeleteCheckpointPayload) { p.CheckpointID = "c0000000-0000-4000-8000-0000000000e2" },
		"namespace":  func(p *DeleteCheckpointPayload) { p.NamespaceID = "c0000000-0000-4000-8000-0000000000e3" },
		"source":     func(p *DeleteCheckpointPayload) { p.SourceOperationID = "c0000000-0000-4000-8000-0000000000e4" },
		"release":    func(p *DeleteCheckpointPayload) { p.ReleaseSHA256 = strings.Repeat("7", 64) },
		"manifest":   func(p *DeleteCheckpointPayload) { p.ManifestSHA256 = strings.Repeat("6", 64) },
		"bytes":      func(p *DeleteCheckpointPayload) { p.ArchiveBytes = "4097" },
		"format":     func(p *DeleteCheckpointPayload) { p.DataFormat = "2" },
		"fence":      func(p *DeleteCheckpointPayload) { p.WriterFence = "4" },
	} {
		other := command
		mutate(&other.Payload)
		changed, err := DeleteCheckpointLogicalDigest(other)
		if err != nil || changed == base {
			t.Fatalf("%s is not bound by the logical digest", name)
		}
	}
	removal, _, _, _ := removeImageFixture(t)
	removalDigest, _ := RemoveImageLogicalDigest(removal)
	if removalDigest == base {
		t.Fatal("a removal and a checkpoint deletion share a logical digest")
	}
}
