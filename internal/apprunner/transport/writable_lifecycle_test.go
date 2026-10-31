package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func managedRemovalTransportFixture(t *testing.T) (control.Authority, control.ManagedRemovalCommand, []byte, []byte, []byte, journal.ManagedRemovalReceipt) {
	t.Helper()
	a, source, _, _ := candidateReceiptAuthority(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	release := sha256.Sum256(descriptor)
	a.ControlRevision = 3
	now := time.Now().UnixMilli()
	command := control.ManagedRemovalCommand{
		Format: control.ManagedRemovalFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch,
		ControlRevision: strconv.FormatInt(a.ControlRevision, 10), InstallationID: source.InstallationID,
		OperationID: "11111111-1111-4111-8111-111111119911", TargetGeneration: "2",
		Stage: "stop", EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111119912", Kind: "stop",
		InstallationFence: "2", IssuedMS: strconv.FormatInt(now, 10), ExpiresMS: strconv.FormatInt(now+25000, 10),
		Payload: control.ManagedRemovalPayload{
			ResourceID: source.Payload.ResourceID, DataNamespaceID: source.Payload.DataNamespaceID,
			ReleaseSHA256: hex.EncodeToString(release[:]), Platform: source.Payload.Platform,
			SourceOperationID: source.OperationID, SourceEffectID: source.EffectID,
			SourceReceiptSHA256: strings.Repeat("a", 64),
		},
	}
	command.PayloadSHA256, err = control.ManagedRemovalLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	raw, signature, err := control.SignManagedRemoval(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt := journal.ManagedRemovalReceipt{
		Format: "portal.runner.managed-removal-receipt/1-dev.1", DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID, OperationID: command.OperationID,
		SourceOperationID: command.Payload.SourceOperationID, SourceEffectID: command.Payload.SourceEffectID,
		TargetGeneration: command.TargetGeneration, Stage: command.Stage, EffectID: command.EffectID,
		PayloadSHA256: command.PayloadSHA256, SourceReceiptSHA256: command.Payload.SourceReceiptSHA256,
		ResourceID: command.Payload.ResourceID, DataNamespaceID: command.Payload.DataNamespaceID,
		ReleaseSHA256: command.Payload.ReleaseSHA256, NativeID: strings.Repeat("b", 64), State: "stopped",
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	return a, command, raw, signature, descriptor, receipt
}

func checkpointTransportFixture(t *testing.T) (control.Authority, control.CheckpointCommand, []byte, []byte, []byte, journal.CheckpointReceipt) {
	t.Helper()
	a, source, _, _ := candidateReceiptAuthority(t)
	vector := candidateReceiptVector(t)
	descriptor, err := os.ReadFile("../../appplatform/testdata/descriptors/managed.json")
	if err != nil {
		t.Fatal(err)
	}
	release := sha256.Sum256(descriptor)
	now := time.Now().UnixMilli()
	command := control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: a.DeploymentID, ControllerEpoch: a.ControllerEpoch,
		ControlRevision: strconv.FormatInt(a.ControlRevision, 10), InstallationID: source.InstallationID,
		OperationID: "11111111-1111-4111-8111-111111119930", TargetGeneration: "2",
		Stage: "snapshot", EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111119931",
		Kind: "snapshot", InstallationFence: source.InstallationFence,
		Payload: control.CheckpointPayload{
			CheckpointID: "11111111-1111-4111-8111-111111119932", NamespaceID: source.Payload.DataNamespaceID,
			ResourceID: source.Payload.ResourceID, ReleaseSHA256: hex.EncodeToString(release[:]),
			SourceOperationID: source.OperationID, SourceStartEffectID: source.EffectID,
			SourceReceiptSHA256:     strings.Repeat("a", 64),
			SourceDataEffectID:      "11111111-1111-4111-8111-111111119933",
			SourceDataReceiptSHA256: strings.Repeat("b", 64),
			ProjectID:               "1", NamespaceInode: vector.Claim.Preflight.NamespaceInode,
			Device: vector.Claim.Preflight.Device, DataFormat: source.Payload.DataFormat,
			WriterFence: source.Payload.WriterFence,
		},
	}
	command.PayloadSHA256, err = control.CheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignCheckpoint(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt := journal.CheckpointReceipt{
		Format: "portal.runner.data-checkpoint-receipt/1-dev.1", DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		CheckpointID: command.Payload.CheckpointID, NamespaceID: command.Payload.NamespaceID,
		ResourceID: command.Payload.ResourceID, ReleaseSHA256: command.Payload.ReleaseSHA256,
		SourceOperationID:       command.Payload.SourceOperationID,
		SourceStartEffectID:     command.Payload.SourceStartEffectID,
		SourceReceiptSHA256:     command.Payload.SourceReceiptSHA256,
		SourceDataEffectID:      command.Payload.SourceDataEffectID,
		SourceDataReceiptSHA256: command.Payload.SourceDataReceiptSHA256,
		ProjectID:               command.Payload.ProjectID, NamespaceInode: command.Payload.NamespaceInode,
		Device: command.Payload.Device, DataFormat: command.Payload.DataFormat,
		WriterFence: command.Payload.WriterFence, ArchiveBytes: "1024", Entries: "3",
		ManifestSHA256: strings.Repeat("c", 64),
		ObservedMS:     strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	return a, command, raw, signature, descriptor, receipt
}

func restoreTransportFixture(t *testing.T) (control.Authority, control.RestoreCommand, []byte, []byte, []byte, journal.RestoreReceipt) {
	t.Helper()
	a, checkpoint, _, _, descriptor, checkpointReceipt := checkpointTransportFixture(t)
	checkpointRaw, err := json.Marshal(checkpointReceipt)
	if err != nil {
		t.Fatal(err)
	}
	checkpointDigest := sha256.Sum256(checkpointRaw)
	now := time.Now().UnixMilli()
	command := control.RestoreCommand{
		Format: control.RestoreFormat, DeploymentID: a.DeploymentID,
		ControllerEpoch: a.ControllerEpoch, ControlRevision: strconv.FormatInt(a.ControlRevision, 10),
		InstallationID: checkpoint.InstallationID, OperationID: "11111111-1111-4111-8111-111111119950",
		TargetGeneration: checkpoint.TargetGeneration, Stage: "restore_data",
		EffectNumber: "1", EffectID: "11111111-1111-4111-8111-111111119951",
		Kind: "restore", InstallationFence: checkpoint.InstallationFence,
		Payload: control.RestorePayload{
			RecoverySetID: "11111111-1111-4111-8111-111111119952",
			CheckpointID:  checkpoint.Payload.CheckpointID, NamespaceID: checkpoint.Payload.NamespaceID,
			ReleaseSHA256:  checkpoint.Payload.ReleaseSHA256,
			ManifestSHA256: checkpointReceipt.ManifestSHA256,
			ArchiveBytes:   checkpointReceipt.ArchiveBytes, ProjectID: checkpoint.Payload.ProjectID,
			NamespaceInode: checkpoint.Payload.NamespaceInode, Device: checkpoint.Payload.Device,
			DataFormat: checkpoint.Payload.DataFormat, WriterFence: checkpoint.Payload.WriterFence,
			SourceCheckpointOperationID: checkpoint.OperationID,
			SourceCheckpointEffectID:    checkpoint.EffectID,
			SourceCheckpointReceiptSHA:  hex.EncodeToString(checkpointDigest[:]),
		},
	}
	command.PayloadSHA256, err = control.RestoreLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	raw, signature, err := control.SignRestore(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	receipt := journal.RestoreReceipt{
		Format:       "portal.runner.data-restore-receipt/1-dev.1",
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		TargetGeneration: command.TargetGeneration, Stage: command.Stage,
		EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
		RecoverySetID: command.Payload.RecoverySetID, CheckpointID: command.Payload.CheckpointID,
		NamespaceID: command.Payload.NamespaceID, ReleaseSHA256: command.Payload.ReleaseSHA256,
		ManifestSHA256: command.Payload.ManifestSHA256, ArchiveBytes: command.Payload.ArchiveBytes,
		ProjectID: command.Payload.ProjectID, NamespaceInode: command.Payload.NamespaceInode,
		Device: command.Payload.Device, DataFormat: command.Payload.DataFormat,
		WriterFence:                 command.Payload.WriterFence,
		SourceCheckpointOperationID: command.Payload.SourceCheckpointOperationID,
		SourceCheckpointEffectID:    command.Payload.SourceCheckpointEffectID,
		SourceCheckpointReceiptSHA:  command.Payload.SourceCheckpointReceiptSHA,
		Entries:                     "3", UsedBytes: "4096", State: "restored", ObservedMS: strconv.FormatInt(now, 10),
		SettledMS: strconv.FormatInt(now, 10),
	}
	return a, command, raw, signature, descriptor, receipt
}

func updateStartTransportFixture(t *testing.T) (control.Authority, control.UpdateStartCommand, []byte, []byte, []byte, journal.UpdateStartReservation) {
	t.Helper()
	a, checkpoint, _, _, descriptor, checkpointReceipt := checkpointTransportFixture(t)
	checkpointRaw, err := json.Marshal(checkpointReceipt)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	command := control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: a.DeploymentID,
		ControllerEpoch: a.ControllerEpoch, ControlRevision: strconv.FormatInt(a.ControlRevision, 10),
		InstallationID: checkpoint.InstallationID, OperationID: checkpoint.OperationID,
		TargetGeneration: checkpoint.TargetGeneration, Stage: "start", EffectNumber: "1",
		EffectID: "11111111-1111-4111-8111-111111119970", Kind: "update-start",
		InstallationFence: "2",
		Payload: control.UpdateStartPayload{
			ResourceID:                  "11111111-1111-4111-8111-111111119971",
			NamespaceID:                 checkpoint.Payload.NamespaceID,
			SourceOperationID:           checkpoint.Payload.SourceOperationID,
			SourceStartEffectID:         checkpoint.Payload.SourceStartEffectID,
			SourceStartReceiptSHA256:    checkpoint.Payload.SourceReceiptSHA256,
			SourceDataEffectID:          checkpoint.Payload.SourceDataEffectID,
			SourceDataReceiptSHA256:     checkpoint.Payload.SourceDataReceiptSHA256,
			SourceCheckpointOperationID: checkpoint.OperationID,
			SourceCheckpointEffectID:    checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: func() string {
				sum := sha256.Sum256(checkpointRaw)
				return hex.EncodeToString(sum[:])
			}(),
			CheckpointID:             checkpoint.Payload.CheckpointID,
			CheckpointManifestSHA256: checkpointReceipt.ManifestSHA256,
			CheckpointArchiveBytes:   checkpointReceipt.ArchiveBytes,
			SourceReleaseSHA256:      checkpoint.Payload.ReleaseSHA256,
			TargetReleaseSHA256:      checkpoint.Payload.ReleaseSHA256,
			SourceGeneration:         "1", TargetPolicyRevision: "2",
			ProjectID: checkpoint.Payload.ProjectID, NamespaceInode: checkpoint.Payload.NamespaceInode,
			Device: checkpoint.Payload.Device, QuotaBytes: "268435456", QuotaInodes: "65536",
			RootFilesystemUUID: "11111111-1111-4111-8111-111111119972", RootInode: "12",
			DataFormat: checkpoint.Payload.DataFormat, SourceWriterFence: checkpoint.Payload.WriterFence,
			TargetWriterFence: "2", Platform: "linux/amd64",
			SandboxProfile: control.CandidateSandboxProfile,
		},
	}
	command.PayloadSHA256, err = control.UpdateStartLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	signed, signature, err := control.SignUpdateStart(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), a, now)
	if err != nil {
		t.Fatal(err)
	}
	reservation := journal.UpdateStartReservation{
		ResourceID: command.Payload.ResourceID, InstallationID: command.InstallationID,
		NamespaceID: command.Payload.NamespaceID, EffectID: command.EffectID,
		OwnershipSHA256: strings.Repeat("d", 64), SourceGeneration: 1, TargetGeneration: 2,
		SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
		ControlFence:        2, SourceWriterFence: 1, TargetWriterFence: 2,
	}
	return a, command, signed, signature, descriptor, reservation
}

func updateStartRunningReceiptFixture(command control.UpdateStartCommand, reservation journal.UpdateStartReservation) journal.UpdateStartRunningReceipt {
	issued, _ := strconv.ParseInt(command.IssuedMS, 10, 64)
	observed := issued
	return journal.UpdateStartRunningReceipt{
		Format: "portal.runner.update-start-running-receipt/1-dev.1",
		Claim: journal.UpdateStartClaimMarker{
			Format:   "portal.runner.update-start-claim/1-dev.1",
			ClaimID:  "11111111-1111-4111-8111-111111119973",
			EffectID: command.EffectID, ResourceID: reservation.ResourceID,
			PayloadSHA256: command.PayloadSHA256, OwnershipSHA256: reservation.OwnershipSHA256,
			CheckpointReceiptSHA256: command.Payload.SourceCheckpointReceiptSHA256,
			ControlRevision:         command.ControlRevision, AdmittedFence: command.InstallationFence,
			SandboxProfile: command.Payload.SandboxProfile,
			ClaimedMS:      command.IssuedMS, ExpiresMS: command.ExpiresMS,
			Preflight: &journal.UpdateStartPreflightEvidence{
				Format:       "portal.runner.update-start-preflight/1-dev.1",
				ImageID:      "sha256:" + strings.Repeat("e", 64),
				Platform:     command.Payload.Platform,
				PolicySHA256: strings.Repeat("f", 64), MountsSHA256: strings.Repeat("1", 64),
				RootFilesystemUUID: command.Payload.RootFilesystemUUID,
				RootInode:          command.Payload.RootInode, ProjectID: command.Payload.ProjectID,
				Device: command.Payload.Device, NamespaceInode: command.Payload.NamespaceInode,
				QuotaBytes: command.Payload.QuotaBytes, QuotaInodes: command.Payload.QuotaInodes,
				SeccompSHA256: driver.UpdateStartSeccompSHA256(command.Payload.SandboxProfile),
			},
			PreflightBoundMS: strconv.FormatInt(observed, 10),
		},
		DeploymentID: command.DeploymentID, ControllerEpoch: command.ControllerEpoch,
		InstallationID: command.InstallationID, OperationID: command.OperationID,
		NamespaceID:         command.Payload.NamespaceID,
		SourceReleaseSHA256: command.Payload.SourceReleaseSHA256,
		TargetReleaseSHA256: command.Payload.TargetReleaseSHA256,
		SourceGeneration:    command.Payload.SourceGeneration,
		TargetGeneration:    command.TargetGeneration, ControlFence: command.InstallationFence,
		SourceWriterFence: command.Payload.SourceWriterFence,
		TargetWriterFence: command.Payload.TargetWriterFence,
		DataFormat:        command.Payload.DataFormat,
		NativeID:          strings.Repeat("3", 64),
		NativeObservedMS:  strconv.FormatInt(observed, 10),
		SettledMS:         strconv.FormatInt(observed, 10),
	}
}

func TestWritableLifecycleIndependentActionReferences(t *testing.T) {
	a, c, raw, sig, descriptor, _ := writableHandshakeRequestFixture(t)
	v, err := control.VerifyWritableBackendRegistration(raw, sig, a, time.Now().UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	start := v.Candidate().Command()
	writer := v.Candidate().Writer().Command()
	for action := WritableWriterExecute; action <= WritableRegistrationRead; action++ {
		request, signature := raw, sig
		if action <= WritableWriterReconcile {
			request, signature = start.WriterRaw, start.WriterSignature
		} else if action <= WritableCandidateProbe {
			request, signature = c.CandidateRaw, c.CandidateSignature
		}
		ref, err := verifyWritableLifecycle(action, request, signature, descriptor, a, time.Now().UnixMilli())
		if err != nil || ref.writer.EffectID != writer.EffectID {
			t.Fatal(action, err)
		}
		badSig := append([]byte(nil), signature...)
		badSig[0] ^= 1
		if _, err := verifyWritableLifecycle(action, request, badSig, descriptor, a, time.Now().UnixMilli()); err == nil {
			t.Fatal("bad signature accepted", action)
		}
		if _, err := verifyWritableLifecycle(action, request, signature, []byte(`{}`), a, time.Now().UnixMilli()); err == nil {
			t.Fatal("bad descriptor accepted", action)
		}
	}
	if _, err := verifyWritableLifecycle(0, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("zero action")
	}
	if _, err := verifyWritableLifecycle(255, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("foreign action")
	}
	if _, err := verifyWritableLifecycle(WritableWriterExecute, raw, sig, descriptor, a, time.Now().UnixMilli()); err == nil {
		t.Fatal("cross-domain request")
	}
}

func TestWritableLifecycleManagedRemovalReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := managedRemovalTransportFixture(t)
	now := time.Now().UnixMilli()
	ref, err := verifyWritableLifecycle(ManagedRemovalStop, raw, sig, descriptor, a, now)
	if err != nil || ref.removal.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
		t.Fatal("managed removal reference", err)
	}
	body, _ := json.Marshal(receipt)
	started, received := time.Now().Add(-time.Second), time.Now()
	if !validWritableLifecycleReply(ManagedRemovalStop, body, ref, started, received) {
		t.Fatal("exact managed stop reply refused")
	}
	for name, mutate := range map[string]func(*journal.ManagedRemovalReceipt){
		"deployment":       func(v *journal.ManagedRemovalReceipt) { v.DeploymentID = "11111111-1111-4111-8111-111111119920" },
		"installation":     func(v *journal.ManagedRemovalReceipt) { v.InstallationID = "11111111-1111-4111-8111-111111119921" },
		"operation":        func(v *journal.ManagedRemovalReceipt) { v.OperationID = "11111111-1111-4111-8111-111111119922" },
		"source operation": func(v *journal.ManagedRemovalReceipt) { v.SourceOperationID = "11111111-1111-4111-8111-111111119923" },
		"source effect":    func(v *journal.ManagedRemovalReceipt) { v.SourceEffectID = "11111111-1111-4111-8111-111111119924" },
		"target":           func(v *journal.ManagedRemovalReceipt) { v.TargetGeneration = "3" },
		"effect":           func(v *journal.ManagedRemovalReceipt) { v.EffectID = "11111111-1111-4111-8111-111111119925" },
		"payload":          func(v *journal.ManagedRemovalReceipt) { v.PayloadSHA256 = strings.Repeat("c", 64) },
		"source receipt":   func(v *journal.ManagedRemovalReceipt) { v.SourceReceiptSHA256 = strings.Repeat("d", 64) },
		"resource":         func(v *journal.ManagedRemovalReceipt) { v.ResourceID = "11111111-1111-4111-8111-111111119926" },
		"namespace":        func(v *journal.ManagedRemovalReceipt) { v.DataNamespaceID = "11111111-1111-4111-8111-111111119927" },
		"release":          func(v *journal.ManagedRemovalReceipt) { v.ReleaseSHA256 = strings.Repeat("e", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(ManagedRemovalStop, encoded, ref, started, received) {
				t.Fatal("changed managed receipt accepted")
			}
		})
	}
	if _, err := verifyWritableLifecycle(ManagedRemovalStop, raw, sig, []byte(`{}`), a, now); err == nil {
		t.Fatal("managed removal accepted changed descriptor")
	}
	badSignature := append([]byte(nil), sig...)
	badSignature[0] ^= 1
	if _, err := verifyWritableLifecycle(ManagedRemovalStop, raw, badSignature, descriptor, a, now); err == nil {
		t.Fatal("managed removal accepted changed signature")
	}
}

func TestWritableLifecycleCheckpointReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := checkpointTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{CheckpointCreate, CheckpointRead} {
		ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil || ref.checkpoint.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("checkpoint reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		started, received := time.Now().Add(-time.Second), time.Now()
		if !validWritableLifecycleReply(action, body, ref, started, received) {
			t.Fatal("exact checkpoint reply refused", action)
		}
		badSignature := append([]byte(nil), sig...)
		badSignature[0] ^= 1
		if _, err := verifyWritableLifecycle(action, raw, badSignature, descriptor, a, now); err == nil {
			t.Fatal("changed checkpoint signature accepted", action)
		}
		if _, err := verifyWritableLifecycle(action, raw, sig, []byte(`{}`), a, now); err == nil {
			t.Fatal("changed checkpoint descriptor accepted", action)
		}
	}
	ref, err := verifyWritableLifecycle(CheckpointCreate, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.CheckpointReceipt){
		"installation":      func(v *journal.CheckpointReceipt) { v.InstallationID = "11111111-1111-4111-8111-111111119940" },
		"operation":         func(v *journal.CheckpointReceipt) { v.OperationID = "11111111-1111-4111-8111-111111119941" },
		"checkpoint":        func(v *journal.CheckpointReceipt) { v.CheckpointID = "11111111-1111-4111-8111-111111119942" },
		"source operation":  func(v *journal.CheckpointReceipt) { v.SourceOperationID = "11111111-1111-4111-8111-111111119943" },
		"source start":      func(v *journal.CheckpointReceipt) { v.SourceStartEffectID = "11111111-1111-4111-8111-111111119944" },
		"source receipt":    func(v *journal.CheckpointReceipt) { v.SourceReceiptSHA256 = strings.Repeat("d", 64) },
		"source data":       func(v *journal.CheckpointReceipt) { v.SourceDataEffectID = "11111111-1111-4111-8111-111111119945" },
		"source data hash":  func(v *journal.CheckpointReceipt) { v.SourceDataReceiptSHA256 = strings.Repeat("e", 64) },
		"namespace":         func(v *journal.CheckpointReceipt) { v.NamespaceID = "11111111-1111-4111-8111-111111119946" },
		"resource":          func(v *journal.CheckpointReceipt) { v.ResourceID = "11111111-1111-4111-8111-111111119947" },
		"writer fence":      func(v *journal.CheckpointReceipt) { v.WriterFence = "2" },
		"target generation": func(v *journal.CheckpointReceipt) { v.TargetGeneration = "3" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(CheckpointCreate, encoded, ref, started, received) {
				t.Fatal("changed checkpoint receipt accepted")
			}
		})
	}
}

func TestWritableLifecycleRestoreReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, receipt := restoreTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{RestoreExecute, RestoreRead} {
		ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil || ref.restore.EffectID != command.EffectID || ref.release != command.Payload.ReleaseSHA256 {
			t.Fatal("restore reference", action, err)
		}
		body, _ := json.Marshal(receipt)
		started, received := time.Now().Add(-time.Second), time.Now()
		if !validWritableLifecycleReply(action, body, ref, started, received) {
			t.Fatal("exact restore reply refused", action)
		}
	}
	ref, err := verifyWritableLifecycle(RestoreExecute, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	started, received := time.Now().Add(-time.Second), time.Now()
	for name, mutate := range map[string]func(*journal.RestoreReceipt){
		"recovery set": func(v *journal.RestoreReceipt) { v.RecoverySetID = "11111111-1111-4111-8111-111111119960" },
		"checkpoint":   func(v *journal.RestoreReceipt) { v.CheckpointID = "11111111-1111-4111-8111-111111119961" },
		"namespace":    func(v *journal.RestoreReceipt) { v.NamespaceID = "11111111-1111-4111-8111-111111119962" },
		"manifest":     func(v *journal.RestoreReceipt) { v.ManifestSHA256 = strings.Repeat("f", 64) },
		"archive":      func(v *journal.RestoreReceipt) { v.ArchiveBytes = "2048" },
		"source operation": func(v *journal.RestoreReceipt) {
			v.SourceCheckpointOperationID = "11111111-1111-4111-8111-111111119963"
		},
		"source effect":     func(v *journal.RestoreReceipt) { v.SourceCheckpointEffectID = "11111111-1111-4111-8111-111111119964" },
		"source receipt":    func(v *journal.RestoreReceipt) { v.SourceCheckpointReceiptSHA = strings.Repeat("a", 64) },
		"namespace inode":   func(v *journal.RestoreReceipt) { v.NamespaceInode = "99" },
		"device":            func(v *journal.RestoreReceipt) { v.Device = "99" },
		"writer fence":      func(v *journal.RestoreReceipt) { v.WriterFence = "2" },
		"target generation": func(v *journal.RestoreReceipt) { v.TargetGeneration = "3" },
		"restored state":    func(v *journal.RestoreReceipt) { v.State = "verified" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(RestoreExecute, encoded, ref, started, received) {
				t.Fatal("changed restore receipt accepted")
			}
		})
	}
}

