package journal

import (
	"bytes"
	"context"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const writableRecoveredRunningFormat = "portal.runner.reconciled-writable-candidate-running-receipt/1-dev.1"

type writableCandidateRecoveryPreflight struct {
	facts    writableCandidateFacts
	marker   writableCandidateClaimMarker
	request  driver.WritableCandidateLaunchRequest
	expected driver.WritableCandidateLaunchSnapshot
}

func writableCandidateRecoveryFresh(m writableCandidateClaimMarker, f writableCandidateFacts) bool {
	issued, _ := strconv.ParseInt(f.verified.Index().IssuedMS, 10, 64)
	expires, _ := strconv.ParseInt(m.ExpiresMS, 10, 64)
	return m.Preflight != nil && writableCandidateMarkerIdentityMatches(m, f) && issued > expires && issued <= f.now
}
func (s *Store) prepareWritableCandidateRecovery(ctx context.Context, raw, signature, descriptor []byte) (writableCandidateRecoveryPreflight, error) {
	var zero writableCandidateRecoveryPreflight
	tx, f, err := s.beginWritableCandidateView(ctx, raw, signature, "unknown", nil, true)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var m writableCandidateClaimMarker
	if !canonicalDataJSON(f.receipt, &m) || !writableCandidateRecoveryFresh(m, f) {
		return zero, ErrFenceReconciliation
	}
	request, err := writableNativeRequest(f, m, descriptor)
	if err != nil {
		return zero, err
	}
	p := m.Preflight
	expected := driver.WritableCandidateLaunchSnapshot{Launch: driver.CandidateLaunchSnapshot{Identity: request.Identity, Data: request.Data, Platform: request.Platform, ImageID: p.Native.ImageID, PolicySHA256: p.Native.PolicySHA256, MountsSHA256: p.Native.MountsSHA256}, Binding: request.Binding, SandboxProfile: m.SandboxProfile, SeccompSHA256: p.SeccompSHA256}
	if !writableSnapshotMatches(expected, f, m) || writablePreflight(expected) != *p {
		return zero, ErrFenceReconciliation
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	return writableCandidateRecoveryPreflight{facts: f, marker: m, request: request, expected: expected}, nil
}

// ReconcileWritableCandidate observes only the exact original expired and
// durably bound unknown start. It never obtains/reconstructs an execution claim,
// provisions IPC, creates a process, starts/stops it, or repairs data/policy.
func (s *Store) ReconcileWritableCandidate(ctx context.Context, raw, signature, descriptor []byte, native *driver.CandidateRuntime) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	if native == nil || s.closing.Load() || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	before, err := s.prepareWritableCandidateRecovery(ctx, raw, signature, descriptor)
	if err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(before.facts.verified.Index().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	observation, err := native.ObserveWritableCandidate(ctx, before.request, before.expected)
	if err != nil {
		return zero, err
	}
	return s.settleRecoveredWritableCandidate(ctx, before, descriptor, observation)
}

// Caller owns the owner gate throughout current authorization, observation and
// settlement. Native success is opaque and distinct from live start evidence.
func (s *Store) settleRecoveredWritableCandidate(ctx context.Context, before writableCandidateRecoveryPreflight, descriptor []byte, observation driver.WritableCandidateRecoveryObservation) (WritableCandidateRunningReceipt, error) {
	var zero WritableCandidateRunningReceipt
	tx, f, err := s.beginWritableCandidateView(ctx, before.facts.raw, before.facts.signature, "unknown", &before.facts.receipt, true)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || f.now < before.facts.now || f.source != before.facts.source || f.created != before.facts.created || f.updated != before.facts.updated || f.resource == nil || before.facts.resource == nil || *f.resource != *before.facts.resource || !writableCandidateRecoveryFresh(before.marker, f) {
		return zero, ErrFenceReconciliation
	}
	request, err := writableNativeRequest(f, before.marker, descriptor)
	if err != nil {
		return zero, err
	}
	actual, err := observation.Snapshot()
	if err != nil || actual.Launch != before.expected || actual.Launch.Binding != request.Binding || !writableSnapshotMatches(actual.Launch, f, before.marker) || actual.ObservedMS < before.facts.now || actual.ObservedMS > f.now {
		return zero, ErrFenceReconciliation
	}
	c := f.verified.Index()
	r := WritableCandidateRunningReceipt{Format: writableRecoveredRunningFormat, Claim: before.marker, NativeID: actual.NativeID, NativeObservedMS: strconv.FormatInt(actual.ObservedMS, 10), SettledMS: strconv.FormatInt(f.now, 10), RecoveryEnvelopeSHA256: candidateHash(f.raw), RecoveryControlRevision: c.ControlRevision, RecoveryIssuedMS: c.IssuedMS, RecoveryExpiresMS: c.ExpiresMS}
	return s.commitWritableCandidateRunning(ctx, tx, f, f.receipt, r)
}
