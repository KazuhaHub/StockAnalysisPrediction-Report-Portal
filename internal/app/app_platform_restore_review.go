package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformManagedRestoreReviewCandidate struct {
	plan           platformManagedRestorePlan
	appID          string
	publisherID    string
	runtimeID      string
	trustRevision  int64
	updateRevision int64
	policyRevision int64
}

func loadPlatformManagedRestoreReviewCandidate(
	e migExec,
	installation, recoverySetID string,
	runtime platformRuntimeRegistration,
	now int64,
) (platformManagedRestoreReviewCandidate, error) {
	var result platformManagedRestoreReviewCandidate
	if !platformValidUUID(installation) ||
		!platformValidUUID(recoverySetID) || now <= 0 {
		return result, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var recoveryInstallation, updateOperation, checkpointID string
	var manifest, recoveryRelease, recoveryEpoch string
	var recoveryFormat, writerFence, archiveBytes int64
	var recoveryState string
	if err := e.queryRow(`SELECT installation_id,operation_id,
		checkpoint_id,manifest_sha256,bytes,release_sha256,data_format,
		writer_fence,state,controller_epoch
		FROM app_platform_recovery_sets
		WHERE id=? AND installation_id=?`+lock,
		recoverySetID, installation).Scan(
		&recoveryInstallation, &updateOperation, &checkpointID,
		&manifest, &archiveBytes, &recoveryRelease, &recoveryFormat,
		&writerFence, &recoveryState, &recoveryEpoch); err != nil ||
		recoveryInstallation != installation || recoveryState != "verified" ||
		!platformValidUUID(updateOperation) ||
		!platformValidUUID(checkpointID) ||
		!platformValidSHA(manifest) ||
		!platformValidSHA(recoveryRelease) ||
		archiveBytes <= 0 || recoveryFormat < 1 || writerFence < 1 ||
		recoveryEpoch != runtime.controllerEpoch {
		return result, errAppPlatformStageEvidence
	}
	var updateAction, updateStage, updateError, installationEpoch,
		appID, publisher, desired, phase, runtimeID string
	var updateManaged, sourceGeneration, targetGeneration,
		updateFence int64
	var completed sql.NullInt64
	if err := e.queryRow(`SELECT action,managed,source_generation,
		target_generation,stage,fence,completed_ms,error_code,
		installation_id,controller_epoch
		FROM app_platform_operations WHERE id=?`,
		updateOperation).Scan(
		&updateAction, &updateManaged, &sourceGeneration,
		&targetGeneration, &updateStage, &updateFence, &completed,
		&updateError, &result.plan.InstallationID,
		&installationEpoch); err != nil ||
		updateAction != "update" || updateManaged != 1 ||
		sourceGeneration < 1 ||
		targetGeneration != sourceGeneration+1 ||
		updateStage != "recovery_required" ||
		updateFence < 1 || !completed.Valid ||
		updateError != "update_recovery_required" ||
		result.plan.InstallationID != installation ||
		installationEpoch != runtime.controllerEpoch {
		return result, errAppPlatformStageEvidence
	}
	// A migration that has not been settled may have left migrated data in the namespace.
	if err := platformRequireSettledMigration(e, updateOperation); err != nil {
		return result, err
	}
	var sourceOperationID string
	if err := e.queryRow(`SELECT id FROM app_platform_operations
		WHERE installation_id=? AND action IN ('install','update','restore')
		AND managed=1 AND stage='complete' AND completed_ms IS NOT NULL
		AND lease_owner IS NULL AND lease_until_ms IS NULL
		AND error_code IS NULL
		ORDER BY target_generation DESC,completed_ms DESC,id DESC
		LIMIT 1`, installation).Scan(&sourceOperationID); err != nil {
		return result, errAppPlatformStageEvidence
	}
	target, err := platformManagedTargetForOperation(e, sourceOperationID)
	if err != nil || target.RuntimeID != runtime.id ||
		target.ControllerEpoch != runtime.controllerEpoch ||
		validateManagedInstallTarget(
			e, target, runtime.controllerEpoch) != nil {
		return result, errAppPlatformStageEvidence
	}
	var activeActivation, activeRelease, activeNamespace string
	var activationGeneration, activationPolicy, activationFormat int64
	if err := e.queryRow(`SELECT id,release_sha256,generation,
		policy_revision,data_namespace_id,data_format
		FROM app_platform_activations
		WHERE installation_id=? AND state='active'`,
		installation).Scan(
		&activeActivation, &activeRelease, &activationGeneration,
		&activationPolicy, &activeNamespace, &activationFormat); err != nil ||
		activeActivation == "" || activeRelease != recoveryRelease ||
		activationGeneration != sourceGeneration ||
		activationPolicy < 1 || activeNamespace == "" ||
		activationFormat != recoveryFormat {
		return result, errAppPlatformStageEvidence
	}
	var namespaceState, namespaceOrigin, namespaceOwner string
	var namespaceFormat, namespaceWriter int64
	if err := e.queryRow(`SELECT origin_installation_id,
		owner_installation_id,state,data_format,writer_fence
		FROM app_platform_data_namespaces WHERE id=?`,
		activeNamespace).Scan(
		&namespaceOrigin, &namespaceOwner, &namespaceState,
		&namespaceFormat, &namespaceWriter); err != nil ||
		namespaceOrigin != installation || namespaceOwner != installation ||
		namespaceState != "active" || namespaceFormat != recoveryFormat ||
		namespaceWriter != writerFence {
		return result, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,
		phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock,
		installation).Scan(
		&appID, &publisher, &desired, &phase,
		&result.plan.SourceGeneration, &result.updateRevision,
		&result.policyRevision, &result.plan.ControllerEpoch,
		&runtimeID); err != nil ||
		desired != "enabled" || phase != "recovery_required" ||
		result.plan.SourceGeneration != sourceGeneration ||
		result.plan.ControllerEpoch != runtime.controllerEpoch ||
		runtimeID != runtime.id || result.updateRevision < 1 ||
		result.policyRevision != activationPolicy {
		return result, errAppPlatformStageEvidence
	}
	var checkpointEffect, checkpointPayload, checkpointRaw,
		checkpointState, checkpointOperation string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,
		operation_id,state FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1`,
		updateOperation).Scan(
		&checkpointEffect, &checkpointPayload, &checkpointRaw,
		&checkpointOperation, &checkpointState); err != nil ||
		checkpointOperation != updateOperation ||
		checkpointState != "applied" ||
		!platformValidUUID(checkpointEffect) ||
		!platformValidSHA(checkpointPayload) {
		return result, errAppPlatformStageEvidence
	}
	checkpoint, err := journal.ParseCheckpointReceipt(
		[]byte(checkpointRaw))
	if err != nil || checkpoint.OperationID != updateOperation ||
		checkpoint.EffectID != checkpointEffect ||
		checkpoint.PayloadSHA256 != checkpointPayload ||
		checkpoint.CheckpointID != checkpointID ||
		checkpoint.ManifestSHA256 != manifest ||
		checkpoint.ReleaseSHA256 != recoveryRelease ||
		checkpoint.ProjectID == "" || checkpoint.NamespaceInode == "" ||
		checkpoint.Device == "" ||
		checkpoint.TargetGeneration !=
			strconv.FormatInt(targetGeneration, 10) ||
		mustPlatformCheckpointInt(checkpoint.ArchiveBytes) != archiveBytes ||
		checkpoint.NamespaceID != activeNamespace ||
		mustPlatformCheckpointInt(checkpoint.DataFormat) != recoveryFormat ||
		mustPlatformCheckpointInt(checkpoint.WriterFence) != writerFence {
		return result, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers
		WHERE id=?`, publisher).Scan(&result.trustRevision); err != nil ||
		result.trustRevision < 1 {
		return result, errAppPlatformStageEvidence
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return result, err
	}
	verified, err := platformVerifyStoredRelease(
		e, recoveryRelease, result.trustRevision, trust, now)
	if err != nil || verified.appID != appID ||
		verified.publisherID != publisher {
		return result, errAppPlatformStageEvidence
	}
	result.appID, result.publisherID = appID, publisher
	result.runtimeID = runtimeID
	result.plan = platformManagedRestorePlan{
		Format:                platformManagedRestoreFormat,
		InstallationID:        installation,
		RecoverySetID:         recoverySetID,
		CheckpointOperationID: updateOperation,
		CheckpointEffectID:    checkpointEffect,
		CheckpointID:          checkpointID,
		ReleaseSHA256:         recoveryRelease,
		ManifestSHA256:        manifest,
		ArchiveBytes:          archiveBytes,
		SourceGeneration:      sourceGeneration,
		TargetGeneration:      targetGeneration,
		ControllerEpoch:       runtime.controllerEpoch,
	}
	return result, nil
}

