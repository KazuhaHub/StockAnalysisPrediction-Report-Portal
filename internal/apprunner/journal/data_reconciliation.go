package journal

import (
	"context"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/data"
)

type dataRecoveryPreflight struct {
	facts       dataCommandFacts
	marker      string
	reservation DataReservation
}

func freshDataRecovery(c control.Command, m dataClaimMarker) bool {
	issued, issuedErr := strconv.ParseInt(c.IssuedMS, 10, 64)
	claimed, err := strconv.ParseInt(m.ClaimedMS, 10, 64)
	return issuedErr == nil && err == nil && issued > claimed
}

// prepareDataRecovery reads exact unknown bound intent under current signed
// authority. It issues no executable handle and performs no native operation.
func (s *Store) prepareDataRecovery(ctx context.Context, raw, signature []byte, pin data.RootIdentity) (dataRecoveryPreflight, error) {
	var zero dataRecoveryPreflight
	if !validDataRoot(pin) {
		return zero, ErrFenceReconciliation
	}
	tx, facts, err := s.beginDataCommandReceipt(ctx, raw, signature, "unknown", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	resource, err := existingDataResource(ctx, tx, facts.command)
	if err != nil {
		return zero, err
	}
	reservation, err := reservedDataResource(facts, resource)
	if err != nil {
		return zero, err
	}
	var marker dataClaimMarker
	if !canonicalDataJSON(facts.receipt, &marker) || !validDataMarker(marker) || markerRoot(marker) != pin || marker.EffectID != facts.command.EffectID || marker.ResourceID != reservation.ResourceID || marker.OwnershipSHA256 != reservation.OwnershipSHA256 || marker.AdmittedFence != facts.command.InstallationFence || !freshDataRecovery(facts.command, marker) {
		return zero, ErrFenceReconciliation
	}
	if err := requireDataRootHistory(ctx, tx, pin); err != nil {
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
	return dataRecoveryPreflight{facts: facts, marker: facts.receipt, reservation: reservation}, nil
}

// ReconcilePrepareData is read-only native recovery of an uncertain completed
// preparation. A freshly signed envelope observes the original exact effect;
// it cannot resume Prepare, mint a live claim, repair partial data or grant a writer.
func (s *Store) ReconcilePrepareData(ctx context.Context, raw, signature []byte, root *data.Root, expected data.RootIdentity) (DataPreparationReceipt, error) {
	var zero DataPreparationReceipt
	if root == nil || !validDataRoot(expected) || s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	preflight, err := s.prepareDataRecovery(ctx, raw, signature, expected)
	if err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(preflight.facts.command.ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrDataClaimUnavailable
	}
	observed, err := root.Inspect(ctx, expected, preflight.reservation.NamespaceID, preflight.reservation.ProjectID)
	if err != nil {
		return zero, err
	}
	return s.settleDataObservation(ctx, preflight.facts.raw, preflight.facts.signature, preflight.marker, expected, observed, true)
}
