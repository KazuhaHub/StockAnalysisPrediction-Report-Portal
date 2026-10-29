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

func validPlatformWritableRegistrationObservation(proof transport.WritableLifecycleObservation, i platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint) bool {
	wire, err := json.Marshal(i.command)
	if err != nil || !bytes.Equal(wire, i.raw) || !bytes.Equal(i.command.CandidateRaw, i.start.raw) || !bytes.Equal(i.command.CandidateSignature, i.start.signature) {
		return false
	}
	age := time.Since(proof.ReceivedAt())
	a, b, p := proof.Controller(), i.start.writer.authority, proof.Endpoint()
	r, err := proof.RegistrationReceipt()
	return (proof.Action() == transport.WritableRegistrationDeliver || proof.Action() == transport.WritableRegistrationRead) && err == nil && !proof.ReceivedAt().IsZero() && age >= 0 && age <= 3*time.Second && p.RootController && p.RunnerUID == 0 && endpoint.controllerUID > 0 && endpoint.controllerGID > 0 && p.Path == endpoint.path && p.ControllerUID == endpoint.controllerUID && p.ControllerGID == endpoint.controllerGID && a.DeploymentID == b.DeploymentID && a.ControllerEpoch == b.ControllerEpoch && a.ControlRevision == b.ControlRevision && a.Fingerprint == b.Fingerprint && bytes.Equal(a.PublicKey, b.PublicKey) && proof.CommandSHA256() == platformDigest(i.raw) && platformWritableRegistrationReceiptMatches(r, i, time.Now().UnixMilli())
}

func (s *Server) dispatchPlatformWritableRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformWritableRegistration(ctx, lease, endpoint, transport.WritableRegistrationDeliver)
}
func (s *Server) completePlatformWritableRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint) (platformOperationLease, error) {
	return s.observePlatformWritableRegistration(ctx, lease, endpoint, transport.WritableRegistrationRead)
}
func (s *Server) observePlatformWritableRegistration(ctx context.Context, lease platformOperationLease, endpoint platformRuntimeEndpoint, action transport.WritableLifecycleAction) (platformOperationLease, error) {
	i, err := s.preparePlatformWritableRegistration(ctx, lease)
	if err != nil {
		return platformOperationLease{}, err
	}
	defer i.registration.Close()
	if err := s.checkPlatformManagedRuntimeRoot(ctx, lease, i.start.writer.runtime, i.start.writer.authority, endpoint); err != nil {
		return platformOperationLease{}, err
	}
	proof, err := platformRunWritableRegistration(ctx, endpoint, i, action)
	if err != nil {
		return platformOperationLease{}, err
	}
	return s.commitPlatformWritableRegistration(ctx, lease, i, endpoint, proof)
}

// Delivery remains at activate with a pending installation. Possession/boot and
// current viewer authorization are separate gates; this creates no access grant.
func (s *Server) commitPlatformWritableRegistration(ctx context.Context, lease platformOperationLease, intent platformWritableRegistrationIntent, endpoint platformRuntimeEndpoint, proof transport.WritableLifecycleObservation) (platformOperationLease, error) {
	var zero platformOperationLease
	intent = clonePlatformWritableRegistrationIntent(intent)
	if !validPlatformWritableActivateStage(lease) || lease.rowRevision == math.MaxInt64 || !validPlatformWritableRegistrationObservation(proof, intent, endpoint) {
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
	runtime, seed, err := s.platformDataRuntime(e, lease.controllerEpoch, dek)
	if err != nil {
		return zero, err
	}
	defer clear(seed)
	facts, err := loadPlatformBoundOperationLease(e, lease)
	if err != nil {
		return zero, err
	}
	trust, err := loadPlatformPublisherTrust(e)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, lease, trust, facts.now); err != nil {
		return zero, err
	}
	current, _, err := loadPlatformWritableRegistration(e, lease, runtime, seed, dek, facts.now, false)
	if err != nil {
		return zero, err
	}
	defer current.registration.Close()
	if !samePlatformWritableRegistrationIntent(intent, current) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyWritableBackendRegistration(intent.raw, intent.signature, current.start.writer.authority, time.Now().UnixMilli()); err != nil {
		return zero, errAppPlatformStageEvidence
	}
	receipt, err := proof.RegistrationReceipt()
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil || len(encoded) > 8192 {
		return zero, errAppPlatformStageEvidence
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
		changed, err := e.exec(`UPDATE app_platform_operation_effects SET state='applied',observed_fence=?,receipt_json=?,updated_ms=? WHERE operation_id=? AND stage='activate' AND effect_number=1 AND effect_id=? AND kind='activate' AND payload_sha256=? AND runtime_id=? AND controller_epoch=? AND state='prepared' AND receipt_json='{}' AND admitted_fence=? AND observed_fence=0 AND created_ms=? AND updated_ms=?`, lease.fence, string(encoded), now, lease.id, current.command.EffectID, current.command.PayloadSHA256, current.start.writer.runtime.id, lease.controllerEpoch, lease.fence, current.created, current.updated)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		result.rowRevision++
		changed, err = e.exec(`UPDATE app_platform_operations SET row_revision=row_revision+1,updated_ms=? WHERE id=? AND installation_id=? AND stage='activate' AND state_revision=? AND row_revision=? AND controller_epoch=? AND fence=? AND lease_owner=? AND lease_until_ms=? AND lease_until_ms>? AND completed_ms IS NULL`, now, lease.id, lease.installationID, int64(lease.snapshot.Revision), lease.rowRevision, lease.controllerEpoch, lease.fence, lease.owner, lease.leaseUntilMS, now)
		if err := platformLeaseCAS(changed, err); err != nil {
			return zero, err
		}
		detail, _ := json.Marshal(struct{ EffectID, ResourceID, ReceiptSHA256 string }{current.command.EffectID, current.start.writer.resourceID, platformDigest(encoded)})
		if _, err := e.exec(`INSERT INTO audit_log(at,actor,actor_ou,action,target_type,target_id,detail,ip) VALUES(?,'system',0,'app_platform.writable_registration_observed','app_platform_operation',?,?,'')`, time.UnixMilli(now).UTC().Format(time.RFC3339), lease.id, string(detail)); err != nil {
			return zero, err
		}
		expected.applied, expected.receipt, expected.updated = true, receipt, now
	}
	now, err := finalPlatformOperationLeaseCheck(e, facts)
	if err != nil {
		return zero, err
	}
	if err := validatePlatformInstallAuthority(e, lease, trust, now); err != nil {
		return zero, err
	}
	after, _, err := loadPlatformWritableRegistration(e, result, runtime, seed, dek, now, false)
	if err != nil {
		return zero, err
	}
	defer after.registration.Close()
	if !samePlatformWritableRegistrationIntent(expected, after) {
		return zero, errAppPlatformStageEvidence
	}
	if err := platformVerifyInstallAuthorityAtCommit(e, lease, trust, now); err != nil {
		return zero, err
	}
	if !validPlatformWritableRegistrationObservation(proof, intent, endpoint) {
		return zero, errAppPlatformStageEvidence
	}
	if _, err := control.VerifyWritableBackendRegistration(intent.raw, intent.signature, current.start.writer.authority, time.Now().UnixMilli()); err != nil {
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
