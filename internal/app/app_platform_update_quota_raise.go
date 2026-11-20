package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// An approved update raises the data quota (doc 185 section 3.2).
//
// The raise runs inside the update's migrate stage, in this order of effects:
//
//  1. the source removal (migrate/1);
//  2. the raise (migrate/platformUpdateRaiseEffectNumber), when the finalized plan carries data_quota;
//  3. the migrate-data effect (migrate/2), when the plan has a migration.
//
// It follows the checkpoint, which was cut at the old quota and always fits, and it comes before a migration whose
// payload carries the quota and whose runner check compares the live limit with it. The operation's stages do not
// change, so every stored operation revision stays valid. An update with no raise has no raise row and its plan has no
// data_quota key: nothing about it differs from an update before this existed.
//
// A raise that fails leaves the quota raised or not exactly as the runner says, never half: the transaction that
// applies a receipt also updates the namespace row. Once applied, the raise stays in force if the update later fails
// and rolls back. A checkpoint restores contents, not limits, the runner's reservations already hold the new size,
// the administrator approved the raise as part of the reviewed update, and lowering would need a native path that
// version 1 does not have.

// platformUpdateRaiseFacts is what the raise command binds, from the finalized plan and the update's own applied
// checkpoint receipt: the checkpoint names the install evidence it was cut from, and the plan the limits.
func platformUpdateRaiseFacts(plan platformManagedUpdatePlan, checkpoint journal.CheckpointReceipt) (platformRaiseFacts, error) {
	q := plan.DataQuota
	if q == nil || !q.RaiseApplied || checkpoint.NamespaceID != plan.DataNamespaceID || checkpoint.ReleaseSHA256 != plan.SourceReleaseSHA256 {
		return platformRaiseFacts{}, errAppPlatformStageEvidence
	}
	return platformRaiseFacts{
		namespaceID: plan.DataNamespaceID, releaseSHA256: plan.SourceReleaseSHA256, projectID: checkpoint.ProjectID,
		namespaceInode: checkpoint.NamespaceInode, device: checkpoint.Device, dataEffectID: checkpoint.SourceDataEffectID,
		dataReceiptSHA256: checkpoint.SourceDataReceiptSHA256, dataFormat: plan.DataFormat, writerFence: plan.SourceWriterFence,
		oldBytes: q.CurrentBytes, oldInodes: q.CurrentInodes, newBytes: q.NewBytes, newInodes: q.NewInodes,
	}, nil
}

// platformPlanDeclaresRaise reports whether the operation's stored plan declares an update raise. It only decides
// which effect the migrate stage runs next: every command the stage then signs is judged against the full evidence.
func platformPlanDeclaresRaise(e migExec, operation string) (bool, error) {
	var raw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.action='update'`, operation).Scan(&raw); err != nil {
		return false, err
	}
	var probe struct {
		DataQuota *struct {
			RaiseApplied bool `json:"raise_applied"`
		} `json:"data_quota"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return false, errAppPlatformStageEvidence
	}
	return probe.DataQuota != nil && probe.DataQuota.RaiseApplied, nil
}

// platformUpdateRaiseEvidence is what a raise of the leased update binds.
type platformUpdateRaiseEvidence struct {
	plan       platformManagedUpdatePlan
	descriptor []byte
	facts      platformRaiseFacts
}

