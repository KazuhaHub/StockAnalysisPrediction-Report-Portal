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

func deleteCheckpointTransportFixture(t *testing.T) (control.Authority, control.DeleteCheckpointCommand, []byte, []byte, []byte, journal.DeleteCheckpointReceipt) {
	t.Helper()
	a, load, _, _, descriptor, _ := loadImageTransportFixture(t)
	now := time.Now().UnixMilli()
	command := control.DeleteCheckpointCommand{CommandHeader: load.CommandHeader, Payload: control.DeleteCheckpointPayload{
		RecoverySetID: loadImageTransportID(21), CheckpointID: loadImageTransportID(22), NamespaceID: loadImageTransportID(23),
		SourceOperationID: loadImageTransportID(24), ReleaseSHA256: load.Payload.ReleaseSHA256, ManifestSHA256: strings.Repeat("5", 64),
		ArchiveBytes: "4096", DataFormat: "1", WriterFence: "3",
	}}
	command.Format, command.Kind, command.Stage, command.EffectNumber, command.EffectID = control.DeleteCheckpointFormat, control.DeleteCheckpointKind, control.DeleteCheckpointStage, "2", loadImageTransportID(25)
	var err error
	command.PayloadSHA256, err = control.DeleteCheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignDeleteCheckpoint(command, migrateTransportKey(), a, now)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(now, 10)
	p := command.Payload
	receipt := journal.DeleteCheckpointReceipt{
		Format: "portal.runner.delete-checkpoint-receipt/1-dev.1",
		Claim: journal.DeleteCheckpointClaim{
			Format: "portal.runner.delete-checkpoint-claim/1-dev.1", ClaimID: loadImageTransportID(26), EffectID: command.EffectID,
			RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID, SourceOperationID: p.SourceOperationID,
			ReleaseSHA256: p.ReleaseSHA256, ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes, DataFormat: p.DataFormat,
			WriterFence: p.WriterFence, AdmittedFence: command.InstallationFence, ClaimedMS: stamp,
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, RecoverySetID: p.RecoverySetID, CheckpointID: p.CheckpointID, NamespaceID: p.NamespaceID,
		ManifestSHA256: p.ManifestSHA256, ArchiveBytes: p.ArchiveBytes, Outcome: "removed", ArchiveFound: true, ObservedMS: stamp, SettledMS: stamp,
	}
	return a, command, raw, signature, descriptor, receipt
}

func TestDeleteCheckpointTransportActionsAndSignaturePurposes(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := deleteCheckpointTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{DeleteCheckpointExecute, DeleteCheckpointRead} {
		if !action.valid() {
			t.Fatal("delete-checkpoint action is not valid", action)
		}
		ref, err := verifyWritableLifecycle(action, raw, lifecycleSignature(t, action, a, raw, sig), descriptor, a, now)
		if err != nil || ref.deleteCheckpoint.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("delete-checkpoint reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact delete-checkpoint reply refused", action)
		}
	}
	if !DeleteCheckpointExecute.mutating() || DeleteCheckpointRead.mutating() {
		t.Fatal("only the execute action may mutate")
	}
	read := lifecycleSignature(t, DeleteCheckpointRead, a, raw, sig)
	if _, err := verifyWritableLifecycle(DeleteCheckpointExecute, raw, read, descriptor, a, now); err == nil {
		t.Fatal("a read signature was accepted as an execute")
	}
	if _, err := verifyWritableLifecycle(DeleteCheckpointRead, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("an execute signature was accepted as a read")
	}
	// A signed removal of an image is never a checkpoint deletion, and the reverse.
	_, _, rraw, rsig, rdescriptor, _ := removeImageTransportFixture(t)
	if _, err := verifyWritableLifecycle(DeleteCheckpointExecute, rraw, rsig, rdescriptor, a, now); err == nil {
		t.Fatal("a remove-image command was accepted as a checkpoint deletion")
	}
	if _, err := verifyWritableLifecycle(RemoveImageExecute, raw, sig, descriptor, a, now); err == nil {
		t.Fatal("a checkpoint deletion was accepted as a remove-image command")
	}
	expired, _ := strconv.ParseInt(command.ExpiresMS, 10, 64)
	if _, err := verifyWritableLifecycle(DeleteCheckpointExecute, raw, sig, descriptor, a, expired+1); err == nil {
		t.Fatal("expired deletion dispatch accepted")
	}
	if _, err := verifyWritableLifecycle(DeleteCheckpointRead, raw, read, descriptor, a, expired+1); err != nil {
		t.Fatal("a read after the signed window was refused", err)
	}
}

