package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A retry-exhausted managed update is never marked failed while a target may
// have mounted data. The source remains the active generation, the installation
// enters review-only recovery, and a separate reviewed restore is required.
func (s *Server) finishPlatformManagedUpdateForReview(ctx context.Context, expected platformOperationLease) error {
	return s.finishPlatformManagedUpdateWith(ctx, expected, nil, platformUpdateRecoveryCode)
}

// platformUpdateRecoveryCode is the error code of an update that ended recovery_required for a reason the update
// itself does not name.
const platformUpdateRecoveryCode = "update_recovery_required"

// platformUpdateRecoveryCodes are the error codes a managed update that ended recovery_required may carry. The
// restore and retry reviews of a failed update accept any of them: what differs is only the reason shown, because the
// recovery is the same. A raise of the data quota that the runner refused (data_quota_capacity) or failed for
// good (data_quota_unavailable) names itself.
func platformUpdateRecoveryCodes(code string) bool {
	return code == platformUpdateRecoveryCode || code == "data_quota_capacity" || code == "data_quota_unavailable"
}

// finishPlatformManagedUpdateWith ends the operation for review with the given error code. effects, when set,
// settles the effect rows the reason for ending belongs to in the same transaction. A migrate-data effect or a
// quota raise still in flight becomes unknown: it is never sent as a new effect, and a migration is settled by a
// discard, a raise by the convergence step.
func (s *Server) finishPlatformManagedUpdateWith(ctx context.Context, expected platformOperationLease, effects func(e migExec, now int64) error, code string) error {
	if !platformUpdateRecoveryCodes(code) {
		return errAppPlatformStageEvidence
	}
	if expected.snapshot.Action != appplatform.ActionUpdate ||
		!expected.snapshot.Managed || expected.id == "" ||
		expected.snapshot.Stage == appplatform.StageComplete ||
		expected.snapshot.Stage == appplatform.StageFailed ||
		expected.snapshot.Stage == appplatform.StageRecoveryRequired {
		return errAppPlatformStageEvidence
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	dek, err := s.dek()
	if err != nil {
		return err
	}
	runtime, seed, err := s.platformDataRuntime(
		e, expected.controllerEpoch, dek)
	if err != nil {
		return err
	}
	defer clear(seed)
	source, err := loadPlatformCheckpointSource(
		e, expected, runtime, facts.now)
	if err != nil {
		return err
	}
	plan, err := loadPlatformManagedUpdateFinishPlan(e, expected, source)
	if err != nil {
		return err
	}
	var phase, desired, epoch string
	var generation, policy, revision int64
	if err := e.queryRow(`SELECT phase,desired_state,generation,revision,
		policy_revision,controller_epoch FROM app_platform_installations
		WHERE id=?`, expected.installationID).
		Scan(&phase, &desired, &generation, &revision, &policy, &epoch); err != nil ||
		!platformRetryPhaseAllowed(phase, plan.RecoversOperationID) || desired != "enabled" ||
		generation != plan.SourceGeneration || policy != plan.SourcePolicyRevision ||
		revision != facts.installationRevision || epoch != expected.controllerEpoch {
		return errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	if effects != nil {
		if err := effects(e, now); err != nil {
			return err
		}
	}
	if _, err := e.exec(`UPDATE app_platform_operation_effects SET state='unknown',updated_ms=?
		WHERE operation_id=? AND kind IN ('migrate-data','set-data-quota') AND state='prepared'`, now, expected.id); err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	recovery, err := model.Fail(
		expected.snapshot.Revision, appplatform.EffectsUnknown)
	if err != nil ||
		recovery.Snapshot().Stage != appplatform.StageRecoveryRequired {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage='recovery_required',state_revision=?,
		row_revision=row_revision+1,error_code=?,
		completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		int64(recovery.Snapshot().Revision), code, now, now, expected.id,
		expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_installations
		SET phase='recovery_required',revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase IN ('ready','recovery_required')
		AND generation=? AND policy_revision=? AND revision=?
		AND control_fence=? AND controller_epoch=?`,
		now, expected.installationID, plan.SourceGeneration,
		plan.SourcePolicyRevision, facts.installationRevision,
		expected.fence, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage            appplatform.Stage `json:"stage"`
		SourceRelease    string            `json:"source_release_sha256"`
		TargetRelease    string            `json:"target_release_sha256"`
		SourceGeneration int64             `json:"source_generation"`
		TargetGeneration int64             `json:"target_generation"`
	}{
		expected.snapshot.Stage, plan.SourceReleaseSHA256,
		plan.TargetReleaseSHA256, plan.SourceGeneration,
		plan.TargetGeneration,
	})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.update_recovery_required',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// loadPlatformManagedUpdateFinishPlan is the plan an update is ended against. From the migrate stage on it is the
// finalized plan the checkpoint committed. An update that ended at the snapshot stage has no finalized plan: the
// checkpoint never committed, and finalizing is what the commit does. It is ended against the exact review it was
// admitted with, which carries every fact the ending reads (the generations, the source policy and release, the
// update it retries), checked against the operation and the source as the finalized plan is.
func loadPlatformManagedUpdateFinishPlan(e migExec, expected platformOperationLease, source platformCheckpointSource) (platformManagedUpdatePlan, error) {
	var zero platformManagedUpdatePlan
	if expected.snapshot.Stage != appplatform.StageSnapshot {
		plan, _, err := loadPlatformManagedUpdatePlan(e, expected, source)
		return plan, err
	}
	var raw, digest, operationScope, planScope string
	var sourceGeneration, targetGeneration int64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.actor_scope,p.actor_scope,o.source_generation,o.target_generation
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='update' AND o.managed=1 AND o.stage='snapshot' AND p.action='update'`,
		expected.id, expected.installationID).Scan(&raw, &digest, &operationScope, &planScope, &sourceGeneration, &targetGeneration); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	var review platformUpdateProjection
	unmarshalErr := json.Unmarshal([]byte(raw), &review)
	canonical, marshalErr := json.Marshal(review)
	if unmarshalErr != nil || marshalErr != nil || string(canonical) != raw || platformDigest([]byte(raw)) != digest ||
		operationScope != planScope || review.Format != platformUpdateReviewFormat ||
		review.InstallationID != expected.installationID || review.ControllerEpoch != expected.controllerEpoch ||
		review.SourceGeneration != sourceGeneration || review.TargetGeneration != targetGeneration ||
		sourceGeneration != int64(expected.snapshot.Generation)-1 || targetGeneration != int64(expected.snapshot.Generation) ||
		review.SourceGeneration < 1 || review.TargetGeneration != review.SourceGeneration+1 ||
		review.PolicyRevision < 1 || review.TargetPolicyRevision <= review.PolicyRevision ||
		review.SourceReleaseSHA256 != source.releaseSHA256 || review.AppID != source.appID || review.PublisherID != source.publisherID ||
		!platformValidSHA(review.SourceReleaseSHA256) || !platformValidSHA(review.Target.ReleaseSHA256) ||
		review.Target.ReleaseSHA256 == review.SourceReleaseSHA256 ||
		!samePlatformManagedUpdateRuntime(review.Target.Runtime, source.runtimeTarget) {
		return zero, errAppPlatformStageEvidence
	}
	return platformManagedUpdatePlan{
		Format:         platformManagedUpdateFormat,
		InstallationID: review.InstallationID, AppID: review.AppID, PublisherID: review.PublisherID,
		SourceReleaseSHA256: review.SourceReleaseSHA256, TargetReleaseSHA256: review.Target.ReleaseSHA256,
		SourceGeneration: review.SourceGeneration, TargetGeneration: review.TargetGeneration,
		SourcePolicyRevision: review.PolicyRevision, TargetPolicyRevision: review.TargetPolicyRevision,
		ControllerEpoch: review.ControllerEpoch, TargetRuntime: review.Target.Runtime,
		Migration: review.Migration, RecoversOperationID: review.RecoversOperationID,
	}, nil
}
