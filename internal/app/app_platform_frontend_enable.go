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

const platformFrontendEnableFormat = "portal.frontend-enable-review/1-dev.1"

type platformFrontendEnablePlan struct {
	Format                     string             `json:"format"`
	InstallationID             string             `json:"installation_id"`
	AppID                      string             `json:"app_id"`
	PublisherID                string             `json:"publisher_id"`
	Action                     appplatform.Action `json:"action"`
	SourceActivationID         string             `json:"source_activation_id"`
	SourceActivationGeneration int64              `json:"source_activation_generation"`
	SourceGeneration           int64              `json:"source_generation"`
	TargetGeneration           int64              `json:"target_generation"`
	SourceReleaseSHA256        string             `json:"source_release_sha256"`
	SourcePolicyRevision       int64              `json:"source_policy_revision"`
	TargetPolicyRevision       int64              `json:"target_policy_revision"`
	ControllerEpoch            string             `json:"controller_epoch"`
	InstallationRevision       int64              `json:"installation_revision"`
	SourceControlFence         int64              `json:"source_control_fence"`
	TrustRevision              int64              `json:"trust_revision"`
	ActorAuthority             string             `json:"actor_authority"`
	GrantsSHA256               string             `json:"grants_sha256"`
	FrontendSHA256             string             `json:"frontend_sha256"`
	DocumentSHA256             string             `json:"document_sha256"`
	AudienceSHA256             string             `json:"audience_sha256"`
	DeclaredDataSchema         int64              `json:"declared_data_schema"`
	SourceDesiredState         string             `json:"source_desired_state"`
	SourcePhase                string             `json:"source_phase"`
	TargetDesiredState         string             `json:"target_desired_state"`
	TargetPhase                string             `json:"target_phase"`
	SourceRuntimeRequired      bool               `json:"source_runtime_required"`
	DataEffect                 string             `json:"data_effect"`
}

