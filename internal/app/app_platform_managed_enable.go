package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

const platformManagedEnableFormat = "portal.managed-enable-review/1-dev.1"

func platformBuildManagedEnablePlan(
	e migExec,
	installation string,
	target *platformManagedInstallTarget,
	runtime platformRuntimeRegistration,
	source platformCheckpointSource,
	now int64,
) (platformManagedUpdatePlan, int64, error) {
	return platformBuildManagedSameReleasePlan(
		e, installation, target, runtime, source, now,
		appplatform.ActionEnable, platformManagedEnableFormat)
}

func platformBuildManagedSameReleasePlan(
	e migExec,
	installation string,
	target *platformManagedInstallTarget,
	runtime platformRuntimeRegistration,
	source platformCheckpointSource,
	now int64,
	action appplatform.Action,
	format string,
) (platformManagedUpdatePlan, int64, error) {
	var zero platformManagedUpdatePlan
	if !platformSameReleaseLifecycle(action) ||
		format != platformSameReleaseLifecycleFormat(action) {
		return zero, 0, errAppPlatformPlanChanged
	}
	var appID, publisher, desired, phase, epoch, runtimeID string
	var generation, revision, policyRevision, controlFence, trustRevision int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		generation,revision,policy_revision,controller_epoch,runtime_id,
		control_fence FROM app_platform_installations WHERE id=?`,
		installation).
		Scan(&appID, &publisher, &desired, &phase, &generation, &revision,
			&policyRevision, &epoch, &runtimeID, &controlFence); err != nil {
		return zero, revision, err
	}
	expectedInstallationGeneration := source.sourceGeneration + 1
	if action == appplatform.ActionRepair &&
		(phase == "recovery_required" ||
			phase == "review_required") {
		expectedInstallationGeneration = source.sourceGeneration
	}
	if runtimeID != runtime.id || epoch != runtime.controllerEpoch ||
		generation != expectedInstallationGeneration ||
		policyRevision < 1 || controlFence < 1 ||
		!((desired == "disabled" &&
			(phase == "disabled" ||
				(action == appplatform.ActionRepair &&
					(phase == "recovery_required" ||
						phase == "review_required")))) ||
			(desired == "enabled" && phase == "pending")) {
		return zero, revision, errAppPlatformPlanChanged
	}
	var activationID, activationRelease, namespace string
	var activationGeneration, activationPolicy, activationFormat int64
	if err := e.queryRow(`SELECT id,release_sha256,generation,policy_revision,
		data_namespace_id,data_format FROM app_platform_activations
		WHERE installation_id=? AND generation=? AND state='retired'`,
		installation, source.sourceGeneration).
		Scan(&activationID, &activationRelease, &activationGeneration,
			&activationPolicy, &namespace, &activationFormat); err != nil {
		return zero, revision, errAppPlatformPlanChanged
	}
	if activationRelease != source.releaseSHA256 ||
		activationGeneration != source.sourceGeneration ||
		activationPolicy != policyRevision ||
		namespace != source.namespaceID ||
		activationFormat != mustPlatformCheckpointInt(source.dataFormat) {
		return zero, revision, errAppPlatformPlanChanged
	}
	parsed, err := appplatform.ParseDescriptor(source.descriptor)
	if err != nil || parsed.Manifest.Backend == nil ||
		parsed.SHA256 != source.releaseSHA256 ||
		int64(parsed.Manifest.Data.Schema) !=
			mustPlatformCheckpointInt(source.dataFormat) ||
		!samePlatformManagedUpdateRuntime(source.runtimeTarget, target) {
		return zero, revision, errAppPlatformPlanChanged
	}
	if _, err := platformInstallCapabilitySelection(
		parsed.Manifest, target); err != nil {
		return zero, revision, errAppPlatformPlanChanged
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers
		WHERE id=?`, publisher).Scan(&trustRevision); err != nil {
		return zero, revision, err
	}
	plan := platformManagedUpdatePlan{
		Format:         format,
		Action:         action,
		InstallationID: installation, AppID: appID, PublisherID: publisher,
		SourceReleaseSHA256:        source.releaseSHA256,
		TargetReleaseSHA256:        source.releaseSHA256,
		SourceGeneration:           source.sourceGeneration,
		TargetGeneration:           source.sourceGeneration + 1,
		SourcePolicyRevision:       policyRevision,
		TargetPolicyRevision:       policyRevision,
		ControllerEpoch:            epoch,
		DataNamespaceID:            source.namespaceID,
		DataFormat:                 mustPlatformCheckpointInt(source.dataFormat),
		SourceWriterFence:          mustPlatformCheckpointInt(source.writerFence),
		SourceControlFence:         controlFence,
		SourceActivationID:         activationID,
		SourceActivationGeneration: activationGeneration,
		TargetRuntime:              target,
	}
	plan.CheckpointWriterFence = mustPlatformCheckpointInt(source.writerFence)
	sourceWriterFence := plan.CheckpointWriterFence
	if source.currentWriterFence != "" {
		sourceWriterFence = mustPlatformCheckpointInt(
			source.currentWriterFence)
	}
	plan.SourceWriterFence = sourceWriterFence
	plan.GrantsSHA256, err = platformRemovalGrantsDigest(e, installation)
	if err != nil {
		return zero, revision, err
	}
	plan.AudienceSHA256, err = platformInstallationAudienceSHA(e, installation)
	if err != nil {
		return zero, revision, err
	}
	if plan.SourceGeneration < 1 || plan.TargetGeneration != plan.SourceGeneration+1 ||
		plan.SourceReleaseSHA256 == "" || plan.AppID == "" ||
		plan.PublisherID == "" || plan.DataNamespaceID == "" ||
		plan.DataFormat < 1 || plan.SourceWriterFence < 1 ||
		!platformValidSHA(plan.GrantsSHA256) ||
		!platformValidSHA(plan.AudienceSHA256) {
		return zero, revision, errAppPlatformPlanChanged
	}
	plan.TrustRevision = trustRevision
	if now < 0 || trustRevision < 1 || target == nil {
		return zero, revision, errAppPlatformPlanChanged
	}
	return plan, revision, nil
}

