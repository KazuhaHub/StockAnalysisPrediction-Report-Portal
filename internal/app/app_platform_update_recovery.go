package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

// A retry-exhausted managed update is never marked failed while a target may
// have mounted data. The source remains the active generation, the installation
// enters review-only recovery, and a separate reviewed restore is required.
func (s *Server) finishPlatformManagedUpdateForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Stage == appplatform.StageSnapshot {
		// An update ends at the snapshot stage either on the runner's definite refusal, whose code it reports, or
		// because its attempts ran out on errors that settled nothing: checkpoint_incomplete.
		code := platformSnapshotRefusal(ctx)
		if code == "" {
			code = platformCheckpointIncompleteCode
		}
		return s.finishPlatformManagedUpdateWith(ctx, expected, nil, code)
	}
	return s.finishPlatformManagedUpdateWith(ctx, expected, nil, platformUpdateRecoveryCode)
}

type platformSnapshotRefusalKey struct{}

// withPlatformSnapshotRefusal tells the finisher of an update that the runner refused its checkpoint, and why.
func withPlatformSnapshotRefusal(ctx context.Context, code string) context.Context {
	return context.WithValue(ctx, platformSnapshotRefusalKey{}, code)
}

// platformSnapshotRefusal is the runner's code for the checkpoint refusal the finisher was told of, or empty.
func platformSnapshotRefusal(ctx context.Context) string {
	code, _ := ctx.Value(platformSnapshotRefusalKey{}).(string)
	if !platformCheckpointRefusalCode(code) {
		return ""
	}
	return code
}

// platformCheckpointRefusalCode is a failure code the runner answers a refused checkpoint with.
func platformCheckpointRefusalCode(code string) bool {
	return code == "checkpoint_unsafe" || code == "checkpoint_too_large" || code == platformCheckpointIncompleteCode
}

// platformCheckpointIncompleteCode is the code of an update that ended at the snapshot stage with no verdict from the
// runner: the checkpoint could not be completed.
const platformCheckpointIncompleteCode = "checkpoint_incomplete"

