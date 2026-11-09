package journal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

var ErrDeleteDataUnavailable = errors.New("runner data deletion is unavailable")

const deleteDataReceiptFormat = "portal.runner.delete-data-receipt/1-dev.1"

// DeleteDataReceipt is immutable evidence that one exact retained namespace
// and project quota are absent. It is not Portal completion or restore evidence.
type DeleteDataReceipt struct {
	Format                 string `json:"format"`
	DeploymentID           string `json:"deployment_id"`
	ControllerEpoch        string `json:"controller_epoch"`
	InstallationID         string `json:"installation_id"`
	OperationID            string `json:"operation_id"`
	TargetGeneration       string `json:"target_generation"`
	Stage                  string `json:"stage"`
	EffectID               string `json:"effect_id"`
	PayloadSHA256          string `json:"payload_sha256"`
	NamespaceID            string `json:"namespace_id"`
	ReleaseSHA256          string `json:"release_sha256"`
	DataReceiptSHA256      string `json:"data_receipt_sha256"`
	OwnershipSHA256        string `json:"ownership_sha256"`
	ProjectID              string `json:"project_id"`
	DataFormat             string `json:"data_format"`
	QuotaBytes             string `json:"quota_bytes"`
	QuotaInodes            string `json:"quota_inodes"`
	ExpectedDevice         string `json:"expected_device"`
	ExpectedNamespaceInode string `json:"expected_namespace_inode"`
	WriterFence            string `json:"writer_fence"`
	Entries                string `json:"entries"`
	UsedBytes              string `json:"used_bytes"`
	UsedInodes             string `json:"used_inodes"`
	NamespaceDevice        string `json:"namespace_device"`
	NamespaceInode         string `json:"namespace_inode"`
	AlreadyAbsent          bool   `json:"already_absent"`
	ObservedMS             string `json:"observed_ms"`
	SettledMS              string `json:"settled_ms"`
}

