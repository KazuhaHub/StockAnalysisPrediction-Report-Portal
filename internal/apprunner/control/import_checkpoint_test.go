package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func importCheckpointFixture(t *testing.T) (ImportCheckpointCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	command := ImportCheckpointCommand{
		Format: ImportCheckpointFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: checkpoint.InstallationID,
		OperationID:    "b0000000-0000-4000-8000-00000000000b", TargetGeneration: "2",
		Stage: "restore_data", EffectNumber: "1",
		EffectID: "c0000000-0000-4000-8000-00000000000c", Kind: "import-checkpoint",
		InstallationFence: "2",
		Payload: ImportCheckpointPayload{
			GrantID:       "e0000000-0000-4000-8000-00000000000e",
			RecoverySetID: "d0000000-0000-4000-8000-00000000000d",
			CheckpointID:  checkpoint.Payload.CheckpointID, NamespaceID: checkpoint.Payload.NamespaceID,
			ReleaseSHA256: checkpoint.Payload.ReleaseSHA256, ManifestSHA256: strings.Repeat("d", 64),
			ArchiveBytes: "4096", DataFormat: checkpoint.Payload.DataFormat, WriterFence: checkpoint.Payload.WriterFence,
			ProjectID: "5201", QuotaBytes: "1048576", QuotaInodes: "1024",
			SourceProjectID: checkpoint.Payload.ProjectID, SourceNamespaceInode: checkpoint.Payload.NamespaceInode,
			SourceDevice:         checkpoint.Payload.Device,
			SourceFilesystemUUID: "f1000000-0000-4000-8000-0000000000f1", SourceRootInode: "64",
			CheckpointFilesystemUUID: "f2000000-0000-4000-8000-0000000000f2", CheckpointRootInode: "65",
		},
	}
	digest, err := ImportCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

// resignImport re-digests and signs a mutated command so a test exercises the
// logical validation rather than the digest or signature.
func resignImport(t *testing.T, c ImportCheckpointCommand, key ed25519.PrivateKey, authority Authority, now int64) error {
	t.Helper()
	digest, err := ImportCheckpointLogicalDigest(c)
	if err != nil {
		return err
	}
	c.PayloadSHA256 = digest
	_, _, err = SignImportCheckpoint(c, key, authority, now)
	return err
}

func TestVerifyImportCheckpointRoundTripAndBinding(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	raw, signature, err := SignImportCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyImportCheckpoint(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("import command round trip", err)
	}
	base, err := ImportCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ImportCheckpointCommand){
		"grant":          func(c *ImportCheckpointCommand) { c.Payload.GrantID = "e0000000-0000-4000-8000-00000000001e" },
		"recovery set":   func(c *ImportCheckpointCommand) { c.Payload.RecoverySetID = "d0000000-0000-4000-8000-00000000001d" },
		"checkpoint":     func(c *ImportCheckpointCommand) { c.Payload.CheckpointID = "60000000-0000-4000-8000-000000000016" },
		"namespace":      func(c *ImportCheckpointCommand) { c.Payload.NamespaceID = "70000000-0000-4000-8000-000000000017" },
		"release":        func(c *ImportCheckpointCommand) { c.Payload.ReleaseSHA256 = strings.Repeat("f", 64) },
		"manifest":       func(c *ImportCheckpointCommand) { c.Payload.ManifestSHA256 = strings.Repeat("1", 64) },
		"archive":        func(c *ImportCheckpointCommand) { c.Payload.ArchiveBytes = "8192" },
		"data format":    func(c *ImportCheckpointCommand) { c.Payload.DataFormat = "2" },
		"writer fence":   func(c *ImportCheckpointCommand) { c.Payload.WriterFence = "2" },
		"project":        func(c *ImportCheckpointCommand) { c.Payload.ProjectID = "5202" },
		"quota bytes":    func(c *ImportCheckpointCommand) { c.Payload.QuotaBytes = "2097152" },
		"quota inodes":   func(c *ImportCheckpointCommand) { c.Payload.QuotaInodes = "2048" },
		"source project": func(c *ImportCheckpointCommand) { c.Payload.SourceProjectID = "4102" },
		"source inode":   func(c *ImportCheckpointCommand) { c.Payload.SourceNamespaceInode = "12" },
		"source device":  func(c *ImportCheckpointCommand) { c.Payload.SourceDevice = "12" },
		"source fs": func(c *ImportCheckpointCommand) {
			c.Payload.SourceFilesystemUUID = "f1000000-0000-4000-8000-0000000000f9"
		},
		"source root": func(c *ImportCheckpointCommand) { c.Payload.SourceRootInode = "66" },
		"checkpoint fs": func(c *ImportCheckpointCommand) {
			c.Payload.CheckpointFilesystemUUID = "f2000000-0000-4000-8000-0000000000f9"
		},
		"checkpoint root":   func(c *ImportCheckpointCommand) { c.Payload.CheckpointRootInode = "67" },
		"target generation": func(c *ImportCheckpointCommand) { c.TargetGeneration = "3" },
		"operation":         func(c *ImportCheckpointCommand) { c.OperationID = "b0000000-0000-4000-8000-00000000001b" },
		"effect number":     func(c *ImportCheckpointCommand) { c.EffectNumber = "2" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			digest, err := ImportCheckpointLogicalDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base {
				t.Fatal("field is not bound by the logical digest")
			}
		})
	}
}