//lint:ignore U1000 explicit managed lifecycle development entry point
func (s *Server) createPlatformManagedEnableReviewContext(ctx context.Context, session platformActorSession, installation string, endpoint platformRuntimeEndpoint) (platformRemovalReview, error) {
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalReview{}, err
	}
	return s.createPlatformManagedEnableReviewWithRuntimeContext(
		ctx, session, installation, target, facts.runtime)
}

func (s *Server) createPlatformManagedEnableReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	installation string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
) (platformRemovalReview, error) {
	return s.createPlatformManagedSameReleaseReviewWithRuntimeContext(
		ctx, session, installation, target, observedRuntime,
		appplatform.ActionEnable)
}

func (s *Server) createPlatformManagedSameReleaseReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	installation string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
	action appplatform.Action,
) (platformRemovalReview, error) {
	var result platformRemovalReview
	if !platformSameReleaseLifecycle(action) {
		return result, errAppPlatformPlanChanged
	}
	format := platformSameReleaseLifecycleFormat(action)
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		if err := lockOUMutation(e); err != nil {
			return err
		}
		dek, err := s.dek()
		if err != nil {
			return err
		}
		runtime, seed, err := s.platformDataRuntime(e, control.epoch, dek)
		clear(seed)
		if err != nil ||
			!samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		var generation int64
		if err := e.queryRow(`SELECT generation FROM app_platform_installations
			WHERE id=?`, installation).Scan(&generation); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if generation < 2 || generation == math.MaxInt64 {
			return errAppPlatformPlanChanged
		}
		expected := platformOperationLease{
			installationID: installation, controllerEpoch: control.epoch,
			snapshot: appplatform.OperationSnapshot{
				Action: action, Managed: true,
				Generation: uint64(generation), Revision: 1,
				Stage: appplatform.StageQueued,
			},
		}
		source, err := loadPlatformCheckpointSourceForEnableReview(
			e, expected, runtime, control.nowMS)
		if err != nil {
			return fmt.Errorf(
				"managed %s review source: %w", action, err)
		}
		plan, revision, err := platformBuildManagedSameReleasePlan(
			e, installation, target, runtime, source, control.nowMS,
			action, format)
		if err != nil {
			return fmt.Errorf(
				"managed %s review plan: %w", action, err)
		}
		if plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 ||
			plan.SourceGeneration+1 != plan.TargetGeneration ||
			plan.SourceActivationGeneration != plan.SourceGeneration ||
			revision == math.MaxInt64 {
			return errAppPlatformPlanChanged
		}
		verified, err := platformVerifyStoredRelease(
			e, plan.TargetReleaseSHA256, plan.TrustRevision,
			trust, control.nowMS)
		if err != nil || verified.appID != plan.AppID ||
			verified.publisherID != plan.PublisherID {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(plan)
		if err != nil || len(raw) > 65536 ||
			control.nowMS > math.MaxInt64-900000 {
			return errAppPlatformPlanChanged
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		result = platformRemovalReview{
			ID: id, SHA256: platformDigest(raw), JSON: string(raw),
			ExpiresMS: control.nowMS + 900000,
		}
		_, err = e.exec(`INSERT INTO app_platform_review_plans(
			id,installation_id,app_id,publisher_id,action,source_release_sha256,
			target_release_sha256,installation_revision,policy_revision,
			trust_revision,controller_epoch,actor_scope,username,
			account_created,account_generation,authority_fingerprint,plan_sha256,
			plan_json,expires_ms,consumed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?)`,
			id, installation, plan.AppID, plan.PublisherID,
			string(action), plan.SourceReleaseSHA256,
			plan.TargetReleaseSHA256, revision, plan.SourcePolicyRevision,
			plan.TrustRevision, plan.ControllerEpoch, actor.scope,
			actor.username, actor.created, actor.generation,
			actor.authorityFingerprint, result.SHA256, result.JSON,
			result.ExpiresMS, control.nowMS, control.nowMS)
		return err
	})
	if err != nil {
		return platformRemovalReview{}, err
	}
	return result, nil
}