func validDeleteDataReceipt(r DeleteDataReceipt) bool {
	if r.Format != deleteDataReceiptFormat || r.Stage != "remove_data" ||
		!dataSHA(r.PayloadSHA256) || !dataSHA(r.ReleaseSHA256) ||
		!dataSHA(r.DataReceiptSHA256) || !dataSHA(r.OwnershipSHA256) {
		return false
	}
	for _, id := range []string{
		r.DeploymentID, r.ControllerEpoch, r.InstallationID,
		r.OperationID, r.EffectID, r.NamespaceID,
	} {
		if !enrollmentUUID.MatchString(id) ||
			id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, number := range []string{
		r.TargetGeneration, r.ProjectID, r.DataFormat,
		r.QuotaBytes, r.QuotaInodes, r.WriterFence,
		r.Entries, r.UsedBytes, r.UsedInodes, r.ObservedMS,
		r.SettledMS,
	} {
		if !dataNumber(number, false) {
			return false
		}
	}
	for _, number := range []string{
		r.ExpectedDevice, r.ExpectedNamespaceInode,
	} {
		if !nativeNumber(number, true) {
			return false
		}
	}
	if !r.AlreadyAbsent {
		if !nativeNumber(r.NamespaceDevice, true) ||
			!nativeNumber(r.NamespaceInode, true) {
			return false
		}
	} else if r.NamespaceDevice != "" || r.NamespaceInode != "" {
		return false
	}
	target, _ := strconv.ParseInt(r.TargetGeneration, 10, 64)
	project, projectErr := strconv.ParseUint(r.ProjectID, 10, 32)
	format, _ := strconv.ParseInt(r.DataFormat, 10, 64)
	quota, _ := strconv.ParseInt(r.QuotaBytes, 10, 64)
	inodes, _ := strconv.ParseInt(r.QuotaInodes, 10, 64)
	writer, _ := strconv.ParseInt(r.WriterFence, 10, 64)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return target > 1 && projectErr == nil && project > 0 &&
		project <= uint64(maxDataProjectID) && format > 0 &&
		quota > 0 && quota%512 == 0 && inodes > 0 && writer > 0 &&
		observed > 0 && settled >= observed
}

func ParseDeleteDataReceipt(raw []byte) (DeleteDataReceipt, error) {
	var receipt DeleteDataReceipt
	if len(raw) == 0 || len(raw) > 32768 ||
		!canonicalDataJSON(string(raw), &receipt) ||
		!validDeleteDataReceipt(receipt) {
		return DeleteDataReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

func deleteDataReceiptMatchesCommand(
	r DeleteDataReceipt, c control.DeleteDataCommand,
) bool {
	p := c.Payload
	return r.DeploymentID == c.DeploymentID &&
		r.ControllerEpoch == c.ControllerEpoch &&
		r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID &&
		r.TargetGeneration == c.TargetGeneration &&
		r.Stage == c.Stage && r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 &&
		r.NamespaceID == p.NamespaceID &&
		r.ReleaseSHA256 == p.ReleaseSHA256 &&
		r.ProjectID == p.ProjectID &&
		r.DataFormat == p.DataFormat &&
		r.QuotaBytes == p.QuotaBytes &&
		r.QuotaInodes == p.QuotaInodes &&
		r.ExpectedDevice == p.ExpectedDevice &&
		r.ExpectedNamespaceInode == p.ExpectedNamespaceInode &&
		r.WriterFence == p.WriterFence
}

type deleteDataVolume struct {
	release, native, desired, observed, ownership   string
	generation, controlFence, writerFence, revision int64
}

type deleteDataContainer struct {
	release, desired, observed                      string
	generation, controlFence, writerFence, revision int64
}

type deleteDataSource struct {
	command        control.DeleteDataCommand
	data           DataPreparationReceipt
	dataReceipt    string
	dataSHA256     string
	volume         deleteDataVolume
	container      deleteDataContainer
	raw, signature []byte
}

func loadDeleteDataSource(
	ctx context.Context, tx *sql.Tx, c control.DeleteDataCommand, now int64,
) (deleteDataSource, error) {
	var result deleteDataSource
	result.command = c
	if now <= 0 {
		return result, fmt.Errorf(
			"%w: delete-data clock", ErrFenceReconciliation)
	}
	var installation, namespace sql.NullString
	var release, native, desired, observed, ownership sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT installation_id,
		data_namespace_id,native_id,release_sha256,desired_state,
		observed_state,ownership_sha256,generation,control_fence,
		writer_fence,revision
		FROM runner_resources WHERE id=? AND kind='volume'`,
		c.Payload.NamespaceID).Scan(
		&installation, &namespace, &native, &release, &desired,
		&observed, &ownership, &result.volume.generation,
		&result.volume.controlFence, &result.volume.writerFence,
		&result.volume.revision); err != nil {
		return result, ErrDeleteDataUnavailable
	}
	if installation.String != c.InstallationID ||
		namespace.String != c.Payload.NamespaceID ||
		!release.Valid || !native.Valid || !ownership.Valid ||
		desired.String != "present" || observed.String != "present" ||
		result.volume.revision < 2 ||
		result.volume.controlFence < 1 ||
		result.volume.writerFence < 1 {
		return result, fmt.Errorf(
			"%w: delete-data volume identity", ErrFenceReconciliation)
	}
	project, ok := dataProjectID(native.String)
	if !ok || strconv.FormatUint(uint64(project), 10) !=
		c.Payload.ProjectID {
		return result, fmt.Errorf(
			"%w: delete-data project identity", ErrFenceReconciliation)
	}
	result.volume.release = release.String
	result.volume.native = native.String
	result.volume.desired = desired.String
	result.volume.observed = observed.String
	result.volume.ownership = ownership.String

	rows, err := tx.QueryContext(ctx, `SELECT payload_sha256,receipt_json
		FROM runner_commands WHERE installation_id=?
		AND stage='prepare_data' AND kind='prepare-data'
		AND state='applied'
		ORDER BY created_ms DESC,effect_id LIMIT 64`,
		c.InstallationID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload, raw string
		if err := rows.Scan(&payload, &raw); err != nil {
			return result, err
		}
		receipt, err := ParseDataPreparationReceipt([]byte(raw))
		if err != nil || receipt.PayloadSHA256 != payload ||
			receipt.Claim.ResourceID != c.Payload.NamespaceID {
			continue
		}
		result.data = receipt
		result.dataReceipt = raw
		digest := sha256.Sum256([]byte(raw))
		result.dataSHA256 = hex.EncodeToString(digest[:])
		break
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if result.dataReceipt == "" {
		return result, fmt.Errorf(
			"%w: delete-data preparation receipt missing",
			ErrFenceReconciliation)
	}
	dataFormat, formatErr := strconv.ParseInt(
		c.Payload.DataFormat, 10, 64)
	storedFormat, storedFormatErr := strconv.ParseInt(
		result.data.DataFormat, 10, 64)
	if formatErr != nil || storedFormatErr != nil ||
		dataFormat <= 0 || storedFormat <= 0 ||
		dataFormat < storedFormat ||
		result.data.InstallationID != c.InstallationID ||
		result.data.ProjectID != c.Payload.ProjectID ||
		result.data.Device != c.Payload.ExpectedDevice ||
		result.data.NamespaceInode !=
			c.Payload.ExpectedNamespaceInode ||
		result.data.QuotaBytes != c.Payload.QuotaBytes ||
		result.data.QuotaInodes != c.Payload.QuotaInodes {
		return result, fmt.Errorf(
			"%w: delete-data preparation binding",
			ErrFenceReconciliation)
	}

	var containerRelease, containerDesired, containerObserved sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT release_sha256,
		desired_state,observed_state,generation,control_fence,
		writer_fence,revision FROM runner_resources
		WHERE kind='container' AND data_namespace_id=?
		ORDER BY generation DESC,revision DESC LIMIT 1`,
		c.Payload.NamespaceID).Scan(
		&containerRelease, &containerDesired, &containerObserved,
		&result.container.generation, &result.container.controlFence,
		&result.container.writerFence,
		&result.container.revision); err != nil {
		return result, fmt.Errorf(
			"%w: delete-data container history",
			ErrFenceReconciliation)
	}
	result.container.release = containerRelease.String
	result.container.desired = containerDesired.String
	result.container.observed = containerObserved.String
	target, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	fence, _ := strconv.ParseInt(c.InstallationFence, 10, 64)
	if !containerRelease.Valid ||
		result.container.release != c.Payload.ReleaseSHA256 ||
		!((result.container.desired == "absent" &&
			result.container.observed == "absent") ||
			(result.container.desired == "stopped" &&
				result.container.observed == "stopped")) ||
		result.container.generation+1 != target ||
		result.container.controlFence > fence ||
		result.volume.controlFence > fence ||
		result.container.writerFence+1 != writer {
		return result, fmt.Errorf(
			"delete-data writer/container binding release=%s/%s desired=%s/%s observed=%s/%s generation=%d/%d control=%d/%d volume-control=%d writer=%d/%d target=%d: %w",
			containerRelease.String, c.Payload.ReleaseSHA256,
			containerDesired.String, "absent|stopped",
			containerObserved.String, "absent|stopped",
			result.container.generation, target-1,
			result.container.controlFence, fence,
			result.volume.controlFence,
			result.container.writerFence, writer-1, target,
			ErrFenceReconciliation)
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM runner_commands WHERE installation_id=? AND effect_id<>?
		AND kind<>'load-image' AND state IN ('prepared','unknown')`,
		c.InstallationID, c.EffectID).Scan(&pending); err != nil ||
		pending != 0 {
		return result, fmt.Errorf(
			"%w: delete-data pending command",
			ErrFenceReconciliation)
	}
	return result, nil
}

func (s *Store) beginDeleteData(
	ctx context.Context, raw, signature []byte,
) (deleteDataSource, bool, error) {
	var zero deleteDataSource
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment
		SET revision=revision WHERE singleton=1`); err != nil {
		return zero, false, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, false, err
	}
	authority := control.Authority{
		DeploymentID:    deployment.DeploymentID,
		ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision,
		PublicKey:       deployment.PublicKey,
		Fingerprint:     deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	verified, err := control.VerifyDeleteData(
		raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	command := verified.Command()
	var storedOperation, storedInstallation, storedStage string
	var storedSlot, storedGeneration, storedFence int64
	var storedKind, storedPayload, storedEpoch, storedState string
	var storedReceipt string
	fresh := false
	err = tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,
		stage,effect_number,kind,payload_sha256,target_generation,
		controller_epoch,admitted_fence,state,receipt_json
		FROM runner_commands WHERE effect_id=?`,
		command.EffectID).Scan(
		&storedOperation, &storedInstallation, &storedStage,
		&storedSlot, &storedKind, &storedPayload, &storedGeneration,
		&storedEpoch, &storedFence, &storedState, &storedReceipt)
	if err == nil {
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		generation, _ := strconv.ParseInt(
			command.TargetGeneration, 10, 64)
		fence, _ := strconv.ParseInt(
			command.InstallationFence, 10, 64)
		if storedOperation != command.OperationID ||
			storedInstallation != command.InstallationID ||
			storedStage != command.Stage ||
			storedSlot != slot || storedKind != command.Kind ||
			storedPayload != command.PayloadSHA256 ||
			storedGeneration != generation ||
			storedEpoch != command.ControllerEpoch ||
			storedFence != fence {
			return zero, false, ErrCommandConflict
		}
		switch storedState {
		case "applied":
			receipt, err := ParseDeleteDataReceipt(
				[]byte(storedReceipt))
			if err != nil ||
				!deleteDataReceiptMatchesCommand(receipt, command) {
				return zero, false, fmt.Errorf(
					"%w: delete-data replay receipt",
					ErrFenceReconciliation)
			}
			return zero, true, nil
		case "unknown":
			return zero, false, fmt.Errorf(
				"%w: delete-data replay uncertain",
				ErrFenceReconciliation)
		case "prepared":
		default:
			return zero, false, fmt.Errorf(
				"%w: delete-data replay state",
				ErrFenceReconciliation)
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		fresh = true
	} else {
		return zero, false, err
	}
	source, err := loadDeleteDataSource(ctx, tx, command, now)
	if err != nil {
		return zero, false, err
	}
	if fresh {
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		generation, _ := strconv.ParseInt(
			command.TargetGeneration, 10, 64)
		fence, _ := strconv.ParseInt(
			command.InstallationFence, 10, 64)
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(
			effect_id,operation_id,installation_id,stage,
			effect_number,kind,payload_sha256,target_generation,
			controller_epoch,admitted_fence,state,receipt_json,
			ack_attempted_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`,
			command.EffectID, command.OperationID,
			command.InstallationID, command.Stage, slot,
			command.Kind, command.PayloadSHA256, generation,
			command.ControllerEpoch, fence, now, now); err != nil {
			return zero, false, err
		}
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands
		SET state='unknown',updated_ms=?
		WHERE effect_id=? AND state='prepared' AND receipt_json='{}'`,
		now, command.EffectID)
	if err != nil {
		return zero, false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, false, fmt.Errorf(
			"%w: delete-data intent transition",
			ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return source, false, nil
}

func (s *Store) DispatchDeleteData(
	ctx context.Context, raw, signature []byte,
	root *data.Root, pin data.RootIdentity,
) (DeleteDataReceipt, error) {
	var zero DeleteDataReceipt
	if root == nil || !validDataRoot(pin) || s.closing.Load() {
		return zero, ErrDeleteDataUnavailable
	}
	raw, signature = bytes.Clone(raw), bytes.Clone(signature)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	source, replay, err := s.beginDeleteData(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if replay {
		return s.ReadDeleteDataReceipt(ctx, raw, signature)
	}
	source.raw, source.signature = raw, signature
	if markerRoot(source.data.Claim) != pin {
		return zero, fmt.Errorf(
			"%w: delete-data root identity",
			ErrFenceReconciliation)
	}
	project, err := strconv.ParseUint(
		source.command.Payload.ProjectID, 10, 32)
	if err != nil {
		return zero, ErrFenceReconciliation
	}
	request := data.DeleteRequest{
		NamespaceID:   source.command.Payload.NamespaceID,
		ReleaseSHA256: source.command.Payload.ReleaseSHA256,
		ProjectID:     uint32(project),
		DataFormat:    mustDataInt(source.command.Payload.DataFormat),
		QuotaBytes:    mustDataInt(source.command.Payload.QuotaBytes),
		QuotaInodes:   mustDataInt(source.command.Payload.QuotaInodes),
		ExpectedDevice: mustDataUint(
			source.command.Payload.ExpectedDevice),
		ExpectedNamespaceInode: mustDataUint(
			source.command.Payload.ExpectedNamespaceInode),
		WriterFence: mustDataInt(source.command.Payload.WriterFence),
	}
	observation, err := root.Delete(ctx, pin, request)
	if err != nil {
		return zero, err
	}
	return s.settleDeleteData(ctx, source, observation)
}

func (s *Store) settleDeleteData(
	ctx context.Context, source deleteDataSource,
	observation data.DeleteObservation,
) (DeleteDataReceipt, error) {
	var zero DeleteDataReceipt
	snapshot, err := observation.Snapshot()
	if err != nil {
		return zero, fmt.Errorf(
			"%w: delete-data observation",
			ErrFenceReconciliation)
	}
	if snapshot.NamespaceID != source.command.Payload.NamespaceID ||
		snapshot.ReleaseSHA256 != source.command.Payload.ReleaseSHA256 ||
		strconv.FormatUint(uint64(snapshot.ProjectID), 10) !=
			source.command.Payload.ProjectID ||
		strconv.FormatInt(snapshot.DataFormat, 10) !=
			source.command.Payload.DataFormat ||
		strconv.FormatInt(snapshot.WriterFence, 10) !=
			source.command.Payload.WriterFence {
		return zero, fmt.Errorf(
			"%w: delete-data observation binding",
			ErrFenceReconciliation)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment
		SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID:    deployment.DeploymentID,
		ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision,
		PublicKey:       deployment.PublicKey,
		Fingerprint:     deployment.Fingerprint,
	}
	now := time.Now().UTC().UnixMilli()
	if _, err := control.VerifyDeleteData(
		source.raw, source.signature, authority, now); err != nil {
		return zero, fmt.Errorf(
			"%w: delete-data settlement authority",
			ErrFenceReconciliation)
	}
	current, err := loadDeleteDataSource(
		ctx, tx, source.command, now)
	if err != nil {
		return zero, err
	}
	if current.dataSHA256 != source.dataSHA256 ||
		current.volume.native != source.volume.native ||
		current.volume.ownership != source.volume.ownership ||
		current.container.writerFence != source.container.writerFence {
		return zero, fmt.Errorf(
			"%w: delete-data settlement source",
			ErrFenceReconciliation)
	}
	var state, receipt string
	if err := tx.QueryRowContext(ctx, `SELECT state,receipt_json
		FROM runner_commands WHERE effect_id=?`,
		source.command.EffectID).Scan(&state, &receipt); err != nil ||
		state != "unknown" || receipt != "{}" {
		return zero, fmt.Errorf(
			"%w: delete-data settlement intent",
			ErrFenceReconciliation)
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources
		SET desired_state='absent',observed_state='absent',
		revision=revision+1,observed_ms=?,updated_ms=?
		WHERE id=? AND kind='volume' AND native_id=?
		AND desired_state='present' AND observed_state='present'
		AND revision=? AND ownership_sha256=?`,
		now, now, source.command.Payload.NamespaceID,
		source.volume.native, source.volume.revision,
		source.volume.ownership)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf(
			"%w: delete-data volume settlement CAS",
			ErrFenceReconciliation)
	}
	result := DeleteDataReceipt{
		Format:            deleteDataReceiptFormat,
		DeploymentID:      source.command.DeploymentID,
		ControllerEpoch:   source.command.ControllerEpoch,
		InstallationID:    source.command.InstallationID,
		OperationID:       source.command.OperationID,
		TargetGeneration:  source.command.TargetGeneration,
		Stage:             source.command.Stage,
		EffectID:          source.command.EffectID,
		PayloadSHA256:     source.command.PayloadSHA256,
		NamespaceID:       source.command.Payload.NamespaceID,
		ReleaseSHA256:     source.command.Payload.ReleaseSHA256,
		DataReceiptSHA256: source.dataSHA256,
		OwnershipSHA256:   source.volume.ownership,
		ProjectID:         source.command.Payload.ProjectID,
		DataFormat:        source.command.Payload.DataFormat,
		QuotaBytes:        source.command.Payload.QuotaBytes,
		QuotaInodes:       source.command.Payload.QuotaInodes,
		ExpectedDevice:    source.command.Payload.ExpectedDevice,
		ExpectedNamespaceInode: source.command.Payload.
			ExpectedNamespaceInode,
		WriterFence: source.command.Payload.WriterFence,
		Entries:     strconv.FormatInt(snapshot.Entries, 10),
		UsedBytes:   strconv.FormatInt(snapshot.UsedBytes, 10),
		UsedInodes:  strconv.FormatInt(snapshot.UsedInodes, 10),
		NamespaceDevice: strconv.FormatUint(
			snapshot.NamespaceDevice, 10),
		NamespaceInode: strconv.FormatUint(
			snapshot.NamespaceInode, 10),
		AlreadyAbsent: snapshot.AlreadyAbsent,
		ObservedMS:    strconv.FormatInt(snapshot.ObservedMS, 10),
		SettledMS:     strconv.FormatInt(now, 10),
	}
	if snapshot.AlreadyAbsent {
		result.NamespaceDevice = ""
		result.NamespaceInode = ""
	}
	if !validDeleteDataReceipt(result) {
		return zero, fmt.Errorf(
			"%w: delete-data settlement receipt",
			ErrFenceReconciliation)
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 32768 {
		return zero, fmt.Errorf(
			"%w: delete-data settlement encoding",
			ErrFenceReconciliation)
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands
		SET state='applied',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND state='unknown'
		AND receipt_json='{}'`,
		string(encoded), now, source.command.EffectID)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, fmt.Errorf(
			"%w: delete-data receipt CAS",
			ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func (s *Store) ReadDeleteDataReceipt(
	ctx context.Context, raw, signature []byte,
) (DeleteDataReceipt, error) {
	var zero DeleteDataReceipt
	if s.closing.Load() {
		return zero, ErrDeleteDataUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE runner_deployment
		SET revision=revision WHERE singleton=1`); err != nil {
		return zero, err
	}
	deployment, err := readControllerDeployment(ctx, tx)
	if err != nil {
		return zero, err
	}
	authority := control.Authority{
		DeploymentID:    deployment.DeploymentID,
		ControllerEpoch: deployment.ControllerEpoch,
		ControlRevision: deployment.ControlRevision,
		PublicKey:       deployment.PublicKey,
		Fingerprint:     deployment.Fingerprint,
	}
	verified, err := control.VerifyDeleteData(
		raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	var receipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json
		FROM runner_commands WHERE effect_id=? AND operation_id=?
		AND installation_id=? AND state='applied'`,
		command.EffectID, command.OperationID,
		command.InstallationID).Scan(&receipt); err != nil {
		return zero, ErrDeleteDataUnavailable
	}
	result, err := ParseDeleteDataReceipt([]byte(receipt))
	if err != nil ||
		!deleteDataReceiptMatchesCommand(result, command) {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