// loadPlatformUpdateRaiseEvidence gathers the evidence of the update's raise and refuses unless it is the step the
// update is at: the source is removed, the migration (which needs the raised quota) has not begun, and the quota
// is still the one the plan was reviewed over.
func loadPlatformUpdateRaiseEvidence(e migExec, lease platformOperationLease, runtime platformRuntimeRegistration, now int64) (platformUpdateRaiseEvidence, error) {
	var zero platformUpdateRaiseEvidence
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, false, 1)
	if err != nil {
		return zero, err
	}
	plan, descriptor, err := loadPlatformManagedUpdatePlan(e, lease, source)
	if err != nil || plan.Action != "" || plan.DataQuota == nil || !plan.DataQuota.RaiseApplied {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, lease, plan)
	if err != nil {
		return zero, err
	}
	var kind, state string
	if err := e.queryRow(`SELECT kind,state FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=1`, lease.id).
		Scan(&kind, &state); err != nil || kind != "remove-code" || state != "applied" {
		return zero, fmt.Errorf("%w: raise needs the applied source removal", errAppPlatformStageEvidence)
	}
	var migrations int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='migrate' AND effect_number=2`, lease.id).Scan(&migrations); err != nil || migrations != 0 {
		return zero, fmt.Errorf("%w: raise comes before the migration", errAppPlatformStageEvidence)
	}
	facts, err := platformUpdateRaiseFacts(plan, checkpoint)
	if err != nil {
		return zero, err
	}
	if source.quotaBytes != itoa64(facts.oldBytes) || source.quotaInodes != itoa64(facts.oldInodes) ||
		source.namespaceID != facts.namespaceID || source.dataEffectID != facts.dataEffectID ||
		source.sourceDataReceiptSHA256 != facts.dataReceiptSHA256 || source.projectID != facts.projectID ||
		source.namespaceInode != facts.namespaceInode || source.device != facts.device {
		return zero, fmt.Errorf("%w: raise is not over the recorded evidence", errAppPlatformStageEvidence)
	}
	return platformUpdateRaiseEvidence{plan: plan, descriptor: descriptor, facts: facts}, nil
}

// preparePlatformUpdateRaise records the raise effect, once, and signs it. A retry or a lease takeover reuses the
// same effect and digest, signed again at the fence it was admitted under.
func (s *Server) preparePlatformUpdateRaise(ctx context.Context, expected platformOperationLease) (platformSetDataQuotaIntent, error) {
	var zero platformSetDataQuotaIntent
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageMigrate ||
		ctx == nil || ctx.Err() != nil {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, fmt.Errorf("raise runtime: %w", err)
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformUpdateRaiseEvidence(e, expected, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	row, err := loadPlatformRaiseRow(e, expected.id, platformUpdateRaiseSlot, true)
	if err != nil {
		return zero, err
	}
	effectID, admitted := row.effectID, row.admitted
	if !row.exists {
		if effectID, err = platformNewUUID(); err != nil {
			return zero, err
		}
		admitted = expected.fence
	}
	command, err := platformRaiseCommand(runtime, expected.controllerEpoch, expected.installationID, expected.id, expected.snapshot.Generation,
		admitted, ev.facts, effectID)
	if err != nil {
		return zero, err
	}
	if !row.exists {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'migrate',?,?,'set-data-quota',?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, platformUpdateRaiseEffectNumber, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now, facts.now); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID string `json:"effect_id"`
		}{effectID})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.data_quota_intent','app_platform_operation',?,?,'')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
		row = platformRaiseRow{exists: true, effectID: effectID, kind: "set-data-quota", digest: command.PayloadSHA256, state: "prepared", receipt: "{}", updated: facts.now,
			admitted: admitted, runtimeID: runtime.id, epoch: expected.controllerEpoch}
	} else if row.kind != "set-data-quota" || row.digest != command.PayloadSHA256 || row.admitted < 1 || row.admitted > expected.fence || row.observed != 0 ||
		row.state != "prepared" || row.runtimeID != runtime.id || row.epoch != expected.controllerEpoch {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformLeaseConflict
	}
	issued, expires, err := platformRaiseWindow(now, expected.leaseUntilMS)
	if err != nil {
		return zero, err
	}
	intent, err := platformSignRaise(e, seed, runtime, row, platformUpdateRaiseSlot, expected.id, command, ev.descriptor, issued, expires, true)
	if err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// endPlatformUpdateRaise ends the update for review after the runner's definite answer about the raise: the row is
// closed reverted with that answer, and the operation ends recovery_required with the code that says why. Nothing
// was changed by the raise, so the namespace row and the quota chain are as they were, and the rollback restore is
// allowed (a reverted row blocks nothing).
func (s *Server) endPlatformUpdateRaise(ctx context.Context, expected platformOperationLease, intent platformSetDataQuotaIntent, receipt, code string) error {
	if err := s.finishPlatformManagedUpdateWith(ctx, expected, func(e migExec, now int64) error {
		return revertPlatformRaiseRow(e, expected.id, intent, receipt, now)
	}, code); err != nil {
		return err
	}
	return errPlatformMigrationEnded
}

// dispatchPlatformUpdateRaise sends the raise of the leased update and commits what the runner answers.
func (s *Server) dispatchPlatformUpdateRaise(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformUpdateRaise(ctx, expected)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf("quota raise prepare: %w", err)
	}
	observation, err := sendPlatformSetDataQuota(ctx, endpoint, intent)
	if err != nil {
		// The runner refused the raise for lack of room: nothing was reserved, and it holds the effect reverted.
		if refusal, ok := platformCapacityRefusalOf(err); ok && refusal.Matches(journal.CapacityIdentityOfHeader(intent.command.CommandHeader)) {
			raw, marshalErr := json.Marshal(refusal)
			if marshalErr != nil || len(raw) > 16384 {
				return platformOperationLease{}, errAppPlatformStageEvidence
			}
			if recordErr := s.recordPlatformCapacityRefusal(ctx, expected, refusal); recordErr != nil {
				return platformOperationLease{}, recordErr
			}
			return platformOperationLease{}, s.endPlatformUpdateRaise(ctx, expected, intent, string(raw), "data_quota_capacity")
		}
		// The runner's definite failure: the row is reverted, the quota is as it was, and no retry would answer differently.
		var failed *platformDataEffectFailedError
		if errors.As(err, &failed) {
			return platformOperationLease{}, s.endPlatformUpdateRaise(ctx, expected, intent, platformRaiseFailedReceipt(intent, failed.code), "data_quota_unavailable")
		}
		return platformOperationLease{}, err
	}
	return s.commitPlatformUpdateRaise(ctx, expected, intent, endpoint, observation)
}

// commitPlatformUpdateRaise applies the runner's receipt in one fenced transaction: the effect row, the namespace row
// (compare-and-set from the old limits), the audit row and the stage move. The operation goes on to the start when it
// has no migration, and stays at the migrate stage for the migration otherwise.
func (s *Server) commitPlatformUpdateRaise(ctx context.Context, expected platformOperationLease, intent platformSetDataQuotaIntent, endpoint platformRuntimeEndpoint, observation platformSetDataQuotaObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageMigrate || expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	receipt, ok := validPlatformSetDataQuotaObservation(observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := platformEncodeRaiseReceipt(receipt)
	if err != nil {
		return zero, err
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformUpdateRaiseEvidence(e, expected, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	row, err := loadPlatformRaiseRow(e, expected.id, platformUpdateRaiseSlot, true)
	if err != nil {
		return zero, err
	}
	if !row.exists || row.effectID != intent.effectID || row.digest != intent.command.PayloadSHA256 || row.state != "prepared" ||
		row.runtimeID != runtime.id || runtime.id != intent.runtime.id ||
		ev.facts.newBytes != mustPlatformCheckpointInt(receipt.NewBytes) || ev.facts.newInodes != mustPlatformCheckpointInt(receipt.NewInodes) {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := applyPlatformRaise(e, platformRaiseCommitRef{
		operation: expected.id, installation: expected.installationID, namespace: ev.plan.DataNamespaceID, slot: platformUpdateRaiseSlot,
		effectID: row.effectID, digest: row.digest, runtimeID: runtime.id, epoch: expected.controllerEpoch,
		admittedFence: row.admitted, observedFence: expected.fence,
	}, receipt, encoded, now); err != nil {
		return zero, err
	}
	result := expected
	if ev.plan.Migration == nil {
		model, err := appplatform.OperationFromSnapshot(expected.snapshot)
		if err != nil {
			return zero, errAppPlatformStageEvidence
		}
		next, err := model.Advance(expected.snapshot.Revision, appplatform.StageStart)
		if err != nil {
			return zero, errAppPlatformStageEvidence
		}
		result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
		changed, err := e.exec(`UPDATE app_platform_operations SET stage='start',state_revision=?,row_revision=row_revision+1,updated_ms=?
			WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='migrate' AND state_revision=? AND row_revision=? AND fence=?
			AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
			int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
			int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}

