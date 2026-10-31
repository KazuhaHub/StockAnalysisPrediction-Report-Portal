package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

type platformRestoreIntent struct {
	recoverySetID, effectID string
	runtime                 platformRuntimeRegistration
	authority               control.Authority
	command                 control.RestoreCommand
	checkpoint              journal.CheckpointReceipt
	source                  platformCheckpointSource
	raw, signature          []byte
}

const platformManagedRestoreFormat = "portal.managed-restore-review/1-dev.1"

type platformManagedRestorePlan struct {
	Format                string `json:"format"`
	InstallationID        string `json:"installation_id"`
	RecoverySetID         string `json:"recovery_set_id"`
	CheckpointOperationID string `json:"checkpoint_operation_id"`
	CheckpointEffectID    string `json:"checkpoint_effect_id"`
	CheckpointID          string `json:"checkpoint_id"`
	ReleaseSHA256         string `json:"release_sha256"`
	ManifestSHA256        string `json:"manifest_sha256"`
	ArchiveBytes          int64  `json:"archive_bytes"`
	SourceGeneration      int64  `json:"source_generation"`
	TargetGeneration      int64  `json:"target_generation"`
	ControllerEpoch       string `json:"controller_epoch"`
}

func platformRestoreReceiptMatchesCommand(receipt journal.RestoreReceipt, command control.RestoreCommand) bool {
	p := command.Payload
	return receipt.Stage == "restore_data" && receipt.State == "restored" &&
		receipt.DeploymentID == command.DeploymentID &&
		receipt.ControllerEpoch == command.ControllerEpoch &&
		receipt.InstallationID == command.InstallationID &&
		receipt.OperationID == command.OperationID &&
		receipt.TargetGeneration == command.TargetGeneration &&
		receipt.EffectID == command.EffectID &&
		receipt.PayloadSHA256 == command.PayloadSHA256 &&
		receipt.RecoverySetID == p.RecoverySetID &&
		receipt.CheckpointID == p.CheckpointID &&
		receipt.NamespaceID == p.NamespaceID &&
		receipt.ReleaseSHA256 == p.ReleaseSHA256 &&
		receipt.ManifestSHA256 == p.ManifestSHA256 &&
		receipt.ArchiveBytes == p.ArchiveBytes &&
		receipt.ProjectID == p.ProjectID &&
		receipt.NamespaceInode == p.NamespaceInode &&
		receipt.Device == p.Device &&
		receipt.DataFormat == p.DataFormat &&
		receipt.WriterFence == p.WriterFence &&
		receipt.SourceCheckpointOperationID == p.SourceCheckpointOperationID &&
		receipt.SourceCheckpointEffectID == p.SourceCheckpointEffectID &&
		receipt.SourceCheckpointReceiptSHA == p.SourceCheckpointReceiptSHA
}

