package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

type platformInstallSelection struct {
	lease       platformOperationLease
	operationID string
	reviewed    bool
}

// Each selector owns one explicit initial installation family. A query is a hint:
// row/authority/lease eligibility is checked again under the usual lock order.
func (s *Server) claimNextPlatformStaticInstall(ctx context.Context, worker string) (platformInstallSelection, error) {
	return s.claimNextPlatformInstallForFormat(ctx, worker, platformNativeInstallFormat)
}
func (s *Server) claimNextPlatformFrontendInstall(ctx context.Context, worker string) (platformInstallSelection, error) {
	return s.claimNextPlatformInstallForFormat(ctx, worker, platformFrontendInstallFormat)
}
func (s *Server) claimNextPlatformWritableInstall(ctx context.Context, worker string) (platformInstallSelection, error) {
	selected, err := s.claimNextPlatformInstallForFormat(ctx, worker, platformWritableInstallFormat)
	if err != nil || selected.reviewed || selected.lease.id != "" {
		return selected, err
	}
	closed, err := s.closeNextUnexecutablePlatformInstall(ctx)
	if err != nil && ctx.Err() == nil {
		// Closing is housekeeping: a row it cannot close must not stop the engine's other work. Log it once.
		if last, _ := s.platformUnclosableInstallLog.Swap(err.Error()).(string); last != err.Error() {
			log.Printf("app-platform: cannot close an install in an unexecutable format: %v", err)
		}
		return platformInstallSelection{}, nil
	}
	return closed, err
}

// platformInstallFormatUnsupportedCode closes a runner-backed install whose plan format no executor runs. Earlier
// builds planned console installs as portal.managed-install-review, which only test drivers execute; such an
// install would otherwise stay queued with no log line.
const platformInstallFormatUnsupportedCode = "install_format_unsupported"

