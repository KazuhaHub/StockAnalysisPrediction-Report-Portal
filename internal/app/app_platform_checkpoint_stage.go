package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

func (s *Server) dispatchPlatformCheckpoint(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformCheckpoint(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformCreateCheckpoint(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformCheckpoint(ctx, expected, intent, endpoint, proof)
}

func (s *Server) readPlatformCheckpoint(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformCheckpoint(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformReadCheckpointReceipt(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformCheckpoint(ctx, expected, intent, endpoint, proof)
}

func platformCheckpointCommandsEqual(a, b control.CheckpointCommand) bool {
	a.IssuedMS, a.ExpiresMS = "", ""
	b.IssuedMS, b.ExpiresMS = "", ""
	return a == b
}

// commitPlatformCheckpoint records one authenticated native receipt and advances
// snapshot -> migrate atomically. Until this commits, the recovery set remains
// preparing and is not admissible for restore or target start.
func (s *Server) commitPlatformCheckpoint(ctx context.Context, expected platformOperationLease, intent platformCheckpointIntent, endpoint platformRuntimeEndpoint, observation platformCheckpointObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageSnapshot || expected.snapshot.Generation < 2 ||
		expected.rowRevision == math.MaxInt64 {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformCheckpointObservation(observation, intent, endpoint)
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
	current, _, err := loadPlatformCheckpointIntent(e, expected, runtime, source, facts.now, false, false)
	if err != nil || current.runtime.id != intent.runtime.id || current.recoverySetID != intent.recoverySetID ||
		current.effectID != intent.effectID || !platformCheckpointCommandsEqual(current.command, intent.command) ||
		current.authority.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	_, verifyErr := control.VerifyCheckpoint(intent.raw, intent.signature, current.authority, time.Now().UnixMilli())
	clear(seed)
	if verifyErr != nil {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 32768 {
		return zero, errAppPlatformStageEvidence
	}
	if parsed, err := journal.ParseCheckpointReceipt(encoded); err != nil || parsed != receipt {
		return zero, errAppPlatformStageEvidence
	}
	archiveBytes, err := strconv.ParseInt(receipt.ArchiveBytes, 10, 64)
	if err != nil || archiveBytes <= 0 {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='snapshot' AND effect_number=1 AND effect_id=? AND kind='snapshot'
		AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared'
		AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, current.effectID, current.command.PayloadSHA256,
		runtime.id, expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_recovery_sets SET manifest_sha256=?,bytes=?,state='verified',verified_ms=?,updated_ms=?
		WHERE id=? AND installation_id=? AND data_namespace_id=? AND operation_id=? AND release_sha256=?
		AND data_format=? AND checkpoint_id=? AND controller_epoch=? AND writer_fence=?
		AND state='preparing' AND manifest_sha256 IS NULL AND bytes=0 AND verified_ms IS NULL`,
		receipt.ManifestSHA256, archiveBytes, now, now, current.recoverySetID, expected.installationID,
		current.source.namespaceID, expected.id, current.source.releaseSHA256,
		mustPlatformCheckpointInt(current.source.dataFormat), current.command.Payload.CheckpointID,
		expected.controllerEpoch, mustPlatformCheckpointInt(current.source.writerFence))
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageMigrate)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='migrate',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='snapshot' AND state_revision=?
		AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		RecoverySetID string `json:"recovery_set_id"`
		CheckpointID  string `json:"checkpoint_id"`
		ManifestSHA   string `json:"manifest_sha256"`
		Bytes         int64  `json:"bytes"`
	}{current.recoverySetID, current.command.Payload.CheckpointID, receipt.ManifestSHA256, archiveBytes})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.checkpoint_verified','app_platform_operation',?,?,'')`,
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
