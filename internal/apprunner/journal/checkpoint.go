package journal

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

var ErrCheckpointUnavailable = errors.New("runner data checkpoint is unavailable")

const checkpointReceiptFormat = "portal.runner.data-checkpoint-receipt/1-dev.1"
const maxCheckpointArchiveBytes int64 = 512 << 20

type CheckpointReceipt struct {
	Format                  string `json:"format"`
	DeploymentID            string `json:"deployment_id"`
	ControllerEpoch         string `json:"controller_epoch"`
	InstallationID          string `json:"installation_id"`
	OperationID             string `json:"operation_id"`
	TargetGeneration        string `json:"target_generation"`
	Stage                   string `json:"stage"`
	EffectID                string `json:"effect_id"`
	PayloadSHA256           string `json:"payload_sha256"`
	CheckpointID            string `json:"checkpoint_id"`
	NamespaceID             string `json:"namespace_id"`
	ResourceID              string `json:"resource_id"`
	ReleaseSHA256           string `json:"release_sha256"`
	SourceOperationID       string `json:"source_operation_id"`
	SourceStartEffectID     string `json:"source_start_effect_id"`
	SourceReceiptSHA256     string `json:"source_receipt_sha256"`
	SourceDataEffectID      string `json:"source_data_effect_id"`
	SourceDataReceiptSHA256 string `json:"source_data_receipt_sha256"`
	ProjectID               string `json:"project_id"`
	NamespaceInode          string `json:"namespace_inode"`
	Device                  string `json:"device"`
	DataFormat              string `json:"data_format"`
	WriterFence             string `json:"writer_fence"`
	ArchiveBytes            string `json:"archive_bytes"`
	Entries                 string `json:"entries"`
	ManifestSHA256          string `json:"manifest_sha256"`
	ObservedMS              string `json:"observed_ms"`
	SettledMS               string `json:"settled_ms"`
}

