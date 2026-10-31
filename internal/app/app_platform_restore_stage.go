package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func (s *Server) dispatchPlatformRestore(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformRestore(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformExecuteRestore(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestore(ctx, expected, intent, endpoint, proof)
}

func (s *Server) readPlatformRestoreReceipt(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformRestore(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformReadRestoreReceipt(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformRestore(ctx, expected, intent, endpoint, proof)
}

func platformRestoreCommandsEqual(a, b control.RestoreCommand) bool {
	a.IssuedMS, a.ExpiresMS = "", ""
	b.IssuedMS, b.ExpiresMS = "", ""
	return a == b
}

func (s *Server) commitPlatformRestore(ctx context.Context, expected platformOperationLease, intent platformRestoreIntent, endpoint platformRuntimeEndpoint, observation platformRestoreObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionRestore || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageRestoreData || expected.snapshot.Generation < 2 ||
		expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformRestoreObservation(observation, intent, endpoint)
	if !ok {
		return zero, errAppPlatformStageEvidence
	}
	if err := ctx.Err(); err != nil {
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
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, expected)
	if err != nil {
		return zero, err
	}
	source, err := loadPlatformCheckpointSource(e, expected, runtime, facts.now)
	if err != nil {
		return zero, err
	}
	recoverySetID, _, checkpoint, _, err := loadPlatformRestoreCheckpoint(e, expected)
	if err != nil {
		return zero, err
	}
	current, _, err := loadPlatformRestoreIntent(e, expected, runtime, source, checkpoint, facts.now, false)
	if err != nil || current.recoverySetID != intent.recoverySetID || current.effectID != intent.effectID ||
		!platformRestoreCommandsEqual(current.command, intent.command) ||
		current.authority.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyRestore(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return zero, errAppPlatformStageEvidence
	}
	if parsed, err := journal.ParseRestoreReceipt(encoded); err != nil || parsed != receipt {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='restore_data' AND effect_number=1 AND effect_id=?
		AND kind='restore' AND payload_sha256=? AND runtime_id=? AND controller_epoch=?
		AND admitted_fence=? AND state='prepared' AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, current.effectID,
		current.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_recovery_sets SET state='verified',updated_ms=?
		WHERE id=? AND installation_id=? AND data_namespace_id=? AND checkpoint_id=?
		AND manifest_sha256=? AND bytes=? AND state='restoring' AND controller_epoch=?
		AND writer_fence=?`,
		now, recoverySetID, expected.installationID, checkpoint.NamespaceID,
		checkpoint.CheckpointID, checkpoint.ManifestSHA256,
		mustPlatformCheckpointInt(checkpoint.ArchiveBytes), expected.controllerEpoch,
		mustPlatformCheckpointInt(checkpoint.WriterFence))
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET observed_bytes=?,revision=revision+1,updated_ms=?
		WHERE id=? AND origin_installation_id=? AND owner_installation_id=? AND state='active'`,
		mustPlatformCheckpointInt(receipt.UsedBytes), now, checkpoint.NamespaceID,
		expected.installationID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageStart)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='start',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='restore_data'
		AND state_revision=? AND row_revision=? AND fence=? AND lease_owner=?
		AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id, expected.installationID,
		expected.controllerEpoch, int64(expected.snapshot.Revision), expected.rowRevision,
		expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		CheckpointID  string `json:"checkpoint_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		Entries       int64  `json:"entries"`
	}{recoverySetID, checkpoint.CheckpointID, checkpoint.ManifestSHA256,
		mustPlatformCheckpointInt(receipt.Entries)})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.restore_verified','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