//lint:ignore U1000 explicit managed lifecycle development entry point
func (s *Server) consumePlatformManagedEnableReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string, endpoint platformRuntimeEndpoint) (platformRemovalCommit, error) {
	if !platformValidUUID(planID) || !platformValidUUID(key) ||
		!platformValidSHA(planSHA) {
		return platformRemovalCommit{}, errAppPlatformPlanChanged
	}
	facts, err := s.observePlatformManagedRuntime(ctx, session, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	target, err := managedInstallTarget(facts, endpoint)
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return s.consumePlatformManagedEnableReviewWithRuntimeContext(
		ctx, session, planID, planSHA, key, target, facts.runtime)
}

func (s *Server) consumePlatformManagedEnableReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
) (platformRemovalCommit, error) {
	return s.consumePlatformManagedSameReleaseReviewWithRuntimeContext(
		ctx, session, planID, planSHA, key, target, observedRuntime,
		appplatform.ActionEnable)
}

func (s *Server) consumePlatformManagedSameReleaseReviewWithRuntimeContext(
	ctx context.Context,
	session platformActorSession,
	planID, planSHA, key string,
	target *platformManagedInstallTarget,
	observedRuntime platformRuntimeRegistration,
	action appplatform.Action,
) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformSameReleaseLifecycle(action) {
		return result, errAppPlatformPlanChanged
	}
	format := platformSameReleaseLifecycleFormat(action)
	request, _ := json.Marshal([]string{
		"portal-operation-commit/v1", planID, planSHA,
	})
	requestDigest, keyDigest := platformDigest(request), platformIdempotencySHA(key)
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, trust appplatform.PublisherTrust) error {
		found, exists, err := platformFindRemovalCommit(
			e, actor.scope, keyDigest, requestDigest)
		if err != nil {
			return err
		}
		if exists {
			result = found
			return nil
		}
		var installation, username sql.NullString
		var raw, digest, storedScope, actionText string
		var expires, accountGeneration int64
		var created string
		var consumed sql.NullInt64
		if err := e.queryRow(`SELECT installation_id,plan_json,plan_sha256,
			actor_scope,username,account_created,account_generation,expires_ms,
			consumed_ms,action FROM app_platform_review_plans WHERE id=?`,
			planID).Scan(&installation, &raw, &digest, &storedScope,
			&username, &created, &accountGeneration, &expires, &consumed,
			&actionText); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errAppPlatformPlanChanged
			}
			return err
		}
		if storedScope != actor.scope {
			return errAppPlatformActorUnavailable
		}
		if consumed.Valid {
			return errAppPlatformOperationConflict
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !installation.Valid ||
			appplatform.Action(actionText) != action {
			return errAppPlatformPlanChanged
		}
		if err := lockOUMutation(e); err != nil {
			return err
		}
		dek, err := s.dek()
		if err != nil {
			return err
		}
		runtime, seed, err := s.platformDataRuntime(e, control.epoch, dek)
		clear(seed)
		if err != nil ||
			!samePlatformManagedRuntime(runtime, observedRuntime) {
			return errAppPlatformRuntimeConflict
		}
		var generation int64
		if err := e.queryRow(`SELECT generation FROM app_platform_installations
			WHERE id=?`, installation.String).Scan(&generation); err != nil {
			return err
		}
		expected := platformOperationLease{
			installationID:  installation.String,
			controllerEpoch: control.epoch,
			snapshot: appplatform.OperationSnapshot{
				Action: action, Managed: true,
				Generation: uint64(generation), Revision: 1,
				Stage: appplatform.StageQueued,
			},
		}
		source, err := loadPlatformCheckpointSourceForEnableReview(
			e, expected, runtime, control.nowMS)
		if err != nil {
			return err
		}
		current, revision, err := platformBuildManagedSameReleasePlan(
			e, installation.String, target, runtime, source,
			control.nowMS, action, format)
		if err != nil {
			return err
		}
		currentRaw, err := json.Marshal(current)
		if err != nil || len(raw) > 65536 ||
			platformDigest([]byte(raw)) != digest || digest != planSHA ||
			string(currentRaw) != raw {
			return errAppPlatformPlanChanged
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS || !username.Valid ||
			username.String != actor.username ||
			created != actor.created ||
			accountGeneration != actor.generation {
			return errAppPlatformPlanChanged
		}
		verified, err := platformVerifyStoredRelease(
			e, current.TargetReleaseSHA256, current.TrustRevision,
			trust, control.nowMS)
		if err != nil || verified.appID != current.AppID ||
			verified.publisherID != current.PublisherID {
			return errAppPlatformPlanChanged
		}
		changed, err := e.exec(`UPDATE app_platform_review_plans
			SET consumed_ms=?,updated_ms=?
			WHERE id=? AND consumed_ms IS NULL AND plan_sha256=?
			AND expires_ms>?`, control.nowMS, control.nowMS, planID,
			planSHA, control.nowMS)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		phaseClause := "phase='disabled'"
		if action == appplatform.ActionRepair {
			phaseClause = "phase IN ('disabled','recovery_required','review_required')"
		}
		changed, err = e.exec(`UPDATE app_platform_installations
			SET desired_state='enabled',phase='pending',generation=?,
			revision=revision+1,control_fence=control_fence+1,updated_ms=?
			WHERE id=? AND desired_state='disabled' AND `+phaseClause+`
			AND generation=? AND revision=? AND policy_revision=?
			AND control_fence=? AND controller_epoch=? AND runtime_id=?`,
			current.TargetGeneration, control.nowMS, installation.String,
			generation,
			revision, current.SourcePolicyRevision,
			current.SourceControlFence, control.epoch, runtime.id)
		if err := platformLeaseCAS(changed, err); err != nil {
			return err
		}
		id, err := platformNewUUID()
		if err != nil {
			return err
		}
		_, err = e.exec(`INSERT INTO app_platform_operations(
			id,installation_id,review_plan_id,actor_scope,idempotency_sha256,
			request_sha256,action,managed,source_generation,target_generation,
			stage,state_revision,row_revision,lease_owner,lease_until_ms,fence,
			controller_epoch,error_code,completed_ms,created_ms,updated_ms)
			VALUES(?,?,?,?,?,?,?,1,?,?,'queued',1,1,NULL,NULL,0,?,
			NULL,NULL,?,?)`,
			id, installation.String, planID, actor.scope, keyDigest,
			requestDigest, string(action),
			current.SourceGeneration, current.TargetGeneration,
			control.epoch, control.nowMS, control.nowMS)
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan       string `json:"plan_id"`
			Action     string `json:"action"`
			Generation int64  `json:"generation"`
		}{planID, string(action), current.TargetGeneration})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,?,'app_platform.operation_admitted',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339),
			actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{
			OperationID: id, InstallationID: installation.String,
			Generation: current.TargetGeneration, Revision: 1,
			Stage: appplatform.StageQueued,
		}
		return nil
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}

