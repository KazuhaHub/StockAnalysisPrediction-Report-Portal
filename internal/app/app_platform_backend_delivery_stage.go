package app

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/transport"
)

func validPlatformBackendDeliveryObservation(proof transport.BackendRegistrationObservation, intent platformBackendDeliveryIntent, endpoint platformRuntimeEndpoint) bool {
	age := time.Since(proof.ReceivedAt())
	a, b, p, r := proof.Controller(), intent.candidate.authority, proof.Endpoint(), proof.Receipt()
	source, _ := json.Marshal(intent.candidate.running)
	return !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(intent.raw) && r.MatchesCommand(intent.command) && r.Claim.SourceSHA256 == platformDigest(source) && r.Claim.NativeID == intent.candidate.running.NativeID
}

// Delivery settlement deliberately keeps the operation at activate. It creates
// no boot/health evidence, active backend connection, viewer or provider grant.
func (s *Server) dispatchPlatformBackendDelivery(ctx context.Context, expected platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	intent, err := s.preparePlatformBackendDelivery(ctx, expected)
	if err != nil {
		return platformOperationLease{}, err
	}
	defer intent.registration.Close()
	if err := s.checkPlatformManagedRuntimeRoot(ctx, expected, intent.candidate.runtime, intent.candidate.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformDeliverBackendRegistration(ctx, endpoint, intent)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformBackendDelivery(ctx, expected, intent, endpoint, proof)
}
func (s *Server) commitPlatformBackendDelivery(ctx context.Context, expected platformOperationLease, intent platformBackendDeliveryIntent, endpoint platformRuntimeEndpoint, proof transport.BackendRegistrationObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	if !validPlatformBackendDeliveryStage(expected) || expected.rowRevision == math.MaxInt64 || !validPlatformBackendDeliveryObservation(proof, intent, endpoint) {
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
	current, _, err := loadPlatformBackendDelivery(e, expected, runtime, seed, dek, facts.now, false)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	// Renewed wire timestamps are outside logical identity. Everything selecting
	// the actual candidate/key/runtime must still match the persisted intent.
	candidate := current.candidate.command
	candidate.IssuedMS, candidate.ExpiresMS = intent.candidate.command.IssuedMS, intent.candidate.command.ExpiresMS
	if candidate != intent.candidate.command || !samePlatformManagedRuntime(current.candidate.runtime, intent.candidate.runtime) || current.command.EffectID != intent.command.EffectID || current.command.PayloadSHA256 != intent.command.PayloadSHA256 || current.command.DocumentSHA256 != intent.command.DocumentSHA256 || !bytes.Equal(current.candidate.descriptor, intent.candidate.descriptor) {
		return zero, errAppPlatformStageEvidence
	}
	verified, err := control.VerifyBackendRegistration(intent.raw, intent.signature, current.candidate.authority, time.Now().UnixMilli())
	if err != nil || verified.Candidate() != candidate {
		return zero, errAppPlatformStageEvidence
	}
	verifiedRaw, _ := json.Marshal(verified.Command())
	intentRaw, _ := json.Marshal(intent.command)
	if !bytes.Equal(verifiedRaw, intentRaw) {
		return zero, errAppPlatformStageEvidence
	}
	receipt := proof.Receipt()
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 4096 {
		return zero, errAppPlatformStageEvidence
	}
	source, _ := json.Marshal(current.candidate.running)
	if receipt.Claim.SourceSHA256 != platformDigest(source) || receipt.Claim.NativeID != current.candidate.running.NativeID || !receipt.MatchesCommand(current.command) {
		return zero, errAppPlatformStageEvidence
	}
	result := expected
	if current.applied {
		if current.receipt != receipt {
			return zero, errAppPlatformStageEvidence
		}
	} else {
		now, err := finalPlatformOperationLeaseCheck(e, facts)
		if err != nil {
			return zero, err
		}
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='activate' AND effect_number=1 AND effect_id=? AND kind='activate' AND state='prepared' AND receipt_json='{}' AND admitted_fence=? AND observed_fence=0`, expected.fence, string(encoded), now, expected.id, intent.command.EffectID, expected.fence)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		result.rowRevision++
		changed, err = e.exec(`UPDATE app_platform_operations SET row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='activate' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, now, expected.id, expected.installationID, int64(expected.snapshot.Revision), expected.rowRevision, expected.controllerEpoch, expected.fence, expected.owner, expected.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct {
			EffectID      string `json:"effect_id"`
			ResourceID    string `json:"resource_id"`
			ReceiptSHA256 string `json:"receipt_sha256"`
		}{intent.command.EffectID, current.candidate.resourceID, platformDigest(encoded)})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.backend_delivery','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), expected.id, string(detail)); err != nil {
			return zero, err
		}
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, expected, trust, now); err != nil {
		return zero, err
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, expected, trust, now); err != nil {
		return zero, err
	}
	if !validPlatformBackendDeliveryObservation(proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyBackendRegistration(intent.raw, intent.signature, current.candidate.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	if settled > time.Now().UnixMilli() {
		return zero, errAppPlatformStageEvidence
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
