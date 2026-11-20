package transport

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func quotaTransportFixture(t *testing.T) (control.Authority, control.SetDataQuotaCommand, []byte, []byte, []byte, journal.SetDataQuotaReceipt) {
	t.Helper()
	a, load, _, _, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.SetDataQuotaCommand{CommandHeader: load.CommandHeader, Payload: control.SetDataQuotaPayload{
		NamespaceID: loadImageTransportID(31), ReleaseSHA256: load.Payload.ReleaseSHA256, DataFormat: "1", WriterFence: "1",
		ProjectID: "4201", NamespaceInode: "77", Device: "66", DataEffectID: loadImageTransportID(32),
		DataReceiptSHA256: strings.Repeat("7", 64), OldBytes: "268435456", OldInodes: "65536", NewBytes: "6442450944", NewInodes: "100000",
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber, command.EffectID = control.SetDataQuotaFormat, control.SetDataQuotaKind, control.SetDataQuotaStage, "1", loadImageTransportID(33)
	var err error
	command.PayloadSHA256, err = control.SetDataQuotaLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignSetDataQuota(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp, p := strconv.FormatInt(now, 10), command.Payload
	receipt := journal.SetDataQuotaReceipt{
		Format: "portal.runner.set-data-quota-receipt/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, NamespaceID: p.NamespaceID,
		ReleaseSHA256: p.ReleaseSHA256, DataFormat: p.DataFormat, WriterFence: p.WriterFence, ProjectID: p.ProjectID,
		NamespaceInode: p.NamespaceInode, Device: p.Device, OldBytes: p.OldBytes, OldInodes: p.OldInodes,
		NewBytes: p.NewBytes, NewInodes: p.NewInodes, UsedBytes: "4096", UsedInodes: "3", State: "raised", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestSetDataQuotaTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := quotaTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{SetDataQuotaExecute, SetDataQuotaRead} {
		if !action.valid() {
			t.Fatal("set-data-quota action is not valid", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.setDataQuota.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("set-data-quota reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact set-data-quota reply refused", action)
		}
		changed := receipt
		changed.NewBytes = "12884901888"
		body, _ = json.Marshal(changed)
		if validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("a receipt for other limits was accepted", action)
		}
	}
	if !SetDataQuotaExecute.mutating() || SetDataQuotaRead.mutating() {
		t.Fatal("only the execute action may mutate")
	}
	read := lifecycleSignature(t, SetDataQuotaRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(SetDataQuotaExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(SetDataQuotaRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	if !capacityRefusalAction(SetDataQuotaExecute) || !capacityRefusalAction(SetDataQuotaRead) {
		t.Fatal("a raise cannot be refused for capacity")
	}
}

func TestSetDataQuotaStatusAndAbsenceReplies(t *testing.T) {
	a, command, raw, sig, descriptor, _ := quotaTransportFixture(t)
	now := time.Now().UnixMilli()
	ref, err := verifyWritableLifecycle(SetDataQuotaRead, raw, lifecycleSignature(t, SetDataQuotaRead, a, raw, sig), descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	status := journal.MigrateDataStatus{
		Format: "portal.runner.migration-status/1-dev.1", Kind: "set-data-quota", DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID, OperationID: command.OperationID,
		TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectNumber: command.EffectNumber, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, State: "absent", ObservedMS: strconv.FormatInt(time.Now().UnixMilli(), 10),
	}
	body, _ := json.Marshal(status)
	if !validWritableLifecycleReply(SetDataQuotaRead, body, ref, time.Now().Add(-time.Second), time.Now().Add(time.Second)) || !dataEffectAbsenceReply(SetDataQuotaRead, body) {
		t.Fatal("a definite absence was not recognised")
	}
	if dataEffectAbsenceReply(SetDataQuotaExecute, body) {
		t.Fatal("an execute can never be answered with an absence")
	}
	status.State = "orphaned"
	body, _ = json.Marshal(status)
	if dataEffectAbsenceReply(SetDataQuotaRead, body) {
		t.Fatal("an orphaned effect was reported absent")
	}
	status.OperationID = loadImageTransportID(38)
	body, _ = json.Marshal(status)
	if validWritableLifecycleReply(SetDataQuotaRead, body, ref, time.Now().Add(-time.Second), time.Now().Add(time.Second)) {
		t.Fatal("a status for another operation was accepted")
	}
}

func importTransportData(t *testing.T) (control.Authority, control.ImportDataCommand, []byte, []byte, []byte, journal.ImportDataReceipt) {
	t.Helper()
	a, load, _, _, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.ImportDataCommand{CommandHeader: load.CommandHeader, Payload: control.ImportDataPayload{
		NamespaceID: loadImageTransportID(34), ReleaseSHA256: load.Payload.ReleaseSHA256, DataFormat: "1", WriterFence: "2",
		ProjectID: "4201", NamespaceInode: "77", Device: "66", QuotaBytes: "6442450944", QuotaInodes: "100000",
		ArchiveSHA256: strings.Repeat("a", 64), ArchiveBytes: "786432", ArchiveEntries: "12", ContentBytes: "700000",
		SourceCheckpointOperationID: load.OperationID, SourceCheckpointEffectID: loadImageTransportID(35), SourceCheckpointReceiptSHA: strings.Repeat("5", 64),
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber, command.EffectID = control.ImportDataFormat, control.ImportDataKind, control.ImportDataStage, "1", loadImageTransportID(36)
	var err error
	command.PayloadSHA256, err = control.ImportDataLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignImportData(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp, p := strconv.FormatInt(now, 10), command.Payload
	receipt := journal.ImportDataReceipt{
		Format: "portal.runner.import-data-receipt/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, NamespaceID: p.NamespaceID,
		ReleaseSHA256: p.ReleaseSHA256, DataFormat: p.DataFormat, WriterFence: p.WriterFence, ProjectID: p.ProjectID,
		NamespaceInode: p.NamespaceInode, Device: p.Device, QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes,
		ArchiveSHA256: p.ArchiveSHA256, ArchiveBytes: p.ArchiveBytes, ArchiveEntries: p.ArchiveEntries, ContentBytes: p.ContentBytes,
		SourceCheckpointOperationID: p.SourceCheckpointOperationID, SourceCheckpointEffectID: p.SourceCheckpointEffectID,
		SourceCheckpointReceiptSHA: p.SourceCheckpointReceiptSHA, Entries: "12", UsedBytes: "800000", UsedInodes: "13",
		State: "imported", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestImportDataTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := importTransportData(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{ImportDataExecute, ImportDataRead} {
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.importData.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("import-data reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact import-data reply refused", action)
		}
		changed := receipt
		changed.ArchiveSHA256 = strings.Repeat("b", 64)
		body, _ = json.Marshal(changed)
		if validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("a receipt for another archive was accepted", action)
		}
	}
	if !ImportDataExecute.mutating() || ImportDataRead.mutating() || !bulkDataAction(ImportDataExecute) {
		t.Fatal("an import mutates, and is bulk work")
	}
	read := lifecycleSignature(t, ImportDataRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(ImportDataExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
}