func (s *Server) loadPlatformAdmittedManagedEnable(
	e migExec,
	lease platformOperationLease,
	trust appplatform.PublisherTrust,
	now int64,
) (platformManagedUpdatePlan, platformCheckpointSource, platformRuntimeRegistration, error) {
	var zeroPlan platformManagedUpdatePlan
	var zeroSource platformCheckpointSource
	var zeroRuntime platformRuntimeRegistration
	if !platformSameReleaseLifecycle(lease.snapshot.Action) ||
		!lease.snapshot.Managed ||
		lease.snapshot.Generation < 2 {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, operationScope, planScope string
	var sourceGeneration, targetGeneration, managed int64
	var installationRevision, policyRevision, trustRevision int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.actor_scope,
		p.actor_scope,o.source_generation,o.target_generation,o.managed,
		p.installation_revision,p.policy_revision,p.trust_revision,
		p.consumed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`+lock, lease.id).
		Scan(&raw, &digest, &operationScope, &planScope, &sourceGeneration,
			&targetGeneration, &managed, &installationRevision,
			&policyRevision, &trustRevision, &consumed); err != nil {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(raw), &plan) != nil ||
		len(raw) > 65536 || platformDigest([]byte(raw)) != digest ||
		operationScope != planScope || managed != 1 ||
		!consumed.Valid || consumed.Int64 > now ||
		plan.Format != platformSameReleaseLifecycleFormat(
			lease.snapshot.Action) ||
		plan.Action != lease.snapshot.Action ||
		plan.InstallationID != lease.installationID ||
		plan.SourceGeneration != sourceGeneration ||
		plan.TargetGeneration != targetGeneration ||
		targetGeneration != int64(lease.snapshot.Generation) ||
		targetGeneration != sourceGeneration+1 ||
		plan.SourceReleaseSHA256 != plan.TargetReleaseSHA256 ||
		plan.SourcePolicyRevision != policyRevision ||
		plan.TargetPolicyRevision != policyRevision ||
		plan.InstallationID == "" ||
		plan.DataNamespaceID == "" || plan.DataFormat < 1 ||
		plan.SourceWriterFence < 1 ||
		plan.CheckpointWriterFence < 1 ||
		plan.CheckpointWriterFence > plan.SourceWriterFence ||
		plan.SourceControlFence < 1 ||
		!platformValidUUID(plan.SourceActivationID) ||
		plan.SourceActivationGeneration != plan.SourceGeneration ||
		!platformValidSHA(plan.GrantsSHA256) ||
		!platformValidSHA(plan.AudienceSHA256) ||
		!platformValidSHA(plan.CheckpointManifestSHA256) &&
			plan.CheckpointManifestSHA256 != "" ||
		plan.TrustRevision != trustRevision {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(plan)
	if err != nil || string(canonical) != raw {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zeroPlan, zeroSource, zeroRuntime, err
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	clear(seed)
	if err != nil {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformRuntimeConflict
	}
	source, err := loadPlatformCheckpointSourceForEnable(
		e, lease, runtime, now)
	if err != nil {
		return zeroPlan, zeroSource, zeroRuntime, err
	}
	actualWriterFence := source.currentWriterFence
	if actualWriterFence == "" {
		actualWriterFence = source.writerFence
	}
	if source.sourceGeneration != plan.SourceGeneration ||
		source.releaseSHA256 != plan.SourceReleaseSHA256 ||
		source.namespaceID != plan.DataNamespaceID ||
		mustPlatformCheckpointInt(source.dataFormat) != plan.DataFormat ||
		mustPlatformCheckpointInt(actualWriterFence) !=
			plan.SourceWriterFence ||
		mustPlatformCheckpointInt(source.writerFence) !=
			plan.CheckpointWriterFence ||
		source.appID != plan.AppID || source.publisherID != plan.PublisherID ||
		!samePlatformManagedUpdateRuntime(
			plan.TargetRuntime, source.runtimeTarget) {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(
		e, plan.TargetReleaseSHA256, trustRevision, trust, now)
	if err != nil || verified.appID != plan.AppID ||
		verified.publisherID != plan.PublisherID {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformPlanChanged
	}
	grants, err := platformRemovalGrantsDigest(
		e, lease.installationID)
	if err != nil || grants != plan.GrantsSHA256 {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	audience, err := platformInstallationAudienceSHA(
		e, lease.installationID)
	if err != nil || audience != plan.AudienceSHA256 {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	var currentInstallationRevision int64
	if err := e.queryRow(`SELECT revision FROM app_platform_installations
		WHERE id=?`, lease.installationID).Scan(&currentInstallationRevision); err != nil ||
		currentInstallationRevision < installationRevision+1 {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	hasRecovery := plan.RecoverySetID != ""
	hasCheckpointEffect := plan.CheckpointOperationID != "" ||
		plan.CheckpointEffectID != ""
	if hasRecovery != hasCheckpointEffect ||
		(hasRecovery && (plan.CheckpointOperationID != lease.id ||
			!platformValidUUID(plan.RecoverySetID) ||
			!platformValidUUID(plan.CheckpointEffectID) ||
			!platformValidUUID(plan.CheckpointID) ||
			plan.CheckpointArchiveBytes <= 0)) ||
		(!hasRecovery && lease.snapshot.Stage != appplatform.StageQueued &&
			lease.snapshot.Stage != appplatform.StageValidate &&
			lease.snapshot.Stage != appplatform.StageDownload &&
			lease.snapshot.Stage != appplatform.StageStage &&
			lease.snapshot.Stage != platformSameReleaseCheckpointStage(
				lease.snapshot.Action)) {
		return zeroPlan, zeroSource, zeroRuntime, errAppPlatformStageEvidence
	}
	return plan, source, runtime, nil
}

func (s *Server) advancePlatformManagedEnableStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	nextByStage := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageQueued:   appplatform.StageValidate,
		appplatform.StageValidate: appplatform.StageSnapshot,
	}
	if expected.snapshot.Action == appplatform.ActionRepair {
		nextByStage = map[appplatform.Stage]appplatform.Stage{
			appplatform.StageQueued:   appplatform.StageValidate,
			appplatform.StageValidate: appplatform.StageDownload,
			appplatform.StageDownload: appplatform.StageStage,
			appplatform.StageStage:    appplatform.StageQuiesce,
		}
	}
	next, ok := nextByStage[expected.snapshot.Stage]
	if !ok || !platformSameReleaseLifecycle(expected.snapshot.Action) ||
		!expected.snapshot.Managed || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
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
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if _, _, _, err := s.loadPlatformAdmittedManagedEnable(
		e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision),
		now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.operation_stage',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
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

func (s *Server) claimNextPlatformManagedEnable(ctx context.Context, worker string) (platformInstallSelection, error) {
	return s.claimNextPlatformManagedSameRelease(
		ctx, worker, appplatform.ActionEnable)
}

func (s *Server) claimNextPlatformManagedSameRelease(
	ctx context.Context,
	worker string,
	action appplatform.Action,
) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if !platformValidUUID(worker) ||
		!platformSameReleaseLifecycle(action) {
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
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).
		Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).
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
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action=? AND o.managed=1 AND o.completed_ms IS NULL
		AND o.stage IN ('queued','validate','download','stage','quiesce',
			'snapshot','start','probe','activate')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.desired_state='enabled' AND i.phase='pending'
		AND i.runtime_id IS NOT NULL AND i.generation=o.target_generation
		AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.action=o.action AND p.controller_epoch=o.controller_epoch
		ORDER BY o.created_ms,o.id LIMIT 32`,
		string(action), now, epoch, epoch)
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
			facts.lease.snapshot.Action != action ||
			!facts.lease.snapshot.Managed {
			continue
		}
		if _, _, _, err := s.loadPlatformAdmittedManagedEnable(
			e, facts.lease, trust, facts.now); err != nil {
			return zero, err
		}
		lease, err := writePlatformOperationLease(e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		audit := "app_platform.managed_enable_claimed"
		if action == appplatform.ActionRepair {
			audit = "app_platform.managed_repair_claimed"
		}
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,?,'app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
			audit, id, string(detail)); err != nil {
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

func (s *Server) runPlatformManagedEnable(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformManagedSameRelease(ctx, expected, endpoint)
}

func (s *Server) runPlatformManagedSameRelease(
	ctx context.Context,
	expected platformOperationLease,
	endpoint platformRuntimeEndpoint,
) (platformOperationLease, error) {
	if !platformSameReleaseLifecycle(expected.snapshot.Action) {
		return expected, errAppPlatformStageEvidence
	}
	result := expected
	for result.snapshot.Stage != appplatform.StageComplete {
		var next platformOperationLease
		var err error
		switch result.snapshot.Stage {
		case appplatform.StageQueued, appplatform.StageValidate,
			appplatform.StageDownload, appplatform.StageStage:
			next, err = s.advancePlatformManagedEnableStage(ctx, result)
		case appplatform.StageSnapshot:
			next, err = s.dispatchPlatformCheckpoint(ctx, result, endpoint)
		case appplatform.StageQuiesce:
			next, err = s.dispatchPlatformCheckpoint(ctx, result, endpoint)
		case appplatform.StageStart:
			next, err = s.executePlatformUpdateStart(ctx, result, endpoint)
		case appplatform.StageProbe:
			next, err = s.probePlatformUpdateStart(ctx, result, endpoint)
		case appplatform.StageActivate:
			registered, err := s.dispatchPlatformUpdateStartRegistration(
				ctx, result, endpoint)
			if err != nil {
				return result, err
			}
			confirmed, err := s.completePlatformUpdateStartRegistration(
				ctx, registered, endpoint)
			if err != nil {
				return confirmed, err
			}
			if _, err := s.activatePlatformUpdateStart(
				ctx, confirmed, endpoint); err != nil {
				return confirmed, err
			}
			return platformOperationLease{}, nil
		default:
			return result, errAppPlatformStageEvidence
		}
		if err != nil {
			return result, err
		}
		result = next
	}
	return result, nil
}

func (s *Server) finishPlatformManagedEnableForReview(ctx context.Context, expected platformOperationLease) error {
	return s.finishPlatformManagedSameReleaseForReview(ctx, expected)
}

func (s *Server) finishPlatformManagedSameReleaseForReview(
	ctx context.Context, expected platformOperationLease,
) error {
	if !platformSameReleaseLifecycle(expected.snapshot.Action) ||
		!expected.snapshot.Managed {
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
	plan, _, _, err := s.loadPlatformAdmittedManagedEnable(
		e, expected, appplatform.PublisherTrust{}, facts.now)
	if err != nil {
		// A failed trust refresh must not strand the deny-only cleanup.
		plan, err = platformLoadManagedEnableFailurePlan(e, expected)
		if err != nil {
			return err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	outcome := appplatform.EffectsUnknown
	errorCode := "managed_enable_recovery_required"
	audit := "app_platform.managed_enable_failed"
	if expected.snapshot.Action == appplatform.ActionRepair {
		errorCode = "managed_repair_recovery_required"
		audit = "app_platform.managed_repair_failed"
	}
	if expected.snapshot.Stage == appplatform.StageQueued ||
		expected.snapshot.Stage == appplatform.StageValidate {
		outcome = appplatform.EffectsNone
		errorCode = "managed_enable_unavailable"
		if expected.snapshot.Action == appplatform.ActionRepair {
			errorCode = "managed_repair_unavailable"
		}
	}
	failed, err := model.Fail(expected.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	phase := "disabled"
	desired := "disabled"
	if failed.Snapshot().Stage == appplatform.StageRecoveryRequired {
		phase = "recovery_required"
	}
	changed, err := e.exec(`UPDATE app_platform_installations
		SET desired_state=?,phase=?,generation=?,revision=revision+1,
		updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='pending'
		AND generation=? AND revision=? AND policy_revision=?
		AND control_fence=? AND controller_epoch=? AND runtime_id IS NOT NULL`,
		desired, phase, plan.SourceGeneration, now, plan.InstallationID,
		plan.TargetGeneration, facts.installationRevision,
		plan.TargetPolicyRevision, expected.fence, plan.ControllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,
		error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,
		lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision),
		errorCode, now, now, expected.id, expected.installationID,
		expected.controllerEpoch, string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,?,'app_platform_operation',?,'{}','')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		audit, expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

func platformLoadManagedEnableFailurePlan(e migExec, lease platformOperationLease) (platformManagedUpdatePlan, error) {
	var zero platformManagedUpdatePlan
	var raw, digest, action string
	var managed int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.action,
		o.managed,p.consumed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`, lease.id).Scan(&raw, &digest, &action, &managed,
		&consumed); err != nil {
		return zero, err
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(raw), &plan) != nil ||
		len(raw) > 65536 || platformDigest([]byte(raw)) != digest ||
		!platformSameReleaseLifecycle(lease.snapshot.Action) ||
		action != string(lease.snapshot.Action) || managed != 1 ||
		!consumed.Valid ||
		plan.Format != platformSameReleaseLifecycleFormat(
			lease.snapshot.Action) ||
		plan.Action != lease.snapshot.Action ||
		plan.InstallationID != lease.installationID {
		return zero, errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(plan)
	if err != nil || string(canonical) != raw {
		return zero, errAppPlatformStageEvidence
	}
	return plan, nil
}
