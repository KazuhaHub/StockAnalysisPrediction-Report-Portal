package app

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A retry is a new update, behind a new review, that begins from the data a settled migration restored. It
// names the failed update it recovers, and only an installation waiting in recovery_required with that update
// settled can admit it.

// platformRetryPhaseAllowed says whether an update with this recovery reference may run against an
// installation in the phase.
func platformRetryPhaseAllowed(phase, recovers string) bool {
	return phase == "ready" || (phase == "recovery_required" && recovers != "")
}

// requirePlatformRetrySource checks that the update a retry names is the installation's failed update with its
// migration settled, and that nothing has resolved it yet.
func requirePlatformRetrySource(e migExec, installation, recovers string) error {
	if !platformValidUUID(recovers) {
		return errAppPlatformPlanChanged
	}
	var action, stage, code string
	var managed int
	var owner string
	if err := e.queryRow(`SELECT action,managed,stage,COALESCE(error_code,''),installation_id FROM app_platform_operations WHERE id=?`, recovers).
		Scan(&action, &managed, &stage, &code, &owner); err != nil || action != "update" || managed != 1 || stage != "recovery_required" ||
		code != "update_recovery_required" || owner != installation {
		return errAppPlatformPlanChanged
	}
	state, err := platformMigrationSettlementState(e, recovers)
	if err != nil {
		return err
	}
	if state != "settled" {
		if state == "" {
			// A failed update with no migration has no settlement to retry from.
			return errAppPlatformPlanChanged
		}
		return errAppPlatformMigrationUnsettled
	}
	var resolved int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='recovered' AND state='applied'`, recovers).Scan(&resolved); err != nil || resolved != 0 {
		return errAppPlatformPlanChanged
	}
	return nil
}

// platformRetryAncestors lists the failed updates a retry recovers: the one it names, then the one that one
// recovered, and so on to the root.
func platformRetryAncestors(e migExec, recovers string) ([]string, error) {
	var chain []string
	for recovers != "" {
		if len(chain) >= 64 {
			return nil, errAppPlatformStageEvidence
		}
		chain = append(chain, recovers)
		var raw string
		if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=?`, recovers).Scan(&raw); err != nil {
			return nil, err
		}
		var probe struct {
			Recovers string `json:"recovers_operation_id"`
		}
		if err := jsonUnmarshalString(raw, &probe); err != nil {
			return nil, err
		}
		recovers = probe.Recovers
	}
	return chain, nil
}

// platformRecoveryChain is the chain a retry's migrate-data signs: the failed updates it recovers from the root to the
// one it names, comma separated. Empty for an update that recovers nothing.
func platformRecoveryChain(e migExec, recovers string) (string, error) {
	if recovers == "" {
		return "", nil
	}
	ancestors, err := platformRetryAncestors(e, recovers)
	if err != nil {
		return "", err
	}
	if len(ancestors) > control.MaxRecoversChain {
		return "", errAppPlatformStageEvidence
	}
	// platformRetryAncestors runs from the update the retry names back to the root.
	for i, j := 0, len(ancestors)-1; i < j; i, j = i+1, j-1 {
		ancestors[i], ancestors[j] = ancestors[j], ancestors[i]
	}
	return control.JoinRecoversChain(ancestors), nil
}

