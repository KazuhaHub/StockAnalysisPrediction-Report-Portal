package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformAdmittedUpdate struct {
	planID                      string
	plan                        platformUpdateProjection
	verified                    platformStagedRelease
	currentInstallationRevision int64
}

func (s *Server) claimNextPlatformFrontendUpdate(ctx context.Context, worker string) (platformInstallSelection, error) {
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
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.action='update' AND o.managed=0 AND o.completed_ms IS NULL AND o.stage IN ('queued','validate','download','stage','quiesce','snapshot','migrate','activate') AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.phase='ready' AND i.desired_state='enabled' AND i.runtime_id IS NULL AND i.generation=o.source_generation AND o.target_generation=i.generation+1 AND i.controller_epoch=? AND o.controller_epoch=? AND p.controller_epoch=o.controller_epoch AND p.plan_json LIKE ? ORDER BY o.created_ms,o.id LIMIT 32`, now, epoch, epoch, `{"format":"`+platformUpdateReviewFormat+`",%`)
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
		if facts.lease.leaseUntilMS > facts.now || facts.lease.snapshot.Action != appplatform.ActionUpdate || facts.lease.snapshot.Managed || facts.lease.snapshot.Stage == appplatform.StageComplete || facts.lease.snapshot.Stage == appplatform.StageFailed || facts.lease.snapshot.Stage == appplatform.StageRecoveryRequired {
			continue
		}
		admitted, err := loadPlatformAdmittedUpdate(e, facts.lease, trust, facts.now)
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
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_claimed','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if admitted.plan.InstallationID != lease.installationID {
			return zero, errAppPlatformStageEvidence
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

func loadPlatformAdmittedUpdate(e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) (platformAdmittedUpdate, error) {
	var result platformAdmittedUpdate
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var raw, digest, operationScope, planScope, username, created, authority, epoch, action, source, target string
	var sourceGeneration, targetGeneration, managed, expires, installationRevision, policy, trustRevision, accountGeneration int64
	var consumed sql.NullInt64
	if err := e.queryRow(`SELECT p.id,p.plan_json,p.plan_sha256,o.actor_scope,p.username,p.account_created,p.account_generation,p.authority_fingerprint,p.actor_scope,p.expires_ms,p.consumed_ms,p.installation_revision,p.policy_revision,p.trust_revision,p.controller_epoch,p.source_release_sha256,p.target_release_sha256,p.action,o.source_generation,o.target_generation,o.managed
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`+lock, lease.id).
		Scan(&result.planID, &raw, &digest, &operationScope, &username, &created, &accountGeneration, &authority, &planScope, &expires, &consumed, &installationRevision, &policy, &trustRevision, &epoch, &source, &target, &action, &sourceGeneration, &targetGeneration, &managed); err != nil {
		return result, err
	}
	if action != "update" || lease.snapshot.Action != appplatform.ActionUpdate || lease.snapshot.Managed || managed != 0 || operationScope != planScope || sourceGeneration < 1 || uint64(sourceGeneration) != lease.snapshot.Generation-1 || uint64(targetGeneration) != lease.snapshot.Generation || targetGeneration != sourceGeneration+1 || !consumed.Valid || consumed.Int64 > now || expires <= now || epoch != lease.controllerEpoch || platformDigest([]byte(raw)) != digest {
		return result, errAppPlatformStageEvidence
	}
	if err := json.Unmarshal([]byte(raw), &result.plan); err != nil || result.plan.Format != platformUpdateReviewFormat || result.plan.SourceReleaseSHA256 != source || result.plan.Target.ReleaseSHA256 != target {
		return result, errAppPlatformStageEvidence
	}
	if result.plan.InstallationRevision != installationRevision || result.plan.PolicyRevision != policy || result.plan.TrustRevision != trustRevision || result.plan.ActorAuthority != authority || result.plan.ControllerEpoch != epoch {
		return result, errAppPlatformStageEvidence
	}
	canonical, _ := json.Marshal(result.plan)
	if string(canonical) != raw {
		return result, errAppPlatformStageEvidence
	}
	var currentApp, currentPublisher, desired, phase, currentEpoch string
	var currentRuntime sql.NullString
	var generation, currentRevision, currentPolicy int64
	if err := e.queryRow(`SELECT app_id,publisher_id,desired_state,phase,generation,revision,policy_revision,controller_epoch,runtime_id FROM app_platform_installations WHERE id=?`, lease.installationID).
		Scan(&currentApp, &currentPublisher, &desired, &phase, &generation, &currentRevision, &currentPolicy, &currentEpoch, &currentRuntime); err != nil {
		return result, err
	}
	// Every fenced claim advances the installation revision. An expired worker may be
	// reclaimed more than once before completion, so revision is a lower-bound review
	// binding, not an exact one-shot counter. Generation, policy, release, phase,
	// audience and capability digests below still reject unrelated live-state changes.
	if currentRuntime.Valid || currentApp != result.plan.AppID || currentPublisher != result.plan.PublisherID || desired != "enabled" || phase != "ready" || generation != result.plan.SourceGeneration || currentRevision < result.plan.InstallationRevision || currentPolicy != result.plan.PolicyRevision || currentEpoch != result.plan.ControllerEpoch {
		return result, errAppPlatformStageEvidence
	}
	result.currentInstallationRevision = currentRevision
	var activeRelease string
	var activeFormat int64
	if err := e.queryRow(`SELECT release_sha256,data_format FROM app_platform_activations WHERE installation_id=? AND generation=? AND policy_revision=? AND state='active'`, lease.installationID, generation, currentPolicy).Scan(&activeRelease, &activeFormat); err != nil || activeRelease != source {
		return result, errAppPlatformStageEvidence
	}
	if activeFormat == 0 {
		activeFormat = result.plan.CurrentDataFormat
	}
	if activeFormat != result.plan.CurrentDataFormat {
		return result, errAppPlatformStageEvidence
	}
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, lease.installationID).Scan(&pending); err != nil || pending != 1 {
		return result, errAppPlatformStageEvidence
	}
	if err := platformInstallSponsor(e, sql.NullString{String: username, Valid: true}, created, accountGeneration, now); err != nil {
		return result, errAppPlatformStageEvidence
	}
	verified, err := platformVerifyStoredRelease(e, target, trustRevision, trust, now)
	if err != nil || verified.appID != result.plan.AppID || verified.publisherID != result.plan.PublisherID {
		return result, errAppPlatformPlanChanged
	}
	actor := platformActorFacts{authorityFingerprint: authority}
	expected, err := platformBuildUpdateProjection(e, lease.installationID, source, sourceGeneration, result.plan.InstallationRevision, result.plan.PolicyRevision, result.plan.CurrentDataFormat, actor, platformActorControl{epoch: epoch, nowMS: now}, verified)
	if err != nil {
		return result, errAppPlatformPlanChanged
	}
	expectedRaw, _ := json.Marshal(expected)
	if string(expectedRaw) != raw {
		return result, errAppPlatformPlanChanged
	}
	result.verified = verified
	return result, nil
}

func (s *Store) advancePlatformUpdateStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate || expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	nextByStage := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageQueued: appplatform.StageValidate, appplatform.StageValidate: appplatform.StageDownload,
		appplatform.StageDownload: appplatform.StageStage, appplatform.StageStage: appplatform.StageQuiesce,
		appplatform.StageQuiesce: appplatform.StageSnapshot, appplatform.StageSnapshot: appplatform.StageMigrate,
		appplatform.StageMigrate: appplatform.StageActivate,
	}
	next, ok := nextByStage[expected.snapshot.Stage]
	if !ok {
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
	if _, err := loadPlatformAdmittedUpdate(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, err
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = advanced.Snapshot()
	result.rowRevision++
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

func (s *Store) activatePlatformStaticUpdate(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	if expected.snapshot.Action != appplatform.ActionUpdate || expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageActivate {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return platformOperationLease{}, err
	}
	defer tx.Rollback()
	e := migExec{s: s, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return platformOperationLease{}, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return platformOperationLease{}, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return platformOperationLease{}, err
	}
	admitted, err := loadPlatformAdmittedUpdate(e, expected, trust, facts.now)
	if err != nil {
		return platformOperationLease{}, err
	}
	descriptor, err := platformReleaseDescriptor(e, admitted.verified.sha256)
	if err != nil {
		return platformOperationLease{}, err
	}
	manifest, err := appplatform.ParseDescriptor(descriptor)
	if err != nil {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	selection, err := platformInstallCapabilitySelection(manifest.Manifest, nil)
	if err != nil {
		return platformOperationLease{}, err
	}
	var sourceActivation, sourceRelease, sourceEvidence string
	var namespace sql.NullString
	var dataFormat, sourceGeneration, sourcePolicy int64
	var observed sql.NullInt64
	if err := e.queryRow(`SELECT id,release_sha256,generation,policy_revision,data_namespace_id,data_format,readiness_evidence,observed_ms FROM app_platform_activations WHERE installation_id=? AND state='active'`, expected.installationID).Scan(&sourceActivation, &sourceRelease, &sourceGeneration, &sourcePolicy, &namespace, &dataFormat, &sourceEvidence, &observed); err != nil || !observed.Valid || sourceRelease != admitted.plan.SourceReleaseSHA256 || sourceGeneration != admitted.plan.SourceGeneration || sourcePolicy != admitted.plan.PolicyRevision {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	if !namespace.Valid {
		sourceDescriptor, err := platformReleaseDescriptor(e, sourceRelease)
		if err != nil {
			return platformOperationLease{}, err
		}
		sourceManifest, err := appplatform.ParseDescriptor(sourceDescriptor)
		if err != nil {
			return platformOperationLease{}, errAppPlatformStageEvidence
		}
		if err := verifyPlatformFrontendActivation(e, expected.installationID, sourceRelease, expected.controllerEpoch, sourceGeneration, sourcePolicy, observed.Int64, sourceEvidence, sourceManifest.Manifest); err != nil {
			return platformOperationLease{}, err
		}
	}
	next, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return platformOperationLease{}, err
	}
	completed, err := next.Advance(expected.snapshot.Revision, appplatform.StageComplete)
	if err != nil {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return platformOperationLease{}, err
	}
	review, err := json.Marshal(admitted.plan)
	if err != nil || len(review) > 65536 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	var archive []byte
	if err := e.queryRow(`SELECT content FROM app_platform_frontend_artifacts WHERE sha256=?`, manifest.Manifest.Frontend.SHA256).Scan(&archive); err != nil {
		return platformOperationLease{}, err
	}
	document, err := appplatform.BuildFrontendDocument(manifest.Manifest, archive)
	if err != nil || document.SourceSHA256 != manifest.Manifest.Frontend.SHA256 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	audienceSHA, err := platformInstallationAudienceSHA(e, expected.installationID)
	if err != nil {
		return platformOperationLease{}, err
	}
	evidence := platformFrontendEvidence(platformStaticCandidate{
		reviewSHA:      platformDigest(review),
		declaredSchema: manifest.Manifest.Data.Schema,
		releaseSHA:     admitted.verified.sha256,
		frontendSHA:    document.SourceSHA256,
		documentSHA:    document.DocumentSHA256,
	}, admitted.plan.ControllerEpoch, expected.installationID, audienceSHA, admitted.plan.TargetGeneration, admitted.plan.TargetPolicyRevision)
	if len(evidence) > 8192 {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	if _, err := e.exec(`UPDATE app_platform_activations SET state='retired',updated_ms=? WHERE id=? AND state='active'`, now, sourceActivation); err != nil {
		return platformOperationLease{}, err
	}
	activationID, err := platformNewUUID()
	if err != nil {
		return platformOperationLease{}, err
	}
	if _, err := e.exec(`INSERT INTO app_platform_activations(id,installation_id,release_sha256,generation,policy_revision,data_namespace_id,data_format,state,readiness_evidence,observed_ms,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,'active',?,?,?,?)`,
		activationID, expected.installationID, admitted.verified.sha256, admitted.plan.TargetGeneration, admitted.plan.TargetPolicyRevision, namespace, dataFormat, string(evidence), now, now, now); err != nil {
		return platformOperationLease{}, err
	}
	changed, err := e.exec(`UPDATE app_platform_installations SET generation=?,policy_revision=?,revision=revision+1,phase='ready',updated_ms=? WHERE id=? AND generation=? AND policy_revision=? AND revision=?`,
		admitted.plan.TargetGeneration, admitted.plan.TargetPolicyRevision, now, expected.installationID, admitted.plan.SourceGeneration, admitted.plan.PolicyRevision, admitted.currentInstallationRevision)
	if err := platformLeaseCAS(changed, err); err != nil {
		return platformOperationLease{}, err
	}
	if _, err := e.exec(`DELETE FROM app_platform_capability_grants WHERE installation_id=?`, expected.installationID); err != nil {
		return platformOperationLease{}, err
	}
	for _, capability := range selection {
		if _, err := e.exec(`INSERT INTO app_platform_capability_grants(installation_id,capability,granted,policy_json,policy_revision,review_plan_id,created_ms,updated_ms) VALUES(?,?,?,?,?,?,?,?)`,
			expected.installationID, capability.capability, boolInt(capability.granted), capability.policy, admitted.plan.TargetPolicyRevision, admitted.planID, now, now); err != nil {
			return platformOperationLease{}, err
		}
	}
	if _, err := e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=? WHERE id=? AND stage='activate' AND state_revision=? AND row_revision=?`,
		int64(completed.Snapshot().Revision), now, now, expected.id, int64(expected.snapshot.Revision), expected.rowRevision); err != nil {
		return platformOperationLease{}, err
	}
	detail, _ := json.Marshal(struct {
		Source, Target string
		Generation     int64 `json:"generation"`
	}{admitted.plan.SourceReleaseSHA256, admitted.verified.sha256, admitted.plan.TargetGeneration})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_activated','app_platform_installation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.installationID, string(detail)); err != nil {
		return platformOperationLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return platformOperationLease{}, err
	}
	return platformOperationLease{}, nil
}

func platformReleaseDescriptor(e migExec, release string) ([]byte, error) {
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		return nil, err
	}
	return descriptor, nil
}

func (s *Server) runPlatformStaticUpdate(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	result := expected
	for result.snapshot.Stage != appplatform.StageActivate {
		next, err := s.st.advancePlatformUpdateStage(ctx, result)
		if err != nil {
			return result, err
		}
		result = next
	}
	_, err := s.st.activatePlatformStaticUpdate(ctx, result)
	return platformOperationLease{}, err
}

func (s *Server) finishPlatformUpdateForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionUpdate || expected.snapshot.Managed || expected.id == "" {
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return err
	}
	admitted, err := loadPlatformAdmittedUpdate(e, expected, trust, facts.now)
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
	failed, err := model.Fail(expected.snapshot.Revision, appplatform.EffectsNone)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code='update_retry_exhausted',completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Code       string `json:"code"`
		Generation int64  `json:"generation"`
	}{"update_retry_exhausted", admitted.plan.SourceGeneration})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_failed','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