func loadPlatformRestoreCheckpoint(e migExec, expected platformOperationLease) (string, string, journal.CheckpointReceipt, int64, error) {
	var planRaw, planSHA, planInstallation, planEpoch string
	var planSource, planTarget int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,p.installation_id,p.controller_epoch,
		o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='restore' AND o.managed=1
		AND o.stage='restore_data'`, expected.id, expected.installationID).
		Scan(&planRaw, &planSHA, &planInstallation, &planEpoch, &planSource, &planTarget); err != nil {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore plan row: %v", errAppPlatformStageEvidence, err)
	}
	var plan platformManagedRestorePlan
	unmarshalErr := json.Unmarshal([]byte(planRaw), &plan)
	canonical, marshalErr := json.Marshal(plan)
	if unmarshalErr != nil || marshalErr != nil ||
		string(canonical) != planRaw || platformDigest([]byte(planRaw)) != planSHA ||
		plan.Format != platformManagedRestoreFormat ||
		plan.InstallationID != expected.installationID || planInstallation != expected.installationID ||
		planEpoch != expected.controllerEpoch || plan.ControllerEpoch != expected.controllerEpoch ||
		planSource != int64(expected.snapshot.Generation)-1 ||
		planTarget != int64(expected.snapshot.Generation) ||
		plan.SourceGeneration != planSource || plan.TargetGeneration != planTarget ||
		!platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.CheckpointOperationID) ||
		!platformValidUUID(plan.CheckpointEffectID) || !platformValidUUID(plan.CheckpointID) ||
		!platformValidSHA(plan.ReleaseSHA256) || !platformValidSHA(plan.ManifestSHA256) ||
		plan.ArchiveBytes <= 0 {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore plan binding", errAppPlatformStageEvidence)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var recoverySetID, recoveryOperation, checkpointID, effectID, receiptRaw, state string
	var manifest string
	var bytes int64
	if err := e.queryRow(`SELECT id,operation_id,checkpoint_id,manifest_sha256,bytes,state
		FROM app_platform_recovery_sets WHERE id=? AND installation_id=? AND operation_id=?`+lock,
		plan.RecoverySetID, expected.installationID, plan.CheckpointOperationID).
		Scan(&recoverySetID, &recoveryOperation, &checkpointID, &manifest, &bytes, &state); err != nil {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore recovery row", errAppPlatformStageEvidence)
	}
	if recoverySetID != plan.RecoverySetID || recoveryOperation != plan.CheckpointOperationID ||
		checkpointID != plan.CheckpointID || manifest != plan.ManifestSHA256 ||
		bytes != plan.ArchiveBytes || (state != "verified" && state != "restoring") {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore recovery fields", errAppPlatformStageEvidence)
	}
	var recoveryAction, recoveryStage string
	var recoveryManaged, recoverySource, recoveryTarget int64
	var recoveryCompleted sql.NullInt64
	if err := e.queryRow(`SELECT action,managed,source_generation,target_generation,stage,completed_ms
		FROM app_platform_operations WHERE id=? AND installation_id=?`,
		recoveryOperation, expected.installationID).
		Scan(&recoveryAction, &recoveryManaged, &recoverySource, &recoveryTarget, &recoveryStage, &recoveryCompleted); err != nil ||
		recoveryAction != "update" || recoveryManaged != 1 ||
		recoverySource != int64(expected.snapshot.Generation)-1 ||
		recoveryTarget != int64(expected.snapshot.Generation) ||
		recoveryStage != "recovery_required" || !recoveryCompleted.Valid {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore source operation", errAppPlatformStageEvidence)
	}
	var checkpointOperation, effectPayload string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,operation_id,state
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`,
		recoveryOperation).
		Scan(&effectID, &effectPayload, &receiptRaw, &checkpointOperation, &state); err != nil ||
		state != "applied" || !platformValidUUID(effectID) || checkpointOperation != recoveryOperation ||
		!platformValidSHA(effectPayload) {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore checkpoint effect", errAppPlatformStageEvidence)
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(receiptRaw))
	if err != nil || receipt.EffectID != effectID || effectID != plan.CheckpointEffectID ||
		receipt.PayloadSHA256 != effectPayload ||
		receipt.OperationID != checkpointOperation ||
		receipt.CheckpointID != checkpointID || receipt.ManifestSHA256 != manifest ||
		receipt.ReleaseSHA256 != plan.ReleaseSHA256 || mustPlatformCheckpointInt(receipt.ArchiveBytes) != bytes ||
		receipt.TargetGeneration != strconv.FormatUint(expected.snapshot.Generation, 10) ||
		!platformCheckpointReceiptMatchesSourceOperation(receipt, expected.installationID) {
		return "", "", journal.CheckpointReceipt{}, 0, fmt.Errorf("%w: restore checkpoint receipt", errAppPlatformStageEvidence)
	}
	return recoverySetID, effectID, receipt, bytes, nil
}

func platformCheckpointReceiptMatchesSourceOperation(receipt journal.CheckpointReceipt, installation string) bool {
	return receipt.InstallationID == installation && platformValidUUID(receipt.SourceOperationID) &&
		platformValidUUID(receipt.SourceStartEffectID) && platformValidUUID(receipt.SourceDataEffectID)
}

