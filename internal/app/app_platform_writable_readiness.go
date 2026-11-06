package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/journal"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformWritableActivateStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageActivate
}
func platformWritableReadinessMatches(r journal.WritableCandidateReadiness, i platformWritableStartIntent) bool {
	raw, _ := json.Marshal(r)
	if _, err := journal.ParseWritableCandidateReadiness(raw); err != nil {
		return false
	}
	running, _ := json.Marshal(i.running)
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	nativeObserved, _ := strconv.ParseInt(i.running.NativeObservedMS, 10, 64)
	return i.applied && observed >= nativeObserved && r.PayloadSHA256 == i.command.PayloadSHA256 && r.RunningReceiptSHA256 == platformDigest(running) && r.NativeID == i.running.NativeID && r.Protocol == "portal.backend/1" && r.DataFormat == i.writer.data.DataFormat
}

// Stored readiness preserves observation history. Parsing it does not mint a
// fresh health/boot proof; every new readiness decision requires fresh native IO.
func validStoredPlatformWritableReadiness(i platformWritableStartIntent, now int64) bool {
	raw := []byte(i.writer.activationReadiness)
	r, err := journal.ParseWritableCandidateReadiness(raw)
	observed, parseErr := strconv.ParseInt(r.ObservedMS, 10, 64)
	nativeObserved, _ := strconv.ParseInt(i.running.NativeObservedMS, 10, 64)
	return err == nil && parseErr == nil && platformWritableReadinessMatches(r, i) && i.writer.activationObserved.Valid && i.writer.activationObserved.Int64 == observed && observed >= nativeObserved && observed <= now+1000 && observed <= time.Now().UnixMilli() && observed <= i.writer.activationUpdated+1000
}
func validPlatformWritableReadinessObservation(proof transport.WritableLifecycleObservation, i platformWritableStartIntent, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p := proof.Controller(), i.writer.authority, proof.Endpoint()
	r, err := proof.Readiness()
	observed, parseErr := strconv.ParseInt(r.ObservedMS, 10, 64)
	return proof.Action() == transport.WritableCandidateProbe && err == nil && parseErr == nil && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(i.raw) && platformWritableReadinessMatches(r, i) && observed <= proof.ReceivedAt().UnixMilli() && observed >= proof.ReceivedAt().Add(-3*time.Second).UnixMilli()
}
func (s *Server) probePlatformWritableCandidate(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	i, err := s.preparePlatformWritableProbe(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, i.writer.runtime, i.writer.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformRunWritableStart(ctx, endpoint, i, transport.WritableCandidateProbe)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformWritableReadiness(ctx, lease, i, endpoint, proof)
}

// Probe advances to activate; a fresh activate-stage probe refreshes only the
// candidate's exact observation history. Neither path grants application access.
func (s *Server) commitPlatformWritableReadiness(ctx context.Context, expected platformOperationLease, intent platformWritableStartIntent, endpoint platformRuntimeEndpoint, proof transport.WritableLifecycleObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	intent = clonePlatformWritableStartIntent(intent)
	if (!validPlatformWritableProbeStage(expected) && !validPlatformWritableActivateStage(expected)) || expected.rowRevision == math.MaxInt64 || !validPlatformWritableReadinessObservation(proof, intent, endpoint) {
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
	current, _, err := loadPlatformWritableStartState(e, expected, runtime, seed, facts.now, false, true)
	if err != nil || !samePlatformWritableStartIntent(intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyWritableCandidate(intent.raw, intent.signature, current.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	r, err := proof.Readiness()
	if err != nil || !validPlatformWritableReadinessObservation(proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > 4096 {
		return zero, errAppPlatformStageEvidence
	}
	observed, _ := strconv.ParseInt(r.ObservedMS, 10, 64)
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	previousObserved := 0
	if current.writer.activationObserved.Valid {
		previousObserved = 1
	}
	changed, err := e.exec(`UPDATE app_platform_activations SET readiness_evidence=?,observed_ms=?,updated_ms=? WHERE id=? AND installation_id=? AND release_sha256=? AND data_namespace_id=? AND state='candidate' AND generation=1 AND policy_revision=1 AND readiness_evidence=? AND created_ms=? AND updated_ms=? AND ((?=0 AND observed_ms IS NULL) OR (?=1 AND observed_ms=?))`, string(raw), observed, now, current.writer.resourceID, expected.installationID, current.writer.data.ReleaseSHA256, current.writer.namespaceID, current.writer.activationReadiness, current.writer.activationCreated, current.writer.activationUpdated, previousObserved, previousObserved, current.writer.activationObserved.Int64)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	committedAt := now
	result := expected
	if validPlatformWritableProbeStage(expected) {
		model, err := appplatform.OperationFromSnapshot(expected.snapshot)
		if err != nil {
			return zero, errAppPlatformStageEvidence
		}
		next, err := model.Advance(expected.snapshot.Revision, appplatform.StageActivate)
		if err != nil {
			return zero, errAppPlatformStageEvidence
		}
		result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
		changed, err = e.exec(`UPDATE app_platform_operations SET stage='activate',state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='probe' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
	}
	detail, _ := json.Marshal(struct{ ResourceID, NativeID, ObservedMS string }{current.writer.resourceID, r.NativeID, r.ObservedMS})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_readiness_observed','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	after, _, err := loadPlatformWritableStartState(e, result, runtime, seed, now, false, true)
	if err != nil {
		return zero, err
	}
	expectedAfter := clonePlatformWritableStartIntent(current)
	expectedAfter.writer.activationReadiness, expectedAfter.writer.activationObserved, expectedAfter.writer.activationUpdated = string(raw), sql.NullInt64{Int64: observed, Valid: true}, committedAt
	if !samePlatformWritableStartIntent(after, expectedAfter) || !validStoredPlatformWritableReadiness(after, now) {
		return zero, errAppPlatformStageEvidence
	}
	if !validPlatformWritableReadinessObservation(proof, intent, endpoint) {
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
