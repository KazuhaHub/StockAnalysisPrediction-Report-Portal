package journal

import (
	"bytes"
	"context"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

type dataWriterRecoveryPreflight struct {
	facts   dataWriterFacts
	marker  dataWriterClaimMarker
	request driver.DataWriterRequest
}

func dataWriterRecoveryFresh(m dataWriterClaimMarker, f dataWriterFacts) bool {
	issued, _ := strconv.ParseInt(f.verified.Data().IssuedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	sourceTime, _ := strconv.ParseInt(f.source.ObservedMS, 10, 64)
	claimed, _ := strconv.ParseInt(m.ClaimedMS, 10, 64)
	return dataWriterMarkerIdentityMatches(m, f) && issued > expires && issued <= f.now && sourceTime <= claimed
}

// This read issues no live claim. Current authority may observe the exact
// original unknown intent only after the original execution deadline has passed.
func (s *Store) prepareDataWriterRecovery(ctx context.Context, raw, signature, descriptor []byte) (dataWriterRecoveryPreflight, error) {
	var zero dataWriterRecoveryPreflight
	tx, f, err := s.beginDataWriter(ctx, raw, signature, "unknown", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var m dataWriterClaimMarker
	if !canonicalDataJSON(f.receipt, &m) || !dataWriterRecoveryFresh(m, f) {
		return zero, ErrFenceReconciliation
	}
	request, err := dataWriterNativeRequest(f.verified.Command(), f.source, m, descriptor)
	if err != nil {
		return zero, err
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalDataWriterCheck(ctx, f); err != nil {
		return zero, err
	}
	return dataWriterRecoveryPreflight{facts: f, marker: m, request: request}, nil
}

// ReconcileDataWriter performs only fresh native observation under current
// signed authority. It never calls EnableDataWriter, repairs permissions or
// resurrects a live claim. Close waits for observation and atomic settlement.
func (s *Store) ReconcileDataWriter(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	if native == nil || s.closing.Load() || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrDataWriterClaimUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	preflight, err := s.prepareDataWriterRecovery(ctx, raw, signature, descriptor)
	if err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(preflight.facts.verified.Data().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrDataWriterClaimUnavailable
	}
	observation, err := native.ObserveDataWriter(ctx, preflight.request)
	if err != nil {
		return zero, err
	}
	return s.settleRecoveredDataWriter(ctx, preflight, descriptor, observation)
}

// Caller holds the owner execution gate throughout preflight, IO and settlement.
func (s *Store) settleRecoveredDataWriter(ctx context.Context, before dataWriterRecoveryPreflight, descriptor []byte, observation driver.DataWriterRecoveryObservation) (DataWriterReceipt, error) {
	var zero DataWriterReceipt
	tx, f, err := s.beginDataWriter(ctx, before.facts.raw, before.facts.signature, "unknown", &before.facts.receipt)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || f.now < before.facts.now || f.source != before.facts.source || !dataWriterRecoveryFresh(before.marker, f) {
		return zero, ErrFenceReconciliation
	}
	request, err := dataWriterNativeRequest(f.verified.Command(), f.source, before.marker, descriptor)
	if err != nil {
		return zero, err
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Binding != request.Binding || actual.Identity != request.Identity || actual.Data != request.Data || actual.Platform != request.Platform || actual.ObservedMS > f.now || actual.ObservedMS < before.facts.now {
		return zero, ErrFenceReconciliation
	}
	r := DataWriterReceipt{Format: dataWriterRecoveredReceiptFormat, Claim: before.marker, ControlRevision: f.verified.Data().ControlRevision, Native: dataWriterEvidence(actual), SettledMS: strconv.FormatInt(f.now, 10), RecoveryEnvelopeSHA256: candidateHash(f.raw), RecoveryIssuedMS: f.verified.Data().IssuedMS, RecoveryExpiresMS: f.verified.Data().ExpiresMS}
	r.BindingSHA256 = dataWriterReceiptBinding(r)
	if !validDataWriterReceipt(r) {
		return zero, ErrFenceReconciliation
	}
	return s.commitDataWriterReceipt(ctx, tx, f, f.receipt, r)
}
