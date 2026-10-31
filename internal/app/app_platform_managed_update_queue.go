package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformAdmittedManagedUpdate struct {
	planID                      string
	plan                        platformUpdateProjection
	source                      platformManagedUpdateSource
	verified                    platformStagedRelease
	target                      *platformManagedInstallTarget
	currentInstallationRevision int64
}

func (s *Server) claimNextPlatformManagedUpdate(ctx context.Context, worker string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if !platformValidUUID(worker) {
		return zero, errAppPlatformLeaseConflict
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
	var epoch, paused string
	if err := e.queryRow(
		`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).
		Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(
		`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).
		Scan(&paused); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id
		FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='update' AND o.managed=1
		AND o.completed_ms IS NULL
		AND o.stage IN ('queued','validate','download','stage','quiesce',
			'snapshot','migrate','start','probe','activate')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.phase='ready' AND i.desired_state='enabled'
		AND i.runtime_id IS NOT NULL
		AND i.generation=o.source_generation
		AND o.target_generation=i.generation+1
		AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.controller_epoch=o.controller_epoch
		AND (p.plan_json LIKE ? OR p.plan_json LIKE ?)
		ORDER BY o.created_ms,o.id LIMIT 32`,
		now, epoch, epoch, `{"format":"`+platformUpdateReviewFormat+`",%`,
		`{"format":"`+platformManagedUpdateFormat+`",%`)
	if err != nil {
		return zero, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return zero, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return zero, err
	}
	if len(ids) == 0 {
		return zero, nil
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	for _, id := range ids {
		facts, err := loadPlatformOperationLease(e, id)
		if err != nil {
			if errors.Is(err, errAppPlatformLeaseConflict) {
				continue
			}
			return zero, err
		}
		if facts.lease.leaseUntilMS > facts.now ||
			facts.lease.snapshot.Action != appplatform.ActionUpdate ||
			!facts.lease.snapshot.Managed ||
			facts.lease.snapshot.Stage == appplatform.StageComplete ||
			facts.lease.snapshot.Stage == appplatform.StageFailed ||
			facts.lease.snapshot.Stage == appplatform.StageRecoveryRequired {
			continue
		}
		admitted, err := s.loadPlatformAdmittedManagedUpdate(
			e, facts.lease, trust, facts.now)
		if err != nil {
			return zero, err
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		detail, _ := json.Marshal(struct {
			Worker   string `json:"worker_id"`
			Runtime  string `json:"runtime_id"`
			Rollback string `json:"rollback_operation_id,omitempty"`
		}{worker, admitted.target.RuntimeID, ""})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.managed_update_claimed',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
			id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

func (s *Server) loadPlatformAdmittedManagedUpdate(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) (platformAdmittedManagedUpdate, error) {
	var result platformAdmittedManagedUpdate
	if lease.snapshot.Action != appplatform.ActionUpdate ||
		!lease.snapshot.Managed || lease.snapshot.Generation < 2 ||
		lease.snapshot.Generation == math.MaxUint64 {
		return result, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, operationScope, planScope, username, created,
		authority, epoch, action, source, targetRelease string
	var sourceGeneration, targetGeneration, managed, expires, revision,
		policy, trustRevision, accountGeneration int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.id,p.plan_json,p.plan_sha256,
		o.actor_scope,p.username,p.account_created,p.account_generation,
		p.authority_fingerprint,p.actor_scope,p.expires_ms,p.consumed_ms,
		p.installation_revision,p.policy_revision,p.trust_revision,
		p.controller_epoch,p.source_release_sha256,p.target_release_sha256,
		p.action,o.source_generation,o.target_generation,o.managed
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`+lock, lease.id).
		Scan(&result.planID, &raw, &digest, &operationScope,
			&username, &created, &accountGeneration, &authority,
			&planScope, &expires, &consumed, &revision, &policy,
			&trustRevision, &epoch, &source, &targetRelease, &action,
			&sourceGeneration, &targetGeneration, &managed); err != nil {
		return result, err
	}
	if action != "update" || managed != 1 ||
		operationScope != planScope || sourceGeneration < 1 ||
		uint64(sourceGeneration) != lease.snapshot.Generation-1 ||
		uint64(targetGeneration) != lease.snapshot.Generation ||
		targetGeneration != sourceGeneration+1 ||
		!consumed.Valid || consumed.Int64 > now || expires <= now ||
		epoch != lease.controllerEpoch ||
		platformDigest([]byte(raw)) != digest ||
		!platformValidSHA(source) || !platformValidSHA(targetRelease) {
		return result, errAppPlatformStageEvidence
	}
	var formatProbe struct {
		Format string `json:"format"`
	}
	if json.Unmarshal([]byte(raw), &formatProbe) != nil {
		return result, errAppPlatformStageEvidence
	}
	if formatProbe.Format == platformManagedUpdateFormat {
		var finalized platformManagedUpdatePlan
		unmarshalErr := json.Unmarshal([]byte(raw), &finalized)
		finalizedRaw, marshalErr := json.Marshal(finalized)
		if unmarshalErr != nil || marshalErr != nil ||
			string(finalizedRaw) != raw {
			return result, errAppPlatformStageEvidence
		}
		return s.loadPlatformFinalizedManagedUpdateAdmission(
			e, lease, trust, now, result.planID, raw, digest,
			username, created, accountGeneration, trustRevision,
			authority, epoch, finalized)
	}
	if err := json.Unmarshal([]byte(raw), &result.plan); err != nil ||
		formatProbe.Format != platformUpdateReviewFormat ||
		result.plan.InstallationID != lease.installationID ||
		result.plan.SourceReleaseSHA256 != source ||
		result.plan.Target.ReleaseSHA256 != targetRelease ||
		result.plan.SourceGeneration != sourceGeneration ||
		result.plan.TargetGeneration != targetGeneration ||
		result.plan.InstallationRevision != revision ||
		result.plan.PolicyRevision != policy ||
		result.plan.TrustRevision != trustRevision ||
		result.plan.ActorAuthority != authority ||
		result.plan.ControllerEpoch != epoch ||
		result.plan.Target.Runtime == nil ||
		!platformManagedFormat(result.plan.Target.Format) {
		return result, errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(result.plan)
	if err != nil || string(canonical) != raw {
		return result, errAppPlatformStageEvidence
	}
	var currentApp, currentPublisher, desired, phase, currentEpoch,
		runtimeID string
	var generation, currentRevision, currentPolicy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`,
		lease.installationID).
		Scan(&currentApp, &currentPublisher, &desired, &phase,
			&generation, &currentRevision, &currentPolicy,
			&currentEpoch, &runtimeID); err != nil ||
		currentApp != result.plan.AppID ||
		currentPublisher != result.plan.PublisherID ||
		desired != "enabled" || phase != "ready" ||
		generation != result.plan.SourceGeneration ||
		currentRevision < result.plan.InstallationRevision ||
		currentPolicy != result.plan.PolicyRevision ||
		currentEpoch != result.plan.ControllerEpoch ||
		runtimeID != result.plan.Target.Runtime.RuntimeID {
		return result, errAppPlatformStageEvidence
	}
	result.currentInstallationRevision = currentRevision
	if err := validateManagedInstallTarget(
		e, result.plan.Target.Runtime, lease.controllerEpoch); err != nil {
		return result, err
	}
	var activeRelease string
	var activeFormat int64
	if err := e.queryRow(`SELECT release_sha256,data_format
		FROM app_platform_activations
		WHERE installation_id=? AND generation=? AND policy_revision=?
		AND state='active'`, lease.installationID, generation, currentPolicy).
		Scan(&activeRelease, &activeFormat); err != nil ||
		activeRelease != result.plan.SourceReleaseSHA256 ||
		activeFormat != result.plan.CurrentDataFormat {
		return result, errAppPlatformStageEvidence
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
		WHERE installation_id=? AND completed_ms IS NULL`,
		lease.installationID).Scan(&pending); err != nil || pending != 1 {
		return result, errAppPlatformStageEvidence
	}
	if err := platformInstallSponsor(
		e, sql.NullString{String: username, Valid: true},
		created, accountGeneration, now); err != nil {
		return result, errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(
		e, targetRelease, trustRevision, trust, now)
	if err != nil || verified.appID != result.plan.AppID ||
		verified.publisherID != result.plan.PublisherID {
		return result, errAppPlatformPlanChanged
	}
	sourcePlan, err := loadPlatformManagedUpdateSource(
		e, lease.installationID, activeRelease, generation,
		result.plan.Target.Runtime)
	if err != nil {
		return result, err
	}
	actor := platformActorFacts{authorityFingerprint: authority}
	expected, err := platformBuildManagedUpdateProjection(
		e, lease.installationID, sourcePlan,
		result.plan.InstallationRevision, result.plan.PolicyRevision,
		result.plan.CurrentDataFormat, actor,
		platformActorControl{epoch: epoch, nowMS: now},
		result.plan.Target.Runtime, verified)
	if err != nil {
		return result, errAppPlatformPlanChanged
	}
	expectedRaw, err := json.Marshal(expected)
	if err != nil || string(expectedRaw) != raw {
		return result, errAppPlatformPlanChanged
	}
	result.source = sourcePlan
	result.verified = verified
	result.target = result.plan.Target.Runtime
	return result, nil
}

func (s *Server) loadPlatformFinalizedManagedUpdateAdmission(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64, planID, raw, digest, username, created string, accountGeneration, trustRevision int64, authority, epoch string, finalized platformManagedUpdatePlan) (platformAdmittedManagedUpdate, error) {
	var result platformAdmittedManagedUpdate
	result.planID = planID
	target := finalized.TargetRuntime
	var revision, policy int64
	if target == nil ||
		!platformValidUUID(target.RuntimeID) ||
		finalized.InstallationID != lease.installationID ||
		finalized.ControllerEpoch != lease.controllerEpoch ||
		finalized.SourceGeneration != int64(lease.snapshot.Generation)-1 ||
		finalized.TargetGeneration != int64(lease.snapshot.Generation) ||
		finalized.TargetReleaseSHA256 == "" {
		return result, errAppPlatformStageEvidence
	}
	var currentApp, currentPublisher, desired, phase, currentEpoch,
		runtimeID string
	var generation, currentRevision, currentPolicy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`,
		lease.installationID).
		Scan(&currentApp, &currentPublisher, &desired, &phase,
			&generation, &currentRevision, &currentPolicy,
			&currentEpoch, &runtimeID); err != nil ||
		currentApp != finalized.AppID ||
		currentPublisher != finalized.PublisherID ||
		desired != "enabled" || phase != "ready" ||
		generation != finalized.SourceGeneration ||
		currentPolicy != finalized.SourcePolicyRevision ||
		currentEpoch != lease.controllerEpoch ||
		runtimeID != target.RuntimeID {
		return result, errAppPlatformStageEvidence
	}
	if err := validateManagedInstallTarget(
		e, target, lease.controllerEpoch); err != nil {
		return result, err
	}
	var activeRelease string
	var activeFormat int64
	if err := e.queryRow(`SELECT release_sha256,data_format
		FROM app_platform_activations
		WHERE installation_id=? AND generation=? AND policy_revision=?
		AND state='active'`, lease.installationID, generation, currentPolicy).
		Scan(&activeRelease, &activeFormat); err != nil ||
		activeRelease != finalized.SourceReleaseSHA256 ||
		activeFormat != finalized.DataFormat {
		return result, errAppPlatformStageEvidence
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations
		WHERE installation_id=? AND completed_ms IS NULL`,
		lease.installationID).Scan(&pending); err != nil || pending != 1 {
		return result, errAppPlatformStageEvidence
	}
	if err := platformInstallSponsor(
		e, sql.NullString{String: username, Valid: true},
		created, accountGeneration, now); err != nil {
		return result, errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(
		e, finalized.TargetReleaseSHA256, trustRevision, trust, now)
	if err != nil || verified.appID != finalized.AppID ||
		verified.publisherID != finalized.PublisherID {
		return result, errAppPlatformPlanChanged
	}
	dek, err := s.dek()
	if err != nil {
		return result, err
	}
	runtime, seed, err := s.platformDataRuntime(
		e, lease.controllerEpoch, dek)
	if err != nil {
		return result, err
	}
	defer clear(seed)
	if runtime.id != target.RuntimeID {
		return result, errAppPlatformStageEvidence
	}
	source, err := loadPlatformCheckpointSource(
		e, lease, runtime, now)
	if err != nil {
		return result, err
	}
	plan, descriptor, err := loadPlatformManagedUpdatePlan(
		e, lease, source)
	if err != nil || !reflect.DeepEqual(plan, finalized) ||
		len(descriptor) == 0 {
		return result, errAppPlatformStageEvidence
	}
	if err := e.queryRow(`SELECT installation_revision,policy_revision,
		trust_revision FROM app_platform_review_plans WHERE id=?`,
		planID).
		Scan(&revision, &policy, &trustRevision); err != nil ||
		policy != finalized.SourcePolicyRevision ||
		trustRevision < 1 ||
		platformDigest([]byte(raw)) != digest {
		return result, errAppPlatformStageEvidence
	}
	sourcePlan, err := loadPlatformManagedUpdateSource(
		e, lease.installationID, activeRelease, generation, target)
	if err != nil {
		return result, err
	}
	result.plan = platformUpdateProjection{
		Format:         platformUpdateReviewFormat,
		InstallationID: lease.installationID,
		AppID:          finalized.AppID, PublisherID: finalized.PublisherID,
		SourceReleaseSHA256:  finalized.SourceReleaseSHA256,
		SourceGeneration:     finalized.SourceGeneration,
		TargetGeneration:     finalized.TargetGeneration,
		InstallationRevision: revision,
		PolicyRevision:       finalized.SourcePolicyRevision,
		TargetPolicyRevision: finalized.TargetPolicyRevision,
		TrustRevision:        trustRevision,
		ControllerEpoch:      epoch, ActorAuthority: authority,
		CurrentDataFormat: finalized.DataFormat,
		Target: platformInstallProjection{
			Format: sourcePlan.format, AppID: finalized.AppID,
			PublisherID:     finalized.PublisherID,
			ReleaseSHA256:   finalized.TargetReleaseSHA256,
			TrustRevision:   trustRevision,
			ControllerEpoch: epoch, ActorAuthority: authority,
			Runtime: target,
		},
	}
	result.source = sourcePlan
	result.verified = verified
	result.target = target
	result.currentInstallationRevision = currentRevision
	return result, nil
}