func validCheckpointReceipt(r CheckpointReceipt) bool {
	if r.Format != checkpointReceiptFormat || r.Stage != "snapshot" || !dataSHA(r.PayloadSHA256) ||
		!dataSHA(r.ReleaseSHA256) || !dataSHA(r.SourceReceiptSHA256) || !dataSHA(r.SourceDataReceiptSHA256) || !dataSHA(r.ManifestSHA256) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.EffectID, r.CheckpointID,
		r.NamespaceID, r.ResourceID, r.SourceOperationID, r.SourceStartEffectID, r.SourceDataEffectID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, value := range []string{r.TargetGeneration, r.ProjectID, r.DataFormat, r.WriterFence, r.ArchiveBytes, r.Entries, r.ObservedMS, r.SettledMS} {
		if !dataNumber(value, false) {
			return false
		}
	}
	if !nativeNumber(r.NamespaceInode, true) || !nativeNumber(r.Device, true) {
		return false
	}
	target, _ := strconv.ParseInt(r.TargetGeneration, 10, 64)
	project, _ := strconv.ParseInt(r.ProjectID, 10, 64)
	inode, _ := strconv.ParseUint(r.NamespaceInode, 10, 64)
	device, _ := strconv.ParseUint(r.Device, 10, 64)
	format, _ := strconv.ParseInt(r.DataFormat, 10, 64)
	writer, _ := strconv.ParseInt(r.WriterFence, 10, 64)
	archive, _ := strconv.ParseInt(r.ArchiveBytes, 10, 64)
	entries, _ := strconv.ParseInt(r.Entries, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return target > 0 && project > 0 && project <= math.MaxUint32 && inode > 0 && device > 0 && format > 0 && writer > 0 &&
		archive > 0 && archive <= maxCheckpointArchiveBytes && entries >= 0 && observed > 0 && settled >= observed
}

func ParseCheckpointReceipt(raw []byte) (CheckpointReceipt, error) {
	var receipt CheckpointReceipt
	if len(raw) == 0 || len(raw) > 32768 || !canonicalDataJSON(string(raw), &receipt) || !validCheckpointReceipt(receipt) {
		return CheckpointReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func checkpointReceiptMatchesCommand(r CheckpointReceipt, c control.CheckpointCommand) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch &&
		r.InstallationID == c.InstallationID && r.OperationID == c.OperationID &&
		r.TargetGeneration == c.TargetGeneration && r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 && r.CheckpointID == p.CheckpointID && r.NamespaceID == p.NamespaceID &&
		r.ResourceID == p.ResourceID && r.ReleaseSHA256 == p.ReleaseSHA256 && r.SourceOperationID == p.SourceOperationID &&
		r.SourceStartEffectID == p.SourceStartEffectID && r.SourceReceiptSHA256 == p.SourceReceiptSHA256 &&
		r.SourceDataEffectID == p.SourceDataEffectID && r.SourceDataReceiptSHA256 == p.SourceDataReceiptSHA256 &&
		r.ProjectID == p.ProjectID && r.NamespaceInode == p.NamespaceInode && r.Device == p.Device &&
		r.DataFormat == p.DataFormat && r.WriterFence == p.WriterFence
}

type checkpointSource struct {
	command     control.CheckpointCommand
	source      CandidateRunningReceipt
	data        DataPreparationReceipt
	sourceFence int64
	resource    managedRemovalResource
	raw         []byte
	signature   []byte
}

func loadCheckpointSource(ctx context.Context, tx *sql.Tx, c control.CheckpointCommand, now int64) (checkpointSource, error) {
	return loadCheckpointSourceExcluding(ctx, tx, c, now, c.EffectID)
}

func loadCheckpointSourceExcluding(ctx context.Context, tx *sql.Tx, c control.CheckpointCommand, now int64, excludeEffect string) (checkpointSource, error) {
	var result checkpointSource
	result.command = c
	var startRaw, startOperation, startState, storedPayload string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,state,payload_sha256,receipt_json,admitted_fence
		FROM runner_commands WHERE effect_id=? AND installation_id=? AND stage='start' AND kind='start-candidate'`,
		c.Payload.SourceStartEffectID, c.InstallationID).Scan(&startOperation, &startState, &storedPayload, &startRaw, &result.sourceFence); err != nil {
		return result, ErrCheckpointUnavailable
	}
	if startOperation != c.Payload.SourceOperationID || startState != "applied" || candidateHash([]byte(startRaw)) != c.Payload.SourceReceiptSHA256 || result.sourceFence < 1 {
		return result, fmt.Errorf("%w: checkpoint source start", ErrFenceReconciliation)
	}
	source, err := ParseCandidateRunningReceipt([]byte(startRaw))
	if err != nil || source.PayloadSHA256 != storedPayload || source.OperationID != c.Payload.SourceOperationID ||
		source.InstallationID != c.InstallationID || source.DataNamespaceID != c.Payload.NamespaceID ||
		source.ReleaseSHA256 != c.Payload.ReleaseSHA256 || source.Claim.ResourceID != c.Payload.ResourceID ||
		source.Claim.Preflight == nil || source.Claim.AdmittedFence != strconv.FormatInt(result.sourceFence, 10) {
		return result, fmt.Errorf("%w: checkpoint source receipt", ErrFenceReconciliation)
	}
	var dataRaw, dataEffect, dataKind, dataState, dataPayload string
	var dataSlot, dataFence int64
	if err := tx.QueryRowContext(ctx, `SELECT effect_id,effect_number,kind,state,payload_sha256,receipt_json,admitted_fence
		FROM runner_commands WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, c.Payload.SourceOperationID).
		Scan(&dataEffect, &dataSlot, &dataKind, &dataState, &dataPayload, &dataRaw, &dataFence); err != nil {
		return result, ErrCheckpointUnavailable
	}
	if dataEffect != c.Payload.SourceDataEffectID || dataSlot != 1 || dataKind != "prepare-data" || dataState != "applied" ||
		dataFence != result.sourceFence || candidateHash([]byte(dataRaw)) != c.Payload.SourceDataReceiptSHA256 {
		return result, fmt.Errorf("%w: checkpoint source data", ErrFenceReconciliation)
	}
	proof, err := ParseDataPreparationReceipt([]byte(dataRaw))
	if err != nil || dataPayload != proof.PayloadSHA256 {
		return result, fmt.Errorf("%w: checkpoint data receipt", ErrFenceReconciliation)
	}
	dataCommand := control.Command{
		Format: control.Format, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		ControlRevision: proof.ControlRevision, InstallationID: c.InstallationID, OperationID: c.Payload.SourceOperationID,
		TargetGeneration: source.Generation, Stage: "prepare_data", EffectNumber: "1", EffectID: dataEffect,
		Kind: "prepare-data", InstallationFence: strconv.FormatInt(dataFence, 10), PayloadSHA256: dataPayload,
		Payload: control.PrepareDataPayload{DataNamespaceID: c.Payload.NamespaceID, ReleaseSHA256: c.Payload.ReleaseSHA256,
			DataFormat: proof.DataFormat, QuotaBytes: proof.QuotaBytes, QuotaInodes: proof.QuotaInodes, WriterFence: proof.WriterFence},
	}
	if !proof.MatchesCommand(dataCommand) || proof.ProjectID != c.Payload.ProjectID || proof.NamespaceInode != c.Payload.NamespaceInode ||
		proof.Device != c.Payload.Device || proof.DataFormat != c.Payload.DataFormat || proof.WriterFence != c.Payload.WriterFence {
		return result, fmt.Errorf("%w: checkpoint data binding", ErrFenceReconciliation)
	}
	target, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	sourceGeneration, _ := strconv.ParseInt(source.Generation, 10, 64)
	if target != sourceGeneration+1 {
		return result, fmt.Errorf("%w: checkpoint generation", ErrFenceReconciliation)
	}
	sourceCommand := control.CandidateCommand{
		Format: control.CandidateFormat, DeploymentID: c.DeploymentID, ControllerEpoch: c.ControllerEpoch,
		ControlRevision: source.ControlRevision, InstallationID: c.InstallationID, OperationID: c.Payload.SourceOperationID,
		TargetGeneration: source.Generation, Stage: "start", EffectNumber: "1", EffectID: c.Payload.SourceStartEffectID,
		Kind: "start-candidate", InstallationFence: strconv.FormatInt(result.sourceFence, 10),
		Payload: control.CandidatePayload{ResourceID: source.Claim.ResourceID, DataNamespaceID: source.DataNamespaceID,
			ReleaseSHA256: source.ReleaseSHA256, Platform: source.Claim.Preflight.Platform, DataFormat: source.DataFormat,
			WriterFence: source.WriterFence, SandboxProfile: control.CandidateSandboxProfile},
	}
	sourceCommand.PayloadSHA256, err = control.CandidateLogicalDigest(sourceCommand)
	if err != nil || sourceCommand.PayloadSHA256 != storedPayload || !source.MatchesCommand(sourceCommand) {
		return result, fmt.Errorf("%w: checkpoint source command", ErrFenceReconciliation)
	}
	result.source, result.data = source, proof
	var nativeID, observed, desired, release, namespace, epoch, ownership sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT native_id,observed_state,desired_state,release_sha256,data_namespace_id,controller_epoch,ownership_sha256,
		revision,generation,control_fence,writer_fence FROM runner_resources WHERE id=? AND kind='container'`, c.Payload.ResourceID).
		Scan(&nativeID, &observed, &desired, &release, &namespace, &epoch, &ownership, &result.resource.revision,
			&result.resource.generation, &result.resource.controlFence, &result.resource.writerFence); err != nil {
		return result, ErrCheckpointUnavailable
	}
	result.resource.nativeID, result.resource.observed, result.resource.desired = nativeID.String, observed.String, desired.String
	result.resource.release, result.resource.namespace, result.resource.epoch = release.String, namespace.String, epoch.String
	result.resource.ownership = ownership.String
	if !nativeID.Valid || nativeID.String != source.NativeID || !ownership.Valid || ownership.String != source.Claim.OwnershipSHA256 ||
		result.resource.release != c.Payload.ReleaseSHA256 || result.resource.namespace != c.Payload.NamespaceID ||
		result.resource.epoch != c.ControllerEpoch || result.resource.generation != sourceGeneration ||
		result.resource.controlFence != result.sourceFence || result.resource.writerFence != mustDataInt(source.WriterFence) ||
		result.resource.revision != 3 || result.resource.observed != "stopped" || result.resource.desired != "stopped" {
		return result, fmt.Errorf("%w: checkpoint stopped resource", ErrFenceReconciliation)
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND state IN ('prepared','unknown')`, c.InstallationID, excludeEffect).Scan(&pending); err != nil || pending != 0 {
		return result, fmt.Errorf("%w: checkpoint pending command", ErrFenceReconciliation)
	}
	if now <= 0 {
		return result, fmt.Errorf("%w: checkpoint clock", ErrFenceReconciliation)
	}
	return result, nil
}