// settlePlatformRefusedSnapshot ends the snapshot effect and the recovery set the runner's refusal leaves behind. The
// snapshot comes before the migration and a checkpoint only reads the data, so nothing was changed: the effect is
// reverted with the runner's code, and the recovery set, which never held an archive, is deleted. Neither is left to
// gate the installation as an unresolved effect for ever.
func settlePlatformRefusedSnapshot(e migExec, operation, code string, now int64) error {
	var effectID, digest string
	err := e.queryRow(`SELECT effect_id,payload_sha256 FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND kind='snapshot'
		AND state IN ('prepared','unknown') AND observed_fence=0 AND receipt_json='{}'`, operation).Scan(&effectID, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	receipt, _ := json.Marshal(struct {
		State         string `json:"state"`
		Code          string `json:"code"`
		EffectID      string `json:"effect_id"`
		PayloadSHA256 string `json:"payload_sha256"`
	}{"failed", code, effectID, digest})
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
		WHERE effect_id=? AND state IN ('prepared','unknown') AND observed_fence=0 AND receipt_json='{}'`, string(receipt), now, effectID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	_, err = e.exec(`UPDATE app_platform_recovery_sets SET state='deleted',deleted_ms=?,updated_ms=?
		WHERE operation_id=? AND state='preparing' AND manifest_sha256 IS NULL AND bytes=0 AND verified_ms IS NULL`, now, now, operation)
	return err
}

// platformUpdateRecoveryCode is the error code of an update that ended recovery_required for a reason the update
// itself does not name.
const platformUpdateRecoveryCode = "update_recovery_required"

// platformUpdateRecoveryCodes are the error codes a managed update that ended recovery_required may carry. The
// restore and retry reviews of a failed update accept any of them: what differs is only the reason shown, because the
// recovery is the same. A raise of the data quota that the runner refused (data_quota_capacity) or failed for
// good (data_quota_unavailable) names itself.
func platformUpdateRecoveryCodes(code string) bool {
	return code == platformUpdateRecoveryCode || code == "data_quota_capacity" || code == "data_quota_unavailable" ||
		platformCheckpointRefusalCode(code)
}

// finishPlatformManagedUpdateWith ends the operation for review with the given error code. effects, when set,
// settles the effect rows the reason for ending belongs to in the same transaction. A migrate-data effect or a
// quota raise still in flight becomes unknown: it is never sent as a new effect, and a migration is settled by a
// discard, a raise by the convergence step.
func (s *Server) finishPlatformManagedUpdateWith(ctx context.Context, expected platformOperationLease, effects func(e migExec, now int64) error, code string) error {
	if !platformUpdateRecoveryCodes(code) {
		return errAppPlatformStageEvidence
	}
	atSnapshot := expected.snapshot.Stage == appplatform.StageSnapshot
	if atSnapshot && !platformCheckpointRefusalCode(code) {
		// Whatever the reason, an update ended at the snapshot stage is ended the same way, below.
		code = platformCheckpointIncompleteCode
	}
	if !atSnapshot && platformCheckpointRefusalCode(code) {
		// A checkpoint code names an ending at the snapshot stage, which changed no data, and nothing else: the repair
		// that resolves such endings (resolvePlatformSnapshotEndingsByRepair) relies on it.
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
	// An update that ends at the snapshot stage, refused by the runner or out of attempts, has its source quiesced and
	// nothing else done: the data was never touched. Its snapshot effect and empty recovery set end, and the
	// installation is left as a disable leaves it, which is where a repair is admitted from, so the source generation
	// has a way back. That holds in every phase the update can be in, a retry of another failed update included: no
	// ending at this stage leaves the installation enabled and in recovery with its source stopped.
	if atSnapshot {
		if err := settlePlatformRefusedSnapshot(e, expected.id, code, now); err != nil {
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
	if atSnapshot {
		if err := disablePlatformInstallationForRecovery(e, expected, source, plan, facts.installationRevision, now); err != nil {
			return err
		}
	} else {
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
	}
	detail, _ := json.Marshal(struct {
		Stage            appplatform.Stage `json:"stage"`
		SourceRelease    string            `json:"source_release_sha256"`
		TargetRelease    string            `json:"target_release_sha256"`
		SourceGeneration int64             `json:"source_generation"`
		TargetGeneration int64             `json:"target_generation"`
		Code             string            `json:"code"`
	}{
		expected.snapshot.Stage, plan.SourceReleaseSHA256,
		plan.TargetReleaseSHA256, plan.SourceGeneration,
		plan.TargetGeneration, code,
	})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.update_recovery_required',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
		return err
	}
	if atSnapshot {
		// The system disabled the installation: that is recorded against the installation, with why.
		disabled, _ := json.Marshal(struct {
			OperationID         string `json:"operation_id"`
			Code                string `json:"code"`
			DisabledForRecovery bool   `json:"disabled_for_recovery"`
			Generation          int64  `json:"generation"`
		}{expected.id, code, true, plan.SourceGeneration})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.installation_disabled_for_recovery',
			'app_platform_installation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339),
			expected.installationID, string(disabled)); err != nil {
			return err
		}
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

// disablePlatformInstallationForRecovery leaves an installation whose source was quiesced by an update that went no
// further the way a disable leaves one: disabled, its activation retired, the data namespace's writer fence raised
// and its tasks and event subscriptions stopped, with the phase recovery_required. That is the state the repair
// action is admitted from, and the generation is kept as every recovery from a disabled installation keeps it.
func disablePlatformInstallationForRecovery(e migExec, expected platformOperationLease, source platformCheckpointSource, plan platformManagedUpdatePlan, revision, now int64) error {
	changed, err := e.exec(`UPDATE app_platform_installations
		SET desired_state='disabled',phase='recovery_required',revision=revision+1,control_fence=control_fence+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase IN ('ready','recovery_required')
		AND generation=? AND policy_revision=? AND revision=?
		AND control_fence=? AND controller_epoch=?`,
		now, expected.installationID, plan.SourceGeneration,
		plan.SourcePolicyRevision, revision,
		expected.fence, expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_activations SET state='retired',updated_ms=?
		WHERE installation_id=? AND state='active' AND generation=?`, now, expected.installationID, plan.SourceGeneration)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET writer_fence=writer_fence+1,revision=revision+1,updated_ms=?
		WHERE id=? AND owner_installation_id=? AND state='active'`, now, source.namespaceID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if err := invalidatePlatformTasksForInstallation(e, expected.installationID, "installation_disabled", now); err != nil {
		return err
	}
	return cancelPlatformEventSubscriptionsForInstallation(e, expected.installationID, "paused", now)
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

// resolvePlatformSnapshotEndingsByRepair resolves, in the transaction that activates a repair, the managed updates of
// the installation that ended at the snapshot stage on the generation the repair brought back. Such an update changed
// no data: the snapshot comes before the migration, and its ending disabled the installation for recovery so that a
// repair could bring the source back. Nothing else resolves it (there is no checkpoint to restore and no migration to
// retry), so without this the installation would stay gated for ever after the repair. A retry that ended there
// resolves the failed updates it retried as well, the way a completed retry does: they were settled before the retry
// was admitted, and the source they all left is what the repair started again. Each operation gets the recovered
// marker launch reads, and an audit row naming the repair.
func resolvePlatformSnapshotEndingsByRepair(e migExec, lease platformOperationLease, runtimeID string, sourceGeneration, now int64) error {
	if lease.snapshot.Action != appplatform.ActionRepair {
		return errAppPlatformStageEvidence
	}
	rows, err := e.query(`SELECT o.id,o.error_code FROM app_platform_operations o
		WHERE o.installation_id=? AND o.action='update' AND o.managed=1 AND o.stage='recovery_required'
		AND o.completed_ms IS NOT NULL AND o.error_code IN (?,?,?) AND o.source_generation=?
		AND NOT EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='recovered')
		ORDER BY o.completed_ms,o.id`,
		lease.installationID, "checkpoint_unsafe", "checkpoint_too_large", platformCheckpointIncompleteCode, sourceGeneration)
	if err != nil {
		return err
	}
	type ended struct{ id, code string }
	var endings []ended
	for rows.Next() {
		var o ended
		if err := rows.Scan(&o.id, &o.code); err != nil {
			rows.Close()
			return err
		}
		endings = append(endings, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	resolved := map[string]bool{}
	at := time.UnixMilli(now).UTC().Format(time.RFC3339)
	for _, o := range endings {
		chain, err := platformRetryAncestors(e, o.id)
		if err != nil {
			return err
		}
		for _, id := range chain {
			if resolved[id] {
				continue
			}
			var marked int
			var action, stage string
			if err := e.queryRow(`SELECT o.action,o.stage,(SELECT COUNT(*) FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.stage='recovered')
				FROM app_platform_operations o WHERE o.id=? AND o.installation_id=?`, id, lease.installationID).Scan(&action, &stage, &marked); err != nil {
				return errAppPlatformStageEvidence
			}
			resolved[id] = true
			if marked != 0 {
				continue
			}
			if action != "update" || stage != "recovery_required" {
				return errAppPlatformStageEvidence
			}
			if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
				VALUES(?,'recovered',1,?,'recovered',?,?,?,?,?,'applied','{}',?,?)`,
				id, platformDerivedUUID("recovered", id), platformDigest([]byte("repair/"+id+"/"+lease.id)),
				runtimeID, lease.controllerEpoch, lease.fence, lease.fence, now, now); err != nil {
				return err
			}
			detail, _ := json.Marshal(struct {
				Operation       string `json:"operation_id"`
				Repair          string `json:"repair_operation_id"`
				EndedAtSnapshot string `json:"ended_at_snapshot_operation_id"`
				Code            string `json:"code"`
			}{id, lease.id, o.id, o.code})
			if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
				VALUES(?,'system',0,'app_platform.recovery_resolved_by_repair','app_platform_operation',?,?,'')`,
				at, id, string(detail)); err != nil {
				return err
			}
		}
	}
	return nil
}