// signPlatformUpdateCapacityQuery signs the question a review and the validate stage ask the runner: can its
// capacity admission hold the namespace at the new quota. The runner verifies it against the installed release.
func (s *Server) signPlatformUpdateCapacityQuery(e migExec, epoch, installation, release string, q *platformUpdateQuotaReview) (platformDataQueryRequest, error) {
	var descriptor []byte
	if err := e.queryRow(`SELECT descriptor FROM app_platform_releases WHERE sha256=?`, release).Scan(&descriptor); err != nil {
		return platformDataQueryRequest{}, errAppPlatformPlanChanged
	}
	var namespace string
	if err := e.queryRow(`SELECT id FROM app_platform_data_namespaces WHERE owner_installation_id=? AND state='active'`, installation).Scan(&namespace); err != nil {
		return platformDataQueryRequest{}, errAppPlatformPlanChanged
	}
	return s.signPlatformDataQuery(e, epoch, release, descriptor, installation, control.DataQueryPayload{
		Operation: control.DataQueryQuotaCapacity, NamespaceID: namespace,
		QuotaBytes: itoa64(q.NewBytes), QuotaInodes: itoa64(q.NewInodes),
	})
}

// platformApplyQuotaCapacity records the runner's capacity answer on the review: "fits", or "short" with the
// dimension and the shortfall.
func platformApplyQuotaCapacity(q *platformUpdateQuotaReview, report journal.DataQueryReport) {
	q.Capacity, q.CapacityDimension, q.CapacityRequested, q.CapacityRemaining = "fits", "", 0, 0
	if !report.CapacityFits {
		q.Capacity, q.CapacityDimension = "short", report.CapacityDim
		q.CapacityRequested, q.CapacityRemaining = mustPlatformCheckpointInt(report.CapacityRequired), mustPlatformCheckpointInt(report.CapacityLeft)
	}
}

