package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// restore_import (doc 168, step 11): re-home one restored installation onto the replacement runtime
// and import one retained checkpoint by content, under a single-use grant.

const platformRestoreImportFormat = "portal.restore-import-review/1-dev.1"

type platformRestoreImportPlan struct {
	Format                string `json:"format"`
	InstallationID        string `json:"installation_id"`
	RecoverySetID         string `json:"recovery_set_id"`
	CheckpointOperationID string `json:"checkpoint_operation_id"`
	CheckpointEffectID    string `json:"checkpoint_effect_id"`
	CheckpointID          string `json:"checkpoint_id"`
	NamespaceID           string `json:"namespace_id"`
	ReleaseSHA256         string `json:"release_sha256"`
	ManifestSHA256        string `json:"manifest_sha256"`
	ArchiveBytes          int64  `json:"archive_bytes"`
	DataFormat            int64  `json:"data_format"`
	WriterFence           int64  `json:"writer_fence"`
	SourceRuntimeID       string `json:"source_runtime_id"`
	RuntimeID             string `json:"runtime_id"`
	DeploymentID          string `json:"deployment_id"`
	SourceGeneration      int64  `json:"source_generation"`
	TargetGeneration      int64  `json:"target_generation"`
	ControllerEpoch       string `json:"controller_epoch"`
}

// platformImportSource is a retained recovery set together with the checkpoint receipt that created
// it and the old host's recorded provenance. Everything an import must prove is read from here.
type platformImportSource struct {
	recoverySetID, installationID, namespaceID, operationID, release, checkpointID, manifest, epoch, state string
	dataFormat, archiveBytes, writerFence, quotaBytes                                                      int64
	checkpoint                                                                                             journal.CheckpointReceipt
	sourceFilesystemUUID, sourceRootInode                                                                  string
}

func loadPlatformImportSource(e migExec, s *Server, installation, recoverySetID string) (platformImportSource, error) {
	var r platformImportSource
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var manifest sql.NullString
	if err := e.queryRow(`SELECT id,installation_id,data_namespace_id,operation_id,release_sha256,data_format,checkpoint_id,manifest_sha256,bytes,state,controller_epoch,writer_fence
		FROM app_platform_recovery_sets WHERE id=? AND installation_id=?`+lock, recoverySetID, installation).Scan(
		&r.recoverySetID, &r.installationID, &r.namespaceID, &r.operationID, &r.release, &r.dataFormat, &r.checkpointID,
		&manifest, &r.archiveBytes, &r.state, &r.epoch, &r.writerFence); err != nil {
		return r, errAppPlatformStageEvidence
	}
	if !manifest.Valid || !platformValidSHA(manifest.String) || !platformValidSHA(r.release) || !platformValidUUID(r.operationID) ||
		!platformValidUUID(r.checkpointID) || !platformValidUUID(r.namespaceID) || r.archiveBytes <= 0 || r.dataFormat < 1 || r.writerFence < 1 {
		return r, errAppPlatformStageEvidence
	}
	r.manifest = manifest.String
	var effectID, payload, raw string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`, r.operationID).Scan(&effectID, &payload, &raw); err != nil {
		return r, errAppPlatformStageEvidence
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(raw))
	if err != nil || receipt.EffectID != effectID || receipt.PayloadSHA256 != payload || receipt.OperationID != r.operationID ||
		receipt.InstallationID != installation || receipt.CheckpointID != r.checkpointID || receipt.ManifestSHA256 != r.manifest ||
		receipt.ReleaseSHA256 != r.release || receipt.NamespaceID != r.namespaceID ||
		mustPlatformCheckpointInt(receipt.ArchiveBytes) != r.archiveBytes || mustPlatformCheckpointInt(receipt.DataFormat) != r.dataFormat ||
		mustPlatformCheckpointInt(receipt.WriterFence) != r.writerFence ||
		receipt.ProjectID == "" || receipt.NamespaceInode == "" || receipt.Device == "" || !platformValidUUID(receipt.SourceDataEffectID) {
		return r, errAppPlatformStageEvidence
	}
	r.checkpoint = receipt
	// The old host's data root is recorded in the data-preparation receipt the checkpoint was cut from.
	var dataRaw string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE effect_id=? AND state='applied'`, receipt.SourceDataEffectID).Scan(&dataRaw); err != nil ||
		platformDigest([]byte(dataRaw)) != receipt.SourceDataReceiptSHA256 {
		return r, errAppPlatformStageEvidence
	}
	data, err := journal.ParseDataPreparationReceipt([]byte(dataRaw))
	if err != nil || data.Claim.RootFilesystemUUID == "" || data.Claim.RootInode == "" || data.ProjectID != receipt.ProjectID ||
		data.NamespaceInode != receipt.NamespaceInode || data.Device != receipt.Device {
		return r, errAppPlatformStageEvidence
	}
	r.sourceFilesystemUUID, r.sourceRootInode = data.Claim.RootFilesystemUUID, data.Claim.RootInode
	if err := e.queryRow(`SELECT quota_bytes FROM app_platform_data_namespaces WHERE id=? AND origin_installation_id=?`, r.namespaceID, installation).Scan(&r.quotaBytes); err != nil ||
		r.quotaBytes <= 0 || r.quotaBytes%512 != 0 {
		return r, errAppPlatformStageEvidence
	}
	return r, nil
}

