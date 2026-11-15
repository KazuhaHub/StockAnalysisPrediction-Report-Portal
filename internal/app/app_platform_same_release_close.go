package app

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Closing an enable or repair whose start is proven never to have run.
//
// The settlement of a held start (app_platform_held_start_settle.go) ends with the start effect reverted. For an
// update that is a step towards restoring the checkpoint; an enable or repair has nothing further to do, and
// leaving it in recovery_required would make an administrator confirm what the evidence already proves. When the
// evidence below holds, the operation is closed failed with the typed reason start_never_ran, the installation
// returns to the disabled state a clean failure leaves, and the proof is recorded in the audit log, all in the
// transaction that reverted the start. When any part of it does not hold, nothing is written and the operation stays
// in recovery_required: the closing fails closed.
//
// The standard follows platformLoadNeverStartedEvidence, which proves the same thing for a refused install:
//
//   - the operation is a managed enable or repair that ended recovery_required, with an error code and no lease,
//     under the fence and epoch the installation still holds, and the installation is disabled in recovery_required;
//   - the start effect (stage start, slot 1) is an enable-start reverted by one of the three receipts the
//     settlement writes (a typed absence of a start that never recorded a reservation, the runner's capacity refusal
//     of exactly that start, or a never-ran verdict after the target was removed and the namespace swept);
//   - no start-bearing effect is anything but reverted: an applied, unknown or merely prepared start may have run;
//   - every effect of the operation is settled (applied or reverted), and none lies past the start stage;
//   - no activation of the installation is active.
const platformStartNeverRanClosedAudit = "app_platform.start_never_ran_closed"

// platformSameReleaseProofOf names how a reverted start was proven never to have run, or "" when its receipt is not
// one of the three the settlement writes.
func platformSameReleaseProofOf(receipt string, lease platformOperationLease, startEffectID string, observedFence int64) string {
	switch receipt {
	case platformAbsentReceipt:
		// The settlement reverts an absent start only when it never recorded a reservation.
		if observedFence == 0 {
			return "absent"
		}
	case platformNeverRanReceipt:
		return "never_ran"
	default:
		if refusal, err := journal.ParseCapacityRefusal([]byte(receipt)); err == nil && refusal.Outcome == "capacity_exhausted" &&
			refusal.EffectID == startEffectID && refusal.OperationID == lease.id && refusal.InstallationID == lease.installationID {
			return "capacity_refused"
		}
	}
	return ""
}

// closePlatformSameReleaseStartNeverRan closes the operation when the evidence holds, in the caller's transaction. It
// reports whether it did. It returns an error only for a failed read or write, which rolls the caller back so the
// settlement is taken again.
func closePlatformSameReleaseStartNeverRan(e migExec, lease platformOperationLease, now int64) (bool, error) {
	if !platformSameReleaseLifecycle(lease.snapshot.Action) {
		return false, nil
	}
	var stage, epoch, action string
	var stateRevision, rowRevision, generation, fence, managed int64
	var completed sql.NullInt64
	var owner sql.NullString
	var errorCode sql.NullString
	if err := e.queryRow(`SELECT stage,state_revision,row_revision,target_generation,managed,controller_epoch,fence,completed_ms,lease_owner,error_code,action
		FROM app_platform_operations WHERE id=? AND installation_id=?`, lease.id, lease.installationID).
		Scan(&stage, &stateRevision, &rowRevision, &generation, &managed, &epoch, &fence, &completed, &owner, &errorCode, &action); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}

	if stage != string(appplatform.StageRecoveryRequired) || managed != 1 || !completed.Valid || owner.Valid || !errorCode.Valid || errorCode.String == "" ||
		errorCode.String == platformStartNeverRanErrorCode || epoch != lease.controllerEpoch || fence != lease.fence ||
		(action != string(appplatform.ActionEnable) && action != string(appplatform.ActionRepair)) {
		return false, nil
	}
	var phase, desired, installationEpoch string
	var installationFence int64
	if err := e.queryRow(`SELECT phase,desired_state,control_fence,controller_epoch FROM app_platform_installations WHERE id=?`, lease.installationID).
		Scan(&phase, &desired, &installationFence, &installationEpoch); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if phase != "recovery_required" || desired != "disabled" || installationFence != lease.fence || installationEpoch != lease.controllerEpoch {
		return false, nil
	}
	var active int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'`, lease.installationID).Scan(&active); err != nil {
		return false, err
	}
	if active != 0 {
		return false, nil
	}
	rows, err := e.query(`SELECT stage,effect_number,effect_id,kind,state,receipt_json,observed_fence FROM app_platform_operation_effects WHERE operation_id=?`, lease.id)
	if err != nil {
		return false, err
	}
	type effect struct {
		stage, id, kind, state, receipt string
		number, observed                int64
	}
	var effects []effect
	for rows.Next() {
		var f effect
		if err := rows.Scan(&f.stage, &f.number, &f.id, &f.kind, &f.state, &f.receipt, &f.observed); err != nil {
			rows.Close()
			return false, err
		}
		effects = append(effects, f)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	var start *effect
	sweepApplied := false
	for index := range effects {
		f := effects[index]
		if f.state != "applied" && f.state != "reverted" {
			return false, nil
		}
		switch f.stage {
		case "download", "stage", "quiesce", "snapshot", "settle":
		case "start":
		default:
			// Nothing that lies past the start stage may have been done.
			return false, nil
		}
		if platformStartBearingKinds[f.kind] || f.stage == "start" {
			if f.stage != "start" || f.number != 1 || f.kind != "enable-start" || f.state != "reverted" || start != nil {
				return false, nil
			}
			start = &effects[index]
		}
		if f.stage == platformSettleStage && f.number == platformSettleSweepEffect && f.state == "applied" {
			sweepApplied = true
		}
	}
	if start == nil {
		return false, nil
	}
	proof := platformSameReleaseProofOf(start.receipt, lease, start.id, start.observed)
	if proof == "" || (proof == "never_ran" && !sweepApplied) {
		return false, nil
	}
	model, err := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: true,
		Generation: uint64(generation), Revision: uint64(stateRevision), Stage: appplatform.StageRecoveryRequired})
	if err != nil {
		return false, nil
	}
	closed, err := model.SettleStartNeverRan(model.Snapshot().Revision)
	if err != nil {
		return false, nil
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,updated_ms=?
		WHERE id=? AND installation_id=? AND stage='recovery_required' AND state_revision=? AND row_revision=? AND fence=? AND lease_owner IS NULL AND completed_ms IS NOT NULL`,
		string(closed.Snapshot().Stage), int64(closed.Snapshot().Revision), platformStartNeverRanErrorCode, now,
		lease.id, lease.installationID, stateRevision, rowRevision, fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	changed, err = e.exec(`UPDATE app_platform_installations SET phase='disabled',revision=revision+1,updated_ms=?
		WHERE id=? AND phase='recovery_required' AND desired_state='disabled' AND control_fence=? AND controller_epoch=?`,
		now, lease.installationID, lease.fence, lease.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	detail, err := json.Marshal(struct {
		OperationID   string `json:"operation_id"`
		Action        string `json:"action"`
		StartEffectID string `json:"start_effect_id"`
		Proof         string `json:"proof"`
		Receipt       string `json:"receipt"`
	}{lease.id, action, start.id, proof, start.receipt})
	if err != nil {
		return false, err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), platformStartNeverRanClosedAudit, lease.id, string(detail))
	return err == nil, err
}
