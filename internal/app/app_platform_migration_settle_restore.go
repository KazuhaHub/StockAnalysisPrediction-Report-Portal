package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

// loadPlatformSettlementRestoreCheckpoint reads the checkpoint the failed update itself took, which the
// settlement restore returns the namespace to. The recovery set must be the update's own, verified or already
// being restored by this settlement.
func loadPlatformSettlementRestoreCheckpoint(e migExec, expected platformOperationLease) (string, string, journal.CheckpointReceipt, int64, error) {
	var zero journal.CheckpointReceipt
	if expected.snapshot.Action != appplatform.ActionUpdate || expected.snapshot.Stage != appplatform.StageRecoveryRequired {
		return "", "", zero, 0, errAppPlatformStageEvidence
	}
	var planRaw, planSHA string
	if err := e.queryRow(`SELECT p.plan_json,p.plan_sha256 FROM app_platform_operations o JOIN app_platform_review_plans p ON p.id=o.review_plan_id
		WHERE o.id=? AND o.installation_id=? AND o.action='update' AND o.managed=1`, expected.id, expected.installationID).Scan(&planRaw, &planSHA); err != nil {
		return "", "", zero, 0, fmt.Errorf("%w: settlement plan row", errAppPlatformStageEvidence)
	}
	var plan platformManagedUpdatePlan
	if json.Unmarshal([]byte(planRaw), &plan) != nil || platformDigest([]byte(planRaw)) != planSHA || plan.Format != platformManagedUpdateFormat ||
		plan.InstallationID != expected.installationID || plan.CheckpointOperationID != expected.id ||
		!platformValidUUID(plan.RecoverySetID) || !platformValidUUID(plan.CheckpointEffectID) {
		return "", "", zero, 0, fmt.Errorf("%w: settlement plan", errAppPlatformStageEvidence)
	}
	lock := ""
	if e.s.driver == "postgres" {
		lock = " FOR UPDATE"
	}
	var checkpointID, manifest, state string
	var bytes int64
	if err := e.queryRow(`SELECT checkpoint_id,manifest_sha256,bytes,state FROM app_platform_recovery_sets WHERE id=? AND installation_id=? AND operation_id=?`+lock,
		plan.RecoverySetID, expected.installationID, expected.id).Scan(&checkpointID, &manifest, &bytes, &state); err != nil ||
		checkpointID != plan.CheckpointID || manifest != plan.CheckpointManifestSHA256 || bytes != plan.CheckpointArchiveBytes ||
		(state != "verified" && state != "restoring") {
		return "", "", zero, 0, fmt.Errorf("%w: settlement recovery set", errAppPlatformStageEvidence)
	}
	var effectID, payload, raw, effectState string
	if err := e.queryRow(`SELECT effect_id,payload_sha256,receipt_json,state FROM app_platform_operation_effects
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND state='applied'`, expected.id).Scan(&effectID, &payload, &raw, &effectState); err != nil ||
		effectID != plan.CheckpointEffectID {
		return "", "", zero, 0, fmt.Errorf("%w: settlement checkpoint effect", errAppPlatformStageEvidence)
	}
	receipt, err := journal.ParseCheckpointReceipt([]byte(raw))
	if err != nil || receipt.EffectID != effectID || receipt.PayloadSHA256 != payload || receipt.OperationID != expected.id ||
		receipt.CheckpointID != checkpointID || receipt.ManifestSHA256 != manifest || mustPlatformCheckpointInt(receipt.ArchiveBytes) != bytes ||
		receipt.ReleaseSHA256 != plan.SourceReleaseSHA256 || !platformCheckpointReceiptMatchesSourceOperation(receipt, expected.installationID) {
		return "", "", zero, 0, fmt.Errorf("%w: settlement checkpoint receipt", errAppPlatformStageEvidence)
	}
	return plan.RecoverySetID, effectID, receipt, bytes, nil
}

