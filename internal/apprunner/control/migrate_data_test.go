package control

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

func migrateDataFixture(t *testing.T) (MigrateDataCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	checkpoint, key, authority, now := checkpointFixture(t)
	command := MigrateDataCommand{CommandHeader: CommandHeader{
		Format: MigrateDataFormat, DeploymentID: authority.DeploymentID,
		ControllerEpoch: authority.ControllerEpoch, ControlRevision: "3",
		InstallationID: checkpoint.InstallationID,
		OperationID:    checkpoint.OperationID, TargetGeneration: "2",
		Stage: "migrate", EffectNumber: "2",
		EffectID: "c0000000-0000-4000-8000-00000000000c", Kind: "migrate-data",
		InstallationFence: "2",
	}, Payload: MigrateDataPayload{
		NamespaceID: checkpoint.Payload.NamespaceID, ResourceID: "d1000000-0000-4000-8000-0000000000d1",
		CheckpointID: checkpoint.Payload.CheckpointID, CheckpointEffectID: checkpoint.EffectID,
		CheckpointReceiptSHA256: strings.Repeat("e", 64),
		RemoveCodeEffectID:      "e1000000-0000-4000-8000-0000000000e1", RemoveCodeReceiptSHA256: strings.Repeat("f", 64),
		SourceReleaseSHA256: checkpoint.Payload.ReleaseSHA256, TargetReleaseSHA256: strings.Repeat("9", 64),
		SourceSchema: "2", TargetSchema: "3",
		CheckpointWriterFence: "5", WriterFence: "6", StartWriterFence: "7",
		ProjectID: checkpoint.Payload.ProjectID, NamespaceInode: checkpoint.Payload.NamespaceInode, Device: checkpoint.Payload.Device,
		QuotaBytes: "1048576", QuotaInodes: "1024", WallClockSeconds: "600",
		EntrypointSHA256: strings.Repeat("8", 64),
	}}
	digest, err := MigrateDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func resignMigrate(t *testing.T, c MigrateDataCommand, key ed25519.PrivateKey, authority Authority, now int64) error {
	t.Helper()
	digest, err := MigrateDataLogicalDigest(c)
	if err != nil {
		return err
	}
	c.PayloadSHA256 = digest
	_, _, err = SignMigrateData(c, key, authority, now)
	return err
}

func TestVerifyMigrateDataCanonical(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	raw, signature, err := SignMigrateData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyMigrateData(raw, signature, authority, now)
	if err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", " "); err != nil {
		t.Fatal(err)
	}
	for name, variant := range map[string][]byte{
		"whitespace": indented.Bytes(),
		"trailing":   append(append([]byte(nil), raw...), '\n'),
		"duplicate":  bytes.Replace(raw, []byte(`"stage":"migrate"`), []byte(`"stage":"migrate","stage":"migrate"`), 1),
		"escaped":    bytes.Replace(raw, []byte(`"migrate"`), []byte(`"migr\u0061te"`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			signature := ed25519.Sign(key, frame(migrateDataSignaturePurpose, variant))
			if _, err := VerifyMigrateData(variant, signature, authority, now); err == nil {
				t.Fatal("non-canonical command accepted")
			}
		})
	}
	foreign := ed25519.Sign(key, frame(importCheckpointSignaturePurpose, raw))
	if _, err := VerifyMigrateData(raw, foreign, authority, now); err == nil {
		t.Fatal("signature with another purpose accepted")
	}
}

func TestVerifyMigrateDataUnknownField(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	raw, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	for name, variant := range map[string][]byte{
		"top level": bytes.Replace(raw, []byte(`"payload":{`), []byte(`"path":"/etc","payload":{`), 1),
		"payload":   bytes.Replace(raw, []byte(`"payload":{`), []byte(`"payload":{"command":"sh",`), 1),
	} {
		t.Run(name, func(t *testing.T) {
			signature := ed25519.Sign(key, frame(migrateDataSignaturePurpose, variant))
			if _, err := VerifyMigrateData(variant, signature, authority, now); err == nil {
				t.Fatal("unknown field accepted")
			}
		})
	}
}

func TestVerifyMigrateDataStageKind(t *testing.T) {
	command, _, _, _ := migrateDataFixture(t)
	for name, mutate := range map[string]func(*MigrateDataCommand){
		"stage":        func(c *MigrateDataCommand) { c.Stage = "start" },
		"kind":         func(c *MigrateDataCommand) { c.Kind = "restore" },
		"format":       func(c *MigrateDataCommand) { c.Format = ImportCheckpointFormat },
		"effect one":   func(c *MigrateDataCommand) { c.EffectNumber = "1" },
		"effect three": func(c *MigrateDataCommand) { c.EffectNumber = "3" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if _, err := MigrateDataLogicalDigest(changed); err == nil {
				t.Fatal("invalid migrate-data action admitted")
			}
		})
	}
}