func (s *Store) beginCheckpoint(ctx context.Context, raw, signature []byte) (checkpointSource, bool, error) {
	var zero checkpointSource
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
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyCheckpoint(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	command := verified.Command()
	createdCommand := false
	var storedOperation, storedInstallation, storedStage, storedKind, storedPayload, storedEpoch, storedState, storedReceipt string
	var storedSlot, storedGeneration, storedFence int64
	err = tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json
		FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&storedOperation, &storedInstallation, &storedStage, &storedSlot,
		&storedKind, &storedPayload, &storedGeneration, &storedEpoch, &storedFence, &storedState, &storedReceipt)
	if err == nil {
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		if storedOperation != command.OperationID || storedInstallation != command.InstallationID || storedStage != command.Stage ||
			storedKind != command.Kind || storedPayload != command.PayloadSHA256 || storedGeneration != generation ||
			storedEpoch != command.ControllerEpoch || storedSlot != slot {
			return zero, false, ErrCommandConflict
		}
		if storedState == "applied" {
			receipt, err := ParseCheckpointReceipt([]byte(storedReceipt))
			if err != nil || !checkpointReceiptMatchesCommand(receipt, command) {
				return zero, false, fmt.Errorf("%w: checkpoint replay receipt", ErrFenceReconciliation)
			}
			return zero, true, nil
		}
		if storedState != "prepared" || storedReceipt != "{}" {
			return zero, false, fmt.Errorf("%w: checkpoint replay state %s", ErrFenceReconciliation, storedState)
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		source, err := loadCheckpointSource(ctx, tx, command, now)
		if err != nil {
			return zero, false, err
		}
		fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
		if fence < source.sourceFence || fence < source.resource.controlFence {
			return zero, false, fmt.Errorf("%w: checkpoint fence", ErrFenceReconciliation)
		}
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`, command.EffectID, command.OperationID, command.InstallationID,
			command.Stage, slot, command.Kind, command.PayloadSHA256, generation, command.ControllerEpoch, fence, now, now); err != nil {
			return zero, false, err
		}
		createdCommand = true
	} else {
		return zero, false, err
	}
	source, err := loadCheckpointSource(ctx, tx, command, now)
	if err != nil {
		return zero, false, err
	}
	if createdCommand {
		format, _ := strconv.ParseInt(command.Payload.DataFormat, 10, 64)
		writer, _ := strconv.ParseInt(command.Payload.WriterFence, 10, 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_checkpoints(id,data_namespace_id,installation_id,operation_id,release_sha256,data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,NULL,0,'preparing',1,NULL,?,?)`,
			command.Payload.CheckpointID, command.Payload.NamespaceID, command.InstallationID,
			command.OperationID, command.Payload.ReleaseSHA256, format, writer, now, now); err != nil {
			return zero, false, err
		}
	} else if err := validateCheckpointRecord(ctx, tx, command, "", 0, false); err != nil {
		return zero, false, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}'`, now, command.EffectID)
	if err != nil {
		return zero, false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, false, fmt.Errorf("%w: checkpoint intent transition", ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return source, false, nil
}

func (s *Store) settleCheckpoint(ctx context.Context, source checkpointSource, observation data.CheckpointObservation) (CheckpointReceipt, error) {
	var zero CheckpointReceipt
	snapshot, err := observation.Snapshot()
	projectID, projectErr := checkpointProjectID(source.command.Payload.ProjectID)
	if err != nil || snapshot.NamespaceID != source.command.Payload.NamespaceID || snapshot.ReleaseSHA256 != source.command.Payload.ReleaseSHA256 ||
		projectErr != nil || snapshot.ProjectID != projectID || snapshot.DataFormat != mustDataInt(source.command.Payload.DataFormat) ||
		snapshot.ArchiveBytes <= 0 || snapshot.ArchiveBytes > maxCheckpointArchiveBytes || snapshot.Entries < 0 {
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
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	now := time.Now().UTC().UnixMilli()
	if _, err := control.VerifyCheckpoint(source.commandRaw(), source.commandSignature(), authority, now); err != nil {
		return zero, ErrFenceReconciliation
	}
	current, err := loadCheckpointSource(ctx, tx, source.command, now)
	if err != nil || !sameCheckpointSource(source, current) {
		return zero, ErrFenceReconciliation
	}
	var state, receipt string
	if err := tx.QueryRowContext(ctx, `SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, source.command.EffectID).Scan(&state, &receipt); err != nil || state != "unknown" || receipt != "{}" {
		return zero, ErrFenceReconciliation
	}
	result := CheckpointReceipt{
		Format: checkpointReceiptFormat, DeploymentID: source.command.DeploymentID, ControllerEpoch: source.command.ControllerEpoch,
		InstallationID: source.command.InstallationID, OperationID: source.command.OperationID,
		TargetGeneration: source.command.TargetGeneration, Stage: source.command.Stage, EffectID: source.command.EffectID,
		PayloadSHA256: source.command.PayloadSHA256, CheckpointID: source.command.Payload.CheckpointID,
		NamespaceID: source.command.Payload.NamespaceID, ResourceID: source.command.Payload.ResourceID,
		ReleaseSHA256: source.command.Payload.ReleaseSHA256, SourceOperationID: source.command.Payload.SourceOperationID,
		SourceStartEffectID: source.command.Payload.SourceStartEffectID, SourceReceiptSHA256: source.command.Payload.SourceReceiptSHA256,
		SourceDataEffectID: source.command.Payload.SourceDataEffectID, SourceDataReceiptSHA256: source.command.Payload.SourceDataReceiptSHA256,
		ProjectID: source.command.Payload.ProjectID, NamespaceInode: source.command.Payload.NamespaceInode,
		Device: source.command.Payload.Device, DataFormat: source.command.Payload.DataFormat,
		WriterFence: source.command.Payload.WriterFence, ArchiveBytes: strconv.FormatInt(snapshot.ArchiveBytes, 10),
		Entries: strconv.FormatInt(snapshot.Entries, 10), ManifestSHA256: snapshot.ManifestSHA256,
		ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validCheckpointReceipt(result) {
		return zero, ErrFenceReconciliation
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 32768 {
		return zero, ErrFenceReconciliation
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}'`, string(encoded), now, source.command.EffectID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_checkpoints SET manifest_sha256=?,bytes=?,state='verified',revision=revision+1,verified_ms=?,updated_ms=?
		WHERE id=? AND data_namespace_id=? AND installation_id=? AND operation_id=? AND release_sha256=?
		AND data_format=? AND writer_fence=? AND manifest_sha256 IS NULL AND bytes=0
		AND state='preparing' AND revision=1 AND verified_ms IS NULL`,
		result.ManifestSHA256, mustDataInt(result.ArchiveBytes), now, now,
		result.CheckpointID, result.NamespaceID, result.InstallationID, result.OperationID,
		result.ReleaseSHA256, mustDataInt(result.DataFormat), mustDataInt(result.WriterFence))
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

func (source checkpointSource) commandRaw() []byte       { return source.raw }
func (source checkpointSource) commandSignature() []byte { return source.signature }

func sameCheckpointSource(a, b checkpointSource) bool {
	return a.command == b.command && sameManagedRemovalJSON(a.source, b.source) && a.data == b.data &&
		a.sourceFence == b.sourceFence && a.resource == b.resource
}

func (s *Store) DispatchCheckpoint(ctx context.Context, raw, signature []byte, dataRoot *data.Root, dataPin data.RootIdentity, checkpointRoot *data.Root, checkpointPin data.RootIdentity) (CheckpointReceipt, error) {
	var zero CheckpointReceipt
	if dataRoot == nil || checkpointRoot == nil || s.closing.Load() {
		return zero, ErrCheckpointUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	source, replay, err := s.beginCheckpoint(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	source.raw, source.signature = raw, signature
	if replay {
		return s.ReadCheckpointReceipt(ctx, raw, signature)
	}
	projectID, err := checkpointProjectID(source.command.Payload.ProjectID)
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	request := data.CheckpointRequest{
		CheckpointID: source.command.Payload.CheckpointID, NamespaceID: source.command.Payload.NamespaceID,
		ReleaseSHA256: source.command.Payload.ReleaseSHA256, ProjectID: projectID,
		DataFormat: mustDataInt(source.command.Payload.DataFormat), MaxArchiveBytes: maxCheckpointArchiveBytes,
		ExpectedDevice: mustDataUint(source.command.Payload.Device), ExpectedNamespaceInode: mustDataUint(source.command.Payload.NamespaceInode),
		WriterFence: mustDataInt(source.command.Payload.WriterFence),
	}
	observation, err := checkpointRoot.Checkpoint(ctx, checkpointPin, dataRoot, dataPin, request)
	if err != nil {
		return zero, err
	}
	return s.settleCheckpoint(ctx, source, observation)
}

func checkpointProjectID(value string) (uint32, error) {
	project, err := strconv.ParseUint(value, 10, 32)
	if err != nil || project == 0 || strconv.FormatUint(project, 10) != value {
		return 0, ErrFenceReconciliation
	}
	return uint32(project), nil
}

func mustDataUint(value string) uint64 {
	n, _ := strconv.ParseUint(value, 10, 64)
	return n
}

func (s *Store) ReadCheckpointReceipt(ctx context.Context, raw, signature []byte) (CheckpointReceipt, error) {
	var zero CheckpointReceipt
	if s.closing.Load() {
		return zero, ErrCheckpointUnavailable
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
	authority := control.Authority{DeploymentID: deployment.DeploymentID, ControllerEpoch: deployment.ControllerEpoch, ControlRevision: deployment.ControlRevision, PublicKey: deployment.PublicKey, Fingerprint: deployment.Fingerprint}
	verified, err := control.VerifyCheckpoint(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	var receipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=? AND operation_id=? AND installation_id=? AND state='applied'`,
		command.EffectID, command.OperationID, command.InstallationID).Scan(&receipt); err != nil {
		return zero, ErrCheckpointUnavailable
	}
	result, err := ParseCheckpointReceipt([]byte(receipt))
	if err != nil || !checkpointReceiptMatchesCommand(result, command) {
		return zero, ErrFenceReconciliation
	}
	if err := validateCheckpointRecord(ctx, tx, command, result.ManifestSHA256, mustDataInt(result.ArchiveBytes), true); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func validateCheckpointRecord(ctx context.Context, tx *sql.Tx, command control.CheckpointCommand, manifest string, bytes int64, verified bool) error {
	var id, namespace, installation, operation, release, state string
	var format, writer, revision int64
	var storedManifest sql.NullString
	var storedBytes, verifiedMS sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id,data_namespace_id,installation_id,operation_id,release_sha256,
		data_format,writer_fence,manifest_sha256,bytes,state,revision,verified_ms
		FROM runner_checkpoints WHERE operation_id=? AND state<>
		'deleted' ORDER BY created_ms DESC,id LIMIT 1`, command.OperationID).
		Scan(&id, &namespace, &installation, &operation, &release, &format, &writer,
			&storedManifest, &storedBytes, &state, &revision, &verifiedMS); err != nil {
		return ErrFenceReconciliation
	}
	if id != command.Payload.CheckpointID || namespace != command.Payload.NamespaceID ||
		installation != command.InstallationID || operation != command.OperationID ||
		release != command.Payload.ReleaseSHA256 || format != mustDataInt(command.Payload.DataFormat) ||
		writer != mustDataInt(command.Payload.WriterFence) {
		return ErrFenceReconciliation
	}
	if !verified {
		if state != "preparing" || revision != 1 || storedManifest.Valid || !storedBytes.Valid ||
			storedBytes.Int64 != 0 || verifiedMS.Valid {
			return ErrFenceReconciliation
		}
		return nil
	}
	if state != "verified" || revision != 2 || !storedManifest.Valid || storedManifest.String != manifest ||
		!storedBytes.Valid || storedBytes.Int64 != bytes || bytes <= 0 || !verifiedMS.Valid ||
		verifiedMS.Int64 <= 0 || verifiedMS.Int64 > time.Now().UTC().UnixMilli() {
		return ErrFenceReconciliation
	}
	return nil
}