type platformRestoreImportCandidate struct {
	plan                                                platformRestoreImportPlan
	appID, publisherID                                  string
	installationRevision, policyRevision, trustRevision int64
}

func loadPlatformRestoreImportCandidate(e migExec, s *Server, installation, recoverySetID string, runtime platformRuntimeRegistration) (platformRestoreImportCandidate, error) {
	var result platformRestoreImportCandidate
	if !platformValidUUID(installation) || !platformValidUUID(recoverySetID) {
		return result, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var desired, phase, epoch string
	var generation int64
	var old sql.NullString
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock, installation).Scan(&result.appID, &result.publisherID, &desired, &phase,
		&generation, &result.installationRevision, &result.policyRevision, &epoch, &old); err != nil {
		return result, errAppPlatformStageEvidence
	}
	if desired == "removed" || phase != "review_required" || epoch != runtime.controllerEpoch || !old.Valid {
		return result, errAppPlatformStageEvidence
	}
	// A retry after a failed import finds the installation already on the replacement runtime. That is
	// admitted, and only because no grant on the recovery set was consumed (checked below): a consumed
	// grant means the checkpoint was imported, and importing it again would be a second copy.
	if old.String != runtime.id {
		// The binding being left must be retired: re-homing never takes over a live runtime's installation.
		var enabled int
		var sealed, pin sql.NullString
		if err := e.queryRow(`SELECT enabled,sealed_control_key,verification_key_fingerprint FROM app_platform_runtime_bindings WHERE id=?`, old.String).Scan(&enabled, &sealed, &pin); err != nil ||
			enabled != 0 || sealed.Valid || pin.Valid {
			return result, errAppPlatformStageEvidence
		}
	}
	source, err := loadPlatformImportSource(e, s, installation, recoverySetID)
	if err != nil {
		return result, err
	}
	var namespaceState string
	if err := e.queryRow(`SELECT state FROM app_platform_data_namespaces WHERE id=?`, source.namespaceID).Scan(&namespaceState); err != nil ||
		namespaceState != "review_required" || source.state != "review_required" || source.epoch != runtime.controllerEpoch {
		return result, errAppPlatformStageEvidence
	}
	var pending, grants int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&pending); err != nil {
		return result, err
	}
	if pending != 0 {
		return result, errAppPlatformOperationConflict
	}
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_import_grants WHERE recovery_set_id=? AND state<>'revoked'`, recoverySetID).Scan(&grants); err != nil {
		return result, err
	}
	if grants != 0 {
		return result, errAppPlatformOperationConflict
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, result.publisherID).Scan(&result.trustRevision); err != nil || result.trustRevision < 1 {
		return result, errAppPlatformStageEvidence
	}
	result.plan = platformRestoreImportPlan{
		Format: platformRestoreImportFormat, InstallationID: installation, RecoverySetID: recoverySetID,
		CheckpointOperationID: source.operationID, CheckpointEffectID: source.checkpoint.EffectID, CheckpointID: source.checkpointID,
		NamespaceID: source.namespaceID, ReleaseSHA256: source.release, ManifestSHA256: source.manifest, ArchiveBytes: source.archiveBytes,
		DataFormat: source.dataFormat, WriterFence: source.writerFence, SourceRuntimeID: old.String, RuntimeID: runtime.id,
		DeploymentID: runtime.deploymentID, SourceGeneration: generation, TargetGeneration: generation + 1, ControllerEpoch: runtime.controllerEpoch,
	}
	return result, nil
}

func (s *Server) createPlatformRestoreImportReview(ctx context.Context, session platformActorSession, installation, recoverySetID string) (platformRemovalReview, error) {
	if !platformValidUUID(installation) || !platformValidUUID(recoverySetID) {
		return platformRemovalReview{}, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return platformRemovalReview{}, err
	}
	var result platformRemovalReview
	err = s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		runtime, seed, err := s.platformDataRuntime(e, control.epoch, dek)
		if err != nil {
			return err
		}
		defer clear(seed)
		candidate, err := loadPlatformRestoreImportCandidate(e, s, installation, recoverySetID, runtime)
		if err != nil {
			if errors.Is(err, errAppPlatformOperationConflict) {
				return err
			}
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(candidate.plan)
		if err != nil || len(raw) > 65536 || control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		digest, expires := platformDigest(raw), control.nowMS+900000
		if _, err := e.exec(`INSERT INTO app_platform_review_plans(id,installation_id,app_id,publisher_id,action,source_release_sha256,target_release_sha256,
			installation_revision,policy_revision,trust_revision,controller_epoch,actor_scope,username,account_created,account_generation,authority_fingerprint,
			plan_sha256,plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'restore_import',?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, candidate.appID, candidate.publisherID, candidate.plan.ReleaseSHA256, candidate.plan.ReleaseSHA256,
			candidate.installationRevision, candidate.policyRevision, candidate.trustRevision, control.epoch, actor.scope, actor.username,
			actor.created, actor.generation, actor.authorityFingerprint, digest, string(raw), expires, control.nowMS, control.nowMS); err != nil {
			return err
		}
		result = platformRemovalReview{ID: id, SHA256: digest, JSON: string(raw), ExpiresMS: expires}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

