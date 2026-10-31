package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) claimNextPlatformFrontendUninstall(ctx context.Context, worker string) (platformInstallSelection, error) {
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
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o JOIN app_platform_installations i ON i.id=o.installation_id JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='uninstall' AND o.managed=0 AND o.completed_ms IS NULL AND o.stage IN ('queued','validate','revoke','remove_code')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?) AND i.desired_state='removed' AND i.phase='pending' AND i.runtime_id IS NULL
		AND i.generation=o.target_generation AND i.controller_epoch=? AND o.controller_epoch=? AND p.action=o.action AND p.controller_epoch=o.controller_epoch
		AND NOT EXISTS(SELECT 1 FROM app_platform_data_namespaces d WHERE d.origin_installation_id=i.id OR d.owner_installation_id=i.id)
		AND NOT EXISTS(SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id)
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
	for _, id := range ids {
		facts, err := loadPlatformOperationLease(e, id)
		if err != nil {
			if errors.Is(err, errAppPlatformLeaseConflict) {
				continue
			}
			return zero, err
		}
		if facts.lease.leaseUntilMS > facts.now || facts.lease.snapshot.Action != appplatform.ActionUninstall || facts.lease.snapshot.Managed {
			continue
		}
		if err := validatePlatformAdmittedRemoval(e, facts.lease, facts); err != nil {
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
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.uninstall_claimed','app_platform_operation',?,?,'')`, time.UnixMilli(facts.now).UTC().Format(time.RFC3339), id, string(detail)); err != nil {
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

func (s *Store) advancePlatformFrontendUninstall(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUninstall || expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	nextByStage := map[appplatform.Stage]appplatform.Stage{
		appplatform.StageQueued: appplatform.StageValidate, appplatform.StageValidate: appplatform.StageRevoke,
		appplatform.StageRevoke: appplatform.StageRemoveCode, appplatform.StageRemoveCode: appplatform.StageComplete,
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
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
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
	if next == appplatform.StageComplete {
		changed, err := e.exec(`UPDATE app_platform_installations SET phase='removed',revision=revision+1,updated_ms=? WHERE id=? AND desired_state='removed' AND phase='pending' AND generation=? AND revision=? AND controller_epoch=?`, now, expected.installationID, expected.snapshot.Generation, facts.installationRevision, expected.controllerEpoch)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		if _, err := e.exec(`DELETE FROM app_platform_capability_grants WHERE installation_id=?`, expected.installationID); err != nil {
			return zero, err
		}
		if _, err := e.exec(`DELETE FROM app_platform_access_grants WHERE installation_id=?`, expected.installationID); err != nil {
			return zero, err
		}
	}
	result := expected
	result.snapshot = advanced.Snapshot()
	result.rowRevision++
	if next == appplatform.StageComplete {
		changed, err := e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,completed_ms=?,lease_owner=NULL,lease_until_ms=NULL,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	} else {
		changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{next})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
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

func (s *Server) runPlatformFrontendUninstall(ctx context.Context, expected platformOperationLease) (platformOperationLease, error) {
	result := expected
	for result.snapshot.Stage != appplatform.StageComplete {
		next, err := s.st.advancePlatformFrontendUninstall(ctx, result)
		if err != nil {
			return result, err
		}
		result = next
	}
	return platformOperationLease{}, nil
}

func (s *Server) finishPlatformFrontendUninstallForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionUninstall || expected.snapshot.Managed {
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
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	if err := validatePlatformAdmittedRemoval(e, expected, facts); err != nil {
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
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code='uninstall_retry_exhausted',completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), now, now, expected.id, expected.installationID, expected.controllerEpoch, string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.uninstall_failed','app_platform_operation',?,'{}','')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