func (s *Server) createPlatformManagedRestoreReview(
	ctx context.Context,
	session platformActorSession,
	installation, recoverySetID string,
) (platformRemovalReview, error) {
	if !platformValidUUID(installation) ||
		!platformValidUUID(recoverySetID) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err = s.withPlatformTrustActorContext(
		ctx, session,
		func(
			e migExec,
			actor platformActorFacts,
			control platformActorControl,
			trust appplatform.PublisherTrust,
		) error {
			var paused string
			if err := e.queryRow(`SELECT v FROM meta
				WHERE k='app_platform_restore_paused'`).
				Scan(&paused); err != nil || paused != "0" {
				return errAppPlatformControlPaused
			}
			var epoch string
			if err := e.queryRow(`SELECT controller_epoch
				FROM app_platform_installations WHERE id=?`,
				installation).Scan(&epoch); err != nil ||
				epoch != control.epoch {
				return errAppPlatformPlanChanged
			}
			runtime, seed, err := s.platformDataRuntime(
				e, epoch, dek)
			if err != nil {
				return err
			}
			defer clear(seed)
			candidate, err := loadPlatformManagedRestoreReviewCandidate(
				e, installation, recoverySetID, runtime, control.nowMS)
			if err != nil || candidate.runtimeID != runtime.id {
				return errAppPlatformPlanChanged
			}
			if _, err := platformVerifyStoredRelease(
				e, candidate.plan.ReleaseSHA256,
				candidate.trustRevision, trust, control.nowMS); err != nil {
				return errAppPlatformPlanChanged
			}
			control, err = platformRefreshActor(
				e, session, actor, control)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(candidate.plan)
			if err != nil || len(raw) > 65536 ||
				control.nowMS > math.MaxInt64-900000 {
				return errAppPlatformPlanChanged
			}
			id, err := platformNewUUID()
			if err != nil {
				return err
			}
			digest := platformDigest(raw)
			expires := control.nowMS + 900000
			if _, err := e.exec(`INSERT INTO app_platform_review_plans(
				id,installation_id,app_id,publisher_id,action,
				source_release_sha256,target_release_sha256,
				installation_revision,policy_revision,trust_revision,
				controller_epoch,actor_scope,username,account_created,
				account_generation,authority_fingerprint,plan_sha256,
				plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
				VALUES(?,?,?,?,'restore',?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
				id, installation, candidate.appID,
				candidate.publisherID, candidate.plan.ReleaseSHA256,
				candidate.plan.ReleaseSHA256,
				candidate.updateRevision, candidate.policyRevision,
				candidate.trustRevision, control.epoch, actor.scope,
				actor.username, actor.created, actor.generation,
				actor.authorityFingerprint, digest, string(raw),
				expires, control.nowMS, control.nowMS); err != nil {
				return err
			}
			result = platformRemovalReview{
				ID: id, SHA256: digest, JSON: string(raw),
				ExpiresMS: expires,
			}
			_, err = platformRefreshActor(e, session, actor, control)
			return err
		})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

func (s *Server) consumePlatformManagedRestoreReview(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) ||
		!platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	request, _ := json.Marshal(
		[]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request),
		platformIdempotencySHA(key)
	err = s.withPlatformTrustActorContext(
		ctx, session,
		func(
			e migExec,
			actor platformActorFacts,
			control platformActorControl,
			trust appplatform.PublisherTrust,
		) error {
			previous, exists, err := platformFindRemovalCommit(
				e, actor.scope, keyDigest, requestDigest)
			if err != nil {
				return err
			}
			if exists {
				var action string
				if err := e.queryRow(`SELECT action
					FROM app_platform_operations WHERE id=?`,
					previous.OperationID).Scan(&action); err != nil ||
					action != "restore" {
					return errAppPlatformOperationConflict
				}
				result = previous
				return nil
			}
			var paused string
			if err := e.queryRow(`SELECT v FROM meta
				WHERE k='app_platform_restore_paused'`).
				Scan(&paused); err != nil || paused != "0" {
				return errAppPlatformControlPaused
			}
			lock := ""
			if e.s.driver == "postgres" {
				lock = " FOR UPDATE"
			}
			var raw, digest, scope, username, created, authority,
				action, epoch, appID, publisher string
			var installation, source, release sql.NullString
			var expires, revision, policyRevision, trustRevision,
				accountGeneration int64
			var consumed sql.NullInt64
			if err := e.queryRow(`SELECT plan_json,plan_sha256,
				actor_scope,username,account_created,account_generation,
				authority_fingerprint,expires_ms,consumed_ms,
				installation_revision,policy_revision,trust_revision,
				controller_epoch,action,installation_id,
				source_release_sha256,target_release_sha256,app_id,publisher_id
				FROM app_platform_review_plans WHERE id=?`+lock,
				planID).Scan(
				&raw, &digest, &scope, &username, &created,
				&accountGeneration, &authority, &expires, &consumed,
				&revision, &policyRevision, &trustRevision, &epoch,
				&action, &installation, &source, &release,
				&appID, &publisher); err != nil {
				return errAppPlatformPlanChanged
			}
			if consumed.Valid {
				return errAppPlatformOperationConflict
			}
			control, err = platformRefreshActor(
				e, session, actor, control)
			if err != nil {
				return err
			}
			if consumed.Valid || expires <= control.nowMS ||
				action != "restore" || !installation.Valid ||
				!source.Valid || !release.Valid ||
				scope != actor.scope || username != actor.username ||
				created != actor.created ||
				accountGeneration != actor.generation ||
				authority != actor.authorityFingerprint ||
				epoch != control.epoch || digest != planSHA ||
				platformDigest([]byte(raw)) != planSHA ||
				source.String != release.String ||
				!platformValidSHA(release.String) {
				return errAppPlatformPlanChanged
			}
			runtime, seed, err := s.platformDataRuntime(
				e, epoch, dek)
			if err != nil {
				return err
			}
			defer clear(seed)
			var plan platformManagedRestorePlan
			if json.Unmarshal([]byte(raw), &plan) != nil ||
				plan.Format != platformManagedRestoreFormat ||
				plan.InstallationID != installation.String ||
				plan.ReleaseSHA256 != release.String {
				return errAppPlatformPlanChanged
			}
			canonical, err := json.Marshal(plan)
			if err != nil || string(canonical) != raw {
				return errAppPlatformPlanChanged
			}
			candidate, err := loadPlatformManagedRestoreReviewCandidate(
				e, installation.String, plan.RecoverySetID,
				runtime, control.nowMS)
			if err != nil || candidate.appID != appID ||
				candidate.publisherID != publisher ||
				candidate.runtimeID != runtime.id ||
				candidate.updateRevision != revision ||
				candidate.policyRevision != policyRevision ||
				candidate.trustRevision != trustRevision {
				return errAppPlatformPlanChanged
			}
			expected, err := json.Marshal(candidate.plan)
			if err != nil || string(expected) != raw {
				return errAppPlatformPlanChanged
			}
			if _, err := platformVerifyStoredRelease(
				e, candidate.plan.ReleaseSHA256, trustRevision,
				trust, control.nowMS); err != nil {
				return errAppPlatformPlanChanged
			}
			var pending int
			if err := e.queryRow(`SELECT COUNT(*)
				FROM app_platform_operations
				WHERE installation_id=? AND completed_ms IS NULL`,
				installation.String).Scan(&pending); err != nil ||
				pending != 0 {
				return errAppPlatformOperationConflict
			}
			operationID, err := platformNewUUID()
			if err != nil {
				return err
			}
			if _, err := e.exec(`INSERT INTO app_platform_operations(
				id,installation_id,review_plan_id,actor_scope,
				idempotency_sha256,request_sha256,action,managed,
				source_generation,target_generation,stage,state_revision,
				row_revision,lease_owner,lease_until_ms,fence,controller_epoch,
				error_code,completed_ms,created_ms,updated_ms)
				VALUES(?,?,?,?,?,?,'restore',1,?,?,'quiesce',4,1,NULL,NULL,0,?,
				NULL,NULL,?,?)`,
				operationID, installation.String, planID, actor.scope,
				keyDigest, requestDigest,
				candidate.plan.SourceGeneration,
				candidate.plan.TargetGeneration,
				control.epoch, control.nowMS, control.nowMS); err != nil {
				return err
			}
			changed, err := e.exec(`UPDATE app_platform_review_plans
				SET consumed_ms=?,updated_ms=? WHERE id=?
				AND consumed_ms IS NULL AND expires_ms>?
				AND plan_sha256=?`,
				control.nowMS, control.nowMS, planID,
				control.nowMS, planSHA)
			if err := platformLeaseCAS(changed, err); err != nil {
				return err
			}
			detail, _ := json.Marshal(struct {
				Plan, Action string
				RecoverySet  string `json:"recovery_set_id"`
				Generation   int64
				Managed      bool
			}{planID, "restore", plan.RecoverySetID,
				candidate.plan.TargetGeneration, true})
			if _, err := e.exec(`INSERT INTO audit_log(
				at,actor,actor_ou,action,target_type,target_id,detail,ip)
				VALUES(?,?,?,'app_platform.operation_admitted',
				'app_platform_operation',?,?,'')`,
				databaseNowRFC3339(control.nowMS),
				actor.username, actor.ouID, operationID,
				string(detail)); err != nil {
				return err
			}
			result = platformRemovalCommit{
				OperationID:    operationID,
				InstallationID: installation.String,
				Generation:     candidate.plan.TargetGeneration,
				Revision:       4, Stage: appplatform.StageQuiesce,
			}
			_, err = platformRefreshActor(e, session, actor, control)
			return err
		})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}