// platformAppliedRaiseCount counts the applied raise rows of an operation: the extra effects an update that raised
// the quota carries beside stop, snapshot, source removal and start. A row in any other state still counts in the
// registration's total and so makes its comparison with the base fail closed.
func platformAppliedRaiseCount(e migExec, operation string) (int, error) {
	var n int
	err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota' AND state='applied'`, operation).Scan(&n)
	return n, err
}

// platformKeepStoredQuotaAnswer copies into a rebuilt review what the stored review recorded that a rebuild cannot
// know: the runner's capacity answer. A review stored before the update raised the quota (raise_applied false) is
// kept exactly as it was, after proving its facts are still true, so an update admitted from it runs on with no raise.
// It reports false when the two disagree on whether there is a difference at all, or on the facts.
func platformKeepStoredQuotaAnswer(rebuilt, stored *platformUpdateQuotaReview) bool {
	if rebuilt == nil || stored == nil {
		return rebuilt == stored
	}
	if !stored.RaiseApplied {
		if stored.CurrentBytes != rebuilt.CurrentBytes || stored.CurrentInodes != rebuilt.CurrentInodes ||
			stored.RequestedBytes != rebuilt.RequestedBytes || stored.RequestedInodes != rebuilt.RequestedInodes ||
			stored.RaiseBytes != rebuilt.RaiseBytes || stored.RaiseInodes != rebuilt.RaiseInodes {
			return false
		}
		*rebuilt = *stored
		return true
	}
	rebuilt.Capacity, rebuilt.CapacityDimension = stored.Capacity, stored.CapacityDimension
	rebuilt.CapacityRequested, rebuilt.CapacityRemaining = stored.CapacityRequested, stored.CapacityRemaining
	return true
}

