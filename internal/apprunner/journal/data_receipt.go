package journal

import (
	"context"
	"strconv"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
)

// ParseDataPreparationReceipt checks canonical structure only. Caller-provided
// JSON is never OS peer authentication, command authorization or readiness proof.
func ParseDataPreparationReceipt(raw []byte) (DataPreparationReceipt, error) {
	var result DataPreparationReceipt
	if len(raw) == 0 || len(raw) > 16384 {
		return result, ErrFenceReconciliation
	}
	if !canonicalDataJSON(string(raw), &result) || !validDataReceipt(result) {
		return DataPreparationReceipt{}, ErrFenceReconciliation
	}
	return result, nil
}

// MatchesCommand compares the signed logical intent and original ownership tuple.
// It does not verify the command signature, authenticate a sender or grant access.
func (r DataPreparationReceipt) MatchesCommand(c control.Command) bool {
	if !validDataReceipt(r) {
		return false
	}
	digest, err := control.LogicalDigest(c)
	if err != nil || digest != c.PayloadSHA256 {
		return false
	}
	project, err := strconv.ParseUint(r.ProjectID, 10, 32)
	if err != nil {
		return false
	}
	return r.DeploymentID == c.DeploymentID &&
		r.ControllerEpoch == c.ControllerEpoch &&
		r.ControlRevision == c.ControlRevision &&
		r.InstallationID == c.InstallationID &&
		r.PayloadSHA256 == c.PayloadSHA256 &&
		r.ReleaseSHA256 == c.Payload.ReleaseSHA256 &&
		r.DataFormat == c.Payload.DataFormat &&
		r.Generation == c.TargetGeneration &&
		r.WriterFence == c.Payload.WriterFence &&
		r.QuotaBytes == c.Payload.QuotaBytes &&
		r.QuotaInodes == c.Payload.QuotaInodes &&
		r.Claim.EffectID == c.EffectID &&
		r.Claim.ResourceID == c.Payload.DataNamespaceID &&
		r.Claim.AdmittedFence == c.InstallationFence &&
		r.Claim.OwnershipSHA256 == dataOwnership(c, uint32(project))
}

// ReadDataPreparationReceipt returns historical applied evidence only after
// rechecking current signed authority and the unchanged observed resource. It
// never executes, reconciles, changes acknowledgements or grants a writer.
func (s *Store) ReadDataPreparationReceipt(ctx context.Context, raw, signature []byte) (DataPreparationReceipt, error) {
	var zero DataPreparationReceipt
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	tx, facts, err := s.beginDataCommandReceipt(ctx, raw, signature, "applied", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseDataPreparationReceipt([]byte(facts.receipt))
	if err != nil || !receipt.MatchesCommand(facts.command) {
		return zero, ErrFenceReconciliation
	}
	resource, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return zero, err
	}
	reservation, err := dataResourceIdentity(facts, resource)
	if err != nil {
		return zero, err
	}
	observed, _ := strconv.ParseInt(receipt.ObservedMS, 10, 64)
	if resource.observed != "present" || resource.revision != 2 || !resource.observedMS.Valid || resource.observedMS.Int64 != observed || resource.updated != observed || observed > facts.now || receipt.ProjectID != strconv.FormatUint(uint64(reservation.ProjectID), 10) {
		return zero, ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, markerRoot(receipt.Claim)); err != nil {
		return zero, err
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalDataCommandCheck(ctx, facts); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	return receipt, nil
}
