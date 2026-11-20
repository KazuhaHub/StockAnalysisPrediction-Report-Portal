package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// A failure after the import began restores the pre-import checkpoint the operation kept (doc 185 section 5.3). The
// restore is an ordinary runner restore of that checkpoint, sent under the second effect number of the import's
// stage, so its journal row never collides with the import's own. If the restore cannot be proven either, the
// operation ends recovery_required and the repair path takes over.

const platformImportRollbackEffectNumber = 2

type platformImportRolledBackKey struct{}

// withPlatformImportRolledBack tells the finisher of an import that its data was restored.
func withPlatformImportRolledBack(ctx context.Context) context.Context {
	return context.WithValue(ctx, platformImportRolledBackKey{}, true)
}

func platformImportRolledBack(ctx context.Context) bool {
	rolled, _ := ctx.Value(platformImportRolledBackKey{}).(bool)
	return rolled
}

func platformImportRollbackCommand(expected platformOperationLease, runtime platformRuntimeRegistration, source platformCheckpointSource, checkpoint journal.CheckpointReceipt, recoverySetID, effectID string) (control.RestoreCommand, error) {
	command, err := platformRestoreCommand(expected, runtime, source, checkpoint, recoverySetID, effectID)
	if err != nil {
		return command, err
	}
	command.EffectNumber = strconv.Itoa(platformImportRollbackEffectNumber)
	if command.PayloadSHA256, err = control.RestoreLogicalDigest(command); err != nil {
		return command, errAppPlatformStageEvidence
	}
	return command, nil
}

// preparePlatformImportRollback loads the evidence and records the restore effect, once. It reports false when the
// import never began, so there is nothing to undo.
func (s *Server) preparePlatformImportRollback(ctx context.Context, expected platformOperationLease) (platformRestoreIntent, bool, error) {
	var zero platformRestoreIntent
	if expected.snapshot.Action != appplatform.ActionImportData || !expected.snapshot.Managed {
		return zero, false, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, false, err
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
	runtime, seed, err := s.platformDataRuntime(e, expected.controllerEpoch, dek)
	if err != nil {
		return zero, false, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, false, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, false, err
	}
	plan, source, _, err := s.loadPlatformAdmittedManagedEnable(e, expected, trust, facts.now)
	if err != nil || plan.Import == nil || plan.RecoverySetID == "" {
		return zero, false, nil
	}
	var importCount int
	if err := e.queryRow(`SELECT COUNT(*) FROM app_platform_operation_effects WHERE operation_id=? AND stage='import' AND effect_number=1`, expected.id).Scan(&importCount); err != nil {
		return zero, false, err
	}
	if importCount == 0 {
		return zero, false, nil
	}
	checkpoint, err := loadPlatformImportCheckpoint(e, expected, plan)
	if err != nil {
		return zero, false, err
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var effectID, kind, digest, effectState, receipt string
	var admitted, observed int64
	effectErr := e.queryRow(`SELECT effect_id,kind,payload_sha256,admitted_fence,observed_fence,state,receipt_json FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='import' AND effect_number=?`+lock, expected.id, platformImportRollbackEffectNumber).Scan(&effectID, &kind, &digest, &admitted, &observed, &effectState, &receipt)
	if effectErr != nil && !errors.Is(effectErr, sql.ErrNoRows) {
		return zero, false, effectErr
	}
	fresh := errors.Is(effectErr, sql.ErrNoRows)
	if fresh {
		if effectID, err = platformNewUUID(); err != nil {
			return zero, false, err
		}
	}
	command, err := platformImportRollbackCommand(expected, runtime, source, checkpoint, plan.RecoverySetID, effectID)
	if err != nil {
		return zero, false, err
	}
	if fresh {
		if _, err := e.exec(`INSERT INTO app_platform_operation_effects(operation_id,stage,effect_number,effect_id,kind,payload_sha256,runtime_id,controller_epoch,admitted_fence,observed_fence,state,receipt_json,created_ms,updated_ms)
			VALUES(?,'import',?,?,'restore',?,?,?,?,0,'prepared','{}',?,?)`,
			expected.id, platformImportRollbackEffectNumber, effectID, command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, facts.now, facts.now); err != nil {
			return zero, false, err
		}
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.data_import_rollback_intent','app_platform_operation',?,'{}','')`,
			time.UnixMilli(facts.now).UTC().Format(time.RFC3339), expected.id); err != nil {
			return zero, false, err
		}
	} else if kind != "restore" || digest != command.PayloadSHA256 || admitted != expected.fence || observed != 0 || effectState != "prepared" || receipt != "{}" {
		return zero, false, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil || now > math.MaxInt64-25000 {
		return zero, false, errAppPlatformLeaseConflict
	}
	issued := time.Now().UnixMilli()
	expires := min(issued+25000, expected.leaseUntilMS-1000)
	if issued < now-1000 || issued > now+1000 || expires <= issued {
		return zero, false, errAppPlatformLeaseConflict
	}
	command.IssuedMS, command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	authority := platformRuntimeAuthority(runtime)
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	intent := platformRestoreIntent{recoverySetID: plan.RecoverySetID, effectID: effectID, runtime: runtime, authority: authority, command: command, checkpoint: checkpoint, source: source}
	if intent.raw, intent.signature, err = control.SignRestore(command, key, authority, issued); err != nil {
		return zero, false, err
	}
	if intent.readSignature, err = control.SignRestoreRead(intent.raw, key, authority, issued); err != nil {
		return zero, false, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, false, err
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	return intent, true, nil
}

// rollbackPlatformImportData restores the pre-import checkpoint of a failed import and reports whether it did.
func (s *Server) rollbackPlatformImportData(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (bool, error) {
	intent, needed, err := s.preparePlatformImportRollback(ctx, expected)
	if err != nil || !needed {
		return false, err
	}
	observation, err := platformExecuteRestore(ctx, endpoint, intent)
	if err != nil {
		var readErr error
		observation, readErr = platformReadRestoreReceipt(ctx, endpoint, intent)
		if readErr != nil {
			return false, errors.Join(err, readErr)
		}
	}
	if err := s.commitPlatformImportRollback(ctx, expected, intent, endpoint, observation); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) commitPlatformImportRollback(ctx context.Context, expected platformOperationLease, intent platformRestoreIntent, endpoint platformRuntimeEndpoint, observation platformRestoreObservation) error {
	receipt, ok := validPlatformRestoreObservation(observation, intent, endpoint)
	if !ok {
		return errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return errAppPlatformStageEvidence
	}
	if parsed, parseErr := journal.ParseRestoreReceipt(encoded); parseErr != nil || parsed != receipt {
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
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return err
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='import' AND effect_number=? AND effect_id=? AND kind='restore' AND payload_sha256=?
		AND controller_epoch=? AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, platformImportRollbackEffectNumber, intent.effectID, intent.command.PayloadSHA256,
		expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active'`,
		mustPlatformCheckpointInt(receipt.UsedBytes), now, intent.checkpoint.NamespaceID, expected.installationID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.data_import_rolled_back','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, fmt.Sprintf(`{"checkpoint_id":%q,"entries":%q}`, intent.checkpoint.CheckpointID, receipt.Entries)); err != nil {
		return err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return err
	}
	return tx.Commit()
}

var _ = bytes.Equal