// platformCheckPlanQuota proves a finalized plan's data_quota is what the review would have written: only an
// update has one, it is re-derived from the target descriptor over the quota the plan was reviewed at, and that
// quota is a link of the chain.
func platformCheckPlanQuota(e migExec, installation string, action appplatform.Action, plan platformManagedUpdatePlan, manifest appplatform.Manifest) error {
	if plan.DataQuota == nil {
		return nil
	}
	q := *plan.DataQuota
	if action != appplatform.ActionUpdate || !q.RaiseApplied || q.Capacity != "fits" {
		return fmt.Errorf("%w: plan quota", errAppPlatformStageEvidence)
	}
	derived := platformUpdateQuotaDifference(q.CurrentBytes, q.CurrentInodes, manifest)
	if derived == nil || !derived.sameFacts(&q) {
		return fmt.Errorf("%w: plan quota is not the descriptor's", errAppPlatformStageEvidence)
	}
	chain, err := platformNamespaceQuotaChain(e, installation, plan.DataNamespaceID)
	if err != nil {
		return err
	}
	if !platformQuotaInChain(chain, q.CurrentBytes, q.CurrentInodes) {
		return fmt.Errorf("%w: plan quota is not a link of the chain", errAppPlatformStageEvidence)
	}
	return nil
}

// platformRequireSettledRaise refuses a recovery choice while the failed operation holds a quota raise with no verdict:
// the choice depends on whether the runner raised the limits.
func platformRequireSettledRaise(e migExec, operation string) error {
	var open int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind='set-data-quota' AND state NOT IN ('applied','reverted')`, operation).Scan(&open); err != nil {
		return err
	}
	if open != 0 {
		return errAppPlatformQuotaRaiseUnsettled
	}
	return nil
}

// checkPlatformUpdateRaiseCapacity asks the runner, at the validate stage, whether its capacity can hold the update's
// quota raise. When it cannot the update ends failed with no effects and the code data_quota_capacity: nothing was
// stopped, so the app keeps running at the old release and the old quota. The race that remains, capacity taken
// between this answer and the raise, ends recovery_required through the refusal of the raise itself.
func (s *Server) checkPlatformUpdateRaiseCapacity(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) error {
	var query platformDataQueryRequest
	asked := false
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return err
	}
	admitted, err := s.loadPlatformAdmittedManagedUpdate(e, expected, trust, facts.now)
	if err != nil {
		return err
	}
	if q := admitted.plan.DataQuota; q != nil && q.RaiseApplied {
		if query, err = s.signPlatformUpdateCapacityQuery(e, expected.controllerEpoch, expected.installationID, admitted.plan.SourceReleaseSHA256, q); err != nil {
			return err
		}
		asked = true
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !asked {
		return nil
	}
	report, err := s.queryPlatformData(ctx, endpoint, query)
	if err != nil {
		return fmt.Errorf("quota raise capacity: %w", err)
	}
	if report.CapacityFits {
		return nil
	}
	return s.failPlatformUpdateBeforeEffects(ctx, expected, "data_quota_capacity")
}

// failPlatformUpdateBeforeEffects ends a managed update that has done nothing yet (no effect, no stopped source) as
// failed with the given code, and reports it so the scheduler lets go. It refuses an update that holds any effect.
func (s *Server) failPlatformUpdateBeforeEffects(ctx context.Context, expected platformOperationLease, code string) error {
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed || expected.snapshot.Stage != appplatform.StageValidate {
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
	var effects int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND kind NOT IN (`+platformInstallationNeutralEffectKinds+`)`, expected.id).Scan(&effects); err != nil || effects != 0 {
		return errAppPlatformStageEvidence
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
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=?,completed_ms=?,updated_ms=?,lease_owner=NULL,lease_until_ms=NULL
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage=? AND state_revision=? AND row_revision=? AND fence=?
		AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		string(failed.Snapshot().Stage), int64(failed.Snapshot().Revision), code, now, now, expected.id, expected.installationID, expected.controllerEpoch,
		string(expected.snapshot.Stage), int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		Code  string `json:"code"`
		Stage string `json:"stage"`
	}{code, string(expected.snapshot.Stage)})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.update_failed','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return errPlatformMigrationEnded
}
