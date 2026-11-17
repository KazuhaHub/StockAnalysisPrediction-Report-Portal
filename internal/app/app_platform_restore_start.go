package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
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
	// An imported checkpoint was cut under an older writer fence than the namespace now holds (the
	// Portal's own restore advanced it). The start is fenced above the namespace's, and names the
	// checkpoint's separately so the runner can match it to the import receipt.
	sourceWriter, checkpointWriter := checkpoint.WriterFence, ""
	if source.currentWriterFence != "" {
		sourceWriter, checkpointWriter = source.currentWriterFence, checkpoint.WriterFence
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
			SourceWriterFence:    sourceWriter,
			TargetWriterFence: strconv.FormatInt(
				mustPlatformCheckpointInt(sourceWriter)+1, 10),
			CheckpointWriterFence: checkpointWriter,
			Platform:              source.platform,
			SandboxProfile:        source.sandboxProfile,
		},
	}
	digest, err := control.UpdateStartLogicalDigest(command)
	if err != nil {
		return control.UpdateStartCommand{}, errAppPlatformStageEvidence
	}
	command.PayloadSHA256 = digest
	return command, nil
}

// platformRestoreStartPlan is the update-shaped plan a restore start is verified against. It is
// derived from the checkpoint alone: a restore has no reviewed update plan of its own.
func platformRestoreStartPlan(lease platformOperationLease, checkpoint journal.CheckpointReceipt, recoverySetID string, policy int64, source platformCheckpointSource) platformManagedUpdatePlan {
	sourceWriter, checkpointWriter := checkpoint.WriterFence, ""
	if source.currentWriterFence != "" {
		sourceWriter, checkpointWriter = source.currentWriterFence, checkpoint.WriterFence
	}
	return platformManagedUpdatePlan{
		Format:              platformManagedUpdateFormat,
		InstallationID:      lease.installationID,
		SourceReleaseSHA256: checkpoint.ReleaseSHA256,
		TargetReleaseSHA256: checkpoint.ReleaseSHA256,
		SourceGeneration: mustPlatformCheckpointInt(
			checkpoint.TargetGeneration) - 1,
		TargetGeneration:         mustPlatformCheckpointInt(checkpoint.TargetGeneration),
		SourcePolicyRevision:     policy,
		TargetPolicyRevision:     policy,
		ControllerEpoch:          lease.controllerEpoch,
		RecoverySetID:            recoverySetID,
		CheckpointOperationID:    checkpoint.OperationID,
		CheckpointEffectID:       checkpoint.EffectID,
		CheckpointID:             checkpoint.CheckpointID,
		CheckpointManifestSHA256: checkpoint.ManifestSHA256,
		CheckpointArchiveBytes:   mustPlatformCheckpointInt(checkpoint.ArchiveBytes),
		DataNamespaceID:          checkpoint.NamespaceID,
		DataFormat:               mustPlatformCheckpointInt(checkpoint.DataFormat),
		SourceWriterFence:        mustPlatformCheckpointInt(sourceWriter),
		CheckpointWriterFence:    mustPlatformCheckpointInt(checkpointWriter),
		TargetRuntime:            source.runtimeTarget,
	}
}

// loadPlatformRestoreCheckpointRaw reads the applied receipt a restore start names as its checkpoint:
// a snapshot for an ordinary restore, the applied import for a resume.
func loadPlatformRestoreCheckpointRaw(e migExec, lease platformOperationLease, checkpoint journal.CheckpointReceipt) ([]byte, error) {
	resume, err := platformOperationIsResume(e, lease.id)
	if err != nil {
		return nil, err
	}
	stage := "snapshot"
	if resume {
		stage = "restore_data"
	}
	var raw string
	if err := e.queryRow(`SELECT receipt_json
		FROM app_platform_operation_effects
		WHERE operation_id=? AND effect_id=? AND stage=?
		AND state='applied'`,
		checkpoint.OperationID, checkpoint.EffectID, stage).Scan(&raw); err != nil {
		return nil, errAppPlatformStageEvidence
	}
	return []byte(raw), nil
}

func (s *Server) preparePlatformRestoreStart(ctx context.Context, lease platformOperationLease) (platformUpdateStartIntent, error) {
	return s.preparePlatformRestoreStartMode(
		ctx, lease, appplatform.StageStart)
}

