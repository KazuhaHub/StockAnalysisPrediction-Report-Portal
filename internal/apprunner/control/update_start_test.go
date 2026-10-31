package control

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func updateStartFixture(t *testing.T) (UpdateStartCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	command := UpdateStartCommand{
		Format: UpdateStartFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID:   checkpoint.InstallationID,
		OperationID:      checkpoint.OperationID,
		TargetGeneration: "2", Stage: "start", EffectNumber: "1",
		EffectID: "f0000000-0000-4000-8000-00000000000f",
		Kind:     "update-start", InstallationFence: "2",
		Payload: UpdateStartPayload{
			ResourceID:                    "10000000-0000-4000-8000-000000000010",
			NamespaceID:                   checkpoint.Payload.NamespaceID,
			SourceOperationID:             checkpoint.Payload.SourceOperationID,
			SourceStartEffectID:           checkpoint.Payload.SourceStartEffectID,
			SourceStartReceiptSHA256:      strings.Repeat("1", 64),
			SourceDataEffectID:            checkpoint.Payload.SourceDataEffectID,
			SourceDataReceiptSHA256:       strings.Repeat("2", 64),
			SourceCheckpointOperationID:   checkpoint.OperationID,
			SourceCheckpointEffectID:      checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: strings.Repeat("3", 64),
			CheckpointID:                  checkpoint.Payload.CheckpointID,
			CheckpointManifestSHA256:      strings.Repeat("4", 64),
			CheckpointArchiveBytes:        "4096",
			SourceReleaseSHA256:           checkpoint.Payload.ReleaseSHA256,
			TargetReleaseSHA256:           strings.Repeat("5", 64),
			SourceGeneration:              "1", TargetPolicyRevision: "2",
			ProjectID: "4101", NamespaceInode: "10", Device: "11",
			QuotaBytes: "268435456", QuotaInodes: "65536",
			RootFilesystemUUID: "00000000-0000-4000-8000-000000000499",
			RootInode:          "12", DataFormat: "1", SourceWriterFence: "1",
			TargetWriterFence: "2", Platform: "linux/amd64",
			SandboxProfile: CandidateSandboxProfile,
		},
	}
	digest, err := UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestUpdateStartCommandRoundTripAndBinding(t *testing.T) {
	command, key, authority, now := updateStartFixture(t)
	raw, signature, err := SignUpdateStart(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyUpdateStart(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("update-start round trip", err)
	}
	for name, mutate := range map[string]func(*UpdateStartCommand){
		"stage":                func(c *UpdateStartCommand) { c.Stage = "snapshot" },
		"kind":                 func(c *UpdateStartCommand) { c.Kind = "start-candidate" },
		"resource":             func(c *UpdateStartCommand) { c.Payload.ResourceID = "10000000-0000-4000-8000-000000000020" },
		"namespace":            func(c *UpdateStartCommand) { c.Payload.NamespaceID = "70000000-0000-4000-8000-000000000017" },
		"source operation":     func(c *UpdateStartCommand) { c.Payload.SourceOperationID = "80000000-0000-4000-8000-000000000018" },
		"source start":         func(c *UpdateStartCommand) { c.Payload.SourceStartEffectID = "90000000-0000-4000-8000-000000000019" },
		"source start receipt": func(c *UpdateStartCommand) { c.Payload.SourceStartReceiptSHA256 = strings.Repeat("6", 64) },
		"source data":          func(c *UpdateStartCommand) { c.Payload.SourceDataEffectID = "a0000000-0000-4000-8000-00000000001a" },
		"source data receipt":  func(c *UpdateStartCommand) { c.Payload.SourceDataReceiptSHA256 = strings.Repeat("7", 64) },
		"checkpoint op": func(c *UpdateStartCommand) {
			c.Payload.SourceCheckpointOperationID = "40000000-0000-4000-8000-000000000014"
		},
		"checkpoint effect": func(c *UpdateStartCommand) {
			c.Payload.SourceCheckpointEffectID = "50000000-0000-4000-8000-000000000015"
		},
		"checkpoint receipt": func(c *UpdateStartCommand) { c.Payload.SourceCheckpointReceiptSHA256 = strings.Repeat("8", 64) },
		"checkpoint id":      func(c *UpdateStartCommand) { c.Payload.CheckpointID = "60000000-0000-4000-8000-000000000016" },
		"manifest":           func(c *UpdateStartCommand) { c.Payload.CheckpointManifestSHA256 = strings.Repeat("9", 64) },
		"archive":            func(c *UpdateStartCommand) { c.Payload.CheckpointArchiveBytes = "8192" },
		"source release":     func(c *UpdateStartCommand) { c.Payload.SourceReleaseSHA256 = strings.Repeat("a", 64) },
		"target release":     func(c *UpdateStartCommand) { c.Payload.TargetReleaseSHA256 = strings.Repeat("b", 64) },
		"source generation":  func(c *UpdateStartCommand) { c.Payload.SourceGeneration = "2" },
		"target generation":  func(c *UpdateStartCommand) { c.TargetGeneration = "3" },
		"target policy":      func(c *UpdateStartCommand) { c.Payload.TargetPolicyRevision = "3" },
		"project":            func(c *UpdateStartCommand) { c.Payload.ProjectID = "4102" },
		"namespace inode":    func(c *UpdateStartCommand) { c.Payload.NamespaceInode = "13" },
		"device":             func(c *UpdateStartCommand) { c.Payload.Device = "13" },
		"quota bytes":        func(c *UpdateStartCommand) { c.Payload.QuotaBytes = "536870912" },
		"quota inodes":       func(c *UpdateStartCommand) { c.Payload.QuotaInodes = "131072" },
		"root uuid":          func(c *UpdateStartCommand) { c.Payload.RootFilesystemUUID = "00000000-0000-4000-8000-000000000498" },
		"root inode":         func(c *UpdateStartCommand) { c.Payload.RootInode = "13" },
		"data format":        func(c *UpdateStartCommand) { c.Payload.DataFormat = "2" },
		"source writer":      func(c *UpdateStartCommand) { c.Payload.SourceWriterFence = "2" },
		"target writer":      func(c *UpdateStartCommand) { c.Payload.TargetWriterFence = "3" },
		"platform":           func(c *UpdateStartCommand) { c.Payload.Platform = "linux/arm64" },
		"sandbox":            func(c *UpdateStartCommand) { c.Payload.SandboxProfile = WritableCandidateSandboxProfile },
		"installation fence": func(c *UpdateStartCommand) { c.InstallationFence = "3" },
		"effect number":      func(c *UpdateStartCommand) { c.EffectNumber = "2"; c.EffectID = "f0000000-0000-4000-8000-00000000001f" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if name == "stage" || name == "kind" || name == "checkpoint op" || name == "source generation" || name == "target generation" || name == "source writer" {
				if _, err := UpdateStartLogicalDigest(changed); err == nil {
					t.Fatal("invalid update start relation admitted")
				}
				return
			}
			digest, err := UpdateStartLogicalDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			changed.PayloadSHA256 = digest
			raw, signature, err := SignUpdateStart(changed, key, authority, now)
			if err != nil || len(raw) == 0 || len(signature) != ed25519.SignatureSize {
				t.Fatal("changed update start could not be signed", err)
			}
		})
	}
}

func TestUpdateStartCommandRejectsAuthorityAndTime(t *testing.T) {
	command, key, authority, now := updateStartFixture(t)
	raw, signature, err := SignUpdateStart(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	other := authority
	other.ControlRevision++
	if _, err := VerifyUpdateStart(raw, signature, other, now); err == nil {
		t.Fatal("changed update-start authority accepted")
	}
	if _, err := VerifyUpdateStart(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired update-start accepted")
	}
	changed := append([]byte(nil), raw...)
	changed[len(changed)/2] ^= 1
	if _, err := VerifyUpdateStart(changed, signature, authority, now); err == nil {
		t.Fatal("changed update-start bytes accepted")
	}
}
