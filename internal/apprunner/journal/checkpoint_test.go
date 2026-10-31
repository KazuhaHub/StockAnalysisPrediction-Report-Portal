//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

type checkpointFixture struct {
	store   *Store
	command control.CheckpointCommand
	raw     []byte
	sig     []byte
	source  CandidateRunningReceipt
	data    DataPreparationReceipt
}

func newCheckpointFixture(t *testing.T) checkpointFixture {
	t.Helper()
	s, _, _, source := appliedCandidateReceiptFixture(t)
	var sourceRaw, sourcePayload, sourceOperation string
	var sourceFence int64
	if err := s.db.QueryRow(`SELECT operation_id,admitted_fence,payload_sha256,receipt_json FROM runner_commands WHERE effect_id=? AND kind='start-candidate' AND state='applied'`,
		source.Claim.EffectID).Scan(&sourceOperation, &sourceFence, &sourcePayload, &sourceRaw); err != nil {
		t.Fatal(err)
	}
	var dataRaw, dataEffect, dataPayload string
	var dataSlot, dataFence int64
	if err := s.db.QueryRow(`SELECT effect_id,effect_number,payload_sha256,receipt_json,admitted_fence FROM runner_commands WHERE operation_id=? AND stage='prepare_data' AND effect_number=1 AND kind='prepare-data' AND state='applied'`,
		sourceOperation).Scan(&dataEffect, &dataSlot, &dataPayload, &dataRaw, &dataFence); err != nil {
		t.Fatal(err)
	}
	dataReceipt, err := ParseDataPreparationReceipt([]byte(dataRaw))
	if err != nil || dataSlot != 1 || dataFence != sourceFence || dataPayload != dataReceipt.PayloadSHA256 {
		t.Fatal("checkpoint data source", err)
	}
	now := time.Now().UnixMilli()
	if _, err := s.db.Exec(`UPDATE runner_resources SET desired_state='stopped',observed_state='stopped',revision=3,observed_ms=?,updated_ms=? WHERE id=? AND native_id=?`,
		now, now, source.Claim.ResourceID, source.NativeID); err != nil {
		t.Fatal(err)
	}
	command := control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: source.DeploymentID, ControllerEpoch: source.ControllerEpoch,
		ControlRevision: source.ControlRevision, InstallationID: source.InstallationID,
		OperationID: fixtureID(880), TargetGeneration: "2", Stage: "snapshot",
		EffectNumber: "1", EffectID: fixtureID(881), Kind: "snapshot", InstallationFence: strconv.FormatInt(sourceFence, 10),
		Payload: control.CheckpointPayload{
			CheckpointID: fixtureID(882), NamespaceID: source.DataNamespaceID, ResourceID: source.Claim.ResourceID,
			ReleaseSHA256: source.ReleaseSHA256, SourceOperationID: sourceOperation,
			SourceStartEffectID: source.Claim.EffectID, SourceReceiptSHA256: candidateHash([]byte(sourceRaw)),
			SourceDataEffectID: dataEffect, SourceDataReceiptSHA256: candidateHash([]byte(dataRaw)),
			ProjectID: dataReceipt.ProjectID, NamespaceInode: dataReceipt.NamespaceInode, Device: dataReceipt.Device,
			DataFormat: dataReceipt.DataFormat, WriterFence: dataReceipt.WriterFence,
		},
	}
	command.PayloadSHA256, err = control.CheckpointLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignCheckpoint(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil || sourcePayload != source.PayloadSHA256 {
		t.Fatal("checkpoint fixture", err)
	}
	return checkpointFixture{store: s, command: command, raw: raw, sig: sig, source: source, data: dataReceipt}
}

func TestCheckpointPersistsOneExactUnknownIntent(t *testing.T) {
	f := newCheckpointFixture(t)
	source, replay, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig)
	if err != nil || replay || source.source.NativeID != f.source.NativeID {
		t.Fatal(source, replay, err)
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=? AND state='unknown' AND receipt_json='{}'`, f.command.EffectID).Scan(&count); err != nil || count != 1 {
		t.Fatal("checkpoint intent not durably unknown")
	}
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_checkpoints WHERE id=? AND operation_id=? AND state='preparing' AND revision=1 AND manifest_sha256 IS NULL AND bytes=0`,
		f.command.Payload.CheckpointID, f.command.OperationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("checkpoint repository intent not durable")
	}
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("unknown checkpoint reclaimed", err)
	}
}

