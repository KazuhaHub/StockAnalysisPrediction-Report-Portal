package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The runner can answer a start it proved never ran (journal.CandidateOrphanSettledError): the start's command
// window ended, the execution gate was free and no container exists for it. That answer is definite. The Portal
// turns it into a settled reverted row for the start effect and ends the operation failed with a closed code, in one
// transaction, the same way a capacity refusal ends an operation.

// platformStartNeverRanErrorCode is the operation error code of a start the runner proved never ran.
const platformStartNeverRanErrorCode = "start_never_ran"

// platformCandidateOrphanOf finds the runner's orphan answer in an error from any stage of an operation.
func platformCandidateOrphanOf(err error) (journal.CandidateOrphanReceipt, bool) {
	var settled *journal.CandidateOrphanSettledError
	if errors.As(err, &settled) && settled != nil {
		return settled.Receipt, true
	}
	return journal.CandidateOrphanReceipt{}, false
}

// settleOrphanedStartEffect closes the operation's start effect (stage start, effect 1) as reverted with the typed
// receipt. It runs in the transaction that ends the operation, under the lease and the fence the caller just
// verified, and compares the effect's exact identity: its id, payload digest, runtime and the lease's controller
// epoch, and an admission fence no newer than the lease's. The receipt must be the canonical valid orphan receipt of
// this very operation. A replay of the same receipt against the row it already closed is a success that changes
// nothing; a row in any other state, or of another identity, is evidence that disagrees with the runner and
// returns errAppPlatformStageEvidence with nothing written.
func settleOrphanedStartEffect(e migExec, facts platformLeaseFacts, r journal.CandidateOrphanReceipt) error {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 4096 {
		return errAppPlatformStageEvidence
	}
	if _, err := journal.ParseCandidateOrphanReceipt(raw); err != nil {
		return errAppPlatformStageEvidence
	}
	lease := facts.lease
	if lease.snapshot.Stage != appplatform.StageStart || !facts.runtimeID.Valid || r.Stage != "start" || r.EffectNumber != "1" ||
		r.OperationID != lease.id || r.InstallationID != lease.installationID || r.ControllerEpoch != lease.controllerEpoch ||
		r.TargetGeneration != strconv.FormatUint(lease.snapshot.Generation, 10) {
		return errAppPlatformStageEvidence
	}
	result, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind='start-candidate' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence<=? AND state IN ('prepared','unknown')`,
		string(raw), facts.now, lease.id, r.EffectID, r.PayloadSHA256, facts.runtimeID.String, lease.controllerEpoch, lease.fence)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed > 1 {
		return errAppPlatformStageEvidence
	}
	if changed == 0 {
		var replay int
		if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects
			WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND payload_sha256=? AND runtime_id=?
			AND controller_epoch=? AND state='reverted' AND receipt_json=?`,
			lease.id, r.EffectID, r.PayloadSHA256, facts.runtimeID.String, lease.controllerEpoch, string(raw)).Scan(&replay); err != nil {
			return err
		}
		if replay != 1 {
			return errAppPlatformStageEvidence
		}
		return nil
	}
	detail, err := json.Marshal(struct {
		Outcome    string `json:"outcome"`
		Stage      string `json:"stage"`
		EffectID   string `json:"effect_id"`
		ResourceID string `json:"resource_id"`
	}{r.Outcome, r.Stage, r.EffectID, r.ResourceID})
	if err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.candidate_never_ran','app_platform_operation',?,?,'')`,
		time.UnixMilli(facts.now).UTC().Format(time.RFC3339), lease.id, string(detail))
	return err
}

// finishOrphan ends the active operation after the runner proved its start never ran. Nothing is retried: the start
// effect's row is closed reverted in the very transaction that ends the operation (the audit entry too), so either
// all of it happens or none of it does.
func (w *platformInstallScheduler) finishOrphan(ctx context.Context, r journal.CandidateOrphanReceipt) error {
	return w.finishAs(withPlatformEffectSettlement(ctx, func(e migExec, facts platformLeaseFacts) error {
		return settleOrphanedStartEffect(e, facts, r)
	}), platformStartNeverRanErrorCode)
}
