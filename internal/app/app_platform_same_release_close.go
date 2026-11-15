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
// evidence below holds, the operation is closed failed with the typed reason start_never_ran and the proof is
// recorded in the audit log, all in the transaction that reverted the start. The installation keeps the phase it is
// in, recovery_required: that is the state a repair is reviewed from (an enable is not admitted from it), so the
// administrator's next step is exactly the one that was open before, with the reason now stated.
//
// "Never ran" must mean the start did nothing at all. A never-ran verdict after a claim proves only that the
// target container was never created: an enable start removes the stopped source container before it creates the
// target, so a start the runner claimed and then settled may have removed the source, and the Portal cannot see
// whether the runner found it (the settlement receipt does not say). A start the runner never claimed cannot have
// touched anything. So the proofs that close the operation are:
//
//   - capacity_refused: the runner refused the start at admission, with the typed refusal of exactly that start;
//   - reserved_removed: the runner held the start only as reserved (never claimed), its pending target was removed
//     bound to the reservation, and the namespace sweep that followed found and left nothing.
//
// Two other outcomes prove the target never existed but not that the start did nothing: a never-ran verdict after an
// orphaned claim (the source may be gone), and a typed absence (no journal row, and no sweep can be obtained for a
// start the runner never held). Those leave the operation in recovery_required with the typed code
// start_never_ran_review_required, which the views show.
//
// The standard otherwise follows platformLoadNeverStartedEvidence:
//
//   - the operation is a managed enable or repair that ended recovery_required, with an error code and no lease,
//     under the fence and epoch the installation still holds, and the installation is disabled in recovery_required;
//   - the start effect (stage start, slot 1) is an enable-start and is reverted; no start-bearing effect is anything
//     but reverted;
//   - every effect of the operation is settled (applied or reverted), and none lies past the start stage;
//   - no activation of the installation is active and no candidate remains at the target generation.
const (
	platformStartNeverRanClosedAudit = "app_platform.start_never_ran_closed"
	// platformStartReviewErrorCode is the operation error code of a reverted start that is not proven to have done
	// nothing: the administrator is told so instead of the generic recovery message.
	platformStartReviewErrorCode = "start_never_ran_review_required"
)

// platformSameReleaseProofOf names how a reverted start was settled, or "" when its receipt is not one of the
// three the settlement writes.
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