func TestCheckpointRejectsChangedSourceAndPendingWork(t *testing.T) {
	for _, name := range []string{"source state", "source hash", "data hash", "resource", "pending", "target", "fence"} {
		t.Run(name, func(t *testing.T) {
			f := newCheckpointFixture(t)
			raw, sig := f.raw, f.sig
			switch name {
			case "source state":
				if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='unknown' WHERE effect_id=? AND kind='start-candidate'`, f.source.Claim.EffectID); err != nil {
					t.Fatal(err)
				}
			case "source hash":
				raw, sig = resignCheckpoint(t, f, func(c *control.CheckpointCommand) { c.Payload.SourceReceiptSHA256 = strings.Repeat("d", 64) })
			case "data hash":
				raw, sig = resignCheckpoint(t, f, func(c *control.CheckpointCommand) { c.Payload.SourceDataReceiptSHA256 = strings.Repeat("e", 64) })
			case "resource":
				if _, err := f.store.db.Exec(`UPDATE runner_resources SET observed_state='running',revision=2 WHERE id=?`, f.source.Claim.ResourceID); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if _, err := f.store.db.Exec(`INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms) VALUES(?,?,?,'start',2,'start-candidate',?,1,?,1,'prepared','{}',NULL,1,1)`, fixtureID(883), f.source.OperationID, f.source.InstallationID, strings.Repeat("f", 64), f.source.ControllerEpoch); err != nil {
					t.Fatal(err)
				}
			case "target":
				raw, sig = resignCheckpoint(t, f, func(c *control.CheckpointCommand) { c.TargetGeneration = "3" })
			case "fence":
				if _, err := f.store.db.Exec(`UPDATE runner_commands SET admitted_fence=2 WHERE effect_id=?`, f.source.Claim.EffectID); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.store.beginCheckpoint(context.Background(), raw, sig); err == nil {
				t.Fatalf("changed %s admitted", name)
			}
			var count int
			if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, f.command.EffectID).Scan(&count); err != nil || count != 0 {
				t.Fatal("refusal wrote checkpoint intent")
			}
		})
	}
}

func TestCheckpointReceiptReadMatchesAppliedIntent(t *testing.T) {
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	receipt := CheckpointReceipt{
		Format: checkpointReceiptFormat, DeploymentID: f.command.DeploymentID, ControllerEpoch: f.command.ControllerEpoch,
		InstallationID: f.command.InstallationID, OperationID: f.command.OperationID, TargetGeneration: f.command.TargetGeneration,
		Stage: f.command.Stage, EffectID: f.command.EffectID, PayloadSHA256: f.command.PayloadSHA256,
		CheckpointID: f.command.Payload.CheckpointID, NamespaceID: f.command.Payload.NamespaceID, ResourceID: f.command.Payload.ResourceID,
		ReleaseSHA256: f.command.Payload.ReleaseSHA256, SourceOperationID: f.command.Payload.SourceOperationID,
		SourceStartEffectID: f.command.Payload.SourceStartEffectID, SourceReceiptSHA256: f.command.Payload.SourceReceiptSHA256,
		SourceDataEffectID: f.command.Payload.SourceDataEffectID, SourceDataReceiptSHA256: f.command.Payload.SourceDataReceiptSHA256,
		ProjectID: f.command.Payload.ProjectID, NamespaceInode: f.command.Payload.NamespaceInode, Device: f.command.Payload.Device,
		DataFormat: f.command.Payload.DataFormat, WriterFence: f.command.Payload.WriterFence,
		ArchiveBytes: "1024", Entries: "3", ManifestSHA256: strings.Repeat("a", 64),
		ObservedMS: strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	raw, _ := json.Marshal(receipt)
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(raw), now, f.command.EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.Exec(`UPDATE runner_checkpoints SET manifest_sha256=?,bytes=1024,state='verified',revision=2,verified_ms=?,updated_ms=? WHERE id=?`,
		receipt.ManifestSHA256, now, now, receipt.CheckpointID); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.ReadCheckpointReceipt(context.Background(), f.raw, f.sig)
	if err != nil || got != receipt {
		t.Fatal(got, err)
	}
	changed := receipt
	changed.SourceStartEffectID = fixtureID(884)
	raw, _ = json.Marshal(changed)
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(raw), f.command.EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ReadCheckpointReceipt(context.Background(), f.raw, f.sig); err == nil {
		t.Fatal("changed checkpoint receipt read as applied intent")
	}
	if _, err := f.store.db.Exec(`UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(raw), f.command.EffectID); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRepositoryBindingRejectsChangedRecord(t *testing.T) {
	for _, mutation := range []string{"manifest", "bytes", "operation", "state"} {
		t.Run(mutation, func(t *testing.T) {
			f := newCheckpointFixture(t)
			if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "manifest":
				_, err := f.store.db.Exec(`UPDATE runner_checkpoints SET manifest_sha256=? WHERE id=?`, strings.Repeat("a", 64), f.command.Payload.CheckpointID)
				if err != nil {
					t.Fatal(err)
				}
			case "bytes":
				_, err := f.store.db.Exec(`UPDATE runner_checkpoints SET bytes=1 WHERE id=?`, f.command.Payload.CheckpointID)
				if err != nil {
					t.Fatal(err)
				}
			case "operation":
				_, err := f.store.db.Exec(`UPDATE runner_checkpoints SET operation_id=? WHERE id=?`, fixtureID(885), f.command.Payload.CheckpointID)
				if err != nil {
					t.Fatal(err)
				}
			case "state":
				_, err := f.store.db.Exec(`UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, f.command.Payload.CheckpointID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); !errors.Is(err, ErrFenceReconciliation) {
				t.Fatal("changed checkpoint repository binding accepted", err)
			}
		})
	}
}

func resignCheckpoint(t *testing.T, f checkpointFixture, mutate func(*control.CheckpointCommand)) ([]byte, []byte) {
	t.Helper()
	c := f.command
	mutate(&c)
	digest, err := control.CheckpointLogicalDigest(c)
	if err != nil {
		t.Fatal(err)
	}
	c.PayloadSHA256 = digest
	now := time.Now().UnixMilli()
	c.IssuedMS, c.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch, ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey, Fingerprint: enrollment.Fingerprint}
	raw, sig, err := control.SignCheckpoint(c, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return raw, sig
}