func TestVerifyMigrateDataFenceOrder(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	if err := resignMigrate(t, command, key, authority, now); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*MigrateDataPayload){
		"writer equals checkpoint": func(p *MigrateDataPayload) { p.WriterFence = "5" },
		"writer below checkpoint":  func(p *MigrateDataPayload) { p.WriterFence = "4" },
		"start equals writer":      func(p *MigrateDataPayload) { p.StartWriterFence = "6" },
		"start below writer":       func(p *MigrateDataPayload) { p.StartWriterFence = "5" },
		"schema not increasing":    func(p *MigrateDataPayload) { p.TargetSchema = "2" },
		"schema zero":              func(p *MigrateDataPayload) { p.SourceSchema = "0" },
		"wall clock above 600":     func(p *MigrateDataPayload) { p.WallClockSeconds = "601" },
		"wall clock zero":          func(p *MigrateDataPayload) { p.WallClockSeconds = "0" },
		"quota unaligned":          func(p *MigrateDataPayload) { p.QuotaBytes = "1000" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed.Payload)
			if _, err := MigrateDataLogicalDigest(changed); err == nil {
				t.Fatal("invalid migrate-data payload admitted")
			}
		})
	}
	base, _ := MigrateDataLogicalDigest(command)
	for name, mutate := range map[string]func(*MigrateDataPayload){
		"namespace": func(p *MigrateDataPayload) { p.NamespaceID = "70000000-0000-4000-8000-000000000017" },
		"resource":  func(p *MigrateDataPayload) { p.ResourceID = "d1000000-0000-4000-8000-0000000000d2" },
		"receipt":   func(p *MigrateDataPayload) { p.CheckpointReceiptSHA256 = strings.Repeat("1", 64) },
		"target":    func(p *MigrateDataPayload) { p.TargetReleaseSHA256 = strings.Repeat("2", 64) },
		"entry":     func(p *MigrateDataPayload) { p.EntrypointSHA256 = strings.Repeat("3", 64) },
		"start":     func(p *MigrateDataPayload) { p.StartWriterFence = "9" },
		"wall":      func(p *MigrateDataPayload) { p.WallClockSeconds = "60" },
	} {
		changed := command
		mutate(&changed.Payload)
		if digest, err := MigrateDataLogicalDigest(changed); err != nil || digest == base {
			t.Fatalf("%s not bound by the digest: %v", name, err)
		}
	}
}

func TestVerifyMigrateDataExpiry(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	raw, signature, err := SignMigrateData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMigrateData(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired command accepted")
	}
	if _, err := VerifyMigrateData(raw, signature, authority, 28000); err == nil {
		t.Fatal("command accepted before issue")
	}
	if _, err := VerifyMigrateDataSettlement(raw, signature, authority, 55000); err != nil {
		t.Fatal("admitted command could not settle after expiry", err)
	}
	command.ExpiresMS = "60001"
	if err := resignMigrate(t, command, key, authority, now); err == nil {
		t.Fatal("lifetime beyond 30 seconds accepted")
	}
}

func TestVerifyMigrateDataForeignDeployment(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	raw, signature, err := SignMigrateData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Authority){
		"deployment": func(a *Authority) { a.DeploymentID = "10000000-0000-4000-8000-0000000000ff" },
		"epoch":      func(a *Authority) { a.ControllerEpoch = "20000000-0000-4000-8000-0000000000ff" },
		"revision":   func(a *Authority) { a.ControlRevision++ },
	} {
		foreign := authority
		mutate(&foreign)
		if _, err := VerifyMigrateData(raw, signature, foreign, now); err == nil {
			t.Fatalf("verified for a foreign %s", name)
		}
	}
}

