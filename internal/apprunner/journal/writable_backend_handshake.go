package journal

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/appplatform"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/control"
	"github.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/internal/apprunner/driver"
)

const writableBackendHandshakeFormat = "portal.runner.writable-backend-handshake/1-dev.1"

// Transient physical transport evidence. A parsed answer is not a verified MAC,
// boot identity, viewer grant, registered connection or activation receipt.
type WritableBackendHandshakeEvidence struct {
	Format                string `json:"format"`
	PayloadSHA256         string `json:"payload_sha256"`
	DeliveryReceiptSHA256 string `json:"delivery_receipt_sha256"`
	NativeID              string `json:"native_id"`
	ChallengeSHA256       string `json:"challenge_sha256"`
	Answer                string `json:"answer"`
	ObservedMS            string `json:"observed_ms"`
}

func ParseWritableBackendHandshakeEvidence(raw []byte) (WritableBackendHandshakeEvidence, error) {
	var e WritableBackendHandshakeEvidence
	if len(raw) == 0 || len(raw) > 8192 || !canonicalDataJSON(string(raw), &e) || e.Format != writableBackendHandshakeFormat || !dataSHA(e.PayloadSHA256) || !dataSHA(e.DeliveryReceiptSHA256) || !dataSHA(e.NativeID) || !dataSHA(e.ChallengeSHA256) || !appplatform.ValidBackendHandshakeToken(e.Answer) || !dataNumber(e.ObservedMS, false) {
		return WritableBackendHandshakeEvidence{}, ErrFenceReconciliation
	}
	return e, nil
}

type writableBackendExchangePreflight struct {
	facts   writableRegistrationFacts
	receipt WritableBackendRegistrationReceipt
	request driver.WritableCandidateLaunchRequest
	binding driver.WritableBackendRegistrationSnapshot
}

func (s *Store) prepareWritableBackendExchange(ctx context.Context, raw, signature, descriptor []byte) (writableBackendExchangePreflight, error) {
	var zero writableBackendExchangePreflight
	tx, f, err := s.beginWritableRegistration(ctx, raw, signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	receipt, err := ParseWritableBackendRegistrationReceipt([]byte(f.row.receipt))
	if err != nil {
		return zero, err
	}
	request, expected, err := writableCandidateExpectedSnapshot(f.source, f.running.Claim, descriptor)
	if err != nil {
		return zero, err
	}
	device, _ := strconv.ParseUint(receipt.Device, 10, 64)
	inode, _ := strconv.ParseUint(receipt.Inode, 10, 64)
	binding := driver.WritableBackendRegistrationSnapshot{Launch: expected, NativeID: receipt.Claim.NativeID, BindingSHA256: receipt.BindingSHA256, DocumentSHA256: receipt.Claim.DocumentSHA256, Device: device, Inode: inode}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	return writableBackendExchangePreflight{facts: f, receipt: receipt, request: request, binding: binding}, nil
}

// Holds owner exclusion through current authorization, fixed private handshake
// IO and postflight. Host-side answer verification is a separate mandatory step.
// This writes no row, file, permission, acknowledgement or access grant.
func (s *Store) ExchangeWritableBackendHandshake(ctx context.Context, raw, signature, descriptor []byte, challenge string, native *driver.CandidateRuntime) (WritableBackendHandshakeEvidence, error) {
	var zero WritableBackendHandshakeEvidence
	if native == nil || s.closing.Load() || len(raw) == 0 || len(raw) > control.MaxCommandBytes || len(signature) != 64 || len(descriptor) == 0 || len(descriptor) > appplatform.MaxDescriptorBytes || !appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrFenceReconciliation
	}
	raw, signature, descriptor = bytes.Clone(raw), bytes.Clone(signature), bytes.Clone(descriptor)
	if err := s.acquireDataExecution(ctx); err != nil {
		return zero, err
	}
	defer s.releaseDataExecution()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	before, err := s.prepareWritableBackendExchange(ctx, raw, signature, descriptor)
	if err != nil {
		return zero, err
	}
	expires, _ := strconv.ParseInt(before.facts.verified.Candidate().Index().ExpiresMS, 10, 64)
	ctx, cancel := context.WithDeadline(ctx, time.UnixMilli(expires))
	defer cancel()
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	observed, err := native.ExchangeWritableBackendHandshake(ctx, before.request, before.binding, challenge)
	if err != nil {
		return zero, err
	}
	return s.finishWritableBackendHandshake(ctx, before, challenge, observed)
}
func (s *Store) finishWritableBackendHandshake(ctx context.Context, before writableBackendExchangePreflight, challenge string, observed driver.WritableBackendHandshakeObservation) (WritableBackendHandshakeEvidence, error) {
	var zero WritableBackendHandshakeEvidence
	tx, f, err := s.beginWritableRegistration(ctx, before.facts.raw, before.facts.signature, "applied")
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	if !sameWritableRegistrationSource(before.facts, f) || s.closing.Load() || !appplatform.ValidBackendHandshakeToken(challenge) {
		return zero, ErrFenceReconciliation
	}
	actual, err := observed.Snapshot()
	if err != nil || actual.Registration != before.binding || !writableSnapshotMatches(actual.Registration.Launch, f.source, f.running.Claim) || actual.ChallengeSHA256 != candidateHash([]byte(challenge)) || actual.ObservedMS < before.facts.source.now || actual.ObservedMS > f.source.now {
		return zero, ErrFenceReconciliation
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	if err := checkWritableRegistrationCurrent(ctx, f); err != nil {
		return zero, err
	}
	if s.closing.Load() {
		return zero, ErrFenceReconciliation
	}
	result := WritableBackendHandshakeEvidence{Format: writableBackendHandshakeFormat, PayloadSHA256: f.verified.Command().PayloadSHA256, DeliveryReceiptSHA256: candidateHash([]byte(f.row.receipt)), NativeID: actual.Registration.NativeID, ChallengeSHA256: actual.ChallengeSHA256, Answer: actual.Answer, ObservedMS: strconv.FormatInt(actual.ObservedMS, 10)}
	raw, _ := json.Marshal(result)
	return ParseWritableBackendHandshakeEvidence(raw)
}