func TestVerifyImportCheckpointRejectsStaleDigestAndSignature(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	command.Payload.GrantID = "e0000000-0000-4000-8000-00000000001e"
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(key, frame(importCheckpointSignaturePurpose, raw))
	if _, err := VerifyImportCheckpoint(raw, signature, authority, now); err == nil {
		t.Fatal("payload digest that does not match the payload accepted")
	}
	command, _, _, _ = importCheckpointFixture(t)
	raw, goodSignature, err := SignImportCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	// A restore signature over the same bytes must not verify as an import.
	foreign := ed25519.Sign(key, frame(restoreSignaturePurpose, raw))
	if _, err := VerifyImportCheckpoint(raw, foreign, authority, now); err == nil {
		t.Fatal("signature with another purpose accepted")
	}
	changed := append([]byte(nil), raw...)
	changed[len(changed)/2] ^= 1
	if _, err := VerifyImportCheckpoint(changed, goodSignature, authority, now); err == nil {
		t.Fatal("changed import bytes accepted")
	}
}

func TestVerifyImportCheckpointCanonicalJSON(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(raw []byte) ([]byte, []byte) {
		return raw, ed25519.Sign(key, frame(importCheckpointSignaturePurpose, raw))
	}
	if _, err := VerifyImportCheckpoint(raw, ed25519.Sign(key, frame(importCheckpointSignaturePurpose, raw)), authority, now); err != nil {
		t.Fatal(err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", " "); err != nil {
		t.Fatal(err)
	}
	for name, variant := range map[string][]byte{
		"whitespace": indented.Bytes(),
		"trailing":   append(append([]byte(nil), raw...), '\n'),
		"duplicate":  bytes.Replace(raw, []byte(`"stage":"restore_data"`), []byte(`"stage":"restore_data","stage":"restore_data"`), 1),
		"missing":    bytes.Replace(raw, []byte(`"format":"`+ImportCheckpointFormat+`",`), nil, 1),
		"escaped":    bytes.Replace(raw, []byte(`"restore_data"`), []byte(`"restore\u005fdata"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			body, signature := sign(variant)
			if _, err := VerifyImportCheckpoint(body, signature, authority, now); err == nil {
				t.Fatal("non-canonical command accepted")
			}
		})
	}
}

func TestVerifyImportCheckpointRejectsUnknownFields(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for name, variant := range map[string][]byte{
		"top level": bytes.Replace(raw, []byte(`"payload":{`), []byte(`"path":"/etc","payload":{`), 1),
		"payload":   bytes.Replace(raw, []byte(`"payload":{`), []byte(`"payload":{"path":"/etc",`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			signature := ed25519.Sign(key, frame(importCheckpointSignaturePurpose, variant))
			if _, err := VerifyImportCheckpoint(variant, signature, authority, now); err == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
}

func TestVerifyImportCheckpointStageAndKind(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	for name, mutate := range map[string]func(*ImportCheckpointCommand){
		"snapshot stage": func(c *ImportCheckpointCommand) { c.Stage = "snapshot" },
		"download stage": func(c *ImportCheckpointCommand) { c.Stage = "download" },
		"restore kind":   func(c *ImportCheckpointCommand) { c.Kind = "restore" },
		"empty kind":     func(c *ImportCheckpointCommand) { c.Kind = "" },
		"format":         func(c *ImportCheckpointCommand) { c.Format = RestoreFormat },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if _, err := ImportCheckpointLogicalDigest(changed); err == nil {
				t.Fatal("invalid import action admitted")
			}
		})
	}
	if err := resignImport(t, command, key, authority, now); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyImportCheckpointEffectBoundAndPayloadLimits(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	command.EffectNumber = "32"
	if err := resignImport(t, command, key, authority, now); err != nil {
		t.Fatalf("effect 32 refused: %v", err)
	}
	for name, mutate := range map[string]func(*ImportCheckpointCommand){
		"effect 33":           func(c *ImportCheckpointCommand) { c.EffectNumber = "33" },
		"effect zero":         func(c *ImportCheckpointCommand) { c.EffectNumber = "0" },
		"nil grant":           func(c *ImportCheckpointCommand) { c.Payload.GrantID = "00000000-0000-0000-0000-000000000000" },
		"archive over bound":  func(c *ImportCheckpointCommand) { c.Payload.ArchiveBytes = "536870913" },
		"archive zero":        func(c *ImportCheckpointCommand) { c.Payload.ArchiveBytes = "0" },
		"project zero":        func(c *ImportCheckpointCommand) { c.Payload.ProjectID = "0" },
		"project over 32 bit": func(c *ImportCheckpointCommand) { c.Payload.ProjectID = "4294967296" },
		"quota unaligned":     func(c *ImportCheckpointCommand) { c.Payload.QuotaBytes = "1000" },
		"quota zero":          func(c *ImportCheckpointCommand) { c.Payload.QuotaBytes = "0" },
		"inodes zero":         func(c *ImportCheckpointCommand) { c.Payload.QuotaInodes = "0" },
		"leading zero":        func(c *ImportCheckpointCommand) { c.Payload.DataFormat = "01" },
		"source inode text":   func(c *ImportCheckpointCommand) { c.Payload.SourceNamespaceInode = "0x10" },
		"source fs invalid": func(c *ImportCheckpointCommand) {
			c.Payload.SourceFilesystemUUID = "F1000000-0000-4000-8000-0000000000F1"
		},
		"same roots": func(c *ImportCheckpointCommand) {
			c.Payload.CheckpointFilesystemUUID = c.Payload.SourceFilesystemUUID
			c.Payload.CheckpointRootInode = c.Payload.SourceRootInode
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if _, err := ImportCheckpointLogicalDigest(changed); err == nil {
				t.Fatal("invalid import payload admitted")
			}
		})
	}
}

func TestVerifyImportCheckpointExpiryAndForeignDeployment(t *testing.T) {
	command, key, authority, now := importCheckpointFixture(t)
	raw, signature, err := SignImportCheckpoint(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyImportCheckpoint(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired import accepted")
	}
	if _, err := VerifyImportCheckpoint(raw, signature, authority, 28000); err == nil {
		t.Fatal("import accepted before issue")
	}
	if verified, err := VerifyImportCheckpointSettlement(raw, signature, authority, 55000); err != nil || verified.Command() != command {
		t.Fatal("admitted import could not settle after expiry", err)
	}
	if _, err := VerifyImportCheckpointSettlement(raw, signature, authority, 28000); err == nil {
		t.Fatal("import settled before issue")
	}
	foreign := authority
	foreign.DeploymentID = "10000000-0000-4000-8000-0000000000ff"
	if _, err := VerifyImportCheckpoint(raw, signature, foreign, now); err == nil {
		t.Fatal("import verified for a foreign runner deployment")
	}
	foreign = authority
	foreign.ControllerEpoch = "20000000-0000-4000-8000-0000000000ff"
	if _, err := VerifyImportCheckpoint(raw, signature, foreign, now); err == nil {
		t.Fatal("import verified for a foreign controller epoch")
	}
	foreign = authority
	foreign.ControlRevision++
	if _, err := VerifyImportCheckpoint(raw, signature, foreign, now); err == nil {
		t.Fatal("import verified for a changed control revision")
	}
	command.ExpiresMS = "60001"
	if err := resignImport(t, command, key, authority, now); err == nil {
		t.Fatal("import lifetime beyond 30 seconds accepted")
	}
}
