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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformReadinessStage(lease platformOperationLease) bool {
	return lease.snapshot.Action == appplatform.ActionInstall && lease.snapshot.Managed && lease.snapshot.Generation == 1 && lease.snapshot.Stage == appplatform.StageProbe
}
func validPlatformReadinessObservation(proof transport.CandidateReadinessObservation, intent platformCandidateIntent, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p, r := proof.Controller(), intent.authority, proof.Endpoint(), proof.Readiness()
	encoded, _ := json.Marshal(intent.running)
	observed, err := strconv.ParseInt(r.ObservedMS, 10, 64)
	return !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(intent.raw) && r.PayloadSHA256 == intent.command.PayloadSHA256 && r.RunningReceiptSHA256 == platformDigest(encoded) && r.NativeID == intent.running.NativeID && r.Protocol == "portal.backend/1" && r.DataFormat == intent.command.Payload.DataFormat && err == nil && observed <= proof.ReceivedAt().UnixMilli() && observed >= proof.ReceivedAt().Add(-3*time.Second).UnixMilli()
}

func (s *Server) probePlatformInstallCandidate(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformCandidateProbe(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformProbeCandidate(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformCandidateReadiness(ctx, lease, intent, endpoint, proof)
}

func (s *Server) commitPlatformCandidateReadiness(ctx context.Context, expected platformOperationLease, intent platformCandidateIntent, endpoint platformRuntimeEndpoint, proof transport.CandidateReadinessObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if !validPlatformReadinessStage(expected) || expected.rowRevision == math.MaxInt64 || !validPlatformReadinessObservation(proof, intent, endpoint) {
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
	clear(seed)
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
	current, _, err := loadPlatformCandidateIntentState(e, expected, runtime, facts.now, false, true)
	if err != nil {
		return zero, err
	}
	current.command.IssuedMS, current.command.ExpiresMS = intent.command.IssuedMS, intent.command.ExpiresMS
	current.raw = bytes.Clone(intent.raw)
	if current.command != intent.command || current.resourceID != intent.resourceID || current.dataSHA != intent.dataSHA || !bytes.Equal(current.descriptor, intent.descriptor) || !validPlatformReadinessObservation(proof, current, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := control.VerifyCandidate(intent.raw, intent.signature, current.authority, time.Now().UnixMilli())
	if err != nil || verified.Command() != current.command {
		return zero, errAppPlatformStageEvidence
	}
	readiness, _ := json.Marshal(proof.Readiness())
	observed, _ := strconv.ParseInt(proof.Readiness().ObservedMS, 10, 64)
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_activations SET readiness_evidence=?,observed_ms=?,updated_ms=? WHERE id=? AND installation_id=? AND state='candidate' AND readiness_evidence='{}' AND observed_ms IS NULL`, string(readiness), observed, now, current.resourceID, expected.installationID)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	model, err := appplatform.OperationFromSnapshot(expected.snapshot)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	next, err := model.Advance(expected.snapshot.Revision, appplatform.StageActivate)
	if err != nil {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	result.snapshot, result.rowRevision = next.Snapshot(), expected.rowRevision+1
	changed, err = e.exec(`UPDATE app_platform_operations SET stage='activate',state_revision=?,row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='probe' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, int64(result.snapshot.Revision), now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
	if err := platformLeaseCAS(changed, err); err != nil {
		return zero, err
	}
	detail, _ := json.Marshal(struct {
		Stage      string `json:"stage"`
		ResourceID string `json:"resource_id"`
	}{"activate", current.resourceID})
	if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.operation_stage','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
		return zero, err
	}
	now, err = finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if !validPlatformReadinessObservation(proof, current, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyCandidate(intent.raw, intent.signature, current.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
