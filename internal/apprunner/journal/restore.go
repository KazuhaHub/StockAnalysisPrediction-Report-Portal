package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

var ErrRestoreUnavailable = errors.New("runner data restore is unavailable")

const restoreReceiptFormat = "portal.runner.data-restore-receipt/1-dev.1"

type RestoreReceipt struct {
	Format                      string `json:"format"`
	DeploymentID                string `json:"deployment_id"`
	ControllerEpoch             string `json:"controller_epoch"`
	InstallationID              string `json:"installation_id"`
	OperationID                 string `json:"operation_id"`
	TargetGeneration            string `json:"target_generation"`
	Stage                       string `json:"stage"`
	EffectID                    string `json:"effect_id"`
	PayloadSHA256               string `json:"payload_sha256"`
	RecoverySetID               string `json:"recovery_set_id"`
	CheckpointID                string `json:"checkpoint_id"`
	NamespaceID                 string `json:"namespace_id"`
	ReleaseSHA256               string `json:"release_sha256"`
	ManifestSHA256              string `json:"manifest_sha256"`
	ArchiveBytes                string `json:"archive_bytes"`
	ProjectID                   string `json:"project_id"`
	NamespaceInode              string `json:"namespace_inode"`
	Device                      string `json:"device"`
	DataFormat                  string `json:"data_format"`
	WriterFence                 string `json:"writer_fence"`
	SourceCheckpointOperationID string `json:"source_checkpoint_operation_id"`
	SourceCheckpointEffectID    string `json:"source_checkpoint_effect_id"`
	SourceCheckpointReceiptSHA  string `json:"source_checkpoint_receipt_sha256"`
	Entries                     string `json:"entries"`
	UsedBytes                   string `json:"used_bytes"`
	State                       string `json:"state"`
	ObservedMS                  string `json:"observed_ms"`
	SettledMS                   string `json:"settled_ms"`
}

func validRestoreReceipt(r RestoreReceipt) bool {
	if r.Format != restoreReceiptFormat || r.Stage != "restore_data" || r.State != "restored" ||
		!dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) ||
		!dataSHA(r.ManifestSHA256) || !dataSHA(r.SourceCheckpointReceiptSHA) {
		return false
	}
	for _, id := range []string{
		r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID,
		r.RecoverySetID, r.CheckpointID, r.NamespaceID,
		r.SourceCheckpointOperationID, r.SourceCheckpointEffectID,
	} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, value := range []string{
		r.TargetGeneration, r.ArchiveBytes, r.ProjectID, r.DataFormat,
		r.WriterFence, r.Entries, r.UsedBytes, r.ObservedMS, r.SettledMS,
	} {
		if !dataNumber(value, false) {
			return false
		}
	}
	if !nativeNumber(r.NamespaceInode, true) || !nativeNumber(r.Device, true) {
		return false
	}
	target, _ := strconv.ParseInt(r.TargetGeneration, 10, 64)
	archive, _ := strconv.ParseInt(r.ArchiveBytes, 10, 64)
	project, _ := strconv.ParseInt(r.ProjectID, 10, 64)
	format, _ := strconv.ParseInt(r.DataFormat, 10, 64)
	writer, _ := strconv.ParseInt(r.WriterFence, 10, 64)
	entries, _ := strconv.ParseInt(r.Entries, 10, 64)
	usedBytes, _ := strconv.ParseInt(r.UsedBytes, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return target > 0 && archive > 0 && archive <= maxCheckpointArchiveBytes &&
		project > 0 && project <= math.MaxUint32 && format > 0 && writer > 0 &&
		entries >= 0 && usedBytes >= 0 && observed > 0 && settled >= observed
}

