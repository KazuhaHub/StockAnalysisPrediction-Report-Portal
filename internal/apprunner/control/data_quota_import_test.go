package control

import (
	"crypto/ed25519"
	"strings"
	"testing"
)

func setDataQuotaFixture(t *testing.T) (SetDataQuotaCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	load, key, authority, now := loadImageFixture(t)
	command := SetDataQuotaCommand{CommandHeader: load.CommandHeader, Payload: SetDataQuotaPayload{
		NamespaceID: "c0000000-0000-4000-8000-0000000000e1", ReleaseSHA256: strings.Repeat("9", 64),
		DataFormat: "1", WriterFence: "1", ProjectID: "4201", NamespaceInode: "77", Device: "66",
		DataEffectID: "c0000000-0000-4000-8000-0000000000e2", DataReceiptSHA256: strings.Repeat("7", 64),
		OldBytes: "268435456", OldInodes: "65536", NewBytes: "6442450944", NewInodes: "100000",
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber = SetDataQuotaFormat, SetDataQuotaKind, SetDataQuotaStage, SetDataQuotaEffectNumber
	command.EffectID = "c0000000-0000-4000-8000-0000000000e3"
	digest, err := SetDataQuotaLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func TestSetDataQuotaRoundTripsAndRefusesNonCanonicalBytes(t *testing.T) {
	command, key, authority, now := setDataQuotaFixture(t)
	raw, signature, err := SignSetDataQuota(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifySetDataQuota(raw, signature, authority, now); err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	variant := append(append([]byte(nil), raw...), '\n')
	if _, err := VerifySetDataQuota(variant, ed25519.Sign(key, frame(setDataQuotaSignaturePurpose, variant)), authority, now); err == nil {
		t.Fatal("non-canonical command accepted")
	}
}

// A raise is only a raise: lowering, an unchanged pair, a limit outside the platform bounds and an unaligned size
// are all refused where the command is signed and verified.
func TestSetDataQuotaIsOnlyARaise(t *testing.T) {
	for name, mutate := range map[string]func(*SetDataQuotaPayload){
		"lower bytes":  func(p *SetDataQuotaPayload) { p.NewBytes = "134217728" },
		"lower inodes": func(p *SetDataQuotaPayload) { p.NewInodes = "1000" },
		"unchanged":    func(p *SetDataQuotaPayload) { p.NewBytes, p.NewInodes = p.OldBytes, p.OldInodes },
		"too large":    func(p *SetDataQuotaPayload) { p.NewBytes = "17179869185" },
		"too many":     func(p *SetDataQuotaPayload) { p.NewInodes = "1000001" },
		"unaligned":    func(p *SetDataQuotaPayload) { p.NewBytes = "6442450945" },
		"zero project": func(p *SetDataQuotaPayload) { p.ProjectID = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			command, _, _, _ := setDataQuotaFixture(t)
			mutate(&command.Payload)
			if _, err := SetDataQuotaLogicalDigest(command); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// A raise below the old 256 MiB floor is still a valid runner quota: the runner holds only to the bounds.
	command, _, _, _ := setDataQuotaFixture(t)
	command.Payload.OldBytes, command.Payload.OldInodes, command.Payload.NewBytes, command.Payload.NewInodes = "8388608", "128", "16777216", "256"
	if _, err := SetDataQuotaLogicalDigest(command); err != nil {
		t.Fatalf("a small raise was refused: %v", err)
	}
}

func importDataFixture(t *testing.T) (ImportDataCommand, ed25519.PrivateKey, Authority, int64) {
	t.Helper()
	load, key, authority, now := loadImageFixture(t)
	command := ImportDataCommand{CommandHeader: load.CommandHeader, Payload: ImportDataPayload{
		NamespaceID: "c0000000-0000-4000-8000-0000000000f1", ReleaseSHA256: strings.Repeat("9", 64),
		DataFormat: "1", WriterFence: "2", ProjectID: "4201", NamespaceInode: "77", Device: "66",
		QuotaBytes: "6442450944", QuotaInodes: "100000", ArchiveSHA256: strings.Repeat("a", 64),
		ArchiveBytes: "786432000", ArchiveEntries: "1200", ContentBytes: "785000000",
		SourceCheckpointOperationID: command0(), SourceCheckpointEffectID: "c0000000-0000-4000-8000-0000000000f3",
		SourceCheckpointReceiptSHA: strings.Repeat("5", 64),
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber = ImportDataFormat, ImportDataKind, ImportDataStage, ImportDataEffectNumber
	command.EffectID = "c0000000-0000-4000-8000-0000000000f4"
	digest, err := ImportDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.PayloadSHA256 = digest
	command.IssuedMS, command.ExpiresMS = "29000", "54000"
	return command, key, authority, now
}

func command0() string { return "c0000000-0000-4000-8000-0000000000f2" }

func TestImportDataRoundTripsAndPurposesAreDisjoint(t *testing.T) {
	command, key, authority, now := importDataFixture(t)
	raw, execute, err := SignImportData(command, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if verified, err := VerifyImportData(raw, execute, authority, now); err != nil || verified.Command() != command {
		t.Fatal("round trip", err)
	}
	read, err := SignImportDataRead(raw, key, authority, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyImportDataRead(raw, read, authority, 55000); err != nil {
		t.Fatal("a read after the signed window was refused:", err)
	}
	if _, err := VerifyImportData(raw, read, authority, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := VerifyImportData(raw, ed25519.Sign(key, frame(restoreSignaturePurpose, raw)), authority, now); err == nil {
		t.Fatal("a restore signature was accepted as an import")
	}
	if _, err := VerifyRestore(raw, execute, authority, now); err == nil {
		t.Fatal("an import command verified as a restore")
	}
}

func TestImportDataRefusesMalformedPayloads(t *testing.T) {
	for name, mutate := range map[string]func(*ImportDataPayload){
		"digest":            func(p *ImportDataPayload) { p.ArchiveSHA256 = "short" },
		"empty archive":     func(p *ImportDataPayload) { p.ArchiveBytes = "0" },
		"quota":             func(p *ImportDataPayload) { p.QuotaBytes = "17179869185" },
		"too many entries":  func(p *ImportDataPayload) { p.ArchiveEntries = "1000001" },
		"checkpoint effect": func(p *ImportDataPayload) { p.SourceCheckpointEffectID = "x" },
		"checkpoint sha":    func(p *ImportDataPayload) { p.SourceCheckpointReceiptSHA = "" },
		"fence":             func(p *ImportDataPayload) { p.WriterFence = "0" },
	} {
		t.Run(name, func(t *testing.T) {
			command, _, _, _ := importDataFixture(t)
			mutate(&command.Payload)
			if _, err := ImportDataLogicalDigest(command); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	// A data format of 0 is the format of an archive without a marker, and is carried as such.
	command, _, _, _ := importDataFixture(t)
	command.Payload.DataFormat = "0"
	if _, err := ImportDataLogicalDigest(command); err != nil {
		t.Fatalf("format 0 refused: %v", err)
	}
}