func TestWritableLifecycleUpdateStartReferenceAndReply(t *testing.T) {
	a, command, raw, sig, descriptor, reservation := updateStartTransportFixture(t)
	now := time.Now().UnixMilli()
	for _, action := range []WritableLifecycleAction{UpdateStartReserve, UpdateStartRead} {
		ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil || ref.updateStart.EffectID != command.EffectID ||
			ref.release != command.Payload.TargetReleaseSHA256 {
			t.Fatal("update-start reference", action, err)
		}
		body, _ := json.Marshal(reservation)
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact update-start reservation refused", action)
		}
	}
	ref, err := verifyWritableLifecycle(UpdateStartReserve, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*journal.UpdateStartReservation){
		"resource":       func(v *journal.UpdateStartReservation) { v.ResourceID = "11111111-1111-4111-8111-111111119980" },
		"effect":         func(v *journal.UpdateStartReservation) { v.EffectID = "11111111-1111-4111-8111-111111119981" },
		"namespace":      func(v *journal.UpdateStartReservation) { v.NamespaceID = "11111111-1111-4111-8111-111111119982" },
		"source gen":     func(v *journal.UpdateStartReservation) { v.SourceGeneration = 2; v.TargetGeneration = 3 },
		"source release": func(v *journal.UpdateStartReservation) { v.SourceReleaseSHA256 = strings.Repeat("a", 64) },
		"target release": func(v *journal.UpdateStartReservation) { v.TargetReleaseSHA256 = strings.Repeat("b", 64) },
		"fence":          func(v *journal.UpdateStartReservation) { v.ControlFence = 3 },
		"source writer":  func(v *journal.UpdateStartReservation) { v.SourceWriterFence = 2; v.TargetWriterFence = 3 },
		"target writer":  func(v *journal.UpdateStartReservation) { v.TargetWriterFence = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := reservation
			mutate(&changed)
			body, _ := json.Marshal(changed)
			if validWritableLifecycleReply(UpdateStartReserve, body, ref, time.Now().Add(-time.Second), time.Now()) {
				t.Fatal("changed update-start reservation accepted")
			}
		})
	}
}