func (s *Server) preparePlatformRestoreProbe(ctx context.Context, lease platformOperationLease) (platformUpdateStartIntent, error) {
	return s.preparePlatformRestoreStartMode(
		ctx, lease, appplatform.StageProbe)
}

func (s *Server) preparePlatformRestoreStartMode(ctx context.Context, lease platformOperationLease, stage appplatform.Stage) (platformUpdateStartIntent, error) {
	var zero platformUpdateStartIntent
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != stage ||
		(stage != appplatform.StageStart && stage != appplatform.StageProbe &&
			stage != appplatform.StageActivate) ||
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
	resume, err := platformOperationIsResume(e, lease.id)
	if err != nil {
		return zero, err
	}
	checkpointRaw, err := loadPlatformRestoreCheckpointRaw(e, lease, checkpoint)
	if err != nil {
		return zero, err
	}
	// A restore first stops and removes the failed update's target. A first resume has none: the import
	// started nothing on the new host. A resume that follows an applied start stops and removes that
	// earlier container first.
	containerRequired := false
	if resume {
		_, containerRequired, err = platformResumeContainerSource(e, lease, runtime.id)
		if err != nil {
			return zero, err
		}
	}
	settled, err := platformRestoreSettledByMigration(e, lease)
	if err != nil {
		return zero, err
	}
	var stopState, removeState string
	if err := e.queryRow(`SELECT
		COALESCE(MAX(CASE WHEN effect_number=1 THEN state END),''),
		COALESCE(MAX(CASE WHEN effect_number=2 THEN state END),'')
		FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce'`,
		lease.id).Scan(&stopState, &removeState); err != nil ||
		(settled && (stopState != "" || removeState != "")) ||
		(!resume && !settled && (stopState != "applied" || removeState != "applied")) ||
		(resume && containerRequired && (stopState != "applied" || removeState != "applied")) ||
		(resume && !containerRequired && (stopState != "" || removeState != "")) {
		return zero, errAppPlatformStageEvidence
	}
	if settled && stage == appplatform.StageStart {
		// The failed update never started a target, so there is no candidate activation to reuse: the
		// roll back opens the one its start will run under.
		var existing int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND generation=?`,
			lease.installationID, lease.snapshot.Generation).Scan(&existing); err != nil {
			return zero, err
		}
		if existing == 0 {
			id, err := platformNewUUID()
			if err != nil {
				return zero, err
			}
			if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms)
				SELECT ?,?,?,?,i.policy_revision,?,?,'candidate','{}',NULL,?,? FROM app_platform_installations i WHERE i.id=?`,
				id, lease.installationID, checkpoint.ReleaseSHA256, lease.snapshot.Generation, checkpoint.NamespaceID,
				mustPlatformCheckpointInt(checkpoint.DataFormat), facts.now, facts.now, lease.installationID); err != nil {
				return zero, err
			}
		}
	}
	var activation, namespace, activationState, readinessRaw string
	var format int64
	var activationObserved sql.NullInt64
	if err := e.queryRow(`SELECT id,data_namespace_id,data_format,state,
		readiness_evidence,observed_ms
		FROM app_platform_activations WHERE installation_id=? AND generation=?`,
		lease.installationID, lease.snapshot.Generation).
		Scan(&activation, &namespace, &format, &activationState,
			&readinessRaw, &activationObserved); err != nil ||
		activationState != "candidate" || namespace != checkpoint.NamespaceID ||
		format != mustPlatformCheckpointInt(checkpoint.DataFormat) {
		return zero, errAppPlatformStageEvidence
	}
	if stage == appplatform.StageProbe &&
		(readinessRaw != "{}" || activationObserved.Valid) {
		return zero, errAppPlatformStageEvidence
	}
	if stage == appplatform.StageActivate &&
		(readinessRaw == "{}" || !activationObserved.Valid) {
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
	if stage == appplatform.StageStart {
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
		if stage != appplatform.StageStart {
			return zero, errAppPlatformStageEvidence
		}
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
		lease, runtime, source, checkpoint, checkpointRaw,
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
	result := platformUpdateStartIntent{
		resourceID: activation, effectID: effectID, runtime: runtime,
		authority: authority, command: command, checkpoint: checkpoint,
		checkpointRaw: checkpointRaw, source: source,
		plan: platformRestoreStartPlan(
			lease, checkpoint, recoverySetID, installationPolicy, source),
		targetDescriptor: source.descriptor, raw: raw, signature: signature,
	}
	if !freshEffect {
		switch state {
		case "prepared":
			if observedFence == 0 {
				if receipt != "{}" {
					return zero, errAppPlatformStageEvidence
				}
			} else {
				if observedFence != lease.fence {
					return zero, errAppPlatformStageEvidence
				}
				reservation, err := journal.ParseUpdateStartReservation(
					[]byte(receipt))
				if err != nil || !platformUpdateStartReservationMatches(
					reservation, command, activation, effectID) {
					return zero, errAppPlatformStageEvidence
				}
				result.reservation = &reservation
			}
		case "applied":
			if observedFence != lease.fence {
				return zero, errAppPlatformStageEvidence
			}
			running, err := journal.ParseUpdateStartRunningReceipt(
				[]byte(receipt))
			if err != nil {
				return zero, errAppPlatformStageEvidence
			}
			reservation := journal.UpdateStartReservation{
				ResourceID:      running.Claim.ResourceID,
				InstallationID:  running.InstallationID,
				NamespaceID:     running.NamespaceID,
				EffectID:        running.Claim.EffectID,
				OwnershipSHA256: running.Claim.OwnershipSHA256,
				SourceGeneration: mustPlatformCheckpointInt(
					running.SourceGeneration),
				TargetGeneration: mustPlatformCheckpointInt(
					running.TargetGeneration),
				SourceReleaseSHA256: running.SourceReleaseSHA256,
				TargetReleaseSHA256: running.TargetReleaseSHA256,
				ControlFence: mustPlatformCheckpointInt(
					running.ControlFence),
				SourceWriterFence: mustPlatformCheckpointInt(
					running.SourceWriterFence),
				TargetWriterFence: mustPlatformCheckpointInt(
					running.TargetWriterFence),
			}
			if !platformUpdateStartRunningMatches(
				running, command, activation, effectID, &reservation) {
				return zero, errAppPlatformStageEvidence
			}
			result.reservation = &reservation
			result.running = &running
			if readinessRaw != "{}" && activationObserved.Valid {
				readiness, err := journal.ParseUpdateStartReadiness(
					[]byte(readinessRaw))
				observed := mustPlatformCheckpointInt(readiness.ObservedMS)
				if err != nil || observed != activationObserved.Int64 ||
					readiness.PayloadSHA256 != command.PayloadSHA256 ||
					readiness.RunningReceiptSHA256 !=
						platformDigest([]byte(receipt)) ||
					readiness.NativeID != running.NativeID ||
					readiness.DataFormat != command.Payload.DataFormat {
					return zero, errAppPlatformStageEvidence
				}
				result.readiness = &readiness
				result.activationObserved = observed
			}
		default:
			return zero, errAppPlatformStageEvidence
		}
	}
	return result, nil
}

func (s *Server) preparePlatformRestoreStartRegistration(ctx context.Context, lease platformOperationLease) (platformUpdateStartRegistrationIntent, error) {
	var zero platformUpdateStartRegistrationIntent
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageActivate ||
		lease.snapshot.Generation < 2 {
		return zero, errAppPlatformStageEvidence
	}
	start, err := s.preparePlatformRestoreStartMode(
		ctx, lease, appplatform.StageActivate)
	if err != nil || start.running == nil || start.readiness == nil {
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
	intent, fresh, err := loadPlatformUpdateStartRegistration(
		e, lease, start, runtime, seed, dek, facts.now, true)
	if err != nil {
		return zero, err
	}
	failed := true
	defer func() {
		if failed {
			intent.registration.Close()
		}
	}()
	if fresh {
		detail, _ := json.Marshal(struct {
			EffectID    string `json:"effect_id"`
			ResourceID  string `json:"resource_id"`
			DocumentSHA string `json:"document_sha256"`
		}{intent.command.EffectID, intent.start.resourceID,
			intent.command.DocumentSHA256})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.restore_start_registration_intent',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
			lease.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	after, _, err := loadPlatformUpdateStartRegistration(
		e, lease, start, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer after.registration.Close()
	if !samePlatformUpdateStartRegistrationIntent(intent, after) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyUpdateStartRegistration(
		intent.raw, intent.signature, start.authority,
		time.Now().UnixMilli()); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	failed = false
	return intent, nil
}
