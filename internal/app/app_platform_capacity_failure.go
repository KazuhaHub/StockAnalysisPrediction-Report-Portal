package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// The runner refuses a start-bearing effect it has no room for with a typed capacity_exhausted receipt that names
// the dimension (doc 171). The Portal keeps the typed outcome as the operation's failure: a closed error code per
// dimension, which the management and status views pass through and the UI turns into one message each.
var platformCapacityErrorCodes = map[string]string{
	journal.CapacityMemory:          "capacity_exhausted_memory",
	journal.CapacityCPU:             "capacity_exhausted_cpu",
	journal.CapacityDisk:            "capacity_exhausted_disk",
	journal.CapacityImageStore:      "capacity_exhausted_image_store",
	journal.CapacityRunningBackends: "capacity_exhausted_running_backends",
}

func platformCapacityErrorCode(dimension string) (string, bool) {
	code, ok := platformCapacityErrorCodes[dimension]
	return code, ok
}

func isPlatformCapacityErrorCode(code string) bool {
	for _, known := range platformCapacityErrorCodes {
		if code == known {
			return true
		}
	}
	return false
}

// platformCapacityRefusalOf finds the runner's refusal in an error from any stage of an operation.
func platformCapacityRefusalOf(err error) (journal.CapacityRefusal, bool) {
	var refusal *journal.CapacityRefusalError
	if errors.As(err, &refusal) && refusal != nil {
		return refusal.Receipt, true
	}
	return journal.CapacityRefusal{}, false
}

// platformPublicOperationErrorCode is the closed set of operation error codes the management and status views
// show. Anything else is reported as a generic failure and never leaks.
func platformPublicOperationErrorCode(code string) string {
	switch code {
	case "install_lease_recovery_required", "install_authority_review_required", "install_execution_review_required", platformStartNeverRanErrorCode:
		return code
	}
	if isPlatformCapacityErrorCode(code) {
		return code
	}
	return "app_platform_operation_failed"
}

// recordPlatformCapacityRefusal keeps the typed outcome in the audit log, whatever the action: the operation's own
// error code can only carry the dimension for the actions that finish with a code of their own.
func (s *Server) recordPlatformCapacityRefusal(ctx context.Context, lease platformOperationLease, r journal.CapacityRefusal) error {
	detail, err := json.Marshal(struct {
		Outcome   string `json:"outcome"`
		Dimension string `json:"dimension"`
		Requested string `json:"requested"`
		Remaining string `json:"remaining"`
		Stage     string `json:"stage"`
		EffectID  string `json:"effect_id"`
		Kind      string `json:"kind"`
	}{r.Outcome, r.Dimension, r.Requested, r.Remaining, r.Stage, r.EffectID, r.Kind})
	if err != nil {
		return err
	}
	_, err = s.st.execContext(ctx, `INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.capacity_refused','app_platform_operation',?,?,'')`,
		time.Now().UTC().Format(time.RFC3339), lease.id, string(detail))
	return err
}

// finishCapacity ends the active operation after a definite capacity refusal. A refusal is not transient: nothing
// is retried, the typed detail is kept, and the operation is finished by the same review path its action always
// uses. An installation, which owns its failure code, carries the dimension in it.
func (w *platformInstallScheduler) finishCapacity(ctx context.Context, r journal.CapacityRefusal) error {
	code, ok := platformCapacityErrorCode(r.Dimension)
	if !ok {
		return w.finish(ctx)
	}
	if err := w.server.recordPlatformCapacityRefusal(ctx, w.active, r); err != nil {
		w.publish("worker_unavailable", w.active.id, w.active.snapshot.Stage)
		return err
	}
	// The runner holds the refused effect reverted: the Portal's row of it is closed the same way, so nothing
	// waits for a verdict the runner will never give. It is closed in the very transaction that ends the operation
	// (see settleRefusedEffect), so either both happen or neither does.
	return w.finishAs(withPlatformRefusedEffect(ctx, r), code)
}

// platformEffectSettlement closes an effect row of the operation inside the transaction that ends it. It runs after
// the lease and the fence are verified and before the finisher commits.
type platformEffectSettlement func(e migExec, facts platformLeaseFacts) error

type platformEffectSettlementKey struct{}

// withPlatformEffectSettlement asks the transaction that ends an operation to run the settlement in it. Every
// operation finisher loads its lease through loadPlatformBoundOperationLease inside its own transaction, which is
// where the request is honoured, after the lease and the fence are verified and before the finisher commits or
// fails: the request has no other way to run. The settlement rides on the context because the finishers are
// shared by every action and their signatures are fixed.
func withPlatformEffectSettlement(ctx context.Context, settle platformEffectSettlement) context.Context {
	return context.WithValue(ctx, platformEffectSettlementKey{}, settle)
}

// withPlatformRefusedEffect asks the ending transaction to close the effect a runner capacity refusal names.
func withPlatformRefusedEffect(ctx context.Context, r journal.CapacityRefusal) context.Context {
	return withPlatformEffectSettlement(ctx, func(e migExec, facts platformLeaseFacts) error {
		return settleRefusedEffect(e, facts, r)
	})
}

func platformEffectSettlementOf(e migExec) (platformEffectSettlement, bool) {
	tx, ok := e.ex.(platformContextTx)
	if !ok || tx.ctx == nil {
		return nil, false
	}
	settle, ok := tx.ctx.Value(platformEffectSettlementKey{}).(platformEffectSettlement)
	return settle, ok && settle != nil
}

// settleRefusedEffect closes the operation's open effect row that has the refused effect's identity as reverted,
// with the typed refusal as its receipt. It runs in the transaction that ends the operation, under the lease and
// the fence the caller just verified, and compares the effect's exact identity: its id, its payload digest and the
// controller epoch of the lease. Effects of other identities, a row whose payload digest is not the refusal's, and
// effects already settled are untouched; a failure of the transaction undoes it.
func settleRefusedEffect(e migExec, facts platformLeaseFacts, r journal.CapacityRefusal) error {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 16384 {
		return errAppPlatformStageEvidence
	}
	result, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',receipt_json=?,updated_ms=?
		WHERE operation_id=? AND effect_id=? AND payload_sha256=? AND controller_epoch=? AND state IN ('prepared','unknown')`,
		string(raw), facts.now, facts.lease.id, r.EffectID, r.PayloadSHA256, facts.lease.controllerEpoch)
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
	return nil
}
