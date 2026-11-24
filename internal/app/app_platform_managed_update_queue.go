package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math"
	"reflect"
	"sync"
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

// platformAdmitManagedUpdate is the admission check of a claim. Tests replace it to make one operation fail.
var platformAdmitManagedUpdate = func(s *Server, e migExec, lease platformOperationLease, trust appplatform.PublisherTrust, now int64) (platformAdmittedManagedUpdate, error) {
	return s.loadPlatformAdmittedManagedUpdate(e, lease, trust, now)
}

// platformManagedUpdateSkipLog remembers the last reason logged for each skipped operation, so that an operation that
// stays unadmittable is logged when its reason changes and not on every tick. It is bounded: past its size it starts
// over, which costs at most one repeated line per operation still being skipped.
var platformManagedUpdateSkipLog = struct {
	sync.Mutex
	reasons map[string]string
}{reasons: map[string]string{}}

const platformManagedUpdateSkipLogLimit = 256

func resetPlatformManagedUpdateSkipLog() {
	platformManagedUpdateSkipLog.Lock()
	defer platformManagedUpdateSkipLog.Unlock()
	clear(platformManagedUpdateSkipLog.reasons)
}

func logPlatformManagedUpdateSkipped(operation string, err error) {
	reason := err.Error()
	platformManagedUpdateSkipLog.Lock()
	previous, seen := platformManagedUpdateSkipLog.reasons[operation]
	if seen && previous == reason {
		platformManagedUpdateSkipLog.Unlock()
		return
	}
	if !seen && len(platformManagedUpdateSkipLog.reasons) >= platformManagedUpdateSkipLogLimit {
		clear(platformManagedUpdateSkipLog.reasons)
	}
	platformManagedUpdateSkipLog.reasons[operation] = reason
	platformManagedUpdateSkipLog.Unlock()
	log.Printf("app-platform managed update claim: skipping operation %s: %v", operation, err)
}

