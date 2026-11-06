package transport

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

const migrateTransportBase = "11111111-1111-4111-8111-1111111199"

func migrateTransportID(n int) string { return migrateTransportBase + strconv.Itoa(n) }

func migrateTransportKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32))
}

func migrateTransportFixture(t *testing.T) (control.Authority, control.MigrateDataCommand, []byte, []byte, []byte, journal.MigrateDataReceipt) {
	t.Helper()
	a, checkpoint, _, _, descriptor, _ := checkpointTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.MigrateDataCommand{CommandHeader: control.CommandHeader{
		Format: control.MigrateDataFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch,
		ControlRevision: strconv.FormatInt(a.ControlRevision, 10), InstallationID: checkpoint.InstallationID,
		OperationID: migrateTransportID(10), TargetGeneration: checkpoint.TargetGeneration, Stage: "migrate",
		EffectNumber: "2", EffectID: migrateTransportID(11), Kind: "migrate-data", InstallationFence: checkpoint.InstallationFence,
		IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
	}, Payload: control.MigrateDataPayload{
		NamespaceID: checkpoint.Payload.NamespaceID, ResourceID: migrateTransportID(12), CheckpointID: checkpoint.Payload.CheckpointID,
		CheckpointEffectID: checkpoint.EffectID, CheckpointReceiptSHA256: strings.Repeat("e", 64),
		RemoveCodeEffectID: migrateTransportID(13), RemoveCodeReceiptSHA256: strings.Repeat("f", 64),
		SourceReleaseSHA256: strings.Repeat("a", 64), TargetReleaseSHA256: checkpoint.Payload.ReleaseSHA256,
		SourceSchema: "1", TargetSchema: "2", CheckpointWriterFence: "1", WriterFence: "2", StartWriterFence: "3",
		ProjectID: checkpoint.Payload.ProjectID, NamespaceInode: checkpoint.Payload.NamespaceInode, Device: checkpoint.Payload.Device,
		QuotaBytes: "1048576", QuotaInodes: "1024", WallClockSeconds: "60", EntrypointSHA256: strings.Repeat("8", 64),
	}}
	var err error
	command.PayloadSHA256, err = control.MigrateDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignMigrateData(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(now, 10)
	p := command.Payload
	receipt := journal.MigrateDataReceipt{
		Format: "portal.runner.migrate-data-receipt/1-dev.1",
		Claim: journal.MigrateDataClaim{
			Format: "portal.runner.migrate-data-claim/1-dev.1", ClaimID: migrateTransportID(14), EffectID: command.EffectID,
			ResourceID: p.ResourceID, OwnershipSHA256: strings.Repeat("d", 64), AdmittedFence: command.InstallationFence, ClaimedMS: stamp,
			NamespaceID: p.NamespaceID, CheckpointID: p.CheckpointID, CheckpointReceiptSHA256: p.CheckpointReceiptSHA256,
			RemoveCodeReceiptSHA256: p.RemoveCodeReceiptSHA256, SourceReleaseSHA256: p.SourceReleaseSHA256,
			TargetReleaseSHA256: p.TargetReleaseSHA256, SourceSchema: p.SourceSchema, TargetSchema: p.TargetSchema,
			CheckpointWriterFence: p.CheckpointWriterFence, WriterFence: p.WriterFence, StartWriterFence: p.StartWriterFence,
			ProjectID: p.ProjectID, NamespaceInode: p.NamespaceInode, Device: p.Device, QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, ExitCode: "0", MarkerSchema: "2", UsedBytes: "4096", UsedInodes: "4",
		State: "migrated", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func discardTransportFixture(t *testing.T) (control.Authority, control.DiscardMigrationCommand, []byte, []byte, []byte) {
	t.Helper()
	a, migrate, _, _, descriptor, _ := migrateTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.DiscardMigrationCommand{CommandHeader: migrate.CommandHeader, Payload: control.DiscardMigrationPayload{
		NamespaceID: migrate.Payload.NamespaceID, ResourceID: migrate.Payload.ResourceID, MigrateEffectID: migrate.EffectID,
		MigratePayloadSHA256: migrate.PayloadSHA256, TargetReleaseSHA256: migrate.Payload.TargetReleaseSHA256,
	}}
	command.Format, command.EffectNumber, command.Kind, command.EffectID = control.DiscardMigrationFormat, "3", "discard-migration", migrateTransportID(15)
	var err error
	command.PayloadSHA256, err = control.DiscardMigrationLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignDiscardMigration(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	return a, command, raw, signature, descriptor
}

func TestMigrateTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := migrateTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{MigrateDataExecute, MigrateDataRead} {
		if !action.valid() {
			t.Fatal("migrate action is not valid", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.migrate.EffectID != command.EffectID || ref.release != command.Payload.TargetReleaseSHA256 {
			t.Fatal("migrate reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact migrate reply refused", action)
		}
	}
	if !MigrateDataExecute.mutating() || MigrateDataRead.mutating() || !DiscardMigrationExecute.mutating() || DiscardMigrationRead.mutating() {
		t.Fatal("only the execute actions may mutate")
	}
	read := lifecycleSignature(t, MigrateDataRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(MigrateDataExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a migrate read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(MigrateDataRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("a migrate execute signature was accepted as a read")
	}
	expired, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if _, err := verifyWritableLifecycle(MigrateDataExecute, raw, sig, descriptor, a, expired+1); err == nil {
		t.Fatal("expired migrate dispatch accepted")
	}
	if _, err := verifyWritableLifecycle(MigrateDataRead, raw, read, descriptor, a, expired+1); err != nil {
		t.Fatal("a migrate read after the signed window was refused", err)
	}
	other := append(append([]byte(nil), descriptor...), ' ')
	if _, err := verifyWritableLifecycle(MigrateDataExecute, raw, sig, other, a, now); err == nil {
		t.Fatal("migrate verified against another release descriptor")
	}
	// Discard has its own purposes as well.
	da, dcommand, draw, dsig, ddescriptor := discardTransportFixture(t)
	now = time.Now().UnixMilli()
	dread := lifecycleSignature(t, DiscardMigrationRead, da, draw, dsig)
	if ref, err := verifyWritableLifecycle(DiscardMigrationRead, draw, dread, ddescriptor, da, now); err != nil || ref.discard.EffectID != dcommand.EffectID {
		t.Fatal("discard read reference", err)
	}
	if _, err := verifyWritableLifecycle(DiscardMigrationExecute, draw, dread, ddescriptor, da, now); err == nil {
		t.Fatal("a discard read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(DiscardMigrationRead, draw, dsig, ddescriptor, da, now); err == nil {
		t.Fatal("a discard execute signature was accepted as a read")
	}
	// A migrate signature is not a discard.
	if _, err := verifyWritableLifecycle(DiscardMigrationExecute, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("a migrate command was accepted as a discard")
	}
}

func TestMigrateTransportReceiptBinding(t *testing.T) {
	a, _, raw, sig, descriptor, receipt := migrateTransportFixture(t)
	ref, err := verifyWritableLifecycle(MigrateDataExecute, raw, sig, descriptor, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.MigrateDataReceipt){
		"effect":       func(v *journal.MigrateDataReceipt) { v.EffectID = migrateTransportID(40) },
		"claim effect": func(v *journal.MigrateDataReceipt) { v.Claim.EffectID = migrateTransportID(41) },
		"payload":      func(v *journal.MigrateDataReceipt) { v.PayloadSHA256 = strings.Repeat("b", 64) },
		"namespace":    func(v *journal.MigrateDataReceipt) { v.Claim.NamespaceID = migrateTransportID(42) },
		"checkpoint":   func(v *journal.MigrateDataReceipt) { v.Claim.CheckpointID = migrateTransportID(43) },
		"target":       func(v *journal.MigrateDataReceipt) { v.Claim.TargetReleaseSHA256 = strings.Repeat("c", 64) },
		"schema":       func(v *journal.MigrateDataReceipt) { v.Claim.TargetSchema = "9" },
		"writer fence": func(v *journal.MigrateDataReceipt) { v.Claim.WriterFence = "7" },
		"start fence":  func(v *journal.MigrateDataReceipt) { v.Claim.StartWriterFence = "9" },
		"fence":        func(v *journal.MigrateDataReceipt) { v.Claim.AdmittedFence = "9" },
		"installation": func(v *journal.MigrateDataReceipt) { v.InstallationID = migrateTransportID(44) },
		"generation":   func(v *journal.MigrateDataReceipt) { v.TargetGeneration = "9" },
		"state":        func(v *journal.MigrateDataReceipt) { v.State = "restored" },
		"future": func(v *journal.MigrateDataReceipt) {
			v.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(MigrateDataExecute, encoded, ref, started, received) {
				t.Fatal("changed migrate receipt accepted")
			}
		})
	}
	// A failed or discarded receipt is a valid answer too: the Portal reads the outcome from it.
	failed := receipt
	failed.State, failed.Outcome, failed.ExitCode = "failed", "exit_nonzero", "1"
	encoded, _ := json.Marshal(failed)
	if !validWritableLifecycleReply(MigrateDataExecute, encoded, ref, started, received) {
		t.Fatal("known-failure receipt refused")
	}
}

func migrateStatusBody(t *testing.T, kind, stage, effectNumber, state string, header control.CommandHeader) []byte {
	t.Helper()
	body, err := json.Marshal(journal.MigrateDataStatus{
		Format: "portal.runner.migration-status/1-dev.1", Kind: kind, DeploymentID: header.DeploymentID, ControllerEpoch: header.ControllerEpoch,
		InstallationID: header.InstallationID, OperationID: header.OperationID, TargetGeneration: header.TargetGeneration, Stage: stage,
		EffectNumber: effectNumber, EffectID: header.EffectID, PayloadSHA256: header.PayloadSHA256, State: state,
		ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestMigrateTransportAbsenceReply(t *testing.T) {
	a, command, raw, sig, descriptor, _ := migrateTransportFixture(t)
	now := time.Now().UnixMilli()
	started, received := time.Now().Add(-time.Second), time.Now().Add(time.Second)
	read, err := verifyWritableLifecycle(MigrateDataRead, raw, lifecycleSignature(t, MigrateDataRead, a, raw, sig), descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	execute, err := verifyWritableLifecycle(MigrateDataExecute, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"absent", "pending", "orphaned"} {
		body := migrateStatusBody(t, "migrate-data", "migrate", "2", state, command.CommandHeader)
		if !validWritableLifecycleReply(MigrateDataRead, body, read, started, received) {
			t.Fatalf("exact %s status refused for a read", state)
		}
		if validWritableLifecycleReply(MigrateDataExecute, body, execute, started, received) {
			t.Fatalf("an execute may never be answered with a %s status", state)
		}
	}
	if !migrateAbsenceReply(MigrateDataRead, migrateStatusBody(t, "migrate-data", "migrate", "2", "absent", command.CommandHeader)) ||
		migrateAbsenceReply(MigrateDataRead, migrateStatusBody(t, "migrate-data", "migrate", "2", "pending", command.CommandHeader)) ||
		migrateAbsenceReply(MigrateDataExecute, migrateStatusBody(t, "migrate-data", "migrate", "2", "absent", command.CommandHeader)) {
		t.Fatal("only an absent status to a read is a typed absence")
	}
	other := command.CommandHeader
	for name, mutate := range map[string]func(*control.CommandHeader){
		"effect":       func(h *control.CommandHeader) { h.EffectID = migrateTransportID(50) },
		"operation":    func(h *control.CommandHeader) { h.OperationID = migrateTransportID(51) },
		"installation": func(h *control.CommandHeader) { h.InstallationID = migrateTransportID(52) },
		"payload":      func(h *control.CommandHeader) { h.PayloadSHA256 = strings.Repeat("f", 64) },
		"generation":   func(h *control.CommandHeader) { h.TargetGeneration = "9" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := other
			mutate(&changed)
			if validWritableLifecycleReply(MigrateDataRead, migrateStatusBody(t, "migrate-data", "migrate", "2", "absent", changed), read, started, received) {
				t.Fatal("a status for another effect was accepted")
			}
		})
	}
	// The kind and slot must be the command's own.
	if validWritableLifecycleReply(MigrateDataRead, migrateStatusBody(t, "discard-migration", "migrate", "3", "absent", command.CommandHeader), read, started, received) {
		t.Fatal("a discard status answered a migrate read")
	}
	future := migrateStatusBody(t, "migrate-data", "migrate", "2", "absent", command.CommandHeader)
	var status journal.MigrateDataStatus
	_ = json.Unmarshal(future, &status)
	status.ObservedMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
	future, _ = json.Marshal(status)
	if validWritableLifecycleReply(MigrateDataRead, future, read, started, received) {
		t.Fatal("a status observed in the future was accepted")
	}
	// The same typed replies answer a discard read and a restore read.
	da, dcommand, draw, dsig, ddescriptor := discardTransportFixture(t)
	now = time.Now().UnixMilli()
	dread, err := verifyWritableLifecycle(DiscardMigrationRead, draw, lifecycleSignature(t, DiscardMigrationRead, da, draw, dsig), ddescriptor, da, now)
	if err != nil {
		t.Fatal(err)
	}
	if !validWritableLifecycleReply(DiscardMigrationRead, migrateStatusBody(t, "discard-migration", "migrate", "3", "absent", dcommand.CommandHeader), dread, started, received) {
		t.Fatal("discard absence refused")
	}
	ra, rcommand, rraw, rsig, rdescriptor, _ := restoreTransportFixture(t)
	now = time.Now().UnixMilli()
	rread, err := verifyWritableLifecycle(RestoreRead, rraw, lifecycleSignature(t, RestoreRead, ra, rraw, rsig), rdescriptor, ra, now)
	if err != nil {
		t.Fatal(err)
	}
	header := control.CommandHeader{DeploymentID: rcommand.DeploymentID, ControllerEpoch: rcommand.ControllerEpoch, InstallationID: rcommand.InstallationID,
		OperationID: rcommand.OperationID, TargetGeneration: rcommand.TargetGeneration, EffectID: rcommand.EffectID, PayloadSHA256: rcommand.PayloadSHA256}
	if !validWritableLifecycleReply(RestoreRead, migrateStatusBody(t, "restore", "restore_data", rcommand.EffectNumber, "absent", header), rread, started, received) {
		t.Fatal("restore absence refused")
	}
	rexecute, err := verifyWritableLifecycle(RestoreExecute, rraw, rsig, rdescriptor, ra, now)
	if err != nil {
		t.Fatal(err)
	}
	if validWritableLifecycleReply(RestoreExecute, migrateStatusBody(t, "restore", "restore_data", rcommand.EffectNumber, "absent", header), rexecute, started, received) {
		t.Fatal("a restore execute was answered with a status")
	}
}