// closeNextUnexecutablePlatformInstall fails one queued, effect-free managed-format install with
// install_format_unsupported and removes its installation. It claims nothing and dispatches nothing; the
// administrator sees the failed operation and plans the install again.
func (s *Server) closeNextUnexecutablePlatformInstall(ctx context.Context) (platformInstallSelection, error) {
	var zero platformInstallSelection
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err = lockOUMutation(e); err != nil {
		return zero, err
	}
	if err = lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	active, err := platformControlActive(e)
	if err != nil || !active {
		return zero, err
	}
	var epoch string
	if err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		return zero, err
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	var id string
	err = e.queryRow(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.action='install' AND o.managed=1 AND o.source_generation=0 AND o.target_generation=1 AND o.completed_ms IS NULL AND o.stage='queued' AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.phase='pending' AND i.desired_state='enabled' AND i.runtime_id IS NOT NULL AND i.generation=1 AND i.controller_epoch=? AND o.controller_epoch=? AND p.plan_json LIKE ? ORDER BY o.created_ms,o.id LIMIT 1`, now, epoch, epoch, `{"format":"`+platformManagedInstallFormat+`",%`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	facts, err := loadPlatformOperationLease(e, id)
	if errors.Is(err, errAppPlatformLeaseConflict) {
		return zero, nil
	}
	if err != nil {
		return zero, err
	}
	resources, err := platformInstallResourceCount(e, facts.lease.installationID)
	if err != nil {
		return zero, err
	}
	if resources > 0 {
		// Not effect-free: leave it to an operator rather than guess.
		return zero, nil
	}
	if err = finalizePlatformInstallForReview(e, facts, platformInstallFormatUnsupportedCode, true); err != nil {
		return zero, err
	}
	// It never took an effect, so nothing needs reviewing: remove the installation outright, as a completed
	// uninstall does, so the administrator can plan the app again. The failed operation keeps the code.
	changed, err := e.exec(`UPDATE app_platform_installations SET desired_state='removed',phase='removed',revision=revision+1,updated_ms=? WHERE id=? AND phase='review_required' AND desired_state='enabled' AND generation=1 AND controller_epoch=?`, now, facts.lease.installationID, epoch)
	if err = platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	if _, err = e.exec(`DELETE FROM app_platform_capability_grants WHERE installation_id=?`, facts.lease.installationID); err != nil {
		return zero, err
	}
	if _, err = e.exec(`DELETE FROM app_platform_access_grants WHERE installation_id=?`, facts.lease.installationID); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	log.Printf("app-platform: install %s uses plan format %s, which no executor runs; closed and removed, plan it again", id, platformManagedInstallFormat)
	return platformInstallSelection{operationID: id, reviewed: true}, nil
}
func (s *Server) claimNextPlatformInstallForFormat(ctx context.Context, worker, format string) (platformInstallSelection, error) {
	var zero platformInstallSelection
	if !platformValidUUID(worker) || (format != platformNativeInstallFormat && format != platformFrontendInstallFormat && format != platformWritableInstallFormat) {
		return zero, errAppPlatformLeaseConflict
	}
	var dek []byte
	var err error
	if format != platformFrontendInstallFormat {
		dek, err = s.dek()
		if err != nil {
			return zero, err
		}
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err = lockOUMutation(e); err != nil {
		return zero, err
	}
	if err = lockPlatformPublisherTrust(e); err != nil {
		return zero, err
	}
	var epoch, paused string
	if err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, errAppPlatformControlPaused
		}
		return zero, err
	}
	if err = e.queryRow(`SELECT v FROM meta WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return zero, errAppPlatformControlPaused
		}
		return zero, err
	}
	if paused != "0" || !platformValidUUID(epoch) {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	managed, runtimeClause := 0, "runtime_id IS NULL"
	stages := "'queued','validate','download','stage','prepare_data','activate'"
	if format == platformWritableInstallFormat {
		managed, runtimeClause = 1, "runtime_id IS NOT NULL"
		stages += ",'start','probe'"
	}
	// Only closed constants above enter SQL syntax; user/review values stay bound.
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.action='install' AND o.managed=? AND o.source_generation=0 AND o.target_generation=1 AND o.completed_ms IS NULL AND o.stage IN (`+stages+`) AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.phase='pending' AND i.desired_state='enabled' AND i.`+runtimeClause+` AND i.generation=1 AND i.controller_epoch=? AND o.controller_epoch=? AND p.plan_json LIKE ? ORDER BY o.created_ms,o.id LIMIT 32`, managed, now, epoch, epoch, `{"format":"`+format+`",%`)
	if err != nil {
		return zero, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
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
	// Lock the shared provider before installation/operation rows. No seed is
	// retained by the selector; this is readiness/trust preflight, not dispatch.
	var runtimeErr error
	if format != platformFrontendInstallFormat {
		var seed []byte
		_, seed, runtimeErr = s.platformDataRuntime(e, epoch, dek)
		clear(seed)
	}
	if runtimeErr != nil && !errors.Is(runtimeErr, errAppPlatformRuntimeConflict) {
		return zero, runtimeErr
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
		old := facts.lease
		if old.leaseUntilMS > facts.now || old.snapshot.Action != appplatform.ActionInstall || old.snapshot.Managed != (managed == 1) || old.snapshot.Generation != 1 {
			continue
		}
		var eligible int
		if err = e.queryRow(`SELECT COUNT(*) FROM app_platform_installations WHERE id=? AND phase='pending' AND desired_state='enabled' AND `+runtimeClause+` AND generation=1 AND controller_epoch=?`, old.installationID, epoch).Scan(&eligible); err != nil {
			return zero, err
		}
		if eligible != 1 {
			continue
		}
		currentProfile, err := platformInstallProfileInTransaction(e, old.id)
		if err != nil {
			return zero, err
		}
		if currentProfile != format {
			return zero, errAppPlatformStageEvidence
		}
		resources, err := platformInstallResourceCount(e, old.installationID)
		if err != nil {
			return zero, err
		}
		if resources > 0 || platformInstallStageMayHaveEffects(old.snapshot.Stage, format) {
			if err = finalizePlatformInstallForReview(e, facts, "install_lease_recovery_required", true); err != nil {
				return zero, err
			}
			if err = tx.Commit(); err != nil {
				return zero, err
			}
			return platformInstallSelection{operationID: id, reviewed: true}, nil
		}
		if err = validatePlatformAdmittedInstall(e, old, trust, facts.now); err != nil {
			if !platformInstallAuthorityRefusal(err) {
				return zero, err
			}
			if err = finalizePlatformInstallForReview(e, facts, "install_authority_review_required", true); err != nil {
				return zero, err
			}
			if err = tx.Commit(); err != nil {
				return zero, err
			}
			return platformInstallSelection{operationID: id, reviewed: true}, nil
		}
		if runtimeErr != nil {
			return zero, runtimeErr
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
		if _, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.install_claimed','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
			return zero, err
		}
		finalNow, err := finalPlatformOperationLeaseCheck(e, check)
		if err != nil {
			return zero, err
		}
		if err = platformVerifyInstallAuthorityAtCommit(e, lease, trust, finalNow); err != nil {
			return zero, err
		}
		if err = tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{lease: lease, operationID: id}, nil
	}
	return zero, nil
}

func platformInstallStageMayHaveEffects(stage appplatform.Stage, format string) bool {
	return (stage == appplatform.StageActivate && format != platformFrontendInstallFormat) ||
		(format == platformWritableInstallFormat && (stage == appplatform.StageStart || stage == appplatform.StageProbe))
}

func platformInstallAuthorityRefusal(err error) bool {
	return errors.Is(err, errAppPlatformStageEvidence) || errors.Is(err, errAppPlatformPlanChanged) || errors.Is(err, errAppPlatformReleaseConflict) || errors.Is(err, errAppPlatformActorUnavailable) || errors.Is(err, errAppPlatformInstallUnavailable)
}
func platformInstallResourceCount(e migExec, installation string) (int, error) {
	var count int
	err := e.queryRow(`SELECT (SELECT COUNT(*) FROM app_platform_data_namespaces WHERE origin_installation_id=? OR owner_installation_id=?)+(SELECT COUNT(*) FROM app_platform_operation_effects f JOIN app_platform_operations o ON o.id=f.operation_id WHERE o.installation_id=? AND f.kind NOT IN (`+platformInstallationNeutralEffectKinds+`))+(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_access_grants WHERE installation_id=?)+(SELECT COUNT(*) FROM app_platform_capability_grants WHERE installation_id=? AND granted=1)`, installation, installation, installation, installation, installation, installation).Scan(&count)
	return count, err
}

// This denial-only transition retains all evidence/data. Expired work is never
// claimed merely to stop it; live work requires the exact still-current lease.
func finalizePlatformInstallForReview(e migExec, facts platformLeaseFacts, code string, expired bool) error {
	lease := facts.lease
	if code != "install_lease_recovery_required" && code != "install_authority_review_required" && code != "install_execution_review_required" && code != platformInstallFormatUnsupportedCode && code != platformStartNeverRanErrorCode && !isPlatformCapacityErrorCode(code) {
		return errAppPlatformStageEvidence
	}
	if lease.snapshot.Action != appplatform.ActionInstall || lease.snapshot.Generation != 1 || facts.controlFence != lease.fence || facts.controlFence == math.MaxInt64 || facts.installationRevision == math.MaxInt64 || lease.rowRevision == math.MaxInt64 {
		return errAppPlatformLeaseConflict
	}
	runtimeID, err := platformInstallReviewRuntime(e, lease, code == platformInstallFormatUnsupportedCode)
	if err != nil {
		return err
	}
	now, err := platformOperationControlTime(e, facts)
	if err != nil {
		return err
	}
	if expired {
		if lease.leaseUntilMS > now {
			return errAppPlatformLeaseConflict
		}
	} else if lease.leaseUntilMS <= now || lease.owner == "" {
		return errAppPlatformLeaseConflict
	}
	resources, err := platformInstallResourceCount(e, lease.installationID)
	if err != nil {
		return err
	}
	outcome, phase := appplatform.EffectsNone, "review_required"
	profile, err := platformInstallProfileInTransaction(e, lease.id)
	if err != nil {
		return err
	}
	if resources > 0 || platformInstallStageMayHaveEffects(lease.snapshot.Stage, profile) {
		outcome, phase = appplatform.EffectsUnknown, "recovery_required"
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	failed, err := model.Fail(lease.snapshot.Revision, outcome)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_installations SET phase=?,control_fence=control_fence+1,revision=revision+1,updated_ms=? WHERE id=? AND phase='pending' AND generation=1 AND desired_state='enabled' AND COALESCE(runtime_id,'')=? AND revision=? AND control_fence=? AND controller_epoch=?`, phase, now, lease.installationID, runtimeID, facts.installationRevision, facts.controlFence, lease.controllerEpoch)
	if err = platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND completed_ms IS NULL`, string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), code, now, now, lease.id, lease.installationID, lease.controllerEpoch, string(lease.snapshot.Stage), int64(lease.snapshot.Revision), lease.rowRevision, lease.fence)
	if err = platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Code    string                    `json:"code"`
		Outcome appplatform.EffectOutcome `json:"outcome"`
	}{code, outcome})
	if _, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.install_review_required','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
		return err
	}
	finalNow, err := platformOperationControlTime(e, facts)
	if err != nil {
		return err
	}
	if !expired && finalNow >= lease.leaseUntilMS {
		return errAppPlatformLeaseConflict
	}
	return nil
}
func (s *Server) finishPlatformInstallForReview(ctx context.Context, expected platformOperationLease) error {
	return s.finishPlatformInstallWithCode(ctx, expected, "install_execution_review_required")
}

// finishPlatformInstallWithCode is the review finish with the failure code the caller names: the generic one, or
// the dimension of a runner capacity refusal.
func (s *Server) finishPlatformInstallWithCode(ctx context.Context, expected platformOperationLease, code string) error {
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err = lockOUMutation(e); err != nil {
		return err
	}
	if err = lockPlatformPublisherTrust(e); err != nil {
		return err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	if err = finalizePlatformInstallForReview(e, facts, code, false); err != nil {
		return err
	}
	return tx.Commit()
}