func TestMigrateReadSignatureRefusedAsExecute(t *testing.T) {
	command, key, authority, now := migrateDataFixture(t)
	raw, executeSignature, err := SignMigrateData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	readSignature, err := SignMigrateDataRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyMigrateDataRead(raw, readSignature, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	if _, err := VerifyMigrateDataRead(raw, executeSignature, authority, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	if _, err := VerifyMigrateData(raw, readSignature, authority, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := VerifyMigrateDataSettlement(raw, readSignature, authority, now); err == nil {
		t.Fatal("a read signature was accepted as a settlement")
	}
}

func discardMigrationFixture(t *testing.T) (DiscardMigrationCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	migrate, key, authority, now := migrateDataFixture(t)
	command := DiscardMigrationCommand{CommandHeader: migrate.CommandHeader, Payload: DiscardMigrationPayload{
		NamespaceID: migrate.Payload.NamespaceID, ResourceID: migrate.Payload.ResourceID,
		MigrateEffectID: migrate.EffectID, MigratePayloadSHA256: migrate.PayloadSHA256,
		TargetReleaseSHA256: migrate.Payload.TargetReleaseSHA256,
	}}
	command.Format = DiscardMigrationFormat
	command.EffectNumber, command.Kind = "3", "discard-migration"
	command.EffectID = "c1000000-0000-4000-8000-0000000000c1"
	digest, err := DiscardMigrationLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestVerifyDiscardMigrationCanonicalAndBound(t *testing.T) {
	command, key, authority, now := discardMigrationFixture(t)
	raw, signature, err := SignDiscardMigration(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := VerifyDiscardMigration(raw, signature, authority, now); err != nil || v.Command() != command {
		t.Fatal("round trip", err)
	}
	spaced := bytes.Replace(raw, []byte(`"stage":`), []byte(`"stage": `), 1)
	if _, err := VerifyDiscardMigration(spaced, ed25519.Sign(key, frame(discardMigrationSignaturePurpose, spaced)), authority, now); err == nil {
		t.Fatal("non-canonical command accepted")
	}
	unknown := bytes.Replace(raw, []byte(`"payload":{`), []byte(`"payload":{"force":"1",`), 1)
	if _, err := VerifyDiscardMigration(unknown, ed25519.Sign(key, frame(discardMigrationSignaturePurpose, unknown)), authority, now); err == nil {
		t.Fatal("unknown field accepted")
	}
	foreign := ed25519.Sign(key, frame(migrateDataSignaturePurpose, raw))
	if _, err := VerifyDiscardMigration(raw, foreign, authority, now); err == nil {
		t.Fatal("migrate signature accepted as a discard")
	}
	base, _ := DiscardMigrationLogicalDigest(command)
	changed := command
	changed.Payload.MigrateEffectID = "c2000000-0000-4000-8000-0000000000c2"
	if digest, err := DiscardMigrationLogicalDigest(changed); err != nil || digest == base {
		t.Fatal("migrate effect not bound")
	}
	for name, mutate := range map[string]func(*DiscardMigrationCommand){
		"effect two":  func(c *DiscardMigrationCommand) { c.EffectNumber = "2" },
		"kind":        func(c *DiscardMigrationCommand) { c.Kind = "migrate-data" },
		"stage":       func(c *DiscardMigrationCommand) { c.Stage = "start" },
		"same effect": func(c *DiscardMigrationCommand) { c.EffectID = c.Payload.MigrateEffectID },
	} {
		changed := command
		mutate(&changed)
		if _, err := DiscardMigrationLogicalDigest(changed); err == nil {
			t.Fatalf("%s admitted", name)
		}
	}
}

func TestVerifyDiscardMigrationExpiryReadAndForeign(t *testing.T) {
	command, key, authority, now := discardMigrationFixture(t)
	raw, signature, err := SignDiscardMigration(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDiscardMigration(raw, signature, authority, 55000); err == nil {
		t.Fatal("expired discard accepted")
	}
	if _, err := VerifyDiscardMigrationSettlement(raw, signature, authority, 55000); err != nil {
		t.Fatal(err)
	}
	read, err := SignDiscardMigrationRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDiscardMigrationRead(raw, read, authority, 55000); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyDiscardMigration(raw, read, authority, now); err == nil {
		t.Fatal("read signature accepted as execute")
	}
	foreign := authority
	foreign.DeploymentID = "10000000-0000-4000-8000-0000000000ff"
	if _, err := VerifyDiscardMigration(raw, signature, foreign, now); err == nil {
		t.Fatal("foreign deployment accepted")
	}
}

func TestRestoreReadPurposeRefusedAsExecute(t *testing.T) {
	checkpoint, key, authority, now := checkpointFixture(t)
	command := RestoreCommand{
		Format: RestoreFormat, DeploymentID: authority.DeploymentID, ControllerEpoch: authority.ControllerEpoch,
		ControlRevision: "3", InstallationID: checkpoint.InstallationID,
		OperationID: "b0000000-0000-4000-8000-00000000000b", TargetGeneration: "2", Stage: "restore_data",
		EffectNumber: "1", EffectID: "c0000000-0000-4000-8000-00000000000c", Kind: "restore", InstallationFence: "2",
		Payload: RestorePayload{
			RecoverySetID: "d0000000-0000-4000-8000-00000000000d", CheckpointID: checkpoint.Payload.CheckpointID,
			NamespaceID: checkpoint.Payload.NamespaceID, ReleaseSHA256: checkpoint.Payload.ReleaseSHA256,
			ManifestSHA256: strings.Repeat("d", 64), ArchiveBytes: "4096", ProjectID: "4101",
			NamespaceInode: "10", Device: "11", DataFormat: "1", WriterFence: "1",
			SourceCheckpointOperationID: checkpoint.OperationID, SourceCheckpointEffectID: checkpoint.EffectID,
			SourceCheckpointReceiptSHA: strings.Repeat("e", 64),
		},
	}
	digest, err := RestoreLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	raw, executeSignature, err := SignRestore(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	readSignature, err := SignRestoreRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRestoreRead(raw, readSignature, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	if _, err := VerifyRestoreRead(raw, executeSignature, authority, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	if _, err := VerifyRestore(raw, readSignature, authority, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := VerifyRestoreSettlement(raw, readSignature, authority, now); err == nil {
		t.Fatal("a read signature was accepted as a settlement")
	}
}
