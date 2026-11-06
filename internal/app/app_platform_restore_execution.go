package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
)

func (s *Server) advancePlatformRestoreStage(ctx context.Context, expected platformOperationLease, next appplatform.Stage) (platformOperationLease, error) {
	var zero platformOperationLease
	if resume, err := platformOperationIsResume(s.st.poolExec(), expected.id); err != nil {
		return zero, err
	} else if resume {
		return s.advancePlatformResumeStage(ctx, expected, next)
	}
	if expected.snapshot.Action != appplatform.ActionRestore ||
		!expected.snapshot.Managed ||
		expected.rowRevision == math.MaxInt64 ||
		!((expected.snapshot.Stage == appplatform.StageQuiesce &&
			next == appplatform.StageSnapshot) ||
			(expected.snapshot.Stage == appplatform.StageSnapshot &&
				next == appplatform.StageRestoreData)) {
		return zero, fmt.Errorf(
			"restore stage transition %s -> %s: %w",
			expected.snapshot.Stage, next,
			errAppPlatformStageEvidence)
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
		return zero, fmt.Errorf(
			"restore stage %s lease: %w", expected.snapshot.Stage, err)
	}
	if _, _, _, _, err := loadPlatformRestoreCheckpoint(e, expected); err != nil {
		return zero, fmt.Errorf(
			"restore stage %s checkpoint: %w",
			expected.snapshot.Stage, err)
	}
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='quiesce'
		AND effect_number IN (1,2) AND state='applied'`,
		expected.id).Scan(&effects); err != nil {
		return zero, fmt.Errorf(
			"restore stage %s target effects: %w",
			expected.snapshot.Stage, err)
	}
	if expected.snapshot.Stage == appplatform.StageQuiesce && effects != 2 {
		return zero, fmt.Errorf(
			"restore quiesce target effects applied=%d: %w",
			effects, errAppPlatformStageEvidence)
	}
	if expected.snapshot.Stage == appplatform.StageSnapshot && effects != 2 {
		return zero, fmt.Errorf(
			"restore snapshot target effects applied=%d: %w",
			effects, errAppPlatformStageEvidence)
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, fmt.Errorf(
			"restore stage %s operation: %w",
			expected.snapshot.Stage, errAppPlatformStageEvidence)
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, fmt.Errorf(
			"restore stage %s -> %s transition: %w",
			expected.snapshot.Stage, next,
			errAppPlatformStageEvidence)
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	result := expected
	result.snapshot, result.rowRevision =
		advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now,
		expected.id, expected.installationID, expected.controllerEpoch,
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

// runPlatformManagedRestore stops and removes only the failed update target
// before restoring data. It never reissues an effect in unknown state.
func (s *Server) runPlatformManagedRestore(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	if expected.snapshot.Action != appplatform.ActionRestore ||
		!expected.snapshot.Managed || endpoint.path == "" {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	if resume, err := platformOperationIsResume(s.st.poolExec(), expected.id); err != nil {
		return platformOperationLease{}, err
	} else if resume {
		// The import performed the data effects; those stages advance on evidence alone.
		if lease, handled, err := s.runPlatformRestoreResumeStage(ctx, expected); handled {
			return lease, err
		}
	}
	switch expected.snapshot.Stage {
	case appplatform.StageQuiesce:
		var stopState, removeState string
		_ = s.st.queryRow(`SELECT COALESCE(MAX(CASE WHEN effect_number=1 THEN state END),''),
			COALESCE(MAX(CASE WHEN effect_number=2 THEN state END),'')
			FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='quiesce'`,
			expected.id).Scan(&stopState, &removeState)
		switch {
		case stopState == "" && removeState == "":
			return s.dispatchPlatformUpdateTargetRemoval(
				ctx, expected, "stop", 1, endpoint)
		case stopState == "applied" && removeState == "":
			return s.dispatchPlatformUpdateTargetRemoval(
				ctx, expected, "remove_code", 2, endpoint)
		case stopState == "applied" && removeState == "applied":
			return s.advancePlatformRestoreStage(
				ctx, expected, appplatform.StageSnapshot)
		default:
			return platformOperationLease{}, errAppPlatformStageEvidence
		}
	case appplatform.StageSnapshot:
		return s.advancePlatformRestoreStage(
			ctx, expected, appplatform.StageRestoreData)
	case appplatform.StageRestoreData:
		return s.dispatchPlatformRestore(ctx, expected, endpoint)
	case appplatform.StageStart:
		return s.executePlatformRestoreStart(ctx, expected, endpoint)
	case appplatform.StageProbe:
		return s.probePlatformRestoreStart(ctx, expected, endpoint)
	case appplatform.StageActivate:
		registered, err := s.dispatchPlatformRestoreStartRegistration(
			ctx, expected, endpoint)
		if err != nil {
			return platformOperationLease{}, err
		}
		confirmed, err := s.completePlatformRestoreStartRegistration(
			ctx, registered, endpoint)
		if err != nil {
			return platformOperationLease{}, err
		}
		if _, err := s.activatePlatformRestoreStart(
			ctx, confirmed, endpoint); err != nil {
			return platformOperationLease{}, err
		}
		return platformOperationLease{}, nil
	default:
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
}

func (s *Server) finishPlatformManagedRestoreForReview(ctx context.Context, expected platformOperationLease) error {
	if expected.snapshot.Action != appplatform.ActionRestore ||
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
	if _, _, _, _, err := loadPlatformRestoreCheckpoint(e, expected); err != nil {
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
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_operations
		SET stage='recovery_required',state_revision=?,
		row_revision=row_revision+1,error_code='restore_recovery_required',
		completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=?
		AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>?
		AND completed_ms IS NULL`,
		int64(recovery.Snapshot().Revision), now, now, expected.id,
		expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision),
		expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_installations
		SET phase='recovery_required',revision=revision+1,updated_ms=?
		WHERE id=? AND desired_state='enabled'
		AND phase IN ('ready','recovery_required')
		AND generation=? AND revision=? AND control_fence=?
		AND controller_epoch=?`,
		now, expected.installationID, expected.snapshot.Generation-1,
		facts.installationRevision, expected.fence,
		expected.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Stage appplatform.Stage `json:"stage"`
	}{expected.snapshot.Stage})
	if _, err := e.exec(`INSERT INTO audit_log(
		at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_recovery_required',
		'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339),
		expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}
