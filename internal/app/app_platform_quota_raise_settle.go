package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// Convergence of a quota raise that ended with no verdict (doc 185 section 3.2).
//
// A raise whose acknowledgement was lost, or whose runner crashed between claim and result, leaves its Portal row
// unknown and its operation recovery_required, a reviewed set_data_quota and an approved update alike. The runner
// finishes an unknown raise when the same signed command arrives and answers a read-back with the receipt or a typed
// status, so the Portal converges the row from the runner's own answer, one step per tick:
//
//   - a receipt applies the row and the namespace together (a reviewed raise's operation completes, an update's
//     stays recovery_required for its rollback);
//   - a failed status, the runner's definite and durable end, reverts the row (a reviewed raise's operation fails);
//   - a runner still working is waited for;
//   - an orphaned effect (claimed, nothing executing it) is finished by sending the same command again under its own
//     identity, signed anew, which the runner verifies under the settlement purpose;
//   - an absent effect is final only once every command signed for it has expired, and only then the row is reverted:
//     the runner never claimed it, and nothing ever will.
//
// The failed and the unknown stay exactly apart: a row is reverted only on the runner's definite answer or a proven
// absence, and never on a guess. While a row has no verdict the restore and retry reviews of the update refuse.

// platformRaiseSettlementWindow is how many operations one tick takes a convergence step for.
const platformRaiseSettlementWindow = 8

const platformRaiseSettlementFrom = `SELECT o.id FROM app_platform_operations o`
const platformRaiseSettlementWhere = `o.action IN ('update','set_data_quota') AND o.managed=1 AND o.stage='recovery_required'
	AND o.completed_ms IS NOT NULL AND o.lease_owner IS NULL
	AND EXISTS (SELECT 1 FROM app_platform_operation_effects f WHERE f.operation_id=o.id AND f.kind='set-data-quota' AND f.state IN ('prepared','unknown'))`

func (s *Server) platformRaisesToSettle(ctx context.Context) ([]string, error) {
	return platformKeysetWindow(ctx, s.st, &s.platformRaiseCursor, platformRaiseSettlementFrom, platformRaiseSettlementWhere,
		"o.id", nil, platformRaiseSettlementWindow, scanPlatformOperationID)
}

// settlePlatformQuotaRaises takes one convergence step for each operation that holds a raise with no verdict, and
// returns how many made progress.
func (s *Server) settlePlatformQuotaRaises(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" {
		return 0, nil
	}
	ids, err := s.platformRaisesToSettle(ctx)
	if err != nil {
		return 0, err
	}
	progressed := 0
	for _, id := range ids {
		done, err := s.settlePlatformQuotaRaise(ctx, endpoint, id)
		if err != nil {
			if ctx.Err() != nil {
				return progressed, ctx.Err()
			}
			if platformRunnerDown(ctx, err) {
				return progressed, err
			}
			log.Printf("app-platform quota raise settlement %s: %v", id, err)
			continue
		}
		if done {
			progressed++
		}
	}
	return progressed, nil
}

// platformRaiseSettlement is a failed operation that holds a raise, read as an unleased operation.
type platformRaiseSettlement struct {
	id, installation, epoch, action, planRaw, planSHA string
	fence, generation, stateRevision, rowRevision     int64
	lease                                             platformOperationLease
	slot                                              platformRaiseSlot
}

func loadPlatformRaiseSettlement(e migExec, operation string) (platformRaiseSettlement, error) {
	var r platformRaiseSettlement
	var completed sql.NullInt64
	var owner sql.NullString
	var stage string
	if err := e.queryRow(`SELECT o.id,o.installation_id,o.controller_epoch,o.action,o.fence,o.target_generation,o.state_revision,o.row_revision,
		o.stage,o.completed_ms,o.lease_owner,p.plan_json,p.plan_sha256
		FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.action IN ('update','set_data_quota') AND o.managed=1`, operation).
		Scan(&r.id, &r.installation, &r.epoch, &r.action, &r.fence, &r.generation, &r.stateRevision, &r.rowRevision,
			&stage, &completed, &owner, &r.planRaw, &r.planSHA); err != nil ||
		stage != "recovery_required" || !completed.Valid || owner.Valid || r.generation < 2 || platformDigest([]byte(r.planRaw)) != r.planSHA {
		return platformRaiseSettlement{}, errAppPlatformStageEvidence
	}
	r.slot = platformStandaloneRaiseSlot
	if r.action == string(appplatform.ActionUpdate) {
		r.slot = platformUpdateRaiseSlot
	}
	r.lease = platformOperationLease{
		id: r.id, installationID: r.installation, controllerEpoch: r.epoch, fence: r.fence, rowRevision: r.rowRevision,
		snapshot: appplatform.OperationSnapshot{Action: appplatform.Action(r.action), Managed: true, Generation: uint64(r.generation),
			Revision: uint64(r.stateRevision), Stage: appplatform.StageRecoveryRequired},
	}
	return r, nil
}