func TestDeleteCheckpointTransportReplyBinding(t *testing.T) {
	a, _, raw, sig, descriptor, receipt := deleteCheckpointTransportFixture(t)
	ref, err := verifyWritableLifecycle(DeleteCheckpointExecute, raw, sig, descriptor, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.DeleteCheckpointReceipt){
		"effect":       func(v *journal.DeleteCheckpointReceipt) { v.EffectID = loadImageTransportID(30) },
		"claim effect": func(v *journal.DeleteCheckpointReceipt) { v.Claim.EffectID = loadImageTransportID(31) },
		"payload":      func(v *journal.DeleteCheckpointReceipt) { v.PayloadSHA256 = strings.Repeat("b", 64) },
		"checkpoint": func(v *journal.DeleteCheckpointReceipt) {
			v.CheckpointID, v.Claim.CheckpointID = loadImageTransportID(32), loadImageTransportID(32)
		},
		"set": func(v *journal.DeleteCheckpointReceipt) {
			v.Claim.RecoverySetID, v.RecoverySetID = loadImageTransportID(33), loadImageTransportID(33)
		},
		"manifest": func(v *journal.DeleteCheckpointReceipt) {
			v.Claim.ManifestSHA256, v.ManifestSHA256 = strings.Repeat("c", 64), strings.Repeat("c", 64)
		},
		"fence":        func(v *journal.DeleteCheckpointReceipt) { v.Claim.AdmittedFence = "9" },
		"installation": func(v *journal.DeleteCheckpointReceipt) { v.InstallationID = loadImageTransportID(34) },
		"stage":        func(v *journal.DeleteCheckpointReceipt) { v.Stage = "stage" },
		"outcome":      func(v *journal.DeleteCheckpointReceipt) { v.Outcome = "deleted" },
		"future": func(v *journal.DeleteCheckpointReceipt) {
			v.SettledMS = strconv.FormatInt(time.Now().Add(time.Hour).UnixMilli(), 10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			v := receipt
			mutate(&v)
			body, _ := json.Marshal(v)
			if validWritableLifecycleReply(DeleteCheckpointExecute, body, ref, started, received) {
				t.Fatal("a reply that does not answer this command was accepted")
			}
		})
	}
	// A refusal is also a receipt, with its typed outcome.
	for _, outcome := range []string{"checkpoint_latest", "checkpoint_in_use", "checkpoint_identity_mismatch", "checkpoint_not_verified", "checkpoint_unrecorded"} {
		refused := receipt
		refused.Outcome, refused.ArchiveFound = outcome, false
		body, _ := json.Marshal(refused)
		if !validWritableLifecycleReply(DeleteCheckpointExecute, body, ref, started, received) {
			t.Fatalf("a %s refusal receipt was refused", outcome)
		}
	}
	// A remove-image receipt never answers a checkpoint deletion.
	_, _, _, _, _, imageReceipt := removeImageTransportFixture(t)
	body, _ := json.Marshal(imageReceipt)
	if validWritableLifecycleReply(DeleteCheckpointExecute, body, ref, started, received) {
		t.Fatal("a remove-image receipt answered a checkpoint deletion")
	}
}

func TestDeleteCheckpointTransportStatusReplies(t *testing.T) {
	a, command, raw, sig, descriptor, _ := deleteCheckpointTransportFixture(t)
	now := time.Now()
	read := lifecycleSignature(t, DeleteCheckpointRead, a, raw, sig)
	ref, err := verifyWritableLifecycle(DeleteCheckpointRead, raw, read, descriptor, a, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	status := func(state string, observed time.Time) []byte {
		raw, _ := json.Marshal(journal.DeleteCheckpointStatus{
			Format: "portal.runner.delete-checkpoint-status/1-dev.1", DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
			InstallationID: command.InstallationID, OperationID: command.OperationID, TargetGeneration: command.TargetGeneration, Stage: command.Stage,
			EffectNumber: command.EffectNumber, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256, State: state,
			ObservedMS: strconv.FormatInt(observed.UnixMilli(), 10),
		})
		return raw
	}
	started, received := now.Add(-time.Second), now.Add(time.Second)
	for _, tc := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"pending", status("pending", now), true},
		{"orphaned", status("orphaned", now), true},
		{"absent", status("absent", now), true},
		{"unknown state", status("blobs_missing", now), false},
		{"observed before the command was issued", status("pending", now.Add(-time.Hour)), false},
		{"observed in the future", status("pending", now.Add(time.Hour)), false},
	} {
		if got := validWritableLifecycleReply(DeleteCheckpointRead, tc.body, ref, started, received); got != tc.valid {
			t.Fatalf("%s: valid=%v want %v", tc.name, got, tc.valid)
		}
	}
	if !deleteCheckpointAbsenceReply(DeleteCheckpointRead, status("absent", now)) || deleteCheckpointAbsenceReply(DeleteCheckpointExecute, status("absent", now)) ||
		deleteCheckpointAbsenceReply(DeleteCheckpointRead, status("pending", now)) || deleteCheckpointAbsenceReply(DeleteCheckpointRead, status("orphaned", now)) {
		t.Fatal("absence classification is wrong")
	}
}

func TestDeleteCheckpointObservationAccessors(t *testing.T) {
	_, _, _, _, _, receipt := deleteCheckpointTransportFixture(t)
	body, _ := json.Marshal(receipt)
	o := WritableLifecycleObservation{action: DeleteCheckpointExecute, body: string(body)}
	if got, err := o.DeleteCheckpointReceipt(); err != nil || got != receipt {
		t.Fatalf("receipt accessor: %v", err)
	}
	if _, err := o.DeleteCheckpointStatus(); err == nil {
		t.Fatal("a receipt parsed as a status")
	}
	foreign := WritableLifecycleObservation{action: RemoveImageExecute, body: string(body)}
	if _, err := foreign.DeleteCheckpointReceipt(); err == nil {
		t.Fatal("a remove-image observation yielded a checkpoint deletion receipt")
	}
}
