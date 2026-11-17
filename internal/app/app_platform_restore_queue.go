package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) claimNextPlatformManagedRestore(ctx context.Context, worker string) (platformInstallSelection, error) {
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
	var epoch string
	if err := e.queryRow(
		`SELECT v FROM meta WHERE k='app_platform_controller_epoch'`).
		Scan(&epoch); err != nil {
		return zero, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	rows, err := e.query(`SELECT o.id FROM app_platform_operations o
		JOIN app_platform_installations i ON i.id=o.installation_id
		JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.action='restore' AND o.managed=1
		AND o.completed_ms IS NULL
		AND o.stage IN ('quiesce','snapshot','restore_data')
		AND (o.lease_until_ms IS NULL OR o.lease_until_ms<=?)
		AND i.desired_state='enabled' AND i.phase='recovery_required'
		AND i.controller_epoch=? AND o.controller_epoch=?
		AND p.plan_json LIKE ? ORDER BY o.created_ms,o.id LIMIT 32`,
		now, epoch, epoch, `{"format":"`+platformManagedRestoreFormat+`",%`)
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
		old := facts.lease
		if old.leaseUntilMS > facts.now ||
			old.snapshot.Action != appplatform.ActionRestore ||
			!old.snapshot.Managed {
			continue
		}
		if _, _, _, _, err := loadPlatformRestoreCheckpoint(e, old); err != nil {
			return zero, err
		}
		lease, err := writePlatformOperationLease(
			e, facts, worker, nil)
		if err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			Worker string `json:"worker_id"`
		}{worker})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.restore_claimed',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339),
			id, string(detail)); err != nil {
			return zero, err
		}
		check := facts
		check.lease = lease
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