// copyPlatformRecoveredRemoval gives a retry the source-removal evidence of the failed update it recovers: the
// source was already stopped and removed there, so the stage advances on that evidence and sends nothing. The
// copy keeps the original receipt, which still names the update that really did it.
func copyPlatformRecoveredRemoval(e migExec, lease platformOperationLease, recovers, stage string, number int, now int64) error {
	var kind, payload, receipt, runtimeID string
	if err := e.queryRow(`SELECT kind,payload_sha256,receipt_json,COALESCE(runtime_id,'') FROM app_platform_operation_effects
		WHERE operation_id=? AND stage=? AND effect_number=? AND state='applied'`, recovers, stage, number).
		Scan(&kind, &payload, &receipt, &runtimeID); err != nil {
		return fmt.Errorf("%w: recovered %s removal", errAppPlatformStageEvidence, stage)
	}
	if _, err := journal.ParseManagedRemovalReceipt([]byte(receipt)); err != nil {
		return errors.Join(errAppPlatformStageEvidence, err)
	}
	_, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
		VALUES(?,?,?,?,?,?,?,?,?,?,'applied',?,?,?)`,
		lease.id, stage, number, platformDerivedUUID("recovered-"+stage, lease.id), kind, payload, runtimeID, lease.controllerEpoch,
		lease.fence, lease.fence, receipt, now, now)
	return err
}

// markPlatformRecovered records, on each failed update the completed retry recovers, that it has been
// resolved. Launch treats such an update like one a restore settled.
func markPlatformRecovered(e migExec, lease platformOperationLease, runtimeID, recovers string, now int64) error {
	chain, err := platformRetryAncestors(e, recovers)
	if err != nil {
		return err
	}
	for _, failed := range chain {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'recovered',1,?,'recovered',?,?,?,?,?,'applied','{}',?,?)`,
			failed, platformDerivedUUID("recovered", failed), platformDigest([]byte("recovered/"+failed+"/"+lease.id)),
			runtimeID, lease.controllerEpoch, lease.fence, lease.fence, now, now); err != nil {
			return err
		}
	}
	return nil
}

// runPlatformRetryEvidenceStage takes the quiesce or migrate stage of a retry on the failed update's removal
// evidence. It reports handled=false for an ordinary update, which takes the stage's normal path.
//
// At the quiesce stage the evidence is the stop; at the migrate stage it is the source removal, after which
// the stage goes on to the migration when the plan declares one, and to the start when it does not.
func (s *Server) runPlatformRetryEvidenceStage(ctx context.Context, expected platformOperationLease) (platformOperationLease, bool, error) {
	var zero platformOperationLease
	stage := expected.snapshot.Stage
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		(stage != appplatform.StageQuiesce && stage != appplatform.StageMigrate) || expected.rowRevision == math.MaxInt64 {
		return zero, false, nil
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, false, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, false, err
	}
	if err := lockPlatformPublisherTrust(e); err != nil {
		return zero, false, err
	}
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, false, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, false, err
	}
	admitted, err := s.loadPlatformAdmittedManagedUpdate(e, expected, trust, facts.now)
	if err != nil {
		return zero, false, err
	}
	recovers := admitted.plan.RecoversOperationID
	if recovers == "" {
		return zero, false, nil
	}
	effectStage, number := string(stage), 1
	var existing int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage=? AND effect_number=?`, expected.id, effectStage, number).Scan(&existing); err != nil {
		return zero, false, err
	}
	if existing != 0 {
		// The evidence is already in place; the stage moves on, or hands over to the migration.
		return zero, false, nil
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, false, err
	}
	if err := copyPlatformRecoveredRemoval(e, expected, recovers, effectStage, number, now); err != nil {
		return zero, false, err
	}
	next := appplatform.StageSnapshot
	if stage == appplatform.StageMigrate {
		if admitted.plan.Migration != nil {
			// The migration is still to run, so the operation stays at this stage.
			if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
				return zero, false, err
			}
			return expected, true, tx.Commit()
		}
		next = appplatform.StageStart
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, false, errAppPlatformStageEvidence
	}
	advanced, err := model.Advance(expected.snapshot.Revision, next)
	if err != nil {
		return zero, false, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = advanced.Snapshot(), expected.rowRevision+1
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(next), int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		string(stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, false, err
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_retry_evidence','app_platform_operation',?,?,'')`,
		databaseNowRFC3339(now), expected.id, `{"recovers_operation_id":"`+recovers+`"}`); err != nil {
		return zero, false, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, false, err
	}
	return result, true, tx.Commit()
}