type platformSameReleaseEffect struct {
	stage, id, kind, state, receipt, runtime, epoch string
	number, admitted, observed                      int64
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
	var completed, leaseUntil sql.NullInt64
	var owner, errorCode sql.NullString
	if err := e.queryRow(`SELECT stage,state_revision,row_revision,target_generation,managed,controller_epoch,fence,completed_ms,lease_owner,lease_until_ms,error_code,action
		FROM app_platform_operations WHERE id=? AND installation_id=?`, lease.id, lease.installationID).
		Scan(&stage, &stateRevision, &rowRevision, &generation, &managed, &epoch, &fence, &completed, &owner, &leaseUntil, &errorCode, &action); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if stage != string(appplatform.StageRecoveryRequired) || managed != 1 || !completed.Valid || owner.Valid || leaseUntil.Valid || !errorCode.Valid || errorCode.String == "" ||
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
	var active, candidates int
	if err := e.queryRow(`SELECT
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND state='active'),
		(SELECT COUNT(*) FROM app_platform_activations WHERE installation_id=? AND generation=? AND state='candidate')`,
		lease.installationID, lease.installationID, generation).Scan(&active, &candidates); err != nil {
		return false, err
	}
	if active != 0 {
		return false, nil
	}
	// A candidate left at the target generation would be found by the next start of the installation: not proof that
	// the start did nothing, so it is the typed review case, not the generic recovery message.
	leftoverCandidate := candidates != 0
	rows, err := e.query(`SELECT stage,effect_number,effect_id,kind,state,receipt_json,COALESCE(runtime_id,''),controller_epoch,admitted_fence,observed_fence
		FROM app_platform_operation_effects WHERE operation_id=?`, lease.id)
	if err != nil {
		return false, err
	}
	var effects []platformSameReleaseEffect
	for rows.Next() {
		var f platformSameReleaseEffect
		if err := rows.Scan(&f.stage, &f.number, &f.id, &f.kind, &f.state, &f.receipt, &f.runtime, &f.epoch, &f.admitted, &f.observed); err != nil {
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
	var start, sweep, held, reservedStop, reservedRemove *platformSameReleaseEffect
	for index := range effects {
		f := &effects[index]
		if f.state != "applied" && f.state != "reverted" {
			return false, nil
		}
		switch f.stage {
		case "download", "stage", "quiesce", "snapshot", "settle", "start":
		default:
			// Nothing that lies past the start stage may have been done.
			return false, nil
		}
		if platformStartBearingKinds[f.kind] || f.stage == "start" {
			if f.stage != "start" || f.number != 1 || f.kind != "enable-start" || f.state != "reverted" || start != nil {
				return false, nil
			}
			start = f
		}
		if f.stage == platformSettleStage && f.state == "applied" {
			switch f.number {
			case platformSettleSweepEffect:
				sweep = f
			case platformSettleHeldEffect:
				held = f
			case platformSettleReservedStopEffect:
				reservedStop = f
			case platformSettleReservedRemoveEffect:
				reservedRemove = f
			}
		}
	}
	if start == nil {
		return false, nil
	}
	proof := platformSameReleaseProofOf(start.receipt, lease, start.id, start.observed)
	if proof == "" {
		return false, nil
	}
	// What proves the start did nothing at all, as opposed to the target never having existed.
	switch {
	case leftoverCandidate:
		return markPlatformStartReview(e, lease, now, errorCode.String, stateRevision, rowRevision, fence)
	case proof == "capacity_refused":
	case proof == "never_ran" && held != nil && held.kind == platformHeldStartKind && held.receipt == platformHeldReservedReceipt &&
		reservedStop != nil && reservedRemove != nil && sweep != nil:
		proof = "reserved_removed"
	default:
		// Not proven: leave the operation for the administrator, and say why instead of the generic message.
		return markPlatformStartReview(e, lease, now, errorCode.String, stateRevision, rowRevision, fence)
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
	type effectEvidence struct {
		EffectID string `json:"effect_id"`
		State    string `json:"state"`
		Receipt  string `json:"receipt"`
	}
	evidence := func(f *platformSameReleaseEffect) *effectEvidence {
		if f == nil {
			return nil
		}
		return &effectEvidence{f.id, f.state, f.receipt}
	}
	detail, err := json.Marshal(struct {
		OperationID      string          `json:"operation_id"`
		Action           string          `json:"action"`
		Proof            string          `json:"proof"`
		StartEffectID    string          `json:"start_effect_id"`
		StartReceipt     string          `json:"start_receipt"`
		StartRuntimeID   string          `json:"start_runtime_id"`
		StartEpoch       string          `json:"start_controller_epoch"`
		StartAdmittedFN  int64           `json:"start_admitted_fence"`
		StartObservedFN  int64           `json:"start_observed_fence"`
		OperationFence   int64           `json:"operation_fence"`
		LeaseOwner       string          `json:"lease_owner"`
		LeaseUntilMS     int64           `json:"lease_until_ms"`
		Held             *effectEvidence `json:"held_start,omitempty"`
		ReservedStop     *effectEvidence `json:"reserved_stop,omitempty"`
		ReservedRemove   *effectEvidence `json:"reserved_remove,omitempty"`
		Sweep            *effectEvidence `json:"sweep,omitempty"`
		ActiveActivation int             `json:"active_activations"`
		CandidateRemain  int             `json:"candidate_activations_at_target"`
	}{lease.id, action, proof, start.id, start.receipt, start.runtime, start.epoch, start.admitted, start.observed, fence, "", 0,
		evidence(held), evidence(reservedStop), evidence(reservedRemove), evidence(sweep), active, candidates})
	if err != nil {
		return false, err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,?,'app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), platformStartNeverRanClosedAudit, lease.id, string(detail))
	return err == nil, err
}

// markPlatformStartReview gives a reverted start that is not proven to have done nothing the typed review code, once.
func markPlatformStartReview(e migExec, lease platformOperationLease, now int64, current string, stateRevision, rowRevision, fence int64) (bool, error) {
	if current == platformStartReviewErrorCode {
		return false, nil
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET error_code=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND stage='recovery_required' AND state_revision=? AND row_revision=? AND fence=? AND lease_owner IS NULL AND completed_ms IS NOT NULL`,
		platformStartReviewErrorCode, now, lease.id, lease.installationID, stateRevision, rowRevision, fence)
	return false, platformLeaseCAS(changed, err)
}
