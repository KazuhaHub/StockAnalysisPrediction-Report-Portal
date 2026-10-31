package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
)

type platformUpdateStartAdmission struct {
	intent      platformUpdateStartIntent
	reservation journal.UpdateStartReservation
}

// reservePlatformUpdateStart persists the signed generation-2 intent and asks
// the runner to reserve its distinct target resource. It does not start a
// process or advance the Portal operation.
func (s *Server) reservePlatformUpdateStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartAdmission, error) {
	intent, err := s.preparePlatformUpdateStart(ctx, expected)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformUpdateStartAdmission{}, err
	}
	proof, err := platformReserveUpdateStart(ctx, endpoint, intent)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	reservation, ok := validPlatformUpdateStartObservation(proof, intent, endpoint)
	if !ok {
		return platformUpdateStartAdmission{}, errAppPlatformStageEvidence
	}
	return s.commitPlatformUpdateStartReservation(ctx, expected, intent, endpoint, proof, reservation)
}

func (s *Server) readPlatformUpdateStartReservation(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformUpdateStartAdmission, error) {
	intent, err := s.preparePlatformUpdateStart(ctx, expected)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformUpdateStartAdmission{}, err
	}
	proof, err := platformReadUpdateStartReservation(ctx, endpoint, intent)
	if err != nil {
		return platformUpdateStartAdmission{}, err
	}
	reservation, ok := validPlatformUpdateStartObservation(proof, intent, endpoint)
	if !ok {
		return platformUpdateStartAdmission{}, errAppPlatformStageEvidence
	}
	return s.commitPlatformUpdateStartReservation(ctx, expected, intent, endpoint, proof, reservation)
}

