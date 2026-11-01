package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) claimNextPlatformDeleteData(
	ctx context.Context, worker string,
) (platformInstallSelection, error) {
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
	if err := e.queryRow(`SELECT v FROM meta
		WHERE k='app_platform_controller_epoch'`).Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	if err := e.queryRow(`SELECT v FROM meta
		WHERE k='app_platform_restore_paused'`).Scan(&paused); err != nil {
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
		JOIN app_platform_data_namespaces d
		  ON d.origin_installation_id=i.id AND d.state='deleting'
		WHERE o.action='delete_data' AND o.managed=1
		AND o.completed_ms IS NULL
		AND o.stage IN ('queued','validate','revoke','remove_data')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.runtime_id IS NOT NULL
		AND ((i.desired_state='disabled' AND
		      i.phase IN ('disabled','recovery_required','review_required')) OR
		     (i.desired_state='removed' AND
		      i.phase IN ('removed','recovery_required','review_required')))
		AND i.generation=o.target_generation
		AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.action=o.action AND p.controller_epoch=o.controller_epoch
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
		if facts.lease.leaseUntilMS > facts.now ||
			facts.lease.snapshot.Action != appplatform.ActionDeleteData ||
			!facts.lease.snapshot.Managed {
			continue
		}
		if err := e.queryRow(`SELECT runtime_id
			FROM app_platform_installations WHERE id=?`,
			facts.lease.installationID).Scan(&facts.runtimeID); err != nil ||
			!facts.runtimeID.Valid {
			if err != nil {
				return zero, err
			}
			return zero, errAppPlatformStageEvidence
		}
		lease, err := writePlatformOperationLease(
			e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
		if _, err := platformValidateAdmittedDeleteData(
			e, lease, check); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.data_delete_claimed',
			'app_platform_operation',?,?,'')`,
			timeUnixMilliUTC(facts.now), id, string(detail)); err != nil {
			return zero, err
		}
		if _, err := finalPlatformOperationLeaseCheck(e, check); err != nil {
			return zero, err
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return platformInstallSelection{
			lease: lease, operationID: id,
		}, nil
	}
	return zero, nil
}

func (s *Server) runPlatformDeleteData(
	ctx context.Context,
	expected platformOperationLease,
	endpoint platformRuntimeEndpoint,
) (platformInstallRun, error) {
	var zero platformInstallRun
	if expected.snapshot.Action != appplatform.ActionDeleteData ||
		!expected.snapshot.Managed {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	for result.snapshot.Stage != appplatform.StageComplete {
		switch result.snapshot.Stage {
		case appplatform.StageQueued, appplatform.StageValidate,
			appplatform.StageRevoke:
			next, err := s.st.advancePlatformDeleteDataControl(result)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		case appplatform.StageRemoveData:
			intent, err := s.preparePlatformStandaloneDeleteDataIntent(
				ctx, result)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			if err := s.checkPlatformManagedOperationRoot(
				ctx, intent.sourceOperationID, intent.runtime,
				intent.authority, endpoint); err != nil {
				return platformInstallRun{lease: result}, err
			}
			observation, err := platformDeleteApplicationData(
				ctx, endpoint, intent)
			if err != nil {
				return platformInstallRun{lease: result}, fmt.Errorf(
					"delete-data dispatch target=%s writer=%s namespace=%s: %w",
					intent.command.TargetGeneration,
					intent.command.Payload.WriterFence,
					intent.command.Payload.NamespaceID, err)
			}
			receipt, ok := validPlatformDeleteDataObservation(
				observation, intent, endpoint)
			if !ok {
				return platformInstallRun{lease: result},
					errAppPlatformStageEvidence
			}
			next, err := s.settlePlatformStandaloneDeleteData(
				ctx, result, intent, receipt)
			if err != nil {
				return platformInstallRun{lease: result}, err
			}
			result = next
		default:
			return zero, errAppPlatformStageEvidence
		}
	}
	return platformInstallRun{lease: result}, nil
}
