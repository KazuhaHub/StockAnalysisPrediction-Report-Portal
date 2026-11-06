//go:build linux || darwin

package journal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

func appliedCheckpointFixture(t *testing.T) (checkpointFixture, CheckpointReceipt) {
	t.Helper()
	f := newCheckpointFixture(t)
	if _, _, err := f.store.beginCheckpoint(context.Background(), f.raw, f.sig); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	receipt := CheckpointReceipt{
		Format: checkpointReceiptFormat, DeploymentID: f.command.DeploymentID,
		ControllerEpoch: f.command.ControllerEpoch, InstallationID: f.command.InstallationID,
		OperationID: f.command.OperationID, TargetGeneration: f.command.TargetGeneration,
		Stage: f.command.Stage, EffectID: f.command.EffectID, PayloadSHA256: f.command.PayloadSHA256,
		CheckpointID: f.command.Payload.CheckpointID, NamespaceID: f.command.Payload.NamespaceID,
		ResourceID: f.command.Payload.ResourceID, ReleaseSHA256: f.command.Payload.ReleaseSHA256,
		SourceOperationID:       f.command.Payload.SourceOperationID,
		SourceStartEffectID:     f.command.Payload.SourceStartEffectID,
		SourceReceiptSHA256:     f.command.Payload.SourceReceiptSHA256,
		SourceDataEffectID:      f.command.Payload.SourceDataEffectID,
		SourceDataReceiptSHA256: f.command.Payload.SourceDataReceiptSHA256,
		ProjectID:               f.command.Payload.ProjectID, NamespaceInode: f.command.Payload.NamespaceInode,
		Device: f.command.Payload.Device, DataFormat: f.command.Payload.DataFormat,
		WriterFence: f.command.Payload.WriterFence, ArchiveBytes: "4096", Entries: "3",
		ManifestSHA256: strings.Repeat("a", 64),
		ObservedMS:     strconv.FormatInt(now, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	raw, _ := json.Marshal(receipt)
	mustJournalExec(t, f.store.db, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(raw), now, f.command.EffectID)
	mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET manifest_sha256=?,bytes=4096,state='verified',revision=2,verified_ms=?,updated_ms=? WHERE id=?`,
		receipt.ManifestSHA256, now, now, receipt.CheckpointID)
	return f, receipt
}

func restoreJournalFixture(t *testing.T) (checkpointFixture, CheckpointReceipt, control.RestoreCommand, []byte, []byte) {
	t.Helper()
	f, checkpoint := appliedCheckpointFixture(t)
	now := time.Now().UTC().UnixMilli()
	receiptRaw, _ := json.Marshal(checkpoint)
	sourceFence, err := strconv.ParseInt(f.source.Claim.AdmittedFence, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	command := control.RestoreCommand{
		Format: control.RestoreFormat, DeploymentID: f.command.DeploymentID,
		ControllerEpoch: f.command.ControllerEpoch, ControlRevision: "3",
		InstallationID: f.command.InstallationID, OperationID: fixtureID(890),
		TargetGeneration: checkpoint.TargetGeneration, Stage: "restore_data",
		EffectNumber: "1", EffectID: fixtureID(891), Kind: "restore",
		InstallationFence: strconv.FormatInt(sourceFence, 10),
		Payload: control.RestorePayload{
			RecoverySetID: fixtureID(892), CheckpointID: checkpoint.CheckpointID,
			NamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
			ManifestSHA256: checkpoint.ManifestSHA256, ArchiveBytes: checkpoint.ArchiveBytes,
			ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
			Device: checkpoint.Device, DataFormat: checkpoint.DataFormat,
			WriterFence:                 checkpoint.WriterFence,
			SourceCheckpointOperationID: checkpoint.OperationID,
			SourceCheckpointEffectID:    checkpoint.EffectID,
			SourceCheckpointReceiptSHA:  candidateHash(receiptRaw),
		},
	}
	command.PayloadSHA256, err = control.RestoreLogicalDigest(command)
	if err != nil {
		t.Fatal(err)
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(now, 10), strconv.FormatInt(now+25000, 10)
	enrollment := enrollmentFixture(30)
	authority := control.Authority{
		DeploymentID: enrollment.DeploymentID, ControllerEpoch: enrollment.ControllerEpoch,
		ControlRevision: enrollment.ControlRevision, PublicKey: enrollment.PublicKey,
		Fingerprint: enrollment.Fingerprint,
	}
	raw, signature, err := control.SignRestore(command, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{30}, 32)), authority, now)
	if err != nil {
		t.Fatal(err)
	}
	return f, checkpoint, command, raw, signature
}

func mustJournalExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	_, err := db.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRestorePersistsCheckpointBoundIntent(t *testing.T) {
	f, _, command, raw, signature := restoreJournalFixture(t)
	source, replay, err := f.store.beginRestore(context.Background(), raw, signature)
	if err != nil || replay || source.command != command {
		t.Fatal("restore intent", source.command, replay, err)
	}
	var state string
	var revision int64
	if err := f.store.db.QueryRow(`SELECT state,revision FROM runner_checkpoints WHERE id=?`, command.Payload.CheckpointID).Scan(&state, &revision); err != nil ||
		state != "restoring" || revision != 3 {
		t.Fatal("checkpoint restore intent", state, revision, err)
	}
	if err := f.store.db.QueryRow(`SELECT state FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&state); err != nil || state != "unknown" {
		t.Fatal("restore command intent", state, err)
	}
	if _, _, err := f.store.beginRestore(context.Background(), raw, signature); !errors.Is(err, ErrFenceReconciliation) {
		t.Fatal("unknown restore was reclaimed", err)
	}
}

func TestRestoreReceiptReadMatchesAppliedIntent(t *testing.T) {
	f, _, command, raw, signature := restoreJournalFixture(t)
	if _, _, err := f.store.beginRestore(context.Background(), raw, signature); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().UnixMilli()
	receipt := RestoreReceipt{
		Format: restoreReceiptFormat, DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, InstallationID: command.InstallationID,
		OperationID: command.OperationID, TargetGeneration: command.TargetGeneration,
		Stage: command.Stage, EffectID: command.EffectID, PayloadSHA256: command.PayloadSHA256,
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
	encoded, _ := json.Marshal(receipt)
	mustJournalExec(t, f.store.db, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=?`, string(encoded), now, command.EffectID)
	mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET state='verified',revision=4,updated_ms=? WHERE id=?`, now, command.Payload.CheckpointID)
	got, err := f.store.ReadRestoreReceipt(context.Background(), raw, signature)
	if err != nil || got != receipt {
		t.Fatal("restore receipt read", got, err)
	}
	changed := receipt
	changed.ManifestSHA256 = strings.Repeat("b", 64)
	encoded, _ = json.Marshal(changed)
	mustJournalExec(t, f.store.db, `UPDATE runner_commands SET receipt_json=? WHERE effect_id=?`, string(encoded), command.EffectID)
	if _, err := f.store.ReadRestoreReceipt(context.Background(), raw, signature); err == nil {
		t.Fatal("changed restore receipt accepted")
	}
}

func TestRestoreRefusesChangedCheckpointAndRunningResource(t *testing.T) {
	for _, mode := range []string{"checkpoint state", "manifest", "source receipt", "running resource"} {
		t.Run(mode, func(t *testing.T) {
			f, checkpoint, _, raw, signature := restoreJournalFixture(t)
			switch mode {
			case "checkpoint state":
				mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET state='review_required' WHERE id=?`, checkpoint.CheckpointID)
			case "manifest":
				mustJournalExec(t, f.store.db, `UPDATE runner_checkpoints SET manifest_sha256=? WHERE id=?`, strings.Repeat("c", 64), checkpoint.CheckpointID)
			case "source receipt":
				mustJournalExec(t, f.store.db, `UPDATE runner_commands SET receipt_json='{}' WHERE effect_id=?`, checkpoint.EffectID)
			case "running resource":
				mustJournalExec(t, f.store.db, `UPDATE runner_resources SET observed_state='running' WHERE id=?`, checkpoint.ResourceID)
			}
			if _, _, err := f.store.beginRestore(context.Background(), raw, signature); err == nil {
				t.Fatal("changed restore source admitted")
			}
			if scalarJournalInt(t, f.store.db, `SELECT COUNT(*) FROM runner_commands WHERE effect_id=?`, fixtureID(891)) != 0 {
				t.Fatal("refused restore wrote command")
			}
		})
	}
}

func scalarJournalInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if err := db.QueryRow(query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
