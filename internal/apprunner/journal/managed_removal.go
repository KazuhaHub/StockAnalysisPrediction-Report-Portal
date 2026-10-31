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

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

var ErrManagedRemovalUnavailable = errors.New("runner managed removal is unavailable")

const managedRemovalReceiptFormat = "portal.runner.managed-removal-receipt/1-dev.1"

// ManagedRemovalReceipt is historical native evidence for one exact stop or
// non-forced container removal. It is not namespace deletion or Portal completion.
type ManagedRemovalReceipt struct {
	Format              string `json:"format"`
	DeploymentID        string `json:"deployment_id"`
	ControllerEpoch     string `json:"controller_epoch"`
	InstallationID      string `json:"installation_id"`
	OperationID         string `json:"operation_id"`
	SourceOperationID   string `json:"source_operation_id"`
	SourceEffectID      string `json:"source_effect_id"`
	TargetGeneration    string `json:"target_generation"`
	Stage               string `json:"stage"`
	EffectID            string `json:"effect_id"`
	PayloadSHA256       string `json:"payload_sha256"`
	SourceReceiptSHA256 string `json:"source_receipt_sha256"`
	ResourceID          string `json:"resource_id"`
	DataNamespaceID     string `json:"data_namespace_id"`
	ReleaseSHA256       string `json:"release_sha256"`
	NativeID            string `json:"native_id"`
	State               string `json:"state"`
	ObservedMS          string `json:"observed_ms"`
	SettledMS           string `json:"settled_ms"`
}

func validManagedRemovalReceipt(r ManagedRemovalReceipt) bool {
	if r.Format != managedRemovalReceiptFormat ||
		!((r.Stage == "stop" && r.State == "stopped") || (r.Stage == "remove_code" && r.State == "absent")) ||
		!dataSHA(r.PayloadSHA256) ||
		!dataSHA(r.SourceReceiptSHA256) ||
		!dataSHA(r.ReleaseSHA256) ||
		!containerSHAValue(r.NativeID) {
		return false
	}
	for _, id := range []string{r.DeploymentID, r.ControllerEpoch, r.InstallationID, r.OperationID, r.SourceOperationID, r.SourceEffectID, r.EffectID, r.ResourceID, r.DataNamespaceID} {
		if !enrollmentUUID.MatchString(id) || id == "00000000-0000-0000-0000-000000000000" {
			return false
		}
	}
	for _, number := range []string{r.TargetGeneration, r.ObservedMS, r.SettledMS} {
		if !dataNumber(number, false) {
			return false
		}
	}
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(r.SettledMS, 10, 64)
	return settled >= observed
}

func managedRemovalReceiptMatchesCommand(r ManagedRemovalReceipt, c control.ManagedRemovalCommand) bool {
	return r.DeploymentID == c.DeploymentID &&
		r.ControllerEpoch == c.ControllerEpoch &&
		r.InstallationID == c.InstallationID &&
		r.OperationID == c.OperationID &&
		r.SourceOperationID == c.Payload.SourceOperationID &&
		r.SourceEffectID == c.Payload.SourceEffectID &&
		r.TargetGeneration == c.TargetGeneration &&
		r.Stage == c.Stage &&
		r.EffectID == c.EffectID &&
		r.PayloadSHA256 == c.PayloadSHA256 &&
		r.SourceReceiptSHA256 == c.Payload.SourceReceiptSHA256 &&
		r.ResourceID == c.Payload.ResourceID &&
		r.DataNamespaceID == c.Payload.DataNamespaceID &&
		r.ReleaseSHA256 == c.Payload.ReleaseSHA256 &&
		((r.Stage == "stop" && r.State == "stopped") || (r.Stage == "remove_code" && r.State == "absent"))
}

func containerSHAValue(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == value
}

func ParseManagedRemovalReceipt(raw []byte) (ManagedRemovalReceipt, error) {
	var receipt ManagedRemovalReceipt
	if len(raw) == 0 || len(raw) > 16384 || !canonicalDataJSON(string(raw), &receipt) || !validManagedRemovalReceipt(receipt) {
		return ManagedRemovalReceipt{}, ErrFenceReconciliation
	}
	return receipt, nil
}