func TestWritableLifecycleUpdateStartRunningReceipt(t *testing.T) {
	a, command, raw, sig, descriptor, reservation := updateStartTransportFixture(t)
	now := time.Now().UnixMilli()
	receipt := updateStartRunningReceiptFixture(command, reservation)
	body, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ParseUpdateStartRunningReceipt(body); err != nil {
		t.Fatal("parse running receipt", err)
	}
	for _, action := range []WritableLifecycleAction{UpdateStartExecute, UpdateStartReceipt} {
		ref, err := verifyWritableLifecycle(action, raw, sig, descriptor, a, now)
		if err != nil || ref.updateStart.EffectID != command.EffectID {
			t.Fatal("update-start running reference", action, err)
		}
		if !validWritableLifecycleReply(action, body, ref, time.Now().Add(-time.Second), time.Now()) {
			t.Fatal("exact update-start running receipt refused", action)
		}
	}
	ref, err := verifyWritableLifecycle(UpdateStartExecute, raw, sig, descriptor, a, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*journal.UpdateStartRunningReceipt){
		"deployment":     func(v *journal.UpdateStartRunningReceipt) { v.DeploymentID = "11111111-1111-4111-8111-111111119980" },
		"operation":      func(v *journal.UpdateStartRunningReceipt) { v.OperationID = "11111111-1111-4111-8111-111111119981" },
		"namespace":      func(v *journal.UpdateStartRunningReceipt) { v.NamespaceID = "11111111-1111-4111-8111-111111119982" },
		"source release": func(v *journal.UpdateStartRunningReceipt) { v.SourceReleaseSHA256 = strings.Repeat("4", 64) },
		"target release": func(v *journal.UpdateStartRunningReceipt) { v.TargetReleaseSHA256 = strings.Repeat("5", 64) },
		"target gen":     func(v *journal.UpdateStartRunningReceipt) { v.TargetGeneration = "3" },
		"control fence":  func(v *journal.UpdateStartRunningReceipt) { v.ControlFence = "3" },
		"source writer":  func(v *journal.UpdateStartRunningReceipt) { v.SourceWriterFence = "2" },
		"target writer":  func(v *journal.UpdateStartRunningReceipt) { v.TargetWriterFence = "3" },
		"effect":         func(v *journal.UpdateStartRunningReceipt) { v.Claim.EffectID = "11111111-1111-4111-8111-111111119983" },
		"resource": func(v *journal.UpdateStartRunningReceipt) {
			v.Claim.ResourceID = "11111111-1111-4111-8111-111111119984"
		},
		"payload":        func(v *journal.UpdateStartRunningReceipt) { v.Claim.PayloadSHA256 = strings.Repeat("6", 64) },
		"checkpoint":     func(v *journal.UpdateStartRunningReceipt) { v.Claim.CheckpointReceiptSHA256 = strings.Repeat("7", 64) },
		"claim revision": func(v *journal.UpdateStartRunningReceipt) { v.Claim.ControlRevision = "99" },
		"claim fence":    func(v *journal.UpdateStartRunningReceipt) { v.Claim.AdmittedFence = "3" },
		"preflight":      func(v *journal.UpdateStartRunningReceipt) { v.Claim.Preflight = nil },
		"future observed": func(v *journal.UpdateStartRunningReceipt) {
			v.NativeObservedMS = strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10)
			v.SettledMS = v.NativeObservedMS
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			preflight := *receipt.Claim.Preflight
			changed.Claim.Preflight = &preflight
			mutate(&changed)
			encoded, _ := json.Marshal(changed)
			if validWritableLifecycleReply(UpdateStartExecute, encoded, ref, time.Now().Add(-time.Second), time.Now()) {
				t.Fatal("changed update-start running receipt accepted")
			}
		})
	}
}