func platformLoadFrontendEnablePlan(e migExec, installation string, action appplatform.Action, actor platformActorFacts, control platformActorControl) (platformFrontendEnablePlan, error) {
	var p platformFrontendEnablePlan
	if !platformValidUUID(installation) || action != appplatform.ActionEnable {
		return p, errAppPlatformPlanChanged
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var runtime sql.NullString
	var fence int64
	if err := e.queryRow(`SELECT app_id,publisher_id,controller_epoch,generation,revision,policy_revision,desired_state,phase,runtime_id,control_fence
		FROM app_platform_installations WHERE id=?`+lock, installation).
		Scan(&p.AppID, &p.PublisherID, &p.ControllerEpoch, &p.SourceGeneration,
			&p.InstallationRevision, &p.SourcePolicyRevision, &p.SourceDesiredState,
			&p.SourcePhase, &runtime, &fence); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, errAppPlatformPlanChanged
		}
		return p, err
	}
	if runtime.Valid || p.ControllerEpoch != control.epoch ||
		p.SourceDesiredState != "disabled" || p.SourcePhase != "disabled" ||
		p.SourceGeneration < 1 || p.SourceGeneration == math.MaxInt64 ||
		p.InstallationRevision == math.MaxInt64 || p.SourcePolicyRevision < 1 ||
		p.SourcePolicyRevision == math.MaxInt64 || fence == math.MaxInt64 {
		return p, fmt.Errorf("frontend enable installation state: %w",
			errAppPlatformPlanChanged)
	}
	p.SourceControlFence = fence
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, installation).Scan(&pending); err != nil {
		return p, err
	}
	if pending != 0 {
		return p, errAppPlatformOperationConflict
	}
	var active, retired, unsafeData int
	if err := e.queryRow(`SELECT
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'),
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='retired'),
		(SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?)`,
		installation, installation, installation, installation).
		Scan(&active, &retired, &unsafeData); err != nil {
		return p, err
	}
	if active != 0 || retired < 1 || unsafeData != 0 {
		return p, fmt.Errorf("frontend enable resources active=%d retired=%d data=%d: %w",
			active, retired, unsafeData, errAppPlatformPlanChanged)
	}
	var namespace sql.NullString
	var activationObserved sql.NullInt64
	var activationPolicy int64
	var activationDataFormat int64
	if err := e.queryRow(`SELECT id,generation,release_sha256,policy_revision,
		data_namespace_id,data_format,observed_ms
		FROM app_platform_activations
		WHERE installation_id=? AND state='retired'
		AND generation=(
			SELECT MAX(generation) FROM app_platform_activations
			WHERE installation_id=? AND state='retired'
		)`, installation, installation).
		Scan(&p.SourceActivationID, &p.SourceActivationGeneration,
			&p.SourceReleaseSHA256, &activationPolicy,
			&namespace, &activationDataFormat, &activationObserved); err != nil {
		return p, err
	}
	if namespace.Valid || !activationObserved.Valid || activationObserved.Int64 < 0 ||
		activationDataFormat != 0 || !platformValidSHA(p.SourceReleaseSHA256) ||
		!platformValidUUID(p.SourceActivationID) ||
		p.SourceActivationGeneration < 1 ||
		p.SourceActivationGeneration >= p.SourceGeneration ||
		activationPolicy != p.SourcePolicyRevision {
		return p, fmt.Errorf("frontend enable source activation: %w",
			errAppPlatformPlanChanged)
	}
	if err := e.queryRow(`SELECT revision FROM app_platform_publishers WHERE id=?`, p.PublisherID).Scan(&p.TrustRevision); err != nil {
		return p, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return p, err
	}
	verified, err := platformVerifyStoredRelease(
		e, p.SourceReleaseSHA256, p.TrustRevision, trust, control.nowMS)
	if err != nil || verified.appID != p.AppID ||
		verified.publisherID != p.PublisherID {
		return p, fmt.Errorf("frontend enable release trust: %v: %w",
			err, errAppPlatformPlanChanged)
	}
	descriptor, err := platformReleaseDescriptor(e, p.SourceReleaseSHA256)
	if err != nil {
		return p, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	compatibility, compatibilityErr := appplatform.EvaluateCompatibility(
		parsed.Manifest,
		appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI})
	if err != nil || parsed.Manifest.Backend != nil ||
		parsed.Manifest.Data.WritesOnStartup ||
		compatibilityErr != nil || !compatibility.Compatible {
		return p, fmt.Errorf("frontend enable descriptor parse=%v compatibility=%v issues=%v writes=%t backend=%t: %w",
			err, compatibilityErr, compatibility.Issues,
			parsed.Manifest.Data.WritesOnStartup,
			parsed.Manifest.Backend != nil, errAppPlatformPlanChanged)
	}
	p.DeclaredDataSchema = int64(parsed.Manifest.Data.Schema)
	var archive []byte
	if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`,
		parsed.Manifest.Frontend.SHA256).Scan(&archive); err != nil {
		return p, err
	}
	document, err := appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	if err != nil {
		return p, err
	}
	p.FrontendSHA256, p.DocumentSHA256 =
		document.SourceSHA256, document.DocumentSHA256
	p.AudienceSHA256, err = platformInstallationAudienceSHA(e, installation)
	if err != nil {
		return p, fmt.Errorf("frontend enable audience: %w", err)
	}
	p.GrantsSHA256, err = platformRemovalGrantsDigest(e, installation)
	if err != nil {
		return p, fmt.Errorf("frontend enable grants: %w", err)
	}
	p.Format = platformFrontendEnableFormat
	p.InstallationID, p.Action = installation, action
	p.TargetGeneration, p.TargetPolicyRevision = p.SourceGeneration+1, p.SourcePolicyRevision
	p.ActorAuthority = actor.authorityFingerprint
	p.SourceRuntimeRequired = false
	p.TargetDesiredState, p.TargetPhase = "enabled", "pending"
	p.DataEffect = "preserve"
	return p, nil
}

func (s *Server) createPlatformFrontendEnableReviewContext(ctx context.Context, session platformActorSession, installation string) (platformRemovalReview, error) {
	var result platformRemovalReview
	err := s.withPlatformTrustActorContext(ctx, session, func(e migExec, actor platformActorFacts, control platformActorControl, _ appplatform.PublisherTrust) error {
		p, err := platformLoadFrontendEnablePlan(
			e, installation, appplatform.ActionEnable, actor, control)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(p)
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
			id, installation, p.AppID, p.PublisherID, string(p.Action),
			p.SourceReleaseSHA256, p.SourceReleaseSHA256, p.InstallationRevision,
			p.SourcePolicyRevision, p.TrustRevision, p.ControllerEpoch, actor.scope,
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

func (s *Server) consumePlatformFrontendEnableReviewContext(ctx context.Context, session platformActorSession, planID, planSHA, key string) (platformRemovalCommit, error) {
	var result platformRemovalCommit
	if !platformValidUUID(planID) || !platformValidUUID(key) ||
		!platformValidSHA(planSHA) {
		return result, errAppPlatformPlanChanged
	}
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
		var raw, digest, storedScope, actionText, created string
		var expires, accountGeneration int64
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
			appplatform.Action(actionText) != appplatform.ActionEnable {
			return errAppPlatformPlanChanged
		}
		p, err := platformLoadFrontendEnablePlan(
			e, installation.String, appplatform.ActionEnable, actor, control)
		if err != nil {
			return err
		}
		control, err = platformRefreshActor(e, session, actor, control)
		if err != nil {
			return err
		}
		if expires <= control.nowMS {
			return errAppPlatformPlanExpired
		}
		if !username.Valid || username.String != actor.username ||
			created != actor.created ||
			accountGeneration != actor.generation ||
			platformDigest([]byte(raw)) != digest ||
			digest != planSHA || len(raw) > 65536 {
			return errAppPlatformPlanChanged
		}
		current, err := json.Marshal(p)
		if err != nil || string(current) != raw {
			return errAppPlatformPlanChanged
		}
		if _, err := platformVerifyStoredRelease(
			e, p.SourceReleaseSHA256, p.TrustRevision, trust,
			control.nowMS); err != nil {
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
		changed, err = e.exec(`UPDATE app_platform_installations
			SET desired_state='enabled',phase='pending',generation=?,
			revision=revision+1,control_fence=control_fence+1,updated_ms=?
			WHERE id=? AND desired_state='disabled' AND phase='disabled'
			AND generation=? AND revision=? AND policy_revision=?
			AND control_fence=? AND controller_epoch=? AND runtime_id IS NULL`,
			p.TargetGeneration, control.nowMS, p.InstallationID,
			p.SourceGeneration, p.InstallationRevision,
			p.SourcePolicyRevision, p.SourceControlFence,
			p.ControllerEpoch)
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
			VALUES(?,?,?,?,?,?,?,0,?,?,'queued',1,1,NULL,NULL,0,?,
			NULL,NULL,?,?)`,
			id, p.InstallationID, planID, actor.scope, keyDigest, requestDigest,
			string(p.Action), p.SourceGeneration, p.TargetGeneration,
			p.ControllerEpoch, control.nowMS, control.nowMS)
		if err != nil {
			return err
		}
		detail, _ := json.Marshal(struct {
			Plan       string `json:"plan_id"`
			Action     string `json:"action"`
			Generation int64  `json:"generation"`
		}{planID, string(p.Action), p.TargetGeneration})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,?,?,'app_platform.operation_admitted',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(control.nowMS).UTC().Format(time.RFC3339),
			actor.username, actor.ouID, id, string(detail)); err != nil {
			return err
		}
		result = platformRemovalCommit{
			OperationID: id, InstallationID: p.InstallationID,
			Generation: p.TargetGeneration, Revision: 1,
			Stage: appplatform.StageQueued,
		}
		return nil
	})
	if err != nil {
		return platformRemovalCommit{}, err
	}
	return result, nil
}

