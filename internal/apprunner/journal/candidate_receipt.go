package journal

import (
	"context"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

// MatchesCommand checks historical logical intent, not the current envelope's
// issuance/expiry. A renewed signed request may retrieve an earlier receipt;
// it cannot change the original admitted ownership fence or control revision.
// This method verifies neither signatures nor current process readiness.
func (r CandidateRunningReceipt) MatchesCommand(c control.CandidateCommand) bool {
	if !validCandidateReceipt(r) {
		return false
	}
	digest, err := control.CandidateLogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return false
	}
	fence, err := strconv.ParseInt(c.InstallationFence, 10, 64)
	if err != nil || !dataNumber(c.InstallationFence, true) {
		return false
	}
	return r.DeploymentID == c.DeploymentID && r.ControllerEpoch == c.ControllerEpoch && r.ControlRevision == c.ControlRevision && r.InstallationID == c.InstallationID && r.OperationID == c.OperationID && r.Generation == c.TargetGeneration && r.WriterFence == c.Payload.WriterFence && r.PayloadSHA256 == c.PayloadSHA256 && r.ReleaseSHA256 == c.Payload.ReleaseSHA256 && r.DataNamespaceID == c.Payload.DataNamespaceID && r.DataFormat == c.Payload.DataFormat && r.Claim.EffectID == c.EffectID && r.Claim.ResourceID == c.Payload.ResourceID && r.Claim.AdmittedFence == c.InstallationFence && r.Claim.Preflight.Platform == c.Payload.Platform && r.Claim.OwnershipSHA256 == candidateOwnership(candidateFacts{command: c, stored: admittedCommand{fence: fence}, dataSHA: r.Claim.DataReceiptSHA256})
}

// ReadCandidateRunningReceipt returns historical applied evidence under current
// signed authority. No Docker/filesystem IO, resource mutation, acknowledgement,
// recovery, activation or fresh readiness is performed by this read.
func (s *Store) ReadCandidateRunningReceipt(ctx context.Context, raw, signature []byte) (CandidateRunningReceipt, error) {
	var zero CandidateRunningReceipt
	tx, f, err := s.beginCandidateCommandReceipt(ctx, raw, signature, "applied", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseCandidateRunningReceipt([]byte(f.receipt))
	if err != nil || !receipt.MatchesCommand(f.command) || receipt.Claim.DataReceiptSHA256 != f.dataSHA {
		return zero, ErrFenceReconciliation
	}
	request, err := candidateNativeRequest(f.command, f.data, nil)
	if err != nil {
		return zero, err
	}
	prior := receipt.Claim.Preflight
	expected := candidatePreflight(driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform, ImageID: prior.ImageID, PolicySHA256: prior.PolicySHA256, MountsSHA256: prior.MountsSHA256})
	if expected != *prior {
		return zero, ErrFenceReconciliation
	}
	resource, err := existingCandidateResource(ctx, tx, f.command)
	if err != nil {
		return zero, err
	}
	c := f.command
	generation, _ := strconv.ParseInt(c.TargetGeneration, 10, 64)
	writer, _ := strconv.ParseInt(c.Payload.WriterFence, 10, 64)
	nativeObserved, _ := strconv.ParseInt(receipt.NativeObservedMS, 10, 64)
	settled, _ := strconv.ParseInt(receipt.SettledMS, 10, 64)
	bound, _ := strconv.ParseInt(receipt.Claim.PreflightBoundMS, 10, 64)
	if resource == nil || resource.id != c.Payload.ResourceID || resource.installation != c.InstallationID || !resource.namespace.Valid || resource.namespace.String != c.Payload.DataNamespaceID || resource.kind != "container" || resource.native != receipt.NativeID || !resource.release.Valid || resource.release.String != c.Payload.ReleaseSHA256 || resource.generation != generation || resource.epoch != c.ControllerEpoch || resource.fence != f.stored.fence || resource.writer != writer || resource.desired != "present" || resource.observed != "running" || resource.ownership != receipt.Claim.OwnershipSHA256 || resource.revision != 2 || !resource.observedMS.Valid || resource.observedMS.Int64 != nativeObserved || resource.created < 0 || resource.created > bound || resource.updated != settled || settled > f.now {
		return zero, ErrFenceReconciliation
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrCandidateClaimUnavailable
	}
	return receipt, nil
}