func platformRestoreCommand(expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, recoverySetID, effectID string) (control.RestoreCommand, error) {
	command := control.RestoreCommand{
		Format: control.RestoreFormat, DeploymentID: runtime.deploymentID,
		ControllerEpoch: expected.controllerEpoch, ControlRevision: strconv.FormatInt(runtime.controlRevision, 10),
		InstallationID: expected.installationID, OperationID: expected.id,
		TargetGeneration: strconv.FormatUint(expected.snapshot.Generation, 10),
		Stage:            "restore_data", EffectNumber: "1", EffectID: effectID,
		Kind: "restore", InstallationFence: strconv.FormatInt(expected.fence, 10),
		Payload: control.RestorePayload{
			RecoverySetID: recoverySetID, CheckpointID: checkpoint.CheckpointID,
			NamespaceID: checkpoint.NamespaceID, ReleaseSHA256: checkpoint.ReleaseSHA256,
			ManifestSHA256: checkpoint.ManifestSHA256, ArchiveBytes: checkpoint.ArchiveBytes,
			ProjectID: checkpoint.ProjectID, NamespaceInode: checkpoint.NamespaceInode,
			Device: checkpoint.Device, DataFormat: checkpoint.DataFormat,
			WriterFence:                 checkpoint.WriterFence,
			SourceCheckpointOperationID: checkpoint.OperationID,
			SourceCheckpointEffectID:    checkpoint.EffectID,
			SourceCheckpointReceiptSHA:  platformDigest([]byte(mustMarshalPlatformCheckpointReceipt(checkpoint))),
		},
	}
	if source.namespaceID != checkpoint.NamespaceID || source.releaseSHA256 != checkpoint.ReleaseSHA256 ||
		source.dataFormat != checkpoint.DataFormat || source.writerFence != checkpoint.WriterFence ||
		source.operationID != checkpoint.SourceOperationID ||
		source.startEffectID != checkpoint.SourceStartEffectID ||
		source.sourceReceiptSHA256 != checkpoint.SourceReceiptSHA256 ||
		source.dataEffectID != checkpoint.SourceDataEffectID ||
		source.sourceDataReceiptSHA256 != checkpoint.SourceDataReceiptSHA256 ||
		source.resourceID != checkpoint.ResourceID || source.projectID != checkpoint.ProjectID ||
		source.namespaceInode != checkpoint.NamespaceInode || source.device != checkpoint.Device {
		return control.RestoreCommand{}, errAppPlatformStageEvidence
	}
	digest, err := control.RestoreLogicalDigest(command)
	if err != nil {
		return control.RestoreCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func mustMarshalPlatformCheckpointReceipt(receipt journal.CheckpointReceipt) []byte {
	raw, _ := json.Marshal(receipt)
	return raw
}

func loadPlatformRestoreIntent(e migExec, expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, now int64, create bool) (platformRestoreIntent, bool, error) {
	var zero platformRestoreIntent
	recoverySetID, _, _, _, err := loadPlatformRestoreCheckpoint(e, expected)
	if err != nil {
		return zero, false, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var recoveryState, storedRelease, storedNamespace, storedEpoch string
	var storedFormat, storedWriter, storedBytes int64
	var storedManifest sql.NullString
	if err := e.queryRow(`SELECT release_sha256,data_format,data_namespace_id,state,controller_epoch,
		writer_fence,manifest_sha256,bytes FROM app_platform_recovery_sets WHERE id=?`+lock,
		recoverySetID).Scan(&storedRelease, &storedFormat, &storedNamespace, &recoveryState,
		&storedEpoch, &storedWriter, &storedManifest, &storedBytes); err != nil {
		return zero, false, err
	}
	var effectID, kind, digest, effectEpoch, effectState, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,controller_epoch,
		admitted_fence,observed_fence,state,receipt_json
		FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`+lock,
		expected.id).Scan(&effectID, &kind, &digest, &effectEpoch, &admitted, &observed,
		&effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	if fresh {
		if !create || recoveryState != "verified" {
			return zero, false, errAppPlatformStageEvidence
		}
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, false, err
		}
		command, err := platformRestoreCommand(expected, runtime, source, checkpoint, recoverySetID, effectID)
		if err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'restore_data',1,?,'restore',?,?,?, ?,0,'prepared','{}',?,?)`,
			expected.id, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch,
			expected.fence, now, now); err != nil {
			return zero, false, err
		}
		changed, err := e.exec(`UPDATE app_platform_recovery_sets SET state='restoring',updated_ms=?
			WHERE id=? AND installation_id=? AND release_sha256=? AND data_format=?
			AND data_namespace_id=? AND checkpoint_id=? AND manifest_sha256=? AND bytes=?
			AND state='verified' AND controller_epoch=? AND writer_fence=?`,
			now, recoverySetID, expected.installationID, checkpoint.ReleaseSHA256,
			mustPlatformCheckpointInt(checkpoint.DataFormat), checkpoint.NamespaceID,
			checkpoint.CheckpointID, checkpoint.ManifestSHA256, mustPlatformCheckpointInt(checkpoint.ArchiveBytes),
			expected.controllerEpoch, mustPlatformCheckpointInt(checkpoint.WriterFence))
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, false, err
		}
		zero = platformRestoreIntent{
			recoverySetID: recoverySetID, effectID: effectID, runtime: runtime,
			authority: control.Authority{
				DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
				ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey),
				Fingerprint: runtime.fingerprint,
			},
			command: command, checkpoint: checkpoint, source: source,
		}
		return zero, true, nil
	}
	if recoveryState != "restoring" || storedRelease != checkpoint.ReleaseSHA256 ||
		storedFormat != mustPlatformCheckpointInt(checkpoint.DataFormat) ||
		storedNamespace != checkpoint.NamespaceID || storedEpoch != expected.controllerEpoch ||
		storedWriter != mustPlatformCheckpointInt(checkpoint.WriterFence) ||
		!storedManifest.Valid || storedManifest.String != checkpoint.ManifestSHA256 ||
		storedBytes != mustPlatformCheckpointInt(checkpoint.ArchiveBytes) ||
		kind != "restore" || effectEpoch != expected.controllerEpoch ||
		admitted != expected.fence || observed != 0 || effectState != "prepared" || receipt != "{}" {
		return zero, false, errAppPlatformStageEvidence
	}
	command, err := platformRestoreCommand(expected, runtime, source, checkpoint, recoverySetID, effectID)
	if err != nil || command.PayloadSHA256 != digest {
		return zero, false, errAppPlatformStageEvidence
	}
	return platformRestoreIntent{
		recoverySetID: recoverySetID, effectID: effectID, runtime: runtime,
		authority: control.Authority{
			DeploymentID: runtime.deploymentID, ControllerEpoch: runtime.controllerEpoch,
			ControlRevision: runtime.controlRevision, PublicKey: bytes.Clone(runtime.publicKey),
			Fingerprint: runtime.fingerprint,
		},
		command: command, checkpoint: checkpoint, source: source,
	}, false, nil
}

