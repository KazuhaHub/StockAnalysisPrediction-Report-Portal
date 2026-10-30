package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const writableCandidateReadinessFormat = "portal.runner.writable-candidate-readiness/1-dev.1"

// Transient current observation metadata only. It is not a durable receipt,
// registered backend, active generation or permission to serve a viewer.
type WritableCandidateReadiness struct {
	Format               string `json:"format"`
	PayloadSHA256        string `json:"payload_sha256"`
	RunningReceiptSHA256 string `json:"running_receipt_sha256"`
	NativeID             string `json:"native_id"`
	Protocol             string `json:"protocol"`
	DataFormat           string `json:"data_format"`
	ObservedMS           string `json:"observed_ms"`
}

func ParseWritableCandidateReadiness(raw []byte) (WritableCandidateReadiness, error) {
	var r WritableCandidateReadiness
	if len(raw) == 0 || len(raw) > 4096 || !canonicalDataJSON(string(raw), &r) || r.Format != writableCandidateReadinessFormat || !dataSHA(r.PayloadSHA256) || !dataSHA(r.RunningReceiptSHA256) || !dataSHA(r.NativeID) || r.Protocol != "portal.backend/1" || !dataNumber(r.DataFormat, true) || !dataNumber(r.ObservedMS, false) {
		return WritableCandidateReadiness{}, driver.ErrCandidateNotReady
	}
	return r, nil
}

type writableCandidateReadinessPreflight struct {
	facts    writableCandidateFacts
	running  WritableCandidateRunningReceipt
	request  driver.WritableCandidateLaunchRequest
	expected driver.WritableCandidateLaunchSnapshot
}

func (s *Store) prepareWritableCandidateReadiness(ctx context.Context, raw, signature, descriptor []byte) (writableCandidateReadinessPreflight, error) {
	var zero writableCandidateReadinessPreflight
	tx, f, err := s.beginWritableCandidate(ctx, raw, signature, "applied", nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	running, err := ParseWritableCandidateRunningReceipt([]byte(f.receipt))
	if err != nil {
		return zero, err
	}
	request, expected, err := writableCandidateExpectedSnapshot(f, running.Claim, descriptor)
	if err != nil {
		return zero, err
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
	return writableCandidateReadinessPreflight{facts: f, running: running, request: request, expected: expected}, nil
}

// ObserveWritableCandidateReadiness holds owner exclusion through current
// authorization, bounded native/socket IO and current postflight validation.
// It accepts both original and reconciled running receipts, never unknown
// commands. No durable state or acknowledgement changes and no activation occur.
func (s *Store) ObserveWritableCandidateReadiness(ctx context.Context, raw, signature []byte, native *driver.CandidateRuntime, descriptor []byte) (WritableCandidateReadiness, error) {
	var zero WritableCandidateReadiness
	if native == nil || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || s.closing.Load() {
		return zero, driver.ErrCandidateNotReady
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	before, err := s.prepareWritableCandidateReadiness(ctx, raw, signature, descriptor)
	if err != nil {
		return zero, err
	}
	c := before.facts.verified.Index()
	expires, _ := strconv.ParseInt(c.ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	observation, err := native.CheckWritableCandidateReadiness(ctx, before.request, before.expected, before.running.NativeID, c.Payload.DataFormat)
	if err != nil {
		return zero, err
	}
	return s.finishWritableCandidateReadiness(ctx, before, observation)
}
func (s *Store) finishWritableCandidateReadiness(ctx context.Context, before writableCandidateReadinessPreflight, observation driver.WritableCandidateReadinessObservation) (WritableCandidateReadiness, error) {
	var zero WritableCandidateReadiness
	tx, f, err := s.beginWritableCandidate(ctx, before.facts.raw, before.facts.signature, "applied", &before.facts.receipt)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if s.closing.Load() || f.now < before.facts.now || f.source != before.facts.source || f.created != before.facts.created || f.updated != before.facts.updated || f.resource == nil || before.facts.resource == nil || *f.resource != *before.facts.resource {
		return zero, ErrFenceReconciliation
	}
	actual, err := observation.Snapshot()
	c := f.verified.Index()
	if err != nil || actual.Launch != before.expected || !writableSnapshotMatches(actual.Launch, f, before.running.Claim) || actual.NativeID != before.running.NativeID || actual.DataFormat != c.Payload.DataFormat || actual.ObservedMS < before.facts.now || actual.ObservedMS > f.now {
		return zero, driver.ErrCandidateNotReady
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := finalWritableCandidateCheck(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrWritableCandidateClaimUnavailable
	}
	result := WritableCandidateReadiness{Format: writableCandidateReadinessFormat, PayloadSHA256: c.PayloadSHA256, RunningReceiptSHA256: candidateHash([]byte(f.receipt)), NativeID: actual.NativeID, Protocol: actual.Protocol, DataFormat: actual.DataFormat, ObservedMS: strconv.FormatInt(actual.ObservedMS, 10)}
	encoded, _ := json.Marshal(result)
	return ParseWritableCandidateReadiness(encoded)
}