// platformManagedUpdateSkippable reports whether a failure to admit one operation is a statement about that
// operation (its evidence, its plan, its state) and so no reason to stop looking at the others. Anything else, a
// database or driver error above all, is returned: on PostgreSQL one failed statement aborts the transaction, and
// every later operation would fail for a reason that is not theirs.
func platformManagedUpdateSkippable(err error) bool {
	for _, evidence := range []error{
		errAppPlatformStageEvidence, errAppPlatformPlanChanged, errAppPlatformPlanExpired, errAppPlatformOperationConflict,
		errAppPlatformRuntimeConflict, errAppPlatformReleaseConflict, errAppPlatformTrustConflict, sql.ErrNoRows,
	} {
		if errors.Is(err, evidence) {
			return true
		}
	}
	return false
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
		AND (i.phase='ready' OR (i.phase='recovery_required' AND p.plan_json LIKE '%"recovers_operation_id":"%'))
		AND i.desired_state='enabled'
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
		// The normal idle state of every tick: nothing to log.
		return zero, nil
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	ended := false
	for _, id := range ids {
		facts, err := loadPlatformOperationLease(e, id)
		if err != nil {
			if errors.Is(err, errAppPlatformLeaseConflict) {
				continue
			}
			if ctx.Err() != nil || !platformManagedUpdateSkippable(err) {
				return zero, err
			}
			// One operation whose evidence does not load must not starve the claims of every other one.
			logPlatformManagedUpdateSkipped(id, err)
			continue
		}
		if facts.lease.leaseUntilMS > facts.now ||
			facts.lease.snapshot.Action != appplatform.ActionUpdate ||
			!facts.lease.snapshot.Managed ||
			facts.lease.snapshot.Stage == appplatform.StageComplete ||
			facts.lease.snapshot.Stage == appplatform.StageFailed ||
			facts.lease.snapshot.Stage == appplatform.StageRecoveryRequired {
			continue
		}
		admitted, err := platformAdmitManagedUpdate(
			s, e, facts.lease, trust, facts.now)
		if err != nil {
			if errors.Is(err, errAppPlatformPlanExpired) {
				// A start whose review plan expired before it began ends, failed, in this same transaction.
				if endErr := endExpiredPlatformManagedUpdate(e, facts); endErr != nil {
					return zero, endErr
				}
				ended = true
				continue
			}
			if ctx.Err() != nil || !platformManagedUpdateSkippable(err) {
				return zero, err
			}
			// An operation that cannot be admitted is skipped and logged, never returned: an error here would
			// starve the claims behind it, for every app and every kind of operation, on every tick.
			logPlatformManagedUpdateSkipped(id, err)
			continue
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
	if ended {
		if err := tx.Commit(); err != nil {
			return zero, err
		}
	}
	return zero, nil
}

// platformManagedUpdateStarted reports whether the update has begun to act on the installation. The expiry of its
// review plan governs only a start that has not begun: an operation whose source may be stopped is carried through to
// its end or to review whatever the clock says, because refusing it would leave the app stopped. The stages before
// quiesce act on nothing. The quiesce stage acts as soon as its effect row exists, in any state, because the stop may
// have been sent; with no row nothing was sent, and the update has still stopped nothing.
func platformManagedUpdateStarted(e migExec, lease platformOperationLease) (bool, error) {
	switch lease.snapshot.Stage {
	case appplatform.StageQueued, appplatform.StageValidate, appplatform.StageDownload, appplatform.StageStage:
		return false, nil
	case appplatform.StageQuiesce:
		var effects int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='quiesce'`, lease.id).Scan(&effects); err != nil {
			return false, err
		}
		return effects > 0, nil
	}
	return true, nil
}

// endExpiredPlatformManagedUpdate ends an update whose review plan expired before it began to act: it is failed with
// plan_expired, with no effect to settle and the installation untouched, so it neither runs on a plan that no longer
// holds nor waits to be skipped for ever.
func endExpiredPlatformManagedUpdate(e migExec, facts platformLeaseFacts) error {
	lease := facts.lease
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=?`, lease.id).Scan(&effects); err != nil || effects != 0 {
		return errAppPlatformStageEvidence
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	failed, err := model.Fail(lease.snapshot.Revision, appplatform.EffectsNone)
	if err != nil || failed.Snapshot().Stage != appplatform.StageFailed {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage='failed',state_revision=?,row_revision=row_revision+1,error_code='plan_expired',
		completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND stage=? AND state_revision=? AND row_revision=? AND completed_ms IS NULL
		AND (lease_until_ms IS NULL OR lease_until_ms<=?)`,
		int64(failed.Snapshot().Revision), facts.now, facts.now, lease.id, lease.installationID,
		string(lease.snapshot.Stage), int64(lease.snapshot.Revision), lease.rowRevision, facts.now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
		Code  string            `json:"code"`
	}{lease.snapshot.Stage, "plan_expired"})
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.update_plan_expired','app_platform_operation',?,?,'')`,
		time.UnixMilli(facts.now).UTC().Format(time.RFC3339), lease.id, string(detail))
	return err
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
		!consumed.Valid || consumed.Int64 > now ||
		epoch != lease.controllerEpoch ||
		platformDigest([]byte(raw)) != digest ||
		!platformValidSHA(source) || !platformValidSHA(targetRelease) {
		return result, errAppPlatformStageEvidence
	}
	if expires <= now && (lease.owner == "" || lease.leaseUntilMS <= now) {
		// Expiry governs only a start that has not begun; one that has is carried on (see platformManagedUpdateStarted).
		// It is checked when a claim admits the start, not under a lease a claim already holds: a plan that expires
		// between the claim and the first effect of the quiesce must not end the update for review with its source
		// never stopped. A worker that lost its lease is claimed again, and the check applies again then.
		started, err := platformManagedUpdateStarted(e, lease)
		if err != nil {
			return result, err
		}
		if !started {
			return result, errAppPlatformPlanExpired
		}
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
		desired != "enabled" || !platformRetryPhaseAllowed(phase, result.plan.RecoversOperationID) ||
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
	expected.RecoversOperationID = result.plan.RecoversOperationID
	// What the review recorded about the runner's capacity cannot be rebuilt, and a review stored before the update
	// raised the quota keeps its form: the update runs on with no raise.
	if !platformKeepStoredQuotaAnswer(expected.DataQuota, result.plan.DataQuota) {
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
		desired != "enabled" || !platformRetryPhaseAllowed(phase, finalized.RecoversOperationID) ||
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
		CurrentDataFormat:   finalized.DataFormat,
		Migration:           finalized.Migration,
		RecoversOperationID: finalized.RecoversOperationID,
		DataQuota:           finalized.DataQuota,
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
