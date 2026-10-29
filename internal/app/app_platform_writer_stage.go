package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformWriterObservation(proof transport.WritableLifecycleObservation, i platformWriterIntent, lease platformOperationLease, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b := proof.Controller(), i.authority
	p := proof.Endpoint()
	action := proof.Action()
	if action != transport.WritableWriterExecute && action != transport.WritableWriterRead && action != transport.WritableWriterReconcile {
		return false
	}
	r, err := proof.WriterReceipt()
	claimed, _ := strconv.ParseInt(r.Claim.ClaimedMS, 10, 64)
	return err == nil && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(i.raw) && claimed >= i.effectCreated && platformWriterReceiptMatches(r, i, lease, time.Now().UnixMilli())
}

func (s *Server) dispatchPlatformInstallWriter(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformInstallWriter(ctx, lease, endpoint, transport.WritableWriterExecute)
}
func (s *Server) completePlatformInstallWriter(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformInstallWriter(ctx, lease, endpoint, transport.WritableWriterRead)
}
func (s *Server) reconcilePlatformInstallWriter(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.runPlatformInstallWriter(ctx, lease, endpoint, transport.WritableWriterReconcile)
}
func (s *Server) runPlatformInstallWriter(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction) (platformOperationLease, error) {
	i, err := s.preparePlatformInstallWriter(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, i.runtime, i.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	if i.applied {
		return lease, nil
	} // Historical metadata, no new native/health claim.
	proof, err := platformRunWriter(ctx, endpoint, i, action)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformObservedWriter(ctx, lease, i, endpoint, proof)
}

// A successful writer observation updates only its typed effect and namespace
// metadata. No process is started, operation stage advanced or app access granted.
func (s *Server) commitPlatformObservedWriter(ctx context.Context, expected platformOperationLease, intent platformWriterIntent, endpoint platformRuntimeEndpoint, proof transport.WritableLifecycleObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	intent = clonePlatformWriterIntent(intent)
	if !validPlatformWriterStage(expected) || intent.applied || !validPlatformWriterObservation(proof, intent, expected, endpoint) {
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
	current, _, err := loadPlatformWriterIntent(e, expected, runtime, seed, facts.now, false)
	if err != nil || !samePlatformWriterIntent(intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := control.VerifyDataWriter(intent.raw, intent.signature, current.authority, time.Now().UnixMilli())
	if err != nil || !samePlatformWriterCommand(verified.Command(), current.command) {
		return zero, errAppPlatformStageEvidence
	}
	r, err := proof.WriterReceipt()
	if err != nil || !validPlatformWriterObservation(proof, currentWithWriterEnvelope(current, intent), expected, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	encoded, err := json.Marshal(r)
	if err != nil || len(encoded) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	used, err := strconv.ParseInt(r.Native.UsedBytes, 10, 64)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='prepare_data' AND effect_number=2 AND effect_id=? AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND state='prepared' AND receipt_json='{}' AND admitted_fence=? AND observed_fence=0 AND created_ms=? AND updated_ms=?`, expected.fence, string(encoded), now, expected.id, current.effectID, current.command.PayloadSHA256, runtime.id, expected.controllerEpoch, expected.fence, current.effectCreated, current.effectUpdated)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	changed, err = e.exec(`UPDATE app_platform_data_namespaces SET revision=3,observed_bytes=?,updated_ms=? WHERE id=? AND owner_installation_id=? AND state='active' AND revision=2 AND writer_fence=1 AND retained_ms IS NULL AND deleted_ms IS NULL AND updated_ms=?`, used, now, current.namespaceID, expected.installationID, current.namespaceUpdated)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct{ ResourceID, EffectID string }{current.resourceID, current.effectID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writer_observed','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	settled, _, err := loadPlatformWriterIntent(e, expected, runtime, seed, now, false)
	if err != nil || !settled.applied || settled.receipt != r {
		return zero, errAppPlatformStageEvidence
	}
	if !validPlatformWriterObservation(proof, intent, expected, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyDataWriter(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return expected, nil
}
func currentWithWriterEnvelope(current, original platformWriterIntent) platformWriterIntent {
	current.raw, current.signature = original.raw, original.signature
	return current
}