// guard checks, under the mutation lock, that the failed operation is still what the convergence was derived from and
// that nothing has moved the installation past it. It returns the database time.
func (r platformRaiseSettlement) guard(e migExec) (int64, error) {
	current, err := loadPlatformRaiseSettlement(e, r.id)
	if err != nil || current.fence != r.fence || current.epoch != r.epoch || current.stateRevision != r.stateRevision || current.rowRevision != r.rowRevision {
		return 0, errors.Join(errAppPlatformLeaseConflict, err)
	}
	if r.action == string(appplatform.ActionUpdate) {
		return platformSettlementGuard(e, r.lease)
	}
	var epoch, paused string
	if err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_controller_epoch'),''),COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'1')`).Scan(&epoch, &paused); err != nil ||
		epoch != r.epoch || paused != "0" {
		return 0, errAppPlatformControlPaused
	}
	var desired, phase, installationEpoch string
	if err := e.queryRow(`SELECT desired_state,phase,controller_epoch FROM app_platform_installations WHERE id=?`, r.installation).Scan(&desired, &phase, &installationEpoch); err != nil ||
		installationEpoch != r.epoch || !((desired == "enabled" && phase == "ready") || (desired == "disabled" && phase == "disabled")) {
		return 0, errAppPlatformLeaseConflict
	}
	// The chain moves with this step: nothing else may be running against it.
	var pending int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operations WHERE installation_id=? AND completed_ms IS NULL`, r.installation).Scan(&pending); err != nil || pending != 0 {
		return 0, errAppPlatformLeaseConflict
	}
	return platformDatabaseNow(e)
}

// raiseFacts rebuilds the facts the raise command bound from the operation's own plan, and proves the plan is
// the one that was admitted.
func (r platformRaiseSettlement) raiseFacts(e migExec) (facts platformRaiseFacts, namespace string, err error) {
	if r.action == string(appplatform.ActionSetDataQuota) {
		var plan platformDataQuotaPlan
		unmarshalErr := json.Unmarshal([]byte(r.planRaw), &plan)
		canonical, marshalErr := json.Marshal(plan)
		if unmarshalErr != nil || marshalErr != nil || string(canonical) != r.planRaw || plan.Format != platformSetDataQuotaFormat ||
			plan.InstallationID != r.installation || plan.ControllerEpoch != r.epoch {
			return facts, "", errAppPlatformStageEvidence
		}
		return platformRaiseFacts{}.fromPlan(plan), plan.DataNamespaceID, nil
	}
	var plan platformManagedUpdatePlan
	unmarshalErr := json.Unmarshal([]byte(r.planRaw), &plan)
	canonical, marshalErr := json.Marshal(plan)
	if unmarshalErr != nil || marshalErr != nil || string(canonical) != r.planRaw || plan.Format != platformManagedUpdateFormat ||
		plan.InstallationID != r.installation || plan.ControllerEpoch != r.epoch || plan.Action != "" {
		return facts, "", errAppPlatformStageEvidence
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, r.lease, plan)
	if err != nil {
		return facts, "", err
	}
	facts, err = platformUpdateRaiseFacts(plan, checkpoint)
	return facts, plan.DataNamespaceID, err
}

// signPlatformRaiseSettlement signs the command of the failed operation's raise, at the fence it was admitted under,
// for a read or, with send set, to be executed (its window is then recorded on the row first).
func (s *Server) signPlatformRaiseSettlement(ctx context.Context, r platformRaiseSettlement, send bool) (platformSetDataQuotaIntent, string, error) {
	var zero platformSetDataQuotaIntent
	dek, err := s.dek()
	if err != nil {
		return zero, "", err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, "", err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return zero, "", err
	}
	now, err := r.guard(e)
	if err != nil {
		return zero, "", err
	}
	runtime, seed, err := s.platformDataRuntime(e, r.epoch, dek)
	if err != nil {
		return zero, "", err
	}
	defer clear(seed)
	row, err := loadPlatformRaiseRow(e, r.id, r.slot, true)
	if err != nil || !row.exists || row.kind != "set-data-quota" || row.runtimeID != runtime.id || row.epoch != r.epoch || row.observed != 0 {
		return zero, "", errors.Join(errAppPlatformStageEvidence, err)
	}
	facts, namespace, err := r.raiseFacts(e)
	if err != nil {
		return zero, "", err
	}
	command, err := platformRaiseCommand(runtime, r.epoch, r.installation, r.id, uint64(r.generation), row.admitted, facts, row.effectID)
	if err != nil || command.PayloadSHA256 != row.digest {
		return zero, "", errors.Join(errAppPlatformStageEvidence, err)
	}
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, facts.releaseSHA256).Scan(&descriptor); err != nil {
		return zero, "", errAppPlatformStageEvidence
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, "", errAppPlatformLeaseConflict
	}
	intent, err := platformSignRaise(e, seed, runtime, row, r.slot, r.id, command, descriptor, issued, issued+25000, send)
	if err != nil {
		return zero, "", err
	}
	if err := tx.Commit(); err != nil {
		return zero, "", err
	}
	return intent, namespace, nil
}

// settlePlatformQuotaRaise takes the next convergence step of one failed operation's raise, and reports whether it
// changed anything. A step that has to wait is not an error: the same step is taken again later.
func (s *Server) settlePlatformQuotaRaise(ctx context.Context, endpoint platformRuntimeEndpoint, operation string) (bool, error) {
	pool := s.st.poolExec()
	r, err := loadPlatformRaiseSettlement(pool, operation)
	if err != nil {
		return false, err
	}
	row, err := loadPlatformRaiseRow(pool, r.id, r.slot, false)
	if err != nil || !row.exists || (row.state != "prepared" && row.state != "unknown") {
		return false, err
	}
	intent, namespace, err := s.signPlatformRaiseSettlement(ctx, r, false)
	if err != nil {
		return false, err
	}
	observation, err := platformSetDataQuotaRead(ctx, endpoint, intent)
	if err != nil {
		return false, err
	}
	if done, handled, err := s.concludePlatformRaise(ctx, r, intent, namespace, endpoint, observation); handled || err != nil {
		return done, err
	}
	status, statusErr := observation.DataEffectStatus()
	if statusErr != nil || !validPlatformMigrateStatus(observation, status, intent.command.CommandHeader, intent.raw, intent.authority, endpoint) {
		return false, errAppPlatformStageEvidence
	}
	switch status.State {
	case "absent":
		// Nothing was claimed. It is final only when no command signed for the effect can still arrive.
		if time.Now().UnixMilli() <= max(intent.priorSignedUntil, 0)+platformImportAbsenceMarginMS {
			return false, nil
		}
		return true, s.revertPlatformRaiseRow(ctx, r, intent, platformRaiseAbsentReceipt)
	case "orphaned":
		// Claimed, and nothing is executing it: sending the same command again finishes it under the same claim.
		resend, _, err := s.signPlatformRaiseSettlement(ctx, r, true)
		if err != nil {
			return false, err
		}
		answer, err := platformSetDataQuotaExecute(ctx, endpoint, resend)
		if err != nil {
			return false, err
		}
		done, handled, err := s.concludePlatformRaise(ctx, r, resend, namespace, endpoint, answer)
		if err != nil || handled {
			return done, err
		}
		return false, nil
	}
	// pending: the runner is still working on it.
	return false, nil
}

// concludePlatformRaise acts on an answer that ends the matter: a receipt for exactly this command, or the runner's
// failed status for it. handled is false for any other answer.
func (s *Server) concludePlatformRaise(ctx context.Context, r platformRaiseSettlement, intent platformSetDataQuotaIntent, namespace string, endpoint platformRuntimeEndpoint, observation platformSetDataQuotaObservation) (done, handled bool, err error) {
	if receipt, ok := validPlatformSetDataQuotaObservation(observation, intent, endpoint); ok {
		return true, true, s.commitPlatformRaiseSettlement(ctx, r, intent, namespace, receipt)
	}
	if failure := platformRaiseFailure(observation, intent, endpoint); failure != nil {
		var failed *platformDataEffectFailedError
		if errors.As(failure, &failed) {
			return true, true, s.revertPlatformRaiseRow(ctx, r, intent, platformRaiseFailedReceipt(intent, failed.code))
		}
		return false, true, failure
	}
	return false, false, nil
}

// commitPlatformRaiseSettlement applies the receipt of a raise that converged: the row and the namespace together, and
// a reviewed raise's operation completes with them.
func (s *Server) commitPlatformRaiseSettlement(ctx context.Context, r platformRaiseSettlement, intent platformSetDataQuotaIntent, namespace string, receipt journal.SetDataQuotaReceipt) error {
	encoded, err := platformEncodeRaiseReceipt(receipt)
	if err != nil {
		return err
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
	now, err := r.guard(e)
	if err != nil {
		return err
	}
	row, err := loadPlatformRaiseRow(e, r.id, r.slot, true)
	if err != nil || !row.exists || row.effectID != intent.effectID || row.digest != intent.command.PayloadSHA256 {
		return errors.Join(errAppPlatformStageEvidence, err)
	}
	if err := applyPlatformRaise(e, platformRaiseCommitRef{
		operation: r.id, installation: r.installation, namespace: namespace, slot: r.slot,
		effectID: row.effectID, digest: row.digest, runtimeID: row.runtimeID, epoch: row.epoch,
		admittedFence: row.admitted, observedFence: r.fence,
	}, receipt, encoded, now); err != nil {
		return err
	}
	if r.action == string(appplatform.ActionSetDataQuota) {
		if err := r.moveStandalone(e, now, true); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// revertPlatformRaiseRow closes the row of a raise the runner will never apply: its definite failure, or a proven
// absence. A reviewed raise's operation fails with it. An update's stays recovery_required for its rollback; its row
// is what the restore review waits for.
func (s *Server) revertPlatformRaiseRow(ctx context.Context, r platformRaiseSettlement, intent platformSetDataQuotaIntent, receipt string) error {
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return err
	}
	now, err := r.guard(e)
	if err != nil {
		return err
	}
	if err := revertPlatformRaiseRow(e, r.id, intent, receipt, now); err != nil {
		return err
	}
	if r.action == string(appplatform.ActionSetDataQuota) {
		if err := r.moveStandalone(e, now, false); err != nil {
			return err
		}
	}
	detail, _ := json.Marshal(struct {
		EffectID string `json:"effect_id"`
	}{intent.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_quota_reverted','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), r.id, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}

// moveStandalone takes a reviewed raise's operation out of recovery_required: complete when the runner's receipt
// shows the raise applied, failed (nothing applied) otherwise.
func (r platformRaiseSettlement) moveStandalone(e migExec, now int64, applied bool) error {
	model, err := appplatform.OperationFromSnapshot(r.lease.snapshot)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	var next appplatform.Operation
	stage, code := "complete", sql.NullString{}
	if applied {
		next, err = model.Recover(r.lease.snapshot.Revision)
	} else {
		next, err = model.Settle(r.lease.snapshot.Revision)
		stage, code = "failed", sql.NullString{String: "data_quota_unavailable", Valid: true}
	}
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='recovery_required' AND state_revision=? AND row_revision=?
		AND fence=? AND lease_owner IS NULL AND completed_ms IS NOT NULL`,
		stage, int64(next.Snapshot().Revision), code, now, r.id, r.installation, r.epoch, r.stateRevision, r.rowRevision, r.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	_, err = e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.data_quota_settled','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), r.id, fmt.Sprintf(`{"stage":%q}`, stage))
	return err
}
