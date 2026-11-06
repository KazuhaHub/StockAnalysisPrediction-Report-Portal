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

func importTransportFixture(t *testing.T) (control.Authority, control.ImportCheckpointCommand, []byte, []byte, []byte, journal.ImportCheckpointReceipt) {
	t.Helper()
	a, checkpoint, _, _, descriptor, _ := checkpointTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.ImportCheckpointCommand{
		Format: control.ImportCheckpointFormat, DeploymentID: a.DeploymentID,
		ControllerEpoch: a.ControllerEpoch, ControlRevision: strconv.FormatInt(a.ControlRevision, 10),
		InstallationID: checkpoint.InstallationID, OperationID: "11111111-1111-4111-8111-111111119a50",
		TargetGeneration: checkpoint.TargetGeneration, Stage: "restore_data",
		EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111119a51",
		Kind: "import-checkpoint", InstallationFence: checkpoint.InstallationFence,
		Payload: control.ImportCheckpointPayload{
			GrantID:       "11111111-1111-4111-8111-111111119a52",
			RecoverySetID: "11111111-1111-4111-8111-111111119a53",
			CheckpointID:  checkpoint.Payload.CheckpointID, NamespaceID: checkpoint.Payload.NamespaceID,
			ReleaseSHA256: checkpoint.Payload.ReleaseSHA256, ManifestSHA256: strings.Repeat("c", 64),
			ArchiveBytes: "1024", DataFormat: checkpoint.Payload.DataFormat,
			WriterFence: checkpoint.Payload.WriterFence, QuotaBytes: "1048576", QuotaInodes: "1024",
			SourceProjectID: checkpoint.Payload.ProjectID, SourceNamespaceInode: checkpoint.Payload.NamespaceInode,
			SourceDevice:         checkpoint.Payload.Device,
			SourceFilesystemUUID: "11111111-1111-4111-8111-111111119a54", SourceRootInode: "64",
			CheckpointFilesystemUUID: "11111111-1111-4111-8111-111111119a55", CheckpointRootInode: "65",
		},
	}
	var err error
	command.PayloadSHA256, err = control.ImportCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignImportCheckpoint(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(now, 10)
	p := command.Payload
	receipt := journal.ImportCheckpointReceipt{
		Format: "portal.runner.import-checkpoint-receipt/1-dev.1",
		Claim: journal.ImportCheckpointClaim{
			Format: "portal.runner.import-checkpoint-claim/1-dev.1", ClaimID: "11111111-1111-4111-8111-111111119a56",
			EffectID: command.EffectID, ResourceID: p.NamespaceID, OwnershipSHA256: strings.Repeat("d", 64),
			AdmittedFence: command.InstallationFence, ClaimedMS: stamp, GrantID: p.GrantID,
			RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID,
			ProjectID: "5201", RootFilesystemUUID: "11111111-1111-4111-8111-111111119a57", RootInode: "70",
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, RecoverySetID: p.RecoverySetID, GrantID: p.GrantID,
		CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, ReleaseSHA256: p.ReleaseSHA256,
		ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes, DataFormat: p.DataFormat,
		WriterFence: p.WriterFence, ProjectID: "5201", NamespaceInode: "77", Device: "78",
		QuotaBytes: p.QuotaBytes, QuotaInodes: p.QuotaInodes, Entries: "3", UsedBytes: "4096",
		UsedInodes: "4", State: "imported", ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestWritableLifecycleImportCheckpointReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := importTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{ImportCheckpointExecute, ImportCheckpointRead} {
		if !action.valid() {
			t.Fatal("import action is not a valid lifecycle action", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil || ref.importCheckpoint.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("import reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact import reply refused", action)
		}
	}
	if !ImportCheckpointExecute.mutating() || ImportCheckpointRead.mutating() {
		t.Fatal("import execution must be the only mutating import action")
	}
	ref, err := verifyWritableLifecycle(ImportCheckpointExecute, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	expired, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	expired++
	if _, err := verifyWritableLifecycle(ImportCheckpointExecute, raw, sig, descriptor, a, expired); err == nil {
		t.Fatal("expired import dispatch accepted")
	}
	if _, err := verifyWritableLifecycleSettlement(ImportCheckpointExecute, raw, sig, descriptor, a, expired); err != nil {
		t.Fatal("admitted import settlement refused", err)
	}
	foreign := a
	foreign.DeploymentID = "11111111-1111-4111-8111-111111119aff"
	if _, err := verifyWritableLifecycle(ImportCheckpointExecute, raw, sig, descriptor, foreign, now); err == nil {
		t.Fatal("import verified for a foreign runner deployment")
	}
	other := append([]byte(nil), descriptor...)
	other = append(other, ' ')
	if _, err := verifyWritableLifecycle(ImportCheckpointExecute, raw, sig, other, a, now); err == nil {
		t.Fatal("import verified against another release descriptor")
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.ImportCheckpointReceipt){
		"grant":          func(v *journal.ImportCheckpointReceipt) { v.GrantID = "11111111-1111-4111-8111-111111119a60" },
		"claim grant":    func(v *journal.ImportCheckpointReceipt) { v.Claim.GrantID = "11111111-1111-4111-8111-111111119a60" },
		"recovery set":   func(v *journal.ImportCheckpointReceipt) { v.RecoverySetID = "11111111-1111-4111-8111-111111119a61" },
		"checkpoint":     func(v *journal.ImportCheckpointReceipt) { v.CheckpointID = "11111111-1111-4111-8111-111111119a62" },
		"namespace":      func(v *journal.ImportCheckpointReceipt) { v.NamespaceID = "11111111-1111-4111-8111-111111119a63" },
		"claim effect":   func(v *journal.ImportCheckpointReceipt) { v.Claim.EffectID = "11111111-1111-4111-8111-111111119a64" },
		"claim fence":    func(v *journal.ImportCheckpointReceipt) { v.Claim.AdmittedFence = "9" },
		"manifest":       func(v *journal.ImportCheckpointReceipt) { v.ManifestSHA256 = strings.Repeat("f", 64) },
		"release":        func(v *journal.ImportCheckpointReceipt) { v.ReleaseSHA256 = strings.Repeat("f", 64) },
		"archive":        func(v *journal.ImportCheckpointReceipt) { v.ArchiveBytes = "2048" },
		"data format":    func(v *journal.ImportCheckpointReceipt) { v.DataFormat = "2" },
		"writer fence":   func(v *journal.ImportCheckpointReceipt) { v.WriterFence = "2" },
		"quota bytes":    func(v *journal.ImportCheckpointReceipt) { v.QuotaBytes = "2097152" },
		"quota inodes":   func(v *journal.ImportCheckpointReceipt) { v.QuotaInodes = "2048" },
		"installation":   func(v *journal.ImportCheckpointReceipt) { v.InstallationID = "11111111-1111-4111-8111-111111119a65" },
		"operation":      func(v *journal.ImportCheckpointReceipt) { v.OperationID = "11111111-1111-4111-8111-111111119a66" },
		"effect":         func(v *journal.ImportCheckpointReceipt) { v.EffectID = "11111111-1111-4111-8111-111111119a67" },
		"payload":        func(v *journal.ImportCheckpointReceipt) { v.PayloadSHA256 = strings.Repeat("a", 64) },
		"generation":     func(v *journal.ImportCheckpointReceipt) { v.TargetGeneration = "9" },
		"imported state": func(v *journal.ImportCheckpointReceipt) { v.State = "restored" },
		"stage":          func(v *journal.ImportCheckpointReceipt) { v.Stage = "snapshot" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(ImportCheckpointExecute, encoded, ref, started, received) {
				t.Fatal("changed import receipt accepted")
			}
		})
	}
	future, _ := json.Marshal(func() journal.ImportCheckpointReceipt {
		v := receipt
		v.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		return v
	}())
	if validWritableLifecycleReply(ImportCheckpointExecute, future, ref, started, received) {
		t.Fatal("import receipt settled in the future accepted")
	}
}