// signPlatformSettlementRestore signs the restore effect that returns the namespace to the update's
// checkpoint. It creates the effect only when create is set and none exists. The finished update holds no
// lease, so the command is bound to the fence it finished under and its expiry is recorded on the effect.
func (s *Server) signPlatformSettlementRestore(ctx context.Context, lease platformOperationLease, create, record bool) (platformRestoreIntent, bool, error) {
	var zero platformRestoreIntent
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, false, err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return zero, false, err
	}
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, false, 0)
	if err != nil {
		return zero, false, err
	}
	_, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, lease)
	if err != nil {
		return zero, false, err
	}
	intent, fresh, err := loadPlatformRestoreIntent(e, lease, runtime, source, checkpoint, now, create)
	if err != nil {
		return zero, false, err
	}
	var pending string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`, lease.id).Scan(&pending); err != nil {
		return zero, false, err
	}
	until, _ := platformImportSignedUntil(pending)
	issued := time.Now().UnixMilli()
	if issued < now-1000 || issued > now+1000 {
		return zero, false, errAppPlatformLeaseConflict
	}
	expires := issued + 25000
	intent.command.IssuedMS, intent.command.ExpiresMS = strconv.FormatInt(issued, 10), strconv.FormatInt(expires, 10)
	if record || fresh {
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET receipt_json=?
			WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND state='prepared' AND observed_fence=0 AND receipt_json=?`,
			platformImportPendingReceipt(max(until, expires)), lease.id, intent.effectID, pendingOrEmpty(pending))
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, false, err
		}
	}
	key := ed25519.NewKeyFromSeed(seed)
	defer clear(key)
	if intent.raw, intent.signature, err = control.SignRestore(intent.command, key, intent.authority, issued); err != nil {
		return zero, false, err
	}
	if intent.readSignature, err = control.SignRestoreRead(intent.raw, key, intent.authority, issued); err != nil {
		return zero, false, err
	}
	if err := tx.Commit(); err != nil {
		return zero, false, err
	}
	signedUntil := until
	intent.signedUntilBefore = signedUntil
	return intent, fresh, nil
}

func pendingOrEmpty(receipt string) string {
	if receipt == "" {
		return "{}"
	}
	return receipt
}

// commitPlatformSettlementRestore settles the restore effect from a verified receipt: the effect, the
// recovery set (back to verified) and the namespace's observed size in one transaction.
func (s *Server) commitPlatformSettlementRestore(ctx context.Context, lease platformOperationLease, intent platformRestoreIntent, endpoint platformRuntimeEndpoint, observation platformRestoreObservation) error {
	intent.raw = bytes.Clone(intent.raw)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformRestoreObservation(observation, intent, endpoint)
	if !ok {
		return errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return errAppPlatformStageEvidence
	}
	if parsed, err := journal.ParseRestoreReceipt(encoded); err != nil || parsed != receipt {
		return errAppPlatformStageEvidence
	}
	dek, err := s.dek()
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return err
	}
	defer clear(seed)
	now, err := platformSettlementGuard(e, lease)
	if err != nil {
		return err
	}
	source, err := loadPlatformCheckpointSourceMode(e, lease, runtime, now, false, 0)
	if err != nil {
		return err
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, lease)
	if err != nil {
		return err
	}
	current, _, err := loadPlatformRestoreIntent(e, lease, runtime, source, checkpoint, now, false)
	if err != nil || current.effectID != intent.effectID || !platformRestoreCommandsEqual(current.command, intent.command) {
		return errors.Join(errAppPlatformStageEvidence, err)
	}
	var pending string
	if err := e.queryRow(`SELECT receipt_json FROM app_platform_operation_effects WHERE operation_id=? AND stage='restore_data' AND effect_number=1`, lease.id).Scan(&pending); err != nil {
		return err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=? AND kind='restore' AND payload_sha256=?
		AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json=?`,
		lease.fence, string(encoded), now, lease.id, current.effectID, current.command.PayloadSHA256, runtime.id, lease.controllerEpoch, lease.fence, pending)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_recovery_sets SET state='verified',updated_ms=?
		WHERE id=? AND installation_id=? AND data_namespace_id=? AND checkpoint_id=? AND manifest_sha256=? AND bytes=?
		AND state='restoring' AND controller_epoch=? AND writer_fence=?`,
		now, recoverySetID, lease.installationID, checkpoint.NamespaceID, checkpoint.CheckpointID, checkpoint.ManifestSHA256,
		mustPlatformCheckpointInt(checkpoint.ArchiveBytes), lease.controllerEpoch, mustPlatformCheckpointInt(checkpoint.WriterFence))
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active'`,
		mustPlatformCheckpointInt(receipt.UsedBytes), now, checkpoint.NamespaceID, lease.installationID, lease.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		CheckpointID  string `json:"checkpoint_id"`
		Entries       int64  `json:"entries"`
	}{recoverySetID, checkpoint.CheckpointID, mustPlatformCheckpointInt(receipt.Entries)})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.migration_settlement_restored','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
		return err
	}
	return tx.Commit()
}
