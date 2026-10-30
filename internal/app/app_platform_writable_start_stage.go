package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformWritableStartObservation(proof transport.WritableLifecycleObservation, i platformWritableStartIntent, lease platformOperationLease, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b := proof.Controller(), i.writer.authority
	p := proof.Endpoint()
	if proof.Action() != transport.WritableCandidateStart && proof.Action() != transport.WritableCandidateRead && proof.Action() != transport.WritableCandidateReconcile {
		return false
	}
	r, err := proof.RunningReceipt()
	return err == nil && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(i.raw) && platformWritableRunningMatches(r, i, lease, time.Now().UnixMilli())
}
func (s *Server) dispatchPlatformWritableStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformWritableStart(ctx, lease, endpoint, transport.WritableCandidateStart)
}
func (s *Server) completePlatformWritableStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformWritableStart(ctx, lease, endpoint, transport.WritableCandidateRead)
}
func (s *Server) reconcilePlatformWritableStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformWritableStart(ctx, lease, endpoint, transport.WritableCandidateReconcile)
}
func (s *Server) runPlatformWritableStart(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction) (platformOperationLease, error) {
	if !validPlatformWriterStage(lease) {
		return platformOperationLease{}, errAppPlatformStageEvidence
	}
	intent, err := s.preparePlatformWritableStart(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, intent.writer.runtime, intent.writer.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformRunWritableStart(ctx, endpoint, intent, action)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformObservedWritableStart(ctx, lease, intent, endpoint, proof)
}

func (s *Server) commitPlatformObservedWritableStart(ctx context.Context, expected platformOperationLease, intent platformWritableStartIntent, endpoint platformRuntimeEndpoint, proof transport.WritableLifecycleObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	intent = clonePlatformWritableStartIntent(intent)
	if !validPlatformWriterStage(expected) || expected.rowRevision == math.MaxInt64 || intent.applied || !validPlatformWritableStartObservation(proof, intent, expected, endpoint) {
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
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, facts.now); err != nil {
		return zero, err
	}
	current, _, err := loadPlatformWritableStartIntent(e, expected, runtime, seed, facts.now, false)
	if err != nil || !samePlatformWritableStartIntent(intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	v, err := control.VerifyWritableCandidate(intent.raw, intent.signature, current.writer.authority, time.Now().UnixMilli())
	if err != nil || v.Command().PayloadSHA256 != current.command.PayloadSHA256 {
		return zero, errAppPlatformStageEvidence
	}
	r, err := proof.RunningReceipt()
	if err != nil || !validPlatformWritableStartObservation(proof, intent, expected, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND admitted_fence=? AND observed_fence=0 AND state='prepared' AND receipt_json='{}' AND created_ms=? AND updated_ms=?`, expected.fence, string(encoded), now, expected.id, current.command.EffectID, current.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, current.effectCreated, current.effectUpdated)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageProbe)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot = next.Snapshot()
	result.rowRevision++
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='probe',state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='start' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct{ Stage, ResourceID, EffectID string }{"probe", current.writer.resourceID, current.command.EffectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_start_observed','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	settled, _, err := loadPlatformWritableStartIntent(e, result, runtime, seed, now, false)
	if err != nil || !settled.applied || !samePlatformWriterIntent(settled.writer, current.writer) {
		return zero, errAppPlatformStageEvidence
	}
	actual, _ := json.Marshal(settled.running)
	if !bytes.Equal(actual, encoded) {
		return zero, errAppPlatformStageEvidence
	}
	if !validPlatformWritableStartObservation(proof, intent, expected, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyWritableCandidate(intent.raw, intent.signature, current.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