type managedRemovalSource struct {
	command            control.ManagedRemovalCommand
	source             CandidateRunningReceipt
	writable           *WritableCandidateRunningReceipt
	data               DataPreparationReceipt
	sourceFence        int64
	generation         int64
	writerFence        int64
	nativeID, platform string
	ownership          string
	resource           managedRemovalResource
	raw                []byte
	signature          []byte
}

type managedRemovalResource struct {
	nativeID, observed, desired, release, namespace, epoch, ownership string
	revision, generation, controlFence, writerFence                   int64
}

func loadManagedRemovalSource(ctx context.Context, tx *sql.Tx, c control.ManagedRemovalCommand, now int64) (managedRemovalSource, error) {
	var result managedRemovalSource
	result.command = c
	var sourceRaw, sourceKind, sourcePayload, sourceState string
	var sourceOperation string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,kind,payload_sha256,state,receipt_json,admitted_fence
        FROM runner_commands WHERE effect_id=? AND installation_id=? AND stage='start'`, c.Payload.SourceEffectID, c.InstallationID).
		Scan(&sourceOperation, &sourceKind, &sourcePayload, &sourceState, &sourceRaw, &result.sourceFence); err != nil {
		return result, ErrManagedRemovalUnavailable
	}
	sourceDigest := sha256.Sum256([]byte(sourceRaw))
	if sourceOperation != c.Payload.SourceOperationID || sourceKind != "start-candidate" || sourceState != "applied" ||
		!dataSHA(sourcePayload) || hex.EncodeToString(sourceDigest[:]) != c.Payload.SourceReceiptSHA256 || result.sourceFence < 1 {
		return result, fmt.Errorf("%w: managed removal source command", ErrFenceReconciliation)
	}
	var format struct {
		Format string `json:"format"`
	}
	if json.Unmarshal([]byte(sourceRaw), &format) != nil {
		return result, fmt.Errorf("%w: managed removal source receipt format", ErrFenceReconciliation)
	}
	target, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	switch format.Format {
	case candidateReceiptFormat:
		source, err := ParseCandidateRunningReceipt([]byte(sourceRaw))
		if err != nil ||
			source.InstallationID != c.InstallationID ||
			source.OperationID != c.Payload.SourceOperationID ||
			source.DataNamespaceID != c.Payload.DataNamespaceID ||
			source.ReleaseSHA256 != c.Payload.ReleaseSHA256 ||
			source.PayloadSHA256 != sourcePayload ||
			source.Claim.ResourceID != c.Payload.ResourceID ||
			source.Claim.Preflight == nil ||
			source.Claim.Preflight.Platform != c.Payload.Platform ||
			source.Claim.AdmittedFence != strconv.FormatInt(result.sourceFence, 10) {
			return result, fmt.Errorf("%w: managed removal ordinary source receipt", ErrFenceReconciliation)
		}
		result.source = source
		result.generation, _ = strconv.ParseInt(source.Generation, 10, 64)
		result.writerFence = mustDataInt(source.WriterFence)
		result.nativeID, result.platform = source.NativeID, source.Claim.Preflight.Platform
		result.ownership = source.Claim.OwnershipSHA256
	case writableRunningFormat, writableRecoveredRunningFormat:
		writable, err := ParseWritableCandidateRunningReceipt([]byte(sourceRaw))
		if err != nil ||
			writable.Claim.Preflight == nil ||
			writable.Claim.EffectID != c.Payload.SourceEffectID ||
			writable.Claim.ResourceID != c.Payload.ResourceID ||
			writable.Claim.PayloadSHA256 != sourcePayload ||
			writable.Claim.AdmittedFence != strconv.FormatInt(result.sourceFence, 10) ||
			writable.Claim.Preflight.Native.Platform != c.Payload.Platform ||
			writable.Claim.SandboxProfile != control.WritableCandidateSandboxProfile {
			return result, fmt.Errorf("%w: managed removal writable source receipt", ErrFenceReconciliation)
		}
		revision, _ := strconv.ParseInt(writable.Claim.ControlRevision, 10, 64)
		current, _ := strconv.ParseInt(c.ControlRevision, 10, 64)
		if revision < 1 || current < revision {
			return result, fmt.Errorf("%w: managed removal writable control revision", ErrFenceReconciliation)
		}
		result.writable = &writable
		result.generation = target - 1
		result.nativeID = writable.NativeID
		result.platform = writable.Claim.Preflight.Native.Platform
		result.ownership = writable.Claim.OwnershipSHA256
	default:
		return result, fmt.Errorf("%w: managed removal source receipt profile", ErrFenceReconciliation)
	}
	if target != result.generation+1 || result.generation < 1 {
		return result, fmt.Errorf("%w: managed removal target generation", ErrFenceReconciliation)
	}

	var dataRaw, dataKind, dataState string
	if err := tx.QueryRowContext(ctx, `SELECT kind,state,receipt_json FROM runner_commands
        WHERE operation_id=? AND stage='prepare_data' AND effect_number=1`, c.Payload.SourceOperationID).
		Scan(&dataKind, &dataState, &dataRaw); err != nil {
		return result, ErrManagedRemovalUnavailable
	}
	data, err := ParseDataPreparationReceipt([]byte(dataRaw))
	dataDigest := sha256.Sum256([]byte(dataRaw))
	if err != nil || dataKind != "prepare-data" || dataState != "applied" ||
		data.InstallationID != c.InstallationID ||
		data.DeploymentID != c.DeploymentID ||
		data.ControllerEpoch != c.ControllerEpoch ||
		data.ReleaseSHA256 != c.Payload.ReleaseSHA256 ||
		data.Generation != strconv.FormatInt(result.generation, 10) ||
		data.Claim.ResourceID != c.Payload.DataNamespaceID {
		return result, fmt.Errorf("%w: managed removal data receipt", ErrFenceReconciliation)
	}
	if result.writable != nil {
		if result.writable.Claim.DataReceiptSHA256 != hex.EncodeToString(dataDigest[:]) {
			return result, fmt.Errorf("%w: managed removal writable data digest", ErrFenceReconciliation)
		}
		result.writerFence = mustDataInt(data.WriterFence)
	} else if data.WriterFence != result.source.WriterFence || data.DataFormat != result.source.DataFormat {
		return result, fmt.Errorf("%w: managed removal ordinary data receipt", ErrFenceReconciliation)
	}
	if result.writerFence < 1 {
		return result, fmt.Errorf("%w: managed removal writer fence", ErrFenceReconciliation)
	}
	result.data = data
	var nativeID, observed, desired, release, namespace, epoch, ownership sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT native_id,observed_state,desired_state,release_sha256,data_namespace_id,controller_epoch,ownership_sha256,
        revision,generation,control_fence,writer_fence FROM runner_resources WHERE id=? AND kind='container'`, c.Payload.ResourceID).
		Scan(&nativeID, &observed, &desired, &release, &namespace, &epoch, &ownership, &result.resource.revision, &result.resource.generation, &result.resource.controlFence, &result.resource.writerFence); err != nil {
		return result, ErrManagedRemovalUnavailable
	}
	result.resource.nativeID, result.resource.observed, result.resource.desired = nativeID.String, observed.String, desired.String
	result.resource.release, result.resource.namespace, result.resource.epoch = release.String, namespace.String, epoch.String
	result.resource.ownership = ownership.String
	if !nativeID.Valid || !ownership.Valid || nativeID.String != result.nativeID || ownership.String != result.ownership ||
		result.resource.release != c.Payload.ReleaseSHA256 ||
		result.resource.namespace != c.Payload.DataNamespaceID ||
		result.resource.epoch != c.ControllerEpoch ||
		result.resource.generation != result.generation ||
		result.resource.controlFence != result.sourceFence ||
		result.resource.writerFence != result.writerFence {
		return result, fmt.Errorf("%w: managed removal resource identity", ErrFenceReconciliation)
	}
	switch c.Stage {
	case "stop":
		if result.resource.revision != 2 || result.resource.observed != "running" || result.resource.desired != "present" {
			return result, fmt.Errorf("%w: managed removal stop resource state", ErrFenceReconciliation)
		}
	case "remove_code":
		if result.resource.revision != 3 || result.resource.observed != "stopped" || result.resource.desired != "stopped" {
			return result, fmt.Errorf("%w: managed removal remove-code resource state", ErrFenceReconciliation)
		}
	default:
		return result, ErrManagedRemovalUnavailable
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_commands WHERE installation_id=? AND effect_id<>? AND state IN ('prepared','unknown')`, c.InstallationID, c.EffectID).Scan(&pending); err != nil || pending != 0 {
		return result, fmt.Errorf("%w: managed removal pending command", ErrFenceReconciliation)
	}
	if now <= 0 {
		return result, fmt.Errorf("%w: managed removal clock", ErrFenceReconciliation)
	}
	return result, nil
}

func mustDataInt(value string) int64 {
	n, _ := strconv.ParseInt(value, 10, 64)
	return n
}

type managedRemovalNativeInput struct {
	request  driver.CandidateLaunchRequest
	writable *driver.WritableCandidateBinding
}

func managedRemovalNativeRequest(source managedRemovalSource, descriptor []byte) (managedRemovalNativeInput, error) {
	var zero managedRemovalNativeInput
	project, err := strconv.ParseUint(source.data.ProjectID, 10, 32)
	if err != nil {
		return zero, fmt.Errorf("%w: managed removal project id", ErrFenceReconciliation)
	}
	device, err := strconv.ParseUint(source.data.Device, 10, 64)
	if err != nil {
		return zero, fmt.Errorf("%w: managed removal device", ErrFenceReconciliation)
	}
	inode, err := strconv.ParseUint(source.data.NamespaceInode, 10, 64)
	if err != nil {
		return zero, fmt.Errorf("%w: managed removal namespace inode", ErrFenceReconciliation)
	}
	quotaBytes, err := strconv.ParseInt(source.data.QuotaBytes, 10, 64)
	if err != nil {
		return zero, fmt.Errorf("%w: managed removal quota bytes", ErrFenceReconciliation)
	}
	quotaInodes, err := strconv.ParseInt(source.data.QuotaInodes, 10, 64)
	if err != nil {
		return zero, fmt.Errorf("%w: managed removal quota inodes", ErrFenceReconciliation)
	}
	result := managedRemovalNativeInput{request: driver.CandidateLaunchRequest{
		Identity: driver.CandidateIdentity{
			DeploymentID:    source.command.DeploymentID,
			ControllerEpoch: source.command.ControllerEpoch,
			InstallationID:  source.command.InstallationID,
			OperationID:     source.command.Payload.SourceOperationID,
			ResourceID:      source.command.Payload.ResourceID,
			NamespaceID:     source.command.Payload.DataNamespaceID,
			ReleaseSHA256:   source.command.Payload.ReleaseSHA256,
			Generation:      strconv.FormatInt(source.generation, 10),
			ControlFence:    strconv.FormatInt(source.sourceFence, 10),
			WriterFence:     strconv.FormatInt(source.writerFence, 10),
		},
		Descriptor: bytes.Clone(descriptor),
		Platform:   source.command.Payload.Platform,
		Data: driver.CandidateDataEvidence{
			Root:           markerRoot(source.data.Claim),
			ProjectID:      uint32(project),
			Device:         device,
			NamespaceInode: inode,
			QuotaBytes:     quotaBytes,
			QuotaInodes:    quotaInodes,
		},
	}}
	if source.writable != nil {
		binding := driver.WritableCandidateBinding{
			EffectID: source.command.Payload.SourceEffectID, ClaimID: source.writable.Claim.ClaimID,
			PayloadSHA256: source.writable.Claim.PayloadSHA256, OwnershipSHA256: source.writable.Claim.OwnershipSHA256,
			DataReceiptSHA256: source.writable.Claim.DataReceiptSHA256, WriterReceiptSHA256: source.writable.Claim.WriterReceiptSHA256,
		}
		result.writable = &binding
	}
	return result, nil
}

func (s *Store) beginManagedRemoval(ctx context.Context, raw, signature []byte) (managedRemovalSource, bool, error) {
	var zero managedRemovalSource
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
	verified, err := control.VerifyManagedRemoval(raw, signature, authority, now)
	if err != nil {
		return zero, false, err
	}
	command := verified.Command()
	var storedStage, storedKind, storedPayload, storedEpoch, storedOperation, storedInstallation, storedState, storedReceipt string
	var storedSlot, storedGeneration, storedFence int64
	err = tx.QueryRowContext(ctx, `SELECT operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json
        FROM runner_commands WHERE effect_id=?`, command.EffectID).Scan(&storedOperation, &storedInstallation, &storedStage, &storedSlot, &storedKind, &storedPayload, &storedGeneration, &storedEpoch, &storedFence, &storedState, &storedReceipt)
	if err == nil {
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		if storedOperation != command.OperationID || storedInstallation != command.InstallationID || storedStage != command.Stage || storedKind != command.Kind || storedPayload != command.PayloadSHA256 || storedGeneration != generation || storedEpoch != command.ControllerEpoch || storedSlot != slot {
			return zero, false, ErrCommandConflict
		}
		if storedState == "applied" {
			receipt, err := ParseManagedRemovalReceipt([]byte(storedReceipt))
			if err != nil || !managedRemovalReceiptMatchesCommand(receipt, command) {
				return zero, false, fmt.Errorf("%w: managed removal replay receipt", ErrFenceReconciliation)
			}
			return zero, true, nil
		}
		if storedState == "unknown" {
			return zero, false, fmt.Errorf("%w: managed removal replay uncertain", ErrFenceReconciliation)
		}
		if storedState != "prepared" {
			return zero, false, fmt.Errorf("%w: managed removal replay state", ErrFenceReconciliation)
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		source, err := loadManagedRemovalSource(ctx, tx, command, now)
		if err != nil {
			return zero, false, err
		}
		slot, _ := strconv.ParseInt(command.EffectNumber, 10, 64)
		generation, _ := strconv.ParseInt(command.TargetGeneration, 10, 64)
		fence, _ := strconv.ParseInt(command.InstallationFence, 10, 64)
		if fence < source.sourceFence || fence < source.resource.controlFence {
			return zero, false, fmt.Errorf("%w: managed removal fence %d below source %d/resource %d", ErrFenceReconciliation, fence, source.sourceFence, source.resource.controlFence)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runner_commands(effect_id,operation_id,installation_id,stage,effect_number,kind,payload_sha256,target_generation,controller_epoch,admitted_fence,state,receipt_json,ack_attempted_ms,created_ms,updated_ms)
            VALUES(?,?,?,?,?,?,?,?,?,?,'prepared','{}',NULL,?,?)`, command.EffectID, command.OperationID, command.InstallationID, command.Stage, slot, command.Kind, command.PayloadSHA256, generation, command.ControllerEpoch, fence, now, now); err != nil {
			return zero, false, err
		}
		changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}'`, now, command.EffectID)
		if err != nil {
			return zero, false, err
		}
		if n, err := changed.RowsAffected(); err != nil || n != 1 {
			return zero, false, fmt.Errorf("%w: managed removal intent transition", ErrFenceReconciliation)
		}
		if err := tx.Commit(); err != nil {
			return zero, false, err
		}
		return source, false, nil
	} else {
		return zero, false, err
	}
	source, err := loadManagedRemovalSource(ctx, tx, command, now)
	if err != nil {
		return zero, false, err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_commands SET state='unknown',updated_ms=? WHERE effect_id=? AND state='prepared' AND receipt_json='{}'`, now, command.EffectID)
	if err != nil {
		return zero, false, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, false, fmt.Errorf("%w: managed removal prepared transition", ErrFenceReconciliation)
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return source, false, nil
}

func (s *Store) checkManagedRemovalPrepared(ctx context.Context, source managedRemovalSource, snapshot driver.CandidateLaunchSnapshot, writable *driver.WritableCandidateLaunchSnapshot) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := loadManagedRemovalSource(ctx, tx, source.command, time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	if !current.preflightMatches(snapshot) {
		return fmt.Errorf("%w: managed removal prepared preflight", ErrFenceReconciliation)
	}
	if current.writable != nil && (writable == nil || writable.Launch != snapshot || writable.Binding != *current.writableBinding()) {
		return fmt.Errorf("%w: managed removal writable prepared binding", ErrFenceReconciliation)
	}
	if current.writable == nil && writable != nil {
		return fmt.Errorf("%w: managed removal prepared profile", ErrFenceReconciliation)
	}
	var state, receipt string
	if err := tx.QueryRowContext(ctx, `SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, source.command.EffectID).Scan(&state, &receipt); err != nil || state != "unknown" || receipt != "{}" {
		return fmt.Errorf("%w: managed removal unknown intent", ErrFenceReconciliation)
	}
	return tx.Commit()
}