func (s *Server) executePlatformUpdateStart(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	admission, err := s.reservePlatformUpdateStart(ctx, expected, endpoint)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, admission.intent.runtime, admission.intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformExecuteUpdateStart(ctx, endpoint, admission.intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformUpdateStartReceipt(ctx, expected, admission.intent, endpoint, proof)
}

func (s *Server) readPlatformUpdateStartReceipt(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformUpdateStart(ctx, expected)
	if err != nil || intent.reservation == nil {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	if err := s.checkPlatformManagedOperationRoot(ctx, expected.id, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformReadUpdateStartReceipt(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformUpdateStartReceipt(ctx, expected, intent, endpoint, proof)
}

func platformUpdateStartCommandsEqual(a, b control.UpdateStartCommand) bool {
	a.IssuedMS, a.ExpiresMS = "", ""
	b.IssuedMS, b.ExpiresMS = "", ""
	return a == b
}

func (s *Server) commitPlatformUpdateStartReceipt(ctx context.Context, expected platformOperationLease, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint, observation platformUpdateStartRunningObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStart || expected.snapshot.Generation < 2 ||
		expected.rowRevision == math.MaxInt64 || intent.reservation == nil {
		return zero, fmt.Errorf("update-start running precondition: %w", errAppPlatformStageEvidence)
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	receipt, ok := validPlatformUpdateStartRunningObservation(observation, intent, endpoint)
	if !ok {
		return zero, fmt.Errorf("update-start running observation: %w", errAppPlatformStageEvidence)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 16384 {
		return zero, fmt.Errorf("update-start running encode: %w", errAppPlatformStageEvidence)
	}
	parsed, err := journal.ParseUpdateStartRunningReceipt(encoded)
	if err != nil {
		return zero, fmt.Errorf("update-start running receipt parse: %w", err)
	}
	if !platformUpdateStartRunningMatches(parsed, intent.command, intent.resourceID, intent.effectID, intent.reservation) {
		return zero, fmt.Errorf("update-start running receipt binding: %w", errAppPlatformStageEvidence)
	}
	reservationRaw, err := json.Marshal(*intent.reservation)
	if err != nil {
		return zero, fmt.Errorf("update-start running reservation encode: %w", errAppPlatformStageEvidence)
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
	plan, _, err := loadPlatformManagedUpdatePlan(e, expected, source)
	if err != nil {
		return zero, err
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, expected, plan)
	if err != nil {
		return zero, err
	}
	current, _, err := loadPlatformUpdateStartIntent(e, expected, runtime, source, checkpoint, plan, facts.now, false)
	if err != nil || current.reservation == nil ||
		current.resourceID != intent.resourceID || current.effectID != intent.effectID ||
		!platformUpdateStartReservationsEqual(*current.reservation, *intent.reservation) ||
		!platformUpdateStartCommandsEqual(current.command, intent.command) ||
		current.authority.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, fmt.Errorf("update-start running current intent: %w", errAppPlatformStageEvidence)
	}
	if _, err := control.VerifyUpdateStart(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, fmt.Errorf("update-start running signature: %w", errAppPlatformStageEvidence)
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind='update-start'
		AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=?
		AND state='prepared' AND observed_fence=? AND receipt_json=?`,
		expected.fence, string(encoded), now, expected.id, current.effectID,
		current.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence,
		expected.fence, string(reservationRaw))
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, fmt.Errorf("update-start running effect CAS: %w", err)
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, fmt.Errorf("update-start running operation model: %w", errAppPlatformStageEvidence)
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageProbe)
	if err != nil {
		return zero, fmt.Errorf("update-start running stage model: %w", errAppPlatformStageEvidence)
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='probe',state_revision=?,row_revision=row_revision+1,updated_ms=?
		WHERE id=? AND installation_id=? AND controller_epoch=? AND stage='start' AND state_revision=?
		AND row_revision=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`,
		int64(result.snapshot.Revision), now, expected.id, expected.installationID, expected.controllerEpoch,
		int64(expected.snapshot.Revision), expected.rowRevision, expected.fence, expected.owner,
		expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, fmt.Errorf("update-start running operation CAS: %w", err)
	}
	detail, _ := json.Marshal(struct {
		ResourceID    string `json:"resource_id"`
		NativeID      string `json:"native_id"`
		TargetRelease string `json:"target_release_sha256"`
	}{receipt.Claim.ResourceID, receipt.NativeID, receipt.TargetReleaseSHA256})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.update_start_running','app_platform_operation',?,?,'')`,
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

// commitPlatformUpdateStartReservation durably records the exact target
// resource acknowledged by the runner while leaving the start effect prepared.
// Native start authority remains separate and cannot be inferred from this row.
func (s *Server) commitPlatformUpdateStartReservation(ctx context.Context, expected platformOperationLease, intent platformUpdateStartIntent, endpoint platformRuntimeEndpoint, observation platformUpdateStartObservation, reservation journal.UpdateStartReservation) (platformUpdateStartAdmission, error) {
	var zero platformUpdateStartAdmission
	if expected.snapshot.Action != appplatform.ActionUpdate || !expected.snapshot.Managed ||
		expected.snapshot.Stage != appplatform.StageStart || expected.snapshot.Generation < 2 ||
		ctx == nil || ctx.Err() != nil {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature = bytes.Clone(intent.raw), bytes.Clone(intent.signature)
	intent.authority.PublicKey = bytes.Clone(intent.authority.PublicKey)
	validated, ok := validPlatformUpdateStartObservation(observation, intent, endpoint)
	if !ok || !platformUpdateStartReservationsEqual(validated, reservation) {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(reservation)
	if err != nil || len(encoded) > 4096 {
		return zero, errAppPlatformStageEvidence
	}
	if parsed, err := journal.ParseUpdateStartReservation(encoded); err != nil || !platformUpdateStartReservationsEqual(parsed, reservation) {
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
	plan, _, err := loadPlatformManagedUpdatePlan(e, expected, source)
	if err != nil {
		return zero, err
	}
	checkpoint, _, err := loadPlatformUpdateStartCheckpoint(e, expected, plan)
	if err != nil {
		return zero, err
	}
	current, _, err := loadPlatformUpdateStartIntent(e, expected, runtime, source, checkpoint, plan, facts.now, false)
	if err != nil || current.resourceID != intent.resourceID || current.effectID != intent.effectID ||
		!platformUpdateStartCommandsEqual(current.command, intent.command) ||
		current.authority.Fingerprint != intent.authority.Fingerprint ||
		!bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyUpdateStart(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if current.reservation != nil {
		if !platformUpdateStartReservationsEqual(*current.reservation, reservation) {
			return zero, errAppPlatformStageEvidence
		}
		if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
			return zero, errAppPlatformStageEvidence
		}
		intent.reservation = current.reservation
		return platformUpdateStartAdmission{intent: intent, reservation: *current.reservation}, nil
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET observed_fence=?,receipt_json=?,updated_ms=?
		WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND kind='update-start'
		AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND state='prepared'
		AND observed_fence=0 AND receipt_json='{}'`,
		expected.fence, string(encoded), now, expected.id, current.effectID,
		current.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		ResourceID       string `json:"resource_id"`
		NamespaceID      string `json:"namespace_id"`
		TargetGeneration int64  `json:"target_generation"`
	}{reservation.ResourceID, reservation.NamespaceID, reservation.TargetGeneration})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip)
		VALUES(?,'system',0,'app_platform.update_start_reserved','app_platform_operation',?,?,'')`,
		time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	if _, err := finalPlatformOperationLeaseCheck(e, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	intent.reservation = &reservation
	return platformUpdateStartAdmission{intent: intent, reservation: reservation}, nil
}