func ParseRestoreReceipt(raw []byte) (RestoreReceipt, error) {
	var receipt RestoreReceipt
	if len(raw) == 0 || len(raw) > 32768 || !canonicalDataJSON(string(raw), &receipt) || !validRestoreReceipt(receipt) {
		return RestoreReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func restoreReceiptMatchesCommand(r RestoreReceipt, c control.RestoreCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch &&
		r.InstallationID == c.InstallationID && r.OperationID == c.OperationID &&
		r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && r.RecoverySetID == p.RecoverySetID &&
		r.CheckpointID == p.CheckpointID && r.NamespaceID == p.NamespaceID &&
		r.ReleaseSHA256 == p.ReleaseSHA256 && r.ManifestSHA256 == p.ManifestSHA256 &&
		r.ArchiveBytes == p.ArchiveBytes && r.ProjectID == p.ProjectID &&
		r.NamespaceInode == p.NamespaceInode && r.Device == p.Device &&
		r.DataFormat == p.DataFormat && r.WriterFence == p.WriterFence &&
		r.SourceCheckpointOperationID == p.SourceCheckpointOperationID &&
		r.SourceCheckpointEffectID == p.SourceCheckpointEffectID &&
		r.SourceCheckpointReceiptSHA == p.SourceCheckpointReceiptSHA
}

type restoreSource struct {
	command           control.RestoreCommand
	checkpointCommand control.CheckpointCommand
	checkpoint        CheckpointReceipt
	resource          managedRemovalResource
	raw, signature    []byte
}

func loadRestoreSource(ctx context.Context, tx *sql.Tx, command control.RestoreCommand, now int64) (restoreSource, error) {
	var result restoreSource
	result.command = command
	var storedOperation, storedInstallation, storedStage, storedKind, storedPayload, state, receipt string
	var storedGeneration int64
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,kind,payload_sha256,
		target_generation,state,receipt_json FROM runner_commands WHERE effect_id=?`,
		command.Payload.SourceCheckpointEffectID).
		Scan(&storedOperation, &storedInstallation, &storedStage, &storedKind, &storedPayload,
			&storedGeneration, &state, &receipt); err != nil {
		return result, ErrRestoreUnavailable
	}
	if storedOperation != command.Payload.SourceCheckpointOperationID ||
		storedInstallation != command.InstallationID || storedStage != "snapshot" ||
		storedKind != "snapshot" || state != "applied" ||
		candidateHash([]byte(receipt)) != command.Payload.SourceCheckpointReceiptSHA {
		return result, ErrFenceReconciliation
	}
	checkpoint, err := ParseCheckpointReceipt([]byte(receipt))
	if err != nil || checkpoint.PayloadSHA256 != storedPayload ||
		checkpoint.CheckpointID != command.Payload.CheckpointID ||
		checkpoint.NamespaceID != command.Payload.NamespaceID ||
		checkpoint.ReleaseSHA256 != command.Payload.ReleaseSHA256 ||
		checkpoint.ManifestSHA256 != command.Payload.ManifestSHA256 ||
		checkpoint.ArchiveBytes != command.Payload.ArchiveBytes ||
		checkpoint.ProjectID != command.Payload.ProjectID ||
		checkpoint.NamespaceInode != command.Payload.NamespaceInode ||
		checkpoint.Device != command.Payload.Device ||
		checkpoint.DataFormat != command.Payload.DataFormat ||
		checkpoint.WriterFence != command.Payload.WriterFence {
		return result, ErrFenceReconciliation
	}
	target, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
	if storedGeneration != target {
		return result, ErrFenceReconciliation
	}
	result.checkpointCommand = control.CheckpointCommand{
		Format: control.CheckpointFormat, DeploymentID: command.DeploymentID,
		ControllerEpoch: command.ControllerEpoch, ControlRevision: "1",
		InstallationID: command.InstallationID, OperationID: checkpoint.OperationID,
		TargetGeneration: checkpoint.TargetGeneration, Stage: checkpoint.Stage,
		EffectNumber: "1", EffectID: checkpoint.EffectID, Kind: "snapshot",
		InstallationFence: "1", PayloadSHA256: checkpoint.PayloadSHA256,
		Payload: control.CheckpointPayload{
			CheckpointID: checkpoint.CheckpointID, NamespaceID: checkpoint.NamespaceID,
			ResourceID: checkpoint.ResourceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
			SourceOperationID:       checkpoint.SourceOperationID,
			SourceStartEffectID:     checkpoint.SourceStartEffectID,
			SourceReceiptSHA256:     checkpoint.SourceReceiptSHA256,
			SourceDataEffectID:      checkpoint.SourceDataEffectID,
			SourceDataReceiptSHA256: checkpoint.SourceDataReceiptSHA256,
			ProjectID:               checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
			Device: checkpoint.Device, DataFormat: checkpoint.DataFormat,
			WriterFence: checkpoint.WriterFence,
		},
	}
	digest, err := control.CheckpointLogicalDigest(result.checkpointCommand)
	if err != nil || digest != checkpoint.PayloadSHA256 {
		return result, ErrFenceReconciliation
	}
	if err := validateRestoreCheckpointRecord(ctx, tx, result.checkpointCommand, checkpoint.ManifestSHA256, mustDataInt(checkpoint.ArchiveBytes)); err != nil {
		return result, ErrFenceReconciliation
	}
	history, err := loadCheckpointSourceExcluding(ctx, tx, result.checkpointCommand, now, command.EffectID)
	if err != nil {
		return result, ErrFenceReconciliation
	}
	result.checkpoint = checkpoint
	result.resource = history.resource
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_resources
		WHERE data_namespace_id=? AND kind='container'
		AND observed_state NOT IN ('stopped','absent')`,
		command.Payload.NamespaceID).Scan(&running); err != nil || running != 0 {
		return result, ErrFenceReconciliation
	}
	return result, nil
}

func validateRestoreCheckpointRecord(ctx context.Context, tx *sql.Tx, command control.CheckpointCommand, manifest string, bytes int64) error {
	var id, namespace, installation, operation, release, state string
	var format, writer, revision int64
	var storedManifest sql.NullString
	var storedBytes, verifiedMS sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id,data_namespace_id,installation_id,operation_id,
		release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms
		FROM runner_checkpoints WHERE id=?`, command.Payload.CheckpointID).
		Scan(&id, &namespace, &installation, &operation, &release, &format, &writer,
			&storedManifest, &storedBytes, &state, &revision, &verifiedMS); err != nil {
		return ErrFenceReconciliation
	}
	if id != command.Payload.CheckpointID || namespace != command.Payload.NamespaceID ||
		installation != command.InstallationID || operation != command.OperationID ||
		release != command.Payload.ReleaseSHA256 || format != mustDataInt(command.Payload.DataFormat) ||
		writer != mustDataInt(command.Payload.WriterFence) || !storedManifest.Valid ||
		storedManifest.String != manifest || !storedBytes.Valid || storedBytes.Int64 != bytes ||
		!verifiedMS.Valid || verifiedMS.Int64 <= 0 ||
		!((state == "verified" && revision == 2) || (state == "restoring" && revision == 3)) {
		return ErrFenceReconciliation
	}
	return nil
}

func (s *Store) beginRestore(ctx context.Context, raw, signature []byte) (restoreSource, bool, error) {
	var zero restoreSource
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, false, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, false, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyRestore(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	command := verified.Command()
	var storedOperation, storedInstallation, storedStage, storedKind, storedPayload, storedEpoch string
	var storedSlot, storedGeneration, storedFence int64
	var state, receipt string
	err = tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,effect_number,kind,
		payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json
		FROM runner_commands WHERE effect_id=?`, command.EffectID).
		Scan(&storedOperation, &storedInstallation, &storedStage, &storedSlot, &storedKind,
			&storedPayload, &storedGeneration, &storedEpoch, &storedFence, &state, &receipt)
	if err == nil {
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		if storedOperation != command.OperationID || storedInstallation != command.InstallationID ||
			storedStage != command.Stage || storedKind != command.Kind ||
			storedPayload != command.PayloadSHA256 || storedGeneration != generation ||
			storedEpoch != command.ControllerEpoch || storedSlot != slot {
			return zero, false, ErrCommandConflict
		}
		if state == "applied" {
			parsed, err := ParseRestoreReceipt([]byte(receipt))
			if err != nil || !restoreReceiptMatchesCommand(parsed, command) {
				return zero, false, ErrFenceReconciliation
			}
			return zero, true, nil
		}
		if state != "prepared" || receipt != "{}" {
			return zero, false, ErrFenceReconciliation
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		source, err := loadRestoreSource(ctx, tx, command, now)
		if err != nil {
			return zero, false, err
		}
		fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
		if fence < source.resource.controlFence {
			return zero, false, ErrFenceReconciliation
		}
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`,
			command.EffectID, command.OperationID, command.InstallationID, command.Stage, slot,
			command.Kind, command.PayloadSHA256, generation, command.ControllerEpoch, fence, now, now); err != nil {
			return zero, false, err
		}
		changed, err := tx.ExecContext(ctx, `UPDATE runner_checkpoints SET state='restoring',revision=revision+1,updated_ms=?
			WHERE id=? AND data_namespace_id=? AND installation_id=? AND operation_id=?
			AND release_sha256=? AND data_format=? AND writer_fence=? AND manifest_sha256=?
			AND bytes=? AND state='verified' AND revision=2 AND verified_ms IS NOT NULL`,
			now, command.Payload.CheckpointID, command.Payload.NamespaceID, command.InstallationID,
			command.Payload.SourceCheckpointOperationID, command.Payload.ReleaseSHA256,
			mustDataInt(command.Payload.DataFormat), mustDataInt(command.Payload.WriterFence),
			command.Payload.ManifestSHA256, mustDataInt(command.Payload.ArchiveBytes))
		if err != nil {
			return zero, false, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, false, ErrFenceReconciliation
		}
	}
	source, err := loadRestoreSource(ctx, tx, command, now)
	if err != nil {
		return zero, false, err
	}
	var checkpointState string
	var checkpointRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT state,revision FROM runner_checkpoints WHERE id=?`,
		command.Payload.CheckpointID).Scan(&checkpointState, &checkpointRevision); err != nil ||
		checkpointState != "restoring" || checkpointRevision != 3 {
		return zero, false, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',updated_ms=?
		WHERE effect_id=? AND state='prepared' AND receipt_json='{}'`, now, command.EffectID)
	if err != nil {
		return zero, false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, false, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return source, false, nil
}

func (s *Store) settleRestore(ctx context.Context, source restoreSource, observation data.RestoreObservation) (RestoreReceipt, error) {
	var zero RestoreReceipt
	snapshot, err := observation.Snapshot()
	if err != nil || snapshot.NamespaceID != source.command.Payload.NamespaceID ||
		snapshot.ReleaseSHA256 != source.command.Payload.ReleaseSHA256 ||
		snapshot.ManifestSHA256 != source.command.Payload.ManifestSHA256 ||
		snapshot.ProjectID != uint32(mustDataInt(source.command.Payload.ProjectID)) ||
		snapshot.DataFormat != mustDataInt(source.command.Payload.DataFormat) ||
		snapshot.NamespaceDevice != mustDataUint(source.command.Payload.Device) ||
		snapshot.NamespaceInode != mustDataUint(source.command.Payload.NamespaceInode) ||
		snapshot.Entries < 0 {
		return zero, ErrFenceReconciliation
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := control.VerifyRestore(source.raw, source.signature, authority, now); err != nil {
		return zero, ErrFenceReconciliation
	}
	current, err := loadRestoreSource(ctx, tx, source.command, now)
	if err != nil || !sameRestoreSource(source, current) {
		return zero, ErrFenceReconciliation
	}
	var state, receipt string
	if err := tx.QueryRowContext(ctx, `SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`,
		source.command.EffectID).Scan(&state, &receipt); err != nil ||
		state != "unknown" || receipt != "{}" {
		return zero, ErrFenceReconciliation
	}
	result := RestoreReceipt{
		Format: restoreReceiptFormat, DeploymentID: source.command.DeploymentID,
		ControllerEpoch: source.command.ControllerEpoch,
		InstallationID:  source.command.InstallationID, OperationID: source.command.OperationID,
		TargetGeneration: source.command.TargetGeneration, Stage: source.command.Stage,
		EffectID: source.command.EffectID, PayloadSHA256: source.command.PayloadSHA256,
		RecoverySetID:  source.command.Payload.RecoverySetID,
		CheckpointID:   source.command.Payload.CheckpointID,
		NamespaceID:    source.command.Payload.NamespaceID,
		ReleaseSHA256:  source.command.Payload.ReleaseSHA256,
		ManifestSHA256: source.command.Payload.ManifestSHA256,
		ArchiveBytes:   source.command.Payload.ArchiveBytes,
		ProjectID:      source.command.Payload.ProjectID,
		NamespaceInode: source.command.Payload.NamespaceInode,
		Device:         source.command.Payload.Device, DataFormat: source.command.Payload.DataFormat,
		WriterFence:                 source.command.Payload.WriterFence,
		SourceCheckpointOperationID: source.command.Payload.SourceCheckpointOperationID,
		SourceCheckpointEffectID:    source.command.Payload.SourceCheckpointEffectID,
		SourceCheckpointReceiptSHA:  source.command.Payload.SourceCheckpointReceiptSHA,
		Entries:                     strconv.FormatInt(snapshot.Entries, 10),
		UsedBytes:                   strconv.FormatInt(snapshot.UsedBytes, 10), State: "restored",
		ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10),
		SettledMS:  strconv.FormatInt(now, 10),
	}
	if !validRestoreReceipt(result) {
		return zero, ErrFenceReconciliation
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 32768 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND state='unknown' AND receipt_json='{}'`, string(encoded), now, result.EffectID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_checkpoints SET state='verified',revision=revision+1,updated_ms=?
		WHERE id=? AND state='restoring' AND revision=3 AND manifest_sha256=? AND bytes=?`,
		now, result.CheckpointID, result.ManifestSHA256, mustDataInt(result.ArchiveBytes))
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func sameRestoreSource(a, b restoreSource) bool {
	return a.command == b.command && a.checkpointCommand == b.checkpointCommand &&
		a.checkpoint == b.checkpoint && a.resource == b.resource
}

func (s *Store) DispatchRestore(ctx context.Context, raw, signature []byte, dataRoot *data.Root, dataPin data.RootIdentity, checkpointRoot *data.Root, checkpointPin data.RootIdentity) (RestoreReceipt, error) {
	var zero RestoreReceipt
	if dataRoot == nil || checkpointRoot == nil || s.closing.Load() {
		return zero, ErrRestoreUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	source, replay, err := s.beginRestore(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	source.raw, source.signature = raw, signature
	if replay {
		return s.ReadRestoreReceipt(ctx, raw, signature)
	}
	request := data.RestoreRequest{
		CheckpointID:           source.command.Payload.CheckpointID,
		NamespaceID:            source.command.Payload.NamespaceID,
		ReleaseSHA256:          source.command.Payload.ReleaseSHA256,
		ManifestSHA256:         source.command.Payload.ManifestSHA256,
		ProjectID:              uint32(mustDataInt(source.command.Payload.ProjectID)),
		DataFormat:             mustDataInt(source.command.Payload.DataFormat),
		ArchiveBytes:           mustDataInt(source.command.Payload.ArchiveBytes),
		MaxArchiveBytes:        maxCheckpointArchiveBytes,
		ExpectedDevice:         mustDataUint(source.command.Payload.Device),
		ExpectedNamespaceInode: mustDataUint(source.command.Payload.NamespaceInode),
		WriterFence:            mustDataInt(source.command.Payload.WriterFence),
	}
	observation, err := checkpointRoot.Restore(ctx, checkpointPin, dataRoot, dataPin, request)
	if err != nil {
		return zero, err
	}
	return s.settleRestore(ctx, source, observation)
}

func (s *Store) ReadRestoreReceipt(ctx context.Context, raw, signature []byte) (RestoreReceipt, error) {
	var zero RestoreReceipt
	if s.closing.Load() {
		return zero, ErrRestoreUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey,
		Fingerprint: deployment.Fingerprint,
	}
	verified, err := control.VerifyRestore(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	var receipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands
		WHERE effect_id=? AND operation_id=? AND installation_id=? AND state='applied'`,
		command.EffectID, command.OperationID, command.InstallationID).Scan(&receipt); err != nil {
		return zero, ErrRestoreUnavailable
	}
	result, err := ParseRestoreReceipt([]byte(receipt))
	if err != nil || !restoreReceiptMatchesCommand(result, command) {
		return zero, ErrFenceReconciliation
	}
	var state string
	var revision int64
	var manifest string
	var bytes int64
	if err := tx.QueryRowContext(ctx, `SELECT state,revision,manifest_sha256,bytes
		FROM runner_checkpoints WHERE id=? AND data_namespace_id=? AND installation_id=?
		AND operation_id=? AND release_sha256=? AND data_format=? AND writer_fence=?`,
		result.CheckpointID, result.NamespaceID, result.InstallationID,
		result.SourceCheckpointOperationID, result.ReleaseSHA256,
		mustDataInt(result.DataFormat), mustDataInt(result.WriterFence)).
		Scan(&state, &revision, &manifest, &bytes); err != nil ||
		state != "verified" || revision != 4 || manifest != result.ManifestSHA256 ||
		bytes != mustDataInt(result.ArchiveBytes) {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
