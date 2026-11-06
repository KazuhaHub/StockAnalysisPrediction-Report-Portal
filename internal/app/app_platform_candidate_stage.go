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
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformCandidateObservation(proof transport.CandidateReceiptObservation, intent platformCandidateIntent, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b := proof.Controller(), intent.authority
	p := proof.Endpoint()
	r := proof.Receipt()
	if proof.ReceivedAt().IsZero() || age < 0 || age > transport.MaxCandidateReceiptAge || !p.RootController || p.RunnerUID != 0 || endpoint.controllerUID == 0 || endpoint.controllerGID == 0 || p.Path != endpoint.path || p.ControllerUID != endpoint.controllerUID || p.ControllerGID != endpoint.controllerGID || a.DeploymentID != b.DeploymentID || a.ControllerEpoch != b.ControllerEpoch || a.ControlRevision != b.ControlRevision || a.Fingerprint != b.Fingerprint || !bytes.Equal(a.PublicKey, b.PublicKey) || !platformValidSHA(proof.RequestSHA256()) || proof.CommandSHA256() != platformDigest(intent.raw) || !r.MatchesCommand(intent.command) || r.Claim.DataReceiptSHA256 != intent.dataSHA {
		return false
	}
	pre := r.Claim.Preflight
	d := intent.data
	settled, err := strconv.ParseInt(r.SettledMS, 10, 64)
	return err == nil && settled <= time.Now().UnixMilli() && pre.RootFilesystemUUID == d.Claim.RootFilesystemUUID && pre.RootInode == d.Claim.RootInode && pre.ProjectID == d.ProjectID && pre.Device == d.Device && pre.NamespaceInode == d.NamespaceInode && pre.QuotaBytes == d.QuotaBytes && pre.QuotaInodes == d.QuotaInodes
}

func (s *Server) dispatchPlatformInstallCandidate(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformInstallCandidate(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	if err := s.checkPlatformManagedRuntimeRoot(ctx, expected, intent.runtime, intent.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformStartCandidate(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformObservedCandidate(ctx, expected, intent, endpoint, proof)
}

// Reconciliation reads the original effect; it never starts a replacement after
// an uncertain response or invents success from a stored/native ID.
func (s *Server) completePlatformInstallCandidate(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformInstallCandidate(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformReadCandidate(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformObservedCandidate(ctx, expected, intent, endpoint, proof)
}

func (s *Server) commitPlatformObservedCandidate(ctx context.Context, expected platformOperationLease, intent platformCandidateIntent, endpoint platformRuntimeEndpoint, proof transport.CandidateReceiptObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if !validPlatformCandidateStage(expected) || expected.rowRevision == math.MaxInt64 || !validPlatformCandidateObservation(proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	intent.raw, intent.signature, intent.descriptor = bytes.Clone(intent.raw), bytes.Clone(intent.signature), bytes.Clone(intent.descriptor)
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
	current, _, err := loadPlatformCandidateIntent(e, expected, runtime, facts.now, false)
	if err != nil {
		return zero, err
	}
	current.command.IssuedMS, current.command.ExpiresMS = intent.command.IssuedMS, intent.command.ExpiresMS
	if current.command != intent.command || current.resourceID != intent.resourceID || current.effectID != intent.effectID || runtime.id != intent.runtime.id || runtime.revision != intent.runtime.revision || !bytes.Equal(current.descriptor, intent.descriptor) || current.dataSHA != intent.dataSHA || current.authority.DeploymentID != intent.authority.DeploymentID || current.authority.ControllerEpoch != intent.authority.ControllerEpoch || current.authority.ControlRevision != intent.authority.ControlRevision || current.authority.Fingerprint != intent.authority.Fingerprint || !bytes.Equal(current.authority.PublicKey, intent.authority.PublicKey) {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := control.VerifyCandidate(intent.raw, intent.signature, current.authority, time.Now().UnixMilli())
	if err != nil || verified.Command() != current.command {
		return zero, errAppPlatformStageEvidence
	}
	raw, err := json.Marshal(proof.Receipt())
	if err != nil || len(raw) > 16384 {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := journal.ParseCandidateRunningReceipt(raw); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='start' AND effect_number=1 AND effect_id=? AND state='prepared' AND receipt_json='{}' AND admitted_fence=? AND observed_fence=0`, expected.fence, string(raw), now, expected.id, intent.effectID, expected.fence)
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
	detail, _ := json.Marshal(struct {
		Stage      string `json:"stage"`
		ResourceID string `json:"resource_id"`
		EffectID   string `json:"effect_id"`
	}{"probe", intent.resourceID, intent.effectID})
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
	if !validPlatformCandidateObservation(proof, intent, endpoint) {
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