func platformLoadAdmittedFrontendEnable(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) (platformFrontendEnablePlan, error) {
	var zero platformFrontendEnablePlan
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, operationScope, planScope, actionText, source, target, epoch, authority string
	var sourceGeneration, targetGeneration, managed, installRevision, policyRevision, trustRevision int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.actor_scope,p.actor_scope,
		p.action,p.source_release_sha256,p.target_release_sha256,p.controller_epoch,
		p.authority_fingerprint,o.source_generation,o.target_generation,o.managed,
		p.installation_revision,p.policy_revision,p.trust_revision,p.consumed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`+lock, lease.id).Scan(&raw, &digest, &operationScope,
		&planScope, &actionText, &source, &target, &epoch, &authority,
		&sourceGeneration, &targetGeneration, &managed, &installRevision,
		&policyRevision, &trustRevision, &consumed); err != nil {
		return zero, err
	}
	var p platformFrontendEnablePlan
	if json.Unmarshal([]byte(raw), &p) != nil ||
		platformDigest([]byte(raw)) != digest ||
		p.Format != platformFrontendEnableFormat ||
		appplatform.Action(actionText) != appplatform.ActionEnable ||
		lease.snapshot.Action != appplatform.ActionEnable ||
		lease.snapshot.Managed || managed != 0 ||
		operationScope != planScope {
		return zero, fmt.Errorf("frontend enable review tuple: %w",
			errAppPlatformStageEvidence)
	}
	if p.InstallationID != lease.installationID ||
		p.Action != appplatform.ActionEnable ||
		p.SourceGeneration != sourceGeneration ||
		!platformValidUUID(p.SourceActivationID) ||
		p.SourceActivationGeneration < 1 ||
		p.SourceActivationGeneration >= sourceGeneration ||
		p.TargetGeneration != targetGeneration ||
		uint64(targetGeneration) != lease.snapshot.Generation ||
		sourceGeneration < 1 || targetGeneration != sourceGeneration+1 {
		return zero, fmt.Errorf("frontend enable generation tuple: %w",
			errAppPlatformStageEvidence)
	}
	if p.SourceReleaseSHA256 != source ||
		p.SourceReleaseSHA256 != target ||
		p.SourcePolicyRevision != policyRevision ||
		p.TargetPolicyRevision != policyRevision ||
		p.ControllerEpoch != epoch || epoch != lease.controllerEpoch ||
		p.ActorAuthority != authority ||
		p.InstallationRevision != installRevision ||
		p.TrustRevision != trustRevision {
		return zero, fmt.Errorf("frontend enable release tuple: %w",
			errAppPlatformStageEvidence)
	}
	if !platformValidSHA(p.GrantsSHA256) ||
		!platformValidSHA(p.AudienceSHA256) ||
		!platformValidSHA(p.FrontendSHA256) ||
		!platformValidSHA(p.DocumentSHA256) ||
		p.SourceControlFence < 1 ||
		!consumed.Valid || consumed.Int64 > now {
		return zero, fmt.Errorf("frontend enable evidence tuple: %w",
			errAppPlatformStageEvidence)
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw {
		return zero, errAppPlatformStageEvidence
	}
	var appID, publisher, desired, phase, currentEpoch string
	var runtime sql.NullString
	var generation, revision, policy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,
		generation,revision,policy_revision,controller_epoch,runtime_id
		FROM app_platform_installations WHERE id=?`+lock,
		lease.installationID).
		Scan(&appID, &publisher, &desired, &phase, &generation, &revision,
			&policy, &currentEpoch, &runtime); err != nil {
		return zero, err
	}
	if appID != p.AppID || publisher != p.PublisherID ||
		desired != "enabled" || phase != "pending" || runtime.Valid ||
		generation != p.TargetGeneration ||
		revision < p.InstallationRevision+1 ||
		policy != p.TargetPolicyRevision ||
		currentEpoch != p.ControllerEpoch {
		return zero, errAppPlatformStageEvidence
	}
	var active, retired, unsafeData, pending int
	if err := e.queryRow(`SELECT
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'),
		(SELECT COUNT(*) FROM app_platform_activations WHERE id=? AND installation_id=? AND generation=? AND state='retired' AND release_sha256=? AND policy_revision=? AND data_namespace_id IS NULL AND data_format=0),
		(SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?),
		(SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL)`,
		lease.installationID, p.SourceActivationID, lease.installationID,
		p.SourceActivationGeneration, p.SourceReleaseSHA256, p.SourcePolicyRevision,
		lease.installationID, lease.installationID,
		lease.installationID).Scan(&active, &retired, &unsafeData, &pending); err != nil {
		return zero, err
	}
	if active != 0 || retired != 1 || unsafeData != 0 || pending != 1 {
		return zero, errAppPlatformStageEvidence
	}
	grants, err := platformRemovalGrantsDigest(e, lease.installationID)
	if err != nil || grants != p.GrantsSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	audience, err := platformInstallationAudienceSHA(e, lease.installationID)
	if err != nil || audience != p.AudienceSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(
		e, p.SourceReleaseSHA256, p.TrustRevision, trust, now)
	if err != nil || verified.appID != p.AppID ||
		verified.publisherID != p.PublisherID {
		return zero, errAppPlatformPlanChanged
	}
	descriptor, err := platformReleaseDescriptor(e, p.SourceReleaseSHA256)
	if err != nil {
		return zero, err
	}
	parsed, err := appplatform.ParseDescriptor(descriptor)
	compatibility, compatibilityErr := appplatform.EvaluateCompatibility(
		parsed.Manifest,
		appplatform.CompatibilityTarget{PlatformAPI: platformDevelopmentAPI})
	if err != nil || parsed.Manifest.Backend != nil ||
		int64(parsed.Manifest.Data.Schema) != p.DeclaredDataSchema ||
		parsed.Manifest.Data.WritesOnStartup ||
		compatibilityErr != nil || !compatibility.Compatible {
		return zero, errAppPlatformStageEvidence
	}
	var archive []byte
	if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`,
		parsed.Manifest.Frontend.SHA256).Scan(&archive); err != nil {
		return zero, err
	}
	document, err := appplatform.BuildFrontendDocument(parsed.Manifest, archive)
	if err != nil || document.SourceSHA256 != p.FrontendSHA256 ||
		document.DocumentSHA256 != p.DocumentSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	return p, nil
}

func (s *Store) advancePlatformFrontendEnable(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionEnable ||
		expected.snapshot.Managed ||
		(expected.snapshot.Stage != appplatform.StageQueued &&
			expected.snapshot.Stage != appplatform.StageValidate) {
		return zero, errAppPlatformStageEvidence
	}
	next := appplatform.StageValidate
	if expected.snapshot.Stage == appplatform.StageValidate {
		next = appplatform.StageActivate
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: platformContextTx{Tx: tx, ctx: ctx}}
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
	if _, err := platformLoadAdmittedFrontendEnable(
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

func (s *Store) activatePlatformFrontendEnable(ctx context.Context, expected platformOperationLease) (platformActivationResult, error) {
	var zero platformActivationResult
	if expected.snapshot.Action != appplatform.ActionEnable ||
		expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageActivate ||
		expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: platformContextTx{Tx: tx, ctx: ctx}}
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
	plan, err := platformLoadAdmittedFrontendEnable(
		e, expected, trust, facts.now)
	if err != nil {
		return zero, err
	}
	var planSHA string
	if err := e.queryRow(`SELECT p.plan_sha256
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`, expected.id).Scan(&planSHA); err != nil {
		return zero, err
	}
	evidence := platformFrontendEvidence(platformStaticCandidate{
		frontendOnly: true, reviewSHA: planSHA,
		declaredSchema: int(plan.DeclaredDataSchema),
		releaseSHA:     plan.SourceReleaseSHA256, frontendSHA: plan.FrontendSHA256,
		documentSHA: plan.DocumentSHA256,
	}, plan.ControllerEpoch, plan.InstallationID, plan.AudienceSHA256,
		plan.TargetGeneration, plan.TargetPolicyRevision)
	if len(evidence) > 8192 {
		return zero, errAppPlatformStageEvidence
	}
	activationID, err := platformNewUUID()
	if err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_activations(
		id,installation_id,release_sha256,generation,policy_revision,
		data_namespace_id,data_format,state,readiness_evidence,observed_ms,
		created_ms,updated_ms)
		VALUES(?,?,?,?,?,NULL,0,'active',?,?,?,?)`,
		activationID, plan.InstallationID, plan.SourceReleaseSHA256,
		plan.TargetGeneration, plan.TargetPolicyRevision, string(evidence),
		now, now, now); err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_installations
		SET phase='ready',revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='pending'
		AND generation=? AND revision=? AND policy_revision=?
		AND control_fence=? AND controller_epoch=? AND runtime_id IS NULL`,
		now, plan.InstallationID, plan.TargetGeneration,
		facts.installationRevision, plan.TargetPolicyRevision,
		expected.fence, plan.ControllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(
		expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage='complete',state_revision=?,row_revision=row_revision+1,
		completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage='activate' AND state_revision=? AND row_revision=?
		AND fence=? AND lease_owner=? AND lease_until_ms=?
		AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(next.Snapshot().Revision), now, now, expected.id,
		expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ActivationID string `json:"activation_id"`
		Generation   int64  `json:"generation"`
		Policy       int64  `json:"policy_revision"`
	}{activationID, plan.TargetGeneration, plan.TargetPolicyRevision})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.frontend_enabled',
		'app_platform_installation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.installationID, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return platformActivationResult{
		activationID: activationID, installationID: expected.installationID,
		snapshot: next.Snapshot(),
	}, nil
}

func (s *Server) claimNextPlatformFrontendEnable(ctx context.Context, worker string) (platformInstallSelection, error) {
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
		WHERE o.action='enable' AND o.managed=0 AND o.completed_ms IS NULL
		AND o.stage IN ('queued','validate','activate')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.desired_state='enabled' AND i.phase='pending'
		AND i.runtime_id IS NULL AND i.generation=o.target_generation
		AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.action=o.action AND p.controller_epoch=o.controller_epoch
		AND NOT EXISTS(SELECT 1 FROM app_platform_activations a
			WHERE a.installation_id=i.id AND a.state='active')
		ORDER BY o.created_ms,o.id LIMIT 32`, now, epoch, epoch)
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
			facts.lease.snapshot.Action != appplatform.ActionEnable ||
			facts.lease.snapshot.Managed {
			continue
		}
		if _, err := platformLoadAdmittedFrontendEnable(
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
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.enable_claimed',
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

func (s *Server) runPlatformFrontendEnable(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	result := expected
	for result.snapshot.Stage != appplatform.StageActivate {
		next, err := s.st.advancePlatformFrontendEnable(ctx, result)
		if err != nil {
			return result, err
		}
		result = next
	}
	_, err := s.st.activatePlatformFrontendEnable(ctx, result)
	return platformOperationLease{}, err
}

func platformLoadFrontendEnableFailurePlan(e migExec, lease platformOperationLease) (platformFrontendEnablePlan, error) {
	var zero platformFrontendEnablePlan
	var raw, digest, operationScope, planScope, action string
	var managed, sourceGeneration, targetGeneration int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256,o.actor_scope,
		p.actor_scope,o.action,o.managed,o.source_generation,o.target_generation,
		p.consumed_ms
		FROM app_platform_operations o
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=?`, lease.id).Scan(&raw, &digest, &operationScope,
		&planScope, &action, &managed, &sourceGeneration, &targetGeneration,
		&consumed); err != nil {
		return zero, err
	}
	var p platformFrontendEnablePlan
	if json.Unmarshal([]byte(raw), &p) != nil ||
		platformDigest([]byte(raw)) != digest ||
		operationScope != planScope ||
		action != "enable" || managed != 0 ||
		p.Format != platformFrontendEnableFormat ||
		p.Action != appplatform.ActionEnable ||
		p.InstallationID != lease.installationID ||
		lease.snapshot.Action != appplatform.ActionEnable ||
		lease.snapshot.Managed ||
		p.SourceGeneration != sourceGeneration ||
		p.TargetGeneration != targetGeneration ||
		targetGeneration != sourceGeneration+1 ||
		!consumed.Valid {
		return zero, errAppPlatformStageEvidence
	}
	canonical, err := json.Marshal(p)
	if err != nil || string(canonical) != raw {
		return zero, errAppPlatformStageEvidence
	}
	return p, nil
}

func (s *Server) finishPlatformFrontendEnableForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionEnable ||
		expected.snapshot.Managed {
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
	plan, err := platformLoadFrontendEnableFailurePlan(e, expected)
	if err != nil {
		return err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	failed, err := model.Fail(
		expected.snapshot.Revision, appplatform.EffectsNone)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_installations
		SET desired_state='disabled',phase='disabled',generation=?,
		revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled' AND phase='pending'
		AND generation=? AND revision=? AND policy_revision=?
		AND control_fence=? AND controller_epoch=? AND runtime_id IS NULL`,
		plan.SourceGeneration, now, plan.InstallationID, plan.TargetGeneration,
		facts.installationRevision, plan.TargetPolicyRevision,
		expected.fence, plan.ControllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,
		error_code='frontend_enable_retry_exhausted',completed_ms=?,
		updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision),
		now, now, expected.id, expected.installationID,
		expected.controllerEpoch, string(expected.snapshot.Stage),
		int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.frontend_enable_failed',
		'app_platform_operation',?,'{}','')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
