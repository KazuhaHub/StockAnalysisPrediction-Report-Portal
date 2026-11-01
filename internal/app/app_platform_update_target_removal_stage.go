package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func (s *Server) dispatchPlatformUpdateTargetRemoval(ctx context.Context, lease platformOperationLease, stage string, effectNumber int, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformUpdateTargetRemoval(
		ctx, lease, stage, effectNumber)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"prepare update target removal %s: %w", stage, err)
	}
	if err := s.checkPlatformManagedRuntimeRoot(
		ctx, lease, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"check update target removal %s runtime: %w", stage, err)
	}
	action := transport.UpdateStartRemovalStop
	if stage == "remove_code" {
		action = transport.UpdateStartRemovalRemoveCode
	}
	proof, err := platformRunUpdateTargetRemoval(
		ctx, endpoint, intent, action)
	if err != nil {
		return platformOperationLease{}, fmt.Errorf(
			"run update target removal %s: %w", stage, err)
	}
	return s.commitPlatformUpdateTargetRemoval(
		ctx, lease, intent, endpoint, proof)
}

func (s *Server) commitPlatformUpdateTargetRemoval(ctx context.Context, lease platformOperationLease, intent platformUpdateTargetRemovalIntent, endpoint platformRuntimeEndpoint, proof platformUpdateTargetRemovalObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if lease.snapshot.Action != appplatform.ActionRestore ||
		!lease.snapshot.Managed ||
		lease.snapshot.Stage != appplatform.StageQuiesce ||
		lease.rowRevision == math.MaxInt64 ||
		!validPlatformUpdateTargetRemovalObservation(
			proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	receipt, err := proof.ManagedRemovalReceipt()
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	dek, err := s.dek()
	if err != nil {
		return zero, err
	}
	current, err := s.preparePlatformUpdateTargetRemoval(
		ctx, lease, intent.command.Stage,
		intent.effectNumber)
	if err != nil {
		return zero, err
	}
	if !samePlatformUpdateTargetRemovalIntent(intent, current) {
		return zero, fmt.Errorf(
			"update target removal current intent: %w",
			errAppPlatformStageEvidence)
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
	runtime, seed, err := s.platformDataRuntime(
		e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	result := lease
	expected := current
	if current.applied {
		if current.receipt != receipt {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		now, err := finalPlatformOperationLeaseCheck(e, facts)
		if err != nil {
			return zero, err
		}
		changed, err := e.exec(`UPDATE app_platform_operation_effects
			SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
			WHERE operation_id=? AND stage='quiesce' AND effect_number=?
			AND effect_id=? AND kind=? AND payload_sha256=?
			AND runtime_id=? AND controller_epoch=? AND admitted_fence=?
			AND state='prepared' AND receipt_json='{}'
			AND observed_fence=0 AND created_ms=? AND updated_ms=?`,
			lease.fence, string(encoded), now, lease.id,
			intent.effectNumber,
			current.command.EffectID, current.command.Stage,
			current.command.PayloadSHA256, runtime.id,
			lease.controllerEpoch, lease.fence,
			current.created, current.updated)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		result.rowRevision++
		changed, err = e.exec(`UPDATE app_platform_operations
			SET row_revision=row_revision+1,updated_ms=?
			WHERE id=? AND installation_id=? AND stage='quiesce'
			AND state_revision=? AND row_revision=? AND controller_epoch=?
			AND fence=? AND lease_owner=? AND lease_until_ms=?
			AND lease_until_ms>? AND completed_ms IS NULL`,
			now, lease.id, lease.installationID,
			int64(lease.snapshot.Revision), lease.rowRevision,
			lease.controllerEpoch, lease.fence, lease.owner,
			lease.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID      string `json:"effect_id"`
			NativeID      string `json:"native_id"`
			State         string `json:"state"`
			ReceiptSHA256 string `json:"receipt_sha256"`
		}{current.command.EffectID, receipt.NativeID, receipt.State,
			platformDigest(encoded)})
		if _, err := e.exec(`INSERT INTO audit_log(
			at,actor,actor_ou,action,target_type,target_id,detail,ip)
			VALUES(?,'system',0,'app_platform.update_target_removed',
			'app_platform_operation',?,?,'')`,
			time.UnixMilli(now).UTC().Format(time.RFC3339),
			lease.id, string(detail)); err != nil {
			return zero, err
		}
		expected.applied, expected.receipt, expected.updated =
			true, receipt, now
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	after, err := s.preparePlatformUpdateTargetRemoval(
		ctx, result, intent.command.Stage,
		intent.effectNumber)
	if err != nil || !samePlatformUpdateTargetRemovalIntent(
		expected, after) {
		return zero, errAppPlatformStageEvidence
	}
	return result, nil
}

func samePlatformUpdateTargetRemovalIntent(a, b platformUpdateTargetRemovalIntent) bool {
	ar, _ := json.Marshal(a.receipt)
	br, _ := json.Marshal(b.receipt)
	return a.command.OperationID == b.command.OperationID &&
		a.command.EffectID == b.command.EffectID &&
		a.command.Stage == b.command.Stage &&
		a.command.InstallationFence == b.command.InstallationFence &&
		a.command.PayloadSHA256 == b.command.PayloadSHA256 &&
		a.command.RunningReceiptSHA256 ==
			b.command.RunningReceiptSHA256 &&
		a.start.command.EffectID == b.start.command.EffectID &&
		a.start.running != nil && b.start.running != nil &&
		platformDigest(mustMarshalPlatformUpdateStartReceipt(*a.start.running)) ==
			platformDigest(mustMarshalPlatformUpdateStartReceipt(*b.start.running)) &&
		a.applied == b.applied && a.created == b.created &&
		a.updated == b.updated && string(ar) == string(br)
}