func (s *Server) preparePlatformRestore(ctx context.Context, expected platformOperationLease) (platformRestoreIntent, error) {
	var zero platformRestoreIntent
	if expected.snapshot.Action != appplatform.ActionRestore || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRestoreData || expected.snapshot.Generation < 2 {
		return zero, errAppPlatformStageEvidence
	}
	if ctx == nil || ctx.Err() != nil {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, fmt.Errorf("restore runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(e, expected, runtime, facts.now)
	if err != nil {
		return zero, fmt.Errorf("%w: restore checkpoint source", errAppPlatformStageEvidence)
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, expected)
	if err != nil {
		return zero, fmt.Errorf("restore checkpoint authority: %w", err)
	}
	if recoverySetID == "" || !platformCheckpointReceiptMatchesSourceOperation(checkpoint, expected.installationID) {
		return zero, fmt.Errorf("%w: restore checkpoint authority", errAppPlatformStageEvidence)
	}
	result, fresh, err := loadPlatformRestoreIntent(e, expected, runtime, source, checkpoint, facts.now, true)
	if err != nil {
		return zero, fmt.Errorf("%w: restore intent load", errAppPlatformStageEvidence)
	}
	if fresh {
		detail, _ := json.Marshal(struct {
			RecoverySetID string `json:"recovery_set_id"`
			EffectID      string `json:"effect_id"`
		}{result.recoverySetID, result.effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.restore_intent','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if expires <= issued {
		return zero, errAppPlatformLeaseConflict
	}
	result.command.IssuedMS, result.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	result.raw, result.signature, err = control.SignRestore(result.command, key, result.authority, issued)
	if err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

type platformRestoreObservation interface {
	RestoreReceipt() (journal.RestoreReceipt, error)
	Controller() control.Authority
	Endpoint() transport.CandidateReceiptEndpoint
	ReceivedAt() time.Time
	CommandSHA256() string
}

func validPlatformRestoreObservation(observation platformRestoreObservation, intent platformRestoreIntent, endpoint platformRuntimeEndpoint) (journal.RestoreReceipt, bool) {
	receipt, err := observation.RestoreReceipt()
	if err != nil || !platformRestoreReceiptMatchesCommand(receipt, intent.command) ||
		observation.CommandSHA256() != platformDigest(intent.raw) || observation.ReceivedAt().IsZero() {
		return journal.RestoreReceipt{}, false
	}
	a, b := observation.Controller(), intent.authority
	if a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch ||
		a.ControlRevision != b.ControlRevision || a.Fingerprint != b.Fingerprint ||
		!bytes.Equal(a.PublicKey, b.PublicKey) {
		return journal.RestoreReceipt{}, false
	}
	peer := observation.Endpoint()
	age := time.Since(observation.ReceivedAt())
	observed, observedErr := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	settled, settledErr := strconv.ParseInt(receipt.SettledMS, 10, 64)
	issued, issuedErr := strconv.ParseInt(intent.command.IssuedMS, 10, 64)
	if !peer.RootController || peer.Path != endpoint.path || peer.RunnerUID != 0 ||
		peer.ControllerUID != endpoint.controllerUID || peer.ControllerGID != endpoint.controllerGID ||
		age < 0 || age > 3*time.Second || observedErr != nil || settledErr != nil || issuedErr != nil ||
		observed < issued || observed > settled || settled > observation.ReceivedAt().UnixMilli() {
		return journal.RestoreReceipt{}, false
	}
	return receipt, true
}
