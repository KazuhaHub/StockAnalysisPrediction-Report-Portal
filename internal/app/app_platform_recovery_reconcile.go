package app

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"log"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// Recovery-mode reconciliation of an import the scheduler gave up on.
//
// A restore import that ended recovery_required has an effect the runner may have applied. It holds no
// lease, so nothing would ever read the effect back, and its grant stays issued: the installation could
// be neither imported again nor resumed. While the Portal is paused and the scheduler has nothing else
// to run, this reads the ORIGINAL effect back from the runner, under its own identity and at the fence
// it was admitted under, and settles it:
//
//   - an applied receipt settles the effect, consumes the grant and completes the operation;
//   - a definite absence, once every command signed for the effect has expired, reverts the effect and
//     revokes the grant, so a new review can be admitted;
//   - anything else changes nothing and is read again later.
//
// It never sends the import: only a read-receipt leaves the Portal. A resume left with an unsettled
// start is not reconciled here: its start is bound to the lease fence it ran under, which a finished
// operation can no longer reach, and the runner reports no typed absence for it.

const platformRecoveryReconcileInterval = 30 * time.Second

// platformRecoveryImportCandidates lists finished imports whose effect is still unsettled, as the
// read-only identity of the operation each belongs to.
func (s *Server) platformRecoveryImportCandidates(ctx context.Context) ([]platformOperationLease, error) {
	rows, err := s.st.db.QueryContext(ctx, s.st.bind(`SELECT o.id,o.installation_id,o.controller_epoch,o.fence,o.target_generation,o.state_revision,o.row_revision
		FROM app_platform_operations o JOIN app_platform_operation_effects f ON f.operation_id=o.id AND f.stage='restore_data' AND f.effect_number=1
		WHERE o.action='restore_import' AND o.managed=1 AND o.stage='recovery_required' AND o.completed_ms IS NOT NULL
		AND f.kind='import-checkpoint' AND f.state IN ('prepared','unknown')
		ORDER BY o.completed_ms,o.id LIMIT 8`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []platformOperationLease
	for rows.Next() {
		var id, installation, epoch string
		var fence, generation, stateRevision, rowRevision int64
		if err := rows.Scan(&id, &installation, &epoch, &fence, &generation, &stateRevision, &rowRevision); err != nil {
			return nil, err
		}
		if !platformValidUUID(id) || !platformValidUUID(installation) || generation < 1 || stateRevision < 1 || rowRevision < 1 {
			continue
		}
		result = append(result, platformOperationLease{
			id: id, installationID: installation, controllerEpoch: epoch, fence: fence, rowRevision: rowRevision,
			snapshot: appplatform.OperationSnapshot{Action: appplatform.ActionRestoreImport, Managed: true, Generation: uint64(generation), Revision: uint64(stateRevision), Stage: appplatform.StageRecoveryRequired},
		})
	}
	return result, rows.Err()
}

// reconcilePlatformRecoveryImports settles what it can and returns how many imports it settled.
func (s *Server) reconcilePlatformRecoveryImports(ctx context.Context, endpoint platformRuntimeEndpoint) (int, error) {
	if endpoint.path == "" || !s.platformRecoveryPaused(ctx) {
		return 0, nil
	}
	candidates, err := s.platformRecoveryImportCandidates(ctx)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, lease := range candidates {
		done, err := s.reconcilePlatformRecoveryImport(ctx, lease, endpoint)
		if err != nil {
			if ctx.Err() != nil {
				return settled, ctx.Err()
			}
			log.Printf("app-platform recovery reconcile %s: %v", lease.id, err)
			continue
		}
		if done {
			settled++
		}
	}
	return settled, nil
}

func (s *Server) reconcilePlatformRecoveryImport(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (bool, error) {
	intent, err := s.signPlatformRecoveryImportRead(ctx, lease)
	if err != nil {
		return false, err
	}
	observation, err := s.platformImportExecutor().Read(ctx, endpoint, intent)
	if errors.Is(err, errPlatformImportAbsent) {
		return s.revertPlatformRecoveryImport(ctx, lease, intent)
	}
	if err != nil {
		return false, nil
	}
	return s.commitPlatformRecoveryImport(ctx, lease, intent, endpoint, observation)
}

func platformRecoveryPaused(e migExec) (bool, error) {
	var paused string
	err := e.queryRow(`SELECT COALESCE((SELECT v FROM meta WHERE k='app_platform_restore_paused'),'')`).Scan(&paused)
	return paused == "1", err
}

// signPlatformRecoveryImportRead signs the original effect for a read. The command is signed afresh, but
// it names the effect's own identity and the fence it was admitted under, and its expiry is recorded on
// the effect like every other command signed for it.
func (s *Server) signPlatformRecoveryImportRead(ctx context.Context, lease platformOperationLease) (platformImportIntent, error) {
	var zero platformImportIntent
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
	if paused, err := platformRecoveryPaused(e); err != nil || !paused {
		return zero, errAppPlatformControlPaused
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		return zero, err
	}
	ev, err := loadPlatformRestoreImportEvidence(e, s, lease)
	if err != nil || ev.plan.RuntimeID != runtime.id {
		return zero, errors.Join(errAppPlatformStageEvidence, err)
	}
	intent, _, err := loadPlatformImportIntent(e, s, lease, runtime, ev, now, false)
	if err != nil {
		return zero, err
	}
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 || issued > math.MaxInt64-25000 {
		return zero, errAppPlatformLeaseConflict
	}
	expires := issued + 25000
	intent.command.IssuedMS, intent.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND state=? AND observed_fence=0 AND receipt_json=?`,
		platformImportPendingReceipt(max(intent.priorSignedUntil, expires)), lease.id, intent.effectID, intent.effectState, intent.pendingReceipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if intent.raw, intent.signature, err = control.SignImportCheckpoint(intent.command, key, intent.authority, issued); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return intent, nil
}

// commitPlatformRecoveryImport settles an applied receipt: the effect, the grant and the operation in one
// transaction, the grant by a conditional update that must change exactly one row.
func (s *Server) commitPlatformRecoveryImport(ctx context.Context, lease platformOperationLease, intent platformImportIntent, endpoint platformRuntimeEndpoint, observation platformImportObservation) (bool, error) {
	receipt, ok := validPlatformImportObservation(observation, intent, endpoint)
	if !ok {
		return false, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return false, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return false, err
	}
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return false, err
	}
	if paused, err := platformRecoveryPaused(e); err != nil || !paused {
		return false, errAppPlatformControlPaused
	}
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return false, err
	}
	defer clear(seed)
	now, err := platformDatabaseNow(e)
	if err != nil {
		return false, err
	}
	ev, err := loadPlatformRestoreImportEvidence(e, s, lease)
	if err != nil || ev.plan.RuntimeID != runtime.id {
		return false, errors.Join(errAppPlatformStageEvidence, err)
	}
	current, _, err := loadPlatformImportIntent(e, s, lease, runtime, ev, now, false)
	if err != nil || current.effectID != intent.effectID || current.command.PayloadSHA256 != intent.command.PayloadSHA256 {
		return false, errors.Join(errAppPlatformStageEvidence, err)
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND kind='import-checkpoint'
		AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state=? AND observed_fence=0 AND receipt_json=?`,
		current.admittedFence, string(encoded), now, lease.id, current.effectID, current.command.PayloadSHA256, runtime.id,
		lease.controllerEpoch, current.admittedFence, current.effectState, current.pendingReceipt)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	changed, err = e.exec(`UPDATE app_platform_import_grants SET state='consumed',consumed_ms=?,updated_ms=?
		WHERE id=? AND recovery_set_id=? AND runtime_id=? AND controller_epoch=? AND state='issued'`,
		now, now, ev.grantID, ev.plan.RecoverySetID, runtime.id, lease.controllerEpoch)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	model, err := appplatform.OperationFromSnapshot(lease.snapshot)
	if err != nil {
		return false, errAppPlatformStageEvidence
	}
	complete, err := model.Recover(lease.snapshot.Revision)
	if err != nil {
		return false, errAppPlatformStageEvidence
	}
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='complete',state_revision=?,row_revision=row_revision+1,error_code=NULL,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='recovery_required' AND state_revision=? AND row_revision=? AND completed_ms IS NOT NULL`,
		int64(complete.Snapshot().Revision), now, lease.id, lease.installationID, lease.controllerEpoch, int64(lease.snapshot.Revision), lease.rowRevision)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		GrantID       string `json:"grant_id"`
		Entries       string `json:"entries"`
	}{ev.plan.RecoverySetID, ev.grantID, receipt.Entries})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_reconciled','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// revertPlatformRecoveryImport settles a definite absence. It is final only after every command signed
// for the effect before this read has expired; until then it changes nothing.
func (s *Server) revertPlatformRecoveryImport(ctx context.Context, lease platformOperationLease, intent platformImportIntent) (bool, error) {
	tx, err := s.st.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	e := migExec{s: s.st, ex: platformContextTx{Tx: tx, ctx: ctx}}
	if err := lockOUMutation(e); err != nil {
		return false, err
	}
	if paused, err := platformRecoveryPaused(e); err != nil || !paused {
		return false, errAppPlatformControlPaused
	}
	now, err := platformDatabaseNow(e)
	if err != nil {
		return false, err
	}
	if now <= intent.priorSignedUntil+platformImportAbsenceMarginMS {
		return false, nil
	}
	var planRaw string
	if err := e.queryRow(`SELECT p.plan_json FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id WHERE o.id=? AND o.action='restore_import'`, lease.id).Scan(&planRaw); err != nil {
		return false, errAppPlatformStageEvidence
	}
	var plan platformRestoreImportPlan
	if json.Unmarshal([]byte(planRaw), &plan) != nil || plan.InstallationID != lease.installationID {
		return false, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='reverted',updated_ms=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND state=? AND observed_fence=0`,
		now, lease.id, intent.effectID, intent.effectState)
	if err := platformLeaseCAS(changed, err); err != nil {
		return false, err
	}
	if _, err := e.exec(`UPDATE app_platform_import_grants SET state='revoked',updated_ms=? WHERE state='issued' AND recovery_set_id=? AND runtime_id=?`,
		now, plan.RecoverySetID, plan.RuntimeID); err != nil {
		return false, err
	}
	// Every effect the import left is now reverted, so the operation is a clean failure rather than a
	// recovery the launch gate waits on for ever.
	if err := settlePlatformRecoveryOperation(e, lease.id, "", now); err != nil {
		return false, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		EffectID      string `json:"effect_id"`
	}{plan.RecoverySetID, intent.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.restore_import_reverted','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// settlePlatformRecoveryOperation moves a finished operation that stayed recovery_required to failed. It
// is admitted only while none of the operation's effects is unsettled, so it can never hide an effect the
// runner may have applied, and it goes through the model's one transition out of recovery_required. An
// empty code keeps the error code the operation already carries.
func settlePlatformRecoveryOperation(e migExec, operation, code string, now int64) error {
	var action, stage string
	var managed int
	var generation, revision, rowRevision int64
	if err := e.queryRow(`SELECT action,managed,target_generation,state_revision,row_revision,stage FROM app_platform_operations WHERE id=? AND completed_ms IS NOT NULL`, operation).
		Scan(&action, &managed, &generation, &revision, &rowRevision, &stage); err != nil || generation < 1 || revision < 1 {
		return errAppPlatformStageEvidence
	}
	var unsettled int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND state IN ('prepared','unknown')`, operation).Scan(&unsettled); err != nil {
		return err
	}
	if unsettled != 0 {
		return errAppPlatformStageEvidence
	}
	model, err := appplatform.OperationFromSnapshot(appplatform.OperationSnapshot{Action: appplatform.Action(action), Managed: managed == 1,
		Generation: uint64(generation), Revision: uint64(revision), Stage: appplatform.Stage(stage)})
	if err != nil {
		return errAppPlatformStageEvidence
	}
	settled, err := model.Settle(model.Snapshot().Revision)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operations SET stage=?,state_revision=?,row_revision=row_revision+1,error_code=COALESCE(NULLIF(?,''),error_code),updated_ms=?
		WHERE id=? AND stage='recovery_required' AND state_revision=? AND row_revision=? AND completed_ms IS NOT NULL`,
		string(settled.Snapshot().Stage), int64(settled.Snapshot().Revision), code, now, operation, revision, rowRevision)
	return platformLeaseCAS(changed, err)
}