func (s *Store) settleManagedRemoval(ctx context.Context, source managedRemovalSource, observation driver.CandidateRemovalObservation) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	snapshot, err := observation.Snapshot()
	if err != nil {
		return zero, err
	}
	if !source.preflightMatches(snapshot.Launch) || snapshot.NativeID != source.resource.nativeID {
		return zero, ErrFenceReconciliation
	}
	expectedState := "exited"
	if source.command.Stage == "remove_code" {
		expectedState = "absent"
	}
	if snapshot.State != expectedState {
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
	if _, err := control.VerifyManagedRemoval(source.commandRaw(), source.commandSignature(), authority, now); err != nil {
		return zero, ErrFenceReconciliation
	}
	current, err := loadManagedRemovalSource(ctx, tx, source.command, now)
	if err != nil {
		return zero, err
	}
	if current.resource.nativeID != source.resource.nativeID || !sameCandidateRemovalProvenance(source, current) {
		return zero, ErrFenceReconciliation
	}
	var state, receipt string
	if err := tx.QueryRowContext(ctx, `SELECT state,receipt_json FROM runner_commands WHERE effect_id=?`, source.command.EffectID).Scan(&state, &receipt); err != nil || state != "unknown" || receipt != "{}" {
		return zero, ErrFenceReconciliation
	}
	nextRevision := int64(3)
	nextState, nextDesired := "stopped", "stopped"
	if source.command.Stage == "remove_code" {
		nextRevision, nextState, nextDesired = 4, "absent", "absent"
	}
	changed, err := tx.ExecContext(ctx, `UPDATE runner_resources SET desired_state=?,observed_state=?,revision=?,observed_ms=?,updated_ms=?
        WHERE id=? AND kind='container' AND native_id=? AND revision=? AND desired_state=? AND observed_state=?`,
		nextDesired, nextState, nextRevision, snapshot.ObservedMS, now, current.resourceID(), current.resource.nativeID, current.resource.revision, current.resource.desired, current.resource.observed)
	if err != nil {
		return zero, err
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, ErrFenceReconciliation
	}
	result := ManagedRemovalReceipt{
		Format: managedRemovalReceiptFormat, DeploymentID: source.command.DeploymentID, ControllerEpoch: source.command.ControllerEpoch,
		InstallationID: source.command.InstallationID, OperationID: source.command.OperationID,
		SourceOperationID: source.command.Payload.SourceOperationID, SourceEffectID: source.command.Payload.SourceEffectID,
		TargetGeneration: source.command.TargetGeneration, Stage: source.command.Stage, EffectID: source.command.EffectID,
		PayloadSHA256: source.command.PayloadSHA256, SourceReceiptSHA256: source.command.Payload.SourceReceiptSHA256,
		ResourceID: source.command.Payload.ResourceID, DataNamespaceID: source.command.Payload.DataNamespaceID,
		ReleaseSHA256: source.command.Payload.ReleaseSHA256, NativeID: snapshot.NativeID, State: nextState,
		ObservedMS: strconv.FormatInt(snapshot.ObservedMS, 10), SettledMS: strconv.FormatInt(now, 10),
	}
	if !validManagedRemovalReceipt(result) {
		return zero, ErrFenceReconciliation
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 16384 {
		return zero, ErrFenceReconciliation
	}
	changed, err = tx.ExecContext(ctx, `UPDATE runner_commands SET state='applied',receipt_json=?,updated_ms=? WHERE effect_id=? AND state='unknown' AND receipt_json='{}'`, string(encoded), now, source.command.EffectID)
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

func (source managedRemovalSource) commandRaw() []byte       { return source.raw }
func (source managedRemovalSource) commandSignature() []byte { return source.signature }
func (source managedRemovalSource) resourceID() string       { return source.command.Payload.ResourceID }
func (source managedRemovalSource) writableBinding() *driver.WritableCandidateBinding {
	if source.writable == nil {
		return nil
	}
	return &driver.WritableCandidateBinding{
		EffectID: source.command.Payload.SourceEffectID, ClaimID: source.writable.Claim.ClaimID,
		PayloadSHA256: source.writable.Claim.PayloadSHA256, OwnershipSHA256: source.writable.Claim.OwnershipSHA256,
		DataReceiptSHA256: source.writable.Claim.DataReceiptSHA256, WriterReceiptSHA256: source.writable.Claim.WriterReceiptSHA256,
	}
}
func (source managedRemovalSource) preflightMatches(snapshot driver.CandidateLaunchSnapshot) bool {
	if source.writable != nil {
		return source.writable.Claim.Preflight != nil && source.writable.Claim.Preflight.Native == candidatePreflight(snapshot)
	}
	return source.source.Claim.Preflight != nil && *source.source.Claim.Preflight == candidatePreflight(snapshot)
}

func sameCandidateRemovalProvenance(a, b managedRemovalSource) bool {
	writable := a.writable == nil && b.writable == nil ||
		a.writable != nil && b.writable != nil && *a.writable == *b.writable
	return a.command == b.command && a.source == b.source && writable && a.data == b.data &&
		a.sourceFence == b.sourceFence && a.generation == b.generation && a.writerFence == b.writerFence &&
		a.nativeID == b.nativeID && a.platform == b.platform && a.ownership == b.ownership
}

func (s *Store) DispatchManagedRemoval(ctx context.Context, raw, signature []byte, descriptor []byte, native *driver.CandidateRuntime) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	if native == nil || s.closing.Load() {
		return zero, ErrManagedRemovalUnavailable
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	if err != nil || parsed.Manifest.Backend == nil {
		return zero, ErrManagedRemovalUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	source, replay, err := s.beginManagedRemoval(ctx, raw, signature)
	if err != nil {
		return zero, err
	}
	if replay {
		return s.ReadManagedRemovalReceipt(ctx, raw, signature)
	}
	source.raw, source.signature = raw, signature
	if source.command.Payload.ReleaseSHA256 != parsed.SHA256 {
		return zero, fmt.Errorf("%w: managed removal descriptor release", ErrFenceReconciliation)
	}
	input, err := managedRemovalNativeRequest(source, descriptor)
	if err != nil {
		return zero, err
	}
	var observation driver.CandidateRemovalObservation
	if input.writable != nil {
		launch, err := native.PrepareWritableCandidateRemoval(ctx, driver.WritableCandidateLaunchRequest{CandidateLaunchRequest: input.request, Binding: *input.writable})
		if err != nil {
			return zero, err
		}
		defer launch.Close()
		snapshot, err := launch.Snapshot()
		if err != nil {
			return zero, err
		}
		if err := s.checkManagedRemovalPrepared(ctx, source, snapshot.Launch, &snapshot); err != nil {
			return zero, err
		}
		if source.command.Stage == "stop" {
			observation, err = native.StopWritableCandidate(ctx, launch, source.nativeID)
		} else {
			observation, err = native.RemoveWritableCandidate(ctx, launch, source.nativeID)
		}
	} else {
		launch, err := native.PrepareCandidate(ctx, input.request)
		if err != nil {
			return zero, err
		}
		defer launch.Close()
		snapshot, err := launch.Snapshot()
		if err != nil {
			return zero, err
		}
		if err := s.checkManagedRemovalPrepared(ctx, source, snapshot, nil); err != nil {
			return zero, err
		}
		if source.command.Stage == "stop" {
			observation, err = native.StopCandidate(ctx, launch, source.nativeID)
		} else {
			observation, err = native.RemoveCandidate(ctx, launch, source.nativeID)
		}
	}
	if err != nil {
		return zero, err
	}
	return s.settleManagedRemoval(ctx, source, observation)
}

func (s *Store) ReadManagedRemovalReceipt(ctx context.Context, raw, signature []byte) (ManagedRemovalReceipt, error) {
	var zero ManagedRemovalReceipt
	if s.closing.Load() {
		return zero, ErrManagedRemovalUnavailable
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
	verified, err := control.VerifyManagedRemoval(raw, signature, authority, time.Now().UTC().UnixMilli())
	if err != nil {
		return zero, err
	}
	command := verified.Command()
	var receipt string
	if err := tx.QueryRowContext(ctx, `SELECT receipt_json FROM runner_commands WHERE effect_id=? AND operation_id=? AND installation_id=? AND state='applied'`, command.EffectID, command.OperationID, command.InstallationID).Scan(&receipt); err != nil {
		return zero, ErrManagedRemovalUnavailable
	}
	result, err := ParseManagedRemovalReceipt([]byte(receipt))
	if err != nil || !managedRemovalReceiptMatchesCommand(result, command) {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
