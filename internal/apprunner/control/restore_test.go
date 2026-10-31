package control

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func restoreFixture(t *testing.T) (RestoreCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	command := RestoreCommand{
		Format: RestoreFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: checkpoint.InstallationID,
		OperationID:    "b0000000-0000-4000-8000-00000000000b", TargetGeneration: "2",
		Stage: "restore_data", EffectNumber: "1",
		EffectID: "c0000000-0000-4000-8000-00000000000c", Kind: "restore",
		InstallationFence: "2",
		Payload: RestorePayload{
			RecoverySetID: "d0000000-0000-4000-8000-00000000000d",
			CheckpointID:  checkpoint.Payload.CheckpointID, NamespaceID: checkpoint.Payload.NamespaceID,
			ReleaseSHA256: checkpoint.Payload.ReleaseSHA256, ManifestSHA256: strings.Repeat("d", 64),
			ArchiveBytes: "4096", ProjectID: checkpoint.Payload.ProjectID,
			NamespaceInode: checkpoint.Payload.NamespaceInode, Device: checkpoint.Payload.Device,
			DataFormat: checkpoint.Payload.DataFormat, WriterFence: checkpoint.Payload.WriterFence,
			SourceCheckpointOperationID: checkpoint.OperationID,
			SourceCheckpointEffectID:    checkpoint.EffectID,
			SourceCheckpointReceiptSHA:  strings.Repeat("e", 64),
		},
	}
	digest, err := RestoreLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestRestoreCommandRoundTripAndBinding(t *testing.T) {
	command, key, authority, now := restoreFixture(t)
	raw, signature, err := SignRestore(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyRestore(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("restore command round trip", err)
	}
	for name, mutate := range map[string]func(*RestoreCommand){
		"stage":        func(c *RestoreCommand) { c.Stage = "snapshot" },
		"kind":         func(c *RestoreCommand) { c.Kind = "snapshot" },
		"recovery set": func(c *RestoreCommand) { c.Payload.RecoverySetID = "d0000000-0000-4000-8000-00000000001d" },
		"checkpoint":   func(c *RestoreCommand) { c.Payload.CheckpointID = "60000000-0000-4000-8000-000000000016" },
		"namespace":    func(c *RestoreCommand) { c.Payload.NamespaceID = "70000000-0000-4000-8000-000000000017" },
		"release":      func(c *RestoreCommand) { c.Payload.ReleaseSHA256 = strings.Repeat("f", 64) },
		"manifest":     func(c *RestoreCommand) { c.Payload.ManifestSHA256 = strings.Repeat("1", 64) },
		"archive":      func(c *RestoreCommand) { c.Payload.ArchiveBytes = "8192" },
		"project":      func(c *RestoreCommand) { c.Payload.ProjectID = "4102" },
		"inode":        func(c *RestoreCommand) { c.Payload.NamespaceInode = "12" },
		"device":       func(c *RestoreCommand) { c.Payload.Device = "12" },
		"data format":  func(c *RestoreCommand) { c.Payload.DataFormat = "2" },
		"writer fence": func(c *RestoreCommand) { c.Payload.WriterFence = "2" },
		"source operation": func(c *RestoreCommand) {
			c.Payload.SourceCheckpointOperationID = "b0000000-0000-4000-8000-00000000001b"
		},
		"source effect":      func(c *RestoreCommand) { c.Payload.SourceCheckpointEffectID = "c0000000-0000-4000-8000-00000000001c" },
		"source receipt":     func(c *RestoreCommand) { c.Payload.SourceCheckpointReceiptSHA = strings.Repeat("2", 64) },
		"target generation":  func(c *RestoreCommand) { c.TargetGeneration = "3" },
		"installation fence": func(c *RestoreCommand) { c.InstallationFence = "3" },
		"effect number":      func(c *RestoreCommand) { c.EffectNumber = "2"; c.EffectID = "c0000000-0000-4000-8000-00000000002c" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if name == "stage" || name == "kind" {
				if _, err := RestoreLogicalDigest(changed); err == nil {
					t.Fatal("invalid restore action admitted")
				}
				return
			}
			if name == "source effect" && changed.Payload.SourceCheckpointEffectID == command.EffectID {
				t.Fatal("fixture reused restore effect as checkpoint source")
			}
			digest, err := RestoreLogicalDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			changed.PayloadSHA256 = digest
			raw, signature, err := SignRestore(changed, key, authority, now)
			if err != nil || len(raw) == 0 || len(signature) != ed25519.SignatureSize {
				t.Fatal("changed restore could not be signed", err)
			}
		})
	}
}

func TestRestoreCommandRejectsAuthorityAndTime(t *testing.T) {
	command, key, authority, now := restoreFixture(t)
	raw, signature, err := SignRestore(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	other := authority
	other.ControlRevision++
	if _, err := VerifyRestore(raw, signature, other, now); err == nil {
		t.Fatal("changed restore authority accepted")
	}
	if _, err := VerifyRestore(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired restore accepted")
	}
	changed := append([]byte(nil), raw...)
	changed[len(changed)/2] ^= 1
	if _, err := VerifyRestore(changed, signature, authority, now); err == nil {
		t.Fatal("changed restore bytes accepted")
	}
}
