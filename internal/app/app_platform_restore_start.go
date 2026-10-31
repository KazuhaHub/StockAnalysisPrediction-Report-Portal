package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func platformRestoreStartCommand(lease platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, checkpointRaw []byte, activation, effectID string, targetPolicy int64) (control.UpdateStartCommand, error) {
	if checkpointRaw == nil || activation == "" || effectID == "" ||
		targetPolicy < 1 {
		return control.UpdateStartCommand{}, errAppPlatformStageEvidence
	}
	command := control.UpdateStartCommand{
		Format: control.UpdateStartFormat, DeploymentID: runtime.deploymentID,
		ControllerEpoch: lease.controllerEpoch,
		ControlRevision: strconv.FormatInt(runtime.controlRevision, 10),
		InstallationID:  lease.installationID, OperationID: lease.id,
		TargetGeneration: strconv.FormatUint(lease.snapshot.Generation, 10),
		Stage:            "start", EffectNumber: "1", EffectID: effectID,
		Kind: "restore-start", InstallationFence: strconv.FormatInt(lease.fence, 10),
		Payload: control.UpdateStartPayload{
			ResourceID: activation, NamespaceID: checkpoint.NamespaceID,
			SourceOperationID:             source.operationID,
			SourceStartEffectID:           source.startEffectID,
			SourceStartReceiptSHA256:      source.sourceReceiptSHA256,
			SourceDataEffectID:            source.dataEffectID,
			SourceDataReceiptSHA256:       source.sourceDataReceiptSHA256,
			SourceCheckpointOperationID:   checkpoint.OperationID,
			SourceCheckpointEffectID:      checkpoint.EffectID,
			SourceCheckpointReceiptSHA256: platformDigest(checkpointRaw),
			CheckpointID:                  checkpoint.CheckpointID,
			CheckpointManifestSHA256:      checkpoint.ManifestSHA256,
			CheckpointArchiveBytes:        checkpoint.ArchiveBytes,
			SourceReleaseSHA256:           checkpoint.ReleaseSHA256,
			TargetReleaseSHA256:           checkpoint.ReleaseSHA256,
			SourceGeneration: strconv.FormatInt(
				mustPlatformCheckpointInt(checkpoint.TargetGeneration)-1, 10),
			TargetPolicyRevision: strconv.FormatInt(targetPolicy, 10),
			ProjectID:            checkpoint.ProjectID,
			NamespaceInode:       checkpoint.NamespaceInode,
			Device:               checkpoint.Device,
			QuotaBytes:           source.quotaBytes,
			QuotaInodes:          source.quotaInodes,
			RootFilesystemUUID:   source.rootFilesystemUUID,
			RootInode:            source.rootInode,
			DataFormat:           checkpoint.DataFormat,
			SourceWriterFence:    checkpoint.WriterFence,
			TargetWriterFence: strconv.FormatInt(
				mustPlatformCheckpointInt(checkpoint.WriterFence)+1, 10),
			Platform:       source.platform,
			SandboxProfile: source.sandboxProfile,
		},
	}
	digest, err := control.UpdateStartLogicalDigest(command)
	if err != nil {
		return control.UpdateStartCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

func (s *Server) preparePlatformRestoreStart(ctx context.Context, lease platformOperationLease) (platformUpdateStartIntent, error) {
	var zero platformUpdateStartIntent
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageStart ||
		lease.snapshot.Generation < 2 {
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
	runtime, seed, err := s.platformDataRuntime(
		e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(
		e, lease, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	recoverySetID, _, checkpoint, _, err :=
		loadPlatformRestoreCheckpoint(e, lease)
	if err != nil || recoverySetID == "" {
		return zero, errAppPlatformStageEvidence
	}
	var checkpointRaw string
	if err := e.queryRow(`SELECT receipt_json
		FROM app_platform_operation_effects
		WHERE operation_id=? AND effect_id=? AND stage='snapshot'
		AND state='applied'`,
		checkpoint.OperationID, checkpoint.EffectID).
		Scan(&checkpointRaw); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var stopState, removeState string
	if err := e.queryRow(`SELECT
		COALESCE(MAX(CASE WHEN effect_number=1 THEN state END),''),
		COALESCE(MAX(CASE WHEN effect_number=2 THEN state END),'')
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce'`,
		lease.id).Scan(&stopState, &removeState); err != nil ||
		stopState != "applied" || removeState != "applied" {
		return zero, errAppPlatformStageEvidence
	}
	var activation, namespace, activationState string
	var format int64
	if err := e.queryRow(`SELECT id,data_namespace_id,data_format,state
		FROM app_platform_activations WHERE installation_id=? AND generation=?`,
		lease.installationID, lease.snapshot.Generation).
		Scan(&activation, &namespace, &format, &activationState); err != nil ||
		activationState != "candidate" || namespace != checkpoint.NamespaceID ||
		format != mustPlatformCheckpointInt(checkpoint.DataFormat) {
		return zero, errAppPlatformStageEvidence
	}
	var installationPolicy int64
	if err := e.queryRow(`SELECT policy_revision
		FROM app_platform_installations
		WHERE id=? AND desired_state='enabled'
		AND phase='recovery_required'`,
		lease.installationID).Scan(&installationPolicy); err != nil ||
		installationPolicy < 1 {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_activations
		SET release_sha256=?,policy_revision=?,state='candidate',
		readiness_evidence='{}',observed_ms=NULL,updated_ms=?
		WHERE id=? AND installation_id=? AND generation=?
		AND data_namespace_id=? AND data_format=? AND state='candidate'`,
		checkpoint.ReleaseSHA256, installationPolicy, facts.now,
		activation, lease.installationID, lease.snapshot.Generation,
		checkpoint.NamespaceID, checkpoint.DataFormat)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	var effectID, kind, digest, epoch, state, receipt string
	var admitted, observedFence, created, updated int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,
		controller_epoch,admitted_fence,observed_fence,state,receipt_json,
		created_ms,updated_ms FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='start' AND effect_number=1`,
		lease.id).
		Scan(&effectID, &kind, &digest, &epoch, &admitted, &observedFence,
			&state, &receipt, &created, &updated)
	freshEffect := errors.Is(effectErr, sql.ErrNoRows)
	if effectErr != nil && !freshEffect {
		return zero, errAppPlatformStageEvidence
	}
	if freshEffect {
		effectID, err = platformNewUUID()
		if err != nil {
			return zero, err
		}
		created, updated = facts.now, facts.now
	} else if kind != "restore-start" || epoch != lease.controllerEpoch ||
		admitted != lease.fence ||
		(state != "prepared" && state != "applied") ||
		updated < created || updated > facts.now {
		return zero, errAppPlatformStageEvidence
	}
	command, err := platformRestoreStartCommand(
		lease, runtime, source, checkpoint, []byte(checkpointRaw),
		activation, effectID, installationPolicy)
	if err != nil {
		return zero, err
	}
	issue := time.Now().UnixMilli()
	if issue < facts.now-1000 || issue > facts.now+1000 ||
		issue > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	command.IssuedMS = strconv.FormatInt(issue, 10)
	command.ExpiresMS = strconv.FormatInt(issue+25000, 10)
	authority := control.Authority{
		DeploymentID:    runtime.deploymentID,
		ControllerEpoch: runtime.controllerEpoch,
		ControlRevision: runtime.controlRevision,
		PublicKey:       bytes.Clone(runtime.publicKey),
		Fingerprint:     runtime.fingerprint,
	}
	key := ed25519.NewKeyFromSeed(seed)
	raw, signature, err := control.SignUpdateStart(
		command, key, authority, issue)
	clear(key)
	if err != nil {
		return zero, err
	}
	if !freshEffect {
		if digest != command.PayloadSHA256 {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(
			operation_id,stage,effect_number,effect_id,kind,payload_sha256,
			runtime_id,controller_epoch,admitted_fence,observed_fence,state,
			receipt_json,created_ms,updated_ms)
			VALUES(?,'start',1,?,'restore-start',?,?,?,?,0,'prepared','{}',?,?)`,
			lease.id, effectID, command.PayloadSHA256, runtime.id,
			lease.controllerEpoch, lease.fence, facts.now, facts.now); err != nil {
			return zero, err
		}
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformUpdateStartIntent{
		resourceID: activation, effectID: effectID, runtime: runtime,
		authority: authority, command: command, checkpoint: checkpoint,
		checkpointRaw: []byte(checkpointRaw), source: source, plan: platformManagedUpdatePlan{
			Format:              platformManagedUpdateFormat,
			InstallationID:      lease.installationID,
			SourceReleaseSHA256: checkpoint.ReleaseSHA256,
			TargetReleaseSHA256: checkpoint.ReleaseSHA256,
			SourceGeneration: mustPlatformCheckpointInt(
				checkpoint.TargetGeneration) - 1,
			TargetGeneration:         mustPlatformCheckpointInt(checkpoint.TargetGeneration),
			SourcePolicyRevision:     installationPolicy,
			TargetPolicyRevision:     installationPolicy,
			ControllerEpoch:          lease.controllerEpoch,
			RecoverySetID:            recoverySetID,
			CheckpointOperationID:    checkpoint.OperationID,
			CheckpointEffectID:       checkpoint.EffectID,
			CheckpointID:             checkpoint.CheckpointID,
			CheckpointManifestSHA256: checkpoint.ManifestSHA256,
			CheckpointArchiveBytes:   mustPlatformCheckpointInt(checkpoint.ArchiveBytes),
			DataNamespaceID:          checkpoint.NamespaceID,
			DataFormat:               mustPlatformCheckpointInt(checkpoint.DataFormat),
			SourceWriterFence:        mustPlatformCheckpointInt(checkpoint.WriterFence),
		},
		targetDescriptor: source.descriptor, raw: raw, signature: signature,
	}, nil
}