func TestWritableLifecycleOpaqueTypedEvidence(t *testing.T) {
	var proof WritableLifecycleObservation
	if _, err := json.Marshal(proof); err == nil {
		t.Fatal("serialized proof")
	}
	proof.action = WritableRegistrationRead
	proof.body = `{}`
	proof.commandSHA = "old"
	if err := json.Unmarshal([]byte(`{}`), &proof); err == nil || proof.action != 0 || proof.commandSHA != "" {
		t.Fatal("decoded proof")
	}
	if _, err := proof.WriterReceipt(); err == nil {
		t.Fatal("zero writer")
	}
	if _, err := proof.RunningReceipt(); err == nil {
		t.Fatal("zero running")
	}
	if _, err := proof.Readiness(); err == nil {
		t.Fatal("zero readiness")
	}
	if _, err := proof.RegistrationReceipt(); err == nil {
		t.Fatal("zero registration")
	}
	a, _, _, _, _, r := writableHandshakeRequestFixture(t)
	body, _ := json.Marshal(r)
	proof = WritableLifecycleObservation{action: WritableRegistrationRead, body: string(body), authority: a}
	if _, err := proof.RegistrationReceipt(); err != nil {
		t.Fatal(err)
	}
	if _, err := proof.RunningReceipt(); err == nil {
		t.Fatal("delivery used as running")
	}
	_, _, _, _, _, removal := managedRemovalTransportFixture(t)
	removalBody, _ := json.Marshal(removal)
	proof = WritableLifecycleObservation{action: ManagedRemovalRead, body: string(removalBody), authority: a}
	if got, err := proof.ManagedRemovalReceipt(); err != nil || got != removal {
		t.Fatal("managed removal evidence", got, err)
	}
	proof.action = WritableRegistrationRead
	if _, err := proof.ManagedRemovalReceipt(); err == nil {
		t.Fatal("registration used as managed removal")
	}
	a, _, _, _, _, checkpoint := checkpointTransportFixture(t)
	checkpointBody, _ := json.Marshal(checkpoint)
	proof = WritableLifecycleObservation{action: CheckpointRead, body: string(checkpointBody), authority: a}
	if got, err := proof.CheckpointReceipt(); err != nil || got != checkpoint {
		t.Fatal("checkpoint evidence", got, err)
	}
	proof.action = ManagedRemovalRead
	if _, err := proof.CheckpointReceipt(); err == nil {
		t.Fatal("managed removal used as checkpoint")
	}
	a, _, _, _, _, restore := restoreTransportFixture(t)
	restoreBody, _ := json.Marshal(restore)
	proof = WritableLifecycleObservation{action: RestoreRead, body: string(restoreBody), authority: a}
	if got, err := proof.RestoreReceipt(); err != nil || got != restore {
		t.Fatal("restore evidence", got, err)
	}
	proof.action = CheckpointRead
	if _, err := proof.RestoreReceipt(); err == nil {
		t.Fatal("checkpoint used as restore")
	}
	a, _, _, _, _, reservation := updateStartTransportFixture(t)
	reservationBody, _ := json.Marshal(reservation)
	proof = WritableLifecycleObservation{action: UpdateStartRead, body: string(reservationBody), authority: a}
	if got, err := proof.UpdateStartReservation(); err != nil || got != reservation {
		t.Fatal("update-start reservation evidence", got, err)
	}
	proof.action = RestoreRead
	if _, err := proof.UpdateStartReservation(); err == nil {
		t.Fatal("restore used as update-start")
	}
	copy := proof.Controller()
	copy.PublicKey[0] ^= 1
	if proof.Controller().PublicKey[0] == copy.PublicKey[0] {
		t.Fatal("authority alias")
	}
}