// consumePlatformRestoreImportReview is the one transaction that re-homes the installation, issues the
// grant and queues the operation. Either all three happen or none does.
func (s *Server) consumePlatformRestoreImportReview(ctx context.Context, session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidSHA(planSHA) || !platformValidUUID(key) {
		return result, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	request, _ := json.Marshal([]string{"portal-operation-commit/v1", planID, planSHA})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err = s.withPlatformRecoveryTransactionContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl) error {
		previous, exists, err := platformFindRemovalCommit(e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			var action string
			if err := e.queryRow(`SELECT action FROM app_platform_operations WHERE id=?`, previous.OperationID).Scan(&action); err != nil || action != "restore_import" {
				return errAppPlatformOperationConflict
			}
			result = previous
			return nil
		}
		lock := ""
		if e.s.driver == "postgres" {
			lock = " FOR UPDATE"
		}
		var raw, digest, scope, username, created, authority, action, epoch, appID, publisher string
		var installation sql.NullString
		var expires, revision, policyRevision, trustRevision, generation int64
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT plan_json,plan_sha256,actor_scope,username,account_created,account_generation,authority_fingerprint,expires_ms,consumed_ms,
			installation_revision,policy_revision,trust_revision,controller_epoch,action,installation_id,app_id,publisher_id
			FROM app_platform_review_plans WHERE id=?`+lock, planID).Scan(&raw, &digest, &scope, &username, &created, &generation, &authority, &expires, &consumed,
			&revision, &policyRevision, &trustRevision, &epoch, &action, &installation, &appID, &publisher); err != nil {
			return errAppPlatformPlanChanged
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS || action != "restore_import" || !installation.Valid || scope != actor.scope || username != actor.username ||
			created != actor.created || generation != actor.generation || authority != actor.authorityFingerprint || epoch != control.epoch ||
			digest != planSHA || platformDigest([]byte(raw)) != planSHA {
			return errAppPlatformPlanChanged
		}
		runtime, seed, err := s.platformDataRuntime(e, epoch, dek)
		if err != nil {
			return err
		}
		defer clear(seed)
		var plan platformRestoreImportPlan
		if json.Unmarshal([]byte(raw), &plan) != nil || plan.Format != platformRestoreImportFormat || plan.InstallationID != installation.String {
			return errAppPlatformPlanChanged
		}
		if canonical, marshalErr := json.Marshal(plan); marshalErr != nil || string(canonical) != raw {
			return errAppPlatformPlanChanged
		}
		candidate, err := loadPlatformRestoreImportCandidate(e, s, installation.String, plan.RecoverySetID, runtime)
		if err != nil {
			return errAppPlatformPlanChanged
		}
		expected, err := json.Marshal(candidate.plan)
		if err != nil || string(expected) != raw || candidate.appID != appID || candidate.publisherID != publisher ||
			candidate.installationRevision != revision || candidate.policyRevision != policyRevision || candidate.trustRevision != trustRevision {
			return errAppPlatformPlanChanged
		}
		now := control.nowMS
		changed, err := e.exec(`UPDATE app_platform_installations SET runtime_id=?,revision=revision+1,updated_ms=?
			WHERE id=? AND runtime_id=? AND revision=? AND phase='review_required' AND controller_epoch=?`,
			runtime.id, now, installation.String, plan.SourceRuntimeID, revision, epoch)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		grantID, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_import_grants(id,recovery_set_id,runtime_id,controller_epoch,state,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,'issued',NULL,?,?)`, grantID, plan.RecoverySetID, runtime.id, epoch, now, now); err != nil {
			return err
		}
		operationID, err := platformNewUUID()
		if err != nil {
			return err
		}
		if _, err := e.exec(`INSERT INTO app_platform_operations(id,installation_id,review_plan_id,actor_scope,idempotency_sha256,request_sha256,action,managed,
			source_generation,target_generation,stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,'restore_import',1,?,?,'queued',1,1,NULL,NULL,0,?,NULL,NULL,?,?)`,
			operationID, installation.String, planID, actor.scope, keyDigest, requestDigest, plan.SourceGeneration, plan.TargetGeneration, epoch, now, now); err != nil {
			return err
		}
		changed, err = e.exec(`UPDATE app_platform_review_plans SET consumed_ms=?,updated_ms=? WHERE id=? AND consumed_ms IS NULL AND expires_ms>? AND plan_sha256=?`,
			now, now, planID, now, planSHA)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan, Action string
			RecoverySet  string `json:"recovery_set_id"`
			GrantID      string `json:"grant_id"`
			RuntimeID    string `json:"runtime_id"`
			SourceID     string `json:"source_runtime_id"`
			Generation   int64
		}{planID, "restore_import", plan.RecoverySetID, grantID, runtime.id, plan.SourceRuntimeID, plan.TargetGeneration})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,?,?,'app_platform.operation_admitted','app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339), actor.username, actor.ouID, operationID, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{OperationID: operationID, InstallationID: installation.String, Generation: plan.TargetGeneration, Revision: 1, Stage: appplatform.StageQueued}
		_, err = platformRefreshActor(e, session, actor, control)
		return err
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}
